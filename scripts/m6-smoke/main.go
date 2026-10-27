// m6-smoke is a manual acceptance harness, never invoked by go test. Run from
// the repository root with a dedicated *_m6_smoke_test database and local keys.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"time"

	"insurance_agent_demo/internal/catalog"
	"insurance_agent_demo/internal/chunker"
	"insurance_agent_demo/internal/document"
	"insurance_agent_demo/internal/embedding"
	"insurance_agent_demo/internal/generation"
	"insurance_agent_demo/internal/httpapi"
	"insurance_agent_demo/internal/indexing"
	"insurance_agent_demo/internal/ingest"
	"insurance_agent_demo/internal/parser"
	"insurance_agent_demo/internal/postgres"
	"insurance_agent_demo/internal/rag"
	"insurance_agent_demo/internal/retrieval"
	"insurance_agent_demo/internal/testpdf"
)

type smokeCase struct {
	Name           string             `json:"name"`
	Product        string             `json:"product"`
	Question       string             `json:"question"`
	Reason         string             `json:"expected_reason"`
	Evidence       []expectedEvidence `json:"expected_evidence"`
	AnswerContains []string           `json:"answer_contains"`
}
type expectedEvidence struct {
	Page     int    `json:"page"`
	Contains string `json:"contains"`
}
type outcome struct {
	Case       smokeCase       `json:"case"`
	HTTPStatus int             `json:"http_status"`
	Passed     bool            `json:"passed"`
	Response   json.RawMessage `json:"response"`
	Run        *rag.Run        `json:"run"`
}

