package parser

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"insurance_agent_demo/internal/document"
)

//go:embed pdf_extract.py
var extractorScript string

const defaultTimeout = 2 * time.Minute

type commandRunner func(ctx context.Context, executable string, args ...string) (stdout, stderr []byte, err error)

type PDF struct {
	PythonExecutable string
	Timeout          time.Duration
	run              commandRunner
	script           string
}

type extractionOutput struct {
	Engine                      string          `json:"engine"`
	PrivateUseCharactersRemoved int             `json:"private_use_characters_removed"`
	Pages                       []document.Page `json:"pages"`
}

func runCommand(ctx context.Context, executable string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, executable, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	return stdout, stderr.Bytes(), err
}

func (p PDF) Parse(path string) (Result, error) {
	python := p.PythonExecutable
	if python == "" {
		python = "python3"
	}
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	runner := p.run
	if runner == nil {
		runner = runCommand
	}
	script := p.script
	if script == "" {
		script = extractorScript
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	stdout, stderr, err := runner(ctx, python, "-c", script, path)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return Result{}, fmt.Errorf("PDF 文本提取超过 %s", timeout)
	}
	if err != nil {
		message := strings.TrimSpace(string(stderr))
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, exec.ErrDot) {
			return Result{}, fmt.Errorf("未找到 Python 解释器 %q，请安装 Python 3", python)
		}
		if message == "" {
			message = err.Error()
		}
		return Result{}, fmt.Errorf("pdfplumber 提取失败: %s", message)
	}

	var output extractionOutput
	if err := json.Unmarshal(stdout, &output); err != nil {
		return Result{}, fmt.Errorf("pdfplumber 返回了无效 JSON: %w", err)
	}
	if strings.TrimSpace(output.Engine) == "" {
		return Result{}, fmt.Errorf("pdfplumber 输出缺少解析器版本")
	}
	if len(output.Pages) == 0 {
		return Result{}, fmt.Errorf("PDF 没有可读取页面")
	}
	for i, page := range output.Pages {
		if page.PageNumber != i+1 {
			return Result{}, fmt.Errorf("pdfplumber 返回的页码不连续: 第 %d 项为 %d", i+1, page.PageNumber)
		}
	}
	return Result{
		Pages:                       output.Pages,
		Engine:                      output.Engine,
		PrivateUseCharactersRemoved: output.PrivateUseCharactersRemoved,
	}, nil
}
