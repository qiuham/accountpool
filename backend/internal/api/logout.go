package api

import "net/http"

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]string{"error": "POST only"})
		return
	}
	token := bearer(r)
	if !s.validToken(token) {
		writeJSON(w, 401, map[string]string{"error": "unauthorized"})
		return
	}
	s.mu.Lock()
	delete(s.tokens, token)
	s.mu.Unlock()
	writeJSON(w, 200, map[string]bool{"success": true})
}
