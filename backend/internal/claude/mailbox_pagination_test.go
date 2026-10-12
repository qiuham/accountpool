package claude

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestFindAddressIDSupportedPagination(t *testing.T) {
	for _, found := range []bool{true, false} {
		calls := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
			offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
			if limit < 1 || limit > 100 {
				http.Error(w, "Invalid limit", 400)
				return
			}
			if offset != (calls-1)*limit {
				t.Errorf("incorrect offset %d", offset)
			}
			addresses := []Address{}
			if offset == 0 {
				for i := 0; i < limit; i++ {
					addresses = append(addresses, Address{ID: i + 1, Name: "other@example.test"})
				}
			} else if found {
				addresses = append(addresses, Address{ID: 101, Name: "TARGET@example.test"})
			}
			json.NewEncoder(w).Encode(map[string]any{"results": addresses})
		}))
		c := &MailClient{Base: srv.URL, AdminAuth: "fixture", HTTP: srv.Client()}
		id, err := c.FindAddressID("target@example.test")
		srv.Close()
		want := 0
		if found {
			want = 101
		}
		if err != nil || id != want || calls != 2 {
			t.Fatalf("found=%v id=%d calls=%d err=%v", found, id, calls, err)
		}
	}
}
