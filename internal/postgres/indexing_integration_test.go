package postgres

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"insurance_agent_demo/internal/catalog"
	"insurance_agent_demo/internal/indexing"
)

func indexTestDB(t *testing.T) (*sql.DB, context.Context) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	db, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	var name string
	if err := db.QueryRowContext(ctx, `SELECT current_database()`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(name), "test") {
		t.Fatalf("database name must contain test: %q", name)
	}
	if err := RunMigrations(ctx, db, MigrationUp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `TRUNCATE insurance_products CASCADE`); err != nil {
		t.Fatal(err)
	}
	return db, ctx
}

type embeddingFunc func(context.Context, []string) ([][]float64, error)

func (f embeddingFunc) Embed(ctx context.Context, texts []string) ([][]float64, error) {
	return f(ctx, texts)
}
func embeddingVectors(count int) [][]float64 {
	v := make([][]float64, count)
	for i := range v {
		v[i] = make([]float64, indexing.Dimensions)
		v[i][0] = float64(i + 1)
	}
	return v
}

func TestM4IndexingIntegration(t *testing.T) {
	db, ctx := indexTestDB(t)
	repo := Repository{DB: db}
	input := integrationDocument("m4-index")
	for i := 2; i <= 3; i++ {
		c := input.Chunks[0]
		c.ChunkIndex = i
		input.Chunks = append(input.Chunks, c)
	}
	input.TotalChunks = 3
	stored, err := (catalog.Service{Repository: repo}).Store(ctx, input, false)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := indexing.NewProfile("", "")
	indexRepo := IndexRepository{DB: db}
	calls := 0
	service := indexing.Service{Repository: indexRepo, Profile: p, Embedder: embeddingFunc(func(_ context.Context, texts []string) ([][]float64, error) {
		calls++
		if calls == 2 {
			return nil, errors.New("upstream error")
		}
		return embeddingVectors(len(texts)), nil
	})}
	result, err := service.Index(ctx, indexing.Request{DocumentID: stored.DocumentID, BatchSize: 2})
	if err == nil || result.Processed != 2 || result.Status != "failed" {
		t.Fatalf("failure=%+v err=%v", result, err)
	}
	doc, err := repo.GetDocument(ctx, stored.DocumentID)
	if err != nil || doc.ProcessingStatus != "failed" || doc.FailedStage != "embedding" || doc.IndexedAt != nil || doc.EmbeddingDimensions != indexing.Dimensions {
		t.Fatalf("failed doc=%+v err=%v", doc, err)
	}
	service.Embedder = embeddingFunc(func(_ context.Context, texts []string) ([][]float64, error) { return embeddingVectors(len(texts)), nil })
	result, err = service.Index(ctx, indexing.Request{DocumentID: stored.DocumentID, BatchSize: 2})
	if err != nil || result.Processed != 1 || result.Reused != 2 || result.Status != "ready" {
		t.Fatalf("resume=%+v err=%v", result, err)
	}
	doc, err = repo.GetDocument(ctx, stored.DocumentID)
	if err != nil || doc.IndexedAt == nil || doc.LastError != "" || doc.EmbeddingProfile != p.Key() {
		t.Fatalf("ready doc=%+v err=%v", doc, err)
	}
	service.Embedder = embeddingFunc(func(context.Context, []string) ([][]float64, error) {
		t.Fatal("skipped document called model")
		return nil, nil
	})
	result, err = service.Index(ctx, indexing.Request{DocumentID: stored.DocumentID})
	if err != nil || result.Outcome != "skipped" || result.Reused != 3 {
		t.Fatalf("skip=%+v err=%v", result, err)
	}
	other, _ := indexing.NewProfile("https://other.example/v1", p.Model)
	service.Profile = other
	if _, err := service.Index(ctx, indexing.Request{DocumentID: stored.DocumentID}); !errors.Is(err, indexing.ErrProfileMismatch) {
		t.Fatalf("profile mismatch=%v", err)
	}
	service.Embedder = embeddingFunc(func(_ context.Context, texts []string) ([][]float64, error) { return embeddingVectors(len(texts)), nil })
	result, err = service.Index(ctx, indexing.Request{DocumentID: stored.DocumentID, Reindex: true})
	if err != nil || result.Processed != 3 || result.Reused != 0 {
		t.Fatalf("reindex=%+v err=%v", result, err)
	}
	var definition string
	if err := db.QueryRowContext(ctx, `SELECT indexdef FROM pg_indexes WHERE indexname = 'document_chunks_embedding_hnsw_idx'`).Scan(&definition); err != nil || !strings.Contains(definition, "hnsw (embedding vector_cosine_ops)") {
		t.Fatalf("index=%q err=%v", definition, err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM document_chunks WHERE document_id = $1 AND vector_dims(embedding)=$3 AND embedding_model=$2 AND embedded_at IS NOT NULL`, stored.DocumentID, p.Model, indexing.Dimensions).Scan(&count); err != nil || count != 3 {
		t.Fatalf("vector count=%d err=%v", count, err)
	}

	t.Run("document lock and replacement", func(t *testing.T) {
		session, err := indexRepo.Lock(ctx, stored.DocumentID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := indexRepo.Lock(ctx, stored.DocumentID); !errors.Is(err, indexing.ErrBusy) {
			t.Fatalf("concurrent lock=%v", err)
		}
		if _, err := repo.Store(ctx, input, true); !errors.Is(err, indexing.ErrBusy) {
			t.Fatalf("replacement during index=%v", err)
		}
		// Ordinary duplicate ingestion is still harmless and idempotent.
		if got, err := repo.Store(ctx, input, false); err != nil || got.Outcome != catalog.OutcomeSkipped {
			t.Fatalf("duplicate=%+v %v", got, err)
		}
		if err := session.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.Store(ctx, input, true); err != nil {
			t.Fatal(err)
		}
		doc, err := repo.GetDocument(ctx, stored.DocumentID)
		if err != nil || doc.EmbeddingProfile != "" || doc.IndexedAt != nil || doc.ProcessingStatus != "chunked" {
			t.Fatalf("replace=%+v err=%v", doc, err)
		}
	})

	t.Run("atomic batch and interrupted recovery", func(t *testing.T) {
		session, err := indexRepo.Lock(ctx, stored.DocumentID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := session.Prepare(ctx, p, false); err != nil {
			t.Fatal(err)
		}
		chunks, err := session.Pending(ctx, 3)
		if err != nil {
			t.Fatal(err)
		}
		broken := append([]indexing.Chunk(nil), chunks...)
		broken[1].ID = "00000000-0000-0000-0000-000000000000"
		if err := session.SaveBatch(ctx, p, broken, embeddingVectors(3)); err == nil {
			t.Fatal("broken batch committed")
		}
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM document_chunks WHERE document_id=$1 AND embedding IS NOT NULL`, stored.DocumentID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("partial batch=%d err=%v", count, err)
		}
		if err := session.Complete(ctx); err == nil {
			t.Fatal("incomplete document became ready")
		}
		if err := session.SaveBatch(ctx, p, chunks[:1], embeddingVectors(1)); err != nil {
			t.Fatal(err)
		}
		if err := session.Close(); err != nil {
			t.Fatal(err)
		} // Simulate process interruption: no Fail call.
		service.Profile = p
		result, err := service.Index(ctx, indexing.Request{DocumentID: stored.DocumentID})
		if err != nil || result.Processed != 2 || result.Reused != 1 {
			t.Fatalf("interrupted resume=%+v %v", result, err)
		}
	})

	t.Run("lost connection releases lock", func(t *testing.T) {
		session, err := indexRepo.Lock(ctx, stored.DocumentID)
		if err != nil {
			t.Fatal(err)
		}
		s := session.(*indexSession)
		var pid int
		if err := s.conn.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `SELECT pg_terminate_backend($1)`, pid); err != nil {
			t.Fatal(err)
		}
		if _, err := session.Pending(ctx, 1); err == nil {
			t.Fatal("lost connection transparently reconnected")
		}
		if err := session.Close(); err == nil {
			t.Fatal("lost connection cleanup failure not reported")
		}
		recovered, err := indexRepo.Lock(ctx, stored.DocumentID)
		if err != nil {
			t.Fatal(err)
		}
		if err := recovered.Close(); err != nil {
			t.Fatal(err)
		}
	})
	if _, err := db.ExecContext(ctx, `DELETE FROM documents WHERE id=$1`, stored.DocumentID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM document_chunks WHERE document_id=$1`, stored.DocumentID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("cascade count=%d err=%v", count, err)
	}
	if _, err := service.Index(ctx, indexing.Request{DocumentID: stored.DocumentID}); !errors.Is(err, catalog.ErrNotFound) {
		t.Fatalf("missing doc=%v", err)
	}
}

func TestM4MigrationPreservesM3Data(t *testing.T) {
	db, ctx := indexTestDB(t)
	// M4 now has two append-only migrations: 00002 introduced the original
	// 1536-dimensional schema and 00003 switched it to local Qwen/1024.
	if err := RunMigrations(ctx, db, MigrationDown); err != nil {
		t.Fatal(err)
	}
	if err := RunMigrations(ctx, db, MigrationDown); err != nil {
		t.Fatal(err)
	}
	// Write through the M3 insert path before applying M4.
	input := integrationDocument("m3-upgrade")
	stored, err := (Repository{DB: db}).Store(ctx, input, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := RunMigrations(ctx, db, MigrationUp); err != nil {
		t.Fatal(err)
	}
	p, _ := indexing.NewProfile("", "")
	s := indexing.Service{Repository: IndexRepository{DB: db}, Profile: p, Embedder: embeddingFunc(func(_ context.Context, texts []string) ([][]float64, error) { return embeddingVectors(len(texts)), nil })}
	if _, err := s.Index(ctx, indexing.Request{DocumentID: stored.DocumentID}); err != nil {
		t.Fatal(err)
	}
	if err := RunMigrations(ctx, db, MigrationDown); err != nil {
		t.Fatal(err)
	}
	// Rolling back only 00003 restores the exact 00002 schema and clears Qwen
	// vectors because vectors from different dimensions cannot be converted.
	var vectorType string
	if err := db.QueryRowContext(ctx, `SELECT format_type(atttypid, atttypmod) FROM pg_attribute WHERE attrelid='document_chunks'::regclass AND attname='embedding'`).Scan(&vectorType); err != nil || vectorType != "vector(1536)" {
		t.Fatalf("00003 down type=%q err=%v", vectorType, err)
	}
	if err := RunMigrations(ctx, db, MigrationDown); err != nil {
		t.Fatal(err)
	}
	var text, status string
	err = db.QueryRowContext(ctx, `SELECT c.text,d.processing_status FROM documents d JOIN document_chunks c ON c.document_id=d.id WHERE d.id=$1`, stored.DocumentID).Scan(&text, &status)
	if err != nil || text != input.Chunks[0].Text || status != "chunked" {
		t.Fatalf("M3 preservation status=%q err=%v", status, err)
	}
	var columns int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_name='document_chunks' AND column_name='embedding'`).Scan(&columns); err != nil || columns != 0 {
		t.Fatalf("embedding column survived down: %d %v", columns, err)
	}
	if err := RunMigrations(ctx, db, MigrationUp); err != nil {
		t.Fatal(err)
	}
	doc, err := (Repository{DB: db}).GetDocument(ctx, stored.DocumentID)
	if err != nil || doc.EmbeddingProfile != "" || doc.TotalChunks != 1 {
		t.Fatalf("replay doc=%+v err=%v", doc, err)
	}
}
