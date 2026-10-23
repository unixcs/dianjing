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

	// orders (token required)
	mux.HandleFunc("POST /v2/api/orders", s.userAuth(s.handleCreateOrder))
	mux.HandleFunc("POST /v2/api/orders/{id}/pay", s.userAuth(s.handleOrderPay))
	mux.HandleFunc("POST /v2/api/orders/{id}/confirm-pay", s.userAuth(s.handleOrderConfirmPay))
	mux.HandleFunc("POST /v2/api/orders/{id}/cancel", s.userAuth(s.handleOrderCancel))
	mux.HandleFunc("POST /v2/api/orders/{id}/refund", s.userAuth(s.handleOrderRefund))
	mux.HandleFunc("GET /v2/api/orders/{id}/refund", s.userAuth(s.handleOrderRefundGet))
	mux.HandleFunc("GET /v2/api/orders", s.userAuth(s.handleOrders))
	mux.HandleFunc("GET /v2/api/orders/{id}", s.userAuth(s.handleOrderDetail))

	// WeChat message push (xpay events); token-signed, no user auth.
	mux.HandleFunc("GET /wx/msg", s.handleWxMsg)
	mux.HandleFunc("POST /wx/msg", s.handleWxMsg)

	return withLogging(ensurePreflight(mux))
}
