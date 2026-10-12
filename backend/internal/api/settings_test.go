package api

import (
	"encoding/json"
	"github.com/ham/accountpool/backend/internal/store"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeSettingsRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	st := store.New(db)
	s := NewServer(st, "admin", "test")
	token, err := st.CreateConsoleSession()
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	s.Routes(mux)
	call := func(method, body, auth string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/console/settings/runtime", strings.NewReader(body))
		req.AddCookie(&http.Cookie{Name: "pool_session", Value: auth})
		req.Header.Set("Origin", "http://example.com")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	if w := call("GET", "", ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	valid := `{"gapSeconds":120,"cooldownSeconds":3600,"failureLimit":4,"taskTimeoutSeconds":600}`
	if w := call("PUT", valid, token); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	for _, bad := range []string{`{}`, valid + " {}", strings.Replace(valid, "120", "1", 1), strings.Replace(valid, "120", "1.5", 1), strings.Replace(valid, "gapSeconds", "unknown", 1)} {
		if w := call("PUT", bad, token); w.Code != 400 {
			t.Fatal("accepted invalid input", w.Code, bad)
		}
	}
	w := call("GET", "", token)
	var v store.RuntimeSettings
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.GapSeconds != 120 {
		t.Fatal(v)
	}
	db.Close()
	db, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	restored, err := store.New(db).RuntimeSettings()
	if err != nil || restored != v {
		t.Fatal(restored, err)
	}
}
