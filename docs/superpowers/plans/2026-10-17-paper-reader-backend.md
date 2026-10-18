# Paper Reader Backend Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the Go backend of Paper Reader — PDF upload and page extraction, per-page Korean translation/explanation via `claude -p` with SSE streaming and DB caching, background prefetch, and page-anchored Q&A — fully usable through its HTTP API.

**Architecture:** Layered Go service (`handler → service → store | pdf | llm | jobs`). `pdf` shells out to MuPDF's `mutool`; `llm` shells out to `claude -p` with stream-json I/O; `jobs` is an in-memory two-priority worker queue plus an event hub that feeds SSE; `store` is Postgres via pgx with goose migrations. Services define the interfaces they consume; `cmd/server/main.go` wires everything by hand.

**Tech Stack:** Go 1.22+ (`net/http` routing patterns), `github.com/jackc/pgx/v5`, `github.com/pressly/goose/v3`, `github.com/google/uuid`, MuPDF `mutool` ≥ 1.23 (verified with 1.28.5), Claude Code CLI (verified with 2.1.292), Postgres.

**Spec:** `docs/superpowers/specs/2026-10-16-paper-reader-design.md`

## Global Constraints

- Go module path: `github.com/choizeus02/paper-reader/backend`, rooted at `backend/`.
- No cgo. External tools are invoked as subprocesses only (`mutool`, `claude`).
- Config comes only from environment variables; no `.env` loader. Defaults: `DATA_DIR=./data`, `LISTEN_ADDR=:8080`, `CLAUDE_BIN=claude`, `MUTOOL_BIN=mutool`, `WORKERS=2`, `JOB_TIMEOUT=5m`, `RENDER_DPI=150`; `DATABASE_URL` required; `CLAUDE_MODEL` optional.
- Dev runs through Infisical: `infisical run --env=dev -- ...`. Secrets never go in the repo.
- IDs for papers and threads are UUID strings generated in Go, stored as `text`.
- Page numbers are 1-based everywhere. PNG files are named `%03d.png` (`001.png`).
- Block bbox is `{x,y,w,h}` in PDF points, top-left origin. Page size (pt) = PNG pixels × 72 / `RENDER_DPI`.
- `claude` is always run with: `-p --input-format stream-json --output-format stream-json --verbose --include-partial-messages --tools "" --no-session-persistence --strict-mcp-config --setting-sources "" --system-prompt <s>` (+ `--model` if set), in an empty temp working directory.
- LLM CLI failures are never auto-retried. Marker-format parse failures are retried exactly once.
- `prompt_version` for page generation is `1`.
- User-facing error messages are Korean.
- JSON arrays in API responses are never `null` (use `[]`).

## Review Focus

1. **Text-less pages (scans, full-page figures)** — a page with zero extracted blocks must still store `blocks: []` (not null) and still generate an explanation. Pinned in Task 2 (`TestProcessSamplePDF` page 2), Task 5 (`TestPaperLifecycle` page 2), Task 9 (`TestRunJobPageWithoutBlocks`).
2. **Opening a page that is already queued by prefetch** — must promote the queued job to high priority, never run it twice. Pinned in Task 7 (`TestQueuePromote`, `TestQueueDedup`) and Task 9 (`TestGenerateTwicePromotes`).
3. **Subscribing to a page stream after the job already finished (or between save and close)** — client must receive `done`, not hang. Pinned in Task 9 (`TestStreamFallsBackToDB`) and Task 11 (`TestStreamFinal`).
4. **Claude usage limit / CLI crash vs. malformed output** — CLI error ⇒ `failed` with the CLI's message and no retry; malformed output ⇒ one retry then `failed`. Pinned in Task 3 (`TestParseStreamError`, `TestRunNonZeroExit`) and Task 9 (`TestRunJobLLMErrorNoRetry`, `TestRunJobRetriesOnceOnBadFormat`, `TestRunJobFailsAfterTwoBadFormats`).
5. **Non-PDF or corrupt upload** — wrong extension or missing `%PDF-` header ⇒ 400 with nothing left on disk; a PDF that `mutool` cannot open ⇒ paper `failed` with the error text. Pinned in Task 2 (`TestProcessCorruptPDF`), Task 8 (`TestUploadRejectsNonPDF`, `TestUploadMarksFailedOnExtractError`), Task 11 (`TestUploadBadRequest`).

---

## File Map

```
Makefile                                   dev/test targets (Task 1)
backend/go.mod, go.sum                     (Task 1)
backend/cmd/server/main.go                 wiring + graceful shutdown (Task 12)
backend/internal/config/config.go          env → Config (Task 1)
backend/internal/domain/domain.go          shared types + sentinel errors (Task 1)
backend/internal/pdf/stext.go              stext.json → []Block (Task 2)
backend/internal/pdf/mutool.go             Extractor.Process (Task 2)
backend/internal/pdf/testdata/             sample.pdf + its sources (Task 2)
backend/internal/llm/stream.go             stream-json parser (Task 3)
backend/internal/llm/claude.go             Claude.Run subprocess (Task 3)
backend/internal/llm/testdata/*.jsonl      recorded CLI output (Task 3)
backend/internal/llm/markers.go            @@-marker incremental parser (Task 4)
backend/internal/llm/prompt.go             prompt builders + system prompts (Task 4)
backend/internal/llm/prompts/*.tmpl        templates (Task 4)
backend/internal/store/db.go               Open/Migrate/Close (Task 5)
backend/internal/store/migrations/00001_init.sql (Task 5)
backend/internal/store/papers.go           papers + pages (Task 5)
backend/internal/store/results.go          page_results (Task 6)
backend/internal/store/glossary.go         glossary (Task 6)
backend/internal/store/threads.go          threads + messages (Task 6)
backend/internal/jobs/queue.go             priority worker queue (Task 7)
backend/internal/jobs/hub.go               per-job event buffer + subscribers (Task 7)
backend/internal/service/ports.go          interfaces services consume (Task 8)
backend/internal/service/fakes_test.go     in-memory fakes (Task 8)
backend/internal/service/paper.go          PaperService (Task 8)
backend/internal/service/page.go           PageService (Task 9)
backend/internal/service/ask.go            AskService (Task 10)
backend/internal/handler/router.go         routes + middleware (Task 11)
backend/internal/handler/respond.go        JSON/error/SSE helpers (Task 11)
backend/internal/handler/dto.go            request/response shapes (Task 11)
backend/internal/handler/papers.go, pages.go, ask.go  (Task 11)
```

---

### Task 1: Scaffold, config, domain types

**Files:**
- Create: `Makefile`
- Create: `backend/go.mod`
- Create: `backend/internal/config/config.go`
- Create: `backend/internal/config/config_test.go`
- Create: `backend/internal/domain/domain.go`

**Interfaces:**
- Consumes: nothing
- Produces:
  - `config.Load() (config.Config, error)`; `Config{DatabaseURL, DataDir, ListenAddr, ClaudeBin, ClaudeModel, MutoolBin string; Workers, RenderDPI int; JobTimeout time.Duration}`
  - `domain.ErrNotFound`, `domain.ErrInvalid`
  - `domain.Paper{ID, Title, Filename string; PageCount int; Status PaperStatus; Error string; DonePages int; CreatedAt time.Time}`, `PaperStatus` consts `PaperProcessing|PaperReady|PaperFailed`
  - `domain.BBox{X,Y,W,H float64}`, `domain.Block{ID string; BBox BBox; Text string}`
  - `domain.Page{PaperID string; PageNo int; Width, Height float64; ImagePath string; Blocks []Block}`
  - `domain.ResultStatus` consts `ResultPending|ResultRunning|ResultDone|ResultFailed`
  - `domain.BlockTranslation{BlockID, Ko string}`
  - `domain.PageResult{PaperID string; PageNo int; Status ResultStatus; Translation []BlockTranslation; Explanation, PageSummary string; PromptVersion int; Error string; UpdatedAt time.Time}`
  - `domain.GlossaryTerm{En, Ko, Note string}`
  - `domain.Anchor{BlockIDs []string; Quote string}`
  - `domain.Thread{ID, PaperID string; PageNo int; Anchor Anchor; CreatedAt time.Time; Messages []Message}`
  - `domain.Message{ID int64; ThreadID string; Role MessageRole; Content string; Status MessageStatus; CreatedAt time.Time}`, `MessageRole` consts `RoleUser|RoleAssistant`, `MessageStatus` consts `MessageDone|MessageStreaming|MessageFailed`

- [ ] **Step 1: Initialize the module**

```bash
mkdir -p backend && cd backend && go mod init github.com/choizeus02/paper-reader/backend
```

- [ ] **Step 2: Write the failing config test**

`backend/internal/config/config_test.go`:

```go
package config

import (
	"strings"
	"testing"
	"time"
)

func clearEnv(t *testing.T) {
	for _, k := range []string{"DATABASE_URL", "DATA_DIR", "LISTEN_ADDR", "CLAUDE_BIN", "CLAUDE_MODEL", "MUTOOL_BIN", "WORKERS", "JOB_TIMEOUT", "RENDER_DPI"} {
		t.Setenv(k, "")
	}
}

func TestLoadDefaults(t *testing.T) {
	clearEnv(t)
	t.Setenv("DATABASE_URL", "postgres://x")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		DatabaseURL: "postgres://x", DataDir: "./data", ListenAddr: ":8080",
		ClaudeBin: "claude", MutoolBin: "mutool",
		Workers: 2, JobTimeout: 5 * time.Minute, RenderDPI: 150,
	}
	if c != want {
		t.Fatalf("got %+v\nwant %+v", c, want)
	}
}

func TestLoadOverrides(t *testing.T) {
	clearEnv(t)
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("WORKERS", "4")
	t.Setenv("JOB_TIMEOUT", "90s")
	t.Setenv("CLAUDE_MODEL", "sonnet")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Workers != 4 || c.JobTimeout != 90*time.Second || c.ClaudeModel != "sonnet" {
		t.Fatalf("overrides not applied: %+v", c)
	}
}

func TestLoadRequiresDatabaseURL(t *testing.T) {
	clearEnv(t)
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("want DATABASE_URL error, got %v", err)
	}
}

func TestLoadRejectsBadNumbers(t *testing.T) {
	clearEnv(t)
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("WORKERS", "0")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "WORKERS") {
		t.Fatalf("want WORKERS error, got %v", err)
	}
	t.Setenv("WORKERS", "2")
	t.Setenv("JOB_TIMEOUT", "soon")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "JOB_TIMEOUT") {
		t.Fatalf("want JOB_TIMEOUT error, got %v", err)
	}
}
```

- [ ] **Step 3: Run it to verify it fails**

Run: `cd backend && go test ./internal/config/`
Expected: FAIL — `undefined: Config` / `undefined: Load`

- [ ] **Step 4: Implement config**

`backend/internal/config/config.go`:

```go
// Package config reads application settings from environment variables.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	DatabaseURL string
	DataDir     string
	ListenAddr  string
	ClaudeBin   string
	ClaudeModel string
	MutoolBin   string
	Workers     int
	JobTimeout  time.Duration
	RenderDPI   int
}

func Load() (Config, error) {
	c := Config{
		DatabaseURL: os.Getenv("DATABASE_URL"),
		DataDir:     envOr("DATA_DIR", "./data"),
		ListenAddr:  envOr("LISTEN_ADDR", ":8080"),
		ClaudeBin:   envOr("CLAUDE_BIN", "claude"),
		ClaudeModel: os.Getenv("CLAUDE_MODEL"),
		MutoolBin:   envOr("MUTOOL_BIN", "mutool"),
	}
	if c.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}
	var err error
	if c.Workers, err = positiveInt("WORKERS", 2); err != nil {
		return Config{}, err
	}
	if c.RenderDPI, err = positiveInt("RENDER_DPI", 150); err != nil {
		return Config{}, err
	}
	if c.JobTimeout, err = time.ParseDuration(envOr("JOB_TIMEOUT", "5m")); err != nil {
		return Config{}, fmt.Errorf("JOB_TIMEOUT: %w", err)
	}
	return c, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func positiveInt(key string, def int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer, got %q", key, v)
	}
	return n, nil
}
```

- [ ] **Step 5: Write domain types**

`backend/internal/domain/domain.go`:

```go
// Package domain holds the core types shared across layers.
package domain

import (
	"errors"
	"time"
)

var (
	ErrNotFound = errors.New("not found")
	ErrInvalid  = errors.New("invalid input")
)

type PaperStatus string

const (
	PaperProcessing PaperStatus = "processing"
	PaperReady      PaperStatus = "ready"
	PaperFailed     PaperStatus = "failed"
)

type Paper struct {
	ID        string      `json:"id"`
	Title     string      `json:"title"`
	Filename  string      `json:"filename"`
	PageCount int         `json:"page_count"`
	Status    PaperStatus `json:"status"`
	Error     string      `json:"error,omitempty"`
	DonePages int         `json:"done_pages"`
	CreatedAt time.Time   `json:"created_at"`
}

// BBox is in PDF points with a top-left origin.
type BBox struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

type Block struct {
	ID   string `json:"id"`
	BBox BBox   `json:"bbox"`
	Text string `json:"text"`
}

type Page struct {
	PaperID   string  `json:"paper_id"`
	PageNo    int     `json:"page_no"`
	Width     float64 `json:"width"`
	Height    float64 `json:"height"`
	ImagePath string  `json:"-"` // relative to DATA_DIR, slash-separated
	Blocks    []Block `json:"blocks"`
}

type ResultStatus string

const (
	ResultPending ResultStatus = "pending"
	ResultRunning ResultStatus = "running"
	ResultDone    ResultStatus = "done"
	ResultFailed  ResultStatus = "failed"
)

type BlockTranslation struct {
	BlockID string `json:"block_id"`
	Ko      string `json:"ko"`
}

type PageResult struct {
	PaperID       string             `json:"-"`
	PageNo        int                `json:"-"`
	Status        ResultStatus       `json:"status"`
	Translation   []BlockTranslation `json:"translation"`
	Explanation   string             `json:"explanation"`
	PageSummary   string             `json:"page_summary"`
	PromptVersion int                `json:"prompt_version"`
	Error         string             `json:"error,omitempty"`
	UpdatedAt     time.Time          `json:"updated_at"`
}

type GlossaryTerm struct {
	En   string `json:"en"`
	Ko   string `json:"ko"`
	Note string `json:"note,omitempty"`
}

type Anchor struct {
	BlockIDs []string `json:"block_ids"`
	Quote    string   `json:"quote"`
}

type Thread struct {
	ID        string    `json:"id"`
	PaperID   string    `json:"paper_id"`
	PageNo    int       `json:"page_no"`
	Anchor    Anchor    `json:"anchor"`
	CreatedAt time.Time `json:"created_at"`
	Messages  []Message `json:"messages"`
}

type MessageRole string

const (
	RoleUser      MessageRole = "user"
	RoleAssistant MessageRole = "assistant"
)

type MessageStatus string

const (
	MessageDone      MessageStatus = "done"
	MessageStreaming MessageStatus = "streaming"
	MessageFailed    MessageStatus = "failed"
)

type Message struct {
	ID        int64         `json:"id"`
	ThreadID  string        `json:"-"`
	Role      MessageRole   `json:"role"`
	Content   string        `json:"content"`
	Status    MessageStatus `json:"status"`
	CreatedAt time.Time     `json:"created_at"`
}
```

- [ ] **Step 6: Write the Makefile** (repo root; recipe lines are tab-indented)

```make
DATA_DIR ?= $(CURDIR)/data
TEST_DATABASE_URL ?= postgres://postgres:pg@localhost:55432/postgres?sslmode=disable

.PHONY: dev test test-unit test-db-up test-db-down

dev:
	infisical run --env=dev -- env DATA_DIR=$(DATA_DIR) go -C backend run ./cmd/server

test-unit:
	cd backend && go test ./...

test:
	cd backend && TEST_DATABASE_URL='$(TEST_DATABASE_URL)' go test ./...

test-db-up:
	docker run -d --name paper-reader-testdb -e POSTGRES_PASSWORD=pg -p 55432:5432 postgres:17

test-db-down:
	docker rm -f paper-reader-testdb
```

- [ ] **Step 7: Run tests and vet**

Run: `cd backend && go test ./internal/config/ && go vet ./...`
Expected: `ok .../internal/config`, vet prints nothing.

- [ ] **Step 8: Commit**

```bash
git add Makefile backend/go.mod backend/internal/config backend/internal/domain
git commit -m "feat(backend): scaffold module with config and domain types"
```

---

### Task 2: PDF extraction with mutool

**Files:**
- Create: `backend/internal/pdf/stext.go`
- Create: `backend/internal/pdf/mutool.go`
- Create: `backend/internal/pdf/pdf_test.go`
- Create: `backend/internal/pdf/testdata/sample-p1.txt`, `sample-p2.txt`, `sample.pdf`

**Interfaces:**
- Consumes: `domain.Page`, `domain.Block`, `domain.BBox`
- Produces: `pdf.Extractor{Bin string; DPI int}` with `func (e Extractor) Process(ctx context.Context, pdfPath, outDir string) ([]domain.Page, error)` — renders `outDir/001.png…`, returns pages with `PageNo`, `Width`, `Height`, `Blocks` (never nil) and `ImagePath` = PNG file name only (e.g. `"001.png"`). `PaperID` is left empty.

- [ ] **Step 1: Create the fixture PDF**

`backend/internal/pdf/testdata/sample-p1.txt`:

```
%%MediaBox 0 0 300 200
%%Font F1 Times-Roman
BT /F1 12 Tf 20 160 Td (Hello paper reader.) Tj ET
BT /F1 12 Tf 20 60 Td (Second block here.) Tj ET
```

`backend/internal/pdf/testdata/sample-p2.txt`:

```
%%MediaBox 0 0 300 200
```

Run: `cd backend/internal/pdf/testdata && mutool create -o sample.pdf sample-p1.txt sample-p2.txt`
Expected: `sample.pdf` created (page 1 has two text blocks, page 2 is empty).

- [ ] **Step 2: Write the failing tests**

`backend/internal/pdf/pdf_test.go`:

```go
package pdf

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/choizeus02/paper-reader/backend/internal/domain"
)

func TestParseSText(t *testing.T) {
	in := `{"file":"x.pdf","pages":[
	  {"blocks":[
	    {"type":"text","bbox":{"x":10,"y":20,"w":100,"h":30},"lines":[
	      {"text":"Attention is all you need and posi-"},
	      {"text":"tion-wise layers"},
	      {"text":"   "}
	    ]},
	    {"type":"image","bbox":{"x":0,"y":0,"w":5,"h":5}},
	    {"type":"text","bbox":{"x":1,"y":2,"w":3,"h":4},"lines":[{"text":"  "}]},
	    {"type":"text","bbox":{"x":5,"y":60,"w":50,"h":8},"lines":[{"text":"Second"}]}
	  ]},
	  {"blocks":[]}
	]}`
	got, err := parseSText(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	want := [][]domain.Block{
		{
			{ID: "b0", BBox: domain.BBox{X: 10, Y: 20, W: 100, H: 30}, Text: "Attention is all you need and posi-tion-wise layers"},
			{ID: "b1", BBox: domain.BBox{X: 5, Y: 60, W: 50, H: 8}, Text: "Second"},
		},
		{},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v\nwant %#v", got, want)
	}
	if got[1] == nil {
		t.Fatal("empty page must be an empty slice, not nil")
	}
}

func TestParseSTextBadJSON(t *testing.T) {
	if _, err := parseSText(strings.NewReader("{")); err == nil {
		t.Fatal("want error")
	}
}

func requireMutool(t *testing.T) string {
	bin, err := exec.LookPath("mutool")
	if err != nil {
		t.Skip("mutool not installed")
	}
	return bin
}

func TestProcessSamplePDF(t *testing.T) {
	bin := requireMutool(t)
	out := t.TempDir()
	pages, err := Extractor{Bin: bin, DPI: 72}.Process(context.Background(), "testdata/sample.pdf", out)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 {
		t.Fatalf("want 2 pages, got %d", len(pages))
	}
	p1 := pages[0]
	if p1.PageNo != 1 || p1.Width != 300 || p1.Height != 200 || p1.ImagePath != "001.png" {
		t.Fatalf("page 1 meta wrong: %+v", p1)
	}
	if len(p1.Blocks) != 2 || p1.Blocks[0].Text != "Hello paper reader." || p1.Blocks[1].Text != "Second block here." {
		t.Fatalf("page 1 blocks wrong: %+v", p1.Blocks)
	}
	if p1.Blocks[0].BBox.Y >= p1.Blocks[1].BBox.Y {
		t.Fatalf("bbox must use top-left origin: %+v", p1.Blocks)
	}
	if pages[1].Blocks == nil || len(pages[1].Blocks) != 0 || pages[1].ImagePath != "002.png" {
		t.Fatalf("page 2 should have empty blocks: %+v", pages[1])
	}
	for _, name := range []string{"001.png", "002.png"} {
		if _, err := os.Stat(filepath.Join(out, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(out, "stext.json")); !os.IsNotExist(err) {
		t.Fatal("stext.json should be removed after processing")
	}
}

func TestProcessCorruptPDF(t *testing.T) {
	bin := requireMutool(t)
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.pdf")
	if err := os.WriteFile(bad, []byte("%PDF-1.4 broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Extractor{Bin: bin, DPI: 72}.Process(context.Background(), bad, filepath.Join(dir, "out"))
	if err == nil || !strings.Contains(err.Error(), "mutool") {
		t.Fatalf("want mutool error, got %v", err)
	}
}
```

- [ ] **Step 3: Run to verify failure**

Run: `cd backend && go test ./internal/pdf/`
Expected: FAIL — `undefined: parseSText`, `undefined: Extractor`

- [ ] **Step 4: Implement the stext parser**

`backend/internal/pdf/stext.go`:

```go
package pdf

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/choizeus02/paper-reader/backend/internal/domain"
)

type stextDoc struct {
	Pages []struct {
		Blocks []struct {
			Type  string      `json:"type"`
			BBox  domain.BBox `json:"bbox"`
			Lines []struct {
				Text string `json:"text"`
			} `json:"lines"`
		} `json:"blocks"`
	} `json:"pages"`
}

// parseSText turns `mutool draw -F stext.json` output into text blocks per page.
// Non-text blocks and blocks with only whitespace are dropped; block IDs are
// renumbered b0, b1, ... per page.
func parseSText(r io.Reader) ([][]domain.Block, error) {
	var doc stextDoc
	if err := json.NewDecoder(r).Decode(&doc); err != nil {
		return nil, fmt.Errorf("parse stext.json: %w", err)
	}
	out := make([][]domain.Block, len(doc.Pages))
	for i, p := range doc.Pages {
		blocks := []domain.Block{}
		for _, b := range p.Blocks {
			if b.Type != "text" {
				continue
			}
			lines := make([]string, len(b.Lines))
			for j, l := range b.Lines {
				lines[j] = l.Text
			}
			text := joinLines(lines)
			if text == "" {
				continue
			}
			blocks = append(blocks, domain.Block{ID: fmt.Sprintf("b%d", len(blocks)), BBox: b.BBox, Text: text})
		}
		out[i] = blocks
	}
	return out, nil
}

// joinLines joins lines with a space, except after a trailing hyphen.
func joinLines(lines []string) string {
	var sb strings.Builder
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if sb.Len() > 0 && !strings.HasSuffix(sb.String(), "-") {
			sb.WriteByte(' ')
		}
		sb.WriteString(l)
	}
	return sb.String()
}
```

- [ ] **Step 5: Implement the extractor**

`backend/internal/pdf/mutool.go`:

