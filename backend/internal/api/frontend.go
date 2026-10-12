package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func FrontendHandler(root string) http.Handler {
	files := http.FileServer(http.Dir(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			w.WriteHeader(405)
			return
		}
		if r.URL.Path == "/" || r.URL.Path == "/login" || r.URL.Path == "/admin" {
			w.Header().Set("Cache-Control", "no-store")
			http.ServeFile(w, r, filepath.Join(root, "index.html"))
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/assets/") && r.URL.Path != "/favicon.svg" && r.URL.Path != "/favicon.ico" {
			http.NotFound(w, r)
			return
		}
		p := filepath.Join(root, filepath.FromSlash(r.URL.Path))
		if info, err := os.Stat(p); err != nil || info.IsDir() {
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		files.ServeHTTP(w, r)
	})
}
