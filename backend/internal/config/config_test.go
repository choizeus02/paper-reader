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
