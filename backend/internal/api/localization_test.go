package api

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestErrorsAreChineseAtResponseBoundary(t *testing.T) {
	for _, value := range []any{map[string]string{"error": "crashed-before-report"}, map[string]any{"error": "login-navigation-timeout"}} {
		w := httptest.NewRecorder()
		writeJSON(w, 400, value)
		if strings.Contains(w.Body.String(), "-report") || strings.Contains(w.Body.String(), "-timeout") {
			t.Fatal(w.Body.String())
		}
	}
}