```go
// Package pdf renders PDF pages and extracts text blocks using MuPDF's mutool.
package pdf

import (
	"bytes"
	"context"
	"fmt"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/choizeus02/paper-reader/backend/internal/domain"
)

type Extractor struct {
	Bin string // mutool binary
	DPI int    // render resolution
}

// Process renders every page of pdfPath into outDir as 001.png, 002.png, ...
// and extracts text blocks. ImagePath on each returned page is the PNG file
// name relative to outDir.
func (e Extractor) Process(ctx context.Context, pdfPath, outDir string) ([]domain.Page, error) {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, err
	}
	if err := e.run(ctx, "draw", "-q", "-r", strconv.Itoa(e.DPI), "-o", filepath.Join(outDir, "%03d.png"), pdfPath); err != nil {
		return nil, fmt.Errorf("render pages: %w", err)
	}
	jsonPath := filepath.Join(outDir, "stext.json")
	defer os.Remove(jsonPath)
	if err := e.run(ctx, "draw", "-q", "-F", "stext.json", "-o", jsonPath, pdfPath); err != nil {
		return nil, fmt.Errorf("extract text: %w", err)
	}
	f, err := os.Open(jsonPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	perPage, err := parseSText(f)
	if err != nil {
		return nil, err
	}

	pages := make([]domain.Page, len(perPage))
	for i, blocks := range perPage {
		name := fmt.Sprintf("%03d.png", i+1)
		w, h, err := pngSize(filepath.Join(outDir, name))
		if err != nil {
			return nil, fmt.Errorf("page %d image: %w", i+1, err)
		}
		pages[i] = domain.Page{
			PageNo:    i + 1,
			Width:     float64(w) * 72 / float64(e.DPI),
			Height:    float64(h) * 72 / float64(e.DPI),
			ImagePath: name,
			Blocks:    blocks,
		}
	}
	return pages, nil
}

func (e Extractor) run(ctx context.Context, args ...string) error {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, e.Bin, args...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 500 {
			msg = msg[len(msg)-500:]
		}
		return fmt.Errorf("mutool %s: %w: %s", args[0], err, msg)
	}
	return nil
}

func pngSize(path string) (int, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	cfg, err := png.DecodeConfig(f)
	if err != nil {
		return 0, 0, err
	}
	return cfg.Width, cfg.Height, nil
}
```

- [ ] **Step 6: Run tests**

Run: `cd backend && go test ./internal/pdf/ -v`
Expected: all 4 tests PASS (the two `Process` tests SKIP only if `mutool` is missing — install with `brew install mupdf-tools`).

- [ ] **Step 7: Commit**

```bash
git add backend/internal/pdf
git commit -m "feat(pdf): render pages and extract text blocks via mutool"
```

---

### Task 3: Claude CLI runner

**Files:**
- Create: `backend/internal/llm/stream.go`
- Create: `backend/internal/llm/claude.go`
- Create: `backend/internal/llm/claude_test.go`
- Create: `backend/internal/llm/real_test.go`
- Create: `backend/internal/llm/testdata/stream_ok.jsonl`, `stream_error.jsonl`

**Interfaces:**
- Consumes: nothing
- Produces:
  - `llm.Request{System string; Image []byte; Prompt string}` (Image is PNG bytes, nil for text-only)
  - `llm.Claude{Bin, Model, WorkDir string}` with `func (c Claude) Run(ctx context.Context, req Request, onDelta func(string)) (string, error)` — calls `onDelta` for every text delta (synchronously, on the caller's goroutine) and returns the full text.

- [ ] **Step 1: Add recorded fixtures** (trimmed from a real `claude -p` run on 2026-10-17)

`backend/internal/llm/testdata/stream_ok.jsonl`:

```
{"type":"system","subtype":"init","session_id":"s1"}
{"type":"stream_event","event":{"type":"message_start"},"session_id":"s1"}
{"type":"stream_event","event":{"type":"content_block_start","index":0},"session_id":"s1"}
{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"paper-"}},"session_id":"s1"}
{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"reader"}},"session_id":"s1"}
{"type":"assistant","message":{"content":[{"type":"text","text":"paper-reader"}]}}
{"type":"rate_limit_event","rate_limit_info":{"status":"allowed"}}
{"type":"result","subtype":"success","is_error":false,"result":"paper-reader","duration_ms":2174}
```

`backend/internal/llm/testdata/stream_error.jsonl`:

```
{"type":"system","subtype":"init","session_id":"s1"}
{"type":"result","subtype":"success","is_error":true,"result":"Claude AI usage limit reached","duration_ms":10}
```

- [ ] **Step 2: Write the failing tests**

`backend/internal/llm/claude_test.go`:

```go
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseStreamOK(t *testing.T) {
	f, err := os.Open("testdata/stream_ok.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var deltas []string
	text, err := parseStream(f, func(d string) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatal(err)
	}
	if text != "paper-reader" || strings.Join(deltas, "|") != "paper-|reader" {
		t.Fatalf("text=%q deltas=%q", text, deltas)
	}
}

func TestParseStreamError(t *testing.T) {
	f, err := os.Open("testdata/stream_error.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, err = parseStream(f, nil)
	if err == nil || !strings.Contains(err.Error(), "usage limit") {
		t.Fatalf("want usage limit error, got %v", err)
	}
}

func TestParseStreamNoResult(t *testing.T) {
	_, err := parseStream(strings.NewReader(`{"type":"system"}`+"\nnot json\n"), nil)
	if !errors.Is(err, errNoResult) {
		t.Fatalf("want errNoResult, got %v", err)
	}
}

// writeFakeClaude creates an executable that records stdin/args, then runs body.
func writeFakeClaude(t *testing.T, body string) (bin, dir string) {
	t.Helper()
	dir = t.TempDir()
	bin = filepath.Join(dir, "claude")
	script := "#!/bin/sh\n" +
		"cat > '" + filepath.Join(dir, "stdin.txt") + "'\n" +
		"printf '%s\\n' \"$@\" > '" + filepath.Join(dir, "args.txt") + "'\n" +
		body + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, dir
}

func TestRunSendsImageAndFlags(t *testing.T) {
	fixture, _ := filepath.Abs("testdata/stream_ok.jsonl")
	bin, dir := writeFakeClaude(t, "cat '"+fixture+"'")
	c := Claude{Bin: bin, Model: "sonnet", WorkDir: t.TempDir()}
	var got strings.Builder
	text, err := c.Run(context.Background(), Request{System: "SYS", Image: []byte{0x89, 'P', 'N', 'G'}, Prompt: "translate"}, func(d string) { got.WriteString(d) })
	if err != nil {
		t.Fatal(err)
	}
	if text != "paper-reader" || got.String() != "paper-reader" {
		t.Fatalf("text=%q streamed=%q", text, got.String())
	}

	args, _ := os.ReadFile(filepath.Join(dir, "args.txt"))
	for _, want := range []string{"-p\n", "--input-format\nstream-json\n", "--output-format\nstream-json\n", "--include-partial-messages\n", "--tools\n\n", "--no-session-persistence\n", "--setting-sources\n\n", "--system-prompt\nSYS\n", "--model\nsonnet\n"} {
		if !strings.Contains(string(args), want) {
			t.Errorf("args missing %q:\n%s", want, args)
		}
	}

	stdin, _ := os.ReadFile(filepath.Join(dir, "stdin.txt"))
	var msg struct {
		Type    string `json:"type"`
		Message struct {
			Role    string `json:"role"`
			Content []struct {
				Type   string `json:"type"`
				Text   string `json:"text"`
				Source struct {
					Type      string `json:"type"`
					MediaType string `json:"media_type"`
					Data      string `json:"data"`
				} `json:"source"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(stdin, &msg); err != nil {
		t.Fatalf("stdin not JSON: %v\n%s", err, stdin)
	}
	c0, c1 := msg.Message.Content[0], msg.Message.Content[1]
	if msg.Type != "user" || c0.Type != "image" || c0.Source.MediaType != "image/png" || c0.Source.Data != "iVBORw==" || c1.Type != "text" || c1.Text != "translate" {
		t.Fatalf("unexpected input message: %+v", msg)
	}
}

func TestRunTextOnlyOmitsImageAndModel(t *testing.T) {
	fixture, _ := filepath.Abs("testdata/stream_ok.jsonl")
	bin, dir := writeFakeClaude(t, "cat '"+fixture+"'")
	if _, err := (Claude{Bin: bin, WorkDir: t.TempDir()}).Run(context.Background(), Request{System: "S", Prompt: "q"}, nil); err != nil {
		t.Fatal(err)
	}
	stdin, _ := os.ReadFile(filepath.Join(dir, "stdin.txt"))
	if strings.Contains(string(stdin), `"image"`) {
		t.Fatalf("text-only request should not include an image: %s", stdin)
	}
	args, _ := os.ReadFile(filepath.Join(dir, "args.txt"))
	if strings.Contains(string(args), "--model") {
		t.Fatalf("--model must be omitted when Model is empty:\n%s", args)
	}
}

func TestRunNonZeroExit(t *testing.T) {
	bin, _ := writeFakeClaude(t, "echo 'not logged in' >&2; exit 3")
	_, err := Claude{Bin: bin, WorkDir: t.TempDir()}.Run(context.Background(), Request{Prompt: "x"}, nil)
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("want stderr in error, got %v", err)
	}
}

func TestRunResultErrorWinsOverExitCode(t *testing.T) {
	fixture, _ := filepath.Abs("testdata/stream_error.jsonl")
	bin, _ := writeFakeClaude(t, "cat '"+fixture+"'; exit 1")
	_, err := Claude{Bin: bin, WorkDir: t.TempDir()}.Run(context.Background(), Request{Prompt: "x"}, nil)
	if err == nil || !strings.Contains(err.Error(), "usage limit") {
		t.Fatalf("want usage limit error, got %v", err)
	}
}

func TestRunHonoursContextAndKillsChildren(t *testing.T) {
	// "sleep" is a child of sh (no exec), so killing only sh would leave stdout open.
	bin, _ := writeFakeClaude(t, "sleep 5")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := Claude{Bin: bin, WorkDir: t.TempDir()}.Run(ctx, Request{Prompt: "x"}, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline exceeded, got %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("Run did not stop promptly: %v", time.Since(start))
	}
}
```

- [ ] **Step 3: Run to verify failure**

Run: `cd backend && go test ./internal/llm/`
Expected: FAIL — `undefined: parseStream`, `undefined: Claude`

- [ ] **Step 4: Implement the stream parser**

`backend/internal/llm/stream.go`:

```go
package llm

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

var errNoResult = errors.New("claude: output ended without a result")

type streamLine struct {
	Type  string `json:"type"`
	Event *struct {
		Type  string `json:"type"`
		Delta *struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	} `json:"event"`
	Subtype string `json:"subtype"`
	IsError bool   `json:"is_error"`
	Result  string `json:"result"`
}

// parseStream reads `claude -p --output-format stream-json --include-partial-messages`
// output, forwarding text deltas to onDelta, and returns the final text.
func parseStream(r io.Reader, onDelta func(string)) (string, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var sb strings.Builder
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var l streamLine
		if err := json.Unmarshal(line, &l); err != nil {
			continue // not a JSON event line
		}
		switch l.Type {
		case "stream_event":
			if l.Event != nil && l.Event.Type == "content_block_delta" && l.Event.Delta != nil && l.Event.Delta.Type == "text_delta" {
				sb.WriteString(l.Event.Delta.Text)
				if onDelta != nil {
					onDelta(l.Event.Delta.Text)
				}
			}
		case "result":
			if l.IsError {
				msg := l.Result
				if msg == "" {
					msg = l.Subtype
				}
				return "", fmt.Errorf("claude: %s", msg)
			}
			if l.Result != "" {
				return l.Result, nil
			}
			return sb.String(), nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("read claude output: %w", err)
	}
	return "", errNoResult
}
```

- [ ] **Step 5: Implement the runner**

`backend/internal/llm/claude.go`:

```go
// Package llm runs the Claude Code CLI and builds/parses prompts.
package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

type Request struct {
	System string
	Image  []byte // PNG bytes; nil for text-only
	Prompt string
}

// Claude runs `claude -p` as a subprocess using the logged-in subscription.
type Claude struct {
	Bin     string
	Model   string
	WorkDir string // empty directory so no project CLAUDE.md/settings are picked up
}

func (c Claude) Run(ctx context.Context, req Request, onDelta func(string)) (string, error) {
	input, err := encodeInput(req)
	if err != nil {
		return "", err
	}
	args := []string{"-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose", "--include-partial-messages",
		"--tools", "",
		"--no-session-persistence",
		"--strict-mcp-config",
		"--setting-sources", "",
		"--system-prompt", req.System,
	}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}

	cmd := exec.CommandContext(ctx, c.Bin, args...)
	cmd.Dir = c.WorkDir
	cmd.Stdin = bytes.NewReader(input)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	// Kill the whole process group on cancel so children can't hold stdout open.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start claude: %w", err)
	}
	text, perr := parseStream(stdout, onDelta)
	_, _ = io.Copy(io.Discard, stdout)
	werr := cmd.Wait()

	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if perr != nil && !errors.Is(perr, errNoResult) {
		return "", perr
	}
	if werr != nil {
		return "", fmt.Errorf("claude exited: %w: %s", werr, tail(stderr.String(), 500))
	}
	if perr != nil {
		return "", perr
	}
	return text, nil
}

type imageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type contentPart struct {
	Type   string       `json:"type"`
	Text   string       `json:"text,omitempty"`
	Source *imageSource `json:"source,omitempty"`
}

func encodeInput(req Request) ([]byte, error) {
	var parts []contentPart
	if len(req.Image) > 0 {
		parts = append(parts, contentPart{Type: "image", Source: &imageSource{
			Type: "base64", MediaType: "image/png", Data: base64.StdEncoding.EncodeToString(req.Image),
		}})
	}
	parts = append(parts, contentPart{Type: "text", Text: req.Prompt})
	b, err := json.Marshal(map[string]any{
		"type":    "user",
		"message": map[string]any{"role": "user", "content": parts},
	})
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}
```

- [ ] **Step 6: Run tests**

Run: `cd backend && go test ./internal/llm/ -v`
Expected: all 8 tests PASS.

- [ ] **Step 7: Add an opt-in test against the real CLI**

`backend/internal/llm/real_test.go`:

```go
package llm

import (
	"context"
	"os"
	"strings"
	"testing"
)

// TestRealClaude calls the installed, logged-in claude CLI. It uses a little
// subscription quota, so it only runs with REAL_CLAUDE=1.
func TestRealClaude(t *testing.T) {
	if os.Getenv("REAL_CLAUDE") == "" {
		t.Skip("set REAL_CLAUDE=1 to call the real claude CLI")
	}
	deltas := 0
	out, err := Claude{Bin: "claude", WorkDir: t.TempDir()}.Run(context.Background(),
		Request{System: "Reply with exactly the word OK.", Prompt: "Say it."},
		func(string) { deltas++ })
	if err != nil {
		t.Fatal(err)
	}
	if deltas == 0 || !strings.Contains(out, "OK") {
		t.Fatalf("deltas=%d out=%q", deltas, out)
	}
}
```

Run: `cd backend && REAL_CLAUDE=1 go test ./internal/llm/ -run TestRealClaude -v`
Expected: PASS (and SKIP when run without `REAL_CLAUDE`).

- [ ] **Step 8: Commit**

```bash
git add backend/internal/llm
git commit -m "feat(llm): run claude -p with stream-json image input"
```

---

### Task 4: Output marker parser and prompt templates

**Files:**
- Create: `backend/internal/llm/markers.go`
- Create: `backend/internal/llm/markers_test.go`
- Create: `backend/internal/llm/prompt.go`
- Create: `backend/internal/llm/prompt_test.go`
- Create: `backend/internal/llm/prompts/page.tmpl`
- Create: `backend/internal/llm/prompts/ask.tmpl`

**Interfaces:**
- Consumes: `domain.Block`, `domain.BlockTranslation`, `domain.GlossaryTerm`, `domain.Anchor`, `domain.Message`
- Produces:
  - `llm.ErrIncompleteOutput`
  - `llm.PageOutput{Translation []domain.BlockTranslation; Explanation, Summary string; Glossary []domain.GlossaryTerm}`
  - `llm.NewMarkerParser(onBlock func(domain.BlockTranslation), onExplanation func(string)) *MarkerParser`; `(*MarkerParser).Write(chunk string)`; `(*MarkerParser).Finish() (PageOutput, error)`
  - `llm.PagePromptVersion = 1`, `llm.PageSystem`, `llm.AskSystem` (string consts)
  - `llm.PageContext{Title string; PageNo, PageCount int; Abstract, PrevSummary string; Glossary []domain.GlossaryTerm; Blocks []domain.Block}`, `llm.BuildPagePrompt(PageContext) (string, error)`
  - `llm.AskContext{Title string; PageNo int; Blocks []domain.Block; Translation []domain.BlockTranslation; Anchor domain.Anchor; History []domain.Message; Question string}`, `llm.BuildAskPrompt(AskContext) (string, error)`

- [ ] **Step 1: Write failing marker parser tests**

`backend/internal/llm/markers_test.go`:

```go
package llm

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/choizeus02/paper-reader/backend/internal/domain"
)

const sampleOutput = `Sure, here it is.
@@BLOCK b0
3. 방법
@@BLOCK b1
우리는 트랜스포머를 채택한다.
두 번째 줄.
@@BLOCK b2

@@EXPLANATION
## 이 페이지 요지
요지 내용
@@SUMMARY
방법을 설명한다.
@@GLOSSARY
attention | 어텐션 | 번역하지 않음
transformer | 트랜스포머
broken line without pipes
 | 빈영어
@@END
trailing junk`

func feed(p *MarkerParser, s string, size int) {
	for i := 0; i < len(s); i += size {
		end := i + size
		if end > len(s) {
			end = len(s)
		}
		p.Write(s[i:end])
	}
}

func TestMarkerParserChunkSizes(t *testing.T) {
	for _, size := range []int{1, 3, 7, 64, len(sampleOutput)} {
		var blocks []domain.BlockTranslation
		var expl strings.Builder
		p := NewMarkerParser(func(b domain.BlockTranslation) { blocks = append(blocks, b) }, func(d string) { expl.WriteString(d) })
		feed(p, sampleOutput, size)
		out, err := p.Finish()
		if err != nil {
			t.Fatalf("size %d: %v", size, err)
		}
		wantBlocks := []domain.BlockTranslation{
			{BlockID: "b0", Ko: "3. 방법"},
			{BlockID: "b1", Ko: "우리는 트랜스포머를 채택한다.\n두 번째 줄."},
		}
		if !reflect.DeepEqual(out.Translation, wantBlocks) || !reflect.DeepEqual(blocks, wantBlocks) {
			t.Fatalf("size %d: blocks out=%#v streamed=%#v", size, out.Translation, blocks)
		}
		if out.Explanation != "## 이 페이지 요지\n요지 내용" {
			t.Fatalf("size %d: explanation %q", size, out.Explanation)
		}
		if expl.String() != "## 이 페이지 요지\n요지 내용\n" {
			t.Fatalf("size %d: streamed explanation %q", size, expl.String())
		}
		if out.Summary != "방법을 설명한다." {
			t.Fatalf("size %d: summary %q", size, out.Summary)
		}
		wantGloss := []domain.GlossaryTerm{{En: "attention", Ko: "어텐션", Note: "번역하지 않음"}, {En: "transformer", Ko: "트랜스포머"}}
		if !reflect.DeepEqual(out.Glossary, wantGloss) {
			t.Fatalf("size %d: glossary %#v", size, out.Glossary)
		}
	}
}

func TestMarkerParserMissingEnd(t *testing.T) {
	p := NewMarkerParser(nil, nil)
	p.Write("@@BLOCK b0\n번역\n@@EXPLANATION\n설명")
	if _, err := p.Finish(); !errors.Is(err, ErrIncompleteOutput) {
		t.Fatalf("want ErrIncompleteOutput, got %v", err)
	}
}

func TestMarkerParserNoBlocksStillValid(t *testing.T) {
	p := NewMarkerParser(nil, nil)
	p.Write("@@EXPLANATION\n그림만 있는 페이지\n@@SUMMARY\n그림\n@@GLOSSARY\n@@END")
	out, err := p.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if out.Translation == nil || len(out.Translation) != 0 || out.Explanation != "그림만 있는 페이지" {
		t.Fatalf("unexpected: %#v", out)
	}
}

func TestMarkerParserCRLF(t *testing.T) {
	p := NewMarkerParser(nil, nil)
	p.Write("@@BLOCK b0\r\n안녕\r\n@@EXPLANATION\r\n설명\r\n@@END\r\n")
	out, err := p.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if out.Translation[0].Ko != "안녕" || out.Explanation != "설명" {
		t.Fatalf("unexpected: %#v", out)
	}
}
```

- [ ] **Step 2: Write failing prompt tests**

`backend/internal/llm/prompt_test.go`:

```go
package llm

import (
	"strings"
	"testing"

	"github.com/choizeus02/paper-reader/backend/internal/domain"
)

