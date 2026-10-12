package api

import (
	"encoding/json"
	"github.com/ham/accountpool/backend/internal/store"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestExportLifecycleContract(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "pool.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st := store.New(db)
	for _, state := range []string{"active", "burned", "registered"} {
		id, err := st.CreateAccount(&store.Account{Name: state, State: state})
		if err != nil {
			t.Fatal(err)
		}
		if state != "registered" {
			if err := st.UpdateFields(id, map[string]any{"access_token": "fixture-access", "refresh_token": "fixture-refresh", "expires_at": int64(1900000000000)}); err != nil {
				t.Fatal(err)
			}
		}
	}
	s := NewServer(st, "fixture", "fixture")
	token, err := s.issueToken()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/admin/sync/export-accounts", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.handleExport(w, req)
	if w.Code != 200 {
		t.Fatalf("status=%d", w.Code)
	}
	var payload struct {
		Data struct {
			Accounts []CRSAccount `json:"claudeAccounts"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	found := map[string]CRSAccount{}
	for _, a := range payload.Data.Accounts {
		found[a.ID] = a
	}
	if _, ok := found["registered"]; ok {
		t.Error("unimportable session-only account exported")
	}
	if a, ok := found["burned"]; !ok || a.IsActive || a.Schedulable {
		t.Error("disabled account missing explicit inactive tombstone")
	}
	if a := found["active"]; a.Credentials["expires_at"] != float64(1900000000) {
		t.Error("expiry must use Unix seconds")
	}
}

func TestExportBoundProxy(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "pool.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st := store.New(db)
	id, err := st.CreateAccount(&store.Account{Name: "bound", State: "active"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateFields(id, map[string]any{"access_token": "fixture", "refresh_token": "fixture"}); err != nil {
		t.Fatal(err)
	}
	proxyID, err := st.EnsureProxy("socks5://user:p%40ss@[::1]:1080")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.BindAccountProxy(id, proxyID); err != nil {
		t.Fatal(err)
	}
	s := NewServer(st, "fixture", "fixture")
	token, err := s.issueToken()
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"available", "burned"} {
		if _, err := db.Exec("UPDATE proxies SET state=? WHERE id=?", state, proxyID); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("GET", "/admin/sync/export-accounts", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.handleExport(w, req)
		if w.Code != 200 {
			t.Fatalf("status=%d", w.Code)
		}
		var body struct {
			Data struct {
				Accounts []CRSAccount `json:"claudeAccounts"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if len(body.Data.Accounts) != 1 {
			t.Fatal("missing account")
		}
		a := body.Data.Accounts[0]
		if a.Proxy == nil || a.Proxy.Host != "::1" || a.Proxy.Port != 1080 || a.Proxy.Username != "user" || a.Proxy.Password != "p@ss" {
			t.Fatal("proxy fields lost")
		}
		if state == "burned" && (a.IsActive || a.Schedulable) {
			t.Fatal("burned exit exported as usable")
		}
	}
}
