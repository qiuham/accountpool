package nettrace

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Capture func(*http.Request, *http.Response, []byte, error, time.Time)

var token = regexp.MustCompile(`sk-[A-Za-z0-9_-]+|eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+|\b[0-9]{6}\b`)
var urls = regexp.MustCompile(`https?://[^\s"'<>]+`)

func cleanText(s string) string {
	s = token.ReplaceAllString(s, "[REDACTED]")
	return urls.ReplaceAllStringFunc(s, func(raw string) string {
		u, e := url.Parse(raw)
		if e != nil {
			return "[URL]"
		}
		u.User = nil
		if u.RawQuery != "" {
			u.RawQuery = "redacted"
		}
		u.Fragment = ""
		return u.String()
	})
}
func secret(k string) bool {
	k = strings.ToLower(k)
	for _, s := range []string{"cookie", "auth", "token", "password", "secret", "session", "credential", "jwt", "proof"} {
		if strings.Contains(k, s) {
			return true
		}
	}
	return k == "code" || k == "key" || k == "raw" || k == "html" || k == "text" || k == "content" || k == "link"
}
func Clean(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, v := range x {
			if secret(k) {
				x[k] = "[REDACTED]"
			} else {
				x[k] = Clean(v)
			}
		}
		return x
	case []any:
		for i := range x {
			x[i] = Clean(x[i])
		}
		return x
	case string:
		return cleanText(x)
	}
	return v
}
func headers(h http.Header) map[string]any {
	v := map[string]any{}
	for k, values := range h {
		if secret(k) {
			v[k] = "[REDACTED]"
		} else {
			v[k] = cleanText(strings.Join(values, ", "))
		}
	}
	return v
}
func body(raw []byte) any {
	if len(raw) > 32768 {
		return map[string]any{"omitted": true, "bytes": len(raw), "reason": "body exceeds 32 KiB"}
	}
	var v any
	if json.Unmarshal(raw, &v) == nil {
		return Clean(v)
	}
	if len(raw) == 0 {
		return nil
	}
	// Non-JSON may contain credentials embedded in HTML, forms or mail MIME.
	return map[string]any{"omitted": true, "bytes": len(raw), "reason": "non-JSON body"}
}
func New(path string) (Capture, error) {
	f, e := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if e != nil {
		return nil, e
	}
	f.Close()
	var mu sync.Mutex
	count := 0
	return func(req *http.Request, resp *http.Response, raw []byte, err error, start time.Time) {
		mu.Lock()
		defer mu.Unlock()
		if count >= 200 {
			return
		}
		count++
		request := map[string]any{"method": req.Method, "url": cleanText(req.URL.String()), "headers": headers(req.Header)}
		if req.GetBody != nil {
			r, e := req.GetBody()
			if e == nil {
				b, _ := io.ReadAll(io.LimitReader(r, 32769))
				r.Close()
				request["body"] = body(b)
			}
		}
		event := map[string]any{"source": "go", "at": start.Format(time.RFC3339Nano), "durationMs": time.Since(start).Milliseconds(), "request": request, "response": nil}
		if resp != nil {
			event["response"] = map[string]any{"status": resp.StatusCode, "headers": headers(resp.Header), "body": body(raw)}
		}
		if err != nil {
			event["error"] = cleanText(err.Error())
		}
		b, e := json.Marshal(event)
		if e != nil {
			return
		}
		b = append(b, '\n')
		f, e := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
		if e != nil {
			return
		}
		defer f.Close()
		if info, e := f.Stat(); e == nil && info.Size() < 4<<20 {
			_, _ = f.Write(b)
		}
	}, nil
}
