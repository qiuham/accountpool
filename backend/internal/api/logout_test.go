package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLogoutRevokesToken(t *testing.T) {
	s := NewServer(nil, "fixture", "test")
	token, err := s.issueToken()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/web/auth/logout", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.handleLogout(w, req)
	if w.Code != 200 || s.validToken(token) {
		t.Fatal("token not revoked")
	}
}
