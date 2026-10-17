package llm

import (
	_ "embed"
	"strings"
	"text/template"

	"github.com/choizeus02/paper-reader/backend/internal/domain"
)

// PagePromptVersion is stored with each page result; bump it when page.tmpl
// or PageSystem changes meaningfully.
const PagePromptVersion = 1

const PageSystem = "You are an expert translator and tutor for academic papers. " +
	"You translate English research papers into natural, precise Korean and explain them to a graduate-level reader. " +
	"You always follow the requested output format exactly."

const AskSystem = "You are a patient tutor helping a reader understand an academic paper. " +
	"Answer in Korean using Markdown. Write math in LaTeX with $...$ (inline) or $$...$$ (display). " +
	"On first use of a technical term, add the English term in parentheses."

var (
	//go:embed prompts/page.tmpl
	pageTmplText string
	//go:embed prompts/ask.tmpl
	askTmplText string

	pageTmpl = template.Must(template.New("page").Parse(pageTmplText))
	askTmpl  = template.Must(template.New("ask").Parse(askTmplText))
)

type PageContext struct {
	Title       string
	PageNo      int
	PageCount   int
	Abstract    string
	PrevSummary string
	Glossary    []domain.GlossaryTerm
	Blocks      []domain.Block
}

func BuildPagePrompt(c PageContext) (string, error) {
	var b strings.Builder
	err := pageTmpl.Execute(&b, c)
	return b.String(), err
}

type AskContext struct {
	Title       string
	PageNo      int
	Blocks      []domain.Block
	Translation []domain.BlockTranslation
	Anchor      domain.Anchor
	History     []domain.Message
	Question    string
}

func BuildAskPrompt(c AskContext) (string, error) {
	var b strings.Builder
	err := askTmpl.Execute(&b, c)
	return b.String(), err
}
