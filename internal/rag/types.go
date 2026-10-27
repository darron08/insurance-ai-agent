// Package rag implements evidence-grounded answers without SDK or database types.
package rag

import (
	"context"
	"errors"
	"time"

	"insurance_agent_demo/internal/retrieval"
)

const (
	PromptVersion  = "insurance-rag-v1"
	ContextBudget  = 12000 // Unicode characters of complete Chunk bodies.
	RequestTimeout = 120 * time.Second
	DefaultBaseURL = "https://api.deepseek.com"
	DefaultModel   = "deepseek-flash"
	Answered       = "answered"
	Insufficient   = "insufficient_evidence"
	Clarification  = "needs_clarification"
	Conflict       = "conflicting_evidence"
	OutOfScope     = "out_of_scope"
	FormalDecision = "formal_decision"
)

var ErrRunNotFound = errors.New("运行记录不存在")

// Error contains only public error information; Cause is never serialized.
type Error struct {
	Code     string   `json:"code"`
	Message  string   `json:"message"`
	RunID    string   `json:"run_id,omitempty"`
	Stage    string   `json:"stage,omitempty"`
	Versions []string `json:"versions,omitempty"`
	Cause    error    `json:"-"`
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.Cause }

type Request struct {
	ProductID string  `json:"product_id"`
	Question  string  `json:"question"`
	Version   *string `json:"version"`
	TopK      int     `json:"top_k"`
	Mode      string  `json:"mode"`
}

// Config is safe to persist; credentials never belong to this type.
type Config struct {
	BaseURL       string  `json:"base_url"`
	Model         string  `json:"model"`
	PromptVersion string  `json:"prompt_version"`
	ContextBudget int     `json:"context_budget"`
	Temperature   float32 `json:"temperature"`
	MaxTokens     int     `json:"max_tokens"`
	Thinking      string  `json:"thinking"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Evidence struct {
	ID  string        `json:"id"`
	Hit retrieval.Hit `json:"hit"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Generation stores final content only, never reasoning_content or HTTP headers.
type Generation struct {
	Content      string `json:"content"`
	Model        string `json:"model"`
	FinishReason string `json:"finish_reason"`
	Usage        *Usage `json:"usage"`
}

type Reference struct {
	EvidenceID string `json:"evidence_id"`
	Quote      string `json:"quote"`
}
type Statement struct {
	Text     string      `json:"text"`
	Evidence []Reference `json:"evidence"`
}

// Decision is the model's contract, distinct from the public rendered answer.
type Decision struct {
	Status             string      `json:"status"`
	Statements         []Statement `json:"statements"`
	MissingInformation []string    `json:"missing_information"`
}
type Citation struct {
	ID         int           `json:"id"`
	EvidenceID string        `json:"evidence_id"`
	Quote      string        `json:"quote"`
	Source     retrieval.Hit `json:"source"`
}
type Timings struct {
	SearchMS     int64 `json:"search_ms"`
	GenerationMS int64 `json:"generation_ms"`
	TotalMS      int64 `json:"total_ms"`
}
type Result struct {
	RunID      string     `json:"run_id"`
	ProductID  string     `json:"product_id"`
	Version    string     `json:"version"`
	Answer     string     `json:"answer"`
	Citations  []Citation `json:"citations"`
	Abstained  bool       `json:"abstained"`
	Reason     string     `json:"reason,omitempty"`
	Usage      *Usage     `json:"usage"` // Generation only; null means unavailable.
	UsageScope string     `json:"usage_scope"`
	Timings    Timings    `json:"timings"`
}
type Run struct {
	ID              string            `json:"run_id"`
	Status          string            `json:"status"`
	StartedAt       time.Time         `json:"started_at"`
	FinishedAt      *time.Time        `json:"finished_at"`
	Request         Request           `json:"request"`
	Config          Config            `json:"config"`
	Retrieval       *retrieval.Result `json:"retrieval"`
	Evidence        []Evidence        `json:"evidence"`
	OmittedChunkIDs []string          `json:"omitted_chunk_ids"`
	Messages        []Message         `json:"messages"`
	Generation      *Generation       `json:"generation"`
	Validation      string            `json:"validation"`
	Result          *Result           `json:"result"`
	Timings         Timings           `json:"timings"`
	Error           *Error            `json:"error"`
}

type Searcher interface {
	Search(context.Context, retrieval.Request) (retrieval.Result, error)
}
type Generator interface {
	Generate(context.Context, []Message) (Generation, error)
}
type RunRepository interface {
	Create(context.Context, Run) error
	Save(context.Context, Run) error
	Get(context.Context, string) (Run, error)
}
