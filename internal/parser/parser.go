package parser

import "insurance_agent_demo/internal/document"

type Result struct {
	Pages                       []document.Page
	Engine                      string
	PrivateUseCharactersRemoved int
}

// Parser extracts page text from a local PDF file.
type Parser interface {
	Parse(path string) (Result, error)
}
