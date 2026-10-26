package indexing

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
)

const testID = "660b909b-7a24-468e-9cea-bd6296e0e550"

type embedFunc func(context.Context, []string) ([][]float64, error)

func (f embedFunc) Embed(ctx context.Context, texts []string) ([][]float64, error) {
	return f(ctx, texts)
}

func testVectors(count int) [][]float64 {
	v := make([][]float64, count)
	for i := range v {
		v[i] = make([]float64, Dimensions)
		v[i][0] = 1
	}
	return v
}

type memoryRepository struct {
	session   *memorySession
	lockCalls int
}

func (r *memoryRepository) Lock(context.Context, string) (Session, error) {
	r.lockCalls++
	return r.session, nil
}

type memorySession struct {
	chunks          []Chunk
	done            map[string]bool
	status          string
	failedStage     string
	closed          int
	failErr         error
	completeErr     error
	cleanupCanceled bool
}

func (s *memorySession) Prepare(_ context.Context, _ Profile, reindex bool) (Progress, error) {
	if reindex {
		s.done = map[string]bool{}
		s.status = "chunked"
	}
	if s.status != "ready" {
		s.status = "embedding"
	}
	return Progress{Total: len(s.chunks), Completed: len(s.done), Status: s.status}, nil
}
func (s *memorySession) Pending(_ context.Context, limit int) ([]Chunk, error) {
	var chunks []Chunk
	for _, c := range s.chunks {
		if !s.done[c.ID] && len(chunks) < limit {
			chunks = append(chunks, c)
		}
	}
	return chunks, nil
}
func (s *memorySession) SaveBatch(_ context.Context, _ Profile, chunks []Chunk, _ [][]float64) error {
	for _, c := range chunks {
		s.done[c.ID] = true
	}
	return nil
}
func (s *memorySession) Complete(context.Context) error {
	if s.completeErr != nil {
		return s.completeErr
	}
	s.status = "ready"
	return nil
}
func (s *memorySession) Fail(ctx context.Context, stage, _ string) error {
	s.cleanupCanceled = ctx.Err() != nil
	s.status = "failed"
	s.failedStage = stage
	return s.failErr
}
func (s *memorySession) Close() error { s.closed++; return nil }

func TestIndexFailureResumeSkipAndReindex(t *testing.T) {
	p, _ := NewProfile("", "")
	session := &memorySession{chunks: []Chunk{{"1", "原文甲"}, {"2", "原文乙"}, {"3", "原文丙"}}, done: map[string]bool{}}
	repo := &memoryRepository{session: session}
	calls := 0
	service := Service{Repository: repo, Profile: p, Embedder: embedFunc(func(_ context.Context, texts []string) ([][]float64, error) {
		calls++
		if calls == 2 {
			return nil, errors.New("model unavailable")
		}
		if strings.Contains(texts[0], "metadata") {
			t.Fatal("unexpected modified input")
		}
		return testVectors(len(texts)), nil
	})}
	result, err := service.Index(context.Background(), Request{DocumentID: testID, BatchSize: 2})
	if err == nil || result.Processed != 2 || result.Status != "failed" || len(session.done) != 2 || session.failedStage != "embedding" || session.closed != 1 {
		t.Fatalf("result=%+v session=%+v err=%v", result, session, err)
	}
	service.Embedder = embedFunc(func(_ context.Context, texts []string) ([][]float64, error) {
		if len(texts) != 1 || texts[0] != "原文丙" {
			t.Fatalf("resume input=%v", texts)
		}
		return testVectors(1), nil
	})
	result, err = service.Index(context.Background(), Request{DocumentID: testID, BatchSize: 2})
	if err != nil || result.Processed != 1 || result.Reused != 2 || result.Status != "ready" {
		t.Fatalf("resume=%+v err=%v", result, err)
	}
	service.Embedder = embedFunc(func(context.Context, []string) ([][]float64, error) {
		t.Fatal("ready document called model")
		return nil, nil
	})
	result, err = service.Index(context.Background(), Request{DocumentID: testID})
	if err != nil || result.Outcome != "skipped" || result.Reused != 3 {
		t.Fatalf("skip=%+v err=%v", result, err)
	}
	service.Embedder = embedFunc(func(_ context.Context, texts []string) ([][]float64, error) { return testVectors(len(texts)), nil })
	result, err = service.Index(context.Background(), Request{DocumentID: testID, Reindex: true})
	if err != nil || result.Reused != 0 || result.Processed != 3 {
		t.Fatalf("reindex=%+v err=%v", result, err)
	}
}

func TestIndexCancellationCleanupAndFailureReporting(t *testing.T) {
	p, _ := NewProfile("", "")
	ctx, cancel := context.WithCancel(context.Background())
	session := &memorySession{chunks: []Chunk{{"1", "原文"}}, done: map[string]bool{}, failErr: errors.New("database unavailable")}
	service := Service{Repository: &memoryRepository{session: session}, Profile: p, Embedder: embedFunc(func(context.Context, []string) ([][]float64, error) { cancel(); return nil, context.Canceled })}
	_, err := service.Index(ctx, Request{DocumentID: testID})
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "无法保存失败状态") || session.cleanupCanceled || session.closed != 1 {
		t.Fatalf("cleanup=%+v err=%v", session, err)
	}
}

func TestInvalidVectorsNeverSaved(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func([][]float64) [][]float64
	}{
		{"count", func(v [][]float64) [][]float64 { return nil }},
		{"dimension", func(v [][]float64) [][]float64 { v[0] = v[0][:2]; return v }},
		{"zero", func(v [][]float64) [][]float64 { v[0][0] = 0; return v }},
		{"nan", func(v [][]float64) [][]float64 { v[0][0] = math.NaN(); return v }},
		{"infinity", func(v [][]float64) [][]float64 { v[0][0] = math.Inf(1); return v }},
		{"overflow", func(v [][]float64) [][]float64 { v[0][0] = math.MaxFloat64; return v }},
		{"underflow", func(v [][]float64) [][]float64 { v[0][0] = math.SmallestNonzeroFloat64; return v }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, _ := NewProfile("", "")
			session := &memorySession{chunks: []Chunk{{"1", "原文"}}, done: map[string]bool{}}
			service := Service{Repository: &memoryRepository{session: session}, Profile: p, Embedder: embedFunc(func(context.Context, []string) ([][]float64, error) { return tc.change(testVectors(1)), nil })}
			_, err := service.Index(context.Background(), Request{DocumentID: testID})
			if err == nil || len(session.done) != 0 || session.failedStage != "validate_embedding" {
				t.Fatalf("session=%+v err=%v", session, err)
			}
		})
	}
}

func TestProfileIdentityAndValidation(t *testing.T) {
	a, _ := NewProfile("https://EXAMPLE.com/v1/", " model ")
	b, _ := NewProfile("https://example.com/v1", "model")
	if a != b || a.Key() != b.Key() {
		t.Fatal("profile is not canonical")
	}
	c, _ := NewProfile("https://other.example/v1", "model")
	if a.Key() == c.Key() {
		t.Fatal("provider omitted from identity")
	}
	for _, url := range []string{"file:///tmp/api", "https://user:secret@example.com/v1", "https://example.com/v1?key=secret"} {
		if _, err := NewProfile(url, ""); err == nil {
			t.Fatalf("accepted %q", url)
		}
	}
	for _, id := range []string{"", "not-a-uuid", strings.Repeat("-", 36)} {
		if ValidateDocumentID(id) == nil {
			t.Fatalf("accepted UUID %q", id)
		}
	}
}
