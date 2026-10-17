// Package domain holds the core types shared across layers.
package domain

import (
	"errors"
	"time"
)

var (
	ErrNotFound = errors.New("찾을 수 없음")
	ErrInvalid  = errors.New("잘못된 요청")
)

type PaperStatus string

const (
	PaperProcessing PaperStatus = "processing"
	PaperReady      PaperStatus = "ready"
	PaperFailed     PaperStatus = "failed"
)

type Paper struct {
	ID        string      `json:"id"`
	Title     string      `json:"title"`
	Filename  string      `json:"filename"`
	PageCount int         `json:"page_count"`
	Status    PaperStatus `json:"status"`
	Error     string      `json:"error,omitempty"`
	DonePages int         `json:"done_pages"`
	CreatedAt time.Time   `json:"created_at"`
}

// BBox is in PDF points with a top-left origin.
type BBox struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

type Block struct {
	ID   string `json:"id"`
	BBox BBox   `json:"bbox"`
	Text string `json:"text"`
}

type Page struct {
	PaperID   string  `json:"paper_id"`
	PageNo    int     `json:"page_no"`
	Width     float64 `json:"width"`
	Height    float64 `json:"height"`
	ImagePath string  `json:"-"` // relative to DATA_DIR, slash-separated
	Blocks    []Block `json:"blocks"`
}

type ResultStatus string

const (
	ResultPending ResultStatus = "pending"
	ResultRunning ResultStatus = "running"
	ResultDone    ResultStatus = "done"
	ResultFailed  ResultStatus = "failed"
)

type BlockTranslation struct {
	BlockID string `json:"block_id"`
	Ko      string `json:"ko"`
}

type PageResult struct {
	PaperID       string             `json:"-"`
	PageNo        int                `json:"-"`
	Status        ResultStatus       `json:"status"`
	Translation   []BlockTranslation `json:"translation"`
	Explanation   string             `json:"explanation"`
	PageSummary   string             `json:"page_summary"`
	PromptVersion int                `json:"prompt_version"`
	Error         string             `json:"error,omitempty"`
	UpdatedAt     time.Time          `json:"updated_at"`
}

type GlossaryTerm struct {
	En   string `json:"en"`
	Ko   string `json:"ko"`
	Note string `json:"note,omitempty"`
}

type Anchor struct {
	BlockIDs []string `json:"block_ids"`
	Quote    string   `json:"quote"`
}

type Thread struct {
	ID        string    `json:"id"`
	PaperID   string    `json:"paper_id"`
	PageNo    int       `json:"page_no"`
	Anchor    Anchor    `json:"anchor"`
	CreatedAt time.Time `json:"created_at"`
	Messages  []Message `json:"messages"`
}

type MessageRole string

const (
	RoleUser      MessageRole = "user"
	RoleAssistant MessageRole = "assistant"
)

type MessageStatus string

const (
	MessageDone      MessageStatus = "done"
	MessageStreaming MessageStatus = "streaming"
	MessageFailed    MessageStatus = "failed"
)

type Message struct {
	ID        int64         `json:"id"`
	ThreadID  string        `json:"-"`
	Role      MessageRole   `json:"role"`
	Content   string        `json:"content"`
	Status    MessageStatus `json:"status"`
	CreatedAt time.Time     `json:"created_at"`
}
