package chunker

import (
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"insurance_agent_demo/internal/document"
)

const (
	Strategy          = "clause-aware-v1"
	MaxCharacters     = 800
	OverlapCharacters = 100
)

var (
	decimalClause = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)+)[[:space:]]*(.*)$`)
	articleClause = regexp.MustCompile(`^(第[零〇一二三四五六七八九十百千万两0-9]+条)(.*)$`)
	listPrefix    = regexp.MustCompile(`^([（(]?[零〇一二三四五六七八九十百千万两0-9]+[）)、.)．]|[0-9]+、)`)
)

type Splitter interface {
	Split(document.Document) (document.ChunkedDocument, error)
}

// ClauseAware applies deterministic, explainable heuristics to page text. Now
// is injectable so callers can make persistence tests deterministic.
type ClauseAware struct {
	Now func() time.Time
}

type block struct {
	page         int
	section      string
	clause       string
	group        int
	lines        []string
	includeTitle string
}

type piece struct {
	page    int
	section string
	clause  string
	group   int
	text    string
}

func (c ClauseAware) Split(doc document.Document) (document.ChunkedDocument, error) {
	if err := validateDocument(doc); err != nil {
		return document.ChunkedDocument{}, err
	}

	blocks := parseBlocks(doc.Pages)
	pieces := make([]piece, 0, len(blocks))
	for _, item := range blocks {
		for _, text := range splitText(item.text(), MaxCharacters, OverlapCharacters) {
			pieces = append(pieces, piece{
				page: item.page, section: item.section, clause: item.clause,
				group: item.group, text: text,
			})
		}
	}

	groupCounts := make(map[int]int)
	for _, item := range pieces {
		groupCounts[item.group]++
	}
	groupIndexes := make(map[int]int)
	chunks := make([]document.Chunk, 0, len(pieces))
	for i, item := range pieces {
		groupIndexes[item.group]++
		chunks = append(chunks, document.Chunk{
			ChunkIndex:      i + 1,
			ProductID:       doc.Metadata.ProductID,
			DocumentName:    doc.Metadata.Name,
			DocumentVersion: doc.Metadata.Version,
			PageNumber:      item.page,
			SectionTitle:    item.section,
			ClauseID:        item.clause,
			PartIndex:       groupIndexes[item.group],
			PartCount:       groupCounts[item.group],
			CharacterCount:  utf8.RuneCountInString(item.text),
			Text:            item.text,
		})
	}
	if len(chunks) == 0 {
		return document.ChunkedDocument{}, fmt.Errorf("输入文档没有生成任何 Chunk")
	}

	now := c.Now
	if now == nil {
		now = time.Now
	}
	return document.ChunkedDocument{
		Metadata:         doc.Metadata,
		SourceFilePath:   doc.FilePath,
		SourceSHA256:     doc.SHA256,
		SourceImportedAt: doc.ImportedAt,
		SourceParser:     doc.Parser,
		TotalPages:       doc.TotalPages,
		Chunking: document.ChunkingConfig{
			Strategy:          Strategy,
			MaxCharacters:     MaxCharacters,
			OverlapCharacters: OverlapCharacters,
		},
		TotalChunks: len(chunks),
		Chunks:      chunks,
		ChunkedAt:   now().UTC(),
	}, nil
}

func validateDocument(doc document.Document) error {
	if strings.TrimSpace(doc.Metadata.ProductID) == "" {
		return fmt.Errorf("输入文档缺少 metadata.product_id")
	}
	if strings.TrimSpace(doc.Metadata.Name) == "" {
		return fmt.Errorf("输入文档缺少 metadata.name")
	}
	if strings.TrimSpace(doc.Metadata.SourceURL) == "" {
		return fmt.Errorf("输入文档缺少 metadata.source_url")
	}
	if strings.TrimSpace(doc.FilePath) == "" || strings.TrimSpace(doc.Parser) == "" || doc.ImportedAt.IsZero() {
		return fmt.Errorf("输入文档缺少 M1 来源信息")
	}
	hash, err := hex.DecodeString(doc.SHA256)
	if err != nil || len(hash) != 32 {
		return fmt.Errorf("输入文档 sha256 无效")
	}
	if doc.Quality.Status != "passed" {
		return fmt.Errorf("输入文档质量状态不是 passed")
	}
	if doc.TotalPages == 0 || doc.TotalPages != len(doc.Pages) {
		return fmt.Errorf("输入文档 total_pages 与 pages 不一致")
	}

	characters := 0
	textPages := 0
	for i, page := range doc.Pages {
		if page.PageNumber != i+1 {
			return fmt.Errorf("输入文档页码必须从 1 连续排列")
		}
		text := strings.TrimSpace(page.Text)
		if strings.Contains(text, "\uFFFD") {
			return fmt.Errorf("输入文档包含无法识别的替换字符 U+FFFD")
		}
		characters += utf8.RuneCountInString(text)
		if text != "" {
			textPages++
		}
	}
	if characters == 0 {
		return fmt.Errorf("输入文档没有可切分文本")
	}
	if doc.CharacterCount != characters {
		return fmt.Errorf("输入文档 character_count 与页面正文不一致")
	}
	if doc.Quality.ReplacementCharacters != 0 || doc.Quality.TextPages != textPages || doc.Quality.EmptyPages != len(doc.Pages)-textPages {
		return fmt.Errorf("输入文档 quality 指标与页面正文不一致")
	}
	return nil
}

func parseBlocks(pages []document.Page) []block {
	var blocks []block
	nextGroup := 1
	stateSection := ""
	stateClause := ""
	stateGroup := 0

	appendBlock := func(item block) {
		if item.text() == "" {
			return
		}
		if item.group == 0 {
			item.group = nextGroup
			nextGroup++
		}
		blocks = append(blocks, item)
	}

	for _, page := range pages {
		if strings.TrimSpace(page.Text) == "" {
			continue
		}
		current := block{page: page.PageNumber, section: stateSection, clause: stateClause, group: stateGroup}
		inContents := false
		for _, line := range strings.Split(page.Text, "\n") {
			trimmed := strings.TrimSpace(line)
			if inContents {
				current.lines = append(current.lines, line)
				continue
			}
			if isContentsHeading(trimmed) {
				appendBlock(current)
				current = block{page: page.PageNumber, lines: []string{line}}
				inContents = true
				continue
			}
			if clauseID, ok := parseClauseID(trimmed); ok {
				title, remaining := detachSectionTitle(current.lines, current.clause == "")
				current.lines = remaining
				appendBlock(current)
				if title != "" {
					stateSection = normalizeTitle(title)
				}
				stateClause = clauseID
				stateGroup = nextGroup
				nextGroup++
				current = block{
					page: page.PageNumber, section: stateSection, clause: stateClause,
					group: stateGroup, includeTitle: title, lines: []string{line},
				}
				continue
			}
			current.lines = append(current.lines, line)
		}
		appendBlock(current)
	}
	return blocks
}

func (b block) text() string {
	parts := make([]string, 0, len(b.lines)+1)
	if b.includeTitle != "" {
		parts = append(parts, b.includeTitle)
	}
	parts = append(parts, b.lines...)
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func parseClauseID(line string) (string, bool) {
	if match := decimalClause.FindStringSubmatch(line); match != nil {
		return match[1], true
	}
	if match := articleClause.FindStringSubmatch(line); match != nil {
		return match[1], true
	}
	return "", false
}

func isContentsHeading(line string) bool {
	compact := strings.Join(strings.Fields(line), "")
	return compact == "目录" || compact == "条款目录"
}

func detachSectionTitle(lines []string, allowWithoutSeparator bool) (string, []string) {
	end := len(lines)
	for end > 0 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	if end == 0 {
		return "", lines[:end]
	}
	candidate := strings.TrimSpace(lines[end-1])
	separated := end == 1 || (end >= 2 && (strings.TrimSpace(lines[end-2]) == "" || endsSentence(lines[end-2])))
	if (!separated && !allowWithoutSeparator) || !isSectionTitle(candidate) {
		return "", lines
	}
	return lines[end-1], lines[:end-1]
}

func endsSentence(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	runes := []rune(line)
	return strings.ContainsRune("。！？；!?;", runes[len(runes)-1])
}

func isSectionTitle(line string) bool {
	if line == "" || utf8.RuneCountInString(line) > 30 || isContentsHeading(line) {
		return false
	}
	if _, ok := parseClauseID(line); ok || listPrefix.MatchString(line) {
		return false
	}
	if strings.ContainsAny(line, "。；：:！？!?，,、）)]》") {
		return false
	}
	allDigits := true
	for _, r := range line {
		if r < '0' || r > '9' {
			allDigits = false
			break
		}
	}
	return !allDigits
}

func normalizeTitle(title string) string {
	return strings.Join(strings.Fields(title), " ")
}

func splitText(text string, maximum, overlap int) []string {
	text = strings.TrimSpace(text)
	runes := []rune(text)
	if len(runes) == 0 {
		return nil
	}
	if len(runes) <= maximum {
		return []string{text}
	}

	var parts []string
	for start := 0; start < len(runes); {
		end := start + maximum
		if end >= len(runes) {
			parts = append(parts, strings.TrimSpace(string(runes[start:])))
			break
		}
		end = preferredEnd(runes, start, end)
		part := strings.TrimSpace(string(runes[start:end]))
		if part != "" {
			parts = append(parts, part)
		}
		next := overlapStart(runes, start, end, overlap)
		if next <= start {
			next = end
		}
		start = next
	}
	return parts
}

func preferredEnd(runes []rune, start, limit int) int {
	minimum := start + (limit-start)/2
	if at := lastDoubleNewline(runes, minimum, limit); at > minimum {
		return at
	}
	if at := lastRune(runes, minimum, limit, func(r rune) bool { return r == '\n' }); at > minimum {
		return at
	}
	if at := lastRune(runes, minimum, limit, isSentenceEnd); at > minimum {
		return at
	}
	return limit
}

func overlapStart(runes []rune, start, end, overlap int) int {
	target := end - overlap
	if target <= start {
		return end
	}
	for i := target; i < end; i++ {
		if runes[i] == '\n' || isSentenceEnd(runes[i]) {
			return i + 1
		}
	}
	return target
}

func lastDoubleNewline(runes []rune, start, end int) int {
	for i := end - 1; i > start; i-- {
		if runes[i-1] == '\n' && runes[i] == '\n' {
			return i + 1
		}
	}
	return -1
}

func lastRune(runes []rune, start, end int, match func(rune) bool) int {
	for i := end - 1; i >= start; i-- {
		if match(runes[i]) {
			return i + 1
		}
	}
	return -1
}

func isSentenceEnd(r rune) bool {
	return strings.ContainsRune("。！？；!?;", r)
}
