package claude

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLatestLoginFreshness(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, tc := range []struct{ name, created, text, kind string }{
		{"code", now.Format(time.RFC3339), "Your code: 123456", "code"},
		{"old", now.Add(-11 * time.Minute).Format(time.RFC3339), "Your code: 123456", "stale"},
		{"unknown", "", "Your code: 123456", "unknown_time"},
		{"future", now.Add(time.Hour).Format(time.RFC3339), "Your code: 123456", "stale"},
		{"link", now.Format(time.RFC3339), "https://claude.ai/magic-link#fixture", "link"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(map[string]any{"results": []Mail{{ID: 2, Subject: "Claude verification code", Text: tc.text, CreatedAt: tc.created}}})
			}))
			defer server.Close()
			c := MailClient{Base: server.URL, HTTP: server.Client()}
			v, err := c.LatestLogin(context.Background(), "fixture", now)
			if err != nil || v.Kind != tc.kind {
				t.Fatalf("wrong result: %v %v", v, err)
			}
			if tc.kind != "code" && v.Code != "" {
				t.Fatal("old or non-code proof exposed")
			}
		})
	}
}
