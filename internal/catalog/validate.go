package catalog

import (
	"encoding/hex"
	"fmt"
	"strings"
	"unicode/utf8"

	"insurance_agent_demo/internal/document"
)

func ValidateChunkedDocument(input document.ChunkedDocument) error {
	for _, field := range []struct {
		name  string
		value string
	}{
		{"metadata.product_id", input.Metadata.ProductID},
		{"metadata.name", input.Metadata.Name},
		{"metadata.source_url", input.Metadata.SourceURL},
		{"source_file_path", input.SourceFilePath},
		{"source_parser", input.SourceParser},
		{"chunking.strategy", input.Chunking.Strategy},
	} {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("M2 JSON 缺少 %s", field.name)
		}
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"metadata.product_id", input.Metadata.ProductID},
		{"metadata.name", input.Metadata.Name},
		{"metadata.version", input.Metadata.Version},
		{"metadata.source_url", input.Metadata.SourceURL},
	} {
		if field.value != strings.TrimSpace(field.value) {
			return fmt.Errorf("M2 JSON %s 不能包含首尾空白", field.name)
		}
	}
	decoded, err := hex.DecodeString(input.SourceSHA256)
	if err != nil || len(decoded) != 32 || input.SourceSHA256 != strings.ToLower(input.SourceSHA256) {
		return fmt.Errorf("M2 JSON source_sha256 无效")
	}
	if input.SourceImportedAt.IsZero() || input.ChunkedAt.IsZero() {
		return fmt.Errorf("M2 JSON 缺少导入或切分时间")
	}
	if input.TotalPages <= 0 {
		return fmt.Errorf("M2 JSON total_pages 必须大于 0")
	}
	if input.Chunking.MaxCharacters <= 0 || input.Chunking.OverlapCharacters < 0 || input.Chunking.OverlapCharacters >= input.Chunking.MaxCharacters {
		return fmt.Errorf("M2 JSON chunking 参数无效")
	}
	if input.TotalChunks <= 0 || input.TotalChunks != len(input.Chunks) {
		return fmt.Errorf("M2 JSON total_chunks 与 chunks 不一致")
	}

	for i, chunk := range input.Chunks {
		index := i + 1
		if chunk.ChunkIndex != index {
			return fmt.Errorf("M2 JSON chunk_index 必须从 1 连续排列：第 %d 项为 %d", index, chunk.ChunkIndex)
		}
		if chunk.ProductID != input.Metadata.ProductID || chunk.DocumentName != input.Metadata.Name || chunk.DocumentVersion != input.Metadata.Version {
			return fmt.Errorf("M2 JSON 第 %d 个 Chunk 与顶层文档元数据不一致", index)
		}
		if chunk.PageNumber <= 0 || chunk.PageNumber > input.TotalPages {
			return fmt.Errorf("M2 JSON 第 %d 个 Chunk 页码越界", index)
		}
		if chunk.PartIndex <= 0 || chunk.PartCount <= 0 || chunk.PartIndex > chunk.PartCount {
			return fmt.Errorf("M2 JSON 第 %d 个 Chunk 分片序号无效", index)
		}
		text := strings.TrimSpace(chunk.Text)
		if text == "" {
			return fmt.Errorf("M2 JSON 第 %d 个 Chunk 正文为空", index)
		}
		if strings.Contains(text, "\uFFFD") {
			return fmt.Errorf("M2 JSON 第 %d 个 Chunk 包含无法识别的替换字符 U+FFFD", index)
		}
		if chunk.Text != text || chunk.CharacterCount != utf8.RuneCountInString(text) {
			return fmt.Errorf("M2 JSON 第 %d 个 Chunk character_count 与正文不一致", index)
		}
	}
	return nil
}
