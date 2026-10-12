package api

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestFrontendRoutes(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "index.html"), []byte("<html>AccountPool</html>"), 0600)
	os.Mkdir(filepath.Join(root, "assets"), 0700)
	os.WriteFile(filepath.Join(root, "assets", "app.js"), []byte("app"), 0600)
	h := FrontendHandler(root)
	for _, tc := range []struct {
		method, path string
		want         int
	}{{"GET", "/login", 200}, {"GET", "/admin", 200}, {"GET", "/assets/app.js", 200}, {"GET", "/console/missing", 404}, {"GET", "/assets/missing.js", 404}, {"POST", "/login", 405}} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != tc.want {
			t.Fatalf("%s: %d", tc.path, w.Code)
		}
	}
}