func TestBuildPagePromptFull(t *testing.T) {
	got, err := BuildPagePrompt(PageContext{
		Title: "Attention Is All You Need", PageNo: 3, PageCount: 15,
		Abstract:    "The dominant sequence transduction models...",
		PrevSummary: "이전 페이지는 배경을 설명한다.",
		Glossary:    []domain.GlossaryTerm{{En: "attention", Ko: "어텐션", Note: "음차"}, {En: "encoder", Ko: "인코더"}},
		Blocks:      []domain.Block{{ID: "b0", Text: "3 Model Architecture"}, {ID: "b1", Text: "Most competitive models..."}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Paper: Attention Is All You Need",
		"Page 3 of 15",
		"The dominant sequence transduction models...",
		"이전 페이지는 배경을 설명한다.",
		"- attention → 어텐션 (음차)",
		"- encoder → 인코더\n",
		"[b0] 3 Model Architecture",
		"[b1] Most competitive models...",
		"@@BLOCK <block id>",
		"@@END",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing %q\n---\n%s", want, got)
		}
	}
}

func TestBuildPagePromptMinimal(t *testing.T) {
	got, err := BuildPagePrompt(PageContext{Title: "T", PageNo: 1, PageCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, absent := range []string{"Paper opening", "Previous page summary", "Glossary ("} {
		if strings.Contains(got, absent) {
			t.Errorf("empty section %q should be omitted\n---\n%s", absent, got)
		}
	}
	if !strings.Contains(got, "(no extractable text") {
		t.Errorf("missing no-text notice\n---\n%s", got)
	}
}

func TestBuildAskPrompt(t *testing.T) {
	got, err := BuildAskPrompt(AskContext{
		Title: "T", PageNo: 2,
		Blocks:      []domain.Block{{ID: "b0", Text: "softmax(QK^T)"}},
		Translation: []domain.BlockTranslation{{BlockID: "b0", Ko: "소프트맥스"}},
		Anchor:      domain.Anchor{BlockIDs: []string{"b0", "b1"}, Quote: "QK^T"},
		History: []domain.Message{
			{Role: domain.RoleUser, Content: "이게 뭐야?"},
			{Role: domain.RoleAssistant, Content: "내적입니다."},
		},
		Question: "왜 루트 d로 나눠?",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Paper: T, page 2",
		"[b0] softmax(QK^T)",
		"[b0] 소프트맥스",
		"Blocks: b0, b1",
		`Quote: "QK^T"`,
		"Reader: 이게 뭐야?",
		"Tutor: 내적입니다.",
		"## Reader's question\n왜 루트 d로 나눠?",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing %q\n---\n%s", want, got)
		}
	}
}
```

- [ ] **Step 3: Run to verify failure**

Run: `cd backend && go test ./internal/llm/`
Expected: FAIL — `undefined: MarkerParser`, `undefined: BuildPagePrompt`, ...

- [ ] **Step 4: Implement the marker parser**

`backend/internal/llm/markers.go`:

```go
package llm

import (
	"errors"
	"strings"

	"github.com/choizeus02/paper-reader/backend/internal/domain"
)

var ErrIncompleteOutput = errors.New("llm output missing @@END marker")

type PageOutput struct {
	Translation []domain.BlockTranslation
	Explanation string
	Summary     string
	Glossary    []domain.GlossaryTerm
}

type section int

const (
	secNone section = iota
	secBlock
	secExplanation
	secSummary
	secGlossary
	secEnd
)

// MarkerParser incrementally parses page-generation output:
//
//	@@BLOCK <id> / @@EXPLANATION / @@SUMMARY / @@GLOSSARY / @@END
//
// Feed it text deltas with Write; completed blocks and explanation lines are
// reported through the callbacks as soon as they are known.
type MarkerParser struct {
	onBlock       func(domain.BlockTranslation)
	onExplanation func(string)

	partial string
	sec     section
	blockID string
	buf     strings.Builder
	expl    strings.Builder
	out     PageOutput
	sawEnd  bool
}

func NewMarkerParser(onBlock func(domain.BlockTranslation), onExplanation func(string)) *MarkerParser {
	return &MarkerParser{onBlock: onBlock, onExplanation: onExplanation}
}

func (p *MarkerParser) Write(chunk string) {
	p.partial += chunk
	for {
		i := strings.IndexByte(p.partial, '\n')
		if i < 0 {
			return
		}
		line := strings.TrimRight(p.partial[:i], "\r")
		p.partial = p.partial[i+1:]
		p.line(line)
	}
}

func (p *MarkerParser) Finish() (PageOutput, error) {
	if p.partial != "" {
		p.line(strings.TrimRight(p.partial, "\r"))
		p.partial = ""
	}
	p.closeSection()
	if !p.sawEnd {
		return PageOutput{}, ErrIncompleteOutput
	}
	if p.out.Translation == nil {
		p.out.Translation = []domain.BlockTranslation{}
	}
	p.out.Explanation = strings.TrimSpace(p.expl.String())
	return p.out, nil
}

func (p *MarkerParser) line(line string) {
	if p.sec == secEnd {
		return
	}
	marker := strings.TrimSpace(line)
	switch {
	case strings.HasPrefix(marker, "@@BLOCK "):
		p.closeSection()
		p.sec = secBlock
		p.blockID = strings.TrimSpace(strings.TrimPrefix(marker, "@@BLOCK "))
		return
	case marker == "@@EXPLANATION":
		p.closeSection()
		p.sec = secExplanation
		return
	case marker == "@@SUMMARY":
		p.closeSection()
		p.sec = secSummary
		return
	case marker == "@@GLOSSARY":
		p.closeSection()
		p.sec = secGlossary
		return
	case marker == "@@END":
		p.closeSection()
		p.sec = secEnd
		p.sawEnd = true
		return
	}
	switch p.sec {
	case secBlock, secSummary, secGlossary:
		p.buf.WriteString(line)
		p.buf.WriteByte('\n')
	case secExplanation:
		p.expl.WriteString(line)
		p.expl.WriteByte('\n')
		if p.onExplanation != nil {
			p.onExplanation(line + "\n")
		}
	}
}

func (p *MarkerParser) closeSection() {
	text := strings.TrimSpace(p.buf.String())
	p.buf.Reset()
	switch p.sec {
	case secBlock:
		if p.blockID != "" && text != "" {
			bt := domain.BlockTranslation{BlockID: p.blockID, Ko: text}
			p.out.Translation = append(p.out.Translation, bt)
			if p.onBlock != nil {
				p.onBlock(bt)
			}
		}
		p.blockID = ""
	case secSummary:
		p.out.Summary = text
	case secGlossary:
		p.out.Glossary = parseGlossary(text)
	}
	p.sec = secNone
}

func parseGlossary(text string) []domain.GlossaryTerm {
	var terms []domain.GlossaryTerm
	for _, line := range strings.Split(text, "\n") {
		parts := strings.Split(line, "|")
		if len(parts) < 2 {
			continue
		}
		t := domain.GlossaryTerm{En: strings.TrimSpace(parts[0]), Ko: strings.TrimSpace(parts[1])}
		if len(parts) > 2 {
			t.Note = strings.TrimSpace(strings.Join(parts[2:], "|"))
		}
		if t.En == "" || t.Ko == "" {
			continue
		}
		terms = append(terms, t)
	}
	return terms
}
```

- [ ] **Step 5: Write the templates**

`backend/internal/llm/prompts/page.tmpl`:

```
Paper: {{.Title}}
Page {{.PageNo}} of {{.PageCount}}
{{- if .Abstract}}

## Paper opening (for context)
{{.Abstract}}
{{- end}}
{{- if .PrevSummary}}

## Previous page summary
{{.PrevSummary}}
{{- end}}
{{- if .Glossary}}

## Glossary (use these Korean terms consistently)
{{- range .Glossary}}
- {{.En}} → {{.Ko}}{{if .Note}} ({{.Note}}){{end}}
{{- end}}
{{- end}}

## Text blocks extracted from this page
The attached image is this page. Blocks are listed in extraction order. Extraction can garble text (especially math), so use the image to correct it.
{{range .Blocks}}
[{{.ID}}] {{.Text}}
{{- else}}
(no extractable text — rely on the image)
{{- end}}

## Task
1. Translate every block into natural, precise Korean. Skip only page numbers, running headers/footers and arXiv stamps. Keep citations like [12] as they are. Write math in LaTeX using $...$ or $$...$$. For a block that is only math, give the LaTeX followed by a one-line Korean statement of what it says.
2. Write a deep-dive explanation of this page in Korean Markdown using these headings (omit a heading only if the page has nothing for it):
   ## 이 페이지 요지
   ## 핵심 내용 풀이
   ## 수식 풀이
   ## 그림·표 해설
   ## 용어
3. Write a one- or two-sentence Korean summary of this page.
4. List technical terms from this page that are not in the glossary above.

## Output format
Output exactly the following and nothing else.
@@BLOCK <block id>
<Korean translation of that block>
(repeat one @@BLOCK section per translated block, in block order)
@@EXPLANATION
<Markdown explanation>
@@SUMMARY
<summary>
@@GLOSSARY
<english term> | <korean term> | <optional note>
@@END
```

`backend/internal/llm/prompts/ask.tmpl`:

```
Paper: {{.Title}}, page {{.PageNo}}. The attached image is this page.

## Page text
{{- range .Blocks}}
[{{.ID}}] {{.Text}}
{{- else}}
(no extractable text)
{{- end}}
{{- if .Translation}}

## Korean translation of this page
{{- range .Translation}}
[{{.BlockID}}] {{.Ko}}
{{- end}}
{{- end}}

## What the reader selected
{{- if .Anchor.BlockIDs}}
Blocks: {{range $i, $id := .Anchor.BlockIDs}}{{if $i}}, {{end}}{{$id}}{{end}}
{{- end}}
{{- if .Anchor.Quote}}
Quote: "{{.Anchor.Quote}}"
{{- end}}
{{- if .History}}

## Conversation so far
{{- range .History}}
{{if eq .Role "user"}}Reader{{else}}Tutor{{end}}: {{.Content}}
{{- end}}
{{- end}}

## Reader's question
{{.Question}}
```

- [ ] **Step 6: Implement the prompt builders**

`backend/internal/llm/prompt.go`:

```go
package llm

import (
	_ "embed"
	"strings"
	"text/template"

	"github.com/choizeus02/paper-reader/backend/internal/domain"
)

// PagePromptVersion is stored with each page result; bump it when page.tmpl
// or PageSystem changes meaningfully.
const PagePromptVersion = 1

const PageSystem = "You are an expert translator and tutor for academic papers. " +
	"You translate English research papers into natural, precise Korean and explain them to a graduate-level reader. " +
	"You always follow the requested output format exactly."

const AskSystem = "You are a patient tutor helping a reader understand an academic paper. " +
	"Answer in Korean using Markdown. Write math in LaTeX with $...$ (inline) or $$...$$ (display). " +
	"On first use of a technical term, add the English term in parentheses."

var (
	//go:embed prompts/page.tmpl
	pageTmplText string
	//go:embed prompts/ask.tmpl
	askTmplText string

	pageTmpl = template.Must(template.New("page").Parse(pageTmplText))
	askTmpl  = template.Must(template.New("ask").Parse(askTmplText))
)

type PageContext struct {
	Title       string
	PageNo      int
	PageCount   int
	Abstract    string
	PrevSummary string
	Glossary    []domain.GlossaryTerm
	Blocks      []domain.Block
}

func BuildPagePrompt(c PageContext) (string, error) {
	var b strings.Builder
	err := pageTmpl.Execute(&b, c)
	return b.String(), err
}

type AskContext struct {
	Title       string
	PageNo      int
	Blocks      []domain.Block
	Translation []domain.BlockTranslation
	Anchor      domain.Anchor
	History     []domain.Message
	Question    string
}

func BuildAskPrompt(c AskContext) (string, error) {
	var b strings.Builder
	err := askTmpl.Execute(&b, c)
	return b.String(), err
}
```

- [ ] **Step 7: Run tests**

Run: `cd backend && go test ./internal/llm/ -v`
Expected: all llm tests PASS (Task 3's plus 7 new).

- [ ] **Step 8: Commit**

```bash
git add backend/internal/llm
git commit -m "feat(llm): add page/ask prompts and streaming marker parser"
```

---

### Task 5: Postgres store — schema, papers, pages

**Files:**
- Create: `backend/internal/store/migrations/00001_init.sql`
- Create: `backend/internal/store/db.go`
- Create: `backend/internal/store/papers.go`
- Create: `backend/internal/store/store_test.go`
- Create: `backend/internal/store/papers_test.go`

**Interfaces:**
- Consumes: `domain.*`
- Produces (`*store.Store` methods; all return `domain.ErrNotFound` when the row is missing):
  - `store.Open(ctx context.Context, url, schema string) (*Store, error)`; `(*Store).Migrate(ctx) error`; `(*Store).Close()`
  - `CreatePaper(ctx, p domain.Paper) error`
  - `SetPaperReady(ctx, id string, pages []domain.Page) error`
  - `SetPaperFailed(ctx, id, msg string) error`
  - `ListPapers(ctx) ([]domain.Paper, error)` (newest first, `DonePages` filled)
  - `GetPaper(ctx, id string) (domain.Paper, error)`
  - `DeletePaper(ctx, id string) error`
  - `GetPage(ctx, paperID string, pageNo int) (domain.Page, error)`
  - test helpers (in `store_test.go`): `newTestStore(t) *Store`, `seedPaper(t, s, id string, pages int)`

- [ ] **Step 1: Start a throwaway test database**

Run: `make test-db-up`
Expected: a container id is printed (`docker ps` shows `paper-reader-testdb`).

- [ ] **Step 2: Add dependencies**

Run: `cd backend && go get github.com/jackc/pgx/v5@latest github.com/pressly/goose/v3@latest`
Expected: both added to `go.mod`.

- [ ] **Step 3: Write the migration**

`backend/internal/store/migrations/00001_init.sql`:

```sql
-- +goose Up
create table papers (
  id          text primary key,
  title       text not null,
  filename    text not null,
  page_count  int  not null default 0,
  status      text not null,
  error       text,
  created_at  timestamptz not null default now()
);

create table pages (
  paper_id    text not null references papers(id) on delete cascade,
  page_no     int  not null,
  width       real not null,
  height      real not null,
  image_path  text not null,
  blocks      jsonb not null,
  primary key (paper_id, page_no)
);

create table page_results (
  paper_id        text not null,
  page_no         int  not null,
  status          text not null,
  translation     jsonb,
  explanation     text,
  page_summary    text,
  prompt_version  int  not null default 0,
  error           text,
  updated_at      timestamptz not null default now(),
  primary key (paper_id, page_no),
  foreign key (paper_id, page_no) references pages(paper_id, page_no) on delete cascade
);

create table glossary (
  paper_id   text not null references papers(id) on delete cascade,
  term_en    text not null,
  term_ko    text not null,
  note       text,
  primary key (paper_id, term_en)
);

create table threads (
  id          text primary key,
  paper_id    text not null,
  page_no     int  not null,
  anchor      jsonb not null,
  created_at  timestamptz not null default now(),
  foreign key (paper_id, page_no) references pages(paper_id, page_no) on delete cascade
);
create index threads_page_idx on threads (paper_id, page_no);

create table messages (
  id          bigserial primary key,
  thread_id   text not null references threads(id) on delete cascade,
  role        text not null,
  content     text not null,
  status      text not null,
  created_at  timestamptz not null default now()
);
create index messages_thread_idx on messages (thread_id, id);

-- +goose Down
drop table messages;
drop table threads;
drop table glossary;
drop table page_results;
drop table pages;
drop table papers;
```

- [ ] **Step 4: Write test helpers and failing tests**

`backend/internal/store/store_test.go`:

```go
package store

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/choizeus02/paper-reader/backend/internal/domain"
)

// newTestStore gives each test its own schema inside TEST_DATABASE_URL and
// drops it afterwards. Tests are skipped when TEST_DATABASE_URL is unset.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set (run `make test-db-up` and `make test`)")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("test_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "create schema "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "drop schema "+schema+" cascade")
		_ = admin.Close(context.Background())
	})
	s, err := Open(ctx, url, schema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}

