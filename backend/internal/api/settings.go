package api

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/ham/accountpool/backend/internal/store"
)

func (s *Server) runtimeSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		v, err := s.Store.RuntimeSettings()
		if err != nil {
			writeJSON(w, 500, map[string]string{"error": "settings read failed"})
			return
		}
		writeJSON(w, 200, v)
	case http.MethodPut:
		r.Body = http.MaxBytesReader(w, r.Body, 8192)
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		var v store.RuntimeSettings
		var extra any
		if err := dec.Decode(&v); err != nil {
			writeJSON(w, 400, map[string]string{"error": "invalid settings JSON"})
			return
		}
		if dec.Decode(&extra) != io.EOF {
			writeJSON(w, 400, map[string]string{"error": "trailing JSON"})
			return
		}
		if err := v.Validate(); err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		if err := s.Store.SaveRuntimeSettings(v); err != nil {
			writeJSON(w, 500, map[string]string{"error": "settings save failed"})
			return
		}
		writeJSON(w, 200, v)
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
	}
}
