package rag

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"insurance_agent_demo/internal/retrieval"
)

func NewConfig(baseURL, model string) (Config, error) {
	baseURL, model = strings.TrimSpace(baseURL), strings.TrimSpace(model)
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	if model == "" {
		model = DefaultModel
	}
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return Config{}, errors.New("LLM_BASE_URL 必须是不含凭据、查询参数或片段的 HTTP/HTTPS 地址")
	}
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimRight(u.Path, "/")
	return Config{BaseURL: u.String(), Model: model, PromptVersion: PromptVersion, ContextBudget: ContextBudget, Temperature: 0, MaxTokens: 4096, Thinking: "disabled"}, nil
}

func NormalizeRequest(req Request) (Request, error) {
	search, err := retrieval.NormalizeRequest(retrieval.Request{ProductID: req.ProductID, Query: req.Question, Version: req.Version, TopK: req.TopK, Mode: req.Mode})
	if err != nil {
		return req, &Error{Code: "invalid_request", Message: err.Error(), Stage: "validation", Cause: err}
	}
	if utf8.RuneCountInString(search.Query) > 2000 || search.TopK > 20 {
		return req, &Error{Code: "invalid_request", Message: "question 最长 2000 个字符，top_k 必须在 1 到 20 之间", Stage: "validation"}
	}
	req.ProductID, req.Question, req.Version, req.TopK, req.Mode = search.ProductID, search.Query, search.Version, search.TopK, search.Mode
	return req, nil
}

// ValidateDecision checks syntax and provenance, not semantic entailment.
var evidenceMarker = regexp.MustCompile(`\bE[0-9]+\b|\[[0-9]+\]`)

func ValidateDecision(g Generation, evidence []Evidence) (Decision, error) {
	invalid := func() (Decision, error) { return Decision{}, errors.New("生成结果结构或引用校验失败") }
	if g.FinishReason != "stop" || strings.TrimSpace(g.Content) == "" {
		return invalid()
	}
	var d Decision
	decoder := json.NewDecoder(strings.NewReader(g.Content))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&d) != nil || decoder.Decode(new(any)) != io.EOF || d.Statements == nil || d.MissingInformation == nil {
		return invalid()
	}
	switch d.Status {
	case Answered:
		if len(d.Statements) == 0 || len(d.MissingInformation) != 0 {
			return invalid()
		}
	case Conflict:
		if len(d.Statements) < 2 || len(d.MissingInformation) != 0 {
			return invalid()
		}
	case Insufficient, Clarification:
		if len(d.Statements) != 0 || len(d.MissingInformation) == 0 {
			return invalid()
		}
	case OutOfScope, FormalDecision:
		if len(d.Statements) != 0 || len(d.MissingInformation) != 0 {
			return invalid()
		}
	default:
		return invalid()
	}
	if len(d.Statements) > 12 || len(d.MissingInformation) > 10 {
		return invalid()
	}
	for _, missing := range d.MissingInformation {
		if !(strings.HasPrefix(missing, "请提供") || strings.HasPrefix(missing, "请补充") || strings.HasPrefix(missing, "请明确")) || utf8.RuneCountInString(missing) > 500 || evidenceMarker.MatchString(missing) {
			return invalid()
		}
	}
	byID := map[string]Evidence{}
	for _, e := range evidence {
		byID[e.ID] = e
	}
	usedChunks := map[string]bool{}
	for _, statement := range d.Statements {
		if strings.TrimSpace(statement.Text) == "" || evidenceMarker.MatchString(statement.Text) || len(statement.Evidence) == 0 || len(statement.Evidence) > 20 {
			return invalid()
		}
		for _, ref := range statement.Evidence {
			e, ok := byID[ref.EvidenceID]
			if !ok || strings.TrimSpace(ref.Quote) == "" || !strings.Contains(e.Hit.Text, ref.Quote) {
				return invalid()
			}
			usedChunks[e.Hit.ChunkID] = true
		}
	}
	if d.Status == Conflict && len(usedChunks) < 2 {
		return invalid()
	}
	return d, nil
}

var refusalText = map[string]string{
	Insufficient:   "根据现有资料无法确认。",
	Clarification:  "根据现有资料无法确认，请补充必要信息。",
	Conflict:       "现有资料存在冲突，根据现有资料无法确认，请联系正式渠道核实。",
	OutOfScope:     "该问题超出本次保险资料解释范围，根据现有资料无法确认。",
	FormalDecision: "本系统不能作出正式核保、核赔、保险资格或赔付决定，请联系保险公司正式渠道。",
}

func render(d Decision, evidence []Evidence) (string, []Citation) {
	byID := map[string]Evidence{}
	for _, e := range evidence {
		byID[e.ID] = e
	}
	citations := []Citation{}
	ids := map[Reference]int{}
	parts := []string{}
	if d.Status != Answered {
		parts = append(parts, refusalText[d.Status])
	}
	for _, statement := range d.Statements {
		line := statement.Text
		seen := map[int]bool{}
		for _, ref := range statement.Evidence {
			id, ok := ids[ref]
			if !ok {
				id = len(citations) + 1
				ids[ref] = id
				citations = append(citations, Citation{ID: id, EvidenceID: ref.EvidenceID, Quote: ref.Quote, Source: byID[ref.EvidenceID].Hit})
			}
			if !seen[id] {
				line += fmt.Sprintf(" [%d]", id)
				seen[id] = true
			}
		}
		parts = append(parts, line)
	}
	if len(d.MissingInformation) > 0 {
		parts = append(parts, "需要补充或核实："+strings.Join(d.MissingInformation, "；"))
	}
	return strings.Join(parts, "\n\n"), citations
}