// seedPaper creates a ready paper with n pages; page k has one block "b0".
func seedPaper(t *testing.T, s *Store, id string, n int) {
	t.Helper()
	ctx := context.Background()
	if err := s.CreatePaper(ctx, domain.Paper{ID: id, Title: id, Filename: id + ".pdf", Status: domain.PaperProcessing, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	pages := make([]domain.Page, n)
	for i := range pages {
		pages[i] = domain.Page{PaperID: id, PageNo: i + 1, Width: 612, Height: 792,
			ImagePath: fmt.Sprintf("papers/%s/pages/%03d.png", id, i+1),
			Blocks:    []domain.Block{{ID: "b0", Text: fmt.Sprintf("page %d", i+1)}}}
	}
	if err := s.SetPaperReady(ctx, id, pages); err != nil {
		t.Fatal(err)
	}
}
```

`backend/internal/store/papers_test.go`:

```go
package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/choizeus02/paper-reader/backend/internal/domain"
)

func TestPaperLifecycle(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	created := time.Now().UTC().Truncate(time.Microsecond)

	if err := s.CreatePaper(ctx, domain.Paper{ID: "p1", Title: "Attention", Filename: "Attention.pdf", Status: domain.PaperProcessing, CreatedAt: created}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetPaper(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.PaperProcessing || got.PageCount != 0 || got.Title != "Attention" || !got.CreatedAt.Equal(created) {
		t.Fatalf("unexpected paper: %+v", got)
	}

	page1Blocks := []domain.Block{{ID: "b0", BBox: domain.BBox{X: 1, Y: 2, W: 3, H: 4}, Text: "Hello"}}
	pages := []domain.Page{
		{PaperID: "p1", PageNo: 1, Width: 612, Height: 792, ImagePath: "papers/p1/pages/001.png", Blocks: page1Blocks},
		{PaperID: "p1", PageNo: 2, Width: 612, Height: 792, ImagePath: "papers/p1/pages/002.png", Blocks: nil},
	}
	if err := s.SetPaperReady(ctx, "p1", pages); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetPaper(ctx, "p1")
	if got.Status != domain.PaperReady || got.PageCount != 2 {
		t.Fatalf("paper not ready: %+v", got)
	}

	pg1, err := s.GetPage(ctx, "p1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pg1.Blocks, page1Blocks) || pg1.ImagePath != "papers/p1/pages/001.png" || pg1.Width != 612 {
		t.Fatalf("page 1 wrong: %+v", pg1)
	}
	pg2, err := s.GetPage(ctx, "p1", 2)
	if err != nil {
		t.Fatal(err)
	}
	if pg2.Blocks == nil || len(pg2.Blocks) != 0 {
		t.Fatalf("page 2 blocks must be empty slice, got %#v", pg2.Blocks)
	}
	if _, err := s.GetPage(ctx, "p1", 3); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}

	list, err := s.ListPapers(ctx)
	if err != nil || len(list) != 1 || list[0].ID != "p1" || list[0].DonePages != 0 {
		t.Fatalf("list wrong: %+v %v", list, err)
	}

	if err := s.DeletePaper(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetPaper(ctx, "p1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want ErrNotFound after delete, got %v", err)
	}
	if _, err := s.GetPage(ctx, "p1", 1); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("pages must cascade, got %v", err)
	}
	if err := s.DeletePaper(ctx, "p1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("second delete: want ErrNotFound, got %v", err)
	}
}

func TestSetPaperFailedAndOrdering(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	old := time.Now().UTC().Add(-time.Hour)
	_ = s.CreatePaper(ctx, domain.Paper{ID: "old", Title: "old", Filename: "old.pdf", Status: domain.PaperProcessing, CreatedAt: old})
	_ = s.CreatePaper(ctx, domain.Paper{ID: "new", Title: "new", Filename: "new.pdf", Status: domain.PaperProcessing, CreatedAt: old.Add(time.Minute)})
	if err := s.SetPaperFailed(ctx, "old", "mutool: cannot open"); err != nil {
		t.Fatal(err)
	}
	p, _ := s.GetPaper(ctx, "old")
	if p.Status != domain.PaperFailed || p.Error != "mutool: cannot open" {
		t.Fatalf("not failed: %+v", p)
	}
	list, _ := s.ListPapers(ctx)
	if len(list) != 2 || list[0].ID != "new" {
		t.Fatalf("want newest first, got %+v", list)
	}
	if _, err := s.GetPaper(ctx, "missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}
```

- [ ] **Step 5: Run to verify failure**

Run: `make test` (from repo root)
Expected: FAIL in `internal/store` — `undefined: Open`, `undefined: Store`

- [ ] **Step 6: Implement db.go**

`backend/internal/store/db.go`:

```go
// Package store persists domain data in Postgres.
package store

import (
	"context"
	"embed"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Store struct {
	pool *pgxpool.Pool
}

// Open connects to Postgres. A non-empty schema becomes every connection's
// search_path (tests use this for isolation).
func Open(ctx context.Context, url, schema string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	if schema != "" {
		cfg.ConnConfig.RuntimeParams["search_path"] = schema
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }

func (s *Store) Migrate(ctx context.Context) error {
	db := stdlib.OpenDBFromPool(s.pool)
	defer db.Close()
	goose.SetBaseFS(migrations)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	return goose.UpContext(ctx, db, "migrations")
}
```

- [ ] **Step 7: Implement papers.go**

`backend/internal/store/papers.go`:

```go
package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/choizeus02/paper-reader/backend/internal/domain"
)

const paperCols = `p.id, p.title, p.filename, p.page_count, p.status, coalesce(p.error, ''), p.created_at,
	(select count(*) from page_results r where r.paper_id = p.id and r.status = 'done')`

func scanPaper(row pgx.Row) (domain.Paper, error) {
	var p domain.Paper
	var status string
	err := row.Scan(&p.ID, &p.Title, &p.Filename, &p.PageCount, &status, &p.Error, &p.CreatedAt, &p.DonePages)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Paper{}, domain.ErrNotFound
	}
	p.Status = domain.PaperStatus(status)
	return p, err
}

func (s *Store) CreatePaper(ctx context.Context, p domain.Paper) error {
	_, err := s.pool.Exec(ctx,
		`insert into papers (id, title, filename, page_count, status, created_at) values ($1, $2, $3, $4, $5, $6)`,
		p.ID, p.Title, p.Filename, p.PageCount, string(p.Status), p.CreatedAt)
	return err
}

func (s *Store) SetPaperReady(ctx context.Context, id string, pages []domain.Page) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, pg := range pages {
		blocks := pg.Blocks
		if blocks == nil {
			blocks = []domain.Block{}
		}
		if _, err := tx.Exec(ctx,
			`insert into pages (paper_id, page_no, width, height, image_path, blocks) values ($1, $2, $3, $4, $5, $6)`,
			id, pg.PageNo, pg.Width, pg.Height, pg.ImagePath, blocks); err != nil {
			return err
		}
	}
	tag, err := tx.Exec(ctx, `update papers set page_count = $2, status = 'ready', error = null where id = $1`, id, len(pages))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return tx.Commit(ctx)
}

func (s *Store) SetPaperFailed(ctx context.Context, id, msg string) error {
	tag, err := s.pool.Exec(ctx, `update papers set status = 'failed', error = $2 where id = $1`, id, msg)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (s *Store) ListPapers(ctx context.Context) ([]domain.Paper, error) {
	rows, err := s.pool.Query(ctx, `select `+paperCols+` from papers p order by p.created_at desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	papers := []domain.Paper{}
	for rows.Next() {
		p, err := scanPaper(rows)
		if err != nil {
			return nil, err
		}
		papers = append(papers, p)
	}
	return papers, rows.Err()
}

func (s *Store) GetPaper(ctx context.Context, id string) (domain.Paper, error) {
	return scanPaper(s.pool.QueryRow(ctx, `select `+paperCols+` from papers p where p.id = $1`, id))
}

func (s *Store) DeletePaper(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `delete from papers where id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (s *Store) GetPage(ctx context.Context, paperID string, pageNo int) (domain.Page, error) {
	pg := domain.Page{PaperID: paperID, PageNo: pageNo}
	err := s.pool.QueryRow(ctx,
		`select width, height, image_path, blocks from pages where paper_id = $1 and page_no = $2`,
		paperID, pageNo).Scan(&pg.Width, &pg.Height, &pg.ImagePath, &pg.Blocks)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Page{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Page{}, err
	}
	if pg.Blocks == nil {
		pg.Blocks = []domain.Block{}
	}
	return pg, nil
}
```

Note: `page_results` and `threads` reference `pages`; deleting a paper cascades papers → pages → results/threads → messages, and papers → glossary.

- [ ] **Step 8: Run tests**

Run: `make test`
Expected: `internal/store` PASS (2 tests). Other packages still pass.

- [ ] **Step 9: Commit**

```bash
git add backend/go.mod backend/go.sum backend/internal/store
git commit -m "feat(store): postgres schema, papers and pages"
```

---

### Task 6: Postgres store — results, glossary, threads

**Files:**
- Create: `backend/internal/store/results.go`
- Create: `backend/internal/store/glossary.go`
- Create: `backend/internal/store/threads.go`
- Create: `backend/internal/store/results_test.go`
- Create: `backend/internal/store/threads_test.go`

**Interfaces:**
- Consumes: `newTestStore`, `seedPaper` (Task 5), `domain.*`
- Produces (`*store.Store` methods):
  - `GetResult(ctx, paperID string, pageNo int) (domain.PageResult, error)` (`Translation` never nil)
  - `MarkPending(ctx, paperID string, pageNo int, force bool) (bool, error)` — creates a `pending` row, or flips `failed` (or `done` when `force`) to `pending`; returns `false` when the row is already `pending`/`running`, or `done` without force.
  - `MarkRunning(ctx, paperID string, pageNo int) error`
  - `SaveDone(ctx, r domain.PageResult) error`
  - `MarkFailed(ctx, paperID string, pageNo int, msg string) error`
  - `FailUnfinished(ctx, msg string) (int64, error)`
  - `ListGlossary(ctx, paperID string) ([]domain.GlossaryTerm, error)` (sorted by `En`)
  - `AddGlossary(ctx, paperID string, terms []domain.GlossaryTerm) error` (first writer wins per `En`)
  - `CreateThread(ctx, t domain.Thread) error`
  - `GetThread(ctx, id string) (domain.Thread, error)` (messages oldest first, never nil)
  - `ListThreads(ctx, paperID string, pageNo int) ([]domain.Thread, error)` (oldest first, messages filled)
  - `AddMessage(ctx, m domain.Message) (int64, error)`
  - `FinishMessage(ctx, id int64, content string, status domain.MessageStatus) error`

- [ ] **Step 1: Write failing results/glossary tests**

`backend/internal/store/results_test.go`:

```go
package store

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/choizeus02/paper-reader/backend/internal/domain"
)

func TestResultStateMachine(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedPaper(t, s, "p1", 2)

	if _, err := s.GetResult(ctx, "p1", 1); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	mustPending := func(force, want bool) {
		t.Helper()
		got, err := s.MarkPending(ctx, "p1", 1, force)
		if err != nil || got != want {
			t.Fatalf("MarkPending(force=%v) = %v, %v; want %v", force, got, err, want)
		}
	}
	mustPending(false, true)  // new row
	mustPending(false, false) // already pending
	if err := s.MarkRunning(ctx, "p1", 1); err != nil {
		t.Fatal(err)
	}
	mustPending(true, false) // running is never re-queued, even with force

	tr := []domain.BlockTranslation{{BlockID: "b0", Ko: "안녕"}}
	if err := s.SaveDone(ctx, domain.PageResult{PaperID: "p1", PageNo: 1, Translation: tr, Explanation: "설명", PageSummary: "요약", PromptVersion: 1}); err != nil {
		t.Fatal(err)
	}
	r, err := s.GetResult(ctx, "p1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != domain.ResultDone || !reflect.DeepEqual(r.Translation, tr) || r.Explanation != "설명" || r.PageSummary != "요약" || r.PromptVersion != 1 {
		t.Fatalf("done result wrong: %+v", r)
	}
	p, _ := s.GetPaper(ctx, "p1")
	if p.DonePages != 1 {
		t.Fatalf("DonePages = %d, want 1", p.DonePages)
	}

	mustPending(false, false) // done without force
	mustPending(true, true)   // done with force
	if err := s.MarkFailed(ctx, "p1", 1, "claude: usage limit"); err != nil {
		t.Fatal(err)
	}
	r, _ = s.GetResult(ctx, "p1", 1)
	if r.Status != domain.ResultFailed || r.Error != "claude: usage limit" {
		t.Fatalf("failed result wrong: %+v", r)
	}
	mustPending(false, true) // failed → pending
	r, _ = s.GetResult(ctx, "p1", 1)
	if r.Error != "" {
		t.Fatalf("error should be cleared on re-queue: %+v", r)
	}

	if _, err := s.MarkPending(ctx, "p1", 2, false); err != nil {
		t.Fatal(err)
	}
	n, err := s.FailUnfinished(ctx, "서버 재시작으로 중단됨")
	if err != nil || n != 2 {
		t.Fatalf("FailUnfinished = %d, %v; want 2", n, err)
	}
	r, _ = s.GetResult(ctx, "p1", 2)
	if r.Status != domain.ResultFailed || r.Error != "서버 재시작으로 중단됨" || r.Translation == nil {
		t.Fatalf("unfinished not failed: %+v", r)
	}
}

func TestGlossaryFirstWins(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedPaper(t, s, "p1", 1)
	if err := s.AddGlossary(ctx, "p1", []domain.GlossaryTerm{{En: "attention", Ko: "어텐션", Note: "음차"}, {En: "encoder", Ko: "인코더"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddGlossary(ctx, "p1", []domain.GlossaryTerm{{En: "attention", Ko: "주의"}, {En: "decoder", Ko: "디코더"}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListGlossary(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.GlossaryTerm{{En: "attention", Ko: "어텐션", Note: "음차"}, {En: "decoder", Ko: "디코더"}, {En: "encoder", Ko: "인코더"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	empty, err := s.ListGlossary(ctx, "nope")
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("want empty non-nil slice, got %#v %v", empty, err)
	}
}
```

- [ ] **Step 2: Write failing thread tests**

`backend/internal/store/threads_test.go`:

```go
package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/choizeus02/paper-reader/backend/internal/domain"
)

func TestThreads(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedPaper(t, s, "p1", 2)
	now := time.Now().UTC()

	th := domain.Thread{ID: "t1", PaperID: "p1", PageNo: 1, Anchor: domain.Anchor{BlockIDs: []string{"b0"}, Quote: "QK^T"}, CreatedAt: now}
	if err := s.CreateThread(ctx, th); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateThread(ctx, domain.Thread{ID: "t2", PaperID: "p1", PageNo: 1, Anchor: domain.Anchor{BlockIDs: []string{}}, CreatedAt: now.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateThread(ctx, domain.Thread{ID: "t3", PaperID: "p1", PageNo: 2, Anchor: domain.Anchor{BlockIDs: []string{}}, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}

	uid, err := s.AddMessage(ctx, domain.Message{ThreadID: "t1", Role: domain.RoleUser, Content: "왜?", Status: domain.MessageDone, CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	aid, err := s.AddMessage(ctx, domain.Message{ThreadID: "t1", Role: domain.RoleAssistant, Content: "", Status: domain.MessageStreaming, CreatedAt: now})
	if err != nil || aid <= uid {
		t.Fatalf("ids: user=%d assistant=%d err=%v", uid, aid, err)
	}
	if err := s.FinishMessage(ctx, aid, "그래서요.", domain.MessageDone); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishMessage(ctx, 999999, "x", domain.MessageDone); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}

	got, err := s.GetThread(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Anchor.Quote != "QK^T" || len(got.Messages) != 2 || got.Messages[0].Role != domain.RoleUser || got.Messages[1].Content != "그래서요." || got.Messages[1].Status != domain.MessageDone {
		t.Fatalf("thread wrong: %+v", got)
	}

	list, err := s.ListThreads(ctx, "p1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != "t1" || list[1].ID != "t2" || len(list[0].Messages) != 2 || list[1].Messages == nil {
		t.Fatalf("list wrong: %+v", list)
	}
	if _, err := s.GetThread(ctx, "missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	empty, err := s.ListThreads(ctx, "p1", 9)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("want empty slice, got %#v %v", empty, err)
	}
}
```

- [ ] **Step 3: Run to verify failure**

Run: `make test`
Expected: FAIL — `s.GetResult undefined`, ...

- [ ] **Step 4: Implement results.go**

`backend/internal/store/results.go`:

```go
package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/choizeus02/paper-reader/backend/internal/domain"
)

func (s *Store) GetResult(ctx context.Context, paperID string, pageNo int) (domain.PageResult, error) {
	r := domain.PageResult{PaperID: paperID, PageNo: pageNo}
	var status string
	err := s.pool.QueryRow(ctx, `
		select status, coalesce(translation, '[]'::jsonb), coalesce(explanation, ''), coalesce(page_summary, ''),
		       prompt_version, coalesce(error, ''), updated_at
		  from page_results where paper_id = $1 and page_no = $2`, paperID, pageNo).
		Scan(&status, &r.Translation, &r.Explanation, &r.PageSummary, &r.PromptVersion, &r.Error, &r.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.PageResult{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.PageResult{}, err
	}
	r.Status = domain.ResultStatus(status)
	if r.Translation == nil {
		r.Translation = []domain.BlockTranslation{}
	}
	return r, nil
}

func (s *Store) MarkPending(ctx context.Context, paperID string, pageNo int, force bool) (bool, error) {
	var one int
	err := s.pool.QueryRow(ctx, `
		insert into page_results (paper_id, page_no, status, updated_at)
		values ($1, $2, 'pending', now())
		on conflict (paper_id, page_no) do update
		   set status = 'pending', error = null, updated_at = now()
		 where page_results.status = 'failed'
		    or ($3::boolean and page_results.status = 'done')
		returning 1`, paperID, pageNo, force).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *Store) setStatus(ctx context.Context, paperID string, pageNo int, status domain.ResultStatus, msg *string) error {
	tag, err := s.pool.Exec(ctx,
		`update page_results set status = $3, error = $4, updated_at = now() where paper_id = $1 and page_no = $2`,
		paperID, pageNo, string(status), msg)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (s *Store) MarkRunning(ctx context.Context, paperID string, pageNo int) error {
	return s.setStatus(ctx, paperID, pageNo, domain.ResultRunning, nil)
}

func (s *Store) MarkFailed(ctx context.Context, paperID string, pageNo int, msg string) error {
	return s.setStatus(ctx, paperID, pageNo, domain.ResultFailed, &msg)
}

func (s *Store) SaveDone(ctx context.Context, r domain.PageResult) error {
	tr := r.Translation
	if tr == nil {
		tr = []domain.BlockTranslation{}
	}
	tag, err := s.pool.Exec(ctx, `
		update page_results
		   set status = 'done', translation = $3, explanation = $4, page_summary = $5,
		       prompt_version = $6, error = null, updated_at = now()
		 where paper_id = $1 and page_no = $2`,
		r.PaperID, r.PageNo, tr, r.Explanation, r.PageSummary, r.PromptVersion)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (s *Store) FailUnfinished(ctx context.Context, msg string) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`update page_results set status = 'failed', error = $1, updated_at = now() where status in ('pending', 'running')`, msg)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
```

- [ ] **Step 5: Implement glossary.go**

`backend/internal/store/glossary.go`:

```go
package store

import (
	"context"
	"strings"

	"github.com/choizeus02/paper-reader/backend/internal/domain"
)

func (s *Store) ListGlossary(ctx context.Context, paperID string) ([]domain.GlossaryTerm, error) {
	rows, err := s.pool.Query(ctx,
		`select term_en, term_ko, coalesce(note, '') from glossary where paper_id = $1 order by term_en`, paperID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	terms := []domain.GlossaryTerm{}
	for rows.Next() {
		var t domain.GlossaryTerm
		if err := rows.Scan(&t.En, &t.Ko, &t.Note); err != nil {
			return nil, err
		}
		terms = append(terms, t)
	}
	return terms, rows.Err()
}

// AddGlossary inserts terms; an existing English term keeps its first translation.
func (s *Store) AddGlossary(ctx context.Context, paperID string, terms []domain.GlossaryTerm) error {
	for _, t := range terms {
		en, ko := strings.TrimSpace(t.En), strings.TrimSpace(t.Ko)
		if en == "" || ko == "" {
			continue
		}
		if _, err := s.pool.Exec(ctx,
			`insert into glossary (paper_id, term_en, term_ko, note) values ($1, $2, $3, nullif($4, ''))
			 on conflict (paper_id, term_en) do nothing`,
			paperID, en, ko, strings.TrimSpace(t.Note)); err != nil {
			return err
		}
	}
	return nil
}
```

- [ ] **Step 6: Implement threads.go**

`backend/internal/store/threads.go`:

```go
package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/choizeus02/paper-reader/backend/internal/domain"
)

func (s *Store) CreateThread(ctx context.Context, t domain.Thread) error {
	anchor := t.Anchor
	if anchor.BlockIDs == nil {
		anchor.BlockIDs = []string{}
	}
	_, err := s.pool.Exec(ctx,
		`insert into threads (id, paper_id, page_no, anchor, created_at) values ($1, $2, $3, $4, $5)`,
		t.ID, t.PaperID, t.PageNo, anchor, t.CreatedAt)
	return err
}

func (s *Store) AddMessage(ctx context.Context, m domain.Message) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx,
		`insert into messages (thread_id, role, content, status, created_at) values ($1, $2, $3, $4, $5) returning id`,
		m.ThreadID, string(m.Role), m.Content, string(m.Status), m.CreatedAt).Scan(&id)
	return id, err
}

func (s *Store) FinishMessage(ctx context.Context, id int64, content string, status domain.MessageStatus) error {
	tag, err := s.pool.Exec(ctx, `update messages set content = $2, status = $3 where id = $1`, id, content, string(status))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (s *Store) GetThread(ctx context.Context, id string) (domain.Thread, error) {
	var t domain.Thread
	err := s.pool.QueryRow(ctx,
		`select id, paper_id, page_no, anchor, created_at from threads where id = $1`, id).
		Scan(&t.ID, &t.PaperID, &t.PageNo, &t.Anchor, &t.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Thread{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Thread{}, err
	}
	msgs, err := s.messagesFor(ctx, []string{id})
	if err != nil {
		return domain.Thread{}, err
	}
	t.Messages = nonNil(msgs[id])
	return t, nil
}

func (s *Store) ListThreads(ctx context.Context, paperID string, pageNo int) ([]domain.Thread, error) {
	rows, err := s.pool.Query(ctx,
		`select id, paper_id, page_no, anchor, created_at from threads
		  where paper_id = $1 and page_no = $2 order by created_at, id`, paperID, pageNo)
	if err != nil {
		return nil, err
	}
	threads := []domain.Thread{}
	var ids []string
	for rows.Next() {
		var t domain.Thread
		if err := rows.Scan(&t.ID, &t.PaperID, &t.PageNo, &t.Anchor, &t.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		threads = append(threads, t)
		ids = append(ids, t.ID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return threads, nil
	}
	msgs, err := s.messagesFor(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range threads {
		threads[i].Messages = nonNil(msgs[threads[i].ID])
	}
	return threads, nil
}

func (s *Store) messagesFor(ctx context.Context, threadIDs []string) (map[string][]domain.Message, error) {
	rows, err := s.pool.Query(ctx,
		`select id, thread_id, role, content, status, created_at from messages where thread_id = any($1) order by id`, threadIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]domain.Message{}
	for rows.Next() {
		var m domain.Message
		var role, status string
		if err := rows.Scan(&m.ID, &m.ThreadID, &role, &m.Content, &status, &m.CreatedAt); err != nil {
			return nil, err
		}
		m.Role, m.Status = domain.MessageRole(role), domain.MessageStatus(status)
		out[m.ThreadID] = append(out[m.ThreadID], m)
	}
	return out, rows.Err()
}

func nonNil(m []domain.Message) []domain.Message {
	if m == nil {
		return []domain.Message{}
	}
	return m
}
```

- [ ] **Step 7: Run tests**

Run: `make test`
Expected: `internal/store` PASS (5 tests).

- [ ] **Step 8: Commit**

```bash
git add backend/internal/store
git commit -m "feat(store): page results, glossary, threads and messages"
```

---

### Task 7: Job queue and event hub

**Files:**
- Create: `backend/internal/jobs/queue.go`
- Create: `backend/internal/jobs/hub.go`
- Create: `backend/internal/jobs/queue_test.go`
- Create: `backend/internal/jobs/hub_test.go`

**Interfaces:**
- Consumes: nothing
- Produces:
  - `jobs.Key{PaperID string; PageNo int}`, `jobs.Priority` with `jobs.Low`, `jobs.High`
  - `jobs.Handler func(ctx context.Context, k Key) error`
  - `jobs.NewQueue(workers int, timeout time.Duration) *Queue`; `(*Queue).Start(ctx, h Handler)`; `(*Queue).Wait()`; `(*Queue).Enqueue(k Key, p Priority)`; `(*Queue).Promote(k Key)` — duplicates of queued or running keys are ignored; a higher-priority Enqueue promotes.
  - `jobs.Event{Name string; Data any}`
  - `jobs.NewHub() *Hub`; `(*Hub).Open(k)`; `(*Hub).Publish(k, e Event)`; `(*Hub).Close(k)`; `(*Hub).Subscribe(k) (replay []Event, ch <-chan Event, cancel func(), ok bool)` — `ok=false` when no stream is open for `k`. `ch` is closed by `Close`, by a newer `Open`, or if the subscriber falls 256 events behind.

- [ ] **Step 1: Write failing queue tests**

`backend/internal/jobs/queue_test.go`:

```go
package jobs

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// recorder runs jobs one at a time; the first job blocks until release is closed.
type recorder struct {
	mu      sync.Mutex
	order   []Key
	started chan Key
	release chan struct{}
	done    chan struct{}
	want    int
}

func newRecorder(want int) *recorder {
	return &recorder{started: make(chan Key, 16), release: make(chan struct{}), done: make(chan struct{}), want: want}
}

func (r *recorder) handle(ctx context.Context, k Key) error {
	r.started <- k
	<-r.release
	r.mu.Lock()
	r.order = append(r.order, k)
	if len(r.order) == r.want {
		close(r.done)
	}
	r.mu.Unlock()
	return nil
}

func (r *recorder) wait(t *testing.T) []Key {
	t.Helper()
	select {
	case <-r.done:
	case <-time.After(2 * time.Second):
		t.Fatal("jobs did not finish")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Key(nil), r.order...)
}

func k(n int) Key { return Key{PaperID: "p", PageNo: n} }

func startQueue(t *testing.T, h Handler) *Queue {
	ctx, cancel := context.WithCancel(context.Background())
	q := NewQueue(1, time.Second)
	q.Start(ctx, h)
	t.Cleanup(func() { cancel(); q.Wait() })
	return q
}

func TestQueueHighBeforeLow(t *testing.T) {
	r := newRecorder(3)
	q := startQueue(t, r.handle)
	q.Enqueue(k(1), Low) // occupies the single worker
	<-r.started
	q.Enqueue(k(2), Low)
	q.Enqueue(k(3), High)
	close(r.release)
	got := r.wait(t)
	if got[0] != k(1) || got[1] != k(3) || got[2] != k(2) {
		t.Fatalf("order = %v", got)
	}
}

func TestQueueDedup(t *testing.T) {
	r := newRecorder(2)
	q := startQueue(t, r.handle)
	q.Enqueue(k(1), High)
	<-r.started
	q.Enqueue(k(1), High) // running → ignored
	q.Enqueue(k(2), Low)
	q.Enqueue(k(2), Low) // queued → ignored
	close(r.release)
	got := r.wait(t)
	time.Sleep(50 * time.Millisecond) // give a duplicate a chance to run
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.order) != 2 || got[0] != k(1) || got[1] != k(2) {
		t.Fatalf("order = %v", r.order)
	}
}

func TestQueuePromote(t *testing.T) {
	r := newRecorder(4)
	q := startQueue(t, r.handle)
	q.Enqueue(k(1), Low)
	<-r.started
	q.Enqueue(k(2), Low)
	q.Enqueue(k(3), Low)
	q.Promote(k(3))
	q.Enqueue(k(4), Low)
	q.Enqueue(k(4), High) // higher-priority enqueue also promotes
	q.Promote(k(9))       // unknown key: no-op
	close(r.release)
	got := r.wait(t)
	if got[1] != k(3) || got[2] != k(4) || got[3] != k(2) {
		t.Fatalf("order = %v", got)
	}
}

func TestQueueTimeout(t *testing.T) {
	errs := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	q := NewQueue(1, 50*time.Millisecond)
	q.Start(ctx, func(ctx context.Context, k Key) error {
		<-ctx.Done()
		errs <- ctx.Err()
		return ctx.Err()
	})
	defer func() { cancel(); q.Wait() }()
	q.Enqueue(k(1), High)
	select {
	case err := <-errs:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("want deadline, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout not applied")
	}
}

func TestQueueSurvivesPanic(t *testing.T) {
	ran := make(chan Key, 2)
	ctx, cancel := context.WithCancel(context.Background())
	q := NewQueue(1, time.Second)
	q.Start(ctx, func(ctx context.Context, k Key) error {
		ran <- k
		if k.PageNo == 1 {
			panic("boom")
		}
		return nil
	})
	defer func() { cancel(); q.Wait() }()
	q.Enqueue(k(1), High)
	q.Enqueue(k(2), High)
	for i := 0; i < 2; i++ {
		select {
		case <-ran:
		case <-time.After(2 * time.Second):
			t.Fatal("worker died after panic")
		}
	}
}
```

- [ ] **Step 2: Write failing hub tests**

`backend/internal/jobs/hub_test.go`:

```go
package jobs

import (
	"testing"
	"time"
)

func recv(t *testing.T, ch <-chan Event) (Event, bool) {
	t.Helper()
	select {
	case e, ok := <-ch:
		return e, ok
	case <-time.After(time.Second):
		t.Fatal("no event")
		return Event{}, false
	}
}

func TestHubReplayAndLive(t *testing.T) {
	h := NewHub()
	key := Key{PaperID: "p", PageNo: 1}
	if _, _, _, ok := h.Subscribe(key); ok {
		t.Fatal("no stream yet")
	}
	h.Open(key)
	h.Publish(key, Event{Name: "status", Data: "running"})
	replay, ch, cancel, ok := h.Subscribe(key)
	defer cancel()
	if !ok || len(replay) != 1 || replay[0].Name != "status" {
		t.Fatalf("replay = %+v ok=%v", replay, ok)
	}
	h.Publish(key, Event{Name: "block", Data: 1})
	if e, _ := recv(t, ch); e.Name != "block" {
		t.Fatalf("got %+v", e)
	}
	h.Publish(key, Event{Name: "done"})
	h.Close(key)
	if e, _ := recv(t, ch); e.Name != "done" {
		t.Fatalf("got %+v", e)
	}
	if _, ok := recv(t, ch); ok {
		t.Fatal("channel should be closed after Close")
	}
	if _, _, _, ok := h.Subscribe(key); ok {
		t.Fatal("stream should be gone after Close")
	}
}

func TestHubCancelAndReopen(t *testing.T) {
	h := NewHub()
	key := Key{PaperID: "p", PageNo: 1}
	h.Open(key)
	_, ch1, cancel1, _ := h.Subscribe(key)
	cancel1()
	if _, ok := recv(t, ch1); ok {
		t.Fatal("cancel should close the channel")
	}
	cancel1() // idempotent

	_, ch2, cancel2, _ := h.Subscribe(key)
	defer cancel2()
	h.Publish(key, Event{Name: "old"})
	h.Open(key) // new run replaces the stream
	if e, ok := recv(t, ch2); !ok || e.Name != "old" {
		t.Fatalf("want buffered old event, got %+v %v", e, ok)
	}
	if _, ok := recv(t, ch2); ok {
		t.Fatal("old subscribers are closed on reopen")
	}
	replay, _, cancel3, ok := h.Subscribe(key)
	defer cancel3()
	if !ok || len(replay) != 0 {
		t.Fatalf("reopened stream should start empty: %+v", replay)
	}
	h.Publish(Key{PaperID: "x"}, Event{Name: "ignored"}) // no stream: no panic
}

func TestHubDropsSlowSubscriber(t *testing.T) {
	h := NewHub()
	key := Key{PaperID: "p", PageNo: 1}
	h.Open(key)
	_, ch, cancel, _ := h.Subscribe(key)
	defer cancel()
	for i := 0; i < subBuffer+10; i++ {
		h.Publish(key, Event{Name: "block", Data: i})
	}
	n := 0
	for range ch {
		n++
	}
	if n != subBuffer {
		t.Fatalf("got %d buffered events before close, want %d", n, subBuffer)
	}
}
```

- [ ] **Step 3: Run to verify failure**

Run: `cd backend && go test ./internal/jobs/`
Expected: FAIL — `undefined: NewQueue`, `undefined: NewHub`

- [ ] **Step 4: Implement the queue**

`backend/internal/jobs/queue.go`:

```go
// Package jobs runs page-generation work in the background and fans out
// progress events to subscribers.
package jobs

import (
	"context"
	"log"
	"sync"
	"time"
)

type Key struct {
	PaperID string
	PageNo  int
}

type Priority int

const (
	Low Priority = iota
	High
)

type Handler func(ctx context.Context, k Key) error

// Queue is an in-memory two-level priority queue with a fixed worker pool.
// A key is never queued or running twice at the same time.
type Queue struct {
	workers int
	timeout time.Duration

	mu      sync.Mutex
	high    []Key
	low     []Key
	queued  map[Key]Priority
	running map[Key]bool

	wake chan struct{}
	wg   sync.WaitGroup
}

func NewQueue(workers int, timeout time.Duration) *Queue {
	return &Queue{
		workers: workers,
		timeout: timeout,
		queued:  map[Key]Priority{},
		running: map[Key]bool{},
		wake:    make(chan struct{}, workers),
	}
}

func (q *Queue) Start(ctx context.Context, h Handler) {
	for i := 0; i < q.workers; i++ {
		q.wg.Add(1)
		go q.loop(ctx, h)
	}
}

// Wait blocks until all workers have exited (after the Start context ends).
func (q *Queue) Wait() { q.wg.Wait() }

func (q *Queue) Enqueue(k Key, p Priority) {
	q.mu.Lock()
	if q.running[k] {
		q.mu.Unlock()
		return
	}
	if cur, ok := q.queued[k]; ok {
		if p > cur {
			q.promoteLocked(k)
		}
		q.mu.Unlock()
		return
	}
	q.queued[k] = p
	if p == High {
		q.high = append(q.high, k)
	} else {
		q.low = append(q.low, k)
	}
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// Promote moves a queued low-priority key to the end of the high queue.
func (q *Queue) Promote(k Key) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.promoteLocked(k)
}

func (q *Queue) promoteLocked(k Key) {
	if cur, ok := q.queued[k]; !ok || cur == High {
		return
	}
	for i, x := range q.low {
		if x == k {
			q.low = append(q.low[:i], q.low[i+1:]...)
			break
		}
	}
	q.high = append(q.high, k)
	q.queued[k] = High
}

func (q *Queue) next() (Key, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	var k Key
	switch {
	case len(q.high) > 0:
		k, q.high = q.high[0], q.high[1:]
	case len(q.low) > 0:
		k, q.low = q.low[0], q.low[1:]
	default:
		return Key{}, false
	}
	delete(q.queued, k)
	q.running[k] = true
	return k, true
}

func (q *Queue) loop(ctx context.Context, h Handler) {
	defer q.wg.Done()
	for {
		if ctx.Err() != nil {
			return
		}
		k, ok := q.next()
		if !ok {
			select {
			case <-ctx.Done():
				return
			case <-q.wake:
			}
			continue
		}
		q.run(ctx, h, k)
	}
}

func (q *Queue) run(ctx context.Context, h Handler, k Key) {
	jctx, cancel := context.WithTimeout(ctx, q.timeout)
	defer cancel()
	defer func() {
		q.mu.Lock()
		delete(q.running, k)
		q.mu.Unlock()
	}()
	defer func() {
		if r := recover(); r != nil {
			log.Printf("job %s/%d panicked: %v", k.PaperID, k.PageNo, r)
		}
	}()
	if err := h(jctx, k); err != nil {
		log.Printf("job %s/%d failed: %v", k.PaperID, k.PageNo, err)
	}
}
```

- [ ] **Step 5: Implement the hub**

`backend/internal/jobs/hub.go`:

```go
package jobs

import "sync"

// subBuffer is how far a subscriber may fall behind before it is dropped.
const subBuffer = 256

type Event struct {
	Name string
	Data any
}

type stream struct {
	events []Event
	subs   map[chan Event]struct{}
}

// Hub buffers the events of each active job so late subscribers can replay them.
type Hub struct {
	mu      sync.Mutex
	streams map[Key]*stream
}

func NewHub() *Hub { return &Hub{streams: map[Key]*stream{}} }

// Open starts a fresh stream for k, closing subscribers of any previous one.
func (h *Hub) Open(k Key) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if old, ok := h.streams[k]; ok {
		for ch := range old.subs {
			close(ch)
		}
	}
	h.streams[k] = &stream{subs: map[chan Event]struct{}{}}
}

func (h *Hub) Publish(k Key, e Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.streams[k]
	if !ok {
		return
	}
	s.events = append(s.events, e)
	for ch := range s.subs {
		select {
		case ch <- e:
		default: // too slow: drop; the client reconnects and replays
			delete(s.subs, ch)
			close(ch)
		}
	}
}

// Close ends the stream for k and closes all subscriber channels.
func (h *Hub) Close(k Key) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.streams[k]
	if !ok {
		return
	}
	for ch := range s.subs {
		close(ch)
	}
	delete(h.streams, k)
}

func (h *Hub) Subscribe(k Key) ([]Event, <-chan Event, func(), bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.streams[k]
	if !ok {
		return nil, nil, func() {}, false
	}
	ch := make(chan Event, subBuffer)
	s.subs[ch] = struct{}{}
	replay := append([]Event(nil), s.events...)
	cancel := func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, live := s.subs[ch]; live {
			delete(s.subs, ch)
			close(ch)
		}
	}
	return replay, ch, cancel, true
}
```

- [ ] **Step 6: Run tests with the race detector**

Run: `cd backend && go test -race ./internal/jobs/ -v`
Expected: 8 tests PASS, no race reports.

- [ ] **Step 7: Commit**

```bash
git add backend/internal/jobs
git commit -m "feat(jobs): priority worker queue and replaying event hub"
```

---

### Task 8: Service ports, fakes, and PaperService

**Files:**
- Create: `backend/internal/service/ports.go`
- Create: `backend/internal/service/fakes_test.go`
- Create: `backend/internal/service/paper.go`
- Create: `backend/internal/service/paper_test.go`

**Interfaces:**
- Consumes: `domain.*`, `llm.Request`, `jobs.Key`, `jobs.Priority`, `jobs.Event`
- Produces:
  - Interfaces `PaperRepo`, `PageRepo`, `ResultRepo`, `GlossaryRepo`, `ThreadRepo`, `PDFProcessor`, `LLM`, `JobQueue`, `EventHub` (exact method sets below — `*store.Store`, `pdf.Extractor`, `llm.Claude`, `*jobs.Queue`, `*jobs.Hub` satisfy them)
  - `service.NewPaperService(repo PaperRepo, pdf PDFProcessor, dataDir string) *PaperService`
  - `(*PaperService).Upload(ctx, filename string, r io.Reader) (domain.Paper, error)` — returns immediately with status `processing`; extraction runs in the background.
  - `(*PaperService).List(ctx) ([]domain.Paper, error)`, `Get(ctx, id) (domain.Paper, error)`, `Delete(ctx, id) error`
  - test fakes: `newFakeRepo()`, `fakePDF`, `fakeLLM`, `fakeResponse`, `fakeQueue`

- [ ] **Step 1: Add the uuid dependency**

Run: `cd backend && go get github.com/google/uuid@latest`

- [ ] **Step 2: Write the ports**

`backend/internal/service/ports.go`:

```go
// Package service holds the application's business logic. It defines the
// interfaces it needs; store, pdf, llm and jobs provide implementations.
package service

import (
	"context"

	"github.com/choizeus02/paper-reader/backend/internal/domain"
	"github.com/choizeus02/paper-reader/backend/internal/jobs"
	"github.com/choizeus02/paper-reader/backend/internal/llm"
)

type PaperRepo interface {
	CreatePaper(ctx context.Context, p domain.Paper) error
	SetPaperReady(ctx context.Context, id string, pages []domain.Page) error
	SetPaperFailed(ctx context.Context, id, msg string) error
	ListPapers(ctx context.Context) ([]domain.Paper, error)
	GetPaper(ctx context.Context, id string) (domain.Paper, error)
	DeletePaper(ctx context.Context, id string) error
}

type PageRepo interface {
	GetPage(ctx context.Context, paperID string, pageNo int) (domain.Page, error)
}

type ResultRepo interface {
	GetResult(ctx context.Context, paperID string, pageNo int) (domain.PageResult, error)
	MarkPending(ctx context.Context, paperID string, pageNo int, force bool) (bool, error)
	MarkRunning(ctx context.Context, paperID string, pageNo int) error
	SaveDone(ctx context.Context, r domain.PageResult) error
	MarkFailed(ctx context.Context, paperID string, pageNo int, msg string) error
	FailUnfinished(ctx context.Context, msg string) (int64, error)
}

type GlossaryRepo interface {
	ListGlossary(ctx context.Context, paperID string) ([]domain.GlossaryTerm, error)
	AddGlossary(ctx context.Context, paperID string, terms []domain.GlossaryTerm) error
}

type ThreadRepo interface {
	CreateThread(ctx context.Context, t domain.Thread) error
	GetThread(ctx context.Context, id string) (domain.Thread, error)
	ListThreads(ctx context.Context, paperID string, pageNo int) ([]domain.Thread, error)
	AddMessage(ctx context.Context, m domain.Message) (int64, error)
	FinishMessage(ctx context.Context, id int64, content string, status domain.MessageStatus) error
}

type PDFProcessor interface {
	Process(ctx context.Context, pdfPath, outDir string) ([]domain.Page, error)
}

type LLM interface {
	Run(ctx context.Context, req llm.Request, onDelta func(string)) (string, error)
}

type JobQueue interface {
	Enqueue(k jobs.Key, p jobs.Priority)
	Promote(k jobs.Key)
}

type EventHub interface {
	Open(k jobs.Key)
	Publish(k jobs.Key, e jobs.Event)
	Close(k jobs.Key)
	Subscribe(k jobs.Key) ([]jobs.Event, <-chan jobs.Event, func(), bool)
}
```

- [ ] **Step 3: Write the in-memory fakes** (used by Tasks 8–10)

`backend/internal/service/fakes_test.go`:

```go
package service

import (
	"context"
	"errors"
	"sort"
	"sync"

	"github.com/choizeus02/paper-reader/backend/internal/domain"
	"github.com/choizeus02/paper-reader/backend/internal/jobs"
	"github.com/choizeus02/paper-reader/backend/internal/llm"
)

type fakeRepo struct {
	mu       sync.Mutex
	papers   map[string]domain.Paper
	pages    map[jobs.Key]domain.Page
	results  map[jobs.Key]domain.PageResult
	glossary map[string][]domain.GlossaryTerm
	threads  map[string]domain.Thread
	nextMsg  int64
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		papers:   map[string]domain.Paper{},
		pages:    map[jobs.Key]domain.Page{},
		results:  map[jobs.Key]domain.PageResult{},
		glossary: map[string][]domain.GlossaryTerm{},
		threads:  map[string]domain.Thread{},
	}
}

func (f *fakeRepo) CreatePaper(_ context.Context, p domain.Paper) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.papers[p.ID] = p
	return nil
}

func (f *fakeRepo) SetPaperReady(_ context.Context, id string, pages []domain.Page) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.papers[id]
	if !ok {
		return domain.ErrNotFound
	}
	p.Status, p.PageCount, p.Error = domain.PaperReady, len(pages), ""
	f.papers[id] = p
	for _, pg := range pages {
		f.pages[jobs.Key{PaperID: id, PageNo: pg.PageNo}] = pg
	}
	return nil
}

func (f *fakeRepo) SetPaperFailed(_ context.Context, id, msg string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.papers[id]
	if !ok {
		return domain.ErrNotFound
	}
	p.Status, p.Error = domain.PaperFailed, msg
	f.papers[id] = p
	return nil
}

func (f *fakeRepo) ListPapers(context.Context) ([]domain.Paper, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []domain.Paper{}
	for _, p := range f.papers {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (f *fakeRepo) GetPaper(_ context.Context, id string) (domain.Paper, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.papers[id]
	if !ok {
		return domain.Paper{}, domain.ErrNotFound
	}
	return p, nil
}

func (f *fakeRepo) DeletePaper(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.papers[id]; !ok {
		return domain.ErrNotFound
	}
	delete(f.papers, id)
	for k := range f.pages {
		if k.PaperID == id {
			delete(f.pages, k)
			delete(f.results, k)
		}
	}
	return nil
}

func (f *fakeRepo) GetPage(_ context.Context, paperID string, pageNo int) (domain.Page, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pg, ok := f.pages[jobs.Key{PaperID: paperID, PageNo: pageNo}]
	if !ok {
		return domain.Page{}, domain.ErrNotFound
	}
	return pg, nil
}

func (f *fakeRepo) GetResult(_ context.Context, paperID string, pageNo int) (domain.PageResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.results[jobs.Key{PaperID: paperID, PageNo: pageNo}]
	if !ok {
		return domain.PageResult{}, domain.ErrNotFound
	}
	return r, nil
}

func (f *fakeRepo) MarkPending(_ context.Context, paperID string, pageNo int, force bool) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := jobs.Key{PaperID: paperID, PageNo: pageNo}
	r, ok := f.results[k]
	if ok && !(r.Status == domain.ResultFailed || (force && r.Status == domain.ResultDone)) {
		return false, nil
	}
	if !ok {
		r = domain.PageResult{PaperID: paperID, PageNo: pageNo, Translation: []domain.BlockTranslation{}}
	}
	r.Status, r.Error = domain.ResultPending, ""
	f.results[k] = r
	return true, nil
}

func (f *fakeRepo) setStatus(paperID string, pageNo int, s domain.ResultStatus, msg string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := jobs.Key{PaperID: paperID, PageNo: pageNo}
	r, ok := f.results[k]
	if !ok {
		return domain.ErrNotFound
	}
	r.Status, r.Error = s, msg
	f.results[k] = r
	return nil
}

func (f *fakeRepo) MarkRunning(_ context.Context, paperID string, pageNo int) error {
	return f.setStatus(paperID, pageNo, domain.ResultRunning, "")
}

func (f *fakeRepo) MarkFailed(_ context.Context, paperID string, pageNo int, msg string) error {
	return f.setStatus(paperID, pageNo, domain.ResultFailed, msg)
}

func (f *fakeRepo) SaveDone(_ context.Context, r domain.PageResult) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := jobs.Key{PaperID: r.PaperID, PageNo: r.PageNo}
	if _, ok := f.results[k]; !ok {
		return domain.ErrNotFound
	}
	r.Status, r.Error = domain.ResultDone, ""
	f.results[k] = r
	return nil
}

func (f *fakeRepo) FailUnfinished(_ context.Context, msg string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var n int64
	for k, r := range f.results {
		if r.Status == domain.ResultPending || r.Status == domain.ResultRunning {
			r.Status, r.Error = domain.ResultFailed, msg
			f.results[k] = r
			n++
		}
	}
	return n, nil
}

func (f *fakeRepo) ListGlossary(_ context.Context, paperID string) ([]domain.GlossaryTerm, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.GlossaryTerm{}, f.glossary[paperID]...), nil
}

func (f *fakeRepo) AddGlossary(_ context.Context, paperID string, terms []domain.GlossaryTerm) error {
	f.mu.Lock()
	defer f.mu.Unlock()
outer:
	for _, t := range terms {
		for _, e := range f.glossary[paperID] {
			if e.En == t.En {
				continue outer
			}
		}
		f.glossary[paperID] = append(f.glossary[paperID], t)
	}
	return nil
}

func (f *fakeRepo) CreateThread(_ context.Context, t domain.Thread) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	t.Messages = []domain.Message{}
	f.threads[t.ID] = t
	return nil
}

func (f *fakeRepo) GetThread(_ context.Context, id string) (domain.Thread, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.threads[id]
	if !ok {
		return domain.Thread{}, domain.ErrNotFound
	}
	t.Messages = append([]domain.Message{}, t.Messages...)
	return t, nil
}

func (f *fakeRepo) ListThreads(_ context.Context, paperID string, pageNo int) ([]domain.Thread, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []domain.Thread{}
	for _, t := range f.threads {
		if t.PaperID == paperID && t.PageNo == pageNo {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (f *fakeRepo) AddMessage(_ context.Context, m domain.Message) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.threads[m.ThreadID]
	if !ok {
		return 0, domain.ErrNotFound
	}
	f.nextMsg++
	m.ID = f.nextMsg
	t.Messages = append(t.Messages, m)
	f.threads[m.ThreadID] = t
	return m.ID, nil
}

func (f *fakeRepo) FinishMessage(_ context.Context, id int64, content string, status domain.MessageStatus) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for tid, t := range f.threads {
		for i, m := range t.Messages {
			if m.ID == id {
				t.Messages[i].Content, t.Messages[i].Status = content, status
				f.threads[tid] = t
				return nil
			}
		}
	}
	return domain.ErrNotFound
}

type fakePDF struct {
	pages []domain.Page
	err   error
}

func (f *fakePDF) Process(context.Context, string, string) ([]domain.Page, error) {
	if f.err != nil {
		return nil, f.err
	}
	return append([]domain.Page(nil), f.pages...), nil
}

type fakeResponse struct {
	text string
	err  error
}

// fakeLLM replays scripted responses in order, streaming them in 7-byte chunks.
type fakeLLM struct {
	mu        sync.Mutex
	responses []fakeResponse
	requests  []llm.Request
}

func (f *fakeLLM) Run(ctx context.Context, req llm.Request, onDelta func(string)) (string, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	i := len(f.requests) - 1
	f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if i >= len(f.responses) {
		return "", errors.New("fakeLLM: no response scripted")
	}
	r := f.responses[i]
	if r.err != nil {
		return "", r.err
	}
	for start := 0; start < len(r.text); start += 7 {
		end := start + 7
		if end > len(r.text) {
			end = len(r.text)
		}
		if onDelta != nil {
			onDelta(r.text[start:end])
		}
	}
	return r.text, nil
}

func (f *fakeLLM) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

type queued struct {
	key jobs.Key
	pri jobs.Priority
}

type fakeQueue struct {
	mu       sync.Mutex
	enqueued []queued
	promoted []jobs.Key
}

func (q *fakeQueue) Enqueue(k jobs.Key, p jobs.Priority) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.enqueued = append(q.enqueued, queued{k, p})
}

func (q *fakeQueue) Promote(k jobs.Key) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.promoted = append(q.promoted, k)
}
```

- [ ] **Step 4: Write failing PaperService tests**

`backend/internal/service/paper_test.go`:

```go
package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/choizeus02/paper-reader/backend/internal/domain"
)

func newPaperSvc(t *testing.T, pdf *fakePDF) (*PaperService, *fakeRepo, string) {
	t.Helper()
	repo := newFakeRepo()
	dir := t.TempDir()
	s := NewPaperService(repo, pdf, dir)
	s.newID = func() string { return "p1" }
	s.spawn = func(f func()) { f() } // run extraction synchronously in tests
	return s, repo, dir
}

func TestUploadProcessesPDF(t *testing.T) {
	pdf := &fakePDF{pages: []domain.Page{
		{PageNo: 1, Width: 612, Height: 792, ImagePath: "001.png", Blocks: []domain.Block{{ID: "b0", Text: "Hi"}}},
		{PageNo: 2, Width: 612, Height: 792, ImagePath: "002.png", Blocks: []domain.Block{}},
	}}
	s, repo, dir := newPaperSvc(t, pdf)
	p, err := s.Upload(context.Background(), "Attention Is All You Need.PDF", strings.NewReader("%PDF-1.5 body"))
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "p1" || p.Title != "Attention Is All You Need" || p.Filename != "Attention Is All You Need.PDF" || p.Status != domain.PaperProcessing {
		t.Fatalf("returned paper wrong: %+v", p)
	}
	data, err := os.ReadFile(filepath.Join(dir, "papers", "p1", "original.pdf"))
	if err != nil || string(data) != "%PDF-1.5 body" {
		t.Fatalf("original not stored: %q %v", data, err)
	}
	got, _ := repo.GetPaper(context.Background(), "p1")
	if got.Status != domain.PaperReady || got.PageCount != 2 {
		t.Fatalf("paper not ready: %+v", got)
	}
	pg, _ := repo.GetPage(context.Background(), "p1", 2)
	if pg.PaperID != "p1" || pg.ImagePath != "papers/p1/pages/002.png" {
		t.Fatalf("page not normalised: %+v", pg)
	}
}

func TestUploadRejectsNonPDF(t *testing.T) {
	s, repo, dir := newPaperSvc(t, &fakePDF{})
	for _, tc := range []struct{ name, body string }{
		{"notes.txt", "%PDF-1.5"},
		{"fake.pdf", "hello world"},
		{"tiny.pdf", "%P"},
	} {
		_, err := s.Upload(context.Background(), tc.name, strings.NewReader(tc.body))
		if !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("%s: want ErrInvalid, got %v", tc.name, err)
		}
	}
	if list, _ := repo.ListPapers(context.Background()); len(list) != 0 {
		t.Fatalf("nothing should be stored: %+v", list)
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "papers")); len(entries) != 0 {
		t.Fatalf("no directories should remain: %v", entries)
	}
}

func TestUploadMarksFailedOnExtractError(t *testing.T) {
	s, repo, _ := newPaperSvc(t, &fakePDF{err: errors.New("mutool draw: exit status 1: cannot open")})
	if _, err := s.Upload(context.Background(), "x.pdf", strings.NewReader("%PDF-1.4")); err != nil {
		t.Fatal(err)
	}
	got, _ := repo.GetPaper(context.Background(), "p1")
	if got.Status != domain.PaperFailed || !strings.Contains(got.Error, "cannot open") {
		t.Fatalf("want failed with message, got %+v", got)
	}
}

func TestUploadMarksFailedOnZeroPages(t *testing.T) {
	s, repo, _ := newPaperSvc(t, &fakePDF{pages: []domain.Page{}})
	if _, err := s.Upload(context.Background(), "x.pdf", strings.NewReader("%PDF-1.4")); err != nil {
		t.Fatal(err)
	}
	got, _ := repo.GetPaper(context.Background(), "p1")
	if got.Status != domain.PaperFailed {
		t.Fatalf("want failed, got %+v", got)
	}
}

func TestDeleteRemovesFiles(t *testing.T) {
	s, repo, dir := newPaperSvc(t, &fakePDF{pages: []domain.Page{{PageNo: 1, ImagePath: "001.png", Blocks: []domain.Block{}}}})
	_, _ = s.Upload(context.Background(), "x.pdf", strings.NewReader("%PDF-1.4"))
	if err := s.Delete(context.Background(), "p1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "papers", "p1")); !os.IsNotExist(err) {
		t.Fatal("paper directory should be removed")
	}
	if _, err := repo.GetPaper(context.Background(), "p1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("paper should be deleted")
	}
	if err := s.Delete(context.Background(), "p1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}
```

- [ ] **Step 5: Run to verify failure**

Run: `cd backend && go test ./internal/service/`
Expected: FAIL — `undefined: NewPaperService`, `undefined: PaperService`

- [ ] **Step 6: Implement PaperService**

`backend/internal/service/paper.go`:

```go
package service

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/choizeus02/paper-reader/backend/internal/domain"
)

const extractTimeout = 10 * time.Minute

type PaperService struct {
	repo    PaperRepo
	pdf     PDFProcessor
	dataDir string

	newID func() string
	spawn func(func())
	now   func() time.Time
}

func NewPaperService(repo PaperRepo, pdf PDFProcessor, dataDir string) *PaperService {
	return &PaperService{
		repo: repo, pdf: pdf, dataDir: dataDir,
		newID: uuid.NewString,
		spawn: func(f func()) { go f() },
		now:   time.Now,
	}
}

func paperDir(dataDir, id string) string { return filepath.Join(dataDir, "papers", id) }

// Upload stores the PDF and starts page extraction in the background.
func (s *PaperService) Upload(ctx context.Context, filename string, r io.Reader) (domain.Paper, error) {
	base := filepath.Base(filename)
	ext := filepath.Ext(base)
	if !strings.EqualFold(ext, ".pdf") {
		return domain.Paper{}, fmt.Errorf("%w: PDF 파일만 올릴 수 있습니다", domain.ErrInvalid)
	}
	br := bufio.NewReader(r)
	if head, _ := br.Peek(5); string(head) != "%PDF-" {
		return domain.Paper{}, fmt.Errorf("%w: PDF 형식이 아닙니다", domain.ErrInvalid)
	}

	id := s.newID()
	dir := paperDir(s.dataDir, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return domain.Paper{}, err
	}
	if err := writeFile(filepath.Join(dir, "original.pdf"), br); err != nil {
		os.RemoveAll(dir)
		return domain.Paper{}, err
	}
	p := domain.Paper{
		ID: id, Title: strings.TrimSuffix(base, ext), Filename: base,
		Status: domain.PaperProcessing, CreatedAt: s.now().UTC(),
	}
	if err := s.repo.CreatePaper(ctx, p); err != nil {
		os.RemoveAll(dir)
		return domain.Paper{}, err
	}
	s.spawn(func() { s.process(id) })
	return p, nil
}

func (s *PaperService) process(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), extractTimeout)
	defer cancel()
	dir := paperDir(s.dataDir, id)
	pages, err := s.pdf.Process(ctx, filepath.Join(dir, "original.pdf"), filepath.Join(dir, "pages"))
	if err == nil && len(pages) == 0 {
		err = fmt.Errorf("페이지가 없는 PDF입니다")
	}
	if err == nil {
		for i := range pages {
			pages[i].PaperID = id
			pages[i].ImagePath = filepath.ToSlash(filepath.Join("papers", id, "pages", pages[i].ImagePath))
		}
		err = s.repo.SetPaperReady(ctx, id, pages)
	}
	if err != nil {
		log.Printf("paper %s: extraction failed: %v", id, err)
		if ferr := s.repo.SetPaperFailed(context.Background(), id, err.Error()); ferr != nil {
			log.Printf("paper %s: mark failed: %v", id, ferr)
		}
	}
}

func (s *PaperService) List(ctx context.Context) ([]domain.Paper, error) { return s.repo.ListPapers(ctx) }

func (s *PaperService) Get(ctx context.Context, id string) (domain.Paper, error) {
	return s.repo.GetPaper(ctx, id)
}

func (s *PaperService) Delete(ctx context.Context, id string) error {
	if err := s.repo.DeletePaper(ctx, id); err != nil {
		return err
	}
	return os.RemoveAll(paperDir(s.dataDir, id))
}

func writeFile(path string, r io.Reader) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
```

- [ ] **Step 7: Run tests**

Run: `cd backend && go test ./internal/service/ -v`
Expected: 5 tests PASS.

- [ ] **Step 8: Commit**

```bash
git add backend/go.mod backend/go.sum backend/internal/service
git commit -m "feat(service): ports, test fakes and paper upload/extraction"
```

---

### Task 9: PageService — view, generate, prefetch, job, stream

**Files:**
- Create: `backend/internal/service/page.go`
- Create: `backend/internal/service/page_test.go`

**Interfaces:**
- Consumes: ports and fakes (Task 8); `llm.NewMarkerParser`, `llm.BuildPagePrompt`, `llm.PageContext`, `llm.PageSystem`, `llm.PagePromptVersion` (Task 4); `jobs.Key/High/Low/Event`, `jobs.NewHub` (Task 7)
- Produces:
  - `service.PageDeps{Papers PaperRepo; Pages PageRepo; Results ResultRepo; Glossary GlossaryRepo; LLM LLM; Queue JobQueue; Hub EventHub; DataDir string}`
  - `service.NewPageService(d PageDeps) *PageService`
  - `service.PageView{Page domain.Page; PageCount int; Result *domain.PageResult}`
  - `service.Stream{Replay []jobs.Event; Events <-chan jobs.Event; Cancel func(); Final *jobs.Event}` — when `Final` is set there is no live job and `Events` is nil.
  - `(*PageService).GetPage(ctx, paperID string, pageNo int) (PageView, error)`
  - `(*PageService).ImageFile(ctx, paperID string, pageNo int) (string, error)` — absolute path to the PNG
  - `(*PageService).Generate(ctx, paperID string, pageNo int, force bool) (domain.ResultStatus, error)`
  - `(*PageService).Prefetch(ctx, paperID string, from, count int) (int, error)` — count clamped to 1..10; returns how many pages were newly queued.
  - `(*PageService).Stream(ctx, paperID string, pageNo int) (Stream, error)`
  - `(*PageService).RunJob(ctx, k jobs.Key) error` — the `jobs.Handler`
  - `(*PageService).Recover(ctx) error`
  - SSE event names/data: `status {"status":"running"|"retrying"}`, `block domain.BlockTranslation`, `explanation {"delta":string}`, `done {}`, `error {"message":string}`

- [ ] **Step 1: Write the failing tests**

`backend/internal/service/page_test.go`:

```go
package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/choizeus02/paper-reader/backend/internal/domain"
	"github.com/choizeus02/paper-reader/backend/internal/jobs"
)

const goodOutput = "@@BLOCK b0\n안녕하세요\n@@BLOCK b9\n없는 블록\n@@EXPLANATION\n## 이 페이지 요지\n인사\n@@SUMMARY\n인사하는 페이지\n@@GLOSSARY\nhello | 안녕 | 인사말\n@@END\n"

type pageFixture struct {
	svc   *PageService
	repo  *fakeRepo
	llm   *fakeLLM
	queue *fakeQueue
	hub   *jobs.Hub
}

// newPageFixture seeds paper "p1" with 3 ready pages. Page n has block b0 with
// text "text n" and an image file containing "png-n".
func newPageFixture(t *testing.T, responses ...fakeResponse) *pageFixture {
	t.Helper()
	dir := t.TempDir()
	repo := newFakeRepo()
	_ = repo.CreatePaper(context.Background(), domain.Paper{ID: "p1", Title: "Greeting Paper", Status: domain.PaperProcessing})
	var pages []domain.Page
	for n := 1; n <= 3; n++ {
		rel := filepath.ToSlash(filepath.Join("papers", "p1", "pages", "00"+string(rune('0'+n))+".png"))
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		_ = os.MkdirAll(filepath.Dir(abs), 0o755)
		_ = os.WriteFile(abs, []byte("png-"+string(rune('0'+n))), 0o644)
		pages = append(pages, domain.Page{PaperID: "p1", PageNo: n, Width: 612, Height: 792, ImagePath: rel,
			Blocks: []domain.Block{{ID: "b0", Text: "text " + string(rune('0'+n))}}})
	}
	_ = repo.SetPaperReady(context.Background(), "p1", pages)
	f := &pageFixture{repo: repo, llm: &fakeLLM{responses: responses}, queue: &fakeQueue{}, hub: jobs.NewHub()}
	f.svc = NewPageService(PageDeps{Papers: repo, Pages: repo, Results: repo, Glossary: repo, LLM: f.llm, Queue: f.queue, Hub: f.hub, DataDir: dir})
	return f
}

func key(n int) jobs.Key { return jobs.Key{PaperID: "p1", PageNo: n} }

// collect subscribes to a page's stream and returns a function that drains it.
func (f *pageFixture) collect(t *testing.T, n int) func() []jobs.Event {
	t.Helper()
	replay, ch, cancel, ok := f.hub.Subscribe(key(n))
	if !ok {
		t.Fatalf("no open stream for page %d", n)
	}
	return func() []jobs.Event {
		defer cancel()
		events := append([]jobs.Event{}, replay...)
		timeout := time.After(time.Second)
		for {
			select {
			case e, ok := <-ch:
				if !ok {
					return events
				}
				events = append(events, e)
			case <-timeout:
				t.Fatal("stream not closed")
			}
		}
	}
}

func names(events []jobs.Event) string {
	var s []string
	for _, e := range events {
		s = append(s, e.Name)
	}
	return strings.Join(s, ",")
}

func TestGetPage(t *testing.T) {
	f := newPageFixture(t)
	v, err := f.svc.GetPage(context.Background(), "p1", 2)
	if err != nil {
		t.Fatal(err)
	}
	if v.PageCount != 3 || v.Page.PageNo != 2 || v.Result != nil {
		t.Fatalf("view wrong: %+v", v)
	}
	if _, err := f.svc.GetPage(context.Background(), "p1", 9); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	path, err := f.svc.ImageFile(context.Background(), "p1", 2)
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); string(data) != "png-2" {
		t.Fatalf("image path wrong: %s", path)
	}
}

func TestGenerateTwicePromotes(t *testing.T) {
	f := newPageFixture(t)
	ctx := context.Background()
	st, err := f.svc.Generate(ctx, "p1", 1, false)
	if err != nil || st != domain.ResultPending {
		t.Fatalf("status=%v err=%v", st, err)
	}
	if _, _, cancel, ok := f.hub.Subscribe(key(1)); !ok {
		t.Fatal("stream should be opened on enqueue")
	} else {
		cancel()
	}
	if _, err := f.svc.Generate(ctx, "p1", 1, false); err != nil {
		t.Fatal(err)
	}
	if len(f.queue.enqueued) != 1 || f.queue.enqueued[0] != (queued{key(1), jobs.High}) {
		t.Fatalf("enqueued = %+v", f.queue.enqueued)
	}
	if len(f.queue.promoted) != 1 || f.queue.promoted[0] != key(1) {
		t.Fatalf("promoted = %+v", f.queue.promoted)
	}
	if _, err := f.svc.Generate(ctx, "p1", 9, false); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestGenerateDoneNeedsForce(t *testing.T) {
	f := newPageFixture(t)
	ctx := context.Background()
	_, _ = f.repo.MarkPending(ctx, "p1", 1, false)
	_ = f.repo.SaveDone(ctx, domain.PageResult{PaperID: "p1", PageNo: 1})
	st, _ := f.svc.Generate(ctx, "p1", 1, false)
	if st != domain.ResultDone || len(f.queue.enqueued) != 0 {
		t.Fatalf("done page must not be requeued: st=%v q=%+v", st, f.queue.enqueued)
	}
	st, _ = f.svc.Generate(ctx, "p1", 1, true)
	if st != domain.ResultPending || len(f.queue.enqueued) != 1 {
		t.Fatalf("force should requeue: st=%v q=%+v", st, f.queue.enqueued)
	}
}

func TestPrefetch(t *testing.T) {
	f := newPageFixture(t)
	ctx := context.Background()
	_, _ = f.svc.Generate(ctx, "p1", 2, false) // already pending
	n, err := f.svc.Prefetch(ctx, "p1", 2, 5)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v; want 1 (page 3 only)", n, err)
	}
	last := f.queue.enqueued[len(f.queue.enqueued)-1]
	if last != (queued{key(3), jobs.Low}) {
		t.Fatalf("last enqueued = %+v", last)
	}
	if _, err := f.svc.Prefetch(ctx, "p1", 0, 1); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for from=0, got %v", err)
	}
	if _, err := f.svc.Prefetch(ctx, "nope", 1, 1); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestRunJobSuccess(t *testing.T) {
	f := newPageFixture(t, fakeResponse{text: goodOutput})
	ctx := context.Background()
	_, _ = f.repo.MarkPending(ctx, "p1", 1, false)
	_ = f.repo.SaveDone(ctx, domain.PageResult{PaperID: "p1", PageNo: 1, PageSummary: "첫 페이지 요약"})
	_ = f.repo.AddGlossary(ctx, "p1", []domain.GlossaryTerm{{En: "paper", Ko: "논문"}})
	_, _ = f.svc.Generate(ctx, "p1", 2, false)
	drain := f.collect(t, 2)

	if err := f.svc.RunJob(ctx, key(2)); err != nil {
		t.Fatal(err)
	}
	events := drain()
	if got := names(events); got != "status,block,explanation,explanation,done" {
		t.Fatalf("events = %s", got)
	}
	res, _ := f.repo.GetResult(ctx, "p1", 2)
	if res.Status != domain.ResultDone || res.PromptVersion != 1 || res.PageSummary != "인사하는 페이지" || res.Explanation != "## 이 페이지 요지\n인사" {
		t.Fatalf("result wrong: %+v", res)
	}
	if !reflect.DeepEqual(res.Translation, []domain.BlockTranslation{{BlockID: "b0", Ko: "안녕하세요"}}) {
		t.Fatalf("unknown block ids must be dropped: %+v", res.Translation)
	}
	gl, _ := f.repo.ListGlossary(ctx, "p1")
	if len(gl) != 2 || gl[1].En != "hello" {
		t.Fatalf("glossary not updated: %+v", gl)
	}
	req := f.llm.requests[0]
	if string(req.Image) != "png-2" {
		t.Fatalf("wrong image sent: %q", req.Image)
	}
	for _, want := range []string{"Greeting Paper", "Page 2 of 3", "text 1", "첫 페이지 요약", "paper → 논문", "[b0] text 2"} {
		if !strings.Contains(req.Prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
	if _, _, _, ok := f.hub.Subscribe(key(2)); ok {
		t.Fatal("stream should be closed after the job")
	}
}

func TestRunJobPageWithoutBlocks(t *testing.T) {
	f := newPageFixture(t, fakeResponse{text: "@@EXPLANATION\n그림 페이지\n@@SUMMARY\n그림\n@@GLOSSARY\n@@END\n"})
	ctx := context.Background()
	f.repo.mu.Lock()
	pg := f.repo.pages[key(3)]
	pg.Blocks = []domain.Block{}
	f.repo.pages[key(3)] = pg
	f.repo.mu.Unlock()
	_, _ = f.svc.Generate(ctx, "p1", 3, false)
	if err := f.svc.RunJob(ctx, key(3)); err != nil {
		t.Fatal(err)
	}
	res, _ := f.repo.GetResult(ctx, "p1", 3)
	if res.Status != domain.ResultDone || res.Translation == nil || len(res.Translation) != 0 || res.Explanation != "그림 페이지" {
		t.Fatalf("result wrong: %+v", res)
	}
	if !strings.Contains(f.llm.requests[0].Prompt, "no extractable text") {
		t.Fatal("prompt should flag missing text")
	}
}

func TestRunJobRetriesOnceOnBadFormat(t *testing.T) {
	f := newPageFixture(t, fakeResponse{text: "@@BLOCK b0\n잘림"}, fakeResponse{text: goodOutput})
	ctx := context.Background()
	_, _ = f.svc.Generate(ctx, "p1", 1, false)
	drain := f.collect(t, 1)
	if err := f.svc.RunJob(ctx, key(1)); err != nil {
		t.Fatal(err)
	}
	if f.llm.calls() != 2 {
		t.Fatalf("calls = %d, want 2", f.llm.calls())
	}
	if got := names(drain()); !strings.HasPrefix(got, "status,block,status,block") || !strings.HasSuffix(got, "done") {
		t.Fatalf("events = %s", got)
	}
	res, _ := f.repo.GetResult(ctx, "p1", 1)
	if res.Status != domain.ResultDone {
		t.Fatalf("want done, got %+v", res)
	}
}

func TestRunJobFailsAfterTwoBadFormats(t *testing.T) {
	f := newPageFixture(t, fakeResponse{text: "garbage"}, fakeResponse{text: "garbage"})
	ctx := context.Background()
	_, _ = f.svc.Generate(ctx, "p1", 1, false)
	drain := f.collect(t, 1)
	if err := f.svc.RunJob(ctx, key(1)); err == nil {
		t.Fatal("want error")
	}
	events := drain()
	last := events[len(events)-1]
	if last.Name != "error" || f.llm.calls() != 2 {
		t.Fatalf("last=%+v calls=%d", last, f.llm.calls())
	}
	res, _ := f.repo.GetResult(ctx, "p1", 1)
	if res.Status != domain.ResultFailed || !strings.Contains(res.Error, "출력 형식") {
		t.Fatalf("want failed with format error, got %+v", res)
	}
}

func TestRunJobLLMErrorNoRetry(t *testing.T) {
	f := newPageFixture(t, fakeResponse{err: errors.New("claude: Claude AI usage limit reached")})
	ctx := context.Background()
	_, _ = f.svc.Generate(ctx, "p1", 1, false)
	drain := f.collect(t, 1)
	_ = f.svc.RunJob(ctx, key(1))
	events := drain()
	if f.llm.calls() != 1 {
		t.Fatalf("CLI errors must not be retried; calls=%d", f.llm.calls())
	}
	last := events[len(events)-1]
	if last.Name != "error" || !strings.Contains(last.Data.(map[string]string)["message"], "usage limit") {
		t.Fatalf("last event = %+v", last)
	}
	res, _ := f.repo.GetResult(ctx, "p1", 1)
	if res.Status != domain.ResultFailed || !strings.Contains(res.Error, "usage limit") {
		t.Fatalf("result = %+v", res)
	}
}

func TestRunJobTimeoutMessage(t *testing.T) {
	f := newPageFixture(t, fakeResponse{text: goodOutput})
	_, _ = f.svc.Generate(context.Background(), "p1", 1, false)
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)
	_ = f.svc.RunJob(ctx, key(1))
	res, _ := f.repo.GetResult(context.Background(), "p1", 1)
	if res.Status != domain.ResultFailed || res.Error != "시간 초과" {
		t.Fatalf("result = %+v", res)
	}
}

func TestRunJobCanceledMessage(t *testing.T) {
	f := newPageFixture(t, fakeResponse{text: goodOutput})
	_, _ = f.svc.Generate(context.Background(), "p1", 1, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = f.svc.RunJob(ctx, key(1))
	res, _ := f.repo.GetResult(context.Background(), "p1", 1)
	if res.Status != domain.ResultFailed || res.Error != "서버 재시작으로 중단됨" {
		t.Fatalf("result = %+v", res)
	}
}

func TestStreamFallsBackToDB(t *testing.T) {
	f := newPageFixture(t)
	ctx := context.Background()

	st, err := f.svc.Stream(ctx, "p1", 1)
	if err != nil || st.Final == nil || st.Final.Name != "error" {
		t.Fatalf("no request yet: %+v %v", st, err)
	}

	_, _ = f.repo.MarkPending(ctx, "p1", 1, false)
	_ = f.repo.SaveDone(ctx, domain.PageResult{PaperID: "p1", PageNo: 1})
	st, _ = f.svc.Stream(ctx, "p1", 1)
	if st.Final == nil || st.Final.Name != "done" {
		t.Fatalf("done page: %+v", st)
	}

	_, _ = f.repo.MarkPending(ctx, "p1", 2, false)
	_ = f.repo.MarkFailed(ctx, "p1", 2, "boom")
	st, _ = f.svc.Stream(ctx, "p1", 2)
	if st.Final == nil || st.Final.Name != "error" || st.Final.Data.(map[string]string)["message"] != "boom" {
		t.Fatalf("failed page: %+v", st)
	}

	_, _ = f.svc.Generate(ctx, "p1", 3, false)
	f.hub.Publish(key(3), jobs.Event{Name: "status", Data: map[string]string{"status": "running"}})
	st, _ = f.svc.Stream(ctx, "p1", 3)
	defer st.Cancel()
	if st.Final != nil || st.Events == nil || len(st.Replay) != 1 {
		t.Fatalf("live page: %+v", st)
	}
}

func TestRecover(t *testing.T) {
	f := newPageFixture(t)
	ctx := context.Background()
	_, _ = f.repo.MarkPending(ctx, "p1", 1, false)
	if err := f.svc.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	res, _ := f.repo.GetResult(ctx, "p1", 1)
	if res.Status != domain.ResultFailed || res.Error != "서버 재시작으로 중단됨" {
		t.Fatalf("result = %+v", res)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd backend && go test ./internal/service/`
Expected: FAIL — `undefined: NewPageService`, `undefined: PageDeps`

- [ ] **Step 3: Implement PageService**

`backend/internal/service/page.go`:

```go
package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/choizeus02/paper-reader/backend/internal/domain"
	"github.com/choizeus02/paper-reader/backend/internal/jobs"
	"github.com/choizeus02/paper-reader/backend/internal/llm"
)

const (
	maxPrefetch   = 10
	abstractChars = 1500
	restartMsg    = "서버 재시작으로 중단됨"
)

type PageDeps struct {
	Papers   PaperRepo
	Pages    PageRepo
	Results  ResultRepo
	Glossary GlossaryRepo
	LLM      LLM
	Queue    JobQueue
	Hub      EventHub
	DataDir  string
}

type PageService struct{ d PageDeps }

func NewPageService(d PageDeps) *PageService { return &PageService{d: d} }

type PageView struct {
	Page      domain.Page
	PageCount int
	Result    *domain.PageResult
}

type Stream struct {
	Replay []jobs.Event
	Events <-chan jobs.Event
	Cancel func()
	Final  *jobs.Event
}

func msgEvent(name, msg string) *jobs.Event {
	return &jobs.Event{Name: name, Data: map[string]string{"message": msg}}
}

func (s *PageService) GetPage(ctx context.Context, paperID string, pageNo int) (PageView, error) {
	paper, err := s.d.Papers.GetPaper(ctx, paperID)
	if err != nil {
		return PageView{}, err
	}
	page, err := s.d.Pages.GetPage(ctx, paperID, pageNo)
	if err != nil {
		return PageView{}, err
	}
	v := PageView{Page: page, PageCount: paper.PageCount}
	res, err := s.d.Results.GetResult(ctx, paperID, pageNo)
	switch {
	case err == nil:
		v.Result = &res
	case !errors.Is(err, domain.ErrNotFound):
		return PageView{}, err
	}
	return v, nil
}

func (s *PageService) ImageFile(ctx context.Context, paperID string, pageNo int) (string, error) {
	page, err := s.d.Pages.GetPage(ctx, paperID, pageNo)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.d.DataDir, filepath.FromSlash(page.ImagePath)), nil
}

// Generate queues a page at high priority (or promotes it if prefetch queued it).
func (s *PageService) Generate(ctx context.Context, paperID string, pageNo int, force bool) (domain.ResultStatus, error) {
	if _, err := s.d.Pages.GetPage(ctx, paperID, pageNo); err != nil {
		return "", err
	}
	k := jobs.Key{PaperID: paperID, PageNo: pageNo}
	changed, err := s.d.Results.MarkPending(ctx, paperID, pageNo, force)
	if err != nil {
		return "", err
	}
	if changed {
		s.d.Hub.Open(k)
		s.d.Queue.Enqueue(k, jobs.High)
		return domain.ResultPending, nil
	}
	s.d.Queue.Promote(k)
	res, err := s.d.Results.GetResult(ctx, paperID, pageNo)
	if err != nil {
		return "", err
	}
	return res.Status, nil
}

func (s *PageService) Prefetch(ctx context.Context, paperID string, from, count int) (int, error) {
	if from < 1 {
		return 0, fmt.Errorf("%w: 시작 페이지는 1 이상이어야 합니다", domain.ErrInvalid)
	}
	paper, err := s.d.Papers.GetPaper(ctx, paperID)
	if err != nil {
		return 0, err
	}
	count = min(max(count, 1), maxPrefetch)
	n := 0
	for p := from; p < from+count && p <= paper.PageCount; p++ {
		changed, err := s.d.Results.MarkPending(ctx, paperID, p, false)
		if err != nil {
			return n, err
		}
		if changed {
			k := jobs.Key{PaperID: paperID, PageNo: p}
			s.d.Hub.Open(k)
			s.d.Queue.Enqueue(k, jobs.Low)
			n++
		}
	}
	return n, nil
}

func (s *PageService) Stream(ctx context.Context, paperID string, pageNo int) (Stream, error) {
	k := jobs.Key{PaperID: paperID, PageNo: pageNo}
	if replay, ch, cancel, ok := s.d.Hub.Subscribe(k); ok {
		return Stream{Replay: replay, Events: ch, Cancel: cancel}, nil
	}
	none := func() {}
	res, err := s.d.Results.GetResult(ctx, paperID, pageNo)
	if errors.Is(err, domain.ErrNotFound) {
		return Stream{Cancel: none, Final: msgEvent("error", "생성 요청이 없습니다")}, nil
	}
	if err != nil {
		return Stream{}, err
	}
	switch res.Status {
	case domain.ResultDone:
		return Stream{Cancel: none, Final: &jobs.Event{Name: "done", Data: struct{}{}}}, nil
	case domain.ResultFailed:
		return Stream{Cancel: none, Final: msgEvent("error", res.Error)}, nil
	default:
		return Stream{Cancel: none, Final: msgEvent("error", "진행 중인 작업을 찾을 수 없습니다. 다시 시도하세요")}, nil
	}
}

func (s *PageService) Recover(ctx context.Context) error {
	n, err := s.d.Results.FailUnfinished(ctx, restartMsg)
	if n > 0 {
		log.Printf("marked %d unfinished page jobs as failed", n)
	}
	return err
}

// RunJob is the jobs.Handler for page generation.
func (s *PageService) RunJob(ctx context.Context, k jobs.Key) error {
	err := s.runJob(ctx, k)
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		msg = "시간 초과"
	case errors.Is(err, context.Canceled):
		msg = restartMsg // server is shutting down
	}
	if ferr := s.d.Results.MarkFailed(context.Background(), k.PaperID, k.PageNo, msg); ferr != nil {
		log.Printf("page %s/%d: mark failed: %v", k.PaperID, k.PageNo, ferr)
	}
	s.d.Hub.Publish(k, *msgEvent("error", msg))
	s.d.Hub.Close(k)
	return err
}

func (s *PageService) runJob(ctx context.Context, k jobs.Key) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.d.Results.MarkRunning(ctx, k.PaperID, k.PageNo); err != nil {
		return err
	}
	s.d.Hub.Publish(k, jobs.Event{Name: "status", Data: map[string]string{"status": "running"}})

	req, page, err := s.buildPageRequest(ctx, k)
	if err != nil {
		return err
	}
	known := make(map[string]bool, len(page.Blocks))
	for _, b := range page.Blocks {
		known[b.ID] = true
	}

	var out llm.PageOutput
	for attempt := 1; ; attempt++ {
		parser := llm.NewMarkerParser(
			func(bt domain.BlockTranslation) {
				if known[bt.BlockID] {
					s.d.Hub.Publish(k, jobs.Event{Name: "block", Data: bt})
				}
			},
			func(delta string) {
				s.d.Hub.Publish(k, jobs.Event{Name: "explanation", Data: map[string]string{"delta": delta}})
			},
		)
		if _, err := s.d.LLM.Run(ctx, req, parser.Write); err != nil {
			return err
		}
		var perr error
		out, perr = parser.Finish()
		if perr == nil {
			break
		}
		if attempt >= 2 {
			return fmt.Errorf("출력 형식 오류: %w", perr)
		}
		s.d.Hub.Publish(k, jobs.Event{Name: "status", Data: map[string]string{"status": "retrying"}})
	}

	translation := make([]domain.BlockTranslation, 0, len(out.Translation))
	for _, bt := range out.Translation {
		if known[bt.BlockID] {
			translation = append(translation, bt)
		}
	}
	res := domain.PageResult{
		PaperID: k.PaperID, PageNo: k.PageNo, Status: domain.ResultDone,
		Translation: translation, Explanation: out.Explanation, PageSummary: out.Summary,
		PromptVersion: llm.PagePromptVersion,
	}
	if err := s.d.Results.SaveDone(ctx, res); err != nil {
		return err
	}
	if len(out.Glossary) > 0 {
		if err := s.d.Glossary.AddGlossary(ctx, k.PaperID, out.Glossary); err != nil {
			log.Printf("page %s/%d: glossary: %v", k.PaperID, k.PageNo, err)
		}
	}
	s.d.Hub.Publish(k, jobs.Event{Name: "done", Data: struct{}{}})
	s.d.Hub.Close(k)
	return nil
}

func (s *PageService) buildPageRequest(ctx context.Context, k jobs.Key) (llm.Request, domain.Page, error) {
	paper, err := s.d.Papers.GetPaper(ctx, k.PaperID)
	if err != nil {
		return llm.Request{}, domain.Page{}, err
	}
	page, err := s.d.Pages.GetPage(ctx, k.PaperID, k.PageNo)
	if err != nil {
		return llm.Request{}, domain.Page{}, err
	}
	img, err := os.ReadFile(filepath.Join(s.d.DataDir, filepath.FromSlash(page.ImagePath)))
	if err != nil {
		return llm.Request{}, domain.Page{}, fmt.Errorf("페이지 이미지를 읽을 수 없습니다: %w", err)
	}
	pc := llm.PageContext{Title: paper.Title, PageNo: page.PageNo, PageCount: paper.PageCount, Blocks: page.Blocks}
	if page.PageNo > 1 {
		if first, err := s.d.Pages.GetPage(ctx, k.PaperID, 1); err == nil {
			pc.Abstract = abstractOf(first.Blocks)
		}
		if prev, err := s.d.Results.GetResult(ctx, k.PaperID, page.PageNo-1); err == nil && prev.Status == domain.ResultDone {
			pc.PrevSummary = prev.PageSummary
		}
	}
	if pc.Glossary, err = s.d.Glossary.ListGlossary(ctx, k.PaperID); err != nil {
		return llm.Request{}, domain.Page{}, err
	}
	prompt, err := llm.BuildPagePrompt(pc)
	if err != nil {
		return llm.Request{}, domain.Page{}, err
	}
	return llm.Request{System: llm.PageSystem, Image: img, Prompt: prompt}, page, nil
}

// abstractOf returns roughly the first abstractChars characters of page 1.
func abstractOf(blocks []domain.Block) string {
	var sb strings.Builder
	for _, b := range blocks {
		if sb.Len() >= abstractChars {
			break
		}
		if sb.Len() > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(b.Text)
	}
	r := []rune(sb.String())
	if len(r) > abstractChars {
		r = r[:abstractChars]
	}
	return string(r)
}
```

- [ ] **Step 4: Run tests with the race detector**

Run: `cd backend && go test -race ./internal/service/ -v`
Expected: all service tests PASS (5 paper + 13 page).

- [ ] **Step 5: Commit**

```bash
git add backend/internal/service
git commit -m "feat(service): page generation with queueing, streaming and retries"
```

---

### Task 10: AskService — page-anchored Q&A

**Files:**
- Create: `backend/internal/service/ask.go`
- Create: `backend/internal/service/ask_test.go`

**Interfaces:**
- Consumes: ports and fakes (Task 8); `newPageFixture` (Task 9, same package test helpers); `llm.BuildAskPrompt`, `llm.AskContext`, `llm.AskSystem` (Task 4)
- Produces:
  - `service.AskDeps{Papers PaperRepo; Pages PageRepo; Results ResultRepo; Threads ThreadRepo; LLM LLM; DataDir string}`
  - `service.NewAskService(d AskDeps) *AskService`
  - `(*AskService).List(ctx, paperID string, pageNo int) ([]domain.Thread, error)`
  - `(*AskService).Start(ctx, paperID string, pageNo int, anchor domain.Anchor, question string, onDelta func(string)) (domain.Thread, error)`
  - `(*AskService).Continue(ctx, threadID, question string, onDelta func(string)) (domain.Thread, error)`
  - Both return the full thread (all messages) after the answer is stored. On LLM failure the assistant message is stored with status `failed` and the error is returned.

- [ ] **Step 1: Write the failing tests**

`backend/internal/service/ask_test.go`:

```go
package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/choizeus02/paper-reader/backend/internal/domain"
)

func newAskSvc(t *testing.T, responses ...fakeResponse) (*AskService, *pageFixture) {
	f := newPageFixture(t)
	f.llm.responses = responses
	svc := NewAskService(AskDeps{Papers: f.repo, Pages: f.repo, Results: f.repo, Threads: f.repo, LLM: f.llm, DataDir: f.svc.d.DataDir})
	svc.newID = func() string { return "t1" }
	return svc, f
}

func TestAskStartAndContinue(t *testing.T) {
	svc, f := newAskSvc(t, fakeResponse{text: "내적입니다."}, fakeResponse{text: "스케일 때문이에요."})
	ctx := context.Background()
	_, _ = f.repo.MarkPending(ctx, "p1", 2, false)
	_ = f.repo.SaveDone(ctx, domain.PageResult{PaperID: "p1", PageNo: 2, Translation: []domain.BlockTranslation{{BlockID: "b0", Ko: "텍스트 2"}}})

	var streamed strings.Builder
	th, err := svc.Start(ctx, "p1", 2, domain.Anchor{BlockIDs: []string{"b0"}, Quote: "QK^T"}, "  이게 뭐야?  ", func(d string) { streamed.WriteString(d) })
	if err != nil {
		t.Fatal(err)
	}
	if streamed.String() != "내적입니다." {
		t.Fatalf("streamed %q", streamed.String())
	}
	if th.ID != "t1" || len(th.Messages) != 2 || th.Messages[0].Content != "이게 뭐야?" || th.Messages[1].Content != "내적입니다." || th.Messages[1].Status != domain.MessageDone {
		t.Fatalf("thread wrong: %+v", th)
	}
	req := f.llm.requests[0]
	if string(req.Image) != "png-2" {
		t.Fatalf("image = %q", req.Image)
	}
	for _, want := range []string{`Quote: "QK^T"`, "[b0] 텍스트 2", "이게 뭐야?"} {
		if !strings.Contains(req.Prompt, want) {
			t.Errorf("first prompt missing %q", want)
		}
	}

	th, err = svc.Continue(ctx, "t1", "왜 루트 d로 나눠?", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(th.Messages) != 4 || th.Messages[3].Content != "스케일 때문이에요." {
		t.Fatalf("thread after continue: %+v", th)
	}
	second := f.llm.requests[1].Prompt
	for _, want := range []string{"Reader: 이게 뭐야?", "Tutor: 내적입니다.", "왜 루트 d로 나눠?"} {
		if !strings.Contains(second, want) {
			t.Errorf("follow-up prompt missing %q", want)
		}
	}

	list, _ := svc.List(ctx, "p1", 2)
	if len(list) != 1 || list[0].ID != "t1" {
		t.Fatalf("list = %+v", list)
	}
}

func TestAskLLMFailureStoresFailedMessage(t *testing.T) {
	svc, f := newAskSvc(t, fakeResponse{err: errors.New("claude: usage limit")})
	ctx := context.Background()
	_, err := svc.Start(ctx, "p1", 1, domain.Anchor{}, "질문", nil)
	if err == nil || !strings.Contains(err.Error(), "usage limit") {
		t.Fatalf("want usage limit error, got %v", err)
	}
	th, _ := f.repo.GetThread(ctx, "t1")
	if len(th.Messages) != 2 || th.Messages[1].Status != domain.MessageFailed || !strings.Contains(th.Messages[1].Content, "usage limit") {
		t.Fatalf("failed message not stored: %+v", th.Messages)
	}
	if th.Anchor.BlockIDs == nil {
		t.Fatal("anchor block ids must be non-nil")
	}
}

func TestAskFailedMessagesExcludedFromHistory(t *testing.T) {
	svc, f := newAskSvc(t, fakeResponse{err: errors.New("boom")}, fakeResponse{text: "답"})
	ctx := context.Background()
	_, _ = svc.Start(ctx, "p1", 1, domain.Anchor{}, "첫 질문", nil)
	if _, err := svc.Continue(ctx, "t1", "다시", nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.llm.requests[1].Prompt, "boom") {
		t.Fatal("failed assistant messages must not be sent as history")
	}
}

func TestAskValidation(t *testing.T) {
	svc, _ := newAskSvc(t)
	ctx := context.Background()
	if _, err := svc.Start(ctx, "p1", 1, domain.Anchor{}, "   ", nil); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("empty question: %v", err)
	}
	if _, err := svc.Start(ctx, "p1", 9, domain.Anchor{}, "q", nil); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing page: %v", err)
	}
	if _, err := svc.Continue(ctx, "nope", "q", nil); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing thread: %v", err)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd backend && go test ./internal/service/`
Expected: FAIL — `undefined: NewAskService`, `undefined: AskDeps`

- [ ] **Step 3: Implement AskService**

`backend/internal/service/ask.go`:

```go
package service

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/choizeus02/paper-reader/backend/internal/domain"
	"github.com/choizeus02/paper-reader/backend/internal/llm"
)

type AskDeps struct {
	Papers  PaperRepo
	Pages   PageRepo
	Results ResultRepo
	Threads ThreadRepo
	LLM     LLM
	DataDir string
}

type AskService struct {
	d     AskDeps
	newID func() string
	now   func() time.Time
}

func NewAskService(d AskDeps) *AskService {
	return &AskService{d: d, newID: uuid.NewString, now: time.Now}
}

func (s *AskService) List(ctx context.Context, paperID string, pageNo int) ([]domain.Thread, error) {
	return s.d.Threads.ListThreads(ctx, paperID, pageNo)
}

func (s *AskService) Start(ctx context.Context, paperID string, pageNo int, anchor domain.Anchor, question string, onDelta func(string)) (domain.Thread, error) {
	question = strings.TrimSpace(question)
	if question == "" {
		return domain.Thread{}, fmt.Errorf("%w: 질문을 입력하세요", domain.ErrInvalid)
	}
	if _, err := s.d.Pages.GetPage(ctx, paperID, pageNo); err != nil {
		return domain.Thread{}, err
	}
	if anchor.BlockIDs == nil {
		anchor.BlockIDs = []string{}
	}
	t := domain.Thread{ID: s.newID(), PaperID: paperID, PageNo: pageNo, Anchor: anchor, CreatedAt: s.now().UTC(), Messages: []domain.Message{}}
	if err := s.d.Threads.CreateThread(ctx, t); err != nil {
		return domain.Thread{}, err
	}
	return s.answer(ctx, t, question, onDelta)
}

func (s *AskService) Continue(ctx context.Context, threadID, question string, onDelta func(string)) (domain.Thread, error) {
	question = strings.TrimSpace(question)
	if question == "" {
		return domain.Thread{}, fmt.Errorf("%w: 질문을 입력하세요", domain.ErrInvalid)
	}
	t, err := s.d.Threads.GetThread(ctx, threadID)
	if err != nil {
		return domain.Thread{}, err
	}
	return s.answer(ctx, t, question, onDelta)
}

func (s *AskService) answer(ctx context.Context, t domain.Thread, question string, onDelta func(string)) (domain.Thread, error) {
	req, err := s.buildRequest(ctx, t, question)
	if err != nil {
		return domain.Thread{}, err
	}
	now := s.now().UTC()
	if _, err := s.d.Threads.AddMessage(ctx, domain.Message{ThreadID: t.ID, Role: domain.RoleUser, Content: question, Status: domain.MessageDone, CreatedAt: now}); err != nil {
		return domain.Thread{}, err
	}
	aid, err := s.d.Threads.AddMessage(ctx, domain.Message{ThreadID: t.ID, Role: domain.RoleAssistant, Status: domain.MessageStreaming, CreatedAt: now})
	if err != nil {
		return domain.Thread{}, err
	}

	text, runErr := s.d.LLM.Run(ctx, req, onDelta)
	bg := context.Background() // persist the outcome even if the client went away
	if runErr != nil {
		if err := s.d.Threads.FinishMessage(bg, aid, "답변 생성 실패: "+runErr.Error(), domain.MessageFailed); err != nil {
			log.Printf("thread %s: store failed message: %v", t.ID, err)
		}
		return domain.Thread{}, runErr
	}
	if err := s.d.Threads.FinishMessage(bg, aid, text, domain.MessageDone); err != nil {
		return domain.Thread{}, err
	}
	return s.d.Threads.GetThread(bg, t.ID)
}

func (s *AskService) buildRequest(ctx context.Context, t domain.Thread, question string) (llm.Request, error) {
	paper, err := s.d.Papers.GetPaper(ctx, t.PaperID)
	if err != nil {
		return llm.Request{}, err
	}
	page, err := s.d.Pages.GetPage(ctx, t.PaperID, t.PageNo)
	if err != nil {
		return llm.Request{}, err
	}
	img, err := os.ReadFile(filepath.Join(s.d.DataDir, filepath.FromSlash(page.ImagePath)))
	if err != nil {
		return llm.Request{}, fmt.Errorf("페이지 이미지를 읽을 수 없습니다: %w", err)
	}
	ac := llm.AskContext{Title: paper.Title, PageNo: t.PageNo, Blocks: page.Blocks, Anchor: t.Anchor, Question: question}
	if res, err := s.d.Results.GetResult(ctx, t.PaperID, t.PageNo); err == nil && res.Status == domain.ResultDone {
		ac.Translation = res.Translation
	}
	for _, m := range t.Messages {
		if m.Status == domain.MessageDone {
			ac.History = append(ac.History, m)
		}
	}
	prompt, err := llm.BuildAskPrompt(ac)
	if err != nil {
		return llm.Request{}, err
	}
	return llm.Request{System: llm.AskSystem, Image: img, Prompt: prompt}, nil
}
```

- [ ] **Step 4: Run tests**

Run: `cd backend && go test -race ./internal/service/ -v`
Expected: all service tests PASS (including 4 new ask tests).

- [ ] **Step 5: Commit**

```bash
git add backend/internal/service
git commit -m "feat(service): page-anchored Q&A threads"
```

---

### Task 11: HTTP handlers

**Files:**
- Create: `backend/internal/handler/respond.go`
- Create: `backend/internal/handler/dto.go`
- Create: `backend/internal/handler/router.go`
- Create: `backend/internal/handler/papers.go`
- Create: `backend/internal/handler/pages.go`
- Create: `backend/internal/handler/ask.go`
- Create: `backend/internal/handler/handler_test.go`

**Interfaces:**
- Consumes: `service.PageView`, `service.Stream` (Task 9); `domain.*`; `jobs.Event`
- Produces:
  - Interfaces `handler.PaperAPI`, `handler.PageAPI`, `handler.AskAPI` (method sets below; `*service.PaperService`, `*service.PageService`, `*service.AskService` satisfy them)
  - `handler.NewRouter(papers PaperAPI, pages PageAPI, ask AskAPI, static http.Handler) http.Handler` — `static` may be nil (Plan 2 passes the embedded SPA)
  - HTTP API exactly as spec §11. SSE frames are `event: <name>\ndata: <json>\n\n`. Q&A SSE events: `delta {"text"}`, `done <thread>`, `error {"message"}`.

- [ ] **Step 1: Write the failing tests**

`backend/internal/handler/handler_test.go`:

```go
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/choizeus02/paper-reader/backend/internal/domain"
	"github.com/choizeus02/paper-reader/backend/internal/jobs"
	"github.com/choizeus02/paper-reader/backend/internal/service"
)

type stubPapers struct {
	uploadedName, uploadedBody string
	uploadErr                  error
}

func (s *stubPapers) Upload(_ context.Context, name string, r io.Reader) (domain.Paper, error) {
	b, _ := io.ReadAll(r)
	s.uploadedName, s.uploadedBody = name, string(b)
	if s.uploadErr != nil {
		return domain.Paper{}, s.uploadErr
	}
	return domain.Paper{ID: "p1", Title: "x", Status: domain.PaperProcessing}, nil
}
func (s *stubPapers) List(context.Context) ([]domain.Paper, error) {
	return []domain.Paper{{ID: "p1", Title: "x", Status: domain.PaperReady, PageCount: 2, DonePages: 1}}, nil
}
func (s *stubPapers) Get(_ context.Context, id string) (domain.Paper, error) {
	if id != "p1" {
		return domain.Paper{}, domain.ErrNotFound
	}
	return domain.Paper{ID: "p1"}, nil
}
func (s *stubPapers) Delete(_ context.Context, id string) error {
	if id != "p1" {
		return domain.ErrNotFound
	}
	return nil
}

type stubPages struct {
	image        string
	stream       service.Stream
	gotForce     bool
	gotFrom      int
	gotCount     int
}

func (s *stubPages) GetPage(_ context.Context, id string, n int) (service.PageView, error) {
	if n != 1 {
		return service.PageView{}, domain.ErrNotFound
	}
	return service.PageView{
		Page:      domain.Page{PaperID: id, PageNo: 1, Width: 612, Height: 792, Blocks: []domain.Block{{ID: "b0", Text: "Hi"}}},
		PageCount: 3,
	}, nil
}
func (s *stubPages) ImageFile(context.Context, string, int) (string, error) { return s.image, nil }
func (s *stubPages) Generate(_ context.Context, _ string, _ int, force bool) (domain.ResultStatus, error) {
	s.gotForce = force
	return domain.ResultPending, nil
}
func (s *stubPages) Prefetch(_ context.Context, _ string, from, count int) (int, error) {
	s.gotFrom, s.gotCount = from, count
	return 2, nil
}
func (s *stubPages) Stream(context.Context, string, int) (service.Stream, error) { return s.stream, nil }

type stubAsk struct{ err error }

func (s *stubAsk) List(context.Context, string, int) ([]domain.Thread, error) { return nil, nil }
func (s *stubAsk) Start(_ context.Context, _ string, _ int, a domain.Anchor, q string, onDelta func(string)) (domain.Thread, error) {
	if s.err != nil {
		return domain.Thread{}, s.err
	}
	onDelta("답")
	onDelta("변")
	return domain.Thread{ID: "t1", Anchor: a, Messages: []domain.Message{{Role: domain.RoleUser, Content: q}, {Role: domain.RoleAssistant, Content: "답변"}}}, nil
}
func (s *stubAsk) Continue(_ context.Context, tid, q string, onDelta func(string)) (domain.Thread, error) {
	if tid != "t1" {
		return domain.Thread{}, domain.ErrNotFound
	}
	onDelta("더")
	return domain.Thread{ID: "t1"}, nil
}

func newTestRouter() (http.Handler, *stubPapers, *stubPages, *stubAsk) {
	p, pg, a := &stubPapers{}, &stubPages{}, &stubAsk{}
	return NewRouter(p, pg, a, nil), p, pg, a
}

func do(h http.Handler, method, path string, body io.Reader, contentType string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, body)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestListPapers(t *testing.T) {
	h, _, _, _ := newTestRouter()
	rec := do(h, "GET", "/api/papers", nil, "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"done_pages":1`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func multipartBody(t *testing.T, field, name, content string) (*bytes.Buffer, string) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile(field, name)
	if err != nil {
		t.Fatal(err)
	}
	fw.Write([]byte(content))
	w.Close()
	return &buf, w.FormDataContentType()
}

func TestUpload(t *testing.T) {
	h, papers, _, _ := newTestRouter()
	body, ct := multipartBody(t, "file", "paper.pdf", "%PDF-1.4 x")
	rec := do(h, "POST", "/api/papers", body, ct)
	if rec.Code != http.StatusCreated || papers.uploadedName != "paper.pdf" || papers.uploadedBody != "%PDF-1.4 x" {
		t.Fatalf("%d %s name=%q body=%q", rec.Code, rec.Body, papers.uploadedName, papers.uploadedBody)
	}
}

func TestUploadBadRequest(t *testing.T) {
	h, papers, _, _ := newTestRouter()
	body, ct := multipartBody(t, "wrong", "paper.pdf", "x")
	if rec := do(h, "POST", "/api/papers", body, ct); rec.Code != 400 {
		t.Fatalf("missing file field: %d", rec.Code)
	}
	papers.uploadErr = errors.Join(domain.ErrInvalid, errors.New("PDF 형식이 아닙니다"))
	body, ct = multipartBody(t, "file", "a.pdf", "nope")
	rec := do(h, "POST", "/api/papers", body, ct)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "PDF 형식이 아닙니다") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestNotFoundAndBadPage(t *testing.T) {
	h, _, _, _ := newTestRouter()
	for path, want := range map[string]int{
		"/api/papers/zzz":           404,
		"/api/papers/p1/pages/2":    404,
		"/api/papers/p1/pages/abc":  400,
		"/api/papers/p1/pages/0":    400,
		"/api/nothing":              404,
	} {
		if rec := do(h, "GET", path, nil, ""); rec.Code != want {
			t.Errorf("%s: got %d want %d (%s)", path, rec.Code, want, rec.Body)
		}
	}
	if rec := do(h, "DELETE", "/api/papers/zzz", nil, ""); rec.Code != 404 {
		t.Errorf("delete missing: %d", rec.Code)
	}
	if rec := do(h, "DELETE", "/api/papers/p1", nil, ""); rec.Code != 204 {
		t.Errorf("delete: %d", rec.Code)
	}
}

func TestGetPage(t *testing.T) {
	h, _, _, _ := newTestRouter()
	rec := do(h, "GET", "/api/papers/p1/pages/1", nil, "")
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if rec.Code != 200 || got["image_url"] != "/api/papers/p1/pages/1/image" || got["page_count"].(float64) != 3 || got["result"] != nil {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestPageImage(t *testing.T) {
	h, _, pages, _ := newTestRouter()
	pages.image = filepath.Join(t.TempDir(), "001.png")
	os.WriteFile(pages.image, []byte("\x89PNG\r\n\x1a\nrest"), 0o644)
	rec := do(h, "GET", "/api/papers/p1/pages/1/image", nil, "")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("%d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}

func TestGenerateAndPrefetch(t *testing.T) {
	h, _, pages, _ := newTestRouter()
	rec := do(h, "POST", "/api/papers/p1/pages/1/generate?force=1", nil, "")
	if rec.Code != 202 || !pages.gotForce || !strings.Contains(rec.Body.String(), `"status":"pending"`) {
		t.Fatalf("%d %s force=%v", rec.Code, rec.Body, pages.gotForce)
	}
	rec = do(h, "POST", "/api/papers/p1/prefetch", strings.NewReader(`{"from":2,"count":3}`), "application/json")
	if rec.Code != 202 || pages.gotFrom != 2 || pages.gotCount != 3 || !strings.Contains(rec.Body.String(), `"queued":2`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "POST", "/api/papers/p1/prefetch", strings.NewReader(`{`), "application/json"); rec.Code != 400 {
		t.Fatalf("bad json: %d", rec.Code)
	}
}

func TestStreamLive(t *testing.T) {
	h, _, pages, _ := newTestRouter()
	ch := make(chan jobs.Event, 4)
	ch <- jobs.Event{Name: "block", Data: domain.BlockTranslation{BlockID: "b0", Ko: "안녕"}}
	ch <- jobs.Event{Name: "done", Data: struct{}{}}
	ch <- jobs.Event{Name: "never", Data: 1}
	pages.stream = service.Stream{Replay: []jobs.Event{{Name: "status", Data: map[string]string{"status": "running"}}}, Events: ch, Cancel: func() {}}
	rec := do(h, "GET", "/api/papers/p1/pages/1/stream", nil, "")
	want := "event: status\ndata: {\"status\":\"running\"}\n\n" +
		"event: block\ndata: {\"block_id\":\"b0\",\"ko\":\"안녕\"}\n\n" +
		"event: done\ndata: {}\n\n"
	if rec.Header().Get("Content-Type") != "text/event-stream" || rec.Body.String() != want {
		t.Fatalf("got %q", rec.Body.String())
	}
}

func TestStreamFinal(t *testing.T) {
	h, _, pages, _ := newTestRouter()
	pages.stream = service.Stream{Cancel: func() {}, Final: &jobs.Event{Name: "done", Data: struct{}{}}}
	rec := do(h, "GET", "/api/papers/p1/pages/1/stream", nil, "")
	if rec.Body.String() != "event: done\ndata: {}\n\n" {
		t.Fatalf("got %q", rec.Body.String())
	}
}

func TestStreamStopsOnTerminalReplay(t *testing.T) {
	h, _, pages, _ := newTestRouter()
	pages.stream = service.Stream{Replay: []jobs.Event{{Name: "error", Data: map[string]string{"message": "x"}}}, Events: make(chan jobs.Event), Cancel: func() {}}
	rec := do(h, "GET", "/api/papers/p1/pages/1/stream", nil, "") // would hang if it waited on Events
	if !strings.HasPrefix(rec.Body.String(), "event: error") {
		t.Fatalf("got %q", rec.Body.String())
	}
}

func TestAskSSE(t *testing.T) {
	h, _, _, ask := newTestRouter()
	rec := do(h, "POST", "/api/papers/p1/pages/1/threads", strings.NewReader(`{"anchor":{"block_ids":["b0"],"quote":"Hi"},"question":"뭐야?"}`), "application/json")
	body := rec.Body.String()
	if !strings.Contains(body, "event: delta\ndata: {\"text\":\"답\"}\n\n") || !strings.Contains(body, "event: done\ndata: {\"id\":\"t1\"") {
		t.Fatalf("got %q", body)
	}
	if rec := do(h, "POST", "/api/papers/p1/pages/1/threads", strings.NewReader(`{"question":"  "}`), "application/json"); rec.Code != 400 {
		t.Fatalf("empty question: %d", rec.Code)
	}
	ask.err = errors.New("claude: usage limit")
	rec = do(h, "POST", "/api/papers/p1/pages/1/threads", strings.NewReader(`{"question":"q"}`), "application/json")
	if !strings.Contains(rec.Body.String(), "event: error\ndata: {\"message\":\"claude: usage limit\"}") {
		t.Fatalf("got %q", rec.Body.String())
	}
	rec = do(h, "POST", "/api/threads/t1/messages", strings.NewReader(`{"question":"더"}`), "application/json")
	if !strings.Contains(rec.Body.String(), "event: delta") {
		t.Fatalf("continue: %q", rec.Body.String())
	}
}

func TestListThreadsNeverNull(t *testing.T) {
	h, _, _, _ := newTestRouter()
	rec := do(h, "GET", "/api/papers/p1/pages/1/threads", nil, "")
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("got %q", rec.Body.String())
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd backend && go test ./internal/handler/`
Expected: FAIL — `undefined: NewRouter`

- [ ] **Step 3: Implement respond.go**

`backend/internal/handler/respond.go`:

```go
package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"

	"github.com/choizeus02/paper-reader/backend/internal/domain"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write json: %v", err)
	}
}

func writeError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, domain.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, domain.ErrInvalid):
		status = http.StatusBadRequest
	}
	if status == http.StatusInternalServerError {
		log.Printf("internal error: %v", err)
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func invalid(msg string) error { return fmt.Errorf("%w: %s", domain.ErrInvalid, msg) }

func pageParams(r *http.Request) (string, int, error) {
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n < 1 {
		return "", 0, invalid("잘못된 페이지 번호")
	}
	return r.PathValue("id"), n, nil
}

type sseWriter struct {
	w http.ResponseWriter
	f http.Flusher
}

func startSSE(w http.ResponseWriter) (*sseWriter, error) {
	f, ok := w.(http.Flusher)
	if !ok {
		return nil, errors.New("streaming unsupported")
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	f.Flush()
	return &sseWriter{w: w, f: f}, nil
}

func (s *sseWriter) send(event string, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", event, b); err != nil {
		return err
	}
	s.f.Flush()
	return nil
}
```

- [ ] **Step 4: Implement dto.go and router.go**

`backend/internal/handler/dto.go`:

```go
package handler

import (
	"fmt"

	"github.com/choizeus02/paper-reader/backend/internal/domain"
	"github.com/choizeus02/paper-reader/backend/internal/service"
)

type pageResponse struct {
	PaperID   string             `json:"paper_id"`
	PageNo    int                `json:"page_no"`
	PageCount int                `json:"page_count"`
	Width     float64            `json:"width"`
	Height    float64            `json:"height"`
	ImageURL  string             `json:"image_url"`
	Blocks    []domain.Block     `json:"blocks"`
	Result    *domain.PageResult `json:"result"`
}

func newPageResponse(v service.PageView) pageResponse {
	blocks := v.Page.Blocks
	if blocks == nil {
		blocks = []domain.Block{}
	}
	return pageResponse{
		PaperID: v.Page.PaperID, PageNo: v.Page.PageNo, PageCount: v.PageCount,
		Width: v.Page.Width, Height: v.Page.Height,
		ImageURL: fmt.Sprintf("/api/papers/%s/pages/%d/image", v.Page.PaperID, v.Page.PageNo),
		Blocks:   blocks, Result: v.Result,
	}
}

type prefetchRequest struct {
	From  int `json:"from"`
	Count int `json:"count"`
}

type questionRequest struct {
	Anchor   domain.Anchor `json:"anchor"`
	Question string        `json:"question"`
}
```

`backend/internal/handler/router.go`:

```go
// Package handler exposes the services over HTTP (JSON + SSE).
package handler

import (
	"context"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/choizeus02/paper-reader/backend/internal/domain"
	"github.com/choizeus02/paper-reader/backend/internal/service"
)

type PaperAPI interface {
	Upload(ctx context.Context, filename string, r io.Reader) (domain.Paper, error)
	List(ctx context.Context) ([]domain.Paper, error)
	Get(ctx context.Context, id string) (domain.Paper, error)
	Delete(ctx context.Context, id string) error
}

type PageAPI interface {
	GetPage(ctx context.Context, paperID string, pageNo int) (service.PageView, error)
	ImageFile(ctx context.Context, paperID string, pageNo int) (string, error)
	Generate(ctx context.Context, paperID string, pageNo int, force bool) (domain.ResultStatus, error)
	Prefetch(ctx context.Context, paperID string, from, count int) (int, error)
	Stream(ctx context.Context, paperID string, pageNo int) (service.Stream, error)
}

type AskAPI interface {
	List(ctx context.Context, paperID string, pageNo int) ([]domain.Thread, error)
	Start(ctx context.Context, paperID string, pageNo int, anchor domain.Anchor, question string, onDelta func(string)) (domain.Thread, error)
	Continue(ctx context.Context, threadID, question string, onDelta func(string)) (domain.Thread, error)
}

type api struct {
	papers PaperAPI
	pages  PageAPI
	ask    AskAPI
}

func NewRouter(papers PaperAPI, pages PageAPI, ask AskAPI, static http.Handler) http.Handler {
	h := &api{papers: papers, pages: pages, ask: ask}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/papers", h.listPapers)
	mux.HandleFunc("POST /api/papers", h.uploadPaper)
	mux.HandleFunc("GET /api/papers/{id}", h.getPaper)
	mux.HandleFunc("DELETE /api/papers/{id}", h.deletePaper)
	mux.HandleFunc("GET /api/papers/{id}/pages/{n}", h.getPage)
	mux.HandleFunc("GET /api/papers/{id}/pages/{n}/image", h.pageImage)
	mux.HandleFunc("POST /api/papers/{id}/pages/{n}/generate", h.generate)
	mux.HandleFunc("GET /api/papers/{id}/pages/{n}/stream", h.stream)
	mux.HandleFunc("POST /api/papers/{id}/prefetch", h.prefetch)
	mux.HandleFunc("GET /api/papers/{id}/pages/{n}/threads", h.listThreads)
	mux.HandleFunc("POST /api/papers/{id}/pages/{n}/threads", h.startThread)
	mux.HandleFunc("POST /api/threads/{tid}/messages", h.continueThread)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	})
	if static != nil {
		mux.Handle("/", static)
	}
	return logRequests(mux)
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Flush keeps SSE working through the logging wrapper.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Printf("%s %s %d %s", r.Method, r.URL.Path, rec.status, time.Since(start).Round(time.Millisecond))
	})
}
```

- [ ] **Step 5: Implement papers.go**

`backend/internal/handler/papers.go`:

```go
package handler

