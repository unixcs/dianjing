// Package app implements the dianjing domain logic: the order state
// machine, refund-mode engine and compensation ticker.
package app

import (
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"dianjing/backend/internal/store"
)

// Order status values (same semantics as the legacy system).
const (
	OrderPendingPay = 0 // 待支付
	OrderContact    = 1 // 待联系
	OrderServing    = 2 // 服务中
	OrderDone       = 3 // 已完成
	OrderCancelled  = 4 // 已取消
	OrderRefunding  = 5 // 退款中
	OrderRefunded   = 6 // 已退款
)

// Refund application modes, ported from Order::getServiceRefundMode:
// paid+not-contacted => auto (instant refund), serving => audit,
// done => not refundable.
const (
	RefundModeNone  = "none"
	RefundModeAuto  = "auto"
	RefundModeAudit = "audit"
)

// ErrConflict marks expected, user-facing state-machine violations.
var ErrConflict = errors.New("订单状态不允许该操作")

type Order struct {
	ID                 int64
	OrderNo            string
	UserID             int64
	ServiceID          int64
	TitleSnapshot      string
	CoverSnapshot      string
	PriceCents         int64
	Quantity           int64
	TotalCents         int64
	Status             int
	PayOrderID         string
	TransactionID      string
	PaySnapshot        string
	ProvideGoodsStatus string
	RiskStatus         int
	RiskSource         string
	CancelReason       string
	Contact            string
	PayTime            int64
	CompleteTime       int64
	CreatedAt          int64
	UpdatedAt          int64
}

const orderCols = `id,order_no,user_id,service_id,title_snapshot,cover_snapshot,price_cents,quantity,total_cents,
	status,pay_order_id,transaction_id,pay_snapshot,provide_goods_status,risk_status,risk_source,cancel_reason,
	contact,pay_time,complete_time,created_at,updated_at`

