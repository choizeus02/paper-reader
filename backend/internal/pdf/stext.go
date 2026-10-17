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
