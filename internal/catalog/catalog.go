package catalog

import (
	"context"
	"errors"
	"time"

	"insurance_agent_demo/internal/document"
)

const (
	StatusChunked   = "chunked"
	StatusEmbedding = "embedding"
	StatusReady     = "ready"
	StatusFailed    = "failed"
)

type StoreOutcome string

const (
	OutcomeInserted StoreOutcome = "inserted"
	OutcomeSkipped  StoreOutcome = "skipped"
	OutcomeReplaced StoreOutcome = "replaced"
)

var ErrNotFound = errors.New("记录不存在")

type StoreResult struct {
	DocumentID string
	Outcome    StoreOutcome
	Status     string
	ChunkCount int
}

type StoredDocument struct {
	ID               string
	ProductID        string
	Name             string
	Version          string
	SourceURL        string
	SourceFilePath   string
	SourceSHA256     string
	SourceImportedAt time.Time
	SourceParser     string
	TotalPages       int
	Chunking         document.ChunkingConfig
	TotalChunks      int
	ProcessingStatus string
	FailedStage      string
	LastError        string
	ChunkedAt        time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type StoredChunk struct {
	ID             string
	DocumentID     string
	ChunkIndex     int
	PageNumber     int
	SectionTitle   string
	ClauseID       string
	PartIndex      int
	PartCount      int
	CharacterCount int
	Text           string
}

type ListFilter struct {
	ProductID string
	Status    string
	Limit     int
	Offset    int
}

type Repository interface {
	Store(context.Context, document.ChunkedDocument, bool) (StoreResult, error)
	GetDocument(context.Context, string) (StoredDocument, error)
	ListDocuments(context.Context, ListFilter) ([]StoredDocument, error)
	ListChunks(context.Context, string) ([]StoredChunk, error)
}

type Service struct {
	Repository Repository
}

func (s Service) Store(ctx context.Context, input document.ChunkedDocument, replace bool) (StoreResult, error) {
	if s.Repository == nil {
		return StoreResult{}, errors.New("未配置文档仓库")
	}
	if err := ValidateChunkedDocument(input); err != nil {
		return StoreResult{}, err
	}
	return s.Repository.Store(ctx, input, replace)
}

func (s Service) GetDocument(ctx context.Context, id string) (StoredDocument, error) {
	if s.Repository == nil {
		return StoredDocument{}, errors.New("未配置文档仓库")
	}
	return s.Repository.GetDocument(ctx, id)
}

func (s Service) ListDocuments(ctx context.Context, filter ListFilter) ([]StoredDocument, error) {
	if s.Repository == nil {
		return nil, errors.New("未配置文档仓库")
	}
	return s.Repository.ListDocuments(ctx, filter)
}

func (s Service) ListChunks(ctx context.Context, documentID string) ([]StoredChunk, error) {
	if s.Repository == nil {
		return nil, errors.New("未配置文档仓库")
	}
	return s.Repository.ListChunks(ctx, documentID)
}
