package migrations

import (
	"io/fs"
	"strings"
	"testing"
)

func TestMigrationHistoryStartsWithM3DDL(t *testing.T) {
	entries, err := fs.ReadDir(Files, ".")
	if err != nil {
		t.Fatal(err)
	}
	const firstMigration = "00001_m3_create_document_catalog.sql"
	found := false
	for _, entry := range entries {
		if entry.Name() == firstMigration {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("migration history does not contain %s: %v", firstMigration, entries)
	}
	raw, err := Files.ReadFile(firstMigration)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, marker := range []string{"-- +goose Up", "-- +goose Down", "CREATE EXTENSION IF NOT EXISTS vector", "CREATE TABLE documents", "CREATE TABLE document_chunks"} {
		if !strings.Contains(text, marker) {
			t.Fatalf("migration missing %q", marker)
		}
	}
}
