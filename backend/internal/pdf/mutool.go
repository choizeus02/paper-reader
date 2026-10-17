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
