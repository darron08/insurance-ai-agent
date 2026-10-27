package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"insurance_agent_demo/internal/catalog"
	"insurance_agent_demo/internal/document"
)

func integrationDocument(seed string) document.ChunkedDocument {
	text := "第一条 保险责任。"
	sum := sha256.Sum256([]byte(seed))
	metadata := document.DocumentMetadata{
		ProductID: "integration-demo",
		Name:      "集成测试条款",
		Version:   "v1",
		SourceURL: "https://example.com/integration-policy",
	}
	return document.ChunkedDocument{
		Metadata:         metadata,
		SourceFilePath:   "/tmp/integration-policy.pdf",
		SourceSHA256:     hex.EncodeToString(sum[:]),
		SourceImportedAt: time.Date(2026, 10, 26, 1, 0, 0, 0, time.UTC),
		SourceParser:     "test-parser@1",
		TotalPages:       1,
		Chunking: document.ChunkingConfig{
			Strategy:          "clause-aware-v1",
			MaxCharacters:     800,
			OverlapCharacters: 100,
		},
		TotalChunks: 1,
		Chunks: []document.Chunk{{
			ChunkIndex:      1,
			ProductID:       metadata.ProductID,
			DocumentName:    metadata.Name,
			DocumentVersion: metadata.Version,
			PageNumber:      1,
			SectionTitle:    "保险责任",
			ClauseID:        "第一条",
			PartIndex:       1,
			PartCount:       1,
			CharacterCount:  utf8.RuneCountInString(text),
			Text:            text,
		}},
		ChunkedAt: time.Date(2026, 10, 26, 2, 0, 0, 0, time.UTC),
	}
}

