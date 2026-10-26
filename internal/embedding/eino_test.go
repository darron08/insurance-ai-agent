package embedding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"insurance_agent_demo/internal/indexing"
)

func row(index int, value float64) map[string]any {
	v := make([]float64, indexing.Dimensions)
	v[0] = value
	return map[string]any{"index": index, "embedding": v, "object": "embedding"}
}
func newTestEmbedder(t *testing.T, handler http.HandlerFunc) *Embedder {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	p, err := indexing.NewProfile(server.URL+"/v1", "")
	if err != nil {
		t.Fatal(err)
	}
	e, err := New(p, "secret-key")
	if err != nil {
		t.Fatal(err)
	}
	e.wait = func(context.Context, time.Duration) error { return nil }
	return e
}
func success(w http.ResponseWriter, data ...map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "model": indexing.DefaultModel})
}

func TestEinoRequestAndResponseOrder(t *testing.T) {
	e := newTestEmbedder(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer secret-key" {
			t.Errorf("unexpected endpoint or authorization")
		}
		var body struct {
			Input      []string `json:"input"`
			Model      string   `json:"model"`
			Dimensions int      `json:"dimensions"`
			Encoding   string   `json:"encoding_format"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body.Input) != 2 || body.Input[0] != "原文甲" || body.Input[1] != "原文乙" || body.Model != indexing.DefaultModel || body.Dimensions != indexing.Dimensions || body.Encoding != "float" {
			t.Errorf("request=%+v", body)
		}
		success(w, row(1, 2), row(0, 1))
	})
	v, err := e.Embed(context.Background(), []string{"原文甲", "原文乙"})
	if err != nil || len(v) != 2 || v[0][0] != 1 || v[1][0] != 2 {
		t.Fatalf("wrong order, err=%v", err)
	}
}

func TestLocalServiceDoesNotRequireAPIKey(t *testing.T) {
	p, err := indexing.NewProfile("http://localhost:8000/v1", "")
	if err != nil {
		t.Fatal(err)
	}
	e, err := New(p, "")
	if err != nil || e.apiKey != "local" {
		t.Fatalf("embedder=%+v err=%v", e, err)
	}

	remote, _ := indexing.NewProfile("https://embedding.example/v1", "")
	if _, err := New(remote, ""); err == nil || !strings.Contains(err.Error(), "EMBEDDING_API_KEY") {
		t.Fatalf("remote service accepted an empty key: %v", err)
	}
}

func TestInvalidResponseNotRetried(t *testing.T) {
	for _, tc := range []struct {
		name string
		data func() any
	}{
		{"count", func() any { return []any{row(0, 1)} }},
		{"duplicate", func() any { return []any{row(0, 1), row(0, 2)} }},
		{"out of range", func() any { return []any{row(0, 1), row(2, 2)} }},
		{"negative index", func() any { return []any{row(-1, 1), row(1, 2)} }},
		{"missing index", func() any { x := row(0, 1); delete(x, "index"); return []any{x, row(1, 2)} }},
		{"dimension", func() any { x := row(0, 1); x["embedding"] = []float64{1}; return []any{x, row(1, 2)} }},
		{"zero", func() any { return []any{row(0, 0), row(1, 2)} }},
		{"overflow", func() any { return []any{row(0, math.MaxFloat64), row(1, 2)} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			e := newTestEmbedder(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				_ = json.NewEncoder(w).Encode(map[string]any{"data": tc.data()})
			})
			_, err := e.Embed(context.Background(), []string{"甲", "乙"})
			if err == nil || calls.Load() != 1 {
				t.Fatalf("calls=%d err=%v", calls.Load(), err)
			}
		})
	}
}

func TestRetryStatusAndRedaction(t *testing.T) {
	for _, status := range []int{408, 429, 500, 503, 400, 401, 403, 302} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			e := newTestEmbedder(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(status)
				fmt.Fprint(w, `{"error":{"message":"secret-key and confidential text"}}`)
			})
			_, err := e.Embed(context.Background(), []string{"原文"})
			want := int32(1)
			if status == 408 || status == 429 || status >= 500 {
				want = 4
			}
			if err == nil || calls.Load() != want || strings.Contains(err.Error(), "secret-key") || strings.Contains(err.Error(), "confidential") {
				t.Fatalf("calls=%d err=%v", calls.Load(), err)
			}
		})
	}
}

func TestRetryAfterThenSuccessAndCancellation(t *testing.T) {
	var calls atomic.Int32
	e := newTestEmbedder(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "9999")
			w.WriteHeader(429)
			return
		}
		success(w, row(0, 1))
	})
	var delay time.Duration
	e.wait = func(_ context.Context, d time.Duration) error { delay = d; return nil }
	if _, err := e.Embed(context.Background(), []string{"原文"}); err != nil || calls.Load() != 2 || delay != maxRetryAfter {
		t.Fatalf("calls=%d delay=%v err=%v", calls.Load(), delay, err)
	}
	calls.Store(0)
	ctx, cancel := context.WithCancel(context.Background())
	e.wait = func(ctx context.Context, _ time.Duration) error { cancel(); return ctx.Err() }
	if _, err := e.Embed(ctx, []string{"原文"}); !errors.Is(err, context.Canceled) || calls.Load() != 1 {
		t.Fatalf("calls=%d err=%v", calls.Load(), err)
	}
	if _, err := e.Embed(ctx, []string{"原文"}); !errors.Is(err, context.Canceled) || calls.Load() != 1 {
		t.Fatalf("canceled request sent to server: %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTimeoutAndTransientNetwork(t *testing.T) {
	p, _ := indexing.NewProfile("", "")
	e, _ := New(p, "secret-key")
	e.wait = func(context.Context, time.Duration) error { return nil }
	calls := 0
	e.transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}
	})
	if _, err := e.Embed(context.Background(), []string{"原文"}); err == nil || calls != 4 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	calls = 0
	e.timeout = 5 * time.Millisecond
	e.transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	if _, err := e.Embed(context.Background(), []string{"原文"}); err == nil || calls != 4 {
		t.Fatalf("timeout calls=%d err=%v", calls, err)
	}
}

func TestRetryAfterBoundsAndContextWait(t *testing.T) {
	for _, value := range []string{"-1", "bogus", "NaN", "-Inf"} {
		if retryAfter(value) != 0 {
			t.Fatalf("accepted %q", value)
		}
	}
	if retryAfter("+Inf") != maxRetryAfter {
		t.Fatal("unbounded retry-after")
	}
	if retryAfter(time.Now().Add(5*time.Minute).UTC().Format(http.TimeFormat)) != maxRetryAfter {
		t.Fatal("date retry-after not bounded")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitContext(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
