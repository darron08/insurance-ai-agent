// Package generation confines Eino and provider details to a model adapter.
package generation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"time"

	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/schema"

	"insurance_agent_demo/internal/rag"
)

const maxResponseBytes = 1 << 20

type Generator struct {
	config    rag.Config
	apiKey    string
	transport http.RoundTripper
	timeout   time.Duration
	wait      func(context.Context, time.Duration) error
}

func New(config rag.Config, apiKey string) (*Generator, error) {
	normalized, err := rag.NewConfig(config.BaseURL, config.Model)
	if err != nil || normalized != config {
		return nil, errors.New("生成模型配置无效")
	}
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, errors.New("缺少环境变量 LLM_API_KEY")
	}
	return &Generator{config: config, apiKey: apiKey, transport: http.DefaultTransport, timeout: 45 * time.Second, wait: waitContext}, nil
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

func (g *Generator) Generate(ctx context.Context, messages []rag.Message) (rag.Generation, error) {
	for attempt := 0; attempt < 2; attempt++ {
		if err := ctx.Err(); err != nil {
			return rag.Generation{}, err
		}
		result, err := g.once(ctx, messages)
		if err == nil {
			return result, nil
		}
		if ctx.Err() != nil {
			return rag.Generation{}, ctx.Err()
		}
		var status *httpError
		var network net.Error
		retry := errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) || (errors.As(err, &network) && (network.Timeout() || network.Temporary()))
		delay := time.Second + time.Duration(rand.Int64N(int64(500*time.Millisecond)))
		if errors.As(err, &status) {
			retry = status.status == 408 || status.status == 429 || status.status >= 500
			if status.after > delay {
				delay = status.after
			}
		}
		if !retry || attempt == 1 {
			return rag.Generation{}, errors.New("生成模型请求失败，请检查服务或配置")
		}
		if err := g.wait(ctx, delay); err != nil {
			return rag.Generation{}, err
		}
	}
	panic("unreachable")
}

func (g *Generator) once(parent context.Context, messages []rag.Message) (rag.Generation, error) {
	ctx, cancel := context.WithTimeout(parent, g.timeout)
	defer cancel()
	// Metadata belongs to this one call; concurrent requests never share it.
	transport := &captureTransport{next: g.transport}
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	maxTokens, temperature := g.config.MaxTokens, g.config.Temperature
	model, err := einoopenai.NewChatModel(ctx, &einoopenai.ChatModelConfig{
		APIKey: g.apiKey, BaseURL: g.config.BaseURL, Model: g.config.Model,
		HTTPClient: client, MaxTokens: &maxTokens, Temperature: &temperature,
		ResponseFormat: &einoopenai.ChatCompletionResponseFormat{Type: einoopenai.ChatCompletionResponseFormatTypeJSONObject},
		ExtraFields:    map[string]any{"thinking": map[string]string{"type": "disabled"}},
	})
	if err != nil {
		return rag.Generation{}, errors.New("初始化生成模型失败")
	}
	in := make([]*schema.Message, len(messages))
	for i, message := range messages {
		in[i] = &schema.Message{Role: schema.RoleType(message.Role), Content: message.Content}
	}
	response, err := model.Generate(ctx, in)
	if err != nil {
		return rag.Generation{}, err
	}
	if response == nil || response.ResponseMeta == nil || len(response.ToolCalls) != 0 {
		return rag.Generation{}, errors.New("生成响应无效")
	}
	return rag.Generation{Content: response.Content, Model: transport.model, FinishReason: response.ResponseMeta.FinishReason, Usage: transport.usage}, nil
}

type httpError struct {
	status int
	after  time.Duration
}

func (e *httpError) Error() string { return "生成服务 HTTP 错误" }

func retryAfter(value string) time.Duration {
	var delay time.Duration
	if seconds, err := strconv.ParseFloat(value, 64); err == nil {
		if seconds >= 60 {
			return time.Minute
		}
		if seconds > 0 {
			delay = time.Duration(seconds * float64(time.Second))
		}
	} else if date, err := http.ParseTime(value); err == nil {
		delay = time.Until(date)
	}
	if delay < 0 {
		return 0
	}
	if delay > time.Minute {
		return time.Minute
	}
	return delay
}

// Eino's SDK supplies zero-valued usage even when the server omits usage. Capture
// its presence and returned model at the HTTP boundary, without retaining headers
// or reasoning. Validate the bounded envelope before allowing SDK decoding.
type captureTransport struct {
	next  http.RoundTripper
	model string
	usage *rag.Usage
}

func (t *captureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
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
	var envelope struct {
		Model   string     `json:"model"`
		Usage   *rag.Usage `json:"usage"`
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Role      string            `json:"role"`
				Content   *string           `json:"content"`
				ToolCalls []json.RawMessage `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if len(raw) > maxResponseBytes || json.Unmarshal(raw, &envelope) != nil || len(envelope.Choices) != 1 || envelope.Model == "" || envelope.Choices[0].Message.Role != "assistant" || len(envelope.Choices[0].Message.ToolCalls) > 0 {
		return nil, errors.New("生成响应格式无效或响应过大")
	}
	if envelope.Usage != nil && (envelope.Usage.PromptTokens < 0 || envelope.Usage.CompletionTokens < 0 || envelope.Usage.TotalTokens < 0) {
		return nil, errors.New("生成用量无效")
	}
	t.model, t.usage = envelope.Model, envelope.Usage
	response.Body, response.ContentLength = io.NopCloser(bytes.NewReader(raw)), int64(len(raw))
	response.Header.Del("Content-Length")
	return response, nil
}
