package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"insurance_agent_demo/internal/document"
)

func cliInput(t *testing.T) string {
	t.Helper()
	text := "保险责任\n第一条 保险正文。"
	doc := document.Document{
		Metadata: document.DocumentMetadata{ProductID: "demo", Name: "测试条款", SourceURL: "local://test.pdf"},
		FilePath: "/tmp/test.pdf", SHA256: strings.Repeat("a", 64), TotalPages: 1,
		CharacterCount: utf8.RuneCountInString(text),
		Pages:          []document.Page{{PageNumber: 1, Text: text}},
		Quality:        document.Quality{Status: "passed", TextPages: 1},
		ImportedAt:     time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC),
		Parser:         "test-parser@1",
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "import.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCLI(t *testing.T) {
	input := cliInput(t)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--input", input, "--output", t.TempDir()}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "切分成功") || !strings.Contains(stdout.String(), "clause-aware-v1") || stderr.Len() != 0 {
		t.Fatalf("success: %d %s %s", code, &stdout, &stderr)
	}
	for _, tc := range []struct {
		args []string
		code int
	}{
		{args: nil, code: 1},
		{args: []string{"--unknown"}, code: 2},
		{args: []string{"unexpected"}, code: 2},
		{args: []string{"--help"}, code: 0},
	} {
		stdout.Reset()
		stderr.Reset()
		if code := run(tc.args, &stdout, &stderr); code != tc.code {
			t.Fatalf("args %v: code %d, want %d", tc.args, code, tc.code)
		}
	}
}
