package httpapi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"insurance_agent_demo/internal/rag"
)

func TestStaticBuildAndAPIBoundary(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := WebHandler(dir); err == nil {
		t.Fatal("missing index accepted")
	}
	if _, _, err := WebHandler(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing dir accepted")
	}
	for name, body := range map[string]string{"index.html": "<h1>问答页面</h1>", "main.js": "console.log('ok')"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("secret-data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	web, closeWeb, err := WebHandler(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer closeWeb()
	h := New(rag.Service{}, nil, Options{Web: web})
	for path, status := range map[string]int{"/": 200, "/main.js": 200, "/missing.js": 404, "/escape": 404, "/api/missing": 404, "/api": 404, "/healthz": 200} {
		w := request(h, "GET", path, "")
		if w.Code != status || strings.Contains(w.Body.String(), "secret-data") {
			t.Fatal(path, w.Code, w.Body.String())
		}
		if strings.HasPrefix(path, "/api") && !strings.Contains(w.Header().Get("Content-Type"), "application/json") {
			t.Fatal("API became HTML", path)
		}
	}
	if w := request(h, "POST", "/", ""); w.Code != 404 {
		t.Fatal(w.Code)
	}
}
