package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"insurance_agent_demo/internal/rag"
	"insurance_agent_demo/internal/retrieval"
)

type fakeSearch struct {
	request retrieval.Request
	err     error
	calls   int
}

func (s *fakeSearch) Search(_ context.Context, r retrieval.Request) (retrieval.Result, error) {
	s.request = r
	s.calls++
	return retrieval.Result{ProductID: r.ProductID, Version: "v1", Hits: []retrieval.Hit{{ChunkID: "c", ProductID: r.ProductID, DocumentVersion: "v1", Text: "等待期为30天。", PageNumber: 3}}}, s.err
}

type fakeModel struct{ content string }

func (m fakeModel) Generate(context.Context, []rag.Message) (rag.Generation, error) {
	return rag.Generation{Content: m.content, Model: "test-version", FinishReason: "stop"}, nil
}

type fakeRuns struct {
	run rag.Run
	err error
}

func (r *fakeRuns) Create(_ context.Context, run rag.Run) error { r.run = run; return r.err }
func (r *fakeRuns) Save(_ context.Context, run rag.Run) error   { r.run = run; return r.err }
func (r *fakeRuns) Get(_ context.Context, id string) (rag.Run, error) {
	if r.err != nil {
		return rag.Run{}, r.err
	}
	if id != r.run.ID {
		return rag.Run{}, rag.ErrRunNotFound
	}
	return r.run, nil
}

func testAPI() (http.Handler, *fakeSearch, *fakeRuns) {
	gin.SetMode(gin.TestMode)
	search, runs := &fakeSearch{}, &fakeRuns{}
	cfg, _ := rag.NewConfig("", "")
	model := fakeModel{content: `{"status":"answered","statements":[{"text":"等待期为30天。","evidence":[{"evidence_id":"E1","quote":"等待期为30天"}]}],"missing_information":[]}`}
	return New(rag.Service{Searcher: search, Generator: model, Runs: runs, Config: cfg}, search), search, runs
}
func request(handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func TestAskSearchVersionsAndRunDetails(t *testing.T) {
	for _, version := range []string{"", `,"version":null`, `,"version":""`, `,"version":"v1"`} {
		h, s, r := testAPI()
		w := request(h, "POST", "/api/rag/ask", `{"product_id":"p","question":"等待期？"`+version+`}`)
		var result rag.Result
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.RunID == "" || result.Version != "v1" || len(result.Citations) != 1 || result.Usage != nil || w.Header().Get("X-Request-ID") == "" {
			t.Fatal(w.Code, w.Body.String())
		}
		if version == `,"version":""` {
			if s.request.Version == nil || *s.request.Version != "" {
				t.Fatal("explicit empty lost")
			}
		} else if version == "" || version == `,"version":null` {
			if s.request.Version != nil {
				t.Fatal("omitted version changed")
			}
		}
		w = request(h, "GET", "/api/rag/runs/"+result.RunID, "")
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"validation":"passed"`) || r.run.Status != "answered" {
			t.Fatal(w.Body.String())
		}
	}
	h, s, _ := testAPI()
	w := request(h, "POST", "/api/rag/search", `{"product_id":"p","query":"等待期","top_k":100,"mode":"exact"}`)
	if w.Code != 200 || s.request.TopK != 100 || s.request.Mode != "exact" {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request(h, "GET", "/healthz", "")
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
}

func TestHTTPRejectsInvalidInputBeforeIO(t *testing.T) {
	for _, body := range []string{`{}`, `null`, `[]`, `{"product_id":"p","question":"x","extra":1}`, `{"product_id":"p","question":"x"} {}`, `{"product_id":"p","question":"x","top_k":0}`, `{"product_id":"p","question":"x","top_k":21}`, `{"product_id":"p","question":"x","mode":"hybrid"}`, `{"product_id":"p","question":"x","version":" v1"}`, `{"product_id":"p","question":"` + strings.Repeat("中", 2001) + `"}`, `{"product_id":"p","question":"` + strings.Repeat("a", 33<<10) + `"}`} {
		h, s, r := testAPI()
		w := request(h, "POST", "/api/rag/ask", body)
		if w.Code != 400 || s.calls != 0 || r.run.ID != "" {
			t.Fatal(w.Code, w.Body.String(), s.calls)
		}
	}
	h, s, _ := testAPI()
	req := httptest.NewRequest("POST", "/api/rag/ask", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 400 || s.calls != 0 {
		t.Fatal(w.Code)
	}
	for _, path := range []string{"/api/rag/runs/not-uuid", "/api/rag/runs/00000000-0000-0000-0000-000000000000"} {
		w = request(h, "GET", path, "")
		if w.Code != 400 && w.Code != 404 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}

func TestHTTPErrorMappingAndAbstention(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{&retrieval.VersionRequiredError{Versions: []string{"", "v1"}}, 409, "version_required"},
		{retrieval.ErrProfileMismatch, 409, "profile_mismatch"}, {retrieval.ErrNoReadyDocuments, 404, "no_ready_documents"},
		{retrieval.ErrEmbeddingUnavailable, 502, "embedding_failed"}, {context.DeadlineExceeded, 504, "timeout"}, {errors.New("private-sql-or-key"), 503, "storage_unavailable"},
	} {
		h, s, _ := testAPI()
		s.err = tc.err
		w := request(h, "POST", "/api/rag/ask", `{"product_id":"p","question":"x"}`)
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.code) || !strings.Contains(w.Body.String(), "run_id") || strings.Contains(w.Body.String(), "private-sql") {
			t.Fatal(w.Code, w.Body.String())
		}
		if tc.code == "version_required" && !strings.Contains(w.Body.String(), `"versions":["","v1"]`) {
			t.Fatal(w.Body.String())
		}
	}
	_, search, runs := testAPI()
	cfg, _ := rag.NewConfig("", "")
	model := fakeModel{content: `{"status":"insufficient_evidence","statements":[],"missing_information":["请提供免责条款"]}`}
	h := New(rag.Service{Searcher: search, Generator: model, Runs: runs, Config: cfg}, search)
	w := request(h, "POST", "/api/rag/ask", `{"product_id":"p","question":"等待期和免责？"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"abstained":true`) || !strings.Contains(w.Body.String(), "请提供免责条款") {
		t.Fatal(w.Body.String())
	}
	model.content = `{}`
	h = New(rag.Service{Searcher: search, Generator: model, Runs: runs, Config: cfg}, search)
	w = request(h, "POST", "/api/rag/ask", `{"product_id":"p","question":"x"}`)
	if w.Code != 502 || !strings.Contains(w.Body.String(), "invalid_generation") {
		t.Fatal(w.Code, w.Body.String())
	}
}
