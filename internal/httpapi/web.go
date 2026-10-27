package httpapi

import (
	"fmt"
	"io/fs"
	"net/http"
	"os"
)

// WebHandler serves a build directory, never a repository root. os.Root prevents
// both path traversal and symlinks from escaping the selected directory.
func WebHandler(directory string) (http.Handler, func() error, error) {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, nil, fmt.Errorf("无法打开前端构建目录")
	}
	info, err := fs.Stat(root.FS(), "index.html")
	if err != nil || !info.Mode().IsRegular() {
		root.Close()
		return nil, nil, fmt.Errorf("前端构建目录缺少 index.html，请先构建页面")
	}
	files := http.FileServerFS(root.FS())
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/" {
			path = "/index.html"
		}
		// No directory listings or SPA fallback for missing assets/routes.
		if len(path) < 2 || !fs.ValidPath(path[1:]) {
			http.NotFound(w, r)
			return
		}
		info, err := fs.Stat(root.FS(), path[1:])
		if err != nil || !info.Mode().IsRegular() {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	}), root.Close, nil
}
