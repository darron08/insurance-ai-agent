package httpapi

import (
	"context"
	"errors"
	"strings"
	"testing"

	"insurance_agent_demo/internal/catalog"
	"insurance_agent_demo/internal/rag"
)

type directoryFunc func(context.Context) ([]catalog.Product, error)

func (f directoryFunc) ListProducts(ctx context.Context) ([]catalog.Product, error) { return f(ctx) }

func TestProductsAndErrors(t *testing.T) {
	for _, tc := range []struct {
		name     string
		items    []catalog.Product
		err      error
		status   int
		contains string
	}{
		{"empty", nil, nil, 200, `"products":[]`},
		{"legacy", []catalog.Product{{ProductID: "p", Versions: []catalog.ProductVersion{{Version: "", DocumentCount: 1, Compatible: true}}}}, nil, 200, `"version":""`},
		{"database", nil, errors.New("password=secret SQL"), 503, `storage_unavailable`},
		{"timeout", nil, context.DeadlineExceeded, 504, `timeout`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := New(rag.Service{}, nil, Options{Products: directoryFunc(func(context.Context) ([]catalog.Product, error) { return tc.items, tc.err })})
			w := request(h, "GET", "/api/rag/products", "")
			if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.contains) || strings.Contains(w.Body.String(), "secret") {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}
