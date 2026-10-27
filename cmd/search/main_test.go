package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"insurance_agent_demo/internal/retrieval"
)

func TestRunValidation(t *testing.T) {
	for _, tc := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"--help"}, 0, "Usage"},
		{[]string{"--unknown"}, 2, "flag"},
		{[]string{"question"}, 2, "位置参数"},
		{nil, 1, "product-id"},
		{[]string{"--product-id", "demo"}, 1, "query"},
		{[]string{"--product-id", "demo", "--query", "问题", "--top-k", "0"}, 1, "top-k"},
		{[]string{"--product-id", "demo", "--query", "问题", "--top-k", "101"}, 1, "top-k"},
		{[]string{"--product-id", "demo", "--query", "问题", "--mode", "bad"}, 1, "mode"},
		{[]string{"--product-id", "demo", "--query", "问题", "--mode="}, 1, "mode"},
		{[]string{"--product-id", "demo", "--query", "问题", "--version", " v1"}, 1, "version"},
	} {
		var out, stderr bytes.Buffer
		code := runWithSearch(tc.args, &out, &stderr, func(string) string { return "" }, func(context.Context, retrieval.Request, func(string) string) (retrieval.Result, error) {
			t.Fatal("invalid arguments reached search")
			return retrieval.Result{}, nil
		})
		if code != tc.code || !strings.Contains(stderr.String(), tc.want) || out.Len() != 0 {
			t.Fatalf("args=%q code=%d stdout=%s stderr=%s", tc.args, code, &out, &stderr)
		}
	}
}

func TestVersionPresenceAndJSON(t *testing.T) {
	for _, suffix := range [][]string{nil, {"--version="}, {"--version", "v1"}} {
		var out, stderr bytes.Buffer
		args := append([]string{"--product-id", "demo", "--query", " 等待期？ "}, suffix...)
		code := runWithSearch(args, &out, &stderr, func(string) string { return "" }, func(_ context.Context, req retrieval.Request, _ func(string) string) (retrieval.Result, error) {
			if (req.Version == nil) != (suffix == nil) || req.Query != "等待期？" {
				t.Fatalf("request=%+v", req)
			}
			if suffix != nil && len(suffix) == 1 && *req.Version != "" {
				t.Fatal("empty version lost")
			}
			return retrieval.Result{Query: req.Query, ProductID: req.ProductID, Hits: []retrieval.Hit{}}, nil
		})
		var result retrieval.Result
		if code != 0 || stderr.Len() != 0 || json.Unmarshal(out.Bytes(), &result) != nil || result.Query != "等待期？" || !strings.Contains(out.String(), `"hits": []`) {
			t.Fatalf("code=%d out=%s err=%s", code, &out, &stderr)
		}
	}
	var out, stderr bytes.Buffer
	if code := runWithSearch([]string{"--product-id", "demo", "--query", "问题"}, &out, &stderr, nil, func(context.Context, retrieval.Request, func(string) string) (retrieval.Result, error) {
		return retrieval.Result{}, errors.New("failed")
	}); code != 1 || out.Len() != 0 {
		t.Fatalf("code=%d stdout=%s", code, &out)
	}
}

func TestConfigurationErrorsAreSafe(t *testing.T) {
	for _, env := range []map[string]string{
		{"EMBEDDING_BASE_URL": "https://user:secret@example.com"},
		{"EMBEDDING_BASE_URL": "https://embedding.example/v1"},
		{},
		{"DATABASE_URL": "postgres://user:secret@127.0.0.1:1/missing?sslmode=disable"},
	} {
		var out, stderr bytes.Buffer
		if code := run([]string{"--product-id", "demo", "--query", "问题"}, &out, &stderr, func(k string) string { return env[k] }); code != 1 || out.Len() != 0 || strings.Contains(stderr.String(), "secret") {
			t.Fatalf("code=%d out=%s err=%s", code, &out, &stderr)
		}
	}
}
