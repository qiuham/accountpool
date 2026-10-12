package claude

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthEvidence(t *testing.T) {
	free := "claude_free"
	until := "2099-01-01"
	for _, tc := range []struct {
		name string
		orgs []Organization
		want string
	}{
		{"unknown", []Organization{{ID: "one"}}, "unknown"},
		{"free", []Organization{{ID: "one", Plan: &free}}, "not_subscribed"},
		{"temporary-other-org", []Organization{{ID: "one", Plan: &free}, {ID: "two", DisabledReason: "temporary", DisabledUntil: &until}}, "restricted"},
		{"permanent-other-org", []Organization{{ID: "one", Plan: &free}, {ID: "two", DisabledReason: "trust_and_safety"}}, "banned"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values, err := ClassifyHealth(tc.orgs, "one")
			if err != nil || values["health"] != tc.want {
				t.Fatalf("health=%v error=%v", values["health"], err)
			}
			if tc.want == "unknown" {
				if _, ok := values["subscribed"]; ok {
					t.Fatal("unknown subscription fabricated")
				}
			}
		})
	}
	if _, err := ClassifyHealth([]Organization{{ID: "one"}}, "missing"); err == nil {
		t.Fatal("unknown selected org silently replaced")
	}
}
func TestKYCFailureIsNotHealthy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/organizations" {
			w.Write([]byte(`[{"uuid":"one","analytics_subscription_plan":"claude_pro"}]`))
		} else {
			w.WriteHeader(503)
		}
	}))
	defer server.Close()
	c, _ := NewClient("")
	c.BaseURL = server.URL
	if _, err := c.Check(context.Background(), "fixture", "one"); err == nil {
		t.Fatal("KYC outage reported healthy")
	}
}

func TestFreeAccountVerificationIsNotHealthy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/organizations" {
			w.Write([]byte(`[{"uuid":"fixture","analytics_subscription_plan":"claude_free"}]`))
			return
		}
		w.Write([]byte(`{"status":"pending"}`))
	}))
	defer server.Close()
	c, _ := NewClient("")
	c.BaseURL = server.URL
	result, err := c.Check(context.Background(), "fixture", "")
	if err != nil || result["health"] != "needs_verification" {
		t.Fatalf("result=%v err=%v", result, err)
	}
}
