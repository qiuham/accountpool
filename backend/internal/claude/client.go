package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/ham/accountpool/backend/internal/nettrace"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client owns Claude HTTP operations. Browser sessions are supplied by the
// browser adapter; this client never opens profiles or logs upstream bodies.
type Client struct {
	Trace    nettrace.Capture
	HTTP     *http.Client
	BaseURL  string
	TokenURL string
}

type APIError struct {
	Reason string
	Status int
}

func (e *APIError) Error() string { return fmt.Sprintf("%s (HTTP %d)", e.Reason, e.Status) }

// UncertainRequestOutcome means a mutating request may have reached upstream.
type UncertainRequestOutcome struct{ Err error }

func (e *UncertainRequestOutcome) Error() string        { return e.Err.Error() }
func (e *UncertainRequestOutcome) Unwrap() error        { return e.Err }
func (e *UncertainRequestOutcome) OutcomeUnknown() bool { return true }

func NewClient(proxy string) (*Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil // Never silently use a different environment proxy.
	if proxy != "" {
		u, err := url.Parse(proxy)
		if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "socks5") {
			return nil, fmt.Errorf("invalid configured proxy")
		}
		transport.Proxy = http.ProxyURL(u)
	}
	return &Client{HTTP: &http.Client{Transport: transport, Timeout: 40 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, BaseURL: "https://claude.ai", TokenURL: "https://platform.claude.com/v1/oauth/token"}, nil
}

func (c *Client) request(ctx context.Context, method, target, session string, body any, dest any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return &APIError{"invalid-request", 0}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", c.BaseURL)
	req.Header.Set("Referer", c.BaseURL+"/new")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:152.0) Gecko/20100101 Firefox/152.0")
	req.Header.Set("anthropic-client-platform", "web_claude_ai")
	if target == c.TokenURL {
		req.Header.Set("User-Agent", "axios/1.13.6")
		req.Header.Set("Accept", "application/json, text/plain, */*")
	}
	if session != "" {
		req.AddCookie(&http.Cookie{Name: "sessionKey", Value: session})
	}
	started := time.Now()
	var traceRaw []byte
	var traceErr error
	resp, err := c.HTTP.Do(req)
	defer func() {
		if c.Trace != nil {
			c.Trace(req, resp, traceRaw, traceErr, started)
		}
	}()
	traceErr = err

	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if method == http.MethodPost {
			return &UncertainRequestOutcome{&APIError{"http-transport-error", 0}}
		}
		return &APIError{"http-transport-error", 0}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	traceRaw, traceErr = raw, err
	if err != nil {
		if method == http.MethodPost {
			return &UncertainRequestOutcome{&APIError{"http-read-error", resp.StatusCode}}
		}
		return &APIError{"http-read-error", resp.StatusCode}
	}
	if resp.StatusCode != http.StatusOK {
		reason := "http-error"
		switch resp.StatusCode {
		case 401:
			reason = "blocked:session-expired"
		case 429:
			reason = "blocked:rate-limited"
		case 403:
			reason = "blocked:access-denied"
		}
		if resp.Header.Get("cf-mitigated") == "challenge" {
			reason = "blocked:cf-challenge-not-passed"
		}
		var payload struct {
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &payload) == nil && payload.Error.Type == "permission_error" && strings.Contains(payload.Error.Message, "requires a Pro or Max subscription") {
			reason = "blocked:not-subscribed"
		}
		if method == http.MethodPost && resp.StatusCode >= 500 {
			return &UncertainRequestOutcome{&APIError{reason, resp.StatusCode}}
		}
		return &APIError{reason, resp.StatusCode}
	}
	if err := json.Unmarshal(raw, dest); err != nil {
		if method == http.MethodPost {
			return &UncertainRequestOutcome{&APIError{"invalid-upstream-response", resp.StatusCode}}
		}
		return &APIError{"invalid-upstream-response", resp.StatusCode}
	}
	return nil
}
