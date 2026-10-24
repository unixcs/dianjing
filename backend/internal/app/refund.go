// Refund engine + iOS App Store refund risk control, ported from the
// legacy yoshop2.0 services:
//   - app/common/service/order/Refund.php  (apply / audit / converge)
//   - app/common/service/order/IosRefundRisk.php (inquiry decision, freeze)
//   - app/timer/service/Order.php (compensation ticker)
//
// The business is tiny (single operator), so the two-tier refund record
// of the legacy system collapses into the refunds table here, and the
// payment_trade snapshot becomes columns on orders/refunds.
package app

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"

	"dianjing/backend/internal/store"
	"dianjing/backend/internal/xpay"
)

// Refund record statuses (refunds.status) and audit flow modes.
const (
	RefundApplying = "applying" // 已提交待商户审核（audit 模式）
	RefundApproved = "approved" // 商户已同意，xpay 退款已发起/等待收口
	RefundRefunded = "refunded" // 退款完成
	RefundRejected = "rejected" // 商户驳回
	RefundFailed   = "failed"   // xpay 发起失败，可重试
)

// Apple inquiry service stages, ported from IosRefundRisk::serviceStage.
const (
	StageNotStarted = "NOT_STARTED" // 待联系：服务未开始
	StageInProgress = "IN_PROGRESS" // 服务中
	StageCompleted  = "COMPLETED"   // 已完成
	StageUnknown    = "UNKNOWN"
)

// Risk statuses (orders.risk_status): monotonic to LOCKED, then REFUNDED.
const (
	RiskNone     = 0
	RiskLocked   = 1 // Apple 问询命中：服务冻结，不再履约/发货
	RiskRefunded = 2 // App Store 退款成功收口
)

// ErrRefundUnavailable marks a refund request the engine refuses.
var ErrRefundUnavailable = fmt.Errorf("当前订单不支持退款")

// ---- fingerprint ----

// InquiryFingerprint hashes the normalized inquiry payload for dedup
// (ported from IosRefundRisk::fingerprint: recursive ksort + sha256).
func InquiryFingerprint(payload map[string]any) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(canonicalJSON(payload))))
}

func canonicalJSON(v any) string {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := "{"
		for i, k := range keys {
			if i > 0 {
				out += ","
			}
			out += fmt.Sprintf("%q:%s", k, canonicalJSON(t[k]))
		}
		return out + "}"
	case []any:
		out := "["
		for i, item := range t {
			if i > 0 {
				out += ","
			}
			out += canonicalJSON(item)
		}
		return out + "]"
	case string:
		b, _ := json.Marshal(t)
		return string(b)
	case float64:
		return strconv.FormatFloat(t, 'g', -1, 64)
	case bool:
		if t {
			return "true"
		}
		return "false"
	case nil:
		return "null"
	default:
		return fmt.Sprintf("%v", t)
	}
}

// ---- refund application (user side) ----

