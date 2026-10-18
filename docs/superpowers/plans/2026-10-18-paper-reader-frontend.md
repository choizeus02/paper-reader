# Paper Reader Frontend Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the React UI of Paper Reader — a library page (upload, list, delete) and a split-view reader (page image with block overlay, streamed Korean translation, explanation with math, page-anchored Q&A) — and serve it from the Go binary via `embed`.

**Architecture:** A Vite + React + TypeScript SPA in `web/`. All server I/O lives in three framework-free modules that are unit-tested with vitest (`api/client.ts` for JSON, `api/sse.ts` + `api/ask.ts` for POST-streamed answers, `lib/pageStream.ts` for the page-generation lifecycle over `EventSource`); React components are thin and checked manually. `vite build` writes into `backend/internal/webui/dist`, which a new Go package `webui` embeds and serves with SPA fallback.

**Tech Stack:** Node ≥ 22.22 (verified with 24.22.0), React 19.3.1, react-router 8.4.0, Vite 8.3.4, @vitejs/plugin-react 6.1.2, TypeScript 7.0.2, vitest 5.0.5, react-markdown 10.1.0 + remark-math 6.0.0 + rehype-katex 7.0.1 + katex 0.16.47; Go 1.27 (`embed`, `http.FileServerFS`).

**Spec:** `docs/superpowers/specs/2026-10-16-paper-reader-design.md` (sections 8, 9.4, 11, 12 are the frontend contract).

## Global Constraints

- Node ≥ 22.22 (`web/.nvmrc` = `24`). Exact dependency versions as listed in Task 1's `package.json`; no other runtime dependencies (no state library, no CSS framework, no HTTP library).
- All user-visible text is Korean.
- The API is same-origin under `/api`. In development Vite (port 5173) proxies `/api` to `http://localhost:8080`.
- Build output goes to `backend/internal/webui/dist` via `make web-build`; only `backend/internal/webui/dist/.gitkeep` is committed.
- Page stream (spec §9.4): events `status{status}`, `block{block_id,ko}`, `explanation{delta}`, `done{}`, `error{message}`.
  - `status` `retrying` → clear accumulated blocks/explanation.
  - Every (re)open of the EventSource → clear accumulated state (the server replays from scratch).
  - Close the EventSource on `done`/`error`; after `done`, re-`GET` the page.
  - A native EventSource `error` (no `data`) while `readyState !== CLOSED` is a reconnect, not a failure.
- Opening a page: result `done` → show it; result `failed` → show the error with [재시도] (no automatic regeneration, so a usage-limit failure does not burn quota on every visit; spec §12/§13); result missing/`pending`/`running` → `POST generate` then subscribe. [재시도] = `generate`; [다시 생성] = `generate?force=1`.
- Ask (spec §9.4 f): `POST` + fetch streaming, events `delta{text}`, `done{thread}`, `error{message}`; NotFound/Invalid arrive as 4xx JSON `{error}` before the stream.
- Error JSON is `{"error": "..."}`; show `error` verbatim; fall back to `요청 실패 (<status>)`; network failure → `서버에 연결할 수 없습니다`.
- Prefetch button queues the next `3` pages (`PREFETCH_COUNT`), clamped to the page count.
- Narrow layout `< 900px` (`(max-width: 899px)`): one tab bar with 원문/번역/해설/질문.
- Tests: vitest in `node` environment for `src/**/*.test.ts` only (spec §14); screens are verified manually.

## Review Focus

1. **Stream ends without a terminal event, browser reconnects, server replays** — the reader must not show duplicated translation/explanation. Pinned in Task 5 (`reconnect replay does not duplicate output`).
2. **Navigating to another page while a stream is live** — events from the old page must never land on the new page. Pinned in Task 5 (`stop ignores late events`).
3. **Ask stream chunks split mid-event, mid-line, or mid-UTF-8 character, and CRLF line endings** — the answer text must be reconstructed exactly. Pinned in Task 4 (`parser is chunk-boundary independent`, `crlf and lone cr`, `korean split across byte chunks`).
4. **Ask failures** — 4xx JSON before the stream, `event: error` mid-stream, and a stream that just stops must each surface a Korean message, never hang. Pinned in Task 4 (`4xx json becomes ApiError`, `error event rejects with its message`, `truncated stream rejects`).
5. **Arrow keys while typing** in the page-number box or the question box must not change the page; ← on page 1 and → on the last page do nothing. Pinned in Task 7 (`navTarget` tests).

---

## File Map

```
Makefile                                   web-install/web-dev/web-test/web-build/build/run (Tasks 1, 2, 9)
.gitignore                                 ignore webui/dist except .gitkeep (Task 1)
README.md                                  how to develop/build/run (Task 9)
web/.nvmrc, package.json, package-lock.json, tsconfig.json, vite.config.ts, index.html (Task 1)
web/src/main.tsx                           entry, global CSS (Task 1)
web/src/index.css                          all styles (Task 1)
web/src/App.tsx                            routes (Task 1 stub → Task 6 → Task 7)
web/src/api/types.ts                       JSON shapes (Task 1)
web/src/api/client.ts (+ .test.ts)         JSON API + ApiError (Task 3)
web/src/api/sse.ts (+ .test.ts)            SSE parser + postStream (Task 4)
web/src/api/ask.ts (+ .test.ts)            askStart/askContinue (Task 4)
web/src/lib/pageStream.ts (+ .test.ts)     genReducer + PageGeneration controller (Task 5)
web/src/lib/nav.ts (+ .test.ts)            keyboard/page-number/prefetch helpers (Task 7)
web/src/lib/anchor.ts (+ .test.ts)         question anchors (Task 8)
web/src/hooks/usePageGeneration.ts         React wrapper over PageGeneration (Task 7)
web/src/hooks/useMediaQuery.ts             (Task 7)
web/src/pages/Library.tsx                  (Task 6)
web/src/pages/Reader.tsx                   (Task 7, Task 8)
web/src/components/Toolbar.tsx, StatusBar.tsx, PageImage.tsx, TranslationPanel.tsx,
                   ExplanationPanel.tsx, MarkdownView.tsx (Task 7; PageImage/TranslationPanel replaced in Task 8)
web/src/components/AskPanel.tsx            (Task 8)
backend/internal/webui/webui.go (+ _test)  embed + SPA handler (Task 2)
backend/internal/webui/dist/.gitkeep       (Task 1)
backend/cmd/server/main.go                 pass webui handler to NewRouter (Task 2)
```

---

### Task 1: Scaffold the web app

**Files:**
- Create: `web/.nvmrc`, `web/package.json`, `web/tsconfig.json`, `web/vite.config.ts`, `web/index.html`, `web/src/main.tsx`, `web/src/App.tsx`, `web/src/index.css`, `web/src/api/types.ts`, `backend/internal/webui/dist/.gitkeep`
- Generate: `web/package-lock.json` (via `npm install`)
- Modify: `Makefile`, `.gitignore`

**Interfaces:**
- Consumes: backend JSON shapes (`backend/internal/domain/domain.go`, `backend/internal/handler/dto.go`).
- Produces: `web/src/api/types.ts` exports `PaperStatus`, `Paper`, `BBox`, `Block`, `ResultStatus`, `BlockTranslation`, `PageResult`, `PageView`, `Anchor`, `MessageRole`, `MessageStatus`, `Message`, `Thread` (exact fields below); make targets `web-install`, `web-dev`, `web-test`, `web-build`.

- [ ] **Step 1: Create `web/.nvmrc`**

```
24
```

- [ ] **Step 2: Create `web/package.json`**

```json
{
  "name": "paper-reader-web",
  "private": true,
  "version": "0.0.0",
  "type": "module",
  "engines": {
    "node": ">=22.22"
  },
  "scripts": {
    "dev": "vite",
    "build": "tsc --noEmit -p . && vite build",
    "typecheck": "tsc --noEmit -p .",
    "test": "vitest run"
  },
  "dependencies": {
    "katex": "0.16.47",
    "react": "19.3.1",
    "react-dom": "19.3.1",
    "react-markdown": "10.1.0",
    "react-router": "8.4.0",
    "rehype-katex": "7.0.1",
    "remark-math": "6.0.0"
  },
  "devDependencies": {
    "@types/react": "19.3.0",
    "@types/react-dom": "19.3.0",
    "@vitejs/plugin-react": "6.1.2",
    "typescript": "7.0.2",
    "vite": "8.3.4",
    "vitest": "5.0.5"
  }
}
```

- [ ] **Step 3: Create `web/tsconfig.json`**

```json
{
  "compilerOptions": {
    "target": "ES2022",
    "lib": ["ES2022", "DOM", "DOM.Iterable"],
    "module": "ESNext",
    "moduleResolution": "bundler",
    "jsx": "react-jsx",
    "strict": true,
    "noUnusedLocals": true,
    "noUnusedParameters": true,
    "noFallthroughCasesInSwitch": true,
    "skipLibCheck": true,
    "isolatedModules": true,
    "noEmit": true,
    "types": ["vite/client"]
  },
  "include": ["src", "vite.config.ts"]
}
```

- [ ] **Step 4: Create `web/vite.config.ts`**

```ts
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vitest/config'

export default defineConfig({
  plugins: [react()],
  build: {
    outDir: '../backend/internal/webui/dist',
    emptyOutDir: true,
    chunkSizeWarningLimit: 2000,
  },
  server: {
    port: 5173,
    proxy: { '/api': 'http://localhost:8080' },
  },
  test: {
    environment: 'node',
    include: ['src/**/*.test.ts'],
  },
})
```

- [ ] **Step 5: Create `web/index.html`**

```html
<!doctype html>
<html lang="ko">
  <head>
    <meta charset="UTF-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1.0" />
    <title>Paper Reader</title>
  </head>
  <body>
    <div id="root"></div>
    <script type="module" src="/src/main.tsx"></script>
  </body>
</html>
```

- [ ] **Step 6: Create `web/src/main.tsx`**

```tsx
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import 'katex/dist/katex.min.css'
import './index.css'
import App from './App'

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
```

- [ ] **Step 7: Create `web/src/App.tsx`** (replaced in Task 6)

```tsx
export default function App() {
  return (
    <main className="page">
      <h1>Paper Reader</h1>
    </main>
  )
}
```

- [ ] **Step 8: Create `web/src/index.css`**

```css
:root {
  --bg: #f7f7f5;
  --panel: #ffffff;
  --text: #1f2328;
  --muted: #6b7280;
  --border: #e5e7eb;
  --accent: #2563eb;
  --accent-soft: rgba(37, 99, 235, 0.12);
  --danger: #dc2626;
  --page-bg: #8b8f96;
  font-family: -apple-system, BlinkMacSystemFont, "Apple SD Gothic Neo", "Pretendard", "Noto Sans KR", sans-serif;
  color: var(--text);
  background: var(--bg);
}

@media (prefers-color-scheme: dark) {
  :root {
    --bg: #111315;
    --panel: #1a1d21;
    --text: #e6e6e6;
    --muted: #9ca3af;
    --border: #2d3238;
    --accent: #60a5fa;
    --accent-soft: rgba(96, 165, 250, 0.18);
    --page-bg: #2a2d31;
  }
}

* { box-sizing: border-box; }
body { margin: 0; }
a { color: var(--accent); }

button {
  font: inherit;
  cursor: pointer;
  color: var(--text);
  background: var(--panel);
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 4px 10px;
}
button:disabled { opacity: 0.5; cursor: default; }
button.primary { background: var(--accent); border-color: var(--accent); color: #fff; }
button.danger { color: var(--danger); }

input, textarea {
  font: inherit;
  color: var(--text);
  background: var(--panel);
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 4px 8px;
}

.page { max-width: 960px; margin: 0 auto; padding: 24px 16px; }
.muted { color: var(--muted); }
.error { color: var(--danger); }

/* Library */
.dropzone { border: 2px dashed var(--border); border-radius: 10px; padding: 28px; text-align: center; margin-bottom: 20px; }
.dropzone.over { border-color: var(--accent); background: var(--accent-soft); }
.paper-list { list-style: none; padding: 0; margin: 0; display: grid; gap: 10px; }
.paper-card { background: var(--panel); border: 1px solid var(--border); border-radius: 10px; padding: 12px 14px; display: flex; gap: 8px; align-items: center; }
.paper-card .info { flex: 1; min-width: 0; }
.paper-card .title { display: block; font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

/* Reader */
.reader { display: flex; flex-direction: column; height: 100vh; }
.toolbar { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; padding: 8px 12px; border-bottom: 1px solid var(--border); background: var(--panel); }
.toolbar .title { flex: 1; min-width: 120px; font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.toolbar .page-input { width: 56px; text-align: right; }
.status-bar { display: flex; gap: 8px; align-items: center; padding: 6px 16px; border-bottom: 1px solid var(--border); font-size: 14px; }
.split { flex: 1; min-height: 0; display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); }
.pane { min-height: 0; overflow: auto; }
.pane.left, .tab-body.source { background: var(--page-bg); padding: 12px; }
.page-image { position: relative; max-width: 900px; margin: 0 auto; background: #fff; box-shadow: 0 1px 4px rgba(0, 0, 0, 0.3); }
.page-image img { display: block; width: 100%; }
.bbox { position: absolute; border: 1px solid transparent; border-radius: 2px; cursor: pointer; }
.bbox:hover, .bbox.hot { border-color: var(--accent); background: var(--accent-soft); }
.tabs { position: sticky; top: 0; z-index: 1; display: flex; gap: 4px; padding: 8px 12px 0; border-bottom: 1px solid var(--border); background: var(--panel); }
.tabs button { border-radius: 6px 6px 0 0; }
.tabs button.active { background: var(--accent-soft); border-color: var(--accent); }
.tab-body { padding: 12px 16px 40px; }
.tblock { padding: 6px 8px; margin-bottom: 6px; border-radius: 6px; line-height: 1.7; }
.tblock.hot { background: var(--accent-soft); }
.tblock.pending { color: var(--muted); }
.markdown { line-height: 1.75; }
.markdown pre { overflow-x: auto; }

/* Ask */
.thread { margin-bottom: 12px; padding: 10px; border: 1px solid var(--border); border-radius: 8px; background: var(--panel); }
.quote { padding-left: 8px; border-left: 3px solid var(--accent); color: var(--muted); font-size: 14px; white-space: pre-wrap; }
.msg { margin: 8px 0; line-height: 1.6; }
.msg.user { font-weight: 600; white-space: pre-wrap; }
.composer { display: grid; gap: 6px; margin-top: 8px; }
.composer textarea { min-height: 70px; resize: vertical; }
.ask-fab { position: sticky; top: 44px; z-index: 2; margin-bottom: 8px; }

@media (max-width: 899px) {
  .split { grid-template-columns: minmax(0, 1fr); }
}
```

