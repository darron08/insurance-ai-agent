package document

import "time"

type DocumentMetadata struct {
	ProductID string `json:"product_id"`
	Name      string `json:"name"`
	Version   string `json:"version"`
	SourceURL string `json:"source_url"`
}

// PageNumber is the physical PDF page, starting at 1, not the printed page label.
type Page struct {
	PageNumber int    `json:"page_number"`
	Text       string `json:"text"`
}

type Quality struct {
	Status                      string `json:"status"`
	ReplacementCharacters       int    `json:"replacement_characters"`
	PrivateUseCharactersRemoved int    `json:"private_use_characters_removed"`
	TextPages                   int    `json:"text_pages"`
	EmptyPages                  int    `json:"empty_pages"`
}

type Document struct {
	Metadata       DocumentMetadata `json:"metadata"`
	FilePath       string           `json:"file_path"`
	SHA256         string           `json:"sha256"`
	TotalPages     int              `json:"total_pages"`
	CharacterCount int              `json:"character_count"`
	Pages          []Page           `json:"pages"`
	Quality        Quality          `json:"quality"`
	ImportedAt     time.Time        `json:"imported_at"`
	Parser         string           `json:"parser"`
}

type ChunkingConfig struct {
	Strategy          string `json:"strategy"`
	MaxCharacters     int    `json:"max_characters"`
	OverlapCharacters int    `json:"overlap_characters"`
}

// Chunk always belongs to one physical PDF page. A clause that crosses a page
// boundary is represented by multiple chunks with the same clause metadata.
type Chunk struct {
	ChunkIndex      int    `json:"chunk_index"`
	ProductID       string `json:"product_id"`
	DocumentName    string `json:"document_name"`
	DocumentVersion string `json:"document_version"`
	PageNumber      int    `json:"page_number"`
	SectionTitle    string `json:"section_title"`
	ClauseID        string `json:"clause_id"`
	PartIndex       int    `json:"part_index"`
	PartCount       int    `json:"part_count"`
	CharacterCount  int    `json:"character_count"`
	Text            string `json:"text"`
}

type ChunkedDocument struct {
	Metadata         DocumentMetadata `json:"metadata"`
	SourceFilePath   string           `json:"source_file_path"`
	SourceSHA256     string           `json:"source_sha256"`
	SourceImportedAt time.Time        `json:"source_imported_at"`
	SourceParser     string           `json:"source_parser"`
	TotalPages       int              `json:"total_pages"`
	Chunking         ChunkingConfig   `json:"chunking"`
	TotalChunks      int              `json:"total_chunks"`
	Chunks           []Chunk          `json:"chunks"`
	ChunkedAt        time.Time        `json:"chunked_at"`
}
