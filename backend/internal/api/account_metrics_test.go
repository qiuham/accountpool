package api

import (
	"encoding/json"
	"github.com/ham/accountpool/backend/internal/store"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestAccountWarmupMetrics(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st := store.New(db)
	id, err := st.CreateAccount(&store.Account{Name: "metrics", State: "registered"})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct{ at, status string }{{"2026-10-01 12:00:00", "ok"}, {"2026-10-01 13:00:00", "ok"}, {"2026-10-02 12:00:00", "ok"}, {"2026-10-03 12:00:00", "failed"}} {
		_, err = db.Exec("INSERT INTO events(account_id,at,kind,detail) VALUES(?,?,'warmup',?)", id, row.at, `{"status":"`+row.status+`"}`)
		if err != nil {
			t.Fatal(err)
		}
	}
	token, err := st.CreateConsoleSession()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/console/accounts", nil)
	req.AddCookie(&http.Cookie{Name: "pool_session", Value: token})
	w := httptest.NewRecorder()
	NewServer(st, "", "").consoleData(w, req)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var rows []struct {
		RegisteredAt            string
		WarmupCount, WarmupDays int
	}
	if err = json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].WarmupCount != 3 || rows[0].WarmupDays != 2 || rows[0].RegisteredAt != "" {
		t.Fatal(rows)
	}
}
