package api

import (
	"encoding/json"
	"github.com/ham/accountpool/backend/internal/store"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestPoolOverviewIsReadOnlyAndSeparatesReservations(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st := store.New(db)
	if err = st.SaveSupplySettings(store.SupplySettings{Target: 5, Daily: 3, CheckHours: 1, Prefix: "free-", MailBase: "https://mail.example.test", RegistrationDomain: "example.test", Domains: []string{"example.test"}, MailPassword: "fixture"}); err != nil {
		t.Fatal(err)
	}
	id, err := st.CreateAccount(&store.Account{Name: "paused", State: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	if err = st.SetAccountsPaused([]int64{id}, true); err != nil {
		t.Fatal(err)
	}
	if _, err = st.CreateAccount(&store.Account{Name: "missing", State: "mailbox_missing"}); err != nil {
		t.Fatal(err)
	}
	token, err := st.CreateConsoleSession()
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer(st, "", "")
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("GET", "/console/pool", nil)
		req.AddCookie(&http.Cookie{Name: "pool_session", Value: token})
		w := httptest.NewRecorder()
		s.consoleData(w, req)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body)
		}
		var result map[string]int
		if err = json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		for key, want := range map[string]int{"target": 5, "ready": 0, "pausedAccounts": 1, "blocked": 1, "recovering": 0, "deficit": 5, "pendingSupply": 3} {
			if result[key] != want {
				t.Fatal(key, result[key], want)
			}
		}
	}
	var count int
	db.QueryRow("SELECT COUNT(*) FROM jobs").Scan(&count)
	if count != 0 {
		t.Fatal("overview queued work")
	}
	db.QueryRow("SELECT COUNT(*) FROM accounts").Scan(&count)
	if count != 2 {
		t.Fatal("overview created accounts")
	}
}
