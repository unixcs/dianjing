package api

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"dianjing/backend/internal/app"
	"dianjing/backend/internal/xpay"
)

type orderJSON struct {
	ID         int64  `json:"id"`
	OrderNo    string `json:"order_no"`
	Title      string `json:"title"`
	Cover      string `json:"cover"`
	PriceCents int64  `json:"price_cents"`
	Quantity   int64  `json:"quantity"`
	TotalCents int64  `json:"total_cents"`
	Status     int    `json:"status"`
	StatusText string `json:"status_text"`
	PayTime    int64  `json:"pay_time"`
	CreatedAt  int64  `json:"created_at"`
	CanPay     bool   `json:"can_pay"`
	CanCancel  bool   `json:"can_cancel"`
	CanRefund  bool   `json:"can_refund"`
	RefundMode string `json:"refund_mode,omitempty"`
}

func statusText(st int) string {
	switch st {
	case app.OrderPendingPay:
		return "待支付"
	case app.OrderContact:
		return "待联系"
	case app.OrderServing:
		return "服务中"
	case app.OrderDone:
		return "已完成"
	case app.OrderCancelled:
		return "已取消"
	case app.OrderRefunding:
		return "退款中"
	case app.OrderRefunded:
		return "已退款"
	}
	return "未知"
}

func (s *Server) toOrderJSON(o *app.Order) orderJSON {
	mode := app.RefundModeFor(o, s.App.HasActiveRefund(o.ID))
	j := orderJSON{
		ID:         o.ID,
		OrderNo:    o.OrderNo,
		Title:      o.TitleSnapshot,
		Cover:      s.serviceCoverURL(o.CoverSnapshot),
		PriceCents: o.PriceCents,
		Quantity:   o.Quantity,
		TotalCents: o.TotalCents,
		Status:     o.Status,
		StatusText: statusText(o.Status),
		PayTime:    o.PayTime,
		CreatedAt:  o.CreatedAt,
		CanPay:     o.Status == app.OrderPendingPay,
		CanCancel:  o.Status == app.OrderPendingPay,
		CanRefund:  mode != app.RefundModeNone,
		RefundMode: mode,
	}
	return j
}

// ---- POST /orders ----

type createOrderReq struct {
	ServiceID   int64  `json:"service_id"`
	Quantity    int64  `json:"quantity"`
	ClientToken string `json:"client_token"`
	Contact     string `json:"contact"`
}

func (s *Server) handleCreateOrder(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	var req createOrderReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ServiceID <= 0 {
		failMsg(w, "参数非法")
		return
	}
	if req.ClientToken == "" {
		req.ClientToken = randToken()
	}
	o, err := s.App.CreateOrder(u.ID, req.ServiceID, req.Quantity, req.ClientToken, req.Contact)
	if err != nil {
		failMsg(w, err.Error())
		return
	}
	okJSON(w, map[string]any{"order": s.toOrderJSON(o), "order_id": o.ID})
}

func randToken() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// ---- POST /orders/:id/pay : build wx.requestVirtualPayment params ----

func (s *Server) handleOrderPay(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	o, err := s.App.OrderOwnedBy(id, u.ID)
	if err != nil {
		fail(w, http.StatusNotFound, "订单不存在")
		return
	}
	if o.Status != app.OrderPendingPay {
		failMsg(w, "订单状态不可支付")
		return
	}
	sv, err := s.App.ServiceByID(o.ServiceID)
	if err != nil || sv.XPayProduct == "" {
		failMsg(w, "该服务未配置虚拟支付商品")
		return
	}
	// session_key freshness: prefer re-login code if provided, fall back
	// to stored session_key (ported from Payment.php).
	sessionKey := u.SessionKey
	if code := r.URL.Query().Get("login_code"); code != "" {
		if sess, err := s.wxCodeToSession(code); err == nil && sess.SessionKey != "" {
			sessionKey = sess.SessionKey
			s.St.DB.Exec(`UPDATE users SET session_key=? WHERE id=?`, sessionKey, u.ID)
		}
	}
	env := xpay.EnvSandboxInt(sv.XPayEnv == 1)
	params, signData, err := xpay.BuildPayParams(s.XPay, xpay.SignData{
		OfferID:      s.Cfg.OfferID,
		BuyQuantity:  int(o.Quantity),
		Env:          env,
		CurrencyType: "CNY",
		ProductID:    sv.XPayProduct,
		GoodsPrice:   o.PriceCents,
		OutTradeNo:   o.OrderNo,
	}, sessionKey)
	if err != nil {
		failMsg(w, err.Error())
		return
	}
	snap, _ := json.Marshal(map[string]any{"sign_data": signData, "params": params, "at": time.Now().Unix()})
	s.St.DB.Exec(`UPDATE orders SET pay_snapshot=? WHERE id=?`, string(snap), o.ID)
	s.St.Event("order.pay_params", "order", o.ID, string(snap))
	okJSON(w, params)
}

