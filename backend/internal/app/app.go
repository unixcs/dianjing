package app

import (
	"database/sql"

	"dianjing/backend/internal/store"
	"dianjing/backend/internal/xpay"
)

type App struct {
	St   *store.Store
	XPay *xpay.Client
}

// OrderCols exposes the canonical order column list for ad-hoc queries.
const OrderCols = `id,order_no,user_id,service_id,title_snapshot,cover_snapshot,price_cents,quantity,total_cents,
	status,pay_order_id,transaction_id,pay_snapshot,provide_goods_status,risk_status,risk_source,cancel_reason,
	pay_time,complete_time,created_at,updated_at`

type RowScanner interface{ Scan(dest ...any) error }

// ScanOrder scans one order row from a *sql.Rows.
func ScanOrder(rs RowScanner) (*Order, error) {
	var o Order
	err := rs.Scan(&o.ID, &o.OrderNo, &o.UserID, &o.ServiceID, &o.TitleSnapshot, &o.CoverSnapshot,
		&o.PriceCents, &o.Quantity, &o.TotalCents, &o.Status, &o.PayOrderID, &o.TransactionID,
		&o.PaySnapshot, &o.ProvideGoodsStatus, &o.RiskStatus, &o.RiskSource, &o.CancelReason,
		&o.PayTime, &o.CompleteTime, &o.CreatedAt, &o.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &o, nil
}

var _ = sql.ErrNoRows
