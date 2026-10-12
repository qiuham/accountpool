package claude

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestFailedProxyNeverFallsBackToDirect(t *testing.T) {
	var calls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.Write([]byte("{}")) }))
	defer target.Close()
	c, e := NewClient("http://127.0.0.1:1")
	if e != nil {
		t.Fatal(e)
	}
	var out any
	e = c.request(context.Background(), "GET", target.URL, "", nil, &out)
	if e == nil || calls.Load() != 0 {
		t.Fatalf("direct fallback: calls=%d error=%v", calls.Load(), e)
	}
}
