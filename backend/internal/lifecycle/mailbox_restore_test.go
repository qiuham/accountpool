package lifecycle

import (
	"fmt"
	"github.com/ham/accountpool/backend/internal/claude"
	"github.com/ham/accountpool/backend/internal/store"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestRestoreMailboxRequiresEvidenceAndResumes(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st := store.New(db)
	id, err := st.CreateAccount(&store.Account{Name: "original", Email: "original@example.test", State: "mailbox_missing"})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch r.URL.Path {
		case "/api/admin/address":
			fmt.Fprint(w, `{"results":[]}`)
		case "/api/admin/new_address":
			fmt.Fprint(w, `{"address":"original@example.test","jwt":"fixture"}`)
		case "/api/settings":
			fmt.Fprint(w, `{"address":"original@example.test"}`)
		default:
			t.Error("unexpected path")
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	mc := &claude.MailClient{Base: srv.URL, Domain: "example.test", AdminAuth: "admin", HTTP: srv.Client()}
	if err = RestoreMailbox(st, mc, id); err == nil || calls != 0 {
		t.Fatal("unconfirmed account modified")
	}
	if err = st.AddEvent(id, "registration-confirmed", "", "", "fixture historical proof"); err != nil {
		t.Fatal(err)
	}
	unlock, err := st.LockWorker()
	if err != nil {
		t.Fatal(err)
	}
	err = RestoreMailbox(st, mc, id)
	unlock()
	if err == nil || calls != 0 {
		t.Fatal("worker lock ignored")
	}
	if err = RestoreMailbox(st, mc, id); err != nil {
		t.Fatal(err)
	}
	a, err := st.GetAccountByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if a.State != "login_required" || a.Email != "original@example.test" || a.MailJWT != "fixture" {
		t.Fatal("wrong restored identity/state")
	}
	before := calls
	if err = RestoreMailbox(st, mc, id); err == nil || calls != before {
		t.Fatal("repeat click recreated mailbox")
	}
}
