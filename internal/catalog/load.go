package catalog

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"insurance_agent_demo/internal/document"
)

func LoadChunkedDocument(path string) (document.ChunkedDocument, error) {
	var result document.ChunkedDocument
	if strings.TrimSpace(path) == "" {
		return result, fmt.Errorf("缺少参数 --input")
	}
	if !strings.EqualFold(filepath.Ext(path), ".json") {
		return result, fmt.Errorf("--input 只接受 .json 文件")
	}
	f, err := os.Open(path)
	if err != nil {
		return result, fmt.Errorf("打开 M2 JSON: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return result, fmt.Errorf("读取 M2 JSON 信息: %w", err)
	}
	if !info.Mode().IsRegular() {
		return result, fmt.Errorf("M2 JSON 必须是普通文件")
	}

	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return result, fmt.Errorf("读取 M2 JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return result, fmt.Errorf("读取 M2 JSON: 文件包含多个 JSON 值")
		}
		return result, fmt.Errorf("读取 M2 JSON: %w", err)
	}
	return result, nil
}
