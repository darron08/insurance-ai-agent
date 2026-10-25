package parser

import (
	"context"
	"errors"
	"insurance_agent_demo/internal/testpdf"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPDFPagesAndUnicode(t *testing.T) {
	data := testpdf.Build("First page", "", "Third page")
	path := filepath.Join(t.TempDir(), "fixture.pdf")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	result, err := (PDF{}).Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	pages := result.Pages
	if len(pages) != 3 {
		t.Fatalf("pages = %d", len(pages))
	}
	if result.Engine != "pdfplumber@0.11.9" {
		t.Fatalf("engine = %q", result.Engine)
	}
	for i, page := range pages {
		if page.PageNumber != i+1 {
			t.Fatalf("page numbering: %+v", pages)
		}
	}
	if pages[1].Text != "" {
		t.Fatalf("blank page lost: %+v", pages[1])
	}
	if !strings.Contains(pages[0].Text, "First page") || !strings.Contains(pages[0].Text, "保险条款测试资料") {
		t.Fatalf("unexpected text: %q", pages[0].Text)
	}
	if !strings.Contains(pages[2].Text, "Third page") {
		t.Fatalf("wrong page order: %+v", pages)
	}
}

func TestCorruptPDF(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrupt.pdf")
	if err := os.WriteFile(path, []byte("%PDF-1.4\nnot a PDF structure\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := (PDF{}).Parse(path); err == nil {
		t.Fatal("corrupt PDF accepted")
	}
}

func TestInvalidExtractorOutput(t *testing.T) {
	p := PDF{run: func(context.Context, string, ...string) ([]byte, []byte, error) {
		return []byte("not-json"), nil, nil
	}}
	if _, err := p.Parse("fixture.pdf"); err == nil || !strings.Contains(err.Error(), "无效 JSON") {
		t.Fatalf("error = %v", err)
	}
}

func TestExtractorTimeout(t *testing.T) {
	p := PDF{
		Timeout: time.Millisecond,
		run: func(ctx context.Context, _ string, _ ...string) ([]byte, []byte, error) {
			<-ctx.Done()
			return nil, nil, ctx.Err()
		},
	}
	if _, err := p.Parse("fixture.pdf"); err == nil || !strings.Contains(err.Error(), "超过") {
		t.Fatalf("error = %v", err)
	}
}

func TestMissingPython(t *testing.T) {
	p := PDF{PythonExecutable: "insurance-agent-python-that-does-not-exist"}
	if _, err := p.Parse("fixture.pdf"); err == nil || !strings.Contains(err.Error(), "未找到 Python") {
		t.Fatalf("error = %v", err)
	}
}

func TestMissingPDFPlumber(t *testing.T) {
	p := PDF{run: func(context.Context, string, ...string) ([]byte, []byte, error) {
		return nil, []byte("缺少 Python 依赖 pdfplumber，请执行安装命令"), errors.New("exit status 3")
	}}
	if _, err := p.Parse("fixture.pdf"); err == nil || !strings.Contains(err.Error(), "缺少 Python 依赖 pdfplumber") {
		t.Fatalf("error = %v", err)
	}
}

func TestRealInsurancePDFs(t *testing.T) {
	tests := []struct {
		file        string
		pages       int
		mustHave    []string
		mustNotHave []string
	}{
		{
			file:        "pingan-ankang-te-xu-ban-2020-medical.pdf",
			pages:       16,
			mustHave:    []string{"1.1 合同构成 本保险条款", "医疗保险", "被保险人", "保险责任", "等待期", "保险计划"},
			mustNotHave: []string{"�", "同成合构", "险保疗医", "人险保被"},
		},
		{
			file:        "pingan-travel-accident-terms-handbook.pdf",
			pages:       8,
			mustHave:    []string{"平安旅行意外伤害保险条款", "保险责任", "责任免除", "民事行为能力人的除外；\n（三）", "第七条 被保险人"},
			mustNotHave: []string{"�"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.file, func(t *testing.T) {
			path := filepath.Join("..", "..", "testdata", "real", tc.file)
			result, err := (PDF{}).Parse(path)
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Pages) != tc.pages {
				t.Fatalf("pages = %d, want %d", len(result.Pages), tc.pages)
			}
			var text strings.Builder
			for _, page := range result.Pages {
				text.WriteString(page.Text)
				text.WriteByte('\n')
			}
			for _, phrase := range tc.mustHave {
				if !strings.Contains(text.String(), phrase) {
					t.Errorf("missing phrase %q", phrase)
				}
			}
			for _, phrase := range tc.mustNotHave {
				if strings.Contains(text.String(), phrase) {
					t.Errorf("unexpected phrase %q", phrase)
				}
			}
		})
	}
}
