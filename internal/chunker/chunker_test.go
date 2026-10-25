package chunker

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"insurance_agent_demo/internal/document"
	"insurance_agent_demo/internal/parser"
)

func testDocument(texts ...string) document.Document {
	pages := make([]document.Page, len(texts))
	characters := 0
	textPages := 0
	for i, text := range texts {
		pages[i] = document.Page{PageNumber: i + 1, Text: text}
		trimmed := strings.TrimSpace(text)
		characters += utf8.RuneCountInString(trimmed)
		if trimmed != "" {
			textPages++
		}
	}
	return document.Document{
		Metadata: document.DocumentMetadata{
			ProductID: "product-demo", Name: "测试保险条款", Version: "v1",
			SourceURL: "https://example.com/terms.pdf",
		},
		FilePath:       "/tmp/terms.pdf",
		SHA256:         strings.Repeat("a", 64),
		TotalPages:     len(pages),
		CharacterCount: characters,
		Pages:          pages,
		Quality: document.Quality{
			Status: "passed", TextPages: textPages, EmptyPages: len(pages) - textPages,
		},
		ImportedAt: time.Date(2026, 10, 25, 1, 2, 3, 0, time.UTC),
		Parser:     "test-parser@1",
	}
}

func TestClauseAwareStructureAndPageContinuity(t *testing.T) {
	doc := testDocument(
		"阅读指引\n\n条款目录\n1.1 合同构成\n第一条 目录项",
		"产品说明\n\n您与我们的合同\n1.1 合同构成 正文甲。\n1.2 合同生效 正文乙。",
		"",
		"上一条跨页续文。\n\n保险责任\n第一条 我们承担下列保险责任。",
	)
	fixed := time.Date(2026, 10, 25, 9, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	result, err := (ClauseAware{Now: func() time.Time { return fixed }}).Split(doc)
	if err != nil {
		t.Fatal(err)
	}
	if result.Chunking.Strategy != Strategy || result.Chunking.MaxCharacters != 800 || result.Chunking.OverlapCharacters != 100 {
		t.Fatalf("chunking = %+v", result.Chunking)
	}
	if !result.ChunkedAt.Equal(fixed.UTC()) || result.TotalChunks != len(result.Chunks) {
		t.Fatalf("metadata = %+v", result)
	}

	var clause11, clause12Page2, clause12Page4, article1 *document.Chunk
	for i := range result.Chunks {
		chunk := &result.Chunks[i]
		if chunk.ChunkIndex != i+1 {
			t.Fatalf("chunk index = %d, want %d", chunk.ChunkIndex, i+1)
		}
		if chunk.PageNumber == 3 {
			t.Fatal("blank page produced a chunk")
		}
		if chunk.PageNumber == 1 && chunk.ClauseID != "" {
			t.Fatalf("contents entry treated as clause: %+v", chunk)
		}
		switch {
		case chunk.ClauseID == "1.1":
			clause11 = chunk
		case chunk.ClauseID == "1.2" && chunk.PageNumber == 2:
			clause12Page2 = chunk
		case chunk.ClauseID == "1.2" && chunk.PageNumber == 4:
			clause12Page4 = chunk
		case chunk.ClauseID == "第一条":
			article1 = chunk
		}
	}
	if clause11 == nil || clause11.SectionTitle != "您与我们的合同" || !strings.HasPrefix(clause11.Text, "您与我们的合同\n1.1") {
		t.Fatalf("decimal clause = %+v", clause11)
	}
	if clause12Page2 == nil || clause12Page4 == nil || clause12Page2.PartIndex != 1 || clause12Page4.PartIndex != 2 || clause12Page4.PartCount != 2 {
		t.Fatalf("cross-page parts = %+v / %+v", clause12Page2, clause12Page4)
	}
	if article1 == nil || article1.SectionTitle != "保险责任" || article1.PageNumber != 4 {
		t.Fatalf("article clause = %+v", article1)
	}
	for _, phrase := range []string{"阅读指引", "1.1 合同构成", "上一条跨页续文", "我们承担下列保险责任"} {
		if !chunksContain(result.Chunks, phrase) {
			t.Fatalf("source phrase %q was lost", phrase)
		}
	}
}

func TestSplitTextLimitOverlapAndUnicode(t *testing.T) {
	parts := splitText(strings.Repeat("保", 1700), MaxCharacters, OverlapCharacters)
	if len(parts) != 3 {
		t.Fatalf("parts = %d", len(parts))
	}
	for _, part := range parts {
		if !utf8.ValidString(part) || utf8.RuneCountInString(part) > MaxCharacters {
			t.Fatalf("invalid part length %d", utf8.RuneCountInString(part))
		}
	}
	first := []rune(parts[0])
	second := []rune(parts[1])
	if string(first[len(first)-OverlapCharacters:]) != string(second[:OverlapCharacters]) {
		t.Fatal("adjacent hard-split chunks do not preserve the configured overlap")
	}
	if got := splitText(strings.Repeat("甲", 500)+"。"+strings.Repeat("乙", 400), MaxCharacters, OverlapCharacters); len(got) != 2 || !strings.HasSuffix(got[0], "。") {
		t.Fatalf("sentence boundary was not preferred: lengths %v", runeLengths(got))
	}
}

func TestClauseAwareRejectsInvalidDocuments(t *testing.T) {
	tests := []struct {
		name string
		edit func(*document.Document)
		want string
	}{
		{name: "missing product", edit: func(d *document.Document) { d.Metadata.ProductID = " " }, want: "product_id"},
		{name: "missing source", edit: func(d *document.Document) { d.Metadata.SourceURL = "" }, want: "source_url"},
		{name: "invalid checksum", edit: func(d *document.Document) { d.SHA256 = "bad" }, want: "sha256"},
		{name: "quality failed", edit: func(d *document.Document) { d.Quality.Status = "failed" }, want: "质量状态"},
		{name: "page count", edit: func(d *document.Document) { d.TotalPages++ }, want: "total_pages"},
		{name: "page order", edit: func(d *document.Document) { d.Pages[0].PageNumber = 2 }, want: "页码"},
		{name: "replacement", edit: func(d *document.Document) { d.Pages[0].Text = "保险�条款"; d.CharacterCount = 5 }, want: "U+FFFD"},
		{name: "empty", edit: func(d *document.Document) {
			d.Pages[0].Text = ""
			d.CharacterCount = 0
			d.Quality.TextPages = 0
			d.Quality.EmptyPages = 1
		}, want: "没有可切分文本"},
		{name: "characters", edit: func(d *document.Document) { d.CharacterCount++ }, want: "character_count"},
		{name: "quality metrics", edit: func(d *document.Document) { d.Quality.TextPages = 0 }, want: "quality"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc := testDocument("保险条款")
			tc.edit(&doc)
			_, err := (ClauseAware{}).Split(doc)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestRealInsurancePDFChunking(t *testing.T) {
	tests := []struct {
		file    string
		checks  map[string]string
		noPage1 string
	}{
		{
			file: "pingan-ankang-te-xu-ban-2020-medical.pdf",
			checks: map[string]string{
				"1.1": "您与我们的合同",
				"2.2": "我们提供的保障",
			},
			noPage1: "1.1",
		},
		{
			file: "pingan-travel-accident-terms-handbook.pdf",
			checks: map[string]string{
				"第一条": "总 则",
				"第五条": "保险责任",
				"第七条": "责任免除",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.file, func(t *testing.T) {
			path := filepath.Join("..", "..", "testdata", "real", tc.file)
			parsed, err := (parser.PDF{}).Parse(path)
			if err != nil {
				t.Fatal(err)
			}
			doc := documentFromParsed(path, parsed)
			result, err := (ClauseAware{}).Split(doc)
			if err != nil {
				t.Fatal(err)
			}
			for clauseID, section := range tc.checks {
				found := false
				for _, chunk := range result.Chunks {
					if chunk.ClauseID == clauseID && chunk.SectionTitle == section {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("missing clause %q in section %q", clauseID, section)
				}
			}
			for _, chunk := range result.Chunks {
				if chunk.PageNumber < 1 || chunk.PageNumber > doc.TotalPages || chunk.CharacterCount > MaxCharacters || strings.Contains(chunk.Text, "�") {
					t.Errorf("invalid chunk: %+v", chunk)
				}
				if tc.noPage1 != "" && chunk.PageNumber == 1 && chunk.ClauseID == tc.noPage1 {
					t.Errorf("contents entry became a clause: %+v", chunk)
				}
			}
		})
	}
}

func documentFromParsed(path string, parsed parser.Result) document.Document {
	texts := make([]string, len(parsed.Pages))
	for i, page := range parsed.Pages {
		texts[i] = page.Text
	}
	doc := testDocument(texts...)
	doc.FilePath = path
	doc.Parser = parsed.Engine
	doc.Quality.PrivateUseCharactersRemoved = parsed.PrivateUseCharactersRemoved
	return doc
}

func chunksContain(chunks []document.Chunk, phrase string) bool {
	for _, chunk := range chunks {
		if strings.Contains(chunk.Text, phrase) {
			return true
		}
	}
	return false
}

func runeLengths(parts []string) []int {
	lengths := make([]int, len(parts))
	for i, part := range parts {
		lengths[i] = utf8.RuneCountInString(part)
	}
	return lengths
}
