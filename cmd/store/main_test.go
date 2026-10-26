package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"insurance_agent_demo/internal/document"
)

func writeChunkedJSON(t *testing.T) string {
	t.Helper()
	text := "第一条 保险责任。"
	sum := sha256.Sum256([]byte("fixture"))
	metadata := document.DocumentMetadata{ProductID: "demo", Name: "测试条款", SourceURL: "https://example.com/policy"}
	doc := document.ChunkedDocument{
		Metadata: metadata, SourceFilePath: "/tmp/policy.pdf",
		SourceSHA256: hex.EncodeToString(sum[:]), SourceImportedAt: time.Now().UTC(),
		SourceParser: "test-parser@1", TotalPages: 1,
		Chunking:    document.ChunkingConfig{Strategy: "clause-aware-v1", MaxCharacters: 800, OverlapCharacters: 100},
		TotalChunks: 1, ChunkedAt: time.Now().UTC(),
		Chunks: []document.Chunk{{
			ChunkIndex: 1, ProductID: metadata.ProductID, DocumentName: metadata.Name,
			PageNumber: 1, PartIndex: 1, PartCount: 1,
			CharacterCount: utf8.RuneCountInString(text), Text: text,
		}},
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "chunks.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunValidation(t *testing.T) {
	tests := []struct {
		name string
		args func(*testing.T) []string
		code int
		want string
	}{
		{"help", func(*testing.T) []string { return []string{"--help"} }, 0, "Usage"},
		{"position", func(*testing.T) []string { return []string{"input.json"} }, 2, "位置参数"},
		{"missing input", func(*testing.T) []string { return nil }, 1, "--input"},
		{"missing database", func(t *testing.T) []string { return []string{"--input", writeChunkedJSON(t)} }, 1, "DATABASE_URL"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(tc.args(t), &stdout, &stderr, func(string) string { return "" })
			combined := stdout.String() + stderr.String()
			if code != tc.code || !strings.Contains(combined, tc.want) {
				t.Fatalf("code = %d, output = %q", code, combined)
			}
		})
	}
}

func TestRunDoesNotExposeDatabaseURL(t *testing.T) {
	secret := "postgres://secret-user:secret-password@127.0.0.1:1/missing?sslmode=disable"
	var stdout, stderr bytes.Buffer
	code := run([]string{"--input", writeChunkedJSON(t)}, &stdout, &stderr, func(string) string { return secret })
	combined := stdout.String() + stderr.String()
	if code != 1 || strings.Contains(combined, "secret-user") || strings.Contains(combined, "secret-password") {
		t.Fatalf("code = %d, output leaked credentials: %q", code, combined)
	}
}
