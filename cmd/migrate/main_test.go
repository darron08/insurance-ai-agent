package main

import (
	"bytes"
	"strings"
	"testing"
)

func noEnvironment(string) string { return "" }

func TestRunValidation(t *testing.T) {
	tests := []struct {
		name string
		args []string
		code int
		want string
	}{
		{"help", []string{"--help"}, 0, "Usage"},
		{"position", []string{"up"}, 2, "位置参数"},
		{"unknown action", []string{"--action", "future"}, 2, "只支持"},
		{"down confirmation", []string{"--action", "down"}, 2, "--confirm-down"},
		{"missing database", nil, 1, "DATABASE_URL"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(tc.args, &stdout, &stderr, noEnvironment)
			combined := stdout.String() + stderr.String()
			if code != tc.code || !strings.Contains(combined, tc.want) {
				t.Fatalf("code = %d, output = %q", code, combined)
			}
		})
	}
}