// ApplyRefund creates the refund record and drives the mode engine:
// auto (待联系) refunds instantly; audit (服务中) waits for the operator.
// Ported from Order::getServiceRefundMode + Refund::handle.
func (a *App) ApplyRefund(o *Order, reason string) (*RefundsRow, error) {
	if active := a.HasActiveRefund(o.ID); active {
		return nil, ErrRefundUnavailable
	}
	mode := RefundModeFor(o, false)
	if mode == RefundModeNone {
		return nil, ErrRefundUnavailable
	}
	status := RefundApplying
	if mode == RefundModeAuto {
		status = RefundApproved // auto: no merchant audit step
	}
	refundNo := genRefundNo()
	now := store.Now()
	res, err := a.St.DB.Exec(`INSERT INTO refunds
		(order_id,refund_no,amount_cents,reason,status,mode,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?)`,
		o.ID, refundNo, o.TotalCents, TruncateStr(reason, 200), status, mode, now, now)
	if err != nil {
		// idx_refunds_active may have rejected a concurrent second
		// application for this order.
		if a.HasActiveRefund(o.ID) {
			return nil, ErrRefundUnavailable
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	a.St.Event("refund.apply", "order", o.ID, fmt.Sprintf(`{"refund_no":%q,"mode":%q,"amount":%d}`, refundNo, mode, o.TotalCents))

	// Order enters 退款中 immediately; it never returns to serving. If the
	// transit loses a race (order just completed / cancelled), roll the
	// refund row back so no orphan active refund blocks future applies.
	if err := a.transit(o, OrderRefunding, fmt.Sprintf(`{"refund_no":%q}`, refundNo)); err != nil {
		a.St.DB.Exec(`DELETE FROM refunds WHERE id=? AND xpay_submitted=0`, id)
		return nil, err
	}

	rr, _ := a.RefundByID(id)
	return rr, nil
}

type RefundsRow struct {
	ID         int64
	OrderID    int64
	RefundNo   string
	Amount     int64
	Reason     string
	Status     string
	Mode       string
	AuditNote  string
	IOSWaiting bool
	Submitted  bool
	CreatedAt  int64
	UpdatedAt  int64
}

const refundCols = `id,order_id,refund_no,amount_cents,reason,status,mode,audit_note,ios_waiting,xpay_submitted,created_at,updated_at`

func scanRefund(row *sql.Row) (*RefundsRow, error) {
	var r RefundsRow
	err := row.Scan(&r.ID, &r.OrderID, &r.RefundNo, &r.Amount, &r.Reason, &r.Status, &r.Mode,
		&r.AuditNote, &r.IOSWaiting, &r.Submitted, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (a *App) RefundByID(id int64) (*RefundsRow, error) {
	return scanRefund(a.St.DB.QueryRow(`SELECT `+refundCols+` FROM refunds WHERE id=?`, id))
}

func (a *App) RefundByNo(no string) (*RefundsRow, error) {
	return scanRefund(a.St.DB.QueryRow(`SELECT `+refundCols+` FROM refunds WHERE refund_no=?`, no))
}

func (a *App) LatestRefundForOrder(orderID int64) (*RefundsRow, error) {
	return scanRefund(a.St.DB.QueryRow(`SELECT `+refundCols+` FROM refunds
		WHERE order_id=? ORDER BY id DESC LIMIT 1`, orderID))
}

// AuditRefund is the operator's approve/reject decision on an audit-mode
// refund. Approve marks it approved; the ticker performs the xpay call
// (single writer, same as the legacy compensation design). Auto-mode iOS
// refunds (created by an Apple inquiry on a serving order) are auditable
// here too — that is the only manual lever for that orphan case.
func (a *App) AuditRefund(refundID int64, approve bool, note string) error {
	rr, err := a.RefundByID(refundID)
	if err != nil {
		return err
	}
	auditable := rr.Mode == RefundModeAudit || (rr.Mode == RefundModeAuto && rr.IOSWaiting)
	if rr.Status != RefundApplying || !auditable {
		return ErrConflict
	}
	target := RefundRejected
	if approve {
		target = RefundApproved
	}
	now := store.Now()
	res, err := a.St.DB.Exec(`UPDATE refunds SET status=?, audit_note=?, updated_at=? WHERE id=? AND status=?`,
		target, TruncateStr(note, 200), now, refundID, RefundApplying)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrConflict // raced with another audit/ticker write
	}
	if !approve {
		// Re-open the order (only audit-mode refunds came from 服务中;
		// iOS auto refunds stay 退款中 pending the App Store outcome).
		if rr.Mode == RefundModeAudit {
			a.St.DB.Exec(`UPDATE orders SET status=?, updated_at=? WHERE id=? AND status=?`,
				OrderServing, now, rr.OrderID, OrderRefunding)
		}
	}
	a.St.Event("refund.audit", "refund", refundID,
		fmt.Sprintf(`{"approve":%v,"note":%q,"order_id":%d}`, approve, note, rr.OrderID))
	return nil
}

// ---- iOS inquiry handling (push side) ----

// HandleIOSInquiry processes one verified Apple refund inquiry push and
// returns the decision reply. Ported from IosRefundRisk::handleInquiry,
// simplified to the single-store schema. result_code 0 = 建议退款,
// 1 = 建议拒绝. Any error path must fail closed (suggest reject).
func (a *App) HandleIOSInquiry(payOrderID string, payload map[string]any) (resultCode int, resultInfo, evidence string) {
	fingerprint := InquiryFingerprint(payload)
	reason := inquiryReason(payload)

	// Dedup on fingerprint: identical replays reuse the stored decision.
	var prevCode int
	var prevInfo, prevEvidence string
	err := a.St.DB.QueryRow(`SELECT result_code, suggestion, evidence FROM refund_inquiries
		WHERE fingerprint=?`, fingerprint).Scan(&prevCode, &prevInfo, &prevEvidence)
	if err == nil {
		return prevCode, prevInfo, prevEvidence
	}
	o, err := a.OrderByNo(payOrderID)
	if err != nil {
		a.insertInquiry(0, payOrderID, fingerprint, "TRADE_NOT_FOUND", reason, payload, StageUnknown, 1)
		return 1, "建议拒绝退款", "交易或订单绑定校验未通过(TRADE_NOT_FOUND)，无法确认退款申请属于当前有效服务订单"
	}
	if o.RiskStatus == RiskLocked || o.RiskStatus == RiskRefunded {
		// Already frozen/refunded: keep the original safe rejection.
		stage := serviceStage(o)
		a.insertInquiry(o.ID, payOrderID, fingerprint, "BOUND", reason, payload, stage, 1)
		return 1, "建议拒绝退款", "服务已冻结或已退款，原订单不会恢复履约"
	}

	stage := serviceStage(o)
	auditStatus := "" // "", applying, approved, rejected
	if rr, err := a.LatestRefundForOrder(o.ID); err == nil {
		auditStatus = rr.Status
	}
	// Ported decision(): only suggest refund when service hasn't started
	// (or merchant already approved while serving).
	code := 1
	switch stage {
	case StageNotStarted:
		if auditStatus == "" || auditStatus == RefundApproved || auditStatus == RefundRefunded {
			code = 0
		}
	case StageInProgress:
		if auditStatus == RefundApproved || auditStatus == RefundRefunded {
			code = 0
		}
	}

	// Freeze the order (monotonic to LOCKED): no more provide-goods or
	// service continuation once Apple asked about a refund.
	now := store.Now()
	a.St.DB.Exec(`UPDATE orders SET risk_status=?, risk_source=?, updated_at=? WHERE id=? AND risk_status=?`,
		RiskLocked, "apple_inquiry", now, o.ID, RiskNone)
	o, _ = a.OrderByID(o.ID)

	// Track the Apple inquiry with a refund record. A rejected/failed
	// history must not block a fresh Apple-side request — without this,
	// an inquiry on a previously-rejected order creates no tracking row
	// and the App Store outcome can never converge.
	needTrack := auditStatus == "" || auditStatus == RefundRejected || auditStatus == RefundFailed
	if needTrack {
		initStatus := RefundApplying
		if stage == StageNotStarted {
			initStatus = RefundApproved
		}
		a.St.DB.Exec(`INSERT INTO refunds (order_id,refund_no,amount_cents,reason,status,mode,ios_waiting,created_at,updated_at)
			VALUES (?,?,?,?,?,?,?,?,?)`,
			o.ID, genRefundNo(), o.TotalCents, FirstNonEmpty(reason, "App Store退款申请"),
			initStatus, "auto", 1, now, now)
	} else if auditStatus == RefundApplying || auditStatus == RefundApproved {
		a.St.DB.Exec(`UPDATE refunds SET ios_waiting=1, updated_at=? WHERE order_id=? AND status IN ('applying','approved')`,
			now, o.ID)
	}

	info := "建议拒绝退款"
	if code == 0 {
		info = "建议退款"
	}
	ev := fmt.Sprintf("服务状态=%s；建议=%s", stage, map[bool]string{true: "退款", false: "拒绝退款"}[code == 0])
	if reason != "" {
		ev += "；用户原因=" + TruncateStr(reason, 200)
	}
	a.insertInquiry(o.ID, payOrderID, fingerprint, "BOUND", reason, payload, stage, code)
	return code, info, ev
}

func (a *App) insertInquiry(orderID int64, payOrderID, fingerprint, binding, reason string, payload map[string]any, stage string, code int) {
	raw, _ := json.Marshal(payload)
	a.St.DB.Exec(`INSERT OR IGNORE INTO refund_inquiries
		(order_id,pay_order_id,fingerprint,binding_status,request_reason,service_stage,result_code,suggestion,raw,created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`,
		orderID, payOrderID, fingerprint, binding, reason, stage, code,
		map[bool]string{true: "建议退款", false: "建议拒绝退款"}[code == 0], string(raw), store.Now())
	a.St.Event("refund.inquiry", "order", orderID,
		fmt.Sprintf(`{"pay_order_id":%q,"binding":%q,"result_code":%d}`, payOrderID, binding, code))
}

func serviceStage(o *Order) string {
	switch o.Status {
	case OrderContact:
		return StageNotStarted
	case OrderServing:
		return StageInProgress
	case OrderDone:
		return StageCompleted
	default:
		return StageUnknown
	}
}

func inquiryReason(payload map[string]any) string {
	for _, k := range []string{"refund_request_reason", "RefundRequestReason", "reason", "Reason"} {
		if s, ok := payload[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func FirstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func TruncateStr(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// ---- refund convergence ----

// SubmitRefundToXPay sends an approved non-iOS refund to xpay
// refund_order. Returns whether the call succeeded. Ported from
// Refund::virtualPaymentRefund (payload shape preserved).
func (a *App) SubmitRefundToXPay(o *Order, rr *RefundsRow) error {
	u, err := a.St.UserByID(o.UserID)
	if err != nil || u.Openid == "" {
		return fmt.Errorf("缺少用户 openid，无法执行虚拟支付退款")
	}
	if o.RiskStatus == RiskLocked {
		// iOS channel: developer refund impossible, wait for App Store.
		a.St.DB.Exec(`UPDATE refunds SET ios_waiting=1, updated_at=? WHERE id=?`, store.Now(), rr.ID)
		return nil
	}
	refundFee := rr.Amount
	if refundFee <= 0 {
		refundFee = o.TotalCents
	}
	refundNo := rr.RefundNo
	if len(refundNo) < 6 || refundNo[:3] != "vrf" {
		refundNo = "vrf" + refundNo
	}
	out, err := a.XPay.RefundOrder(u.Openid, o.OrderNo, refundNo, o.TotalCents, refundFee, o.OrderNo, xpay.EnvStr(a.XPay.Sandbox))
	if err != nil {
		a.St.DB.Exec(`UPDATE refunds SET status=?, updated_at=? WHERE id=? AND status=?`,
			RefundFailed, store.Now(), rr.ID, RefundApproved)
		a.St.Event("refund.submit_fail", "refund", rr.ID, fmt.Sprintf(`{"error":%q}`, err.Error()))
		return err
	}
	raw, _ := json.Marshal(out)
	a.St.DB.Exec(`UPDATE refunds SET xpay_submitted=1, updated_at=? WHERE id=?`, store.Now(), rr.ID)
	a.St.Event("refund.submit", "refund", rr.ID, fmt.Sprintf(`{"refund_no":%q,"resp":%s}`, refundNo, raw))
	return nil
}

// finalizeRefund converges a refund to completed: refund → refunded and
// order → 6已退款. Both updates are idempotent and independently guarded,
// so an interrupted run (crash between them) always converges on retry —
// the old early-return-on-refunded left the order stuck at 退款中 forever.
func (a *App) finalizeRefund(o *Order, rr *RefundsRow, via string) error {
	now := store.Now()
	a.St.DB.Exec(`UPDATE refunds SET status=?, updated_at=? WHERE id=? AND status IN ('applying','approved','failed')`,
		RefundRefunded, now, rr.ID)
	// Money is back with the user: any paid/live order state collapses to
	// 已退款 (never from 待支付/已取消, which cannot carry a real refund).
	res, err := a.St.DB.Exec(`UPDATE orders SET status=?, updated_at=? WHERE id=? AND status IN (?,?,?,?,?)`,
		OrderRefunded, now, o.ID, OrderContact, OrderServing, OrderDone, OrderRefunding, OrderRefunded)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		a.St.Event("order.refunded", "order", o.ID, fmt.Sprintf(`{"via":%q,"refund_no":%q}`, via, rr.RefundNo))
	}
	return nil
}

// RejectRefundByMerchant re-opens the order when the operator rejects an
// audit-mode refund (order leaves 退款中 back to 服务中).
func (a *App) RejectRefundByMerchant(rr *RefundsRow) {
	if rr.Mode != RefundModeAudit || rr.Status != RefundRejected {
		return
	}
	if o, err := a.OrderByID(rr.OrderID); err == nil && o.Status == OrderRefunding {
		a.St.DB.Exec(`UPDATE orders SET status=?, updated_at=? WHERE id=? AND status=?`,
			OrderServing, store.Now(), o.ID, OrderRefunding)
		a.St.Event("order.refund_rejected", "order", o.ID, fmt.Sprintf(`{"refund_no":%q}`, rr.RefundNo))
	}
}

// ---- iOS refund push convergence ----

// MarkRefundedFromPush converges order+refund when the App Store refund
// succeeded (xpay_refund_notify or inquiry-side recovery).
func (a *App) MarkRefundedFromPush(orderNo string) bool {
	o, err := a.OrderByNo(orderNo)
	if err != nil {
		return false
	}
	now := store.Now()
	if o.RiskStatus == RiskNone {
		a.St.DB.Exec(`UPDATE orders SET risk_status=?, risk_source=?, updated_at=? WHERE id=?`,
			RiskRefunded, "refund_notify", now, o.ID)
	}
	if rr, err := a.LatestRefundForOrder(o.ID); err == nil {
		a.finalizeRefund(o, rr, "ios_push")
	} else {
		if o.Status != OrderRefunded {
			a.St.DB.Exec(`UPDATE orders SET status=?, updated_at=? WHERE id=?`, OrderRefunded, now, o.ID)
			a.St.Event("order.refunded", "order", o.ID, `{"via":"ios_push"}`)
		}
	}
	return true
}

func genRefundNo() string {
	// Random suffix avoids same-second collisions on the UNIQUE refund_no.
	return "vrf" + fmt.Sprintf("%d%04d", store.Now(), randIntn(10000))
}
