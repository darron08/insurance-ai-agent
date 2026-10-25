package chunker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"insurance_agent_demo/internal/document"
)

func writeDocument(t *testing.T, doc document.Document) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "import.json")
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestServiceWritesAtomicIndependentResults(t *testing.T) {
	doc := testDocument("保险责任\n第一条 保险正文。")
	input := writeDocument(t, doc)
	outputDir := filepath.Join(t.TempDir(), "chunks")
	fixed := time.Date(2026, 10, 25, 2, 0, 0, 0, time.UTC)
	service := Service{Splitter: ClauseAware{Now: func() time.Time { return fixed }}}

	first, firstPath, err := service.Run(input, outputDir)
	if err != nil {
		t.Fatal(err)
	}
	second, secondPath, err := service.Run(input, outputDir)
	if err != nil {
		t.Fatal(err)
	}
	if firstPath == secondPath || first.TotalChunks == 0 || second.TotalChunks != first.TotalChunks {
		t.Fatalf("repeat = %q / %q, results %+v / %+v", firstPath, secondPath, first, second)
	}
	entries, err := os.ReadDir(outputDir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries = %v, error = %v", entries, err)
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" || strings.HasPrefix(entry.Name(), ".") {
			t.Fatalf("unexpected output entry %q", entry.Name())
		}
	}
	raw, err := os.ReadFile(firstPath)
	if err != nil {
		t.Fatal(err)
	}
	var saved document.ChunkedDocument
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.SourceSHA256 != doc.SHA256 || saved.TotalChunks != len(saved.Chunks) || !saved.ChunkedAt.Equal(fixed) {
		t.Fatalf("saved = %+v", saved)
	}
}

func TestServiceRejectsBadInputsWithoutOutput(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T) string
		want  string
	}{
		{name: "missing", setup: func(t *testing.T) string { return filepath.Join(t.TempDir(), "missing.json") }, want: "打开输入"},
		{name: "extension", setup: func(t *testing.T) string { return filepath.Join(t.TempDir(), "input.txt") }, want: ".json"},
		{name: "invalid json", setup: func(t *testing.T) string {
			path := filepath.Join(t.TempDir(), "input.json")
			if err := os.WriteFile(path, []byte("not-json"), 0600); err != nil {
				t.Fatal(err)
			}
			return path
		}, want: "读取 M1 JSON"},
		{name: "unknown field", setup: func(t *testing.T) string {
			path := writeDocument(t, testDocument("正文"))
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			raw = append(raw[:len(raw)-1], []byte(`,"unexpected":true}`)...)
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			return path
		}, want: "unknown field"},
		{name: "trailing value", setup: func(t *testing.T) string {
			path := writeDocument(t, testDocument("正文"))
			f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.WriteString("\n{}"); err != nil {
				t.Fatal(err)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			return path
		}, want: "多个 JSON"},
		{name: "quality", setup: func(t *testing.T) string {
			doc := testDocument("正文")
			doc.Quality.Status = "failed"
			return writeDocument(t, doc)
		}, want: "质量状态"},
		{name: "replacement", setup: func(t *testing.T) string {
			doc := testDocument("保险�条款")
			return writeDocument(t, doc)
		}, want: "U+FFFD"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "output")
			_, _, err := (Service{}).Run(tc.setup(t), output)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
				t.Fatalf("failed run created output: %v", statErr)
			}
		})
	}
}

func TestServiceReportsOutputFailure(t *testing.T) {
	input := writeDocument(t, testDocument("正文"))
	output := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(output, []byte("occupied"), 0600); err != nil {
		t.Fatal(err)
	}
	_, _, err := (Service{}).Run(input, output)
	if err == nil || !strings.Contains(err.Error(), "创建输出目录") {
		t.Fatalf("error = %v", err)
	}
}