- [ ] **Step 9: Create `web/src/api/types.ts`**

```ts
// JSON shapes returned by the Go API (backend/internal/domain, handler/dto.go).

export type PaperStatus = 'processing' | 'ready' | 'failed'

export interface Paper {
  id: string
  title: string
  filename: string
  page_count: number
  status: PaperStatus
  error?: string
  done_pages: number
  created_at: string
}

export interface BBox {
  x: number
  y: number
  w: number
  h: number
}

export interface Block {
  id: string
  bbox: BBox
  text: string
}

export type ResultStatus = 'pending' | 'running' | 'done' | 'failed'

export interface BlockTranslation {
  block_id: string
  ko: string
}

export interface PageResult {
  status: ResultStatus
  translation: BlockTranslation[]
  explanation: string
  page_summary: string
  prompt_version: number
  error?: string
  updated_at: string
}

export interface PageView {
  paper_id: string
  page_no: number
  page_count: number
  width: number
  height: number
  image_url: string
  blocks: Block[]
  result: PageResult | null
}

export interface Anchor {
  block_ids: string[]
  quote: string
}

export type MessageRole = 'user' | 'assistant'
export type MessageStatus = 'done' | 'streaming' | 'failed'

export interface Message {
  id: number
  role: MessageRole
  content: string
  status: MessageStatus
  created_at: string
}

export interface Thread {
  id: string
  paper_id: string
  page_no: number
  anchor: Anchor
  created_at: string
  messages: Message[]
}
```

- [ ] **Step 10: Ignore build output, keep the embed directory**

Append to `.gitignore`:

```
backend/internal/webui/dist/*
!backend/internal/webui/dist/.gitkeep
```

Create the empty file `backend/internal/webui/dist/.gitkeep`.

- [ ] **Step 11: Add make targets**

Add to the `.PHONY` line of `Makefile`: `web-install web-dev web-test web-build`. Append:

```make
web-install:
	npm --prefix web install

web-dev:
	npm --prefix web run dev

web-test:
	npm --prefix web test

# vite empties dist/; restore the committed placeholder so git stays clean.
web-build:
	npm --prefix web run build
	touch backend/internal/webui/dist/.gitkeep
```

- [ ] **Step 12: Install and build**

Run: `node -v` → must be ≥ v22.22 (if not: `source ~/.nvm/nvm.sh && nvm use 24`).
Run: `make web-install` → creates `web/package-lock.json`, no errors.
Run: `make web-build`
Expected: `tsc` passes, vite prints `✓ built`, `backend/internal/webui/dist/index.html` exists, and `git status --short backend/internal/webui` shows nothing except the new `.gitkeep`.

- [ ] **Step 13: Commit**

```bash
git add web/.nvmrc web/package.json web/package-lock.json web/tsconfig.json web/vite.config.ts web/index.html web/src Makefile .gitignore backend/internal/webui/dist/.gitkeep
git commit -m "feat(web): scaffold Vite React app"
```

---

### Task 2: Embed and serve the SPA from Go

**Files:**
- Create: `backend/internal/webui/webui.go`, `backend/internal/webui/webui_test.go`
- Modify: `backend/cmd/server/main.go` (the `handler.NewRouter(papers, pages, ask, nil)` call), `Makefile`

**Interfaces:**
- Consumes: `handler.NewRouter(papers PaperAPI, pages PageAPI, ask AskAPI, static http.Handler) http.Handler` (existing; mounts `static` at `/`, `/api/*` takes precedence); `backend/internal/webui/dist/` from Task 1.
- Produces: `webui.Dist() fs.FS`, `webui.Handler(fsys fs.FS) http.Handler`; make targets `build`, `run`.

- [ ] **Step 1: Write the failing tests**

`backend/internal/webui/webui_test.go`:

```go
package webui

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/choizeus02/paper-reader/backend/internal/handler"
)

func testFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":        {Data: []byte("<html>app</html>")},
		"assets/app-123.js": {Data: []byte("console.log(1)")},
		"favicon.svg":       {Data: []byte("<svg/>")},
	}
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestServesIndexAtRoot(t *testing.T) {
	rec := get(t, Handler(testFS()), "/")
	if rec.Code != 200 || rec.Body.String() != "<html>app</html>" {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content-type %q", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("cache-control %q", cc)
	}
}

func TestClientRoutesFallBackToIndex(t *testing.T) {
	for _, p := range []string{"/papers/abc/3", "/papers/abc", "/index.html"} {
		rec := get(t, Handler(testFS()), p)
		if rec.Code != 200 || rec.Body.String() != "<html>app</html>" {
			t.Fatalf("%s: got %d %q", p, rec.Code, rec.Body.String())
		}
	}
}

func TestServesAssetsWithLongCache(t *testing.T) {
	rec := get(t, Handler(testFS()), "/assets/app-123.js")
	if rec.Code != 200 || rec.Body.String() != "console.log(1)" {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Fatalf("cache-control %q", cc)
	}
	if rec := get(t, Handler(testFS()), "/favicon.svg"); rec.Code != 200 {
		t.Fatalf("favicon: %d", rec.Code)
	}
}

func TestMissingAssetIs404(t *testing.T) {
	if rec := get(t, Handler(testFS()), "/assets/gone-999.js"); rec.Code != 404 {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestNotBuiltExplainsItself(t *testing.T) {
	rec := get(t, Handler(fstest.MapFS{}), "/")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "make web-build") {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
}

func TestRouterKeepsAPIAhead(t *testing.T) {
	r := handler.NewRouter(nil, nil, nil, Handler(testFS()))
	rec := get(t, r, "/api/nope")
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "찾을 수 없음") {
		t.Fatalf("api: got %d %q", rec.Code, rec.Body.String())
	}
	if rec := get(t, r, "/papers/x/1"); rec.Code != 200 || rec.Body.String() != "<html>app</html>" {
		t.Fatalf("spa: got %d %q", rec.Code, rec.Body.String())
	}
}

func TestDistIsReadable(t *testing.T) {
	if _, err := fs.ReadDir(Dist(), "."); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go -C backend test ./internal/webui/`
Expected: FAIL — `undefined: Handler`, `undefined: Dist`.

- [ ] **Step 3: Implement**

`backend/internal/webui/webui.go`:

```go
// Package webui serves the embedded React build as a single-page app.
package webui

import (
	"embed"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var embedded embed.FS

// Dist returns the frontend build produced by `make web-build`.
func Dist() fs.FS {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}

const notBuilt = "프론트엔드가 빌드되지 않았습니다. `make web-build`를 실행하세요.\n"

// Handler serves files from fsys. Unknown paths outside assets/ get
// index.html so client-side routes work on reload.
func Handler(fsys fs.FS) http.Handler {
	files := http.FileServerFS(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name != "" && name != "index.html" {
			if st, err := fs.Stat(fsys, name); err == nil && !st.IsDir() {
				if strings.HasPrefix(name, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
			if strings.HasPrefix(name, "assets/") {
				http.NotFound(w, r)
				return
			}
		}
		index, err := fs.ReadFile(fsys, "index.html")
		if errors.Is(err, fs.ErrNotExist) {
			http.Error(w, notBuilt, http.StatusServiceUnavailable)
			return
		}
		if err != nil {
			log.Printf("webui: %v", err)
			http.Error(w, "서버 오류가 발생했습니다", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(index)
	})
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go -C backend test ./internal/webui/`
Expected: PASS (7 tests).

- [ ] **Step 5: Wire it into the server**

In `backend/cmd/server/main.go` add the import `"github.com/choizeus02/paper-reader/backend/internal/webui"` and change

```go
		Handler:           handler.NewRouter(papers, pages, ask, nil),
```

to

```go
		Handler:           handler.NewRouter(papers, pages, ask, webui.Handler(webui.Dist())),
```

- [ ] **Step 6: Add build/run targets**

Add `build run` to the `.PHONY` line of `Makefile` and append:

```make
build: web-build
	go -C backend build -o bin/paper-reader ./cmd/server

run: build
	infisical run --env=dev -- env DATA_DIR=$(DATA_DIR) ./backend/bin/paper-reader
```

- [ ] **Step 7: Verify the whole build**

Run: `make build && go -C backend vet ./... && go -C backend test ./...`
Expected: binary at `backend/bin/paper-reader`; vet clean; all packages pass.

- [ ] **Step 8: Commit**

```bash
git add backend/internal/webui/webui.go backend/internal/webui/webui_test.go backend/cmd/server/main.go Makefile
git commit -m "feat(webui): embed and serve the SPA"
```

---

### Task 3: JSON API client

**Files:**
- Create: `web/src/api/client.ts`, `web/src/api/client.test.ts`

**Interfaces:**
- Consumes: `web/src/api/types.ts` (Task 1).
- Produces:
  - `class ApiError extends Error { status: number }`
  - `errorFrom(res: Response): Promise<ApiError>`
  - `errorMessage(e: unknown): string`
  - `streamURL(paperId: string, pageNo: number): string` → `/api/papers/{id}/pages/{n}/stream`
  - `api.listPapers(): Promise<Paper[]>`, `api.getPaper(id)`, `api.uploadPaper(file: File): Promise<Paper>`, `api.deletePaper(id): Promise<void>`, `api.getPage(id, n): Promise<PageView>`, `api.generate(id, n, force = false): Promise<{ status: ResultStatus }>`, `api.prefetch(id, from, count): Promise<{ queued: number }>`, `api.listThreads(id, n): Promise<Thread[]>`

- [ ] **Step 1: Write the failing tests**

`web/src/api/client.test.ts`:

