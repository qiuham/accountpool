package claude

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProfileDoesNotRequireHealthCheck(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/organizations" {
			t.Error("profile attempted health endpoint")
			w.WriteHeader(500)
			return
		}
		w.Write([]byte(`[{"uuid":"original","created_at":"2026-09-30T00:00:00Z","analytics_subscription_plan":"claude_free"}]`))
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), BaseURL: srv.URL}
	data, err := c.Profile(context.Background(), "fixture", "original")
	if err != nil || data["orgCreatedAt"] != "2026-09-30T00:00:00Z" || data["plan"] != "claude_free" || calls != 1 {
		t.Fatal(data, err, calls)
	}
	if _, ok := data["health"]; ok {
		t.Fatal("metadata fabricated health result")
	}
}