import "net/http"

const maxUpload = 200 << 20

func (h *api) listPapers(w http.ResponseWriter, r *http.Request) {
	papers, err := h.papers.List(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, papers)
}

func (h *api) uploadPaper(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload)
	file, hdr, err := r.FormFile("file")
	if err != nil {
		writeError(w, invalid("file 필드에 PDF를 담아 보내세요"))
		return
	}
	defer file.Close()
	p, err := h.papers.Upload(r.Context(), hdr.Filename, file)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

func (h *api) getPaper(w http.ResponseWriter, r *http.Request) {
	p, err := h.papers.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (h *api) deletePaper(w http.ResponseWriter, r *http.Request) {
	if err := h.papers.Delete(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
```

- [ ] **Step 6: Implement pages.go**

`backend/internal/handler/pages.go`:

```go
package handler

import (
	"encoding/json"
	"net/http"
)

func (h *api) getPage(w http.ResponseWriter, r *http.Request) {
	id, n, err := pageParams(r)
	if err != nil {
		writeError(w, err)
		return
	}
	v, err := h.pages.GetPage(r.Context(), id, n)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newPageResponse(v))
}

func (h *api) pageImage(w http.ResponseWriter, r *http.Request) {
	id, n, err := pageParams(r)
	if err != nil {
		writeError(w, err)
		return
	}
	path, err := h.pages.ImageFile(r.Context(), id, n)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeFile(w, r, path)
}

func (h *api) generate(w http.ResponseWriter, r *http.Request) {
	id, n, err := pageParams(r)
	if err != nil {
		writeError(w, err)
		return
	}
	st, err := h.pages.Generate(r.Context(), id, n, r.URL.Query().Get("force") == "1")
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": string(st)})
}

func (h *api) prefetch(w http.ResponseWriter, r *http.Request) {
	var req prefetchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, invalid("잘못된 요청 본문"))
		return
	}
	n, err := h.pages.Prefetch(r.Context(), r.PathValue("id"), req.From, req.Count)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]int{"queued": n})
}

