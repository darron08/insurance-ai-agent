package main

import (
	"bytes"
	"context"
	"net/http"
	"testing"
)

func TestAPIFlagsAndMissingConfiguration(t *testing.T) {
	for _, tc := range []struct {
		args []string
		code int
	}{{[]string{"--help"}, 0}, {[]string{"--unknown"}, 2}, {[]string{"unexpected"}, 2}, {nil, 1}} {
		var out, err bytes.Buffer
		if code := run(tc.args, &out, &err, func(string) string { return "" }); code != tc.code {
			t.Fatal(code, err.String())
		}
	}
}

func TestServerCancellationAndListenFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := serve(ctx, "127.0.0.1:0", http.NotFoundHandler()); err != nil {
		t.Fatal(err)
	}
	if err := serve(context.Background(), "not-an-address", http.NotFoundHandler()); err == nil {
		t.Fatal("invalid listener succeeded")
	}
}
