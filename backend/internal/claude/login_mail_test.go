package claude

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFreshLoginProof(t *testing.T) {
	for _, tc := range []struct {
		name  string
		mails []Mail
		want  string
	}{
		{"old", []Mail{{ID: 10, Text: "https://claude.ai/magic-link#old"}}, ""},
		{"fresh", []Mail{{ID: 11, Text: "https://claude.ai/magic-link#fresh"}}, "https://claude.ai/magic-link#fresh"},
		{"unrelated", []Mail{{ID: 11, Text: "newsletter 123456"}}, ""},
		{"wrong-domain", []Mail{{ID: 11, Text: "https://claude.ai.attacker.example/magic-link#bad"}}, ""},
		{"newest", []Mail{{ID: 11, Text: "https://claude.ai/magic-link#old"}, {ID: 12, Text: "https://claude.ai/magic-link#new"}}, "https://claude.ai/magic-link#new"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := selectLoginProof(tc.mails, 10)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == "" {
				if p != nil {
					t.Fatal("unexpected proof")
				}
			} else if p == nil || p.Link != tc.want {
				t.Fatal("wrong fresh proof")
			}
		})
	}
	p, err := selectLoginProof([]Mail{{ID: 11, Subject: "Claude verification code", Text: "Your code: 123456"}}, 10)
	if err != nil || p == nil || p.Code != "123456" {
		t.Fatal("OTP not selected")
	}
	if _, err := selectLoginProof([]Mail{{Text: "private"}}, 10); err == nil {
		t.Fatal("malformed row accepted")
	}
}
func TestLoginPollFailures(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		recover bool
		want    string
	}{{"expired", 401, false, "credential-invalid"}, {"empty", 200, false, "verification-timeout"}, {"outage", 503, false, "read-failed"}, {"recovered", 503, true, ""}} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if tc.recover && calls > 1 {
					json.NewEncoder(w).Encode(map[string]any{"results": []Mail{{ID: 11, Text: "https://claude.ai/magic-link#fresh"}}})
					return
				}
				w.WriteHeader(tc.status)
				w.Write([]byte(`{"results":[]}`))
			}))
			defer server.Close()
			c := &MailClient{Base: server.URL, HTTP: server.Client()}
			proof, err := c.WaitForLogin(context.Background(), "fixture", 10, 60*time.Millisecond, 5*time.Millisecond)
			if tc.want == "" {
				if err != nil || proof == nil {
					t.Fatal("transient recovery failed")
				}
			} else {
				var me *MailboxError
				if !errors.As(err, &me) || me.Kind != tc.want {
					t.Fatalf("wrong classification: %v", err)
				}
			}
			if tc.status == 401 && calls != 1 {
				t.Fatal("expired credential polled repeatedly")
			}
		})
	}
}
