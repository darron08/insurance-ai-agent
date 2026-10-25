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
