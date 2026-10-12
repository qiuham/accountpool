package nettrace

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTracePreservesTrafficAndRedacts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	capture, e := New(path)
	if e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(b), "fixture-password") {
			t.Error("request body changed")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Set-Cookie", "private-cookie")
		w.WriteHeader(403)
		w.Write([]byte(`{"error":"denied","sessionKey":"fixture-session","nested":{"token":"fixture-token"}}`))
	}))
	defer server.Close()
	req, _ := http.NewRequest("POST", server.URL+"/api?token=fixture-query", strings.NewReader(`{"password":"fixture-password","name":"fixture"}`))
	req.Header.Set("Authorization", "fixture-auth")
	start := time.Now()
	resp, e := http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := io.ReadAll(resp.Body)
	resp.Body.Close()
	capture(req, resp, raw, e, start)
	capture(req, nil, nil, errors.New("connection reset by peer"), start)
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	for _, secret := range []string{"fixture-password", "private-cookie", "fixture-session", "fixture-token", "fixture-auth", "fixture-query"} {
		if strings.Contains(string(b), secret) {
			t.Fatal("secret persisted", secret)
		}
	}
	if !strings.Contains(string(b), "denied") || !strings.Contains(string(b), "connection reset by peer") || !strings.Contains(string(b), `"response":null`) {
		t.Fatal("missing exchange")
	}
}
