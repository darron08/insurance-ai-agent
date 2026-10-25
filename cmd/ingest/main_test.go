package main

import (
	"bytes"
	"insurance_agent_demo/internal/testpdf"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
