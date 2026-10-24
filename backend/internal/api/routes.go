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

	// my refunds + feedback (token required)
	mux.HandleFunc("GET /v2/api/refunds", s.userAuth(s.handleMyRefunds))
	mux.HandleFunc("GET /v2/api/feedbacks", s.userAuth(s.handleMyFeedbacks))
	mux.HandleFunc("POST /v2/api/feedbacks", s.userAuth(s.handleMyFeedbacks))

	// WeChat message push (xpay events); token-signed, no user auth.
	mux.HandleFunc("GET /wx/msg", s.handleWxMsg)
	mux.HandleFunc("POST /wx/msg", s.handleWxMsg)

	// Admin panel: page + assets embedded, API behind password session.
	mux.HandleFunc("GET /v2/admin", s.handleAdminPage)
	mux.HandleFunc("POST /v2/api/admin/login", s.handleAdminLogin)
	mux.HandleFunc("GET /v2/api/admin/overview", s.adminAuth(s.handleAdminOverview))
	mux.HandleFunc("GET /v2/api/admin/categories", s.adminAuth(s.handleAdminCategories))
	mux.HandleFunc("POST /v2/api/admin/categories", s.adminAuth(s.handleAdminCategories))
	mux.HandleFunc("GET /v2/api/admin/services", s.adminAuth(s.handleAdminServices))
	mux.HandleFunc("POST /v2/api/admin/services", s.adminAuth(s.handleAdminServices))
	mux.HandleFunc("PUT /v2/api/admin/services/{id}", s.adminAuth(s.handleAdminServiceOne))
	mux.HandleFunc("DELETE /v2/api/admin/services/{id}", s.adminAuth(s.handleAdminServiceOne))
	mux.HandleFunc("GET /v2/api/admin/orders", s.adminAuth(s.handleAdminOrders))
	mux.HandleFunc("POST /v2/api/admin/orders/{id}/status", s.adminAuth(s.handleAdminOrderStatus))
	mux.HandleFunc("GET /v2/api/admin/refunds", s.adminAuth(s.handleAdminRefunds))
	mux.HandleFunc("POST /v2/api/admin/refunds/{id}/audit", s.adminAuth(s.handleAdminRefundAudit))
	mux.HandleFunc("GET /v2/api/admin/feedbacks", s.adminAuth(s.handleAdminFeedbacks))
	mux.HandleFunc("POST /v2/api/admin/feedbacks", s.adminAuth(s.handleAdminFeedbacks))
	mux.HandleFunc("GET /v2/api/admin/settings", s.adminAuth(s.handleAdminSettings))
	mux.HandleFunc("POST /v2/api/admin/settings", s.adminAuth(s.handleAdminSettings))
	mux.HandleFunc("POST /v2/api/admin/upload", s.adminAuth(s.handleAdminUpload))

	return withLogging(ensurePreflight(mux))
}
