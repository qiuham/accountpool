package claude

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestOAuthProtocol(t *testing.T) {
	for _, mode := range []string{"success", "state-mismatch", "wrong-redirect", "incomplete-token", "exchange-failure", "not-subscribed", "org-mismatch"} {
		t.Run(mode, func(t *testing.T) {
			var challenge, state string
			tokenCalls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/organizations":
					if cookie, err := r.Cookie("sessionKey"); err != nil || cookie.Value != "fixture-session" {
						t.Error("missing browser session")
					}
					json.NewEncoder(w).Encode([]map[string]any{{"uuid": "fixture-org"}})
				case "/v1/oauth/fixture-org/authorize":
					var body map[string]string
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					challenge = body["code_challenge"]
					state = body["state"]
					if len(state) < 32 || body["code_challenge_method"] != "S256" || body["organization_uuid"] != "fixture-org" || body["client_id"] != clientID || body["redirect_uri"] != redirectURI || body["scope"] != oauthScope {
						t.Error("invalid authorization request")
					}
					if mode == "not-subscribed" {
						w.WriteHeader(403)
						w.Write([]byte(`{"error":{"type":"permission_error","message":"Claude Code requires a Pro or Max subscription."}}`))
						return
					}
					returnedState := state
					if mode == "state-mismatch" {
						returnedState = "wrong"
					}
					target := redirectURI
					if mode == "wrong-redirect" {
						target = "https://other.example/callback"
					}
					json.NewEncoder(w).Encode(map[string]string{"redirect_uri": target + "?code=fixture-code&state=" + url.QueryEscape(returnedState)})
				case "/token":
					tokenCalls++
					if r.Header.Get("Cookie") != "" {
						t.Error("browser session leaked to token endpoint")
					}
					var body map[string]string
					json.NewDecoder(r.Body).Decode(&body)
					sum := sha256.Sum256([]byte(body["code_verifier"]))
					if base64.RawURLEncoding.EncodeToString(sum[:]) != challenge || body["state"] != state || body["code"] != "fixture-code" {
						t.Error("PKCE/state/code mismatch")
					}
					if mode == "exchange-failure" {
						w.WriteHeader(503)
						w.Write([]byte("private-body"))
						return
					}
					if mode == "incomplete-token" {
						w.Write([]byte(`{"access_token":"partial"}`))
						return
					}
					org := "fixture-org"
					if mode == "org-mismatch" {
						org = "wrong-org"
					}
					json.NewEncoder(w).Encode(map[string]any{"access_token": "fixture-access", "refresh_token": "fixture-refresh", "expires_in": 3600, "scope": "user:profile user:inference", "organization": map[string]string{"uuid": org}})
				default:
					t.Error("unexpected path")
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			client, _ := NewClient("")
			client.BaseURL = server.URL
			client.TokenURL = server.URL + "/token"
			creds, err := client.Authorize(context.Background(), "fixture-session", "")
			if mode == "success" {
				if err != nil || creds["accessToken"] != "fixture-access" {
					t.Fatalf("authorization failed: %v", err)
				}
			} else if err == nil {
				t.Fatal("invalid authorization accepted")
			}
			if mode == "exchange-failure" {
				var uncertain *UncertainRequestOutcome
				if !errors.As(err, &uncertain) {
					t.Fatal("uncertain token exchange not flagged")
				}
			}
			if mode == "state-mismatch" || mode == "wrong-redirect" || mode == "not-subscribed" {
				if tokenCalls != 0 {
					t.Fatal("invalid authorize response reached exchange")
				}
			} else if tokenCalls != 1 {
				t.Fatal("token request was repeated or missing")
			}
			if err != nil && (strings.Contains(err.Error(), "private-body") || strings.Contains(err.Error(), "fixture-access")) {
				t.Fatal("credential in diagnostic")
			}
		})
	}
}

func TestHTTPFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		status int
		reason string
	}{{401, "blocked:session-expired"}, {403, "blocked:access-denied"}, {429, "blocked:rate-limited"}, {503, "http-error"}} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status) }))
		c, _ := NewClient("")
		c.BaseURL = server.URL
		_, err := c.Organizations(context.Background(), "fixture")
		if e, ok := err.(*APIError); !ok || e.Reason != tc.reason {
			t.Errorf("HTTP %d: %v", tc.status, err)
		}
		server.Close()
	}
}
