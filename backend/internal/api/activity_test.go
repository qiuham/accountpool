package api

import (
	"github.com/ham/accountpool/backend/internal/store"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestActivitySettingsValidateAndPersist(t *testing.T) {
	db, e := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	st := store.New(db)
	s := NewServer(st, "", "")
	token, _ := st.CreateConsoleSession()
	call := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("PUT", "/console/settings/activity", strings.NewReader(body))
		r.Header.Set("Origin", "http://example.com")
		r.AddCookie(&http.Cookie{Name: "pool_session", Value: token})
		w := httptest.NewRecorder()
		s.consoleData(w, r)
		return w
	}
	w := call(`{"enabled":true,"intervalHours":24,"prompts":["hello","你好"]}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if reason, err := st.SchedulerPause(); err != nil || reason != "" {
		t.Fatalf("saving activity settings paused scheduler: %q %v", reason, err)
	}
	w = call(`{"enabled":true,"intervalHours":0,"prompts":["hello","你好"]}`)
	if w.Code != 400 {
		t.Fatal(w.Code, w.Body)
	}
	got, err := st.ActivitySettings()
	if err != nil || !got.Enabled || got.IntervalHours != 24 || len(got.Prompts) != 2 || got.Prompts[0] != "hello" {
		t.Fatal(got, err)
	}
}