func isTerminal(name string) bool { return name == "done" || name == "error" }

func (h *api) stream(w http.ResponseWriter, r *http.Request) {
	id, n, err := pageParams(r)
	if err != nil {
		writeError(w, err)
		return
	}
	st, err := h.pages.Stream(r.Context(), id, n)
	if err != nil {
		writeError(w, err)
		return
	}
	defer st.Cancel()
	sse, err := startSSE(w)
	if err != nil {
		writeError(w, err)
		return
	}
	for _, e := range st.Replay {
		if sse.send(e.Name, e.Data) != nil || isTerminal(e.Name) {
			return
		}
	}
	if st.Final != nil {
		_ = sse.send(st.Final.Name, st.Final.Data)
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case e, ok := <-st.Events:
			if !ok {
				return // dropped or reopened: the client reconnects
			}
			if sse.send(e.Name, e.Data) != nil || isTerminal(e.Name) {
				return
			}
		}
	}
}
```

- [ ] **Step 7: Implement ask.go**

`backend/internal/handler/ask.go`:

```go
package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/choizeus02/paper-reader/backend/internal/domain"
)

func (h *api) listThreads(w http.ResponseWriter, r *http.Request) {
	id, n, err := pageParams(r)
	if err != nil {
		writeError(w, err)
		return
	}
	threads, err := h.ask.List(r.Context(), id, n)
	if err != nil {
		writeError(w, err)
		return
	}
	if threads == nil {
		threads = []domain.Thread{}
	}
	writeJSON(w, http.StatusOK, threads)
}

