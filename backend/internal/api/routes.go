package api

import "net/http"

// Routes builds the user-facing miniapp API mux. Routes use the Go 1.22+
// pattern syntax served by net/http.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	// auth (no token required)
	mux.HandleFunc("POST /v2/api/auth/login", s.handleLogin)

	// catalog (no token required: browsing is public)
	mux.HandleFunc("GET /v2/api/home", s.handleHome)
	mux.HandleFunc("GET /v2/api/categories", s.handleCategories)
	mux.HandleFunc("GET /v2/api/services", s.handleServices)
	mux.HandleFunc("GET /v2/api/services/{id}", s.handleServiceDetail)
	mux.HandleFunc("GET /v2/api/docs/{key}", s.handleDoc)

	return withLogging(ensurePreflight(mux))
}
