package api

import (
	"encoding/json"
	"github.com/ham/accountpool/backend/internal/store"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestHealthStatusDoesNotFlicker(t *testing.T) {
	cases := []struct {
		name, previous, current, want string
		age                           time.Duration
	}{
		{"overdue successful check", "ok", "", "normal", 2 * time.Hour},
		{"queued next check", "ok", "pending", "normal", time.Minute},
		{"running next check", "ok", "running", "normal", time.Minute},
		{"overdue and running", "ok", "running", "normal", 2 * time.Hour},
		{"failed completed check", "failed", "", "abnormal", time.Minute},
		{"failed then running retry", "failed", "running", "abnormal", time.Minute},
		{"blocked then queued retry", "blocked", "pending", "abnormal", time.Minute},
		{"successful then failed", "ok", "failed", "abnormal", time.Minute},
		{"failed then recovered", "failed", "ok", "normal", time.Minute},
		{"first completed success", "", "ok", "normal", time.Minute},
		{"first completed failure", "", "failed", "abnormal", time.Minute},
		{"first queued check", "", "pending", "initializing", time.Minute},
		{"first running check", "", "running", "initializing", time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, err := store.Open(filepath.Join(t.TempDir(), "db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			st := store.New(db)
			id, err := st.CreateAccount(&store.Account{Name: "fixture", Email: "fixture@example.test", State: "aging"})
			if err != nil {
				t.Fatal(err)
			}
			if err = st.UpdateFields(id, map[string]any{"session_key": "fixture-session"}); err != nil {
				t.Fatal(err)
			}
			add := func(kind, state string) {
				t.Helper()
				finished := ""
				if state != "pending" && state != "running" {
					finished = time.Now().Add(-tc.age).Format("2006-01-02 15:04:05")
				}
				if _, e := db.Exec(`INSERT INTO jobs(account_id,kind,state,created_at,finished_at) VALUES(?,?,?,?,?)`, id, kind, state, "2026-01-01 00:00:00", finished); e != nil {
					t.Fatal(e)
				}
			}
			add("register", "ok")
			add("onboard", "ok")
			if tc.previous != "" {
				add("check", tc.previous)
			}
			if tc.current != "" {
				add("check", tc.current)
			}
			token, err := st.CreateConsoleSession()
			if err != nil {
				t.Fatal(err)
			}
			s := NewServer(st, "", "")
			req := httptest.NewRequest("GET", "/console/accounts", nil)
			req.AddCookie(&http.Cookie{Name: "pool_session", Value: token})
			w := httptest.NewRecorder()
			s.consoleData(w, req)
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body)
			}
			var rows []struct {
				DisplayStatus string `json:"displayStatus"`
				Schedule      struct {
					Label string `json:"label"`
				} `json:"schedule"`
			}
			if err = json.Unmarshal(w.Body.Bytes(), &rows); err != nil || len(rows) != 1 {
				t.Fatal(err, w.Body)
			}
			if rows[0].DisplayStatus != tc.want {
				t.Fatalf("status=%s want=%s", rows[0].DisplayStatus, tc.want)
			}
			if tc.current == "running" && rows[0].Schedule.Label != "执行中" {
				t.Fatal("running check must remain visible in execution plan")
			}
		})
	}
}
