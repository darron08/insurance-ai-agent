// Package embedding adapts Eino to the application-owned Embedder interface.
package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"

	einoopenai "github.com/cloudwego/eino-ext/components/embedding/openai"

	"insurance_agent_demo/internal/indexing"
)

const maxResponseBytes = 64 << 20
const maxRetryAfter = 60 * time.Second

type Embedder struct {
	profile   indexing.Profile
	apiKey    string
	transport http.RoundTripper
	timeout   time.Duration
	wait      func(context.Context, time.Duration) error
}

func New(profile indexing.Profile, apiKey string) (*Embedder, error) {
	p, err := indexing.NewProfile(profile.BaseURL, profile.Model)
	if err != nil || profile != p {
		return nil, errors.New("Embedding 模型配置无效")
	}
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" && isLoopback(profile.BaseURL) {
		// Eino/OpenAI 客户端总会构造 Authorization 头。本地 vLLM 默认不校验
		// 密钥，因此使用不落库的占位值，让首次本地运行无需伪造配置。
		apiKey = "local"
	}
	if apiKey == "" {
		return nil, errors.New("缺少环境变量 EMBEDDING_API_KEY")
	}
	return &Embedder{profile: p, apiKey: apiKey, transport: http.DefaultTransport, timeout: 60 * time.Second, wait: waitContext}, nil
}

func isLoopback(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func waitContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (e *Embedder) Embed(ctx context.Context, texts []string) ([][]float64, error) {
	if len(texts) == 0 || len(texts) > 2048 {
		return nil, errors.New("Embedding 输入数量必须在 1 到 2048 之间")
	}
	for _, text := range texts {
		if strings.TrimSpace(text) == "" {
			return nil, errors.New("Embedding 输入不能是空文本")
		}
	}
	for attempt := 0; attempt <= 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		vectors, err := e.once(ctx, texts)
		if err == nil {
			return vectors, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		retry, after, safe := retryInfo(err)
		if !retry || attempt == 3 {
			return nil, fmt.Errorf("%s（已尝试 %d 次）", safe, attempt+1)
		}
		base := time.Second * time.Duration(1<<attempt)
		delay := base + time.Duration(rand.Int64N(int64(base/2)))
		if after > delay {
			delay = after
		}
		if err := e.wait(ctx, delay); err != nil {
			return nil, err
		}
	}
	panic("unreachable")
}

func (e *Embedder) once(ctx context.Context, texts []string) ([][]float64, error) {
	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()
	format := einoopenai.EmbeddingEncodingFormatFloat
	dimensions := e.profile.Dimensions
	client := &http.Client{
		Transport: orderedTransport{next: e.transport, count: len(texts)},
		// Never forward credentials or input texts to a redirected endpoint.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	model, err := einoopenai.NewEmbedder(ctx, &einoopenai.EmbeddingConfig{
		APIKey: e.apiKey, BaseURL: e.profile.BaseURL, Model: e.profile.Model,
		Dimensions: &dimensions, EncodingFormat: &format, HTTPClient: client,
	})
	if err != nil {
		return nil, errors.New("初始化 Eino Embedding 失败")
	}
	return model.EmbedStrings(ctx, texts)
}

type httpError struct {
	status int
	after  time.Duration
}

func (e *httpError) Error() string { return fmt.Sprintf("Embedding API HTTP %d", e.status) }

type protocolError struct{ reason string }

func (e *protocolError) Error() string { return "Embedding 响应无效: " + e.reason }

func retryInfo(err error) (bool, time.Duration, string) {
	var status *httpError
	if errors.As(err, &status) {
		return status.status == 408 || status.status == 429 || status.status >= 500, status.after, status.Error()
	}
	var protocol *protocolError
	if errors.As(err, &protocol) {
		return false, 0, protocol.Error()
	}
	var network net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &network) && (network.Timeout() || network.Temporary())) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return true, 0, "Embedding 网络请求失败或超时"
	}
	// SDK and URL errors can contain credentials, URLs or response bodies.
	return false, 0, "Embedding 请求失败，请检查服务地址和配置"
}

func retryAfter(value string) time.Duration {
	var delay time.Duration
	if n, err := strconv.ParseFloat(value, 64); err == nil {
		if n >= maxRetryAfter.Seconds() {
			return maxRetryAfter
		}
		if n > 0 {
			delay = time.Duration(n * float64(time.Second))
		}
	} else if date, err := http.ParseTime(value); err == nil {
		delay = time.Until(date)
	}
	if delay < 0 {
		return 0
	}
	if delay > maxRetryAfter {
		return maxRetryAfter
	}
	return delay
}

// Eino's adapter returns vectors in array order. Validate the index permutation
// and reorder the response before Eino decodes it, retaining its model adapter.
type orderedTransport struct {
	next  http.RoundTripper
	count int
}

func (t orderedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	response, err := t.next.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, &httpError{status: response.StatusCode, after: retryAfter(response.Header.Get("Retry-After"))}
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxResponseBytes {
		return nil, &protocolError{"响应过大"}
	}
	var envelope map[string]json.RawMessage
	var data []json.RawMessage
	if json.Unmarshal(raw, &envelope) != nil || json.Unmarshal(envelope["data"], &data) != nil || len(data) != t.count {
		return nil, &protocolError{"返回数量与输入不一致或格式错误"}
	}
	ordered := make([]json.RawMessage, t.count)
	vectors := make([][]float64, t.count)
	for _, item := range data {
		var row struct {
			Index     *int      `json:"index"`
			Embedding []float64 `json:"embedding"`
		}
		if json.Unmarshal(item, &row) != nil || row.Index == nil || *row.Index < 0 || *row.Index >= t.count || ordered[*row.Index] != nil {
			return nil, &protocolError{"index 缺失、重复或越界"}
		}
		ordered[*row.Index], vectors[*row.Index] = item, row.Embedding
	}
	if err := indexing.ValidateVectors(vectors, t.count); err != nil {
		return nil, &protocolError{err.Error()}
	}
	envelope["data"], _ = json.Marshal(ordered)
	raw, err = json.Marshal(envelope)
	if err != nil {
		return nil, &protocolError{"无法规范化响应"}
	}
	response.Body = io.NopCloser(bytes.NewReader(raw))
	response.ContentLength = int64(len(raw))
	response.Header.Del("Content-Length")
	return response, nil
}
