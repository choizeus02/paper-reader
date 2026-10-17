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
