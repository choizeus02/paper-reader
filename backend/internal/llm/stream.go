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
