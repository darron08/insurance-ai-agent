package catalog

import (
	"context"
	"errors"

	"insurance_agent_demo/internal/indexing"
)

// ProductDirectory is separate from document administration and has no pagination:
// a version choice must not silently omit documents that make it incompatible.
type ProductDirectory interface {
	ListProducts(context.Context, indexing.Profile) ([]Product, error)
}

type Product struct {
	ProductID string           `json:"product_id"`
	Name      *string          `json:"name"`
	Versions  []ProductVersion `json:"versions"`
}

type ProductVersion struct {
	Version       string `json:"version"`
	DocumentCount int    `json:"document_count"`
	Compatible    bool   `json:"compatible"`
}

type DirectoryService struct {
	Repository ProductDirectory
	Profile    indexing.Profile
}

func (s DirectoryService) ListProducts(ctx context.Context) ([]Product, error) {
	profile, err := indexing.NewProfile(s.Profile.BaseURL, s.Profile.Model)
	if err != nil || profile != s.Profile {
		return nil, errors.New("目录服务的 Embedding 配置无效")
	}
	if s.Repository == nil {
		return nil, errors.New("未配置产品目录仓库")
	}
	products, err := s.Repository.ListProducts(ctx, profile)
	if products == nil && err == nil {
		products = []Product{}
	}
	return products, err
}
