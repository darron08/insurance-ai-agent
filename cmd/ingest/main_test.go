package main

import (
	"bytes"
	"encoding/json"
	"insurance_agent_demo/internal/document"
	"insurance_agent_demo/internal/testpdf"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionFlag(t *testing.T) {
	file := filepath.Join(t.TempDir(), "policy.pdf")
	if err := os.WriteFile(file, testpdf.Build("Policy terms"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"", "v2026", " v1", "v1 "} {
		dir := t.TempDir()
		var out, stderr bytes.Buffer
		code := run([]string{"--file", file, "--product-id", "demo", "--version", version, "--output", dir}, &out, &stderr)
		if version != strings.TrimSpace(version) {
			if code != 1 || !strings.Contains(stderr.String(), "version") {
				t.Fatalf("code=%d err=%s", code, &stderr)
			}
			continue
		}
		entries, err := os.ReadDir(dir)
		if code != 0 || err != nil || len(entries) != 1 {
			t.Fatalf("code=%d err=%s files=%v", code, &stderr, entries)
		}
		raw, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
		if err != nil {
			t.Fatal(err)
		}
		var doc document.Document
		if err := json.Unmarshal(raw, &doc); err != nil || doc.Metadata.Version != version {
			t.Fatalf("doc=%+v err=%v", doc, err)
		}
	}
}

func TestCLI(t *testing.T) {
	file := filepath.Join(t.TempDir(), "sample.pdf")
	if err := os.WriteFile(file, testpdf.Build("CLI fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--file", file, "--product-id", "demo", "--document-name", "测试", "--source-url", "https://example.com/demo", "--output", t.TempDir()}
	var out, stderr bytes.Buffer
	if code := run(args, &out, &stderr); code != 0 || !strings.Contains(out.String(), "导入成功") || stderr.Len() != 0 {
		t.Fatalf("success: %d %s %s", code, &out, &stderr)
	}
	args = []string{"--file", file, "--product-id", "demo", "--output", t.TempDir()}
	out.Reset()
	stderr.Reset()
	if code := run(args, &out, &stderr); code != 0 || !strings.Contains(out.String(), "导入成功") || stderr.Len() != 0 {
		t.Fatalf("filename default: %d %s %s", code, &out, &stderr)
	}
	for _, tc := range []struct {
		args []string
		code int
	}{{nil, 1}, {[]string{"--unknown"}, 2}, {[]string{"unexpected"}, 2}, {[]string{"--help"}, 0}} {
		out.Reset()
		stderr.Reset()
		if code := run(tc.args, &out, &stderr); code != tc.code {
			t.Fatalf("args %v: code %d, want %d", tc.args, code, tc.code)
		}
	}
}
