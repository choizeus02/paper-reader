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
