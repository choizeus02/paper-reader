package llm

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/choizeus02/paper-reader/backend/internal/domain"
)

const sampleOutput = `Sure, here it is.
@@BLOCK b0
3. 방법
@@BLOCK b1
우리는 트랜스포머를 채택한다.
두 번째 줄.
@@BLOCK b2

@@EXPLANATION
## 이 페이지 요지
요지 내용
@@SUMMARY
방법을 설명한다.
@@GLOSSARY
attention | 어텐션 | 번역하지 않음
transformer | 트랜스포머
broken line without pipes
 | 빈영어
@@END
trailing junk`

func feed(p *MarkerParser, s string, size int) {
	for i := 0; i < len(s); i += size {
		end := i + size
		if end > len(s) {
			end = len(s)
		}
		p.Write(s[i:end])
	}
}

func TestMarkerParserChunkSizes(t *testing.T) {
	for _, size := range []int{1, 3, 7, 64, len(sampleOutput)} {
		var blocks []domain.BlockTranslation
		var expl strings.Builder
		p := NewMarkerParser(func(b domain.BlockTranslation) { blocks = append(blocks, b) }, func(d string) { expl.WriteString(d) })
		feed(p, sampleOutput, size)
		out, err := p.Finish()
		if err != nil {
			t.Fatalf("size %d: %v", size, err)
		}
		wantBlocks := []domain.BlockTranslation{
			{BlockID: "b0", Ko: "3. 방법"},
			{BlockID: "b1", Ko: "우리는 트랜스포머를 채택한다.\n두 번째 줄."},
		}
		if !reflect.DeepEqual(out.Translation, wantBlocks) || !reflect.DeepEqual(blocks, wantBlocks) {
			t.Fatalf("size %d: blocks out=%#v streamed=%#v", size, out.Translation, blocks)
		}
		if out.Explanation != "## 이 페이지 요지\n요지 내용" {
			t.Fatalf("size %d: explanation %q", size, out.Explanation)
		}
		if expl.String() != "## 이 페이지 요지\n요지 내용\n" {
			t.Fatalf("size %d: streamed explanation %q", size, expl.String())
		}
		if out.Summary != "방법을 설명한다." {
			t.Fatalf("size %d: summary %q", size, out.Summary)
		}
		wantGloss := []domain.GlossaryTerm{{En: "attention", Ko: "어텐션", Note: "번역하지 않음"}, {En: "transformer", Ko: "트랜스포머"}}
		if !reflect.DeepEqual(out.Glossary, wantGloss) {
			t.Fatalf("size %d: glossary %#v", size, out.Glossary)
		}
	}
}

func TestMarkerParserMissingEnd(t *testing.T) {
	p := NewMarkerParser(nil, nil)
	p.Write("@@BLOCK b0\n번역\n@@EXPLANATION\n설명")
	if _, err := p.Finish(); !errors.Is(err, ErrIncompleteOutput) {
		t.Fatalf("want ErrIncompleteOutput, got %v", err)
	}
}

func TestMarkerParserNoBlocksStillValid(t *testing.T) {
	p := NewMarkerParser(nil, nil)
	p.Write("@@EXPLANATION\n그림만 있는 페이지\n@@SUMMARY\n그림\n@@GLOSSARY\n@@END")
	out, err := p.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if out.Translation == nil || len(out.Translation) != 0 || out.Explanation != "그림만 있는 페이지" {
		t.Fatalf("unexpected: %#v", out)
	}
}

func TestMarkerParserCRLF(t *testing.T) {
	p := NewMarkerParser(nil, nil)
	p.Write("@@BLOCK b0\r\n안녕\r\n@@EXPLANATION\r\n설명\r\n@@END\r\n")
	out, err := p.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if out.Translation[0].Ko != "안녕" || out.Explanation != "설명" {
		t.Fatalf("unexpected: %#v", out)
	}
}
