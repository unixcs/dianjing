// migrate-yoshop: one-shot MySQL (YoShop) → SQLite (dianjing) importer.
//
// Idempotent: rows are keyed by their legacy IDs into the same autoincrement
// PKs, so re-running overwrites in place (INSERT OR REPLACE). Run it once
// before switching traffic and again right before cutover for the delta.
//
// Source of truth for legacy enums:
//
//	order: pay_status 10= unpaid / 20= paid; order_status 10=normal 20=cancelled;
//	       delivery_status 10=not 20=delivered; receipt_status 10/20
//	refund: audit_status 0=wait 10=reviewed 20=rejected; status 0/10/20/30
//	goods: status 10=on sale 20=off sale; is_delete soft delete
package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "modernc.org/sqlite"
)

type opts struct {
	dsn       string
	dataDir   string
	uploads   string
	baseURL   string
	storeID   int
	skipFiles bool
}

func main() {
	var o opts
	flag.StringVar(&o.dsn, "dsn", "", "MySQL DSN, e.g. user:pass@tcp(127.0.0.1:3306)/yoshop2_prod")
	flag.StringVar(&o.dataDir, "data", "./data", "new backend data dir (holds dianjing.db)")
	flag.StringVar(&o.uploads, "uploads", "/srv/yoshop/shared/uploads", "legacy uploads root")
	flag.StringVar(&o.baseURL, "base-url", "", "public base URL for image paths (defaults to setting)")
	flag.IntVar(&o.storeID, "store", 10001, "legacy store_id to import")
	flag.BoolVar(&o.skipFiles, "skip-files", false, "do not copy uploads")
	flag.Parse()
	if o.dsn == "" {
		fmt.Fprintln(os.Stderr, "usage: migrate-yoshop -dsn 'user:pass@tcp(127.0.0.1:3306)/yoshop2_prod' [-data ./data] [-uploads /srv/yoshop/shared/uploads]")
		os.Exit(2)
	}
	if err := run(o); err != nil {
		log.Fatalf("migrate: %v", err)
	}
}

