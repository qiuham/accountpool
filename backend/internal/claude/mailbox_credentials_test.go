package claude

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMailboxFailureCategories(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		body, kind string
	}{
		{"expired", 401, "fixture-private", "credential-invalid"},
		{"empty", 200, `{"results":[]}`, "verification-timeout"},
		{"outage", 503, "fixture-private", "read-failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			c := &MailClient{Base: srv.URL, HTTP: srv.Client()}
			_, err := c.WaitForMail("fixture", 15*time.Millisecond, 5*time.Millisecond, nil)
			if err == nil || !strings.Contains(err.Error(), tc.kind) {
				t.Fatalf("wrong category: %v", err)
			}
			if strings.Contains(err.Error(), "fixture-private") {
				t.Fatal("response body leaked")
			}
			if tc.status == 401 {
				var typed *MailboxError
				if calls != 1 || !errors.As(err, &typed) {
					t.Fatal("credential failure was polled repeatedly")
				}
			}
		})
	}
}
func TestMalformedMailboxListIsNotEmptyInbox(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"unexpected":true}`) }))
	defer srv.Close()
	c := &MailClient{Base: srv.URL, HTTP: srv.Client()}
	if _, err := c.ListMails(context.Background(), "fixture", 20, 0); err == nil {
		t.Fatal("malformed inbox was accepted")
	}
	if _, err := c.ListAddresses(20, 0); err == nil {
		t.Fatal("malformed address list was accepted")
	}
}

func TestMissingMailboxAdminCredentialStopsLocally(t *testing.T) {
	t.Setenv("MAIL_ADMIN_AUTH", "")
	client := NewMailClient()
	if client.AdminAuth != "" {
		t.Fatal("embedded admin credential still present")
	}
	client.HTTP = nil // No HTTP request should be attempted.
	if _, err := client.ListAddresses(1, 0); err == nil {
		t.Fatal("missing admin credential accepted")
	}
}
