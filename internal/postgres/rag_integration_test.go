package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"insurance_agent_demo/internal/catalog"
	"insurance_agent_demo/internal/rag"
	"insurance_agent_demo/internal/retrieval"
)

type ragSearchFunc func(context.Context, retrieval.Request) (retrieval.Result, error)

func (f ragSearchFunc) Search(ctx context.Context, r retrieval.Request) (retrieval.Result, error) {
	return f(ctx, r)
}

type ragGenerateFunc func(context.Context, []rag.Message) (rag.Generation, error)

func (f ragGenerateFunc) Generate(ctx context.Context, m []rag.Message) (rag.Generation, error) {
	return f(ctx, m)
}

func TestM6RunSnapshotsAndMigration(t *testing.T) {
	db, ctx := indexTestDB(t)
	if _, err := db.ExecContext(ctx, `TRUNCATE rag_runs`); err != nil {
		t.Fatal(err)
	}
	input := integrationDocument("m6-original")
	catalogService := catalog.Service{Repository: Repository{DB: db}}
	stored, err := catalogService.Store(ctx, input, false)
	if err != nil {
		t.Fatal(err)
	}
	var chunkID string
	if err := db.QueryRowContext(ctx, `SELECT id::text FROM document_chunks WHERE document_id=$1`, stored.DocumentID).Scan(&chunkID); err != nil {
		t.Fatal(err)
	}
	repository := RunRepository{DB: db}
	cfg, _ := rag.NewConfig("", "")
	service := rag.Service{Config: cfg, Runs: repository, Searcher: ragSearchFunc(func(context.Context, retrieval.Request) (retrieval.Result, error) {
		return retrieval.Result{Version: "v1", Hits: []retrieval.Hit{{ChunkID: chunkID, DocumentID: stored.DocumentID, ProductID: input.Metadata.ProductID, DocumentVersion: "v1", DocumentName: input.Metadata.Name, SourceSHA256: input.SourceSHA256, PageNumber: 1, Text: input.Chunks[0].Text}}}, nil
	}), Generator: ragGenerateFunc(func(ctx context.Context, m []rag.Message) (rag.Generation, error) {
		// A model request observes committed context, outside any writer transaction.
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM rag_runs WHERE status='running' AND jsonb_array_length(snapshot->'messages')=2`).Scan(&count); err != nil || count != 1 {
			t.Fatalf("uncommitted context: %d %v", count, err)
		}
		return rag.Generation{Content: `{"status":"answered","statements":[{"text":"条款载明保险责任。","evidence":[{"evidence_id":"E1","quote":"保险责任"}]}],"missing_information":[]}`, Model: "real-version", FinishReason: "stop"}, nil
	})}
	result, err := service.Ask(ctx, rag.Request{ProductID: input.Metadata.ProductID, Question: "保险责任？"})
	if err != nil {
		t.Fatal(err)
	}
	input.Chunks[0].Text = "第一条 替换后的责任。"
	input.Chunks[0].CharacterCount = len([]rune(input.Chunks[0].Text))
	if _, err := catalogService.Store(ctx, input, true); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM documents WHERE id=$1`, stored.DocumentID); err != nil {
		t.Fatal(err)
	}
	run, err := repository.Get(ctx, result.RunID)
	if err != nil || run.Status != "answered" || run.Result.Citations[0].Source.ChunkID != chunkID || run.Result.Citations[0].Source.Text != "第一条 保险责任。" || run.Generation.Model != "real-version" || run.Result.Usage != nil {
		t.Fatal(run, err)
	}
	if err := repository.Save(ctx, run); err == nil {
		t.Fatal("terminal run overwritten")
	}
	if _, err := repository.Get(ctx, "00000000-0000-0000-0000-000000000000"); !errors.Is(err, rag.ErrRunNotFound) {
		t.Fatal(err)
	}
	service.Searcher = ragSearchFunc(func(context.Context, retrieval.Request) (retrieval.Result, error) {
		return retrieval.Result{}, retrieval.ErrNoReadyDocuments
	})
	_, err = service.Ask(ctx, rag.Request{ProductID: input.Metadata.ProductID, Question: "失败？"})
	var public *rag.Error
	if !errors.As(err, &public) {
		t.Fatal(err)
	}
	run, err = repository.Get(ctx, public.RunID)
	if err != nil || run.Status != "failed" || run.Error.Code != "no_ready_documents" || run.FinishedAt == nil {
		t.Fatal(run, err)
	}
	service.Searcher = ragSearchFunc(func(context.Context, retrieval.Request) (retrieval.Result, error) {
		return retrieval.Result{Version: "v1", Hits: []retrieval.Hit{}}, nil
	})
	result, err = service.Ask(ctx, rag.Request{ProductID: input.Metadata.ProductID, Question: "缺少证据？"})
	if err != nil || !result.Abstained {
		t.Fatal(result, err)
	}
	run, err = repository.Get(ctx, result.RunID)
	if err != nil || run.Status != "abstained" {
		t.Fatal(run, err)
	}
	// Crash-like running records remain visible and clearly have no final result.
	running := rag.Run{ID: "00000000-0000-0000-0000-000000000001", Status: "running", StartedAt: time.Now().UTC(), Config: cfg}
	if err := repository.Create(ctx, running); err != nil {
		t.Fatal(err)
	}
	run, err = repository.Get(ctx, running.ID)
	if err != nil || run.Status != "running" || run.FinishedAt != nil || run.Result != nil {
		t.Fatal(run, err)
	}
	raw, _ := json.Marshal(run)
	if !json.Valid(raw) {
		t.Fatal("invalid snapshot")
	}
	if err := RunMigrations(ctx, db, MigrationDown); err != nil {
		t.Fatal(err)
	}
	var tables int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_name='rag_runs'`).Scan(&tables); err != nil || tables != 0 {
		t.Fatal(tables, err)
	}
	if err := RunMigrations(ctx, db, MigrationUp); err != nil {
		t.Fatal(err)
	}
}
