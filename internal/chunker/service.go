package chunker

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"insurance_agent_demo/internal/document"
)

type Service struct {
	Splitter Splitter
}

func (s Service) Run(input, outputDir string) (document.ChunkedDocument, string, error) {
	var result document.ChunkedDocument
	if strings.TrimSpace(input) == "" {
		return result, "", fmt.Errorf("缺少参数 --input")
	}
	if strings.TrimSpace(outputDir) == "" {
		return result, "", fmt.Errorf("缺少参数 --output")
	}
	if !strings.EqualFold(filepath.Ext(input), ".json") {
		return result, "", fmt.Errorf("--input 只接受 .json 文件")
	}

	f, err := os.Open(input)
	if err != nil {
		return result, "", fmt.Errorf("打开输入 JSON: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return result, "", fmt.Errorf("读取输入信息: %w", err)
	}
	if !info.Mode().IsRegular() {
		return result, "", fmt.Errorf("输入必须是普通文件")
	}

	var doc document.Document
	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&doc); err != nil {
		return result, "", fmt.Errorf("读取 M1 JSON: %w", err)
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return result, "", err
	}

	splitter := s.Splitter
	if splitter == nil {
		splitter = ClauseAware{}
	}
	result, err = splitter.Split(doc)
	if err != nil {
		return result, "", fmt.Errorf("切分阶段: %w", err)
	}

	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return result, "", fmt.Errorf("创建输出目录: %w", err)
	}
	fout, err := os.CreateTemp(outputDir, ".chunk-*.tmp")
	if err != nil {
		return result, "", fmt.Errorf("创建输出文件: %w", err)
	}
	temp := fout.Name()
	defer os.Remove(temp)
	encoder := json.NewEncoder(fout)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(result); err != nil {
		fout.Close()
		return result, "", fmt.Errorf("写入 Chunk JSON: %w", err)
	}
	if err := fout.Close(); err != nil {
		return result, "", fmt.Errorf("关闭 Chunk JSON: %w", err)
	}
	output := strings.TrimSuffix(temp, ".tmp") + ".json"
	output = filepath.Join(filepath.Dir(output), strings.TrimPrefix(filepath.Base(output), "."))
	if err := os.Rename(temp, output); err != nil {
		return result, "", fmt.Errorf("保存 Chunk JSON: %w", err)
	}
	return result, output, nil
}

func ensureJSONEnd(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("读取 M1 JSON: 文件包含多个 JSON 值")
		}
		return fmt.Errorf("读取 M1 JSON: %w", err)
	}
	return nil
}
