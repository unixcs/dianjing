package api

import (
	"embed"
	"net/http"
)

//go:embed admin_static/index.html
var adminFS embed.FS

// handleAdminPage serves the single-page admin UI (native HTML/JS, no
// build chain). The page talks to /v2/api/admin/* with the session token.
func (s *Server) handleAdminPage(w http.ResponseWriter, r *http.Request) {
	b, err := adminFS.ReadFile("admin_static/index.html")
	if err != nil {
		http.Error(w, "admin page missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(b)
}
