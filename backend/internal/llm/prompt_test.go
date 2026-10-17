package llm

import (
	"strings"
	"testing"

	"github.com/choizeus02/paper-reader/backend/internal/domain"
)

func TestBuildPagePromptFull(t *testing.T) {
	got, err := BuildPagePrompt(PageContext{
		Title: "Attention Is All You Need", PageNo: 3, PageCount: 15,
		Abstract:    "The dominant sequence transduction models...",
		PrevSummary: "이전 페이지는 배경을 설명한다.",
		Glossary:    []domain.GlossaryTerm{{En: "attention", Ko: "어텐션", Note: "음차"}, {En: "encoder", Ko: "인코더"}},
		Blocks:      []domain.Block{{ID: "b0", Text: "3 Model Architecture"}, {ID: "b1", Text: "Most competitive models..."}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Paper: Attention Is All You Need",
		"Page 3 of 15",
		"The dominant sequence transduction models...",
		"이전 페이지는 배경을 설명한다.",
		"- attention → 어텐션 (음차)",
		"- encoder → 인코더\n",
		"[b0] 3 Model Architecture",
		"[b1] Most competitive models...",
		"@@BLOCK <block id>",
		"@@END",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing %q\n---\n%s", want, got)
		}
	}
}

func TestBuildPagePromptMinimal(t *testing.T) {
	got, err := BuildPagePrompt(PageContext{Title: "T", PageNo: 1, PageCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, absent := range []string{"Paper opening", "Previous page summary", "Glossary ("} {
		if strings.Contains(got, absent) {
			t.Errorf("empty section %q should be omitted\n---\n%s", absent, got)
		}
	}
	if !strings.Contains(got, "(no extractable text") {
		t.Errorf("missing no-text notice\n---\n%s", got)
	}
}

func TestBuildAskPrompt(t *testing.T) {
	got, err := BuildAskPrompt(AskContext{
		Title: "T", PageNo: 2,
		Blocks:      []domain.Block{{ID: "b0", Text: "softmax(QK^T)"}},
		Translation: []domain.BlockTranslation{{BlockID: "b0", Ko: "소프트맥스"}},
		Anchor:      domain.Anchor{BlockIDs: []string{"b0", "b1"}, Quote: "QK^T"},
		History: []domain.Message{
			{Role: domain.RoleUser, Content: "이게 뭐야?"},
			{Role: domain.RoleAssistant, Content: "내적입니다."},
		},
		Question: "왜 루트 d로 나눠?",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Paper: T, page 2",
		"[b0] softmax(QK^T)",
		"[b0] 소프트맥스",
		"Blocks: b0, b1",
		`Quote: "QK^T"`,
		"Reader: 이게 뭐야?",
		"Tutor: 내적입니다.",
		"## Reader's question\n왜 루트 d로 나눠?",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing %q\n---\n%s", want, got)
		}
	}
}