// ---- POST /orders/:id/confirm-pay : client reports success; server
// verifies via xpay query_order before marking paid. ----

func (s *Server) handleOrderConfirmPay(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	o, err := s.App.OrderOwnedBy(id, u.ID)
	if err != nil {
		fail(w, http.StatusNotFound, "订单不存在")
		return
	}
	if o.Status != app.OrderPendingPay {
		okJSON(w, map[string]any{"order": s.toOrderJSON(o)})
		return
	}
	q, err := s.XPay.QueryOrder(o.OrderNo, xpay.EnvStr(s.Cfg.XPaySandbox))
	if err != nil {
		s.St.Event("order.confirm_pay_fail", "order", o.ID, fmt.Sprintf(`{"error":%q}`, err.Error()))
		// Do not trust the client report on query failure; the ticker
		// will reconcile later. Return current order state.
		fail(w, http.StatusBadGateway, "查单未确认，请稍后刷新订单状态")
		return
	}
	raw, _ := json.Marshal(q)
	s.St.DB.Exec(`UPDATE orders SET pay_snapshot=? WHERE id=?`, string(raw), o.ID)
	if !xpay.QueryOrderPaid(q) {
		failMsg(w, "微信侧订单未支付成功")
		return
	}
	payOrderID := xpay.Str(q, "pay_order_id")
	if err := s.App.MarkPaid(o.ID, payOrderID, xpay.Str(q, "transaction_id")); err != nil {
		failMsg(w, err.Error())
		return
	}
	o2, _ := s.App.OrderByID(o.ID)
	okJSON(w, map[string]any{"order": s.toOrderJSON(o2)})
}

// ---- POST /orders/:id/cancel ----

func (s *Server) handleOrderCancel(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	o, err := s.App.OrderOwnedBy(id, u.ID)
	if err != nil {
		fail(w, http.StatusNotFound, "订单不存在")
		return
	}
	if err := s.App.Cancel(o.ID, "user_cancel"); err != nil {
		if errors.Is(err, app.ErrConflict) {
			failMsg(w, "当前状态不可取消")
			return
		}
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	o2, _ := s.App.OrderByID(o.ID)
	okJSON(w, map[string]any{"order": s.toOrderJSON(o2)})
}

// ---- GET /orders?status= ----

func (s *Server) handleOrders(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	st := r.URL.Query().Get("status")
	limit := intQuery(r, "limit", 20)
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	q := `SELECT ` + app.OrderCols + ` FROM orders WHERE user_id=?`
	var args []any
	args = append(args, u.ID)
	switch st {
	case "pending":
		q += ` AND status=?`
		args = append(args, app.OrderPendingPay)
	case "contact":
		q += ` AND status=?`
		args = append(args, app.OrderContact)
	case "serving":
		q += ` AND status=?`
		args = append(args, app.OrderServing)
	case "done":
		q += ` AND status IN (?,?)`
		args = append(args, app.OrderDone, app.OrderRefunded)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.St.DB.Query(q, args...)
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	list := []orderJSON{}
	for rows.Next() {
		o, err := app.ScanOrder(rows)
		if err != nil {
			continue
		}
		list = append(list, s.toOrderJSON(o))
	}
	okJSON(w, map[string]any{"orders": list})
}

// ---- GET /orders/:id ----

func (s *Server) handleOrderDetail(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	o, err := s.App.OrderOwnedBy(id, u.ID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			fail(w, http.StatusNotFound, "订单不存在")
			return
		}
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	okJSON(w, map[string]any{"order": s.toOrderJSON(o)})
}
