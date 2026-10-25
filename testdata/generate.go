//go:build ignore

// Run from the repository root: go run ./testdata/generate.go
package main

import (
	"insurance_agent_demo/internal/testpdf"
	"log"
	"os"
)

func main() {
	data := testpdf.Build("Page 1: Synthetic insurance fixture. Not a real policy.", "Page 2: Test coverage clause. For parser tests only.", "Page 3: Test exclusion clause. No insurance advice.")
	if err := os.WriteFile("testdata/sample.pdf", data, 0644); err != nil {
		log.Fatal(err)
	}
}
