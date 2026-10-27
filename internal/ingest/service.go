package ingest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"insurance_agent_demo/internal/document"
	"insurance_agent_demo/internal/parser"
)

type Request struct {
	File      string
	OutputDir string
	Metadata  document.DocumentMetadata
}

type Service struct{ Parser parser.Parser }

func (s Service) Run(req Request) (doc document.Document, output string, err error) {
	if req.Metadata.Version != strings.TrimSpace(req.Metadata.Version) {
		return doc, "", fmt.Errorf("version 不能包含首尾空白")
	}
	for _, field := range []struct{ name, value string }{
		{"file", req.File}, {"output", req.OutputDir}, {"product-id", req.Metadata.ProductID},
	} {
		if strings.TrimSpace(field.value) == "" {
			return doc, "", fmt.Errorf("缺少参数 --%s", field.name)
		}
	}
	if !strings.EqualFold(filepath.Ext(req.File), ".pdf") {
		return doc, "", fmt.Errorf("只接受 .pdf 文件")
	}
	path, err := filepath.Abs(req.File)
	if err != nil {
		return doc, "", fmt.Errorf("解析文件路径: %w", err)
	}
	if strings.TrimSpace(req.Metadata.Name) == "" {
		req.Metadata.Name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	if req.Metadata.Name == "" {
		return doc, "", fmt.Errorf("无法从文件名推断文档名称，请使用 --document-name")
	}
	if strings.TrimSpace(req.Metadata.SourceURL) == "" {
		req.Metadata.SourceURL = "local://" + url.PathEscape(filepath.Base(path))
	} else {
		u, err := url.Parse(req.Metadata.SourceURL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return doc, "", fmt.Errorf("--source-url 必须是有效的 HTTP 或 HTTPS 地址")
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return doc, "", fmt.Errorf("打开文件: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return doc, "", err
	}
	if !info.Mode().IsRegular() {
		return doc, "", fmt.Errorf("输入必须是普通文件")
	}
	var signature [5]byte
	if _, err := f.ReadAt(signature[:], 0); err != nil || string(signature[:]) != "%PDF-" {
		return doc, "", fmt.Errorf("文件内容不是有效 PDF：缺少 PDF 文件头")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return doc, "", fmt.Errorf("计算校验值: %w", err)
	}
	result, err := s.Parser.Parse(path)
	if err != nil {
		return doc, "", fmt.Errorf("解析阶段: %w", err)
	}
	characters := 0
	replacementCharacters := 0
	textPages := 0
	for _, page := range result.Pages {
		text := strings.TrimSpace(page.Text)
		characters += utf8.RuneCountInString(text)
		replacementCharacters += strings.Count(text, "\uFFFD")
		if text != "" {
			textPages++
		}
	}
	if characters == 0 {
		return doc, "", fmt.Errorf("PDF 未提取到文本：可能是扫描件或字体编码不受支持；M1 不支持 OCR")
	}
	if replacementCharacters > 0 {
		return doc, "", fmt.Errorf("PDF 文本质量不达标：发现 %d 个无法识别的替换字符 U+FFFD", replacementCharacters)
	}
	doc = document.Document{
		Metadata:       req.Metadata,
		FilePath:       path,
		SHA256:         hex.EncodeToString(hash.Sum(nil)),
		TotalPages:     len(result.Pages),
		CharacterCount: characters,
		Pages:          result.Pages,
		Quality: document.Quality{
			Status:                      "passed",
			ReplacementCharacters:       replacementCharacters,
			PrivateUseCharactersRemoved: result.PrivateUseCharactersRemoved,
			TextPages:                   textPages,
			EmptyPages:                  len(result.Pages) - textPages,
		},
		ImportedAt: time.Now().UTC(),
		Parser:     result.Engine,
	}
	if err := os.MkdirAll(req.OutputDir, 0755); err != nil {
		return doc, "", fmt.Errorf("创建输出目录: %w", err)
	}
	// A unique filename preserves earlier imports, even when the PDF is the same.
	fout, err := os.CreateTemp(req.OutputDir, ".ingest-*.tmp")
	if err != nil {
		return doc, "", fmt.Errorf("创建输出文件: %w", err)
	}
	temp := fout.Name()
	defer os.Remove(temp)
	encoder := json.NewEncoder(fout)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(doc); err != nil {
		fout.Close()
		return doc, "", fmt.Errorf("写入 JSON: %w", err)
	}
	if err := fout.Close(); err != nil {
		return doc, "", fmt.Errorf("关闭 JSON: %w", err)
	}
	output = strings.TrimSuffix(temp, ".tmp") + ".json"
	output = filepath.Join(filepath.Dir(output), strings.TrimPrefix(filepath.Base(output), "."))
	if err := os.Rename(temp, output); err != nil {
		return doc, "", fmt.Errorf("保存 JSON: %w", err)
	}
	return doc, output, nil
}
