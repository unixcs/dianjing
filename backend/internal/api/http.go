// Package api implements the user-facing (miniapp) HTTP endpoints and
// the shared middleware/auth used by both user and admin routes.
package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"dianjing/backend/internal/app"
	"dianjing/backend/internal/config"
	"dianjing/backend/internal/store"
	"dianjing/backend/internal/xpay"
)

type Server struct {
	Cfg  *config.Config
	St   *store.Store
	App  *app.App
	XPay *xpay.Client

	// plaintext-push replay cache (see push.go pushNonceSeen)
	pushMu    sync.Mutex
	pushNonce map[string]int64
}

// ---- response helpers ----

type errResp struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func okJSON(w http.ResponseWriter, v any) {
	writeJSON(w, http.StatusOK, v)
}

func fail(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errResp{Code: status, Msg: msg})
}

func failMsg(w http.ResponseWriter, msg string) {
	fail(w, http.StatusBadRequest, msg)
}

// ---- auth token (stateless HMAC) ----

func (s *Server) signToken(userID int64, expires int64) string {
	payload := fmt.Sprintf("%d.%d", userID, expires)
	mac := hmac.New(sha256.New, []byte(s.tokenSecret()))
	mac.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Server) tokenSecret() string {
	// Derive a stable secret from the xpay app key or app secret; the
	// deployment must set at least one of them.
	return s.Cfg.XPayAppKey + "|" + s.Cfg.WxAppSecret + "|dianjing-token"
}

func (s *Server) verifyToken(token string) (int64, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return 0, errors.New("bad token")
	}
	userID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, err
	}
	expires, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, err
	}
	if expires < time.Now().Unix() {
		return 0, errors.New("token expired")
	}
	want := s.signToken(userID, expires)
	if !hmac.Equal([]byte(want), []byte(token)) {
		return 0, errors.New("bad token signature")
	}
	return userID, nil
}

// userAuth wraps a handler requiring a valid Bearer token of an active user.
func (s *Server) userAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Fail closed: an empty token secret (both signing inputs missing)
		// would make every token verifiable as the degenerate constant.
		if s.Cfg.XPayAppKey == "" && s.Cfg.WxAppSecret == "" {
			fail(w, http.StatusInternalServerError, "服务端未配置签名密钥")
			return
		}
		h := r.Header.Get("Authorization")
		if !strings.HasPrefix(h, "Bearer ") {
			fail(w, http.StatusUnauthorized, "未登录")
			return
		}
		userID, err := s.verifyToken(strings.TrimPrefix(h, "Bearer "))
		if err != nil {
			fail(w, http.StatusUnauthorized, "登录已过期")
			return
		}
		u, err := s.St.UserByID(userID)
		if err != nil || u.Status != 1 {
			fail(w, http.StatusUnauthorized, "账号不可用")
			return
		}
		next(w, r.WithContext(withUser(r.Context(), u)))
	}
}

// ---- logging middleware ----

func withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		fmt.Printf("%s %s %s\n", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}

func ensurePreflight(next http.Handler) http.Handler { return next } // miniapp requests are same-origin; no CORS needed.

var _ = sql.ErrNoRows
