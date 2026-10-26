package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"insurance_agent_demo/internal/document"
)

func validChunkedDocument() document.ChunkedDocument {
	text := "第一条 保险责任。"
	sum := sha256.Sum256([]byte("fixture"))
	metadata := document.DocumentMetadata{
		ProductID: "demo",
		Name:      "测试条款",
		Version:   "v1",
		SourceURL: "https://example.com/policy",
	}
	return document.ChunkedDocument{
		Metadata:         metadata,
		SourceFilePath:   "/tmp/policy.pdf",
		SourceSHA256:     hex.EncodeToString(sum[:]),
		SourceImportedAt: time.Date(2026, 10, 26, 1, 0, 0, 0, time.UTC),
		SourceParser:     "test-parser@1",
		TotalPages:       1,
		Chunking: document.ChunkingConfig{
			Strategy:          "clause-aware-v1",
			MaxCharacters:     800,
			OverlapCharacters: 100,
		},
		TotalChunks: 1,
		Chunks: []document.Chunk{{
			ChunkIndex:      1,
			ProductID:       metadata.ProductID,
			DocumentName:    metadata.Name,
			DocumentVersion: metadata.Version,
			PageNumber:      1,
			SectionTitle:    "保险责任",
			ClauseID:        "第一条",
			PartIndex:       1,
			PartCount:       1,
			CharacterCount:  utf8.RuneCountInString(text),
			Text:            text,
		}},
		ChunkedAt: time.Date(2026, 10, 26, 2, 0, 0, 0, time.UTC),
	}
}

func TestValidateChunkedDocument(t *testing.T) {
	if err := ValidateChunkedDocument(validChunkedDocument()); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*document.ChunkedDocument)
		want   string
	}{
		{"missing product", func(d *document.ChunkedDocument) { d.Metadata.ProductID = "" }, "product_id"},
		{"bad hash", func(d *document.ChunkedDocument) { d.SourceSHA256 = "bad" }, "source_sha256"},
		{"bad pages", func(d *document.ChunkedDocument) { d.Chunks[0].PageNumber = 2 }, "页码越界"},
		{"bad index", func(d *document.ChunkedDocument) { d.Chunks[0].ChunkIndex = 2 }, "连续排列"},
		{"bad count", func(d *document.ChunkedDocument) { d.Chunks[0].CharacterCount++ }, "character_count"},
		{"bad parent", func(d *document.ChunkedDocument) { d.Chunks[0].DocumentVersion = "v2" }, "元数据不一致"},
		{"replacement", func(d *document.ChunkedDocument) {
			d.Chunks[0].Text = "保险�条款"
			d.Chunks[0].CharacterCount = utf8.RuneCountInString(d.Chunks[0].Text)
		}, "U+FFFD"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc := validChunkedDocument()
			tc.mutate(&doc)
			if err := ValidateChunkedDocument(doc); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestLoadChunkedDocumentIsStrict(t *testing.T) {
	doc := validChunkedDocument()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "chunks.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadChunkedDocument(path)
	if err != nil || loaded.SourceSHA256 != doc.SourceSHA256 {
		t.Fatalf("loaded = %+v, error = %v", loaded, err)
	}

	unknown := append(append([]byte{}, raw[:len(raw)-1]...), []byte(`,"unknown":true}`)...)
	if err := os.WriteFile(path, unknown, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadChunkedDocument(path); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("unknown field error = %v", err)
	}
	if err := os.WriteFile(path, append(raw, []byte("\n{}")...), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadChunkedDocument(path); err == nil || !strings.Contains(err.Error(), "多个 JSON") {
		t.Fatalf("trailing value error = %v", err)
	}
}

type fakeRepository struct {
	called bool
	result StoreResult
	err    error
}

func (f *fakeRepository) Store(context.Context, document.ChunkedDocument, bool) (StoreResult, error) {
	f.called = true
	return f.result, f.err
}
func (*fakeRepository) GetDocument(context.Context, string) (StoredDocument, error) {
	return StoredDocument{}, ErrNotFound
}
func (*fakeRepository) ListDocuments(context.Context, ListFilter) ([]StoredDocument, error) {
	return nil, nil
}
func (*fakeRepository) ListChunks(context.Context, string) ([]StoredChunk, error) {
	return nil, nil
}

func TestServiceValidatesBeforeRepository(t *testing.T) {
	repo := &fakeRepository{result: StoreResult{DocumentID: "id", Outcome: OutcomeInserted}}
	service := Service{Repository: repo}
	bad := validChunkedDocument()
	bad.TotalChunks = 2
	if _, err := service.Store(context.Background(), bad, false); err == nil || repo.called {
		t.Fatalf("error = %v, called = %v", err, repo.called)
	}
	wantErr := errors.New("store failed")
	repo.err = wantErr
	if _, err := service.Store(context.Background(), validChunkedDocument(), false); !errors.Is(err, wantErr) || !repo.called {
		t.Fatalf("error = %v, called = %v", err, repo.called)
	}
}