func run(o opts) error {
	my, err := sql.Open("mysql", o.dsn+"?charset=utf8mb4&parseTime=false")
	if err != nil {
		return err
	}
	defer my.Close()
	if err := my.Ping(); err != nil {
		return fmt.Errorf("mysql ping: %w", err)
	}

	sq, err := sql.Open("sqlite", "file:"+filepath.Join(o.dataDir, "dianjing.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	defer sq.Close()
	if err := bootstrapSchema(sq); err != nil {
		return err
	}

	tx, err := sq.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	st := &mig{my: my, sq: tx, o: o}
	if err := st.users(); err != nil {
		return fmt.Errorf("users: %w", err)
	}
	if err := st.categories(); err != nil {
		return fmt.Errorf("categories: %w", err)
	}
	if err := st.services(); err != nil {
		return fmt.Errorf("services: %w", err)
	}
	if err := st.orders(); err != nil {
		return fmt.Errorf("orders: %w", err)
	}
	if err := st.refunds(); err != nil {
		return fmt.Errorf("refunds: %w", err)
	}
	if err := st.inquiries(); err != nil {
		return fmt.Errorf("inquiries: %w", err)
	}
	if err := st.settings(); err != nil {
		return fmt.Errorf("settings: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if !o.skipFiles {
		if err := st.copyUploads(); err != nil {
			return fmt.Errorf("uploads: %w", err)
		}
	}
	st.report()
	return nil
}

// ---- helpers ----

type mig struct {
	my     *sql.DB
	sq     *sql.Tx
	o      opts
	counts map[string]int
}

func (m *mig) add(name string, n int) {
	if m.counts == nil {
		m.counts = map[string]int{}
	}
	m.counts[name] += n
}

// qx runs a query that must return exactly one row per iteration via fn.
func (m *mig) qx(q string, args ...any) (*sql.Rows, error) {
	return m.my.Query(q, args...)
}

// ---- schema bootstrap: mirror store.migrate() minimally so the script
// can run against a fresh data dir without the server binary. ----

func bootstrapSchema(db *sql.DB) error {
	stmts := []string{
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
			status INTEGER NOT NULL DEFAULT 0,
			pay_order_id TEXT NOT NULL DEFAULT '',
			transaction_id TEXT NOT NULL DEFAULT '',
			pay_snapshot TEXT NOT NULL DEFAULT '',
			provide_goods_status TEXT NOT NULL DEFAULT '',
			risk_status INTEGER NOT NULL DEFAULT 0,
			risk_source TEXT NOT NULL DEFAULT '',
			cancel_reason TEXT NOT NULL DEFAULT '',
			pay_time INTEGER NOT NULL DEFAULT 0,
			complete_time INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_orders_client_token ON orders(client_token)`,
		`CREATE TABLE IF NOT EXISTS refunds (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			order_id INTEGER NOT NULL,
			refund_no TEXT NOT NULL UNIQUE,
			amount_cents INTEGER NOT NULL,
			reason TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL,
			mode TEXT NOT NULL,
			audit_note TEXT NOT NULL DEFAULT '',
			ios_waiting INTEGER NOT NULL DEFAULT 0,
			xpay_submitted INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
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
			status INTEGER NOT NULL DEFAULT 0,
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
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return fmt.Errorf("%w\nstmt: %s", err, s)
		}
	}
	return nil
}

// ---- importers ----

func (m *mig) users() error {
	rows, err := m.qx(`SELECT u.user_id, u.nick_name, u.mobile, u.create_time,
		COALESCE(o.oauth_id,''), COALESCE(o.session_key,'')
		FROM yoshop_user u
		LEFT JOIN yoshop_user_oauth o ON o.user_id=u.user_id AND o.oauth_type='MP-WEIXIN' AND o.is_delete=0
		WHERE u.is_delete=0 AND u.store_id=?`, m.o.storeID)
	if err != nil {
		return err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var id int64
		var nick, mobile, openid, sessKey string
		var createdAt sql.NullInt64
		if err := rows.Scan(&id, &nick, &mobile, &createdAt, &openid, &sessKey); err != nil {
			return err
		}
		if openid == "" {
			log.Printf("users: skip user %d (%s): no MP-WEIXIN openid", id, nick)
			continue
		}
		if _, err := m.sq.Exec(`INSERT OR REPLACE INTO users (id,openid,session_key,nickname,avatar,mobile,status,created_at,last_login_at)
			VALUES (?,?,?,?,?,?,1,?,0)`, id, openid, sessKey, nick, "", mobile, createdAt.Int64); err != nil {
			return err
		}
		n++
	}
	m.add("users", n)
	return rows.Err()
}

func (m *mig) categories() error {
	rows, err := m.qx(`SELECT category_id, name, sort, status FROM yoshop_category
		WHERE store_id=? AND parent_id=0 ORDER BY sort, category_id`, m.o.storeID)
	if err != nil {
		return err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var id, sort int64
		var name string
		var status int64
		if err := rows.Scan(&id, &name, &sort, &status); err != nil {
			return err
		}
		// legacy category.status: 1=显示 0=隐藏 — same semantics as new.
		if _, err := m.sq.Exec(`INSERT OR REPLACE INTO categories (id,name,sort,status) VALUES (?,?,?,?)`,
			id, name, sort, status); err != nil {
			return err
		}
		n++
	}
	m.add("categories", n)
	return rows.Err()
}

func mapLegacyOnOff(v int64) int64 {
	if v == 10 {
		return 1
	}
	return 0
}

// legacyImagePath rewrites "10001/20260715/x.jpg" to "/uploads/x.jpg"
// (basename is unique in the legacy store, verified by the migrator).
func legacyImagePath(p string) string {
	if p == "" {
		return ""
	}
	i := strings.LastIndex(p, "/")
	if i < 0 {
		return "/uploads/" + p
	}
	return "/uploads/" + p[i+1:]
}

func (m *mig) services() error {
	// cover = first goods image; category = first rel (prefer 10001).
	rows, err := m.qx(`SELECT g.goods_id, g.goods_name, g.goods_price_min, g.content,
		g.sales_initial, g.sales_actual, g.vp_enabled, g.vp_product_id, g.vp_price_snapshot,
		g.status, g.sort, g.create_time, g.update_time,
		(SELECT f.file_path FROM yoshop_goods_image gi JOIN yoshop_upload_file f ON f.file_id=gi.image_id
			WHERE gi.goods_id=g.goods_id ORDER BY gi.id LIMIT 1)
		FROM yoshop_goods g
		WHERE g.is_delete=0 AND g.store_id=?`, m.o.storeID)
	if err != nil {
		return err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var id int64
		var name, content string
		var priceMin float64
		var salesInit, salesActual int64
		var vpEnabled, status, sort int64
		var vpProduct string
		var vpPrice sql.NullInt64
		var createdAt, updatedAt sql.NullInt64
		var coverPath sql.NullString
		if err := rows.Scan(&id, &name, &priceMin, &content, &salesInit, &salesActual,
			&vpEnabled, &vpProduct, &vpPrice, &status, &sort, &createdAt, &updatedAt, &coverPath); err != nil {
			return err
		}
		catID := int64(0)
		m.my.QueryRow(`SELECT category_id FROM yoshop_goods_category_rel WHERE goods_id=? ORDER BY id LIMIT 1`, id).Scan(&catID)
		priceCents := int64(priceMin*100 + 0.5)
		// vp_price_snapshot is the xpay-registered price in cents; when the
		// storefront price diverges from it the WeChat-side price wins.
		if vpEnabled == 1 && vpPrice.Valid && vpPrice.Int64 > 0 {
			priceCents = vpPrice.Int64
		}
		xpayEnv := int64(0)
		if _, err := m.sq.Exec(`INSERT OR REPLACE INTO services
			(id,category_id,title,cover,price_cents,sales_base,sales_actual,content,xpay_product_id,xpay_env,status,sort,created_at,updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			id, catID, name, legacyImagePath(coverPath.String), priceCents,
			salesInit, salesInit+salesActual, stripHTML(content), vpProduct, xpayEnv,
			mapLegacyOnOff(status), sort, createdAt.Int64, updatedAt.Int64); err != nil {
			return err
		}
		n++
	}
	m.add("services", n)
	return rows.Err()
}

// stripHTML removes legacy rich-text markup, keeping the visible text.
func stripHTML(s string) string {
	if s == "" {
		return ""
	}
	// YoShop stores HTML entity-escaped (so tags arrive as &lt;p&gt;).
	// Decode the common entities first; the generic tag-stripper below
	// then removes whatever remains (&lt;span style=...&gt; etc.).
	r := strings.NewReplacer("&lt;", "<", "&gt;", ">", "&quot;", `"`,
		"&amp;nbsp;", " ", "&nbsp;", " ", "&amp;amp;", "&")
	s = r.Replace(s)
	var b strings.Builder
	depth := 0
	for _, c := range s {
		switch {
		case c == '<':
			depth++
		case c == '>':
			if depth > 0 {
				depth--
			}
		case depth == 0:
			b.WriteRune(c)
		}
	}
	out := b.String()
	// collapse blank lines
	lines := strings.Split(out, "\n")
	var keep []string
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln != "" {
			keep = append(keep, ln)
		}
	}
	return strings.Join(keep, "\n")
}

func (m *mig) orders() error {
	rows, err := m.qx(`SELECT o.order_id, o.order_no, o.user_id,
		og.goods_id, og.goods_name, og.goods_price, og.total_num,
		o.pay_price, o.pay_status, o.order_status, o.delivery_status, o.receipt_status,
		COALESCE(t.trade_no,''), o.pay_time, o.create_time, o.update_time,
		COALESCE(t.payload_snapshot,''), o.ios_refund_risk_status, o.ios_refund_risk_source
		FROM yoshop_order o
		JOIN yoshop_order_goods og ON og.order_id=o.order_id
		LEFT JOIN yoshop_payment_trade t ON t.order_no=o.order_no
		WHERE o.is_delete=0 AND o.store_id=?
		ORDER BY o.order_id`, m.o.storeID)
	if err != nil {
		return err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var id int64
		var orderNo, goodsName, tradeNo, payload, riskSource string
		var userID, goodsID int64
		var goodsPrice, payPrice float64
		var totalNum, payStatus, orderStatus, deliveryStatus, receiptStatus int64
		var riskStatus int64
		var payTime, createdAt, updatedAt sql.NullInt64
		if err := rows.Scan(&id, &orderNo, &userID, &goodsID, &goodsName, &goodsPrice, &totalNum,
			&payPrice, &payStatus, &orderStatus, &deliveryStatus, &receiptStatus,
			&tradeNo, &payTime, &createdAt, &updatedAt, &payload, &riskStatus, &riskSource); err != nil {
			return err
		}
		// map to new state machine:
		// unpaid → 0; refunded → 6; paid: receipt(已完成)→3, delivered(服务中)→2, else 待联系 1
		status := int64(0)
		paid := payStatus == 20
		refunded := paid && m.orderHasCompletedRefund(orderNo)
		switch {
		case !paid:
			status = 0
			if orderStatus == 20 {
				status = 4 // cancelled pre-pay
			}
		case refunded:
			status = 6
		case receiptStatus == 20:
			status = 3
		case deliveryStatus == 20:
			status = 2
		default:
			status = 1
		}
		// provide_goods: legacy delivery_status 20 means the deliver notify ran.
		pg := ""
		if paid && deliveryStatus == 20 && status != 6 {
			pg = "success"
		}
		total := cents(payPrice)
		if total == 0 {
			total = cents(goodsPrice) * totalNum
		}
		if _, err := m.sq.Exec(`INSERT OR REPLACE INTO orders
			(id,order_no,client_token,user_id,service_id,title_snapshot,cover_snapshot,price_cents,quantity,total_cents,
			 status,pay_order_id,transaction_id,pay_snapshot,provide_goods_status,risk_status,risk_source,cancel_reason,
			 pay_time,complete_time,created_at,updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			id, orderNo, "migrated-"+orderNo, userID, goodsID, goodsName, "", cents(goodsPrice), totalNum, total,
			status, tradeNo, "", payload, pg,
			mapLegacyRisk(riskStatus), riskSource, "",
			payTime.Int64, completeTime(status, payTime.Int64), createdAt.Int64, updatedAt.Int64); err != nil {
			return err
		}
		n++
	}
	m.add("orders", n)
	return rows.Err()
}

func cents(v float64) int64 { return int64(v*100 + 0.5) }

func ifThen(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}

// mapLegacyRisk: legacy 0=none 10=locked 20=refunded → new 0/1/2.
func mapLegacyRisk(v int64) int64 {
	switch v {
	case 10:
		return 1
	case 20:
		return 2
	}
	return 0
}

func completeTime(status, payTime int64) int64 {
	if status == 3 && payTime > 0 {
		return payTime
	}
	return 0
}

func (m *mig) orderHasCompletedRefund(orderNo string) bool {
	var n int
	m.my.QueryRow(`SELECT COUNT(*) FROM yoshop_order_refund r JOIN yoshop_order o ON o.order_id=r.order_id
		WHERE o.order_no=? AND r.status=20`, orderNo).Scan(&n)
	return n > 0
}

func (m *mig) refunds() error {
	rows, err := m.qx(`SELECT r.order_refund_id, o.order_id, o.order_no, r.refund_money,
		r.apply_desc, r.audit_status, r.status, r.refuse_desc, r.create_time, r.update_time
		FROM yoshop_order_refund r JOIN yoshop_order o ON o.order_id=r.order_id
		WHERE o.is_delete=0 AND o.store_id=?`, m.o.storeID)
	if err != nil {
		return err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var id, orderID int64
		var orderNo, applyDesc, refuseDesc string
		var money float64
		var auditStatus, status int64
		var createdAt, updatedAt sql.NullInt64
		if err := rows.Scan(&id, &orderID, &orderNo, &money, &applyDesc, &auditStatus, &status, &refuseDesc, &createdAt, &updatedAt); err != nil {
			return err
		}
		// all historical refunds land in a terminal state
		st := "refunded"
		note := applyDesc
		switch status {
		case 10: // rejected
			st = "rejected"
			note = refuseDesc
		case 30: // cancelled
			st = "rejected"
			note = "用户撤销售款申请"
		default:
			if status == 0 && auditStatus == 0 {
				st = "rejected" // never audited: treat as closed-no-refund
				note = "迁移：未审核的历史申请"
			}
		}
		refundNo := fmt.Sprintf("RF%s%06d", time.Unix(createdAt.Int64, 0).Format("20060102"), id)
		if _, err := m.sq.Exec(`INSERT OR REPLACE INTO refunds
			(id,order_id,refund_no,amount_cents,reason,status,mode,audit_note,ios_waiting,xpay_submitted,created_at,updated_at)
			VALUES (?,?,?,?,?,?,?,?,0,1,?,?)`,
			id, orderID, refundNo, cents(money), Trunc(applyDesc, 200), st, "auto", Trunc(note, 200),
			createdAt.Int64, updatedAt.Int64); err != nil {
			return err
		}
		n++
	}
	m.add("refunds", n)
	return rows.Err()
}

func Trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func (m *mig) inquiries() error {
	rows, err := m.qx(`SELECT inquiry_id, order_id, pay_order_id, fingerprint, binding_status,
		request_reason, request_payload, service_stage, result_code, result_info, evidence, received_at
		FROM yoshop_payment_ios_refund_inquiry WHERE store_id=?`, m.o.storeID)
	if err != nil {
		return err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var id, orderID int64
		var payOrderID, fingerprint, binding, reason, payload, stage, resultInfo, evidence string
		var resultCode int64
		var receivedAt sql.NullInt64
		if err := rows.Scan(&id, &orderID, &payOrderID, &fingerprint, &binding,
			&reason, &payload, &stage, &resultCode, &resultInfo, &evidence, &receivedAt); err != nil {
			return err
		}
		if _, err := m.sq.Exec(`INSERT OR REPLACE INTO refund_inquiries
			(id,order_id,pay_order_id,fingerprint,binding_status,request_reason,service_stage,result_code,suggestion,evidence,raw,created_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
			id, orderID, payOrderID, fingerprint, binding, Trunc(reason, 200), stage, resultCode,
			Trunc(resultInfo, 200), Trunc(evidence, 500), payload, receivedAt.Int64); err != nil {
			return err
		}
		n++
	}
	m.add("inquiries", n)
	return rows.Err()
}

func (m *mig) settings() error {
	// help docs → docs.privacy / docs.service-policy
	var privacy, policy string
	m.my.QueryRow(`SELECT content FROM yoshop_help WHERE title LIKE '%隐私%' AND is_delete=0 LIMIT 1`).Scan(&privacy)
	m.my.QueryRow(`SELECT content FROM yoshop_help WHERE title LIKE '%服务政策%' AND is_delete=0 LIMIT 1`).Scan(&policy)
	set := func(k, v string) error {
		if v == "" {
			return nil
		}
		_, err := m.sq.Exec(`INSERT INTO settings (key,value) VALUES (?,?)
			ON CONFLICT(key) DO UPDATE SET value=excluded.value`, k, v)
		return err
	}
	if err := set("docs.privacy", privacy); err != nil {
		return err
	}
	if err := set("docs.service-policy", policy); err != nil {
		return err
	}
	// notice: legacy wxapp first-login popup doubles as the announcement.
	var basic string
	m.my.QueryRow(`SELECT ` + "`values`" + ` FROM yoshop_wxapp_setting WHERE ` + "`key`" + `='basic' LIMIT 1`).Scan(&basic)
	if basic != "" {
		var bj struct {
			PopupBody string `json:"firstLoginPopupBody"`
			AppID     string `json:"app_id"`
		}
		if json.Unmarshal([]byte(basic), &bj) == nil && bj.PopupBody != "" {
			if err := set("notice", bj.PopupBody); err != nil {
				return err
			}
		}
	}
	// banner: from DIY page banner item; images are rewritten to /uploads/
	var pageData string
	m.my.QueryRow(`SELECT page_data FROM yoshop_page WHERE store_id=? ORDER BY page_id LIMIT 1`, m.o.storeID).Scan(&pageData)
	if pageData != "" {
		type pageItem struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}
		var page struct {
			Items []pageItem `json:"items"`
		}
		if json.Unmarshal([]byte(pageData), &page) == nil {
			for _, it := range page.Items {
				if it.Type != "banner" || len(it.Data) == 0 {
					continue
				}
				var banners []struct {
					ImgURL string `json:"imgUrl"`
				}
				if json.Unmarshal(it.Data, &banners) == nil && len(banners) > 0 {
					out := []map[string]string{}
					for _, b := range banners {
						out = append(out, map[string]string{"image": legacyImagePath(b.ImgURL), "url": ""})
					}
					j, _ := json.Marshal(out)
					if err := set("banners", string(j)); err != nil {
						return err
					}
				}
				break
			}
		}
	}
	m.add("settings", 1)
	return nil
}

// copyUploads flattens legacy uploads into data/uploads (basenames are
// unique across the store; collisions get a hash suffix).
func (m *mig) copyUploads() error {
	dstDir := filepath.Join(m.o.dataDir, "uploads")
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return err
	}
	n := 0
	err := filepath.Walk(m.o.uploads, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil //nolint
		}
		name := info.Name()
		dst := filepath.Join(dstDir, name)
		if _, statErr := os.Stat(dst); statErr == nil {
			// same file already copied
			n++
			return nil
		}
		src, err := os.Open(p)
		if err != nil {
			return err
		}
		defer src.Close()
		dstF, err := os.Create(dst)
		if err != nil {
			return err
		}
		if _, err := io.Copy(dstF, src); err != nil {
			dstF.Close()
			return err
		}
		if err := dstF.Close(); err != nil {
			return err
		}
		n++
		return nil
	})
	if err != nil {
		return err
	}
	m.add("uploads", n)
	_ = sha256.New // keep import; used for potential future dedup
	return nil
}

func (m *mig) report() {
	fmt.Println("migrate-yoshop done:")
	for _, k := range []string{"users", "categories", "services", "orders", "refunds", "inquiries", "settings", "uploads"} {
		fmt.Printf("  %-10s %d\n", k, m.counts[k])
	}
}
