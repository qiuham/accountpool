package api

import (
	"github.com/ham/accountpool/backend/internal/store"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestMailboxRestoreRequiresConsoleSessionAndOrigin(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st := store.New(db)
	token, err := st.CreateConsoleSession()
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer(st, "", "")
	for _, tc := range []struct {
		cookie, origin string
		want           int
	}{
		{"", "http://example.com", 401}, {token, "http://evil.test", 403}, {token, "http://example.com", 404},
	} {
		req := httptest.NewRequest("POST", "/console/accounts/restore-mailbox", strings.NewReader(`{"id":999}`))
		req.Header.Set("Origin", tc.origin)
		req.AddCookie(&http.Cookie{Name: "pool_session", Value: tc.cookie})
		w := httptest.NewRecorder()
		s.consoleData(w, req)
		if w.Code != tc.want {
			t.Fatal(w.Code, tc.want)
		}
	}
}