func main() {
	if err := execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func execute() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	db, err := postgres.Open(ctx, os.Getenv("M6_SMOKE_DATABASE_URL"))
	if err != nil {
		return errors.New("请设置专用 M6_SMOKE_DATABASE_URL")
	}
	defer db.Close()
	var name string
	if err = db.QueryRowContext(ctx, `SELECT current_database()`).Scan(&name); err != nil || !strings.HasSuffix(name, "_m6_smoke_test") {
		return errors.New("联调只允许在名称以 _m6_smoke_test 结尾的独立数据库中运行")
	}
	if err = postgres.RunMigrations(ctx, db, postgres.MigrationUp); err != nil {
		return err
	}
	cfg, err := rag.NewConfig(os.Getenv("LLM_BASE_URL"), os.Getenv("LLM_MODEL"))
	if err != nil {
		return err
	}
	model, err := generation.New(cfg, os.Getenv("LLM_API_KEY"))
	if err != nil {
		return err
	}
	profile, err := indexing.NewProfile(os.Getenv("EMBEDDING_BASE_URL"), os.Getenv("EMBEDDING_MODEL"))
	if err != nil {
		return err
	}
	embedder, err := embedding.New(profile, os.Getenv("EMBEDDING_API_KEY"))
	if err != nil {
		return err
	}
	outdir := "var/m6-smoke"
	if err = os.MkdirAll(outdir, 0700); err != nil {
		return err
	}
	cat := catalog.Service{Repository: postgres.Repository{DB: db}}
	index := indexing.Service{Repository: postgres.IndexRepository{DB: db}, Embedder: embedder, Profile: profile}
	fixtures := []struct{ product, version, file, title, source string }{
		{"m6-ankang", "2020", "testdata/real/pingan-ankang-te-xu-ban-2020-medical.pdf", "平安安康（特需版2020）医疗保险条款", "https://resources.pingan.com/app_upload/file/health/aktxb20200751.pdf"},
		{"m6-travel", "historical-handbook", "testdata/real/pingan-travel-accident-terms-handbook.pdf", "平安历史旅行险条款手册", "https://www.pingan.com/app_upload/file/insurance/201004271656.pdf"},
	}
	for _, f := range fixtures {
		doc, _, err := (ingest.Service{Parser: parser.PDF{}}).Run(ingest.Request{File: f.file, OutputDir: filepath.Join(outdir, "imports"), Metadata: document.DocumentMetadata{ProductID: f.product, Version: f.version, Name: f.title, SourceURL: f.source}})
		if err != nil {
			return err
		}
		chunks, err := (chunker.ClauseAware{}).Split(doc)
		if err != nil {
			return err
		}
		stored, err := cat.Store(ctx, chunks, false)
		if err != nil {
			return err
		}
		indexed, err := index.Index(ctx, indexing.Request{DocumentID: stored.DocumentID})
		if err != nil {
			return err
		}
		fmt.Printf("%s: %s %d chunks\n", f.product, indexed.Outcome, indexed.Total)
	}
	// A clearly synthetic two-page PDF provides conflicting sources for the SAME
	// fictional contract, without changing either real regression fixture.
	file := filepath.Join(outdir, "synthetic-conflict.pdf")
	// testpdf.Build accepts ASCII page strings; its separate Chinese CMap is fixed.
	if err = os.WriteFile(file, testpdf.Build("Synthetic policy A: The waiting period is 30 days.", "Synthetic policy A: The waiting period is 60 days."), 0600); err != nil {
		return err
	}
	doc, _, err := (ingest.Service{Parser: parser.PDF{}}).Run(ingest.Request{File: file, OutputDir: filepath.Join(outdir, "imports"), Metadata: document.DocumentMetadata{ProductID: "m6-synthetic-conflict-ascii", Version: "synthetic-v1", Name: "合成冲突测试资料（非真实保单）"}})
	if err != nil {
		return err
	}
	chunks, err := (chunker.ClauseAware{}).Split(doc)
	if err != nil {
		return err
	}
	stored, err := cat.Store(ctx, chunks, false)
	if err != nil {
		return err
	}
	if _, err = index.Index(ctx, indexing.Request{DocumentID: stored.DocumentID}); err != nil {
		return err
	}
	raw, err := os.ReadFile("testdata/m6/cases.json")
	if err != nil {
		return err
	}
	var cases []smokeCase
	if err = json.Unmarshal(raw, &cases); err != nil {
		return err
	}
	// Check manually predetermined evidence against original extracted text BEFORE
	// any generation calls. A missing anchor is a broken fixture, not a model miss.
	for _, c := range cases {
		for _, e := range c.Evidence {
			var found bool
			if err = db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM document_chunks c JOIN documents d ON d.id=c.document_id WHERE d.product_id=$1 AND c.page_number=$2 AND strpos(c.text,$3)>0)`, c.Product, e.Page, e.Contains).Scan(&found); err != nil || !found {
				return fmt.Errorf("预期原文不存在：%s / %d / %s", c.Name, e.Page, e.Contains)
			}
		}
	}
	search := retrieval.Service{Repository: postgres.SearchRepository{DB: db}, Embedder: embedder, Profile: profile}
	service := rag.Service{Searcher: search, Generator: model, Runs: postgres.RunRepository{DB: db}, Config: cfg}
	handler := httpapi.New(service, search)
	results := []outcome{}
	passed := 0
	for _, c := range cases {
		body, _ := json.Marshal(rag.Request{ProductID: c.Product, Question: c.Question, TopK: 5, Mode: "hnsw"})
		req := httptest.NewRequest(http.MethodPost, "/api/rag/ask", bytes.NewReader(body)).WithContext(ctx)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		o := outcome{Case: c, HTTPStatus: w.Code, Response: json.RawMessage(w.Body.Bytes())}
		var result rag.Result
		if w.Code == 200 && json.Unmarshal(w.Body.Bytes(), &result) == nil {
			run, err := service.GetRun(ctx, result.RunID)
			if err != nil {
				return err
			}
			o.Run = &run
			o.Passed = result.Reason == c.Reason && result.Abstained == (c.Reason != "")
			for _, text := range c.AnswerContains {
				if !strings.Contains(result.Answer, text) {
					o.Passed = false
				}
			}
			for _, e := range c.Evidence {
				found := false
				for _, citation := range result.Citations {
					if citation.Source.PageNumber == e.Page && strings.Contains(citation.Source.Text, e.Contains) {
						found = true
					}
				}
				if !found {
					o.Passed = false
				}
			}
		} else {
			var failure struct {
				Error rag.Error `json:"error"`
			}
			if json.Unmarshal(w.Body.Bytes(), &failure) == nil && failure.Error.RunID != "" {
				run, err := service.GetRun(ctx, failure.Error.RunID)
				if err == nil {
					o.Run = &run
				}
			}
		}
		if o.Passed {
			passed++
		}
		results = append(results, o)
		if err = writeResults(filepath.Join(outdir, "results.json"), results); err != nil {
			return err
		}
		fmt.Printf("%s: HTTP %d pass=%v\n", c.Name, o.HTTPStatus, o.Passed)
	}
	fmt.Printf("M6 smoke: %d/%d（仍需逐条人工核对结论及原文）\n", passed, len(cases))
	if passed != len(cases) {
		return errors.New("部分真实联调样本未达预期，请检查 var/m6-smoke/results.json")
	}
	return nil
}
func writeResults(path string, results []outcome) error {
	raw, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0600)
}