```ts
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError, api, errorMessage, streamURL } from './client'

function mockFetch(impl: (url: string, init?: RequestInit) => Promise<Response>) {
  const fn = vi.fn(impl)
  vi.stubGlobal('fetch', fn)
  return fn
}

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

afterEach(() => vi.unstubAllGlobals())

describe('api client', () => {
  it('returns parsed JSON', async () => {
    const f = mockFetch(async () => json([{ id: 'p1' }]))
    expect(await api.listPapers()).toEqual([{ id: 'p1' }])
    expect(f).toHaveBeenCalledWith('/api/papers', undefined)
  })

  it('uses the server error message', async () => {
    mockFetch(async () => json({ error: '찾을 수 없음' }, 404))
    const err = await api.getPaper('x').catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(404)
    expect((err as ApiError).message).toBe('찾을 수 없음')
  })

  it('falls back when the error body is not JSON', async () => {
    mockFetch(async () => new Response('boom', { status: 502 }))
    await expect(api.listPapers()).rejects.toThrow('요청 실패 (502)')
  })

  it('reports network failures in Korean', async () => {
    mockFetch(async () => {
      throw new TypeError('fetch failed')
    })
    const err = (await api.listPapers().catch((e: unknown) => e)) as ApiError
    expect(err.status).toBe(0)
    expect(err.message).toBe('서버에 연결할 수 없습니다')
  })

  it('posts generate with force flag', async () => {
    const f = mockFetch(async () => json({ status: 'pending' }, 202))
    expect(await api.generate('p1', 3, true)).toEqual({ status: 'pending' })
    expect(f.mock.calls[0][0]).toBe('/api/papers/p1/pages/3/generate?force=1')
    expect(f.mock.calls[0][1]?.method).toBe('POST')
    await api.generate('p1', 3)
    expect(f.mock.calls[1][0]).toBe('/api/papers/p1/pages/3/generate')
  })

  it('sends prefetch as JSON', async () => {
    const f = mockFetch(async () => json({ queued: 2 }, 202))
    expect(await api.prefetch('p1', 4, 2)).toEqual({ queued: 2 })
    const init = f.mock.calls[0][1]!
    expect(init.method).toBe('POST')
    expect(JSON.parse(init.body as string)).toEqual({ from: 4, count: 2 })
  })

  it('uploads the file as multipart field "file"', async () => {
    const f = mockFetch(async () => json({ id: 'p9' }, 201))
    const file = new File(['%PDF-1.7'], 'a.pdf', { type: 'application/pdf' })
    expect(await api.uploadPaper(file)).toEqual({ id: 'p9' })
    const body = f.mock.calls[0][1]!.body as FormData
    expect((body.get('file') as File).name).toBe('a.pdf')
  })

  it('treats 204 as empty success', async () => {
    mockFetch(async () => new Response(null, { status: 204 }))
    await expect(api.deletePaper('p1')).resolves.toBeUndefined()
  })

  it('builds the stream URL', () => {
    expect(streamURL('p1', 7)).toBe('/api/papers/p1/pages/7/stream')
  })

  it('formats unknown errors', () => {
    expect(errorMessage(new Error('x'))).toBe('x')
    expect(errorMessage('y')).toBe('y')
  })
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `npm --prefix web test -- src/api/client.test.ts`
Expected: FAIL — cannot resolve `./client`.

- [ ] **Step 3: Implement**

`web/src/api/client.ts`:

```ts
import type { PageView, Paper, ResultStatus, Thread } from './types'

export class ApiError extends Error {
  readonly status: number

  constructor(status: number, message: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
  }
}

/** Builds an ApiError from a non-OK response, using the server's `{error}` text when present. */
export async function errorFrom(res: Response): Promise<ApiError> {
  let message = `요청 실패 (${res.status})`
  try {
    const body: unknown = await res.json()
    if (body && typeof body === 'object' && 'error' in body) {
      const text = (body as { error: unknown }).error
      if (typeof text === 'string' && text) message = text
    }
  } catch {
    // not JSON — keep the generic message
  }
  return new ApiError(res.status, message)
}

export function errorMessage(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  let res: Response
  try {
    res = await fetch(path, init)
  } catch {
    throw new ApiError(0, '서버에 연결할 수 없습니다')
  }
  if (!res.ok) throw await errorFrom(res)
  if (res.status === 204) return undefined as T
  return (await res.json()) as T
}

const postJSON = (body: unknown): RequestInit => ({
  method: 'POST',
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify(body),
})

export const streamURL = (paperId: string, pageNo: number) => `/api/papers/${paperId}/pages/${pageNo}/stream`

export const api = {
  listPapers: () => request<Paper[]>('/api/papers'),
  getPaper: (id: string) => request<Paper>(`/api/papers/${id}`),
  uploadPaper: (file: File) => {
    const form = new FormData()
    form.append('file', file)
    return request<Paper>('/api/papers', { method: 'POST', body: form })
  },
  deletePaper: (id: string) => request<void>(`/api/papers/${id}`, { method: 'DELETE' }),
  getPage: (id: string, n: number) => request<PageView>(`/api/papers/${id}/pages/${n}`),
  generate: (id: string, n: number, force = false) =>
    request<{ status: ResultStatus }>(`/api/papers/${id}/pages/${n}/generate${force ? '?force=1' : ''}`, {
      method: 'POST',
    }),
  prefetch: (id: string, from: number, count: number) =>
    request<{ queued: number }>(`/api/papers/${id}/prefetch`, postJSON({ from, count })),
  listThreads: (id: string, n: number) => request<Thread[]>(`/api/papers/${id}/pages/${n}/threads`),
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `npm --prefix web test -- src/api/client.test.ts && npm --prefix web run typecheck`
Expected: 10 passed; typecheck clean.

- [ ] **Step 5: Commit**

```bash
git add web/src/api/client.ts web/src/api/client.test.ts
git commit -m "feat(web): JSON API client with Korean errors"
```

---

### Task 4: SSE parser and streamed Q&A client

**Files:**
- Create: `web/src/api/sse.ts`, `web/src/api/sse.test.ts`, `web/src/api/ask.ts`, `web/src/api/ask.test.ts`

**Interfaces:**
- Consumes: `ApiError`, `errorFrom` from `./client` (Task 3); `Anchor`, `Thread` from `./types`.
- Produces:
  - `interface SSEEvent { event: string; data: string }`
  - `class SSEParser { feed(text: string): SSEEvent[] }`
  - `postStream(url: string, body: unknown, onEvent: (e: SSEEvent) => void, signal?: AbortSignal): Promise<void>`
  - `askStart(paperId: string, pageNo: number, anchor: Anchor, question: string, onDelta: (text: string) => void, signal?: AbortSignal): Promise<Thread>`
  - `askContinue(threadId: string, question: string, onDelta: (text: string) => void, signal?: AbortSignal): Promise<Thread>`

- [ ] **Step 1: Write the failing parser tests**

`web/src/api/sse.test.ts`:

```ts
import { describe, expect, it } from 'vitest'
import { SSEParser, type SSEEvent } from './sse'

const TWO = 'event: delta\ndata: {"text":"안녕"}\n\nevent: done\ndata: {}\n\n'
const WANT: SSEEvent[] = [
  { event: 'delta', data: '{"text":"안녕"}' },
  { event: 'done', data: '{}' },
]

describe('SSEParser', () => {
  it('parses whole events', () => {
    expect(new SSEParser().feed(TWO)).toEqual(WANT)
  })

  it('parser is chunk-boundary independent', () => {
    for (let i = 0; i <= TWO.length; i++) {
      const p = new SSEParser()
      const got = [...p.feed(TWO.slice(0, i)), ...p.feed(TWO.slice(i))]
      expect(got, `split at ${i}`).toEqual(WANT)
    }
  })

  it('crlf and lone cr', () => {
    expect(new SSEParser().feed('event: a\r\ndata: 1\r\n\r\nevent: b\rdata: 2\r\r\n')).toEqual([
      { event: 'a', data: '1' },
      { event: 'b', data: '2' },
    ])
    const p = new SSEParser()
    expect(p.feed('data: x\r')).toEqual([])
    expect(p.feed('\n\r\n')).toEqual([{ event: 'message', data: 'x' }])
  })

  it('joins multi-line data and ignores comments and unknown fields', () => {
    expect(new SSEParser().feed(': ping\nid: 7\ndata: a\ndata: b\nretry: 10\n\n')).toEqual([
      { event: 'message', data: 'a\nb' },
    ])
  })

  it('keeps an unterminated event buffered', () => {
    const p = new SSEParser()
    expect(p.feed('event: delta\ndata: {}\n')).toEqual([])
    expect(p.feed('\n')).toEqual([{ event: 'delta', data: '{}' }])
  })

  it('skips events without data', () => {
    expect(new SSEParser().feed('event: x\n\n')).toEqual([])
  })
})
```

- [ ] **Step 2: Write the failing ask tests**

`web/src/api/ask.test.ts`:

```ts
import { afterEach, describe, expect, it, vi } from 'vitest'
import { askContinue, askStart } from './ask'
import { ApiError } from './client'

const enc = new TextEncoder()

function streamResponse(chunks: Uint8Array[], init: ResponseInit = {}) {
  const body = new ReadableStream<Uint8Array>({
    start(c) {
      for (const ch of chunks) c.enqueue(ch)
      c.close()
    },
  })
  return new Response(body, { status: 200, headers: { 'Content-Type': 'text/event-stream' }, ...init })
}

const sse = (...parts: string[]) => parts.map((p) => enc.encode(p))

function mockFetch(res: Response) {
  const fn = vi.fn(async (_url: string, _init?: RequestInit) => res)
  vi.stubGlobal('fetch', fn)
  return fn
}

const thread = { id: 't1', paper_id: 'p1', page_no: 2, anchor: { block_ids: [], quote: '' }, created_at: '', messages: [] }

afterEach(() => vi.unstubAllGlobals())

describe('ask', () => {
  it('streams deltas and resolves the thread', async () => {
    const f = mockFetch(
      streamResponse(
        sse(
          'event: delta\ndata: {"text":"가"}\n\n',
          'event: delta\ndata: {"text":"나"}\n\n',
          `event: done\ndata: ${JSON.stringify(thread)}\n\n`,
        ),
      ),
    )
    const deltas: string[] = []
    const t = await askStart('p1', 2, { block_ids: ['b1'], quote: 'q' }, '왜?', (d) => deltas.push(d))
    expect(t.id).toBe('t1')
    expect(deltas).toEqual(['가', '나'])
    const [url, init] = f.mock.calls[0]
    expect(url).toBe('/api/papers/p1/pages/2/threads')
    expect(init?.method).toBe('POST')
    expect(JSON.parse(init?.body as string)).toEqual({ anchor: { block_ids: ['b1'], quote: 'q' }, question: '왜?' })
  })

  it('korean split across byte chunks', async () => {
    const bytes = enc.encode('event: delta\ndata: {"text":"안녕하세요"}\n\nevent: done\ndata: {"id":"t1"}\n\n')
    const chunks = [bytes.slice(0, 29), bytes.slice(29, 33), bytes.slice(33)] // cuts inside 안 and 녕
    mockFetch(streamResponse(chunks))
    const deltas: string[] = []
    await askContinue('t1', '더', (d) => deltas.push(d))
    expect(deltas.join('')).toBe('안녕하세요')
  })

  it('continue posts to the thread', async () => {
    const f = mockFetch(streamResponse(sse('event: done\ndata: {"id":"t1"}\n\n')))
    await askContinue('t1', '그리고?', () => {})
    expect(f.mock.calls[0][0]).toBe('/api/threads/t1/messages')
    expect(JSON.parse(f.mock.calls[0][1]?.body as string)).toEqual({ question: '그리고?' })
  })

  it('4xx json becomes ApiError', async () => {
    mockFetch(
      new Response(JSON.stringify({ error: '찾을 수 없음' }), {
        status: 404,
        headers: { 'Content-Type': 'application/json' },
      }),
    )
    const onDelta = vi.fn()
    const err = (await askContinue('nope', 'q', onDelta).catch((e: unknown) => e)) as ApiError
    expect(err).toBeInstanceOf(ApiError)
    expect(err.status).toBe(404)
    expect(err.message).toBe('찾을 수 없음')
    expect(onDelta).not.toHaveBeenCalled()
  })

  it('error event rejects with its message', async () => {
    mockFetch(streamResponse(sse('event: delta\ndata: {"text":"부분"}\n\n', 'event: error\ndata: {"message":"claude: usage limit"}\n\n')))
    await expect(askContinue('t1', 'q', () => {})).rejects.toThrow('claude: usage limit')
  })

  it('truncated stream rejects', async () => {
    mockFetch(streamResponse(sse('event: delta\ndata: {"text":"부분"}\n\n')))
    await expect(askContinue('t1', 'q', () => {})).rejects.toThrow('응답이 중간에 끊겼습니다')
  })

  it('network failure is reported in Korean', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => {
        throw new TypeError('fetch failed')
      }),
    )
    await expect(askContinue('t1', 'q', () => {})).rejects.toThrow('서버에 연결할 수 없습니다')
  })
})
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `npm --prefix web test -- src/api/sse.test.ts src/api/ask.test.ts`
Expected: FAIL — cannot resolve `./sse` / `./ask`.

- [ ] **Step 4: Implement the parser and `postStream`**

`web/src/api/sse.ts`:

```ts
import { ApiError, errorFrom } from './client'

export interface SSEEvent {
  event: string
  data: string
}

/** Incremental text/event-stream parser: feed decoded text, get completed events. */
export class SSEParser {
  private buf = ''
  private event = ''
  private data: string[] = []

  feed(text: string): SSEEvent[] {
    this.buf += text
    const out: SSEEvent[] = []
    for (;;) {
      const i = this.buf.search(/[\r\n]/)
      if (i < 0) break
      if (this.buf[i] === '\r' && i + 1 === this.buf.length) break // may be the first half of \r\n
      const width = this.buf[i] === '\r' && this.buf[i + 1] === '\n' ? 2 : 1
      const line = this.buf.slice(0, i)
      this.buf = this.buf.slice(i + width)
      this.line(line, out)
    }
    return out
  }

  private line(line: string, out: SSEEvent[]) {
    if (line === '') {
      if (this.data.length > 0) out.push({ event: this.event || 'message', data: this.data.join('\n') })
      this.event = ''
      this.data = []
      return
    }
    if (line.startsWith(':')) return
    const colon = line.indexOf(':')
    const field = colon < 0 ? line : line.slice(0, colon)
    let value = colon < 0 ? '' : line.slice(colon + 1)
    if (value.startsWith(' ')) value = value.slice(1)
    if (field === 'event') this.event = value
    else if (field === 'data') this.data.push(value)
  }
}

/** POSTs JSON and feeds each SSE event of the response to onEvent until the stream ends. */
export async function postStream(
  url: string,
  body: unknown,
  onEvent: (e: SSEEvent) => void,
  signal?: AbortSignal,
): Promise<void> {
  let res: Response
  try {
    res = await fetch(url, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Accept: 'text/event-stream' },
      body: JSON.stringify(body),
      signal,
    })
  } catch (e) {
    if (signal?.aborted) throw e
    throw new ApiError(0, '서버에 연결할 수 없습니다')
  }
  if (!res.ok) throw await errorFrom(res)
  if (!res.body) throw new ApiError(res.status, '응답 본문이 없습니다')
  const reader = res.body.pipeThrough(new TextDecoderStream()).getReader()
  const parser = new SSEParser()
  for (;;) {
    const { value, done } = await reader.read()
    if (done) break
    for (const e of parser.feed(value)) onEvent(e)
  }
}
```

- [ ] **Step 5: Implement the ask client**

`web/src/api/ask.ts`:

```ts
import { ApiError } from './client'
import { postStream } from './sse'
import type { Anchor, Thread } from './types'