func decodeQuestion(r *http.Request) (questionRequest, error) {
	var req questionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return req, invalid("잘못된 요청 본문")
	}
	if strings.TrimSpace(req.Question) == "" {
		return req, invalid("질문을 입력하세요")
	}
	return req, nil
}

// streamAnswer runs fn while forwarding deltas as SSE, then sends done/error.
func streamAnswer(ctx context.Context, w http.ResponseWriter, fn func(onDelta func(string)) (domain.Thread, error)) {
	sse, err := startSSE(w)
	if err != nil {
		writeError(w, err)
		return
	}
	thread, err := fn(func(d string) { _ = sse.send("delta", map[string]string{"text": d}) })
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		_ = sse.send("error", map[string]string{"message": err.Error()})
		return
	}
	_ = sse.send("done", thread)
}

func (h *api) startThread(w http.ResponseWriter, r *http.Request) {
	id, n, err := pageParams(r)
	if err != nil {
		writeError(w, err)
		return
	}
	req, err := decodeQuestion(r)
	if err != nil {
		writeError(w, err)
		return
	}
	streamAnswer(r.Context(), w, func(onDelta func(string)) (domain.Thread, error) {
		return h.ask.Start(r.Context(), id, n, req.Anchor, req.Question, onDelta)
	})
}