func scanOrder(row *sql.Row) (*Order, error) {
	var o Order
	err := row.Scan(&o.ID, &o.OrderNo, &o.UserID, &o.ServiceID, &o.TitleSnapshot, &o.CoverSnapshot,
		&o.PriceCents, &o.Quantity, &o.TotalCents, &o.Status, &o.PayOrderID, &o.TransactionID,
		&o.PaySnapshot, &o.ProvideGoodsStatus, &o.RiskStatus, &o.RiskSource, &o.CancelReason,
		&o.Contact, &o.PayTime, &o.CompleteTime, &o.CreatedAt, &o.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &o, nil
}

type Service struct {
	ID          int64
	CategoryID  int64
	Title       string
	Cover       string
	PriceCents  int64
	Content     string
	XPayProduct string
	XPayEnv     int
	Status      int
}

func (a *App) ServiceByID(id int64) (*Service, error) {
	var s Service
	err := a.St.DB.QueryRow(`SELECT id,category_id,title,cover,price_cents,content,xpay_product_id,xpay_env,status
		FROM services WHERE id=?`, id).
		Scan(&s.ID, &s.CategoryID, &s.Title, &s.Cover, &s.PriceCents, &s.Content, &s.XPayProduct, &s.XPayEnv, &s.Status)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// CreateOrder validates stock-free service purchase and inserts a
// pending-pay order. clientToken gives idempotency against double taps.
func (a *App) CreateOrder(userID, serviceID, quantity int64, clientToken, contact string) (*Order, error) {
	if quantity < 1 || quantity > 99 {
		return nil, fmt.Errorf("数量非法")
	}
	sv, err := a.ServiceByID(serviceID)
	if err != nil {
		return nil, fmt.Errorf("服务不存在或已下架")
	}
	if sv.Status != 1 {
		return nil, fmt.Errorf("服务已下架")
	}
	if sv.XPayProduct == "" {
		return nil, fmt.Errorf("该服务未配置虚拟支付商品，暂不可购买")
	}
	now := store.Now()
	total := sv.PriceCents * quantity
	orderNo := genOrderNo()
	res, err := a.St.DB.Exec(`INSERT INTO orders
		(order_no,client_token,user_id,service_id,title_snapshot,cover_snapshot,price_cents,quantity,total_cents,
		 status,contact,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		orderNo, clientToken, userID, sv.ID, sv.Title, sv.Cover, sv.PriceCents, quantity, total,
		OrderPendingPay, TruncateStr(contact, 100), now, now)
	if err != nil {
		// unique index on client_token => duplicate submit; the lookup is
		// scoped to this user so another user's token can never be replayed
		// into someone else's order.
		var existing Order
		if clientToken != "" && readOrderErr(a.St, userID, clientToken, &existing) == nil {
			return &existing, nil
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	a.St.Event("order.create", "order", id, fmt.Sprintf(`{"order_no":%q,"total_cents":%d,"contact":%q}`,
		orderNo, total, contact))
	return a.OrderByID(id)
}

func readOrderErr(s *store.Store, userID int64, clientToken string, out *Order) error {
	row := s.DB.QueryRow(`SELECT `+orderCols+` FROM orders WHERE client_token=? AND user_id=?`,
		clientToken, userID)
	o, err := scanOrder(row)
	if err == nil {
		*out = *o
	}
	return err
}

func (a *App) OrderByID(id int64) (*Order, error) {
	o, err := scanOrder(a.St.DB.QueryRow(`SELECT `+orderCols+` FROM orders WHERE id=?`, id))
	if err != nil {
		return nil, err
	}
	return o, nil
}

func (a *App) OrderByNo(no string) (*Order, error) {
	o, err := scanOrder(a.St.DB.QueryRow(`SELECT `+orderCols+` FROM orders WHERE order_no=?`, no))
	if err != nil {
		return nil, err
	}
	return o, nil
}

func (a *App) OrderOwnedBy(id, userID int64) (*Order, error) {
	o, err := a.OrderByID(id)
	if err != nil {
		return nil, err
	}
	if o.UserID != userID {
		return nil, sql.ErrNoRows
	}
	return o, nil
}

// Cancel cancels a pending-pay order (user or timeout).
func (a *App) Cancel(orderID int64, reason string) error {
	o, err := a.OrderByID(orderID)
	if err != nil {
		return err
	}
	if o.Status != OrderPendingPay {
		return ErrConflict
	}
	return a.transit(o, OrderCancelled, fmt.Sprintf(`{"cancel_reason":%q}`, reason))
}

// MarkPaid moves a confirmed-paid order into 待联系. Idempotent: calling
// twice or out of order is safe. A locally-cancelled order that WeChat
// actually charged (the cashier was still open when we timed out) is
// revived here — money received must be delivered, and the guarded
// status IN clause keeps it from clobbering any other transition.
func (a *App) MarkPaid(orderID int64, payOrderID, transactionID string) error {
	o, err := a.OrderByID(orderID)
	if err != nil {
		return err
	}
	if o.Status == OrderContact || o.Status == OrderServing || o.Status == OrderDone || o.Status == OrderRefunding {
		return nil // already converged
	}
	if o.Status != OrderPendingPay && o.Status != OrderCancelled {
		return ErrConflict
	}
	now := store.Now()
	res, err := a.St.DB.Exec(`UPDATE orders SET status=?, pay_order_id=?, transaction_id=?,
		pay_time=?, cancel_reason='', updated_at=? WHERE id=? AND status IN (?,?)`,
		OrderContact, payOrderID, transactionID, now, now, orderID, OrderPendingPay, OrderCancelled)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrConflict // raced with a concurrent transition
	}
	a.St.DB.Exec(`UPDATE services SET sales_actual=sales_actual+? WHERE id=?`, o.Quantity, o.ServiceID)
	a.St.Event("order.paid", "order", orderID,
		fmt.Sprintf(`{"pay_order_id":%q,"from_status":%d}`, payOrderID, o.Status))
	return nil
}

// AdminSetStatus lets the operator advance 待联系 → 服务中 → 已完成.
// The write carries the expected from-status so a concurrent paid/refund
// transition can never be silently overwritten (P0: unguarded UPDATE).
func (a *App) AdminSetStatus(orderID int64, target int) error {
	o, err := a.OrderByID(orderID)
	if err != nil {
		return err
	}
	if o.RiskStatus == RiskLocked {
		return fmt.Errorf("%w：订单已被风控锁定（Apple 退款问询处理中），暂不可变更状态", ErrConflict)
	}
	allowed := (target == OrderServing && o.Status == OrderContact) ||
		(target == OrderDone && o.Status == OrderServing) ||
		(target == OrderContact && o.Status == OrderServing)
	if !allowed || o.Status == target {
		return ErrConflict
	}
	now := store.Now()
	var res sql.Result
	if target == OrderDone {
		res, err = a.St.DB.Exec(`UPDATE orders SET status=?, complete_time=?, updated_at=? WHERE id=? AND status=?`,
			target, now, now, orderID, o.Status)
	} else {
		res, err = a.St.DB.Exec(`UPDATE orders SET status=?, updated_at=? WHERE id=? AND status=?`,
			target, now, orderID, o.Status)
	}
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrConflict
	}
	a.St.Event("order.status", "order", orderID, fmt.Sprintf(`{"from":%d,"to":%d}`, o.Status, target))
	return nil
}

// transit performs a guarded status transition: the UPDATE matches the
// status the caller read, so a racing write (e.g. MarkPaid winning over a
// timeout-cancel) fails loudly instead of clobbering it.
func (a *App) transit(o *Order, target int, eventJSON string) error {
	now := store.Now()
	res, err := a.St.DB.Exec(`UPDATE orders SET status=?, updated_at=? WHERE id=? AND status=?`,
		target, now, o.ID, o.Status)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrConflict
	}
	a.St.Event("order.transit", "order", o.ID, fmt.Sprintf(`{"from":%d,"to":%d}%s`, o.Status, target, eventJSON))
	return nil
}

// RefundModeFor returns the refund engine decision for an order,
// ported from Order::getServiceRefundMode.
func RefundModeFor(o *Order, activeRefundExists bool) string {
	if o.Status != OrderContact && o.Status != OrderServing {
		return RefundModeNone
	}
	if o.PayTime == 0 {
		return RefundModeNone
	}
	if activeRefundExists {
		return RefundModeNone
	}
	if o.Status == OrderContact {
		return RefundModeAuto
	}
	return RefundModeAudit // serving
}

// randIntn returns a cryptographically random value in [0,n).
func randIntn(n int) int {
	var b [4]byte
	rand.Read(b[:])
	v := int(binary.LittleEndian.Uint32(b[:])) % n
	if v < 0 {
		v = -v
	}
	return v
}

func genOrderNo() string {
	// 20-digit numeric out_trade_no compatible with xpay: microsecond ts +
	// 4 random digits (the random suffix avoids same-second collisions;
	// the UNIQUE index turns any residual collision into a retryable error).
	return fmt.Sprintf("%d%04d", time.Now().UnixNano()/1000, randIntn(10000))
}

func (a *App) HasActiveRefund(orderID int64) bool {
	var n int
	a.St.DB.QueryRow(`SELECT COUNT(*) FROM refunds WHERE order_id=? AND status IN ('applying','approved')`,
		orderID).Scan(&n)
	return n > 0
}
