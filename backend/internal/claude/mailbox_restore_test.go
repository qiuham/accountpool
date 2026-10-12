package claude

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRestoreSameAddress(t *testing.T) {
	for _, tc := range []struct {
		name, email, returned string
		exists                bool
		wantErr               bool
		creates               int
	}{
		{"missing", "original@example.test", "original@example.test", false, false, 1},
		{"existing", "original@example.test", "original@example.test", true, false, 0},
		{"unsupported", "original@other.test", "", false, true, 0},
		{"changed-address", "original@example.test", "different@example.test", false, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			creates := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/admin/address":
					if r.Header.Get("x-admin-auth") != "admin" {
						t.Error("missing admin auth")
					}
					if tc.exists {
						fmt.Fprint(w, `{"results":[{"id":1,"name":"original@example.test"}]}`)
					} else {
						fmt.Fprint(w, `{"results":[]}`)
					}
				case "/api/admin/new_address":
					creates++
					var body map[string]string
					json.NewDecoder(r.Body).Decode(&body)
					if body["name"] != "original" || body["domain"] != "example.test" {
						t.Error("changed requested address")
					}
					json.NewEncoder(w).Encode(map[string]any{"address": tc.returned, "jwt": "fixture", "address_id": 1})
				case "/api/admin/show_password/1":
					fmt.Fprint(w, `{"jwt":"fixture"}`)
				case "/api/settings":
					json.NewEncoder(w).Encode(map[string]string{"address": tc.returned})
				default:
					t.Error("unexpected endpoint")
					w.WriteHeader(404)
				}
			}))
			defer srv.Close()
			c := &MailClient{Base: srv.URL, Domain: "example.test", AdminAuth: "admin", HTTP: srv.Client()}
			jwt, err := c.RestoreAddress(tc.email)
			if (err != nil) != tc.wantErr || creates != tc.creates {
				t.Fatal(err, creates)
			}
			if tc.wantErr && jwt != "" {
				t.Fatal("unverified credential returned")
			}
		})
	}
}
