package api

import (
	"fmt"
	"github.com/ham/accountpool/backend/internal/store"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestAccountPauseAPI(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st := store.New(db)
	id, _ := st.CreateAccount(&store.Account{Name: "pause", State: "registered"})
	token, _ := st.CreateConsoleSession()
	server := NewServer(st, "", "")
	call := func(body string, auth bool, origin string) int {
		r := httptest.NewRequest("POST", "/console/accounts/maintenance", strings.NewReader(body))
		r.Header.Set("Origin", origin)
		if auth {
			r.AddCookie(&http.Cookie{Name: "pool_session", Value: token})
		}
		w := httptest.NewRecorder()
		server.consoleData(w, r)
		return w.Code
	}
	if code := call(fmt.Sprintf(`{"ids":[%d],"paused":true}`, id), true, "http://example.com"); code != 200 {
		t.Fatal(code)
	}
	if paused, _ := st.AccountMaintenancePaused(id); !paused {
		t.Fatal("not paused")
	}
	if code := call(`{"ids":[],"paused":true}`, true, "http://example.com"); code != 400 {
		t.Fatal(code)
	}
	if code := call(fmt.Sprintf(`{"ids":[%d],"paused":false}`, id), true, "https://foreign.invalid"); code < 400 {
		t.Fatal("cross origin allowed")
	}
	if code := call(fmt.Sprintf(`{"ids":[%d],"paused":false}`, id), false, "http://example.com"); code < 400 {
		t.Fatal("unauthorized allowed")
	}
}
