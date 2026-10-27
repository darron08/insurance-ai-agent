package generation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"insurance_agent_demo/internal/rag"
)

func testGenerator(t *testing.T, handler http.HandlerFunc) *Generator {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	cfg, _ := rag.NewConfig(server.URL, "deepseek-flash")
	g, err := New(cfg, "test-secret-key")
	if err != nil {
		t.Fatal(err)
	}
	g.wait = func(context.Context, time.Duration) error { return nil }
	return g
}
func respond(w http.ResponseWriter, model string, usage bool) {
	w.Header().Set("Content-Type", "application/json")
	body := map[string]any{"model": model, "choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": "{}", "reasoning_content": "private-thought"}}}}
	if usage {
		body["usage"] = map[string]int{"prompt_tokens": 10, "completion_tokens": 20, "total_tokens": 30}
	}
	_ = json.NewEncoder(w).Encode(body)
}

func TestEinoGenerationContractAndMetadata(t *testing.T) {
	g := testGenerator(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer test-secret-key" {
			t.Error("wrong endpoint or credentials")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["model"] != "deepseek-flash" || body["max_tokens"] != float64(4096) || body["temperature"] != float64(0) || body["thinking"].(map[string]any)["type"] != "disabled" || body["response_format"].(map[string]any)["type"] != "json_object" || body["stream"] == true || body["tools"] != nil {
			t.Errorf("unexpected configuration: %+v", body)
		}
		respond(w, "actual-provider-version", true)
	})
	result, err := g.Generate(context.Background(), []rag.Message{{Role: "system", Content: "JSON"}, {Role: "user", Content: "问题"}})
	if err != nil || result.Model != "actual-provider-version" || result.FinishReason != "stop" || result.Usage == nil || result.Usage.TotalTokens != 30 {
		t.Fatal(result, err)
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), "private-thought") {
		t.Fatal("persisted reasoning")
	}
	g = testGenerator(t, func(w http.ResponseWriter, r *http.Request) { respond(w, "version", false) })
	result, err = g.Generate(context.Background(), nil)
	if err != nil || result.Usage != nil {
		t.Fatal("missing usage became zero", result, err)
	}
}

func TestRetryLimitsAndSafeErrors(t *testing.T) {
	for _, status := range []int{400, 401, 408, 429, 500, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			g := testGenerator(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Retry-After", "5")
				w.WriteHeader(status)
				fmt.Fprint(w, "test-secret-key provider-sensitive-body")
			})
			waited := time.Duration(0)
			g.wait = func(_ context.Context, d time.Duration) error { waited = d; return nil }
			_, err := g.Generate(context.Background(), nil)
			want := 2
			if status < 408 {
				want = 1
			}
			if err == nil || calls != want || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "provider-sensitive") {
				t.Fatal(calls, err)
			}
			if want == 2 && waited < 5*time.Second {
				t.Fatal("Retry-After ignored", waited)
			}
		})
	}
	calls := 0
	g := testGenerator(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(503)
			return
		}
		respond(w, "version", true)
	})
	if _, err := g.Generate(context.Background(), nil); err != nil || calls != 2 {
		t.Fatal(calls, err)
	}
	if retryAfter("999999999") != time.Minute || retryAfter("-1") != 0 || retryAfter(time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)) != 0 {
		t.Fatal("Retry-After bounds")
	}
}

func TestMalformedRedirectTimeoutAndCancel(t *testing.T) {
	for _, body := range []string{"not-json", `{"model":"v","choices":[]}`, `{"model":"v","choices":[{"message":{"role":"assistant","tool_calls":[{}]}}]}`, strings.Repeat("x", maxResponseBytes+1)} {
		calls := 0
		g := testGenerator(t, func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, body)
		})
		if _, err := g.Generate(context.Background(), nil); err == nil || calls != 1 {
			t.Fatal(calls, err)
		}
	}
	var forwarded atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded.Add(1) }))
	defer target.Close()
	g := testGenerator(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) })
	if _, err := g.Generate(context.Background(), nil); err == nil || forwarded.Load() != 0 {
		t.Fatal("redirect followed")
	}
	release := make(chan struct{})
	defer close(release)
	g = testGenerator(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	g.timeout = 10 * time.Millisecond
	if _, err := g.Generate(context.Background(), nil); err == nil {
		t.Fatal("timeout ignored")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := g.Generate(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	g = testGenerator(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(429) })
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	g.wait = func(ctx context.Context, _ time.Duration) error { cancel(); return ctx.Err() }
	if _, err := g.Generate(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestConcurrentMetadataIsolation(t *testing.T) {
	g := testGenerator(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []rag.Message `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		respond(w, body.Messages[0].Content, true)
	})
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("model-%d", i)
			result, err := g.Generate(context.Background(), []rag.Message{{Role: "user", Content: name}})
			if err != nil || result.Model != name {
				t.Errorf("metadata crossed requests: %+v %v", result, err)
			}
		}(i)
	}
	wg.Wait()
}
