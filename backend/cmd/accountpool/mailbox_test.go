package main

import (
	"fmt"
	"github.com/ham/accountpool/backend/internal/claude"
	"github.com/ham/accountpool/backend/internal/store"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestEnsureMailboxRecovery(t *testing.T) {
	for _, tc := range []struct {
		name, old  string
		status     int
		wrong      bool
		wantErr    bool
		recoveries int
	}{
		{"missing", "", 0, false, false, 1},
		{"expired", "fixture-old", 401, false, false, 1},
		{"healthy", "fixture-old", 200, false, false, 0},
		{"outage", "fixture-old", 503, false, true, 0},
		{"wrong-address", "", 0, true, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			restores, creates := 0, 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/settings":
					if r.Header.Get("Authorization") == "Bearer fixture-old" && tc.status != 200 {
						w.WriteHeader(tc.status)
						return
					}
					if tc.wrong {
						fmt.Fprint(w, `{"address":"other@example.test"}`)
					} else {
						fmt.Fprint(w, `{"address":"fixture@example.test"}`)
					}
				case "/api/admin/address":
					if r.Header.Get("x-admin-auth") != "fixture-admin" {
						t.Error("missing administrator auth")
					}
					fmt.Fprint(w, `{"results":[{"id":7,"name":"fixture@example.test"}]}`)
				case "/api/admin/show_password/7":
					restores++
					fmt.Fprint(w, `{"jwt":"fixture-recovered"}`)
				case "/api/admin/new_address":
					creates++
					w.WriteHeader(500)
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer srv.Close()
			db, err := store.Open(filepath.Join(t.TempDir(), "pool.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			st := store.New(db)
			cfg, cfgErr := st.SupplySettings()
			if cfgErr != nil {
				t.Fatal(cfgErr)
			}
			cfg.Domains = []string{"example.test", "second.test"}
			if cfgErr = st.SaveSupplySettings(cfg); cfgErr != nil {
				t.Fatal(cfgErr)
			}
			a := &store.Account{Name: "fixture", Email: "fixture@example.test", MailJWT: tc.old}
			id, err := st.CreateAccount(a)
			if err != nil {
				t.Fatal(err)
			}
			a.ID = id
			st.UpdateFields(id, map[string]any{"mail_jwt": tc.old})
			mc := &claude.MailClient{Base: srv.URL, AdminAuth: "fixture-admin", HTTP: srv.Client()}
			jwt, err := ensureMailbox(st, mc, a)
			if (err != nil) != tc.wantErr {
				t.Fatalf("unexpected error: %v", err)
			}
			if restores != tc.recoveries || creates != 0 {
				t.Fatalf("restores=%d creates=%d", restores, creates)
			}
			saved, err := st.GetAccountByID(id)
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantErr {
				if saved.MailJWT != tc.old {
					t.Fatal("unverified credentials persisted")
				}
			} else if jwt == "" || saved.MailJWT != jwt {
				t.Fatal("verified credential missing")
			}
		})
	}
}