func TestMigrationsAndRepository(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	db, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var databaseName string
	if err := db.QueryRowContext(ctx, `SELECT current_database()`).Scan(&databaseName); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(databaseName), "test") {
		t.Fatalf("TEST_DATABASE_URL must point to a database whose name contains test, got %q", databaseName)
	}

	if err := RunMigrations(ctx, db, MigrationUp); err != nil {
		t.Fatal(err)
	}
	if err := RunMigrations(ctx, db, MigrationUp); err != nil {
		t.Fatalf("repeated migration: %v", err)
	}
	if err := RunMigrations(ctx, db, MigrationStatus); err != nil {
		t.Fatal(err)
	}
	var vectorVersion string
	if err := db.QueryRowContext(ctx, `SELECT extversion FROM pg_extension WHERE extname = 'vector'`).Scan(&vectorVersion); err != nil || vectorVersion == "" {
		t.Fatalf("vector extension version = %q, error = %v", vectorVersion, err)
	}
	if _, err := db.ExecContext(ctx, `TRUNCATE insurance_products CASCADE`); err != nil {
		t.Fatal(err)
	}

	repository := Repository{DB: db}
	service := catalog.Service{Repository: repository}
	input := integrationDocument("insert")
	inserted, err := service.Store(ctx, input, false)
	if err != nil {
		t.Fatal(err)
	}
	if inserted.Outcome != catalog.OutcomeInserted || inserted.Status != catalog.StatusChunked || inserted.ChunkCount != 1 {
		t.Fatalf("inserted = %+v", inserted)
	}
	if _, err := db.ExecContext(ctx, `UPDATE documents SET processing_status = 'ready' WHERE id = $1`, inserted.DocumentID); err != nil {
		t.Fatal(err)
	}
	skipped, err := service.Store(ctx, input, false)
	if err != nil {
		t.Fatal(err)
	}
	if skipped.DocumentID != inserted.DocumentID || skipped.Outcome != catalog.OutcomeSkipped || skipped.Status != catalog.StatusReady {
		t.Fatalf("skipped = %+v", skipped)
	}

	doc, err := service.GetDocument(ctx, inserted.DocumentID)
	if err != nil || doc.ID != inserted.DocumentID || doc.TotalChunks != 1 {
		t.Fatalf("document = %+v, error = %v", doc, err)
	}
	documents, err := service.ListDocuments(ctx, catalog.ListFilter{ProductID: input.Metadata.ProductID, Status: catalog.StatusReady})
	if err != nil || len(documents) != 1 {
		t.Fatalf("documents = %+v, error = %v", documents, err)
	}
	chunks, err := service.ListChunks(ctx, inserted.DocumentID)
	if err != nil || len(chunks) != 1 || chunks[0].ChunkIndex != 1 {
		t.Fatalf("chunks = %+v, error = %v", chunks, err)
	}

	replacementText := "第一条 替换后的保险责任。"
	input.Metadata.Name = "集成测试条款（更新）"
	input.Chunks[0].DocumentName = input.Metadata.Name
	input.Chunks[0].Text = replacementText
	input.Chunks[0].CharacterCount = utf8.RuneCountInString(replacementText)
	replaced, err := service.Store(ctx, input, true)
	if err != nil {
		t.Fatal(err)
	}
	if replaced.DocumentID != inserted.DocumentID || replaced.Outcome != catalog.OutcomeReplaced || replaced.Status != catalog.StatusChunked {
		t.Fatalf("replaced = %+v", replaced)
	}
	chunks, err = service.ListChunks(ctx, inserted.DocumentID)
	if err != nil || len(chunks) != 1 || chunks[0].Text != replacementText {
		t.Fatalf("replacement chunks = %+v, error = %v", chunks, err)
	}

	broken := integrationDocument("rollback")
	broken.TotalChunks = 2
	broken.Chunks = append(broken.Chunks, broken.Chunks[0])
	if _, err := repository.Store(ctx, broken, false); err == nil {
		t.Fatal("broken insert unexpectedly succeeded")
	}
	var rollbackCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM documents WHERE source_sha256 = $1`, broken.SourceSHA256).Scan(&rollbackCount); err != nil || rollbackCount != 0 {
		t.Fatalf("rollback document count = %d, error = %v", rollbackCount, err)
	}

	if _, err := db.ExecContext(ctx, `DELETE FROM documents WHERE id = $1`, inserted.DocumentID); err != nil {
		t.Fatal(err)
	}
	var chunkCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM document_chunks WHERE document_id = $1`, inserted.DocumentID).Scan(&chunkCount); err != nil || chunkCount != 0 {
		t.Fatalf("cascade chunk count = %d, error = %v", chunkCount, err)
	}

	// M6 is the newest migration. Roll it back before checking M4/M3 history.
	if err := RunMigrations(ctx, db, MigrationDown); err != nil {
		t.Fatal(err)
	}
	if err := RunMigrations(ctx, db, MigrationDown); err != nil {
		t.Fatal(err)
	}
	// The first down reverts Qwen's 1024-dimensional migration to the original
	// empty M4 schema. The second down removes M4 columns; M3 tables still live.
	var preserved string
	if err := db.QueryRowContext(ctx, `SELECT to_regclass('public.documents')::text`).Scan(&preserved); err != nil || preserved != "documents" {
		t.Fatalf("documents after Qwen down = %q, error = %v", preserved, err)
	}
	if err := RunMigrations(ctx, db, MigrationDown); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT to_regclass('public.documents')::text`).Scan(&preserved); err != nil || preserved != "documents" {
		t.Fatalf("M3 table after M4 down = %q, error = %v", preserved, err)
	}
	if err := RunMigrations(ctx, db, MigrationDown); err != nil {
		t.Fatal(err)
	}
	var tableName sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT to_regclass('public.documents')`).Scan(&tableName); err != nil || tableName.Valid {
		t.Fatalf("documents after down = %+v, error = %v", tableName, err)
	}
	if err := RunMigrations(ctx, db, MigrationUp); err != nil {
		t.Fatalf("migration replay: %v", err)
	}
}
