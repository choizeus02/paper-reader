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
	for _, want := range []string{
		"-p\n",
		"--input-format\nstream-json\n",
		"--output-format\nstream-json\n",
		"--verbose\n",
		"--include-partial-messages\n",
		"--tools\n\n",
		"--no-session-persistence\n",
		"--strict-mcp-config\n",
		"--setting-sources\n\n",
		"--system-prompt\nSYS\n",
		"--model\nsonnet\n",
	} {
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
