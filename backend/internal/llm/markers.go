package llm

import (
	"errors"
	"strings"

	"github.com/choizeus02/paper-reader/backend/internal/domain"
)

var ErrIncompleteOutput = errors.New("출력이 완전하지 않음")

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
	sawExpl bool
	sawSum  bool
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
	if !p.sawEnd || !p.sawExpl || !p.sawSum {
		return PageOutput{}, ErrIncompleteOutput
	}
	if p.out.Translation == nil {
		p.out.Translation = []domain.BlockTranslation{}
	}
	if p.out.Glossary == nil {
		p.out.Glossary = []domain.GlossaryTerm{}
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
		p.sawExpl = true
		return
	case marker == "@@SUMMARY":
		p.closeSection()
		p.sec = secSummary
		p.sawSum = true
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
