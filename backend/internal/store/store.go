// Package store owns the SQLite database: connection setup, schema
// migrations and small data-access helpers shared by handlers.
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

type Store struct {
	DB *sql.DB
}

// Open opens (creating if needed) the sqlite database at path with WAL
// journaling and busy timeout suitable for a single-process server.
func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	dsn := "file:" + filepath.Join(dataDir, "dianjing.db") +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// modernc/sqlite serializes writes internally; a small pool is plenty.
	db.SetMaxOpenConns(4)
	s := &Store{DB: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.DB.Close() }

func (s *Store) migrate() error {
	for _, stmt := range schema {
		if _, err := s.DB.Exec(stmt); err != nil {
			return fmt.Errorf("migrate: %w\nstmt: %s", err, stmt)
		}
	}
	// Column additions for databases created before the column existed
	// (CREATE TABLE IF NOT EXISTS never touches existing tables).
	s.addColumn("orders", "contact", "TEXT NOT NULL DEFAULT ''")
	return nil
}

func (s *Store) addColumn(table, col, def string) {
	q := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, col, def)
	if _, err := s.DB.Exec(q); err != nil && !strings.Contains(err.Error(), "duplicate column") {
		fmt.Printf("store: addColumn %s.%s: %v\n", table, col, err)
	}
}

var schema = []string{
	`CREATE TABLE IF NOT EXISTS users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		openid TEXT NOT NULL UNIQUE,
		session_key TEXT NOT NULL DEFAULT '',
		nickname TEXT NOT NULL DEFAULT '',
		avatar TEXT NOT NULL DEFAULT '',
		mobile TEXT NOT NULL DEFAULT '',
		status INTEGER NOT NULL DEFAULT 1,
		created_at INTEGER NOT NULL,
		last_login_at INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE TABLE IF NOT EXISTS categories (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		sort INTEGER NOT NULL DEFAULT 100,
		status INTEGER NOT NULL DEFAULT 1
	)`,
	`CREATE TABLE IF NOT EXISTS services (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		category_id INTEGER NOT NULL DEFAULT 0,
		title TEXT NOT NULL,
		cover TEXT NOT NULL DEFAULT '',
		price_cents INTEGER NOT NULL DEFAULT 0,
		sales_base INTEGER NOT NULL DEFAULT 0,
		sales_actual INTEGER NOT NULL DEFAULT 0,
		content TEXT NOT NULL DEFAULT '',
		xpay_product_id TEXT NOT NULL DEFAULT '',
		xpay_env INTEGER NOT NULL DEFAULT 0,
		status INTEGER NOT NULL DEFAULT 1,
		sort INTEGER NOT NULL DEFAULT 100,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS orders (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		order_no TEXT NOT NULL UNIQUE,
		client_token TEXT NOT NULL DEFAULT '',
		user_id INTEGER NOT NULL,
		service_id INTEGER NOT NULL,
		title_snapshot TEXT NOT NULL,
		cover_snapshot TEXT NOT NULL DEFAULT '',
		price_cents INTEGER NOT NULL,
		quantity INTEGER NOT NULL DEFAULT 1,
		total_cents INTEGER NOT NULL,
		status INTEGER NOT NULL DEFAULT 0, -- 0待支付 1待联系 2服务中 3已完成 4已取消 5退款中 6已退款
		pay_order_id TEXT NOT NULL DEFAULT '',
		transaction_id TEXT NOT NULL DEFAULT '',
		pay_snapshot TEXT NOT NULL DEFAULT '',
		provide_goods_status TEXT NOT NULL DEFAULT '', -- sending/success/failed/locked
		risk_status INTEGER NOT NULL DEFAULT 0,        -- 0无 1命中锁定 2已放行
		risk_source TEXT NOT NULL DEFAULT '',
		cancel_reason TEXT NOT NULL DEFAULT '',
		contact TEXT NOT NULL DEFAULT '', -- 用户下单联系方式（微信/手机号）
		pay_time INTEGER NOT NULL DEFAULT 0,
		complete_time INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_orders_client_token ON orders(client_token)`,
	`CREATE INDEX IF NOT EXISTS idx_orders_user ON orders(user_id, status, created_at DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_orders_status ON orders(status, id)`,
	`CREATE TABLE IF NOT EXISTS refunds (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		order_id INTEGER NOT NULL,
		refund_no TEXT NOT NULL UNIQUE,
		amount_cents INTEGER NOT NULL,
		reason TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL, -- applying/approved/refunded/rejected/failed
		mode TEXT NOT NULL,   -- auto/audit
		audit_note TEXT NOT NULL DEFAULT '',
		ios_waiting INTEGER NOT NULL DEFAULT 0,     -- iOS 渠道：等待 App Store 退款
		xpay_submitted INTEGER NOT NULL DEFAULT 0,  -- 已调用 xpay refund_order
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_refunds_order ON refunds(order_id)`,
	// One active (applying/approved) refund per order — DB-level backstop
	// against double-refund races in ApplyRefund.
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_refunds_active ON refunds(order_id) WHERE status IN ('applying','approved')`,
	`CREATE TABLE IF NOT EXISTS refund_inquiries (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		order_id INTEGER NOT NULL DEFAULT 0,
		pay_order_id TEXT NOT NULL DEFAULT '',
		fingerprint TEXT NOT NULL UNIQUE,
		binding_status TEXT NOT NULL DEFAULT '',
		request_reason TEXT NOT NULL DEFAULT '',
		service_stage TEXT NOT NULL DEFAULT '',
		result_code INTEGER NOT NULL DEFAULT 1,
		suggestion TEXT NOT NULL DEFAULT '',
		evidence TEXT NOT NULL DEFAULT '',
		raw TEXT NOT NULL,
		created_at INTEGER NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS feedbacks (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER NOT NULL,
		type TEXT NOT NULL DEFAULT 'complaint',
		content TEXT NOT NULL,
		images TEXT NOT NULL DEFAULT '',
		reply TEXT NOT NULL DEFAULT '',
		status INTEGER NOT NULL DEFAULT 0, -- 0待处理 1已回复
		created_at INTEGER NOT NULL,
		replied_at INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE TABLE IF NOT EXISTS settings (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		kind TEXT NOT NULL,
		ref_type TEXT NOT NULL DEFAULT '',
		ref_id INTEGER NOT NULL DEFAULT 0,
		payload TEXT NOT NULL,
		created_at INTEGER NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_events_ref ON events(kind, ref_id)`,
}

// Event records an audit event. Failures are swallowed: auditing must
// never break the request path.
func (s *Store) Event(kind, refType string, refID int64, payload string) {
	s.DB.Exec(`INSERT INTO events (kind, ref_type, ref_id, payload, created_at)
		VALUES (?,?,?,?,?)`, kind, refType, refID, payload, Now())
}

func Now() int64 { return timeNow() }
