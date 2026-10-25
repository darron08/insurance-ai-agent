package ingest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"insurance_agent_demo/internal/document"
	"insurance_agent_demo/internal/parser"
	"insurance_agent_demo/internal/testpdf"
)

type stubParser struct {
	result parser.Result
	err    error
}

func (s stubParser) Parse(string) (parser.Result, error) { return s.result, s.err }

func request(t *testing.T, data []byte) Request {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "input.pdf")
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	return Request{File: file, OutputDir: filepath.Join(dir, "output"), Metadata: document.DocumentMetadata{ProductID: "demo", Name: "测试条款", Version: "v1", SourceURL: "https://example.com/fixture"}}
}

func TestImportAndRepeat(t *testing.T) {
	data := testpdf.Build("Page one", "Page two", "Page three")
	req := request(t, data)
	s := Service{Parser: parser.PDF{}}
	doc, path, err := s.Run(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved document.Document
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	wantHash := sha256.Sum256(data)
	if saved.SHA256 != hex.EncodeToString(wantHash[:]) || saved.TotalPages != 3 || saved.Metadata != req.Metadata || saved.ImportedAt.IsZero() || saved.Parser == "" || saved.Quality.Status != "passed" {
		t.Fatalf("bad saved document: %+v", saved)
	}
	repeated, secondPath, err := s.Run(req)
	if err != nil {
		t.Fatal(err)
	}
	if repeated.SHA256 != doc.SHA256 || path == secondPath {
		t.Fatal("repeat must preserve checksum and earlier output")
	}
	entries, err := os.ReadDir(req.OutputDir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("unexpected output files: %v, %v", entries, err)
	}
}

func TestImportRejectsReplacementCharacter(t *testing.T) {
	req := request(t, testpdf.Build("Page one"))
	s := Service{Parser: stubParser{result: parser.Result{
		Engine: "test-parser@1",
		Pages:  []document.Page{{PageNumber: 1, Text: "保险�条款"}},
	}}}
	_, _, err := s.Run(req)
	if err == nil || !strings.Contains(err.Error(), "U+FFFD") {
		t.Fatalf("error = %v", err)
	}
	if _, err := os.Stat(req.OutputDir); !os.IsNotExist(err) {
		t.Fatalf("quality failure created output directory: %v", err)
	}
}

func TestImportRecordsQualityMetrics(t *testing.T) {
	req := request(t, testpdf.Build("Page one", ""))
	s := Service{Parser: stubParser{result: parser.Result{
		Engine:                      "test-parser@1",
		PrivateUseCharactersRemoved: 4,
		Pages: []document.Page{
			{PageNumber: 1, Text: "保险条款"},
			{PageNumber: 2, Text: ""},
		},
	}}}
	doc, _, err := s.Run(req)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Quality.Status != "passed" || doc.Quality.ReplacementCharacters != 0 || doc.Quality.PrivateUseCharactersRemoved != 4 || doc.Quality.TextPages != 1 || doc.Quality.EmptyPages != 1 {
		t.Fatalf("quality = %+v", doc.Quality)
	}
}

func TestImportUsesFilenameWhenDocumentNameIsOmitted(t *testing.T) {
	req := request(t, testpdf.Build("Page one"))
	req.Metadata.Name = ""
	req.Metadata.SourceURL = ""
	doc, _, err := (Service{Parser: parser.PDF{}}).Run(req)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Metadata.Name != "input" {
		t.Fatalf("document name = %q, want input", doc.Metadata.Name)
	}
	if doc.Metadata.SourceURL != "local://input.pdf" {
		t.Fatalf("source URL = %q, want local://input.pdf", doc.Metadata.SourceURL)
	}
}

func TestImportFailuresLeaveNoOutput(t *testing.T) {
	for _, name := range []string{"missing-file", "wrong-extension", "wrong-header", "corrupt", "empty-text", "missing-product-id", "invalid-source"} {
		t.Run(name, func(t *testing.T) {
			req := request(t, testpdf.Build("Test"))
			want := ""
			switch name {
			case "missing-file":
				req.File += ".missing.pdf"
				want = "打开文件"
			case "wrong-extension":
				req.File += ".txt"
				want = "只接受"
			case "wrong-header":
				if err := os.WriteFile(req.File, []byte("pretend PDF"), 0600); err != nil {
					t.Fatal(err)
				}
				want = "文件头"
			case "corrupt":
				if err := os.WriteFile(req.File, []byte("%PDF-1.4\nbroken\n"), 0600); err != nil {
					t.Fatal(err)
				}
				want = "解析阶段"
			case "empty-text":
				if err := os.WriteFile(req.File, testpdf.Build(""), 0600); err != nil {
					t.Fatal(err)
				}
				want = "未提取到文本"
			case "missing-product-id":
				req.Metadata.ProductID = " "
				want = "--product-id"
			case "invalid-source":
				req.Metadata.SourceURL = "not a URL"
				want = "--source-url"
			}
			_, _, err := (Service{Parser: parser.PDF{}}).Run(req)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("error = %v, want %q", err, want)
			}
			if _, err := os.Stat(req.OutputDir); !os.IsNotExist(err) {
				t.Fatalf("failed import created output directory: %v", err)
			}
		})
	}
}
