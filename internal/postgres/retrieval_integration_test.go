package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"insurance_agent_demo/internal/catalog"
	"insurance_agent_demo/internal/chunker"
	"insurance_agent_demo/internal/document"
	"insurance_agent_demo/internal/indexing"
	"insurance_agent_demo/internal/ingest"
	"insurance_agent_demo/internal/parser"
	"insurance_agent_demo/internal/retrieval"
	"insurance_agent_demo/internal/testpdf"
)

func TestM5VersionImportPipeline(t *testing.T) {
	db, ctx := indexTestDB(t)
	file := filepath.Join(t.TempDir(), "policy.pdf")
	if err := os.WriteFile(file, testpdf.Build("Policy coverage", "", "Policy exclusions"), 0600); err != nil {
		t.Fatal(err)
	}
	service := catalog.Service{Repository: Repository{DB: db}}
	ids := map[string]string{}
	for _, version := range []string{"", "v1", "v2"} {
		_, imported, err := (ingest.Service{Parser: parser.PDF{}}).Run(ingest.Request{File: file, OutputDir: t.TempDir(), Metadata: document.DocumentMetadata{ProductID: "version-pipeline", Version: version}})
		if err != nil {
			t.Fatal(err)
		}
		_, path, err := (chunker.Service{}).Run(imported, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		input, err := catalog.LoadChunkedDocument(path)
		if err != nil {
			t.Fatal(err)
		}
		stored, err := service.Store(ctx, input, false)
		if err != nil {
			t.Fatal(err)
		}
		ids[version] = stored.DocumentID
		doc, err := service.GetDocument(ctx, stored.DocumentID)
		if err != nil || doc.Version != version {
			t.Fatalf("document=%+v err=%v", doc, err)
		}
		chunks, err := service.ListChunks(ctx, stored.DocumentID)
		if err != nil || len(chunks) != 2 || chunks[0].PageNumber != 1 || chunks[1].PageNumber != 3 {
			t.Fatalf("chunks=%+v err=%v", chunks, err)
		}
		for _, c := range input.Chunks {
			if c.DocumentVersion != version {
				t.Fatalf("chunk version=%q", c.DocumentVersion)
			}
		}
		duplicate, err := service.Store(ctx, input, false)
		if err != nil || duplicate.DocumentID != stored.DocumentID || duplicate.Outcome != catalog.OutcomeSkipped {
			t.Fatalf("duplicate=%+v err=%v", duplicate, err)
		}
	}
	if ids[""] == ids["v1"] || ids["v1"] == ids["v2"] {
		t.Fatal("distinct versions merged")
	}
}

func angleVector(angle float64) []float64 {
	v := make([]float64, indexing.Dimensions)
	v[0], v[1] = math.Cos(angle), math.Sin(angle)
	return v
}

func storeSearchDocument(t *testing.T, db *sql.DB, ctx context.Context, seed, product, version string, p indexing.Profile, angles ...float64) (document.ChunkedDocument, string) {
	t.Helper()
	input := integrationDocument(seed)
	input.Metadata.ProductID, input.Metadata.Version = product, version
	input.TotalPages = 3
	base := input.Chunks[0]
	input.Chunks = nil
	for i := range angles {
		c := base
		c.ProductID, c.DocumentVersion, c.ChunkIndex, c.PageNumber = product, version, i+1, 3
		input.Chunks = append(input.Chunks, c)
	}
	input.TotalChunks = len(input.Chunks)
	stored, err := (catalog.Service{Repository: Repository{DB: db}}).Store(ctx, input, false)
	if err != nil {
		t.Fatal(err)
	}
	offset := 0
	s := indexing.Service{Profile: p, Repository: IndexRepository{DB: db}, Embedder: embeddingFunc(func(_ context.Context, texts []string) ([][]float64, error) {
		vectors := make([][]float64, len(texts))
		for i := range texts {
			vectors[i] = angleVector(angles[offset+i])
		}
		offset += len(texts)
		return vectors, nil
	})}
	if _, err := s.Index(ctx, indexing.Request{DocumentID: stored.DocumentID}); err != nil {
		t.Fatal(err)
	}
	return input, stored.DocumentID
}

func TestM5SearchIntegration(t *testing.T) {
	db, ctx := indexTestDB(t)
	p, _ := indexing.NewProfile("", "")
	input, id := storeSearchDocument(t, db, ctx, "search-main", "target", "v1", p, 0.3, 0, 0.3, math.Pi)
	storeSearchDocument(t, db, ctx, "other-product", "other", "v1", p, 0)
	storeSearchDocument(t, db, ctx, "other-version", "target", "v2", p, 0)
	version := "v1"
	params := retrieval.SearchParams{Request: retrieval.Request{ProductID: "target", Query: "保险责任", Version: &version, TopK: 100, Mode: retrieval.ModeExact}, Profile: p, Vector: angleVector(0)}
	repo := SearchRepository{DB: db}
	result, err := repo.Search(ctx, params)
	if err != nil || len(result.Hits) != 4 || result.Version != "v1" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	for i, index := range []int{2, 1, 3, 4} {
		h := result.Hits[i]
		if h.DocumentID != id || h.ChunkIndex != index || h.PageNumber != 3 || h.ProductID != "target" || h.DocumentVersion != "v1" || h.DocumentName != input.Metadata.Name || h.SourceURL != input.Metadata.SourceURL || h.SourceSHA256 != input.SourceSHA256 || h.SectionTitle != "保险责任" || h.ClauseID != "第一条" || h.PartIndex != 1 || h.PartCount != 1 || h.Text != input.Chunks[index-1].Text || h.ChunkID == "" || math.Abs(h.Score-(1-h.Distance)) > 1e-10 {
			t.Fatalf("hit=%+v", h)
		}
		if math.Abs(h.Distance-(1-math.Cos([]float64{0, 0.3, 0.3, math.Pi}[i]))) > 1e-6 {
			t.Fatalf("distance=%f", h.Distance)
		}
	}
	params.Request.TopK = 2
	for _, mode := range []string{retrieval.ModeExact, retrieval.ModeHNSW} {
		params.Request.Mode = mode
		got, err := repo.Search(ctx, params)
		if err != nil || len(got.Hits) != 2 || got.Hits[0].ChunkIndex != 2 {
			t.Fatalf("mode=%s result=%+v err=%v", mode, got, err)
		}
	}

	params.Request.Version = nil
	_, err = repo.Search(ctx, params)
	var required *retrieval.VersionRequiredError
	if !errors.As(err, &required) || len(required.Versions) != 2 || required.Versions[0] != "v1" || required.Versions[1] != "v2" {
		t.Fatalf("versions=%+v err=%v", required, err)
	}
	params.Request.ProductID = "other"
	if got, err := repo.Search(ctx, params); err != nil || got.Version != "v1" || len(got.Hits) != 1 {
		t.Fatalf("auto version=%+v err=%v", got, err)
	}
	params.Request.ProductID = "missing"
	if _, err := repo.Search(ctx, params); !errors.Is(err, retrieval.ErrNoReadyDocuments) {
		t.Fatal(err)
	}
	params.Request.ProductID, params.Request.Version = "target", &version
	p2, _ := indexing.NewProfile("https://other.example/v1", p.Model)
	params.Profile = p2
	if _, err := repo.Search(ctx, params); !errors.Is(err, retrieval.ErrProfileMismatch) {
		t.Fatal(err)
	}
	params.Profile = p

	// A second ready document with the wrong profile must fail the entire scope,
	// rather than silently searching only the compatible document.
	_, incompatible := storeSearchDocument(t, db, ctx, "mismatch-in-scope", "target", "v1", p2, 0)
	if _, err := repo.Search(ctx, params); !errors.Is(err, retrieval.ErrProfileMismatch) {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE documents SET processing_status='failed' WHERE id=$1`, incompatible); err != nil {
		t.Fatal(err)
	}
	if got, err := repo.Search(ctx, params); err != nil || len(got.Hits) != 2 {
		t.Fatalf("failed exclusion=%+v %v", got, err)
	}

	// Empty versions remain selectable even after a named version is added.
	storeSearchDocument(t, db, ctx, "legacy", "legacy-product", "", p, 0)
	params.Request.ProductID, params.Request.Version = "legacy-product", nil
	if got, err := repo.Search(ctx, params); err != nil || got.Version != "" {
		t.Fatalf("legacy=%+v %v", got, err)
	}
	storeSearchDocument(t, db, ctx, "legacy-v2", "legacy-product", "v2", p, 0)
	if _, err := repo.Search(ctx, params); !errors.As(err, &required) || len(required.Versions) != 2 || required.Versions[0] != "" || required.Versions[1] != "v2" {
		t.Fatalf("legacy version choices=%+v err=%v", required, err)
	}
	empty := ""
	params.Request.Version = &empty
	if got, err := repo.Search(ctx, params); err != nil || got.Version != "" || len(got.Hits) != 1 {
		t.Fatalf("explicit empty=%+v %v", got, err)
	}
	params.Request.Version = &version
	if _, err := repo.Search(ctx, params); !errors.Is(err, retrieval.ErrNoReadyDocuments) {
		t.Fatal(err)
	}

	// An otherwise ready row can still have no eligible vectors. Filtering must
	// never return chunks without embeddings or with a mismatched chunk model.
	params.Request.ProductID, params.Request.Version = "target", &version
	if _, err := db.ExecContext(ctx, `UPDATE document_chunks SET embedding_model='bad' WHERE document_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Search(ctx, params)
	if err != nil || got.Hits == nil || len(got.Hits) != 0 {
		t.Fatalf("bad chunk models=%+v %v", got, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE document_chunks SET embedding=NULL, embedding_model=NULL, embedded_at=NULL WHERE document_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if got, err := repo.Search(ctx, params); err != nil || len(got.Hits) != 0 {
		t.Fatalf("null vectors=%+v %v", got, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE documents SET processing_status='embedding' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Search(ctx, params); !errors.Is(err, retrieval.ErrNoReadyDocuments) {
		t.Fatal(err)
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := repo.Search(canceled, params); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	var status string
	if err := db.QueryRowContext(ctx, `SELECT processing_status FROM documents WHERE id=$1`, id).Scan(&status); err != nil || status != "embedding" {
		t.Fatalf("status=%s %v", status, err)
	}

	// Exact ties across different documents must also have deterministic order.
	storeSearchDocument(t, db, ctx, "tie-a", "ties", "v1", p, 0, 0)
	storeSearchDocument(t, db, ctx, "tie-b", "ties", "v1", p, 0, 0)
	params.Request.ProductID, params.Request.Mode, params.Request.TopK = "ties", retrieval.ModeExact, 4
	ties, err := repo.Search(ctx, params)
	if err != nil || len(ties.Hits) != 4 {
		t.Fatalf("ties=%+v err=%v", ties, err)
	}
	for i := 1; i < len(ties.Hits); i++ {
		left, right := ties.Hits[i-1], ties.Hits[i]
		if left.DocumentID > right.DocumentID || (left.DocumentID == right.DocumentID && left.ChunkIndex >= right.ChunkIndex) {
			t.Fatalf("unstable ties: %+v", ties.Hits)
		}
	}
}

func TestM5SnapshotDuringReplacementAndReindex(t *testing.T) {
	db, ctx := indexTestDB(t)
	p, _ := indexing.NewProfile("", "")
	for _, operation := range []string{"replace", "reindex"} {
		t.Run(operation, func(t *testing.T) {
			input, id := storeSearchDocument(t, db, ctx, "snapshot-"+operation, operation, "v1", p, 0)
			params := retrieval.SearchParams{Request: retrieval.Request{ProductID: operation, Query: "保险责任", TopK: 5, Mode: retrieval.ModeExact}, Profile: p, Vector: angleVector(0)}
			tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			// searchSnapshot's first query establishes the same snapshot used below.
			old, err := searchSnapshot(ctx, tx, params)
			if err != nil || len(old.Hits) != 1 {
				t.Fatalf("before=%+v %v", old, err)
			}
			if operation == "replace" {
				input.Metadata.Name = "更新后的名称"
				input.Chunks[0].DocumentName = input.Metadata.Name
				input.Chunks[0].Text = "第二条 新的条款正文。"
				input.Chunks[0].CharacterCount = utf8.RuneCountInString(input.Chunks[0].Text)
				if _, err := (Repository{DB: db}).Store(ctx, input, true); err != nil {
					t.Fatal(err)
				}
			} else {
				session, err := (IndexRepository{DB: db}).Lock(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				_, prepareErr := session.Prepare(ctx, p, true)
				closeErr := session.Close()
				if prepareErr != nil || closeErr != nil {
					t.Fatalf("prepare=%v close=%v", prepareErr, closeErr)
				}
			}
			// The concurrent writer committed while the read transaction remained open.
			stillOld, err := searchSnapshot(ctx, tx, params)
			if err != nil || len(stillOld.Hits) != 1 || stillOld.Hits[0] != old.Hits[0] {
				t.Fatalf("snapshot=%+v err=%v", stillOld, err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if _, err := (SearchRepository{DB: db}).Search(ctx, params); !errors.Is(err, retrieval.ErrNoReadyDocuments) {
				t.Fatalf("new snapshot=%v", err)
			}
		})
	}
}

func TestM5HNSWFilteringPlanAndConnectionSettings(t *testing.T) {
	db, ctx := indexTestDB(t)
	p, _ := indexing.NewProfile("", "")
	noise := make([]float64, 250)
	for i := range noise {
		noise[i] = float64(i) * 0.0001
	}
	storeSearchDocument(t, db, ctx, "noise", "unrelated", "v1", p, noise...)
	storeSearchDocument(t, db, ctx, "filtered", "selected", "v1", p, 0.5, 0.6, 0.7)
	params := retrieval.SearchParams{Request: retrieval.Request{ProductID: "selected", Query: "保险责任", TopK: 3, Mode: retrieval.ModeHNSW}, Profile: p, Vector: angleVector(0)}
	// Hold one pool connection to make post-search setting checks meaningful.
	db.SetMaxOpenConns(1)
	settings := func() string {
		var value string
		if err := db.QueryRowContext(ctx, `SELECT current_setting('hnsw.iterative_scan') || ':' || current_setting('hnsw.ef_search') || ':' || current_setting('hnsw.max_scan_tuples')`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	// Load the extension's GUCs into the reused backend before recording defaults.
	if _, err := db.ExecContext(ctx, `SELECT '[1,0]'::vector <=> '[1,0]'::vector`); err != nil {
		t.Fatal(err)
	}
	before := settings()
	result, err := (SearchRepository{DB: db}).Search(ctx, params)
	if err != nil || len(result.Hits) != 3 || math.Abs(result.Hits[0].Distance-(1-math.Cos(0.5))) > 1e-6 {
		t.Fatalf("filtered=%+v %v", result, err)
	}
	if after := settings(); after != before {
		t.Fatalf("connection settings leaked: %q -> %q", before, after)
	}
	params.Request.Mode = retrieval.ModeExact
	exact, err := (SearchRepository{DB: db}).Search(ctx, params)
	if err != nil || len(exact.Hits) != 3 {
		t.Fatalf("exact=%+v %v", exact, err)
	}
	for i := range exact.Hits {
		if exact.Hits[i] != result.Hits[i] {
			t.Fatalf("rank %d differs", i)
		}
	}

	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	// Force consideration of the index for this diagnostic only. Production lets
	// the planner choose exact scans for small or highly selective scopes.
	for _, setting := range []string{`SET LOCAL enable_seqscan=off`, `SET LOCAL enable_sort=off`, `SET LOCAL enable_bitmapscan=off`, `SET LOCAL hnsw.iterative_scan=strict_order`, `SET LOCAL hnsw.ef_search=100`} {
		if _, err := tx.ExecContext(ctx, setting); err != nil {
			t.Fatal(err)
		}
	}
	var plan []byte
	args := []any{"selected", "v1", p.Key(), p.Model, p.Dimensions, vectorLiteral(params.Vector), 3}
	if err := tx.QueryRowContext(ctx, `EXPLAIN (FORMAT JSON) `+searchHNSWSQL, args...).Scan(&plan); err != nil {
		t.Fatal(err)
	}
	if !json.Valid(plan) || !strings.Contains(string(plan), "document_chunks_embedding_hnsw_idx") {
		t.Fatalf("HNSW path unavailable: %s", plan)
	}
	// Execute the forced HNSW query too: unrelated nearer vectors must not consume K.
	rows, err := tx.QueryContext(ctx, searchHNSWSQL, args...)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for rows.Next() {
		count++
	}
	err = rows.Err()
	rows.Close()
	if err != nil || count != 3 {
		t.Fatalf("forced HNSW count=%d err=%v", count, err)
	}
	if err := tx.QueryRowContext(ctx, `EXPLAIN (FORMAT JSON) `+searchExactSQL, args...).Scan(&plan); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(plan), "document_chunks_embedding_hnsw_idx") {
		t.Fatalf("exact used HNSW: %s", plan)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	// A pool wait is also bounded by the caller, with no persisted state changes.
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	deadline, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if _, err := (SearchRepository{DB: db}).Search(deadline, params); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
