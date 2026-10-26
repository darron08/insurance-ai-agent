package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunValidation(t *testing.T) {
	const id = "660b909b-7a24-468e-9cea-bd6296e0e550"
	for _, tc := range []struct {
		name string
		args []string
		env  map[string]string
		code int
		want string
	}{
		{"help", []string{"--help"}, nil, 0, "Usage"},
		{"position", []string{id}, nil, 2, "位置参数"},
		{"unknown", []string{"--unknown"}, nil, 2, "flag"},
		{"missing id", nil, nil, 1, "UUID"},
		{"invalid id", []string{"--document-id", "bad"}, nil, 1, "UUID"},
		{"batch", []string{"--document-id", id, "--batch-size", "0"}, nil, 1, "batch-size"},
		{"missing remote key", []string{"--document-id", id}, map[string]string{"EMBEDDING_BASE_URL": "https://embedding.example/v1"}, 1, "EMBEDDING_API_KEY"},
		{"bad base", []string{"--document-id", id}, map[string]string{"EMBEDDING_BASE_URL": "https://user:secret@example.com"}, 1, "EMBEDDING_BASE_URL"},
		{"missing db", []string{"--document-id", id}, map[string]string{"EMBEDDING_API_KEY": "secret"}, 1, "DATABASE_URL"},
		{"db credentials", []string{"--document-id", id}, map[string]string{"EMBEDDING_API_KEY": "secret", "DATABASE_URL": "postgres://user:secret@127.0.0.1:1/missing?sslmode=disable"}, 1, "索引失败"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, errout bytes.Buffer
			code := run(tc.args, &out, &errout, func(key string) string { return tc.env[key] })
			output := out.String() + errout.String()
			if code != tc.code || !strings.Contains(output, tc.want) || strings.Contains(output, "secret") {
				t.Fatalf("code=%d output=%q", code, output)
			}
		})
	}
}