type DeltaHandler = (text: string) => void

async function run(url: string, body: unknown, onDelta: DeltaHandler, signal?: AbortSignal): Promise<Thread> {
  const out: { thread?: Thread; failure?: string } = {}
  await postStream(
    url,
    body,
    (e) => {
      let data: Record<string, unknown>
      try {
        data = JSON.parse(e.data) as Record<string, unknown>
      } catch {
        return
      }
      if (e.event === 'delta' && typeof data.text === 'string') onDelta(data.text)
      else if (e.event === 'done') out.thread = data as unknown as Thread
      else if (e.event === 'error')
        out.failure = typeof data.message === 'string' && data.message ? data.message : '답변 생성에 실패했습니다'
    },
    signal,
  )
  if (out.failure) throw new ApiError(200, out.failure)
  if (!out.thread) throw new ApiError(0, '응답이 중간에 끊겼습니다')
  return out.thread
}

export function askStart(
  paperId: string,
  pageNo: number,
  anchor: Anchor,
  question: string,
  onDelta: DeltaHandler,
  signal?: AbortSignal,
): Promise<Thread> {
  return run(`/api/papers/${paperId}/pages/${pageNo}/threads`, { anchor, question }, onDelta, signal)
}

export function askContinue(
  threadId: string,
  question: string,
  onDelta: DeltaHandler,
  signal?: AbortSignal,
): Promise<Thread> {
  return run(`/api/threads/${threadId}/messages`, { question }, onDelta, signal)
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `npm --prefix web test && npm --prefix web run typecheck`
Expected: all client/sse/ask tests pass (10 + 6 + 7); typecheck clean.

- [ ] **Step 7: Commit**

```bash
git add web/src/api/sse.ts web/src/api/sse.test.ts web/src/api/ask.ts web/src/api/ask.test.ts
git commit -m "feat(web): SSE parser and streamed Q&A client"
```

---

### Task 5: Page-generation state and stream controller

**Files:**
- Create: `web/src/lib/pageStream.ts`, `web/src/lib/pageStream.test.ts`
- Modify: `docs/superpowers/specs/2026-10-16-paper-reader-design.md` §8.2 step 3

**Interfaces:**
- Consumes: `api`, `errorMessage`, `streamURL` from `../api/client` (Task 3); `PageView` from `../api/types`.
- Produces:
  - `type GenPhase = 'loading' | 'waiting' | 'streaming' | 'done' | 'failed'`
  - `interface GenState { phase: GenPhase; blocks: Record<string, string>; explanation: string; error: string }`, `initialGen: GenState`
  - `type GenAction = { type: 'reset' } | { type: 'open' } | { type: 'event'; name: string; data: unknown }`
  - `genReducer(s: GenState, a: GenAction): GenState`
  - `interface EventSourceLike { readonly readyState: number; onopen: ((ev: unknown) => void) | null; addEventListener(type: string, listener: (ev: StreamMessage) => void): void; close(): void }`, `interface StreamMessage { data?: string }`
  - `interface GenDeps { getPage(paperId, pageNo): Promise<PageView>; generate(paperId, pageNo, force: boolean): Promise<unknown>; openStream(url: string): EventSourceLike }`, `defaultGenDeps: GenDeps`
  - `class PageGeneration { constructor(deps: GenDeps, paperId: string, pageNo: number, onView: (v: PageView) => void, dispatch: (a: GenAction) => void); open(): Promise<void>; run(force: boolean): Promise<void>; stop(): void }`

- [ ] **Step 1: Write the failing tests**

`web/src/lib/pageStream.test.ts`:

```ts
import { describe, expect, it, vi } from 'vitest'
import type { PageResult, PageView } from '../api/types'
import {
  PageGeneration,
  genReducer,
  initialGen,
  type EventSourceLike,
  type GenAction,
  type StreamMessage,
} from './pageStream'

class FakeES implements EventSourceLike {
  readyState = 0
  onopen: ((ev: unknown) => void) | null = null
  closed = false
  private listeners = new Map<string, ((ev: StreamMessage) => void)[]>()

  constructor(readonly url: string) {}

  addEventListener(type: string, listener: (ev: StreamMessage) => void) {
    this.listeners.set(type, [...(this.listeners.get(type) ?? []), listener])
  }
  close() {
    this.closed = true
    this.readyState = 2
  }
  open() {
    this.readyState = 1
    this.onopen?.({})
  }
  send(name: string, data: unknown) {
    for (const l of this.listeners.get(name) ?? []) l({ data: JSON.stringify(data) })
  }
  fail(readyState: number) {
    this.readyState = readyState
    for (const l of this.listeners.get('error') ?? []) l({})
  }
}

function result(status: PageResult['status'], extra: Partial<PageResult> = {}): PageResult {
  return { status, translation: [], explanation: '', page_summary: '', prompt_version: 1, updated_at: '', ...extra }
}

function view(r: PageResult | null): PageView {
  return { paper_id: 'p1', page_no: 3, page_count: 9, width: 600, height: 800, image_url: '/img', blocks: [], result: r }
}

function setup(...views: PageView[]) {
  const streams: FakeES[] = []
  const actions: GenAction[] = []
  const shown: PageView[] = []
  const deps = {
    getPage: vi.fn(async () => (views.length > 1 ? views.shift()! : views[0])),
    generate: vi.fn(async () => ({ status: 'pending' })),
    openStream: vi.fn((url: string) => {
      const s = new FakeES(url)
      streams.push(s)
      return s
    }),
  }
  const g = new PageGeneration(deps, 'p1', 3, (v) => shown.push(v), (a) => actions.push(a))
  const state = () => actions.reduce(genReducer, initialGen)
  return { g, deps, streams, actions, shown, state }
}

const flush = () => new Promise((r) => setTimeout(r, 0))

describe('genReducer', () => {
  it('accumulates blocks and explanation', () => {
    let s = genReducer(initialGen, { type: 'open' })
    expect(s.phase).toBe('waiting')
    s = genReducer(s, { type: 'event', name: 'status', data: { status: 'running' } })
    s = genReducer(s, { type: 'event', name: 'block', data: { block_id: 'b0', ko: '가' } })
    s = genReducer(s, { type: 'event', name: 'explanation', data: { delta: '줄1\n' } })
    s = genReducer(s, { type: 'event', name: 'explanation', data: { delta: '줄2\n' } })
    expect(s).toEqual({ phase: 'streaming', blocks: { b0: '가' }, explanation: '줄1\n줄2\n', error: '' })
    s = genReducer(s, { type: 'event', name: 'done', data: {} })
    expect(s.phase).toBe('done')
  })

  it('retrying clears first attempt output', () => {
    let s = genReducer(initialGen, { type: 'event', name: 'block', data: { block_id: 'b0', ko: '가' } })
    s = genReducer(s, { type: 'event', name: 'explanation', data: { delta: 'x' } })
    s = genReducer(s, { type: 'event', name: 'status', data: { status: 'retrying' } })
    expect(s).toEqual({ phase: 'streaming', blocks: {}, explanation: '', error: '' })
  })

  it('error records the message', () => {
    const s = genReducer(initialGen, { type: 'event', name: 'error', data: { message: '시간 초과' } })
    expect(s.phase).toBe('failed')
    expect(s.error).toBe('시간 초과')
    expect(genReducer(initialGen, { type: 'event', name: 'error', data: {} }).error).toBe('알 수 없는 오류')
  })

  it('ignores malformed and unknown events', () => {
    expect(genReducer(initialGen, { type: 'event', name: 'block', data: { ko: '가' } })).toBe(initialGen)
    expect(genReducer(initialGen, { type: 'event', name: 'weird', data: {} })).toBe(initialGen)
  })
})

describe('PageGeneration', () => {
  it('done result shows immediately without generating', async () => {
    const t = setup(view(result('done')))
    await t.g.open()
    expect(t.deps.generate).not.toHaveBeenCalled()
    expect(t.deps.openStream).not.toHaveBeenCalled()
    expect(t.state().phase).toBe('done')
    expect(t.shown).toHaveLength(1)
  })

  it('failed result is shown, not regenerated', async () => {
    const t = setup(view(result('failed', { error: 'claude: usage limit' })))
    await t.g.open()
    expect(t.deps.generate).not.toHaveBeenCalled()
    expect(t.state()).toMatchObject({ phase: 'failed', error: 'claude: usage limit' })
  })

  it('missing result generates and follows the stream', async () => {
    const t = setup(view(null), view(result('done')))
    await t.g.open()
    expect(t.deps.generate).toHaveBeenCalledWith('p1', 3, false)
    const es = t.streams[0]
    expect(es.url).toBe('/api/papers/p1/pages/3/stream')
    es.open()
    es.send('status', { status: 'running' })
    es.send('block', { block_id: 'b0', ko: '안녕' })
    es.send('explanation', { delta: '요지\n' })
    expect(t.state()).toMatchObject({ phase: 'streaming', blocks: { b0: '안녕' }, explanation: '요지\n' })
    es.send('done', {})
    await flush()
    expect(t.state().phase).toBe('done')
    expect(es.closed).toBe(true)
    expect(t.deps.getPage).toHaveBeenCalledTimes(2)
    expect(t.shown[1].result?.status).toBe('done')
  })

  it('pending and running results also subscribe', async () => {
    for (const status of ['pending', 'running'] as const) {
      const t = setup(view(result(status)))
      await t.g.open()
      expect(t.deps.generate).toHaveBeenCalledWith('p1', 3, false)
      expect(t.streams).toHaveLength(1)
    }
  })

  it('reconnect replay does not duplicate output', async () => {
    const t = setup(view(null))
    await t.g.open()
    const es = t.streams[0]
    es.open()
    es.send('block', { block_id: 'b0', ko: '가' })
    es.send('explanation', { delta: 'x\n' })
    es.fail(0) // browser is reconnecting
    expect(t.state().phase).toBe('streaming')
    es.open()
    es.send('block', { block_id: 'b0', ko: '가' })
    es.send('explanation', { delta: 'x\n' })
    expect(t.state()).toMatchObject({ blocks: { b0: '가' }, explanation: 'x\n' })
  })

  it('stop ignores late events', async () => {
    const t = setup(view(null))
    await t.g.open()
    const es = t.streams[0]
    es.open()
    const before = t.actions.length
    t.g.stop()
    expect(es.closed).toBe(true)
    es.send('block', { block_id: 'b0', ko: '늦음' })
    es.send('done', {})
    expect(t.actions.length).toBe(before)
  })

  it('stop during load prevents generation', async () => {
    const t = setup(view(null))
    const p = t.g.open()
    t.g.stop()
    await p
    expect(t.shown).toHaveLength(0)
    expect(t.deps.generate).not.toHaveBeenCalled()
  })

  it('server error event closes the stream without refetching', async () => {
    const t = setup(view(null))
    await t.g.open()
    const es = t.streams[0]
    es.open()
    es.send('error', { message: 'claude: usage limit' })
    expect(t.state()).toMatchObject({ phase: 'failed', error: 'claude: usage limit' })
    expect(es.closed).toBe(true)
    expect(t.deps.getPage).toHaveBeenCalledTimes(1)
  })

  it('closed connection falls back to stored result', async () => {
    const t = setup(view(null), view(result('failed', { error: '시간 초과' })))
    await t.g.open()
    t.streams[0].fail(2)
    await flush()
    expect(t.state()).toMatchObject({ phase: 'failed', error: '시간 초과' })
  })

  it('closed connection with nothing stored asks for a retry', async () => {
    const t = setup(view(null), view(result('running')))
    await t.g.open()
    t.streams[0].fail(2)
    await flush()
    expect(t.state().error).toBe('생성 스트림 연결이 끊겼습니다. 재시도하세요.')
  })

  it('run(true) forces regeneration and replaces the stream', async () => {
    const t = setup(view(result('done')))
    await t.g.open()
    await t.g.run(true)
    expect(t.deps.generate).toHaveBeenCalledWith('p1', 3, true)
    expect(t.state().phase).toBe('waiting')
    await t.g.run(false)
    expect(t.streams[0].closed).toBe(true)
    expect(t.streams).toHaveLength(2)
  })

  it('generate failure is reported', async () => {
    const t = setup(view(null))
    t.deps.generate.mockRejectedValueOnce(new Error('찾을 수 없음'))
    await t.g.open()
    expect(t.state()).toMatchObject({ phase: 'failed', error: '찾을 수 없음' })
    expect(t.streams).toHaveLength(0)
  })
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `npm --prefix web test -- src/lib/pageStream.test.ts`
Expected: FAIL — cannot resolve `./pageStream`.

- [ ] **Step 3: Implement**

`web/src/lib/pageStream.ts`:

```ts
import { api, errorMessage, streamURL } from '../api/client'
import type { PageView } from '../api/types'

export type GenPhase = 'loading' | 'waiting' | 'streaming' | 'done' | 'failed'

export interface GenState {
  phase: GenPhase
  blocks: Record<string, string>
  explanation: string
  error: string
}

export const initialGen: GenState = { phase: 'loading', blocks: {}, explanation: '', error: '' }

export type GenAction =
  | { type: 'reset' } // a different page: back to loading
  | { type: 'open' } // a (re)opened stream replays from scratch
  | { type: 'event'; name: string; data: unknown }

function field(data: unknown, key: string): string {
  if (data && typeof data === 'object') {
    const v = (data as Record<string, unknown>)[key]
    if (typeof v === 'string') return v
  }
  return ''
}

export function genReducer(s: GenState, a: GenAction): GenState {
  switch (a.type) {
    case 'reset':
      return initialGen
    case 'open':
      return { phase: 'waiting', blocks: {}, explanation: '', error: '' }
    case 'event':
      switch (a.name) {
        case 'status':
          if (field(a.data, 'status') === 'retrying') return { ...s, phase: 'streaming', blocks: {}, explanation: '' }
          return { ...s, phase: 'streaming' }
        case 'block': {
          const id = field(a.data, 'block_id')
          if (!id) return s
          return { ...s, phase: 'streaming', blocks: { ...s.blocks, [id]: field(a.data, 'ko') } }
        }
        case 'explanation':
          return { ...s, phase: 'streaming', explanation: s.explanation + field(a.data, 'delta') }
        case 'done':
          return { ...s, phase: 'done', error: '' }
        case 'error':
          return { ...s, phase: 'failed', error: field(a.data, 'message') || '알 수 없는 오류' }
        default:
          return s
      }
  }
}

export interface StreamMessage {
  data?: string
}

export interface EventSourceLike {
  readonly readyState: number
  onopen: ((ev: unknown) => void) | null
  addEventListener(type: string, listener: (ev: StreamMessage) => void): void
  close(): void
}

const CLOSED = 2
const EVENTS = ['status', 'block', 'explanation', 'done', 'error'] as const

export interface GenDeps {
  getPage(paperId: string, pageNo: number): Promise<PageView>
  generate(paperId: string, pageNo: number, force: boolean): Promise<unknown>
  openStream(url: string): EventSourceLike
}

export const defaultGenDeps: GenDeps = {
  getPage: (id, n) => api.getPage(id, n),
  generate: (id, n, force) => api.generate(id, n, force),
  openStream: (url) => new EventSource(url) as unknown as EventSourceLike,
}

/** Drives one page: load it, start generation when needed, and follow the SSE stream. */
export class PageGeneration {
  private es: EventSourceLike | null = null
  private stopped = false
  private readonly deps: GenDeps
  private readonly paperId: string
  private readonly pageNo: number
  private readonly onView: (v: PageView) => void
  private readonly dispatch: (a: GenAction) => void

  constructor(
    deps: GenDeps,
    paperId: string,
    pageNo: number,
    onView: (v: PageView) => void,
    dispatch: (a: GenAction) => void,
  ) {
    this.deps = deps
    this.paperId = paperId
    this.pageNo = pageNo
    this.onView = onView
    this.dispatch = dispatch
  }

  /** Loads the page. A done or failed result is shown as is; otherwise generation starts. */
  async open(): Promise<void> {
    const v = await this.load(true)
    if (!v) return
    const r = v.result
    if (r?.status === 'done') this.emit('done', {})
    else if (r?.status === 'failed') this.emit('error', { message: r.error || '생성에 실패했습니다' })
    else await this.run(false)
  }

  /** Requests generation (force = regenerate a done page) and follows its stream. */
  async run(force: boolean): Promise<void> {
    if (this.stopped) return
    this.closeStream()
    this.dispatch({ type: 'open' })
    try {
      await this.deps.generate(this.paperId, this.pageNo, force)
    } catch (e) {
      this.emit('error', { message: errorMessage(e) })
      return
    }
    if (!this.stopped) this.subscribe()
  }

  stop(): void {
    this.stopped = true
    this.closeStream()
  }

  private async load(reportErrors: boolean): Promise<PageView | null> {
    try {
      const v = await this.deps.getPage(this.paperId, this.pageNo)
      if (this.stopped) return null
      this.onView(v)
      return v
    } catch (e) {
      if (reportErrors) this.emit('error', { message: errorMessage(e) })
      return null
    }
  }

  private emit(name: string, data: unknown) {
    if (!this.stopped) this.dispatch({ type: 'event', name, data })
  }

  private closeStream() {
    this.es?.close()
    this.es = null
  }

  private subscribe() {
    const es = this.deps.openStream(streamURL(this.paperId, this.pageNo))
    this.es = es
    const live = () => !this.stopped && this.es === es
    es.onopen = () => {
      if (live()) this.dispatch({ type: 'open' })
    }
    for (const name of EVENTS) {
      es.addEventListener(name, (ev) => {
        if (!live()) return
        if (name === 'error' && ev.data === undefined) {
          this.connectionError(es)
          return
        }
        let data: unknown = {}
        try {
          data = JSON.parse(ev.data ?? '{}')
        } catch {
          data = {}
        }
        this.emit(name, data)
        if (name === 'done' || name === 'error') {
          this.closeStream()
          if (name === 'done') void this.load(false)
        }
      })
    }
  }

  // A native EventSource error: while reconnecting the replay will follow
  // onopen; once CLOSED, fall back to whatever the server stored.
  private connectionError(es: EventSourceLike) {
    if (es.readyState !== CLOSED) return
    this.closeStream()
    void this.load(true).then((v) => {
      if (!v) return
      const r = v.result
      if (r?.status === 'done') this.emit('done', {})
      else if (r?.status === 'failed') this.emit('error', { message: r.error || '생성에 실패했습니다' })
      else this.emit('error', { message: '생성 스트림 연결이 끊겼습니다. 재시도하세요.' })
    })
  }
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `npm --prefix web test -- src/lib/pageStream.test.ts && npm --prefix web run typecheck`
Expected: 16 passed; typecheck clean.

- [ ] **Step 5: Align the spec with the failed-page behavior**

In `docs/superpowers/specs/2026-10-16-paper-reader-design.md` §8.2, replace step 3's first line

```
3. 결과가 `done`이 아니면(없음, `failed`, `pending`, `running` 모두) 프론트가 `POST /api/papers/{id}/pages/{n}/generate`를 호출한다.
```

with

```
3. 결과가 없거나 `pending`/`running`이면 프론트가 `POST /api/papers/{id}/pages/{n}/generate`를 호출한다. `failed`면 에러와 [재시도]를 보여 주고, 사용자가 누를 때 같은 호출을 한다 (사용량 한도 실패가 페이지를 열 때마다 재시도되지 않도록).
```

- [ ] **Step 6: Commit**

```bash
git add web/src/lib/pageStream.ts web/src/lib/pageStream.test.ts docs/superpowers/specs/2026-10-16-paper-reader-design.md
git commit -m "feat(web): page generation state and stream controller"
```

---

### Task 6: Library page

**Files:**
- Create: `web/src/pages/Library.tsx`
- Modify: `web/src/App.tsx` (replace)

**Interfaces:**
- Consumes: `api`, `errorMessage` (Task 3); `Paper` (Task 1).
- Produces: default export `Library` component; `App` routes `/` → `Library`, `*` → not-found. Task 7 adds the reader routes to this `App`.

- [ ] **Step 1: Write the page**

`web/src/pages/Library.tsx`:

```tsx
import { useCallback, useEffect, useRef, useState } from 'react'
import { Link } from 'react-router'
import { api, errorMessage } from '../api/client'
import type { Paper } from '../api/types'

const POLL_MS = 2000

function statusLine(p: Paper): string {
  if (p.status === 'processing') return 'PDF 처리 중…'
  if (p.status === 'failed') return '처리 실패'
  return `${p.page_count}쪽 · 번역 ${p.done_pages}/${p.page_count}`
}

export default function Library() {
  const [papers, setPapers] = useState<Paper[] | null>(null)
  const [error, setError] = useState('')
  const [uploading, setUploading] = useState(false)
  const [dragOver, setDragOver] = useState(false)
  const [confirmId, setConfirmId] = useState<string | null>(null)
  const input = useRef<HTMLInputElement>(null)

  const load = useCallback(async () => {
    try {
      setPapers(await api.listPapers())
    } catch (e) {
      setError(errorMessage(e))
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  const processing = papers?.some((p) => p.status === 'processing') ?? false
  useEffect(() => {
    if (!processing) return
    const t = setInterval(() => void load(), POLL_MS)
    return () => clearInterval(t)
  }, [processing, load])

  async function upload(files: File[]) {
    if (files.length === 0) return
    setUploading(true)
    setError('')
    const problems: string[] = []
    for (const f of files) {
      if (!f.name.toLowerCase().endsWith('.pdf')) {
        problems.push(`${f.name}: PDF 파일만 올릴 수 있습니다`)
        continue
      }
      try {
        await api.uploadPaper(f)
      } catch (e) {
        problems.push(`${f.name}: ${errorMessage(e)}`)
      }
    }
    setError(problems.join('\n'))
    setUploading(false)
    void load()
  }

  async function remove(id: string) {
    try {
      await api.deletePaper(id)
    } catch (e) {
      setError(errorMessage(e))
    }
    setConfirmId(null)
    void load()
  }

  return (
    <main className="page">
      <h1>Paper Reader</h1>
      <div
        className={dragOver ? 'dropzone over' : 'dropzone'}
        onDragOver={(e) => {
          e.preventDefault()
          setDragOver(true)
        }}
        onDragLeave={() => setDragOver(false)}
        onDrop={(e) => {
          e.preventDefault()
          setDragOver(false)
          void upload(Array.from(e.dataTransfer.files))
        }}
      >
        <p>PDF를 여기에 끌어다 놓거나</p>
        <button onClick={() => input.current?.click()} disabled={uploading}>
          {uploading ? '올리는 중…' : '파일 선택'}
        </button>
        <input
          ref={input}
          type="file"
          accept="application/pdf,.pdf"
          multiple
          hidden
          onChange={(e) => {
            const files = Array.from(e.target.files ?? [])
            e.target.value = ''
            void upload(files)
          }}
        />
      </div>
      {error && <p className="error" style={{ whiteSpace: 'pre-line' }}>{error}</p>}
      {papers === null ? (
        <p className="muted">불러오는 중…</p>
      ) : papers.length === 0 ? (
        <p className="muted">아직 올린 논문이 없습니다.</p>
      ) : (
        <ul className="paper-list">
          {papers.map((p) => (
            <li key={p.id} className="paper-card">
              <div className="info">
                {p.status === 'ready' ? (
                  <Link className="title" to={`/papers/${p.id}/1`}>
                    {p.title}
                  </Link>
                ) : (
                  <span className="title">{p.title}</span>
                )}
                <div className="muted">{statusLine(p)}</div>
                {p.status === 'failed' && p.error && <div className="error">{p.error}</div>}
              </div>
              {confirmId === p.id ? (
                <>
                  <span>삭제할까요?</span>
                  <button className="danger" onClick={() => void remove(p.id)}>
                    삭제
                  </button>
                  <button onClick={() => setConfirmId(null)}>취소</button>
                </>
              ) : (
                <button onClick={() => setConfirmId(p.id)}>삭제</button>
              )}
            </li>
          ))}
        </ul>
      )}
    </main>
  )
}
```

- [ ] **Step 2: Replace `web/src/App.tsx`**

```tsx
import { BrowserRouter, Link, Route, Routes } from 'react-router'
import Library from './pages/Library'

function NotFound() {
  return (
    <main className="page">
      <p>페이지를 찾을 수 없습니다.</p>
      <Link to="/">목록으로</Link>
    </main>
  )
}

export default function App() {
  return (
    <BrowserRouter>
      <Routes>
        <Route path="/" element={<Library />} />
        <Route path="*" element={<NotFound />} />
      </Routes>
    </BrowserRouter>
  )
}
```

- [ ] **Step 3: Typecheck and build**

Run: `npm --prefix web run typecheck && make web-build`
Expected: clean; `✓ built`.

- [ ] **Step 4: Manual check**

Run the backend (`make dev`) and the dev server (`make web-dev`), open `http://localhost:5173/`:
- empty list shows `아직 올린 논문이 없습니다.`;
- dropping a `.txt` shows `<name>: PDF 파일만 올릴 수 있습니다`;
- uploading a PDF shows `PDF 처리 중…`, then within a few seconds `N쪽 · 번역 0/N` without reloading (polling stops afterwards — Network tab shows no more `/api/papers` requests);
- 삭제 → 삭제할까요? → 취소 keeps it; 삭제 removes it.

- [ ] **Step 5: Commit**

```bash
git add web/src/pages/Library.tsx web/src/App.tsx
git commit -m "feat(web): library page with upload, polling and delete"
```

---

### Task 7: Reader — page view, translation, explanation, navigation

**Files:**
- Create: `web/src/lib/nav.ts`, `web/src/lib/nav.test.ts`, `web/src/hooks/usePageGeneration.ts`, `web/src/hooks/useMediaQuery.ts`, `web/src/components/Toolbar.tsx`, `web/src/components/StatusBar.tsx`, `web/src/components/PageImage.tsx`, `web/src/components/TranslationPanel.tsx`, `web/src/components/MarkdownView.tsx`, `web/src/components/ExplanationPanel.tsx`, `web/src/pages/Reader.tsx`
- Modify: `web/src/App.tsx`

**Interfaces:**
- Consumes: `api`, `errorMessage` (Task 3); `PageGeneration`, `defaultGenDeps`, `genReducer`, `initialGen`, `GenState` (Task 5); types (Task 1).
- Produces:
  - `nav.ts`: `PREFETCH_COUNT = 3`, `isEditable(target: unknown): boolean`, `navTarget(key: string, page: number, count: number, editable: boolean): number | null`, `clampPage(input: string, count: number): number | null`, `prefetchRange(page: number, count: number, n?: number): { from: number; count: number } | null`, `parsePageParam(raw: string | undefined): number | null`
  - `usePageGeneration(paperId: string, pageNo: number): { view: PageView | null; gen: GenState; retry: () => void; regenerate: () => void }`
  - `useMediaQuery(query: string): boolean`
  - `MarkdownView({ text }: { text: string })` (reused by Task 8)
  - `PageImage` props `{ view: PageView; hot: string | null; onHover: (id: string | null) => void }` and `TranslationPanel` props `{ blocks: Block[]; translations: Record<string, string>; generating: boolean; hot: string | null; onHover: (id: string | null) => void }` — both get an `onAsk` prop in Task 8
  - `Reader.tsx`: default export `Reader`; inner `ReaderPage` with `type Tab = 'source' | 'translation' | 'explanation'` (Task 8 adds `'ask'`)

- [ ] **Step 1: Write the failing nav tests**

`web/src/lib/nav.test.ts`:

```ts
import { describe, expect, it } from 'vitest'
import { clampPage, isEditable, navTarget, parsePageParam, prefetchRange } from './nav'

describe('navTarget', () => {
  it('moves within bounds', () => {
    expect(navTarget('ArrowLeft', 3, 9, false)).toBe(2)
    expect(navTarget('ArrowRight', 3, 9, false)).toBe(4)
  })
  it('stops at the edges', () => {
    expect(navTarget('ArrowLeft', 1, 9, false)).toBeNull()
    expect(navTarget('ArrowRight', 9, 9, false)).toBeNull()
  })
  it('does nothing while typing', () => {
    expect(navTarget('ArrowLeft', 3, 9, true)).toBeNull()
    expect(navTarget('ArrowRight', 3, 9, true)).toBeNull()
  })
  it('ignores other keys', () => {
    expect(navTarget('a', 3, 9, false)).toBeNull()
  })
})

describe('isEditable', () => {
  it('detects form fields and contenteditable', () => {
    expect(isEditable({ tagName: 'INPUT' })).toBe(true)
    expect(isEditable({ tagName: 'textarea' })).toBe(true)
    expect(isEditable({ tagName: 'SELECT' })).toBe(true)
    expect(isEditable({ tagName: 'DIV', isContentEditable: true })).toBe(true)
    expect(isEditable({ tagName: 'DIV', isContentEditable: false })).toBe(false)
    expect(isEditable(null)).toBe(false)
  })
})

describe('clampPage', () => {
  it('parses and clamps', () => {
    expect(clampPage('4', 9)).toBe(4)
    expect(clampPage(' 12 ', 9)).toBe(9)
    expect(clampPage('0', 9)).toBe(1)
  })
  it('rejects junk and unknown counts', () => {
    expect(clampPage('abc', 9)).toBeNull()
    expect(clampPage('3.5', 9)).toBeNull()
    expect(clampPage('', 9)).toBeNull()
    expect(clampPage('3', 0)).toBeNull()
  })
})

describe('prefetchRange', () => {
  it('queues the next pages', () => {
    expect(prefetchRange(3, 9)).toEqual({ from: 4, count: 3 })
  })
  it('clamps to the end', () => {
    expect(prefetchRange(8, 9)).toEqual({ from: 9, count: 1 })
    expect(prefetchRange(9, 9)).toBeNull()
  })
})

describe('parsePageParam', () => {
  it('accepts positive integers only', () => {
    expect(parsePageParam('7')).toBe(7)
    expect(parsePageParam('0')).toBeNull()
    expect(parsePageParam('-1')).toBeNull()
    expect(parsePageParam('2x')).toBeNull()
    expect(parsePageParam(undefined)).toBeNull()
  })
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `npm --prefix web test -- src/lib/nav.test.ts`
Expected: FAIL — cannot resolve `./nav`.

- [ ] **Step 3: Implement `nav.ts`**

`web/src/lib/nav.ts`:

```ts
export const PREFETCH_COUNT = 3

/** True for elements where arrow keys belong to the user's typing. */
export function isEditable(target: unknown): boolean {
  if (!target || typeof target !== 'object') return false
  const el = target as { tagName?: unknown; isContentEditable?: unknown }
  const tag = typeof el.tagName === 'string' ? el.tagName.toUpperCase() : ''
  return tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || el.isContentEditable === true
}

export function navTarget(key: string, page: number, count: number, editable: boolean): number | null {
  if (editable) return null
  if (key === 'ArrowLeft' && page > 1) return page - 1
  if (key === 'ArrowRight' && page < count) return page + 1
  return null
}

export function clampPage(input: string, count: number): number | null {
  if (count < 1 || !/^\s*\d+\s*$/.test(input)) return null
  return Math.min(Math.max(Number.parseInt(input, 10), 1), count)
}

export function prefetchRange(page: number, count: number, n = PREFETCH_COUNT): { from: number; count: number } | null {
  if (page >= count) return null
  return { from: page + 1, count: Math.min(n, count - page) }
}

export function parsePageParam(raw: string | undefined): number | null {
  if (!raw || !/^\d+$/.test(raw)) return null
  const n = Number.parseInt(raw, 10)
  return n >= 1 ? n : null
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `npm --prefix web test -- src/lib/nav.test.ts`
Expected: PASS (10 tests).

- [ ] **Step 5: Hooks**

`web/src/hooks/usePageGeneration.ts`:

```ts
import { useCallback, useEffect, useReducer, useRef, useState } from 'react'
import type { PageView } from '../api/types'
import { PageGeneration, defaultGenDeps, genReducer, initialGen } from '../lib/pageStream'

export function usePageGeneration(paperId: string, pageNo: number) {
  const [view, setView] = useState<PageView | null>(null)
  const [gen, dispatch] = useReducer(genReducer, initialGen)
  const ctl = useRef<PageGeneration | null>(null)

  useEffect(() => {
    setView(null)
    dispatch({ type: 'reset' })
    const c = new PageGeneration(defaultGenDeps, paperId, pageNo, setView, dispatch)
    ctl.current = c
    void c.open()
    return () => {
      c.stop()
      if (ctl.current === c) ctl.current = null
    }
  }, [paperId, pageNo])

  const retry = useCallback(() => void ctl.current?.run(false), [])
  const regenerate = useCallback(() => void ctl.current?.run(true), [])
  return { view, gen, retry, regenerate }
}
```

`web/src/hooks/useMediaQuery.ts`:

```ts
import { useSyncExternalStore } from 'react'

export function useMediaQuery(query: string): boolean {
  return useSyncExternalStore(
    (onChange) => {
      const m = window.matchMedia(query)
      m.addEventListener('change', onChange)
      return () => m.removeEventListener('change', onChange)
    },
    () => window.matchMedia(query).matches,
  )
}
```

- [ ] **Step 6: Components**

`web/src/components/Toolbar.tsx`:

```tsx
import { useEffect, useState } from 'react'
import { Link } from 'react-router'
import { PREFETCH_COUNT, clampPage } from '../lib/nav'

interface Props {
  title: string
  pageNo: number
  pageCount: number
  onGo: (page: number) => void
  onPrefetch: () => void
  prefetchNote: string
}

export default function Toolbar({ title, pageNo, pageCount, onGo, onPrefetch, prefetchNote }: Props) {
  const [input, setInput] = useState(String(pageNo))
  useEffect(() => setInput(String(pageNo)), [pageNo])

  const submit = () => {
    const n = clampPage(input, pageCount)
    if (n === null || n === pageNo) {
      setInput(String(pageNo))
      return
    }
    onGo(n)
  }

  return (
    <header className="toolbar">
      <Link to="/">← 목록</Link>
      <span className="title" title={title}>
        {title}
      </span>
      <button onClick={() => onGo(pageNo - 1)} disabled={pageNo <= 1} aria-label="이전 페이지">
        ◀
      </button>
      <input
        className="page-input"
        value={input}
        inputMode="numeric"
        aria-label="페이지 번호"
        onChange={(e) => setInput(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Enter') submit()
        }}
        onBlur={submit}
      />
      <span className="muted">/ {pageCount || '?'}</span>
      <button onClick={() => onGo(pageNo + 1)} disabled={pageCount > 0 && pageNo >= pageCount} aria-label="다음 페이지">
        ▶
      </button>
      <button onClick={onPrefetch} disabled={pageCount === 0 || pageNo >= pageCount}>
        다음 {PREFETCH_COUNT}장 미리 생성
      </button>
      {prefetchNote && <span className="muted">{prefetchNote}</span>}
    </header>
  )
}
```

`web/src/components/StatusBar.tsx`:

```tsx
import type { GenState } from '../lib/pageStream'

interface Props {
  gen: GenState
  onRetry: () => void
  onRegenerate: () => void
}

export default function StatusBar({ gen, onRetry, onRegenerate }: Props) {
  switch (gen.phase) {
    case 'loading':
      return <div className="status-bar muted">불러오는 중…</div>
    case 'waiting':
      return <div className="status-bar muted">대기 중… 앞선 작업이 끝나면 생성을 시작합니다.</div>
    case 'streaming':
      return <div className="status-bar">생성 중…</div>
    case 'failed':
      return (
        <div className="status-bar">
          <span className="error">실패: {gen.error}</span>
          <button onClick={onRetry}>재시도</button>
        </div>
      )
    case 'done':
      return (
        <div className="status-bar">
          <span className="muted">완료</span>
          <button onClick={onRegenerate}>다시 생성</button>
        </div>
      )
  }
}
```

`web/src/components/PageImage.tsx`:

```tsx
import type { PageView } from '../api/types'

interface Props {
  view: PageView
  hot: string | null
  onHover: (id: string | null) => void
}

const pct = (v: number) => `${(v * 100).toFixed(3)}%`

export default function PageImage({ view, hot, onHover }: Props) {
  const scaled = view.width > 0 && view.height > 0
  return (
    <div className="page-image">
      <img src={view.image_url} alt={`${view.page_no}쪽 원문`} />
      {scaled &&
        view.blocks.map((b) => (
          <div
            key={b.id}
            className={b.id === hot ? 'bbox hot' : 'bbox'}
            style={{
              left: pct(b.bbox.x / view.width),
              top: pct(b.bbox.y / view.height),
              width: pct(b.bbox.w / view.width),
              height: pct(b.bbox.h / view.height),
            }}
            onMouseEnter={() => onHover(b.id)}
            onMouseLeave={() => onHover(null)}
          />
        ))}
    </div>
  )
}
```

`web/src/components/TranslationPanel.tsx`:

```tsx
import type { Block } from '../api/types'

interface Props {
  blocks: Block[]
  translations: Record<string, string>
  generating: boolean
  hot: string | null
  onHover: (id: string | null) => void
}

export default function TranslationPanel({ blocks, translations, generating, hot, onHover }: Props) {
  if (blocks.length === 0) return <p className="muted">이 페이지에는 추출된 텍스트가 없습니다. 해설 탭을 확인하세요.</p>
  return (
    <div>
      {blocks.map((b) => {
        const ko = translations[b.id]
        const cls = ['tblock', b.id === hot ? 'hot' : '', ko ? '' : 'pending'].filter(Boolean).join(' ')
        return (
          <div
            key={b.id}
            data-block-id={b.id}
            className={cls}
            title={b.text}
            onMouseEnter={() => onHover(b.id)}
            onMouseLeave={() => onHover(null)}
          >
            {ko ?? (generating ? '…' : b.text)}
          </div>
        )
      })}
    </div>
  )
}
```

`web/src/components/MarkdownView.tsx`:

```tsx
import Markdown from 'react-markdown'
import rehypeKatex from 'rehype-katex'
import remarkMath from 'remark-math'

export default function MarkdownView({ text }: { text: string }) {
  return (
    <div className="markdown">
      <Markdown remarkPlugins={[remarkMath]} rehypePlugins={[rehypeKatex]}>
        {text}
      </Markdown>
    </div>
  )
}
```

`web/src/components/ExplanationPanel.tsx`:

```tsx
import MarkdownView from './MarkdownView'

export default function ExplanationPanel({ text, generating }: { text: string; generating: boolean }) {
  if (!text) return <p className="muted">{generating ? '해설을 생성하는 중…' : '아직 해설이 없습니다.'}</p>
  return <MarkdownView text={text} />
}
```

- [ ] **Step 7: Reader page**

`web/src/pages/Reader.tsx`:

```tsx
import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link, Navigate, useNavigate, useParams } from 'react-router'
import { api, errorMessage } from '../api/client'
import type { Paper } from '../api/types'
import ExplanationPanel from '../components/ExplanationPanel'
import PageImage from '../components/PageImage'
import StatusBar from '../components/StatusBar'
import Toolbar from '../components/Toolbar'
import TranslationPanel from '../components/TranslationPanel'
import { useMediaQuery } from '../hooks/useMediaQuery'
import { usePageGeneration } from '../hooks/usePageGeneration'
import { isEditable, navTarget, parsePageParam, prefetchRange } from '../lib/nav'

type Tab = 'source' | 'translation' | 'explanation'

export default function Reader() {
  const { id = '', page } = useParams()
  const pageNo = parsePageParam(page)
  if (pageNo === null) return <Navigate to={`/papers/${id}/1`} replace />
  return <ReaderPage key={id} paperId={id} pageNo={pageNo} />
}

function ReaderPage({ paperId, pageNo }: { paperId: string; pageNo: number }) {
  const navigate = useNavigate()
  const narrow = useMediaQuery('(max-width: 899px)')
  const [paper, setPaper] = useState<Paper | null>(null)
  const [paperError, setPaperError] = useState('')
  const [tab, setTab] = useState<Tab>('translation')
  const [hot, setHot] = useState<string | null>(null)
  const [prefetchNote, setPrefetchNote] = useState('')
  const { view, gen, retry, regenerate } = usePageGeneration(paperId, pageNo)

  useEffect(() => {
    api.getPaper(paperId).then(setPaper, (e: unknown) => setPaperError(errorMessage(e)))
  }, [paperId])

  useEffect(() => {
    setHot(null)
    setPrefetchNote('')
  }, [pageNo])

  const pageCount = view?.page_count ?? paper?.page_count ?? 0

  const go = useCallback(
    (n: number) => {
      if (n >= 1 && (pageCount === 0 || n <= pageCount)) navigate(`/papers/${paperId}/${n}`)
    },
    [navigate, paperId, pageCount],
  )

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.altKey || e.ctrlKey || e.metaKey || e.shiftKey) return
      const n = navTarget(e.key, pageNo, pageCount, isEditable(e.target))
      if (n !== null) {
        e.preventDefault()
        go(n)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [go, pageNo, pageCount])

  async function prefetch() {
    const r = prefetchRange(pageNo, pageCount)
    if (!r) return
    try {
      const { queued } = await api.prefetch(paperId, r.from, r.count)
      setPrefetchNote(queued > 0 ? `${queued}장을 대기열에 넣었습니다` : '이미 생성됐거나 대기 중입니다')
    } catch (e) {
      setPrefetchNote(errorMessage(e))
    }
  }

  const generating = gen.phase === 'waiting' || gen.phase === 'streaming'
  const translations = useMemo(() => {
    if (generating) return gen.blocks
    const m: Record<string, string> = {}
    for (const t of view?.result?.translation ?? []) m[t.block_id] = t.ko
    return m
  }, [generating, gen.blocks, view])
  const explanation = generating ? gen.explanation : (view?.result?.explanation ?? '')

  if (paperError) {
    return (
      <main className="page">
        <p className="error">{paperError}</p>
        <Link to="/">목록으로</Link>
      </main>
    )
  }

  const source = view ? <PageImage view={view} hot={hot} onHover={setHot} /> : <p className="muted">불러오는 중…</p>
  const tabs: [Tab, string][] = [
    ...(narrow ? ([['source', '원문']] as [Tab, string][]) : []),
    ['translation', '번역'],
    ['explanation', '해설'],
  ]
  const active: Tab = !narrow && tab === 'source' ? 'translation' : tab

  return (
    <div className="reader">
      <Toolbar
        title={paper?.title ?? ''}
        pageNo={pageNo}
        pageCount={pageCount}
        onGo={go}
        onPrefetch={() => void prefetch()}
        prefetchNote={prefetchNote}
      />
      <StatusBar gen={gen} onRetry={retry} onRegenerate={regenerate} />
      <div className="split">
        {!narrow && <section className="pane left">{source}</section>}
        <section className="pane right">
          <nav className="tabs">
            {tabs.map(([key, label]) => (
              <button key={key} className={key === active ? 'active' : ''} onClick={() => setTab(key)}>
                {label}
              </button>
            ))}
          </nav>
          {active === 'source' && <div className="tab-body source">{source}</div>}
          {active === 'translation' && (
            <div className="tab-body">
              {view && (
                <TranslationPanel
                  blocks={view.blocks}
                  translations={translations}
                  generating={generating}
                  hot={hot}
                  onHover={setHot}
                />
              )}
            </div>
          )}
          {active === 'explanation' && (
            <div className="tab-body">
              <ExplanationPanel text={explanation} generating={generating} />
            </div>
          )}
        </section>
      </div>
    </div>
  )
}
```

- [ ] **Step 8: Add the reader routes**

In `web/src/App.tsx`, change the router import line to

```tsx
import { BrowserRouter, Link, Navigate, Route, Routes, useParams } from 'react-router'
import Library from './pages/Library'
import Reader from './pages/Reader'
```

add above `NotFound`:

```tsx
function FirstPage() {
  const { id = '' } = useParams()
  return <Navigate to={`/papers/${id}/1`} replace />
}
```

and add these two routes between the `/` route and the `*` route:

```tsx
        <Route path="/papers/:id" element={<FirstPage />} />
        <Route path="/papers/:id/:page" element={<Reader />} />
```

- [ ] **Step 9: Test, typecheck, build**

Run: `npm --prefix web test && make web-build`
Expected: all tests pass; build `✓ built`.

- [ ] **Step 10: Manual check** (backend via `make dev`, UI via `make web-dev`, a ready paper)

- Open a paper: page 1 image on the left; hovering a block box highlights the matching translation and vice versa.
- A fresh page shows `대기 중…`/`생성 중…`, translations fill in block by block, the 해설 tab grows line by line and renders `$...$` math; on completion the bar shows `완료 [다시 생성]`.
- Reload during generation: the page continues streaming with no duplicated text.
- ←/→ change pages; typing digits + ← in the page box does not change page; page box `999` + Enter goes to the last page.
- [다음 3장 미리 생성] shows `3장을 대기열에 넣었습니다` (or the clamped count near the end).
- Narrow the window below 900px: one tab bar 원문/번역/해설.

- [ ] **Step 11: Commit**

```bash
git add web/src
git commit -m "feat(web): reader with streamed translation, explanation and navigation"
```

---

### Task 8: Questions (anchored Q&A)

**Files:**
- Create: `web/src/lib/anchor.ts`, `web/src/lib/anchor.test.ts`, `web/src/components/AskPanel.tsx`
- Replace: `web/src/components/PageImage.tsx`, `web/src/components/TranslationPanel.tsx`
- Modify: `web/src/pages/Reader.tsx`

**Interfaces:**
- Consumes: `askStart`, `askContinue` (Task 4); `api.listThreads`, `errorMessage` (Task 3); `MarkdownView` (Task 7); `Anchor`, `Thread` (Task 1).
- Produces: `makeAnchor(blockIds: string[], quote: string): Anchor`, `quotePreview(a: Anchor, max?: number): string`, `MAX_QUOTE = 1000`; `AskPanel` props `{ paperId: string; pageNo: number; draft: Anchor | null; onDraft: (a: Anchor | null) => void }`; `PageImage`/`TranslationPanel` gain `onAsk: (blockIds: string[], quote: string) => void`.

- [ ] **Step 1: Write the failing anchor tests**

`web/src/lib/anchor.test.ts`:

```ts
import { describe, expect, it } from 'vitest'
import { MAX_QUOTE, makeAnchor, quotePreview } from './anchor'

describe('makeAnchor', () => {
  it('dedupes ids and normalizes whitespace', () => {
    expect(makeAnchor(['b1', 'b2', 'b1', ''], '  a\n\n b\t c ')).toEqual({ block_ids: ['b1', 'b2'], quote: 'a b c' })
  })
  it('truncates long quotes', () => {
    const a = makeAnchor([], 'x'.repeat(MAX_QUOTE + 50))
    expect(a.quote).toHaveLength(MAX_QUOTE + 1)
    expect(a.quote.endsWith('…')).toBe(true)
  })
})

describe('quotePreview', () => {
  it('shows the quote, shortened', () => {
    expect(quotePreview({ block_ids: [], quote: 'abc' })).toBe('abc')
    expect(quotePreview({ block_ids: [], quote: 'abcdef' }, 3)).toBe('abc…')
  })
  it('falls back to blocks or the whole page', () => {
    expect(quotePreview({ block_ids: ['b1', 'b2'], quote: '' })).toBe('블록 b1, b2')
    expect(quotePreview({ block_ids: [], quote: '' })).toBe('페이지 전체')
  })
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `npm --prefix web test -- src/lib/anchor.test.ts`
Expected: FAIL — cannot resolve `./anchor`.

- [ ] **Step 3: Implement `anchor.ts`**

`web/src/lib/anchor.ts`:

```ts
import type { Anchor } from '../api/types'

export const MAX_QUOTE = 1000

export function makeAnchor(blockIds: string[], quote: string): Anchor {
  const ids = [...new Set(blockIds.filter(Boolean))]
  let q = quote.replace(/\s+/g, ' ').trim()
  if (q.length > MAX_QUOTE) q = q.slice(0, MAX_QUOTE) + '…'
  return { block_ids: ids, quote: q }
}

export function quotePreview(a: Anchor, max = 120): string {
  if (!a.quote) return a.block_ids.length > 0 ? `블록 ${a.block_ids.join(', ')}` : '페이지 전체'
  return a.quote.length > max ? a.quote.slice(0, max) + '…' : a.quote
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `npm --prefix web test -- src/lib/anchor.test.ts`
Expected: PASS (4 tests).

- [ ] **Step 5: Replace `PageImage.tsx` (block click starts a question)**

```tsx
import type { PageView } from '../api/types'

interface Props {
  view: PageView
  hot: string | null
  onHover: (id: string | null) => void
  onAsk: (blockIds: string[], quote: string) => void
}

const pct = (v: number) => `${(v * 100).toFixed(3)}%`

export default function PageImage({ view, hot, onHover, onAsk }: Props) {
  const scaled = view.width > 0 && view.height > 0
  return (
    <div className="page-image">
      <img src={view.image_url} alt={`${view.page_no}쪽 원문`} />
      {scaled &&
        view.blocks.map((b) => (
          <div
            key={b.id}
            className={b.id === hot ? 'bbox hot' : 'bbox'}
            style={{
              left: pct(b.bbox.x / view.width),
              top: pct(b.bbox.y / view.height),
              width: pct(b.bbox.w / view.width),
              height: pct(b.bbox.h / view.height),
            }}
            title="클릭해서 이 블록에 대해 질문"
            onMouseEnter={() => onHover(b.id)}
            onMouseLeave={() => onHover(null)}
            onClick={() => onAsk([b.id], b.text)}
          />
        ))}
    </div>
  )
}
```

- [ ] **Step 6: Replace `TranslationPanel.tsx` (drag-select starts a question)**

```tsx
import { useRef, useState } from 'react'
import type { Block } from '../api/types'

interface Props {
  blocks: Block[]
  translations: Record<string, string>
  generating: boolean
  hot: string | null
  onHover: (id: string | null) => void
  onAsk: (blockIds: string[], quote: string) => void
}

function selectedBlockIds(root: HTMLElement, sel: Selection): string[] {
  if (sel.rangeCount === 0 || sel.isCollapsed) return []
  const range = sel.getRangeAt(0)
  if (!root.contains(range.commonAncestorContainer)) return []
  return Array.from(root.querySelectorAll<HTMLElement>('[data-block-id]'))
    .filter((el) => range.intersectsNode(el))
    .map((el) => el.dataset.blockId ?? '')
}

export default function TranslationPanel({ blocks, translations, generating, hot, onHover, onAsk }: Props) {
  const root = useRef<HTMLDivElement>(null)
  const [picked, setPicked] = useState<{ ids: string[]; text: string } | null>(null)

  const capture = () => {
    const sel = window.getSelection()
    const el = root.current
    if (!sel || !el) return
    const ids = selectedBlockIds(el, sel)
    const text = sel.toString().trim()
    setPicked(ids.length > 0 && text ? { ids, text } : null)
  }

  if (blocks.length === 0) return <p className="muted">이 페이지에는 추출된 텍스트가 없습니다. 해설 탭을 확인하세요.</p>
  return (
    <div ref={root} onMouseUp={capture} onKeyUp={capture}>
      {picked && (
        <button
          className="primary ask-fab"
          onMouseDown={(e) => e.preventDefault()}
          onClick={() => {
            onAsk(picked.ids, picked.text)
            setPicked(null)
            window.getSelection()?.removeAllRanges()
          }}
        >
          💬 선택 영역으로 질문
        </button>
      )}
      {blocks.map((b) => {
        const ko = translations[b.id]
        const cls = ['tblock', b.id === hot ? 'hot' : '', ko ? '' : 'pending'].filter(Boolean).join(' ')
        return (
          <div
            key={b.id}
            data-block-id={b.id}
            className={cls}
            title={b.text}
            onMouseEnter={() => onHover(b.id)}
            onMouseLeave={() => onHover(null)}
          >
            {ko ?? (generating ? '…' : b.text)}
          </div>
        )
      })}
    </div>
  )
}
```

- [ ] **Step 7: Create `AskPanel.tsx`**

```tsx
import { useCallback, useEffect, useRef, useState } from 'react'
import { askContinue, askStart } from '../api/ask'
import { api, errorMessage } from '../api/client'
import type { Anchor, Thread } from '../api/types'
import { quotePreview } from '../lib/anchor'
import MarkdownView from './MarkdownView'

interface Props {
  paperId: string
  pageNo: number
  draft: Anchor | null
  onDraft: (a: Anchor | null) => void
}

interface Pending {
  threadId: string | null
  question: string
  answer: string
}

const PAGE_WIDE: Anchor = { block_ids: [], quote: '' }

export default function AskPanel({ paperId, pageNo, draft, onDraft }: Props) {
  const [threads, setThreads] = useState<Thread[]>([])
  const [error, setError] = useState('')
  const [pending, setPending] = useState<Pending | null>(null)
  const inflight = useRef<AbortController | null>(null)

  const load = useCallback(async () => {
    try {
      setThreads(await api.listThreads(paperId, pageNo))
    } catch (e) {
      setError(errorMessage(e))
    }
  }, [paperId, pageNo])

  useEffect(() => {
    setThreads([])
    setError('')
    void load()
    return () => inflight.current?.abort()
  }, [load])

  /** Sends a question; resolves true when the composer should be cleared. */
  async function ask(threadId: string | null, anchor: Anchor, question: string): Promise<boolean> {
    const q = question.trim()
    if (!q || pending) return false
    const ac = new AbortController()
    inflight.current = ac
    setError('')
    setPending({ threadId, question: q, answer: '' })
    const onDelta = (d: string) => setPending((p) => (p ? { ...p, answer: p.answer + d } : p))
    try {
      const t = threadId
        ? await askContinue(threadId, q, onDelta, ac.signal)
        : await askStart(paperId, pageNo, anchor, q, onDelta, ac.signal)
      setThreads((ts) => (ts.some((x) => x.id === t.id) ? ts.map((x) => (x.id === t.id ? t : x)) : [...ts, t]))
      return true
    } catch (e) {
      if (ac.signal.aborted) return false
      setError(errorMessage(e))
      void load() // the server may have stored the question with a failed answer
      return false
    } finally {
      if (inflight.current === ac) inflight.current = null
      setPending(null)
    }
  }

  return (
    <div>
      {error && <p className="error">{error}</p>}
      {draft ? (
        <div className="thread">
          <div className="quote">{quotePreview(draft)}</div>
          {pending?.threadId === null ? (
            <PendingView p={pending} />
          ) : (
            <Composer
              placeholder="이 부분에 대해 질문하세요"
              busy={pending !== null}
              autoFocus
              onSubmit={async (q) => {
                const ok = await ask(null, draft, q)
                if (ok) onDraft(null)
                return ok
              }}
              onCancel={() => onDraft(null)}
            />
          )}
        </div>
      ) : (
        <p className="muted">
          원문 블록을 클릭하거나 번역을 드래그해서 질문하세요.{' '}
          <button onClick={() => onDraft(PAGE_WIDE)}>페이지 전체에 질문</button>
        </p>
      )}
      {threads.length === 0 && !draft && <p className="muted">이 페이지에는 아직 질문이 없습니다.</p>}
      {threads.map((t) => (
        <div className="thread" key={t.id}>
          <div className="quote">{quotePreview(t.anchor)}</div>
          {t.messages.map((m) =>
            m.role === 'user' ? (
              <div key={m.id} className="msg user">
                Q. {m.content}
              </div>
            ) : (
              <div key={m.id} className="msg">
                {m.content ? <MarkdownView text={m.content} /> : <span className="muted">(빈 답변)</span>}
                {m.status === 'failed' && <span className="error">답변이 완료되지 않았습니다.</span>}
              </div>
            ),
          )}
          {pending?.threadId === t.id && <PendingView p={pending} />}
          <Composer placeholder="이어서 질문" busy={pending !== null} onSubmit={(q) => ask(t.id, t.anchor, q)} />
        </div>
      ))}
    </div>
  )
}

function PendingView({ p }: { p: Pending }) {
  return (
    <>
      <div className="msg user">Q. {p.question}</div>
      <div className="msg">{p.answer ? <MarkdownView text={p.answer} /> : <span className="muted">답변을 생성하는 중…</span>}</div>
    </>
  )
}

interface ComposerProps {
  placeholder: string
  busy: boolean
  autoFocus?: boolean
  onSubmit: (q: string) => Promise<boolean>
  onCancel?: () => void
}

function Composer({ placeholder, busy, autoFocus, onSubmit, onCancel }: ComposerProps) {
  const [text, setText] = useState('')
  const submit = async () => {
    if (await onSubmit(text)) setText('')
  }
  return (
    <div className="composer">
      <textarea
        value={text}
        placeholder={placeholder}
        autoFocus={autoFocus}
        disabled={busy}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
            e.preventDefault()
            void submit()
          }
        }}
      />
      <div>
        <button className="primary" disabled={busy || !text.trim()} onClick={() => void submit()}>
          질문 (⌘+Enter)
        </button>{' '}
        {onCancel && (
          <button onClick={onCancel} disabled={busy}>
            취소
          </button>
        )}
      </div>
    </div>
  )
}
```

- [ ] **Step 8: Wire the 질문 tab into `Reader.tsx`**

Make these edits in `web/src/pages/Reader.tsx`:

1. Imports — replace `import type { Paper } from '../api/types'` with `import type { Anchor, Paper } from '../api/types'`, and add:

```tsx
import AskPanel from '../components/AskPanel'
import { makeAnchor } from '../lib/anchor'
```

2. Replace `type Tab = 'source' | 'translation' | 'explanation'` with:

```tsx
type Tab = 'source' | 'translation' | 'explanation' | 'ask'
```

3. After `const [prefetchNote, setPrefetchNote] = useState('')` add:

```tsx
  const [draft, setDraft] = useState<Anchor | null>(null)
  const startAsk = useCallback((blockIds: string[], quote: string) => {
    setDraft(makeAnchor(blockIds, quote))
    setTab('ask')
  }, [])
```

4. In the `[pageNo]` effect, after `setPrefetchNote('')` add `setDraft(null)`.

5. Replace the `source` constant with:

```tsx
  const source = view ? (
    <PageImage view={view} hot={hot} onHover={setHot} onAsk={startAsk} />
  ) : (
    <p className="muted">불러오는 중…</p>
  )
```

6. Replace the `tabs` array's last entry `['explanation', '해설'],` with:

```tsx
    ['explanation', '해설'],
    ['ask', '질문'],
```

7. Add `onAsk={startAsk}` to the `<TranslationPanel … />` props.

8. After the explanation tab block (`{active === 'explanation' && (…)}`) add — always mounted so an answer keeps streaming while another tab is shown:

```tsx
          <div className="tab-body" hidden={active !== 'ask'}>
            <AskPanel paperId={paperId} pageNo={pageNo} draft={draft} onDraft={setDraft} />
          </div>
```

- [ ] **Step 9: Test, typecheck, build**

Run: `npm --prefix web test && make web-build`
Expected: all tests pass; `✓ built`.

- [ ] **Step 10: Manual check** (real backend, a page with a done result)

- Click a block on the image → 질문 tab opens with that block's text quoted; ask → the answer streams, renders markdown/math, then appears as a thread.
- Drag-select translated text → `💬 선택 영역으로 질문` → quote shows the selection.
- [페이지 전체에 질문] → quote shows `페이지 전체`.
- 이어서 질문 in a thread streams and appends to that thread.
- Switch to 번역 mid-answer and back: the answer kept streaming.
- Change page mid-answer: the new page's 질문 tab is clean; the old thread later shows the interrupted answer with `답변이 완료되지 않았습니다.` (or its partial text).

- [ ] **Step 11: Commit**

```bash
git add web/src
git commit -m "feat(web): anchored Q&A with streamed answers"
```

---

### Task 9: Build integration, docs, end-to-end check

**Files:**
- Create: `README.md`
- Modify: `docs/superpowers/specs/2026-10-16-paper-reader-design.md` (status line)

**Interfaces:**
- Consumes: `make build`, `make run` (Task 2), the whole UI.
- Produces: documented dev/build/run workflow.

- [ ] **Step 1: Write `README.md`**

````markdown
# Paper Reader

PDF 논문을 한 페이지씩 읽으며 블록별 한국어 번역, 페이지 해설, 선택 영역 Q&A를 보는 개인용 웹 앱입니다.
번역과 해설은 Anthropic API가 아니라 `claude -p`(Claude 구독)로 생성합니다.

## 요구 사항

- Go 1.27+, Node 22.22+ (`web/.nvmrc`)
- MuPDF `mutool` 1.23+ (`brew install mupdf-tools`)
- Claude Code CLI (`claude`, 로그인된 상태)
- Postgres, Infisical CLI (`DATABASE_URL`을 Infisical `dev` 환경에 둡니다)

## 개발

```bash
make web-install      # 최초 1회
make dev              # API 서버 :8080 (infisical run --env=dev)
make web-dev          # UI :5173, /api는 :8080으로 프록시
```

## 테스트

```bash
make test-db-up       # 최초 1회: 테스트용 Postgres (docker, :55432)
make test             # Go 전체 (DB 통합 테스트 포함)
make web-test         # 프론트 유틸 (vitest)
```

## 빌드와 실행

```bash
make build            # web 빌드 → Go 바이너리에 embed → backend/bin/paper-reader
make run              # 빌드한 바이너리를 Infisical dev 시크릿으로 실행, http://localhost:8080
```

설계: `docs/superpowers/specs/2026-10-16-paper-reader-design.md`
````

- [ ] **Step 2: Update the spec status**

In `docs/superpowers/specs/2026-10-16-paper-reader-design.md` replace the line `- 상태: 설계 승인됨, 구현 계획 작성 전` with:

```
- 상태: 백엔드·프론트엔드 구현됨 (배포 계획 남음)
```

- [ ] **Step 3: Full verification**

Run: `make build && go -C backend vet ./... && make test && make web-test`
Expected: binary built; vet clean; all Go and web tests pass.

Run (server in background, stop it afterwards): `make run`, then

```bash
curl -s -o /dev/null -w '%{http_code} %{content_type}\n' localhost:8080/
curl -s -o /dev/null -w '%{http_code}\n' localhost:8080/papers/x/1
curl -s localhost:8080/api/nope
```

Expected: `200 text/html; charset=utf-8`, `200`, `{"error":"찾을 수 없음"}`. The asset referenced in the served `index.html` (`/assets/index-*.js`) returns `200` with `Cache-Control: public, max-age=31536000, immutable`.

- [ ] **Step 4: Manual end-to-end in the built binary** (http://localhost:8080, real Claude; uses a few pages of quota)

Upload a paper → open page 1 → watch translation and explanation stream → ask one question on a block → prefetch the next 3 → visit page 2 (already done or promoted) → reload in the middle of a generation (no duplicated text) → delete the paper. Record anything that misbehaves.

- [ ] **Step 5: Commit**

```bash
git add README.md docs/superpowers/specs/2026-10-16-paper-reader-design.md
git commit -m "docs: README and spec status for the frontend"
```
