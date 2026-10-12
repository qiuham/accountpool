package api

import (
	"encoding/json"
	"github.com/ham/accountpool/backend/internal/store"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestJobPages(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st := store.New(db)
	id, err := st.CreateAccount(&store.Account{Name: "example", Email: "example@test.invalid", State: "registered"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 105; i++ {
		if _, err = db.Exec("INSERT INTO jobs(account_id,kind,state,created_at) VALUES(?,'check','ok','2026-10-05 00:00:00')", id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec("INSERT INTO cleared_job_history(job_id) VALUES(1)"); err != nil {
		t.Fatal(err)
	}
	token, _ := st.CreateConsoleSession()
	server := NewServer(st, "", "")
	for _, test := range []struct {
		url               string
		code, page, count int
	}{{"?page=1", 200, 1, 20}, {"?page=6", 200, 6, 4}, {"?page=999", 200, 6, 4}, {"?page=0", 400, 0, 0}} {
		req := httptest.NewRequest("GET", "/console/jobs"+test.url, nil)
		req.AddCookie(&http.Cookie{Name: "pool_session", Value: token})
		w := httptest.NewRecorder()
		server.consoleData(w, req)
		if w.Code != test.code {
			t.Fatal(w.Code, w.Body)
		}
		if w.Code != 200 {
			continue
		}
		var got struct {
			Total, Page int
			Items       []struct{ Email string }
		}
		if err = json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Total != 104 || got.Page != test.page || len(got.Items) != test.count || got.Items[0].Email != "example@test.invalid" {
			t.Fatal(got)
		}
	}
}
