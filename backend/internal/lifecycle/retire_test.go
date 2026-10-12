package lifecycle

import (
	"context"
	"github.com/ham/accountpool/backend/internal/claude"
	"github.com/ham/accountpool/backend/internal/runner"
	"github.com/ham/accountpool/backend/internal/store"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestRetireThenPurgeRetainsDownstreamTombstone(t *testing.T) {
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "pool.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st := store.New(db)
	id, err := st.CreateAccount(&store.Account{Name: "fixture", State: "active", BundlePath: "bundles/fixture"})
	if err != nil {
		t.Fatal(err)
	}
	st.UpdateFields(id, map[string]any{"access_token": "fixture-access", "refresh_token": "fixture-refresh", "session_key": "fixture-session"})
	bundle := filepath.Join(root, "bundles/fixture")
	os.MkdirAll(bundle, 0700)
	os.WriteFile(filepath.Join(bundle, "proof"), []byte("fixture"), 0600)
	r := &runner.Runner{ProjectRoot: root}
	if err := Retire(context.Background(), st, r, nil, "fixture", true, true); err == nil {
		t.Fatal("purge before retirement accepted")
	}
	if err := Retire(context.Background(), st, r, nil, "fixture", false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bundle); err != nil {
		t.Fatal("retirement prematurely deleted profile")
	}
	if err := Retire(context.Background(), st, r, nil, "fixture", true, false); err == nil {
		t.Fatal("unconfirmed downstream purge accepted")
	}
	if err := Retire(context.Background(), st, r, nil, "fixture", true, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bundle); !os.IsNotExist(err) {
		t.Fatal("profile retained after purge")
	}
	a, err := st.GetAccount("fixture")
	if err != nil || a.State != "burned" || a.AccessToken != "fixture-access" || a.SessionKey != "" {
		t.Fatal("downstream tombstone lost")
	}
}
func TestPurgeRejectsOutsideBundle(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	db, err := store.Open(filepath.Join(root, "pool.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st := store.New(db)
	st.CreateAccount(&store.Account{Name: "fixture", State: "burned", BundlePath: outside})
	if err := Retire(context.Background(), st, &runner.Runner{ProjectRoot: root}, nil, "fixture", true, true); err == nil {
		t.Fatal("outside path accepted")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatal("unrelated directory removed")
	}
}

func TestMailboxDeletionFailurePreservesProfile(t *testing.T) {
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "pool.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st := store.New(db)
	st.CreateAccount(&store.Account{Name: "fixture", Email: "fixture@example.test", State: "burned", BundlePath: "bundles/fixture"})
	bundle := filepath.Join(root, "bundles/fixture")
	os.MkdirAll(bundle, 0700)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.Write([]byte(`{"results":[{"id":1,"name":"fixture@example.test"}]}`))
		} else {
			w.WriteHeader(503)
		}
	}))
	defer server.Close()
	client := &claude.MailClient{Base: server.URL, HTTP: server.Client(), AdminAuth: "fixture"}
	if err := Retire(context.Background(), st, &runner.Runner{ProjectRoot: root}, client, "fixture", true, true); err == nil {
		t.Fatal("failed mailbox deletion ignored")
	}
	if _, err := os.Stat(bundle); err != nil {
		t.Fatal("profile deleted after mailbox failure")
	}
}
