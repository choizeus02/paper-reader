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
