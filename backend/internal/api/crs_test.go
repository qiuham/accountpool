package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func login(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	s.handleLogin(w, httptest.NewRequest(http.MethodPost, "/web/auth/login", strings.NewReader(body)))
	return w
}
func TestLoginRejectsEmptyConfiguration(t *testing.T) {
	w := login(t, NewServer(nil, "", ""), `{"username":"","password":""}`)
	if w.Code != 503 {
		t.Fatalf("empty configured credentials accepted: status=%d", w.Code)
	}
}
func TestLoginRejectsTrailingJSON(t *testing.T) {
	w := login(t, NewServer(nil, "fixture", "secret"), `{"username":"fixture","password":"secret"} {}`)
	if w.Code != 400 {
		t.Fatalf("trailing JSON accepted: status=%d", w.Code)
	}
}
func TestLoginPrunesExpiredTokens(t *testing.T) {
	s := NewServer(nil, "fixture", "secret")
	s.tokens["expired"] = time.Now().Add(-time.Hour)
	w := login(t, s, `{"username":"fixture","password":"secret"}`)
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["success"] != true {
		t.Fatal("valid login failed")
	}
	if _, ok := s.tokens["expired"]; ok {
		t.Fatal("expired token retained")
	}
}
func TestLoginRejectsOversizeBody(t *testing.T) {
	w := login(t, NewServer(nil, "fixture", "secret"), `{"username":"`+strings.Repeat("x", 65536)+`","password":"secret"}`)
	if w.Code != 413 {
		t.Fatalf("oversize body status=%d", w.Code)
	}
}
