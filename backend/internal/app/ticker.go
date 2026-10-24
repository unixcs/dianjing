package app

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"dianjing/backend/internal/store"
	"dianjing/backend/internal/xpay"
)

// Ticker compensates what pushes/clients may have missed, ported from
// the legacy per-60s systemd timers (app/timer/service/Order.php):
//  1. pending-pay orders older than closeHours  -> auto cancel
//  2. refunding orders                          -> query_order classify
//     (paid but unmarked -> MarkPaid; refunded 5/8 -> finalize)
//  3. approved non-iOS refunds not yet submitted-> xpay refund_order
//  4. paid orders without provide_goods success -> claim + dispatch
//     (skipped when risk-locked)
type Ticker struct {
	App        *App
	Interval   time.Duration
	CloseHours int64 // pending-pay auto-cancel age, default 2h
	Sandbox    bool
	stop       chan struct{}
}

func (t *Ticker) Start() {
	if t.Interval <= 0 {
		t.Interval = 60 * time.Second
	}
	if t.CloseHours <= 0 {
		t.CloseHours = 2
	}
	t.stop = make(chan struct{})
	go func() {
		// First tick after one interval so startup isn't blocked.
		for {
			select {
			case <-t.stop:
				return
			case <-time.After(t.Interval):
				t.runOnce()
			}
		}
	}()
}

func (t *Ticker) Stop() {
	if t.stop != nil {
		close(t.stop)
	}
}

func (t *Ticker) runOnce() {
	defer func() {
		if rec := recover(); rec != nil {
			t.App.St.Event("ticker.panic", "", 0, fmt.Sprintf("%v", rec))
		}
	}()
	t.closeStalePendingOrders()
	t.syncRefundingOrders()
	t.submitApprovedRefunds()
	t.dispatchProvideGoods()
}

// 1. auto-cancel 待支付 orders older than CloseHours (ported closeEvent).
func (t *Ticker) closeStalePendingOrders() {
	cutoff := store.Now() - t.CloseHours*3600
	rows, err := t.App.St.DB.Query(`SELECT `+OrderCols+` FROM orders
		WHERE status=? AND created_at < ? LIMIT 50`, OrderPendingPay, cutoff)
	if err != nil {
		return
	}
	orders := collectOrders(rows)
	for _, o := range orders {
		if err := t.App.Cancel(o.ID, "timeout_auto_close"); err == nil {
			t.App.St.Event("order.auto_cancel", "order", o.ID, fmt.Sprintf(`{"age_h":%d}`, t.CloseHours))
		}
	}
}

//  2. query_order reconciliation for orders in 退款中 or stuck pending-pay
//     (ported syncVirtualTradeStates + syncVirtualRefund).
func (t *Ticker) syncRefundingOrders() {
	rows, err := t.App.St.DB.Query(`SELECT `+OrderCols+` FROM orders
		WHERE status IN (?,?) ORDER BY id LIMIT 20`, OrderRefunding, OrderPendingPay)
	if err != nil {
		return
	}
	orders := collectOrders(rows)
	for _, o := range orders {
		// Cancelled locally? nothing to reconcile.
		if o.Status == OrderPendingPay && store.Now()-o.CreatedAt < 15*60 {
			continue // too young; confirm-pay flow handles fresh orders
		}
		u, err := t.App.St.UserByID(o.UserID)
		if err != nil || u.Openid == "" {
			continue
		}
		resp, err := t.App.XPay.QueryOrder(u.Openid, o.OrderNo, xpay.EnvStr(t.Sandbox))
		if err != nil {
			continue // network/wx error: try next tick
		}
		raw, _ := json.Marshal(resp)
		t.App.St.DB.Exec(`UPDATE orders SET pay_snapshot=? WHERE id=?`, string(raw), o.ID)
		st := xpay.QueryOrderStatus(resp)
		switch st {
		case xpay.VOrderPaid, xpay.VOrderPaidPending:
			if o.Status == OrderPendingPay {
				t.App.MarkPaid(o.ID, xpay.Str(resp, "wxpay_order_id"), "")
			}
		case xpay.VOrderRefunded, xpay.VOrderUserRefunded:
			if o.Status == OrderRefunding {
				if rr, err := t.App.LatestRefundForOrder(o.ID); err == nil {
					t.App.finalizeRefund(o, rr, "ticker_query")
				}
			}
		case xpay.VOrderClosed:
			if o.Status == OrderPendingPay {
				t.App.Cancel(o.ID, "wx_order_closed")
			}
		}
	}
}

// 3. approved refunds not yet sent to xpay (single-writer submit).
func (t *Ticker) submitApprovedRefunds() {
	rows, err := t.App.St.DB.Query(`SELECT id, order_id FROM refunds
		WHERE status=? AND xpay_submitted=0 AND ios_waiting=0 ORDER BY id LIMIT 20`, RefundApproved)
	if err != nil {
		return
	}
	type rid struct {
		id, orderID int64
	}
	var list []rid
	for rows.Next() {
		var r rid
		if err := rows.Scan(&r.id, &r.orderID); err != nil {
			continue
		}
		list = append(list, r)
	}
	rows.Close()
	for _, r := range list {
		o, err := t.App.OrderByID(r.orderID)
		if err != nil {
			continue
		}
		rr, err := t.App.RefundByID(r.id)
		if err != nil || rr.Status != RefundApproved {
			continue
		}
		t.App.SubmitRefundToXPay(o, rr)
	}
}

//  4. provide-goods dispatch for paid orders (ported syncVirtualProvideGoods):
//     待联系 → 服务中 transition implies service started; we treat 待联系
//     orders as delivered (contact info shown to user), dispatch once.
func (t *Ticker) dispatchProvideGoods() {
	rows, err := t.App.St.DB.Query(`SELECT `+OrderCols+` FROM orders
		WHERE status IN (?,?) AND pay_time > 0 AND provide_goods_status = '' ORDER BY id LIMIT 20`,
		OrderContact, OrderServing)
	if err != nil {
		return
	}
	orders := collectOrders(rows)
	for _, o := range orders {
		// Atomic claim: '' -> 'sending' guards against double dispatch.
		res, err := t.App.St.DB.Exec(`UPDATE orders SET provide_goods_status='sending', updated_at=?
			WHERE id=? AND provide_goods_status='' AND risk_status=?`, store.Now(), o.ID, RiskNone)
		if err != nil {
			continue
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue // raced or risk-locked
		}
		env := xpay.EnvSandboxInt(t.Sandbox)
		out, err := t.App.XPay.NotifyProvideGoods(env, o.OrderNo)
		status := "failed"
		if err == nil && intOf(out["errcode"]) == 0 {
			status = "success"
		}
		payload, _ := json.Marshal(map[string]any{"resp": out, "error": errString(err)})
		t.App.St.DB.Exec(`UPDATE orders SET provide_goods_status=?, pay_snapshot=? WHERE id=?`,
			status, string(payload), o.ID)
		t.App.St.Event("order.provide_goods", "order", o.ID,
			fmt.Sprintf(`{"status":%q,"order_no":%q}`, status, o.OrderNo))
	}
}

func collectOrders(rows *sql.Rows) []*Order {
	defer rows.Close()
	var out []*Order
	for rows.Next() {
		o, err := ScanOrder(rows)
		if err == nil {
			out = append(out, o)
		}
	}
	return out
}

func intOf(v any) int {
	if f, ok := v.(float64); ok {
		return int(f)
	}
	return -1
}

func errString(err error) string {
	if err != nil {
		return err.Error()
	}
	return ""
}