func (h *api) continueThread(w http.ResponseWriter, r *http.Request) {
	req, err := decodeQuestion(r)
	if err != nil {
		writeError(w, err)
		return
	}
	streamAnswer(r.Context(), w, func(onDelta func(string)) (domain.Thread, error) {
		return h.ask.Continue(r.Context(), r.PathValue("tid"), req.Question, onDelta)
	})
}
```

- [ ] **Step 8: Run tests**

Run: `cd backend && go test -race ./internal/handler/ -v`
Expected: all 12 handler tests PASS.

- [ ] **Step 9: Commit**

```bash
git add backend/internal/handler
git commit -m "feat(handler): JSON API and SSE endpoints"
```

---

### Task 12: Server wiring and end-to-end smoke test

**Files:**
- Create: `backend/cmd/server/main.go`
- Create: `backend/internal/service/compile_test.go`

**Interfaces:**
- Consumes: everything above
- Produces: runnable server (`make dev`)

- [ ] **Step 1: Write a compile-time check that real implementations satisfy the ports**

`backend/internal/service/compile_test.go`:

```go
package service_test

import (
	"github.com/choizeus02/paper-reader/backend/internal/handler"
	"github.com/choizeus02/paper-reader/backend/internal/jobs"
	"github.com/choizeus02/paper-reader/backend/internal/llm"
	"github.com/choizeus02/paper-reader/backend/internal/pdf"
	"github.com/choizeus02/paper-reader/backend/internal/service"
	"github.com/choizeus02/paper-reader/backend/internal/store"
)

var (
	_ service.PaperRepo    = (*store.Store)(nil)
	_ service.PageRepo     = (*store.Store)(nil)
	_ service.ResultRepo   = (*store.Store)(nil)
	_ service.GlossaryRepo = (*store.Store)(nil)
	_ service.ThreadRepo   = (*store.Store)(nil)
	_ service.PDFProcessor = pdf.Extractor{}
	_ service.LLM          = llm.Claude{}
	_ service.JobQueue     = (*jobs.Queue)(nil)
	_ service.EventHub     = (*jobs.Hub)(nil)
	_ jobs.Handler         = (*service.PageService)(nil).RunJob

	_ handler.PaperAPI = (*service.PaperService)(nil)
	_ handler.PageAPI  = (*service.PageService)(nil)
	_ handler.AskAPI   = (*service.AskService)(nil)
)
```

Run: `cd backend && go vet ./internal/service/`
Expected: no output (if a signature drifted, this fails to compile and names the method).

- [ ] **Step 2: Write main.go**

`backend/cmd/server/main.go`:

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/choizeus02/paper-reader/backend/internal/config"
	"github.com/choizeus02/paper-reader/backend/internal/handler"
	"github.com/choizeus02/paper-reader/backend/internal/jobs"
	"github.com/choizeus02/paper-reader/backend/internal/llm"
	"github.com/choizeus02/paper-reader/backend/internal/pdf"
	"github.com/choizeus02/paper-reader/backend/internal/service"
	"github.com/choizeus02/paper-reader/backend/internal/store"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return fmt.Errorf("DATA_DIR: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.DatabaseURL, "")
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	workDir, err := os.MkdirTemp("", "paper-reader-claude-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(workDir)

	runner := llm.Claude{Bin: cfg.ClaudeBin, Model: cfg.ClaudeModel, WorkDir: workDir}
	hub := jobs.NewHub()
	queue := jobs.NewQueue(cfg.Workers, cfg.JobTimeout)

	papers := service.NewPaperService(st, pdf.Extractor{Bin: cfg.MutoolBin, DPI: cfg.RenderDPI}, cfg.DataDir)
	pages := service.NewPageService(service.PageDeps{
		Papers: st, Pages: st, Results: st, Glossary: st,
		LLM: runner, Queue: queue, Hub: hub, DataDir: cfg.DataDir,
	})
	ask := service.NewAskService(service.AskDeps{
		Papers: st, Pages: st, Results: st, Threads: st, LLM: runner, DataDir: cfg.DataDir,
	})
	if err := pages.Recover(ctx); err != nil {
		return fmt.Errorf("recover: %w", err)
	}
	queue.Start(ctx, pages.RunJob)

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler.NewRouter(papers, pages, ask, nil),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			srv.Close() // open SSE streams
		}
	}()

	log.Printf("paper-reader listening on %s (data: %s, workers: %d)", cfg.ListenAddr, cfg.DataDir, cfg.Workers)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	queue.Wait()
	return nil
}
```

- [ ] **Step 3: Build and run the full test suite**

Run: `cd backend && go build ./... && cd .. && make test`
Expected: build succeeds; every package PASS (store tests run against the test DB).

- [ ] **Step 4: Connect Infisical (one-time, done by the user)**

The user runs in the repo root (interactive):

```bash
infisical login --domain https://<infisical-host>
infisical init        # pick the paper-reader project; writes .infisical.yaml
```

Make sure the `dev` environment has `DATABASE_URL` pointing at a `paper_reader` database on the TrueNAS Postgres. Then commit the project link:

```bash
git add .infisical.yaml && git commit -m "chore: link Infisical project"
```

- [ ] **Step 5: End-to-end smoke test** (uses a few pages of subscription quota)

Terminal A: `make dev`
Expected log: `paper-reader listening on :8080 ...`

Terminal B:

```bash
mkdir -p /tmp/pr-smoke && curl -sL -o /tmp/pr-smoke/attn.pdf https://arxiv.org/pdf/1706.03762
ID=$(curl -s -F file=@/tmp/pr-smoke/attn.pdf localhost:8080/api/papers | jq -r .id); echo $ID
sleep 5; curl -s localhost:8080/api/papers/$ID | jq '{status, page_count}'
```

Expected: `{"status":"ready","page_count":15}`

```bash
curl -s localhost:8080/api/papers/$ID/pages/3 | jq '{page_count, blocks: (.blocks|length), result}'
curl -s -XPOST localhost:8080/api/papers/$ID/pages/3/generate
curl -sN localhost:8080/api/papers/$ID/pages/3/stream
```

Expected: ~20 blocks, `result: null`; generate → `{"status":"pending"}`; the stream prints `event: status`, a series of `event: block` with Korean text, `event: explanation` lines, and ends with `event: done`.

```bash
curl -s localhost:8080/api/papers/$ID/pages/3 | jq '.result | {status, n: (.translation|length), summary: .page_summary}'
curl -sN -XPOST localhost:8080/api/papers/$ID/pages/3/threads -H 'content-type: application/json' \
  -d '{"anchor":{"block_ids":["b3"],"quote":""},"question":"인코더 스택이 6개인 이유가 있어?"}'
curl -s -XPOST localhost:8080/api/papers/$ID/prefetch -H 'content-type: application/json' -d '{"from":4,"count":2}'
```

Expected: `status: "done"` with translations and a Korean summary; the thread stream shows `event: delta` chunks then `event: done` with the thread JSON; prefetch → `{"queued":2}` and the server log shows pages 4 and 5 being processed.

Restart check: start a generate on page 6, stop the server with Ctrl+C while it runs, start it again, then `curl -s localhost:8080/api/papers/$ID/pages/6 | jq .result` → `status: "failed"`, `error: "서버 재시작으로 중단됨"`.

- [ ] **Step 6: Commit**

```bash
git add backend/cmd backend/internal/service/compile_test.go
git commit -m "feat(server): wire services and start HTTP server"
git push
```
