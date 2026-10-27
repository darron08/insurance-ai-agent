// Package retrieval implements M5 product-scoped vector search without SDK or SQL types.
package retrieval

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"insurance_agent_demo/internal/indexing"
)

const (
	DefaultTopK   = 5
	ModeHNSW      = "hnsw"
	ModeExact     = "exact"
	QueryTemplate = "qwen3-insurance-v1"
)

var (
	ErrNoReadyDocuments     = errors.New("所选产品或版本没有可检索的 ready 文档，请先完成索引")
	ErrProfileMismatch      = errors.New("所选版本的 Embedding 配置与查询不同，请使用索引时的配置或重新索引")
	ErrEmbeddingUnavailable = errors.New("Query Embedding 服务不可用")
)

// VersionRequiredError includes every ready version, including the legacy empty version.
type VersionRequiredError struct{ Versions []string }

func (e *VersionRequiredError) Error() string {
	return fmt.Sprintf("存在多个可检索版本，请指定 --version（空版本使用 --version=''）：%q", e.Versions)
}

type Request struct {
	ProductID string
	Query     string
	// nil selects the sole ready version; a pointer to "" selects the legacy version.
	Version *string
	// Zero values choose DefaultTopK and ModeHNSW for application callers.
	TopK int
	Mode string
}

// NormalizeRequest is shared by the service and CLI so invalid input fails before I/O.
func NormalizeRequest(req Request) (Request, error) {
	if strings.TrimSpace(req.ProductID) == "" {
		return req, errors.New("缺少参数 --product-id")
	}
	if req.ProductID != strings.TrimSpace(req.ProductID) {
		return req, errors.New("product-id 不能包含首尾空白")
	}
	req.Query = strings.TrimSpace(req.Query)
	if req.Query == "" {
		return req, errors.New("--query 不能为空")
	}
	if req.Version != nil {
		version := *req.Version
		if version != strings.TrimSpace(version) {
			return req, errors.New("version 不能包含首尾空白")
		}
		req.Version = &version
	}
	if req.TopK == 0 {
		req.TopK = DefaultTopK
	}
	if req.TopK < 1 || req.TopK > 100 {
		return req, errors.New("top-k 必须在 1 到 100 之间")
	}
	if req.Mode == "" {
		req.Mode = ModeHNSW
	}
	if req.Mode != ModeHNSW && req.Mode != ModeExact {
		return req, errors.New("mode 必须为 hnsw 或 exact")
	}
	return req, nil
}

type Hit struct {
	ChunkID         string  `json:"chunk_id"`
	DocumentID      string  `json:"document_id"`
	ProductID       string  `json:"product_id"`
	DocumentName    string  `json:"document_name"`
	DocumentVersion string  `json:"document_version"`
	SourceURL       string  `json:"source_url"`
	SourceSHA256    string  `json:"source_sha256"`
	PageNumber      int     `json:"page_number"`
	ChunkIndex      int     `json:"chunk_index"`
	SectionTitle    string  `json:"section_title"`
	ClauseID        string  `json:"clause_id"`
	PartIndex       int     `json:"part_index"`
	PartCount       int     `json:"part_count"`
	Text            string  `json:"text"`
	Distance        float64 `json:"distance"`
	Score           float64 `json:"score"`
}

type Result struct {
	Query            string `json:"query"`
	ProductID        string `json:"product_id"`
	Version          string `json:"version"`
	Mode             string `json:"mode"`
	Model            string `json:"model"`
	EmbeddingProfile string `json:"embedding_profile"`
	QueryTemplate    string `json:"query_template"`
	TopK             int    `json:"top_k"`
	Hits             []Hit  `json:"hits"`
}

type QueryEmbedder interface {
	EmbedQuery(context.Context, string) ([]float64, error)
}

// SearchParams carries a validated request and vector into the persistence boundary.
type SearchParams struct {
	Request Request
	Profile indexing.Profile
	Vector  []float64
}

type Repository interface {
	Search(context.Context, SearchParams) (Result, error)
}

type Service struct {
	Repository Repository
	Embedder   QueryEmbedder
	Profile    indexing.Profile
}

func (s Service) Search(ctx context.Context, req Request) (Result, error) {
	req, err := NormalizeRequest(req)
	if err != nil {
		return Result{}, err
	}
	p, err := indexing.NewProfile(s.Profile.BaseURL, s.Profile.Model)
	if err != nil {
		return Result{}, err
	}
	if p != s.Profile {
		return Result{}, errors.New("检索服务必须使用规范化的 1024 维模型配置")
	}
	if s.Repository == nil || s.Embedder == nil {
		return Result{}, errors.New("未配置检索仓库或 Query Embedding 服务")
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	vector, err := s.Embedder.EmbedQuery(ctx, req.Query)
	if err != nil {
		return Result{}, fmt.Errorf("生成 Query Embedding: %w: %w", ErrEmbeddingUnavailable, err)
	}
	if err := indexing.ValidateVectors([][]float64{vector}, 1); err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	result, err := s.Repository.Search(ctx, SearchParams{Request: req, Profile: p, Vector: vector})
	if err != nil {
		return Result{}, fmt.Errorf("检索条款: %w", err)
	}
	result.Query, result.ProductID, result.Mode, result.TopK = req.Query, req.ProductID, req.Mode, req.TopK
	result.Model, result.EmbeddingProfile, result.QueryTemplate = p.Model, p.Key(), QueryTemplate
	if result.Hits == nil {
		result.Hits = []Hit{}
	}
	return result, nil
}
