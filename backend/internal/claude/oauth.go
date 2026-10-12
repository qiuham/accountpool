package claude

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const clientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
const redirectURI = "https://platform.claude.com/oauth/code/callback"
const oauthScope = "user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload"

func randomURLString() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// Authorize exchanges a browser session for OAuth credentials once. An uncertain
// POST is never retried here. Refresh remains the downstream pool's responsibility.
func (c *Client) Authorize(ctx context.Context, session, orgID string) (map[string]any, error) {
	orgs, err := c.Organizations(ctx, session)
	if err != nil {
		return nil, err
	}
	org, err := selectOrganization(orgs, orgID)
	if err != nil {
		return nil, err
	}
	verifier, err := randomURLString()
	if err != nil {
		return nil, err
	}
	state, err := randomURLString()
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(verifier))
	body := map[string]any{"response_type": "code", "client_id": clientID, "organization_uuid": org.ID, "redirect_uri": redirectURI, "scope": oauthScope, "state": state, "code_challenge": base64.RawURLEncoding.EncodeToString(sum[:]), "code_challenge_method": "S256"}
	var auth struct {
		Redirect string `json:"redirect_uri"`
	}
	if err := c.request(ctx, http.MethodPost, c.BaseURL+"/v1/oauth/"+url.PathEscape(org.ID)+"/authorize", session, body, &auth); err != nil {
		return nil, err
	}
	callback, err := url.Parse(auth.Redirect)
	if err != nil {
		return nil, &APIError{"invalid-oauth-redirect", 200}
	}
	expected, _ := url.Parse(redirectURI)
	if callback.Scheme != expected.Scheme || callback.Host != expected.Host || callback.Path != expected.Path || callback.User != nil || callback.Fragment != "" {
		return nil, &APIError{"invalid-oauth-redirect", 200}
	}
	q, err := url.ParseQuery(callback.RawQuery)
	if err != nil || len(q["state"]) != 1 || len(q["code"]) != 1 {
		return nil, &APIError{"invalid-oauth-response", 200}
	}
	if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1 {
		return nil, &APIError{"oauth-state-mismatch", 200}
	}
	code := q.Get("code")
	if strings.TrimSpace(code) == "" {
		return nil, &APIError{"missing-oauth-code", 200}
	}
	tokenBody := map[string]any{"grant_type": "authorization_code", "client_id": clientID, "redirect_uri": redirectURI, "code": code, "state": state, "code_verifier": verifier}
	var token struct {
		Access       string `json:"access_token"`
		Refresh      string `json:"refresh_token"`
		Expires      int64  `json:"expires_in"`
		Scope        string `json:"scope"`
		Organization struct {
			ID string `json:"uuid"`
		} `json:"organization"`
	}
	// The Claude session cookie must never be sent to the token service.
	if err := c.request(ctx, http.MethodPost, c.TokenURL, "", tokenBody, &token); err != nil {
		return nil, err
	}
	if token.Access == "" || token.Refresh == "" || token.Expires <= 0 || token.Expires > int64((365*24*time.Hour)/time.Second) || strings.TrimSpace(token.Scope) == "" {
		return nil, &APIError{"incomplete-oauth-credentials", 200}
	}
	if token.Organization.ID != "" && token.Organization.ID != org.ID {
		return nil, fmt.Errorf("oauth organization mismatch")
	}
	return map[string]any{"accessToken": token.Access, "refreshToken": token.Refresh, "expiresAt": time.Now().Add(time.Duration(token.Expires) * time.Second).UnixMilli(), "scopes": strings.Fields(token.Scope), "orgId": org.ID}, nil
}
