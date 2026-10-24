// Admin panel backend: bcrypt password login with an HMAC session
// token, plus CRUD endpoints for services/orders/refunds/feedbacks/
// settings and file upload. The UI itself is embedded HTML (admin_ui.go).
package api

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"dianjing/backend/internal/app"
	"dianjing/backend/internal/store"
)

// ---- admin session ----

func (s *Server) adminToken(expires int64) string {
	payload := fmt.Sprintf("admin.%d", expires)
	mac := hmac.New(sha256.New, []byte(s.adminSecret()))
	mac.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Server) adminSecret() string {
	return s.Cfg.AdminPasswordHash + "|dianjing-admin"
}

func (s *Server) verifyAdminToken(token string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	expires, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || expires < time.Now().Unix() {
		return false
	}
	want := s.adminToken(expires)
	return hmac.Equal([]byte(want), []byte(token))
}

func (s *Server) adminAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Fail closed: an empty hash would make the HMAC secret a constant.
		if s.Cfg.AdminPasswordHash == "" {
			fail(w, http.StatusInternalServerError, "管理密码未配置")
			return
		}
		token := r.Header.Get("X-Admin-Token")
		if token == "" || !s.verifyAdminToken(token) {
			fail(w, http.StatusUnauthorized, "未登录")
			return
		}
		next(w, r)
	}
}

// ---- POST /admin/login ----

func (s *Server) handleAdminLogin(w http.ResponseWriter, r *http.Request) {
	if s.Cfg.AdminPasswordHash == "" {
		failMsg(w, "管理密码未配置（ADMIN_PASSWORD_HASH）")
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil || req.Password == "" {
		failMsg(w, "参数非法")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(s.Cfg.AdminPasswordHash), []byte(req.Password)) != nil {
		// Constant-ish delay to blunt brute force on the tiny surface.
		time.Sleep(300 * time.Millisecond)
		fail(w, http.StatusUnauthorized, "密码错误")
		return
	}
	expires := time.Now().Unix() + s.Cfg.AdminSessionTTL
	okJSON(w, map[string]any{"token": s.adminToken(expires), "expires_at": expires})
}

// ---- GET /admin/overview ----

func (s *Server) handleAdminOverview(w http.ResponseWriter, r *http.Request) {
	count := func(q string, args ...any) int64 {
		var n int64
		s.St.DB.QueryRow(q, args...).Scan(&n)
		return n
	}
	okJSON(w, map[string]any{
		"users_total":     count(`SELECT COUNT(*) FROM users`),
		"orders_total":    count(`SELECT COUNT(*) FROM orders`),
		"orders_pending":  count(`SELECT COUNT(*) FROM orders WHERE status=?`, app.OrderPendingPay),
		"orders_contact":  count(`SELECT COUNT(*) FROM orders WHERE status=?`, app.OrderContact),
		"orders_serving":  count(`SELECT COUNT(*) FROM orders WHERE status=?`, app.OrderServing),
		"orders_done":     count(`SELECT COUNT(*) FROM orders WHERE status=?`, app.OrderDone),
		"refunds_pending": count(`SELECT COUNT(*) FROM refunds WHERE status IN ('applying','approved')`),
		"feedbacks_open":  count(`SELECT COUNT(*) FROM feedbacks WHERE status=0`),
		"revenue_cents":   count(`SELECT COALESCE(SUM(total_cents),0) FROM orders WHERE status IN (1,2,3) AND pay_time>0`),
	})
}

// ---- categories (read + create/rename) ----

func (s *Server) handleAdminCategories(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		rows, err := s.St.DB.Query(`SELECT id,name,sort,status FROM categories ORDER BY sort,id`)
		if err != nil {
			fail(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer rows.Close()
		list := []map[string]any{}
		for rows.Next() {
			var id, sort, status int64
			var name string
			rows.Scan(&id, &name, &sort, &status)
			list = append(list, map[string]any{"id": id, "name": name, "sort": sort, "status": status})
		}
		okJSON(w, map[string]any{"categories": list})
	case http.MethodPost:
		var req struct {
			Name string `json:"name"`
			Sort int64  `json:"sort"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil || strings.TrimSpace(req.Name) == "" {
			failMsg(w, "名称不能为空")
			return
		}
		res, err := s.St.DB.Exec(`INSERT INTO categories (name,sort,status) VALUES (?,?,1)`,
			app.TruncateStr(strings.TrimSpace(req.Name), 20), req.Sort)
		if err != nil {
			fail(w, http.StatusInternalServerError, err.Error())
			return
		}
		id, _ := res.LastInsertId()
		okJSON(w, map[string]any{"id": id})
	default:
		fail(w, http.StatusMethodNotAllowed, "method")
	}
}

// ---- services CRUD ----

func (s *Server) handleAdminServices(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		rows, err := s.St.DB.Query(`SELECT id,category_id,title,cover,price_cents,sales_base,sales_actual,content,xpay_product_id,xpay_env,status,sort FROM services ORDER BY sort,id`)
		if err != nil {
			fail(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer rows.Close()
		list := []map[string]any{}
		for rows.Next() {
			var id, cat, price, sb, sa, env, status, sort int64
			var title, cover, content, product string
			rows.Scan(&id, &cat, &title, &cover, &price, &sb, &sa, &content, &product, &env, &status, &sort)
			list = append(list, map[string]any{
				"id": id, "category_id": cat, "title": title, "cover": cover,
				"price_cents": price, "sales_base": sb, "sales_actual": sa,
				"content": content, "xpay_product_id": product, "xpay_env": env,
				"status": status, "sort": sort,
			})
		}
		okJSON(w, map[string]any{"services": list})
	case http.MethodPost:
		s.adminSaveService(w, r, 0)
	default:
		fail(w, http.StatusMethodNotAllowed, "method")
	}
}

func (s *Server) handleAdminServiceOne(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	switch r.Method {
	case http.MethodPut, http.MethodPost:
		s.adminSaveService(w, r, id)
	case http.MethodDelete:
		// Soft-delete: 下架 rather than row deletion (orders reference it).
		s.St.DB.Exec(`UPDATE services SET status=0, updated_at=? WHERE id=?`, store.Now(), id)
		okJSON(w, map[string]any{"ok": true})
	default:
		fail(w, http.StatusMethodNotAllowed, "method")
	}
}

func (s *Server) adminSaveService(w http.ResponseWriter, r *http.Request, id int64) {
	var req map[string]any
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		failMsg(w, "参数非法")
		return
	}
	getInt := func(k string, def int64) int64 {
		switch v := req[k].(type) {
		case float64:
			return int64(v)
		case string:
			n, _ := strconv.ParseInt(v, 10, 64)
			return n
		}
		return def
	}
	getStr := func(k string) string {
		v, _ := req[k].(string)
		return v
	}
	now := store.Now()
	if id == 0 {
		title := app.TruncateStr(strings.TrimSpace(getStr("title")), 50)
		if title == "" {
			failMsg(w, "标题不能为空")
			return
		}
		res, err := s.St.DB.Exec(`INSERT INTO services (category_id,title,cover,price_cents,sales_base,content,xpay_product_id,xpay_env,status,sort,created_at,updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
			getInt("category_id", 0), title, getStr("cover"), getInt("price_cents", 0),
			getInt("sales_base", 0), getStr("content"), getStr("xpay_product_id"),
			getInt("xpay_env", 0), getInt("status", 1), getInt("sort", 100), now, now)
		if err != nil {
			fail(w, http.StatusInternalServerError, err.Error())
			return
		}
		id, _ = res.LastInsertId()
		s.St.Event("admin.service.create", "service", id, title)
		okJSON(w, map[string]any{"id": id})
		return
	}
	// Partial update (PUT): absent fields keep their stored values, so a
	// narrow request like {status:0} from the toggle button can't blank
	// the rest of the row.
	var curCat, curPrice, curBase, curEnv, curStatus, curSort int64
	var curTitle, curCover, curContent, curProduct string
	if err := s.St.DB.QueryRow(`SELECT category_id,title,cover,price_cents,sales_base,content,xpay_product_id,xpay_env,status,sort
		FROM services WHERE id=?`, id).
		Scan(&curCat, &curTitle, &curCover, &curPrice, &curBase, &curContent, &curProduct, &curEnv, &curStatus, &curSort); err != nil {
		if isNoRows(err) {
			fail(w, http.StatusNotFound, "服务不存在")
		} else {
			fail(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	title := curTitle
	if _, ok := req["title"]; ok {
		title = app.TruncateStr(strings.TrimSpace(getStr("title")), 50)
	}
	if title == "" {
		failMsg(w, "标题不能为空")
		return
	}
	setIf := func(k string, cur int64) int64 {
		if _, ok := req[k]; ok {
			return getInt(k, cur)
		}
		return cur
	}
	setStr := func(k string, cur string) string {
		if _, ok := req[k]; ok {
			return getStr(k)
		}
		return cur
	}
	if _, err := s.St.DB.Exec(`UPDATE services SET category_id=?,title=?,cover=?,price_cents=?,sales_base=?,content=?,xpay_product_id=?,xpay_env=?,status=?,sort=?,updated_at=? WHERE id=?`,
		setIf("category_id", curCat), title, setStr("cover", curCover), setIf("price_cents", curPrice),
		setIf("sales_base", curBase), setStr("content", curContent), setStr("xpay_product_id", curProduct),
		setIf("xpay_env", curEnv), setIf("status", curStatus), setIf("sort", curSort), now, id); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.St.Event("admin.service.update", "service", id, title)
	okJSON(w, map[string]any{"id": id})
}

// ---- orders ----

func (s *Server) handleAdminOrders(w http.ResponseWriter, r *http.Request) {
	st := r.URL.Query().Get("status")
	q := `SELECT ` + app.OrderCols + ` FROM orders`
	var args []any
	if st != "" {
		if n, err := strconv.Atoi(st); err == nil {
			q += ` WHERE status=?`
			args = append(args, n)
		}
	}
	q += ` ORDER BY id DESC LIMIT 100`
	rows, err := s.St.DB.Query(q, args...)
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	var os []*app.Order
	for rows.Next() {
		o, err := app.ScanOrder(rows)
		if err != nil {
			continue
		}
		os = append(os, o)
	}
	// Batch-load user mobile/nickname for the page (plus the order's own
	// contact field captured at checkout).
	um := map[int64][2]string{}
	seen := map[int64]bool{}
	var ids []string
	for _, o := range os {
		if !seen[o.UserID] {
			seen[o.UserID] = true
			ids = append(ids, fmt.Sprint(o.UserID))
		}
	}
	if len(ids) > 0 {
		urows, err := s.St.DB.Query(`SELECT id,mobile,nickname FROM users WHERE id IN (` +
			strings.Join(ids, ",") + `)`)
		if err == nil {
			for urows.Next() {
				var id int64
				var mobile, nickname string
				if urows.Scan(&id, &mobile, &nickname) == nil {
					um[id] = [2]string{mobile, nickname}
				}
			}
			urows.Close()
		}
	}
	list := []map[string]any{}
	for _, o := range os {
		list = append(list, map[string]any{
			"id": o.ID, "order_no": o.OrderNo, "user_id": o.UserID,
			"user_nickname": um[o.UserID][1], "user_mobile": um[o.UserID][0],
			"title": o.TitleSnapshot, "total_cents": o.TotalCents,
			"status": o.Status, "status_text": statusText(o.Status),
			"pay_time": o.PayTime, "created_at": o.CreatedAt,
			"provide_goods_status": o.ProvideGoodsStatus, "risk_status": o.RiskStatus,
			"contact": o.Contact,
		})
	}
	okJSON(w, map[string]any{"orders": list})
}

// POST /admin/orders/:id/status  {status: 2|3}
func (s *Server) handleAdminOrderStatus(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var req struct {
		Status int `json:"status"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		failMsg(w, "参数非法")
		return
	}
	if err := s.App.AdminSetStatus(id, req.Status); err != nil {
		if errors.Is(err, app.ErrConflict) {
			// err.Error() carries the specific reason (e.g. risk-locked).
			failMsg(w, err.Error())
			return
		}
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	o, _ := s.App.OrderByID(id)
	okJSON(w, map[string]any{"order": s.toOrderJSON(o)})
}

// ---- refunds ----

func (s *Server) handleAdminRefunds(w http.ResponseWriter, r *http.Request) {
	rows, err := s.St.DB.Query(`SELECT rf.id,rf.order_id,rf.refund_no,rf.amount_cents,rf.reason,rf.status,rf.mode,rf.audit_note,rf.ios_waiting,o.order_no,o.title_snapshot
		FROM refunds rf JOIN orders o ON o.id=rf.order_id ORDER BY rf.id DESC LIMIT 100`)
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	list := []map[string]any{}
	for rows.Next() {
		var id, orderID, amount int64
		var refundNo, reason, status, mode, note, orderNo, title string
		var iosWaiting bool
		rows.Scan(&id, &orderID, &refundNo, &amount, &reason, &status, &mode, &note, &iosWaiting, &orderNo, &title)
		list = append(list, map[string]any{
			"id": id, "order_id": orderID, "refund_no": refundNo, "amount_cents": amount,
			"reason": reason, "status": status, "mode": mode, "audit_note": note,
			"ios_waiting": iosWaiting, "order_no": orderNo, "title": title,
		})
	}
	okJSON(w, map[string]any{"refunds": list})
}

// POST /admin/refunds/:id/audit  {approve: bool, note: string}
func (s *Server) handleAdminRefundAudit(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var req struct {
		Approve bool   `json:"approve"`
		Note    string `json:"note"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		failMsg(w, "参数非法")
		return
	}
	if err := s.App.AuditRefund(id, req.Approve, req.Note); err != nil {
		if errors.Is(err, app.ErrConflict) {
			failMsg(w, "该退款不在待审核状态")
			return
		}
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Rejection restores the order to 服务中 immediately.
	if !req.Approve {
		if rr, err := s.App.RefundByID(id); err == nil {
			s.App.RejectRefundByMerchant(rr)
		}
	}
	okJSON(w, map[string]any{"ok": true})
}

// ---- feedbacks ----

func (s *Server) handleAdminFeedbacks(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		s.adminReplyFeedback(w, r)
		return
	}
	rows, err := s.St.DB.Query(`SELECT id,user_id,type,content,images,reply,status,created_at FROM feedbacks ORDER BY id DESC LIMIT 100`)
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	list := []map[string]any{}
	for rows.Next() {
		var id, uid, status, createdAt int64
		var typ, content, images, reply string
		rows.Scan(&id, &uid, &typ, &content, &images, &reply, &status, &createdAt)
		list = append(list, map[string]any{
			"id": id, "user_id": uid, "type": typ, "content": content,
			"images": images, "reply": reply, "status": status, "created_at": createdAt,
		})
	}
	okJSON(w, map[string]any{"feedbacks": list})
}

func (s *Server) adminReplyFeedback(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID    int64  `json:"id"`
		Reply string `json:"reply"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil || req.ID == 0 {
		failMsg(w, "参数非法")
		return
	}
	now := store.Now()
	_, err := s.St.DB.Exec(`UPDATE feedbacks SET reply=?, status=1, replied_at=? WHERE id=?`,
		app.TruncateStr(req.Reply, 500), now, req.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.St.Event("admin.feedback.reply", "feedback", req.ID, "")
	okJSON(w, map[string]any{"ok": true})
}

// ---- settings (notice / docs) ----

// Keys match what the public /v2/api/docs/{key} endpoint and /home read.
var adminSettingKeys = []string{"notice", "banners", "docs.privacy", "docs.service-policy"}

func (s *Server) handleAdminSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var req map[string]string
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
			failMsg(w, "参数非法")
			return
		}
		for _, k := range adminSettingKeys {
			if v, ok := req[k]; ok {
				s.St.SetSetting(k, v)
			}
		}
		okJSON(w, map[string]any{"ok": true})
		return
	}
	out := map[string]any{}
	for _, k := range adminSettingKeys {
		v, _ := s.St.GetSetting(k)
		out[k] = v
	}
	okJSON(w, out)
}

// ---- upload ----

func (s *Server) handleAdminUpload(w http.ResponseWriter, r *http.Request) {
	const maxUpload = 8 << 20
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		failMsg(w, "上传解析失败")
		return
	}
	f, hdr, err := r.FormFile("file")
	if err != nil {
		failMsg(w, "缺少文件")
		return
	}
	defer f.Close()
	ext := ".png"
	if i := strings.LastIndex(hdr.Filename, "."); i >= 0 && len(hdr.Filename)-i <= 6 {
		ext = strings.ToLower(hdr.Filename[i:])
		switch ext {
		case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		default:
			failMsg(w, "仅支持图片格式")
			return
		}
	}
	// Hard cap on total size (ParseMultipartForm's memory limit only
	// controls the in-memory threshold, larger bodies spool to disk).
	data, err := io.ReadAll(io.LimitReader(f, maxUpload+1))
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(data) > maxUpload {
		failMsg(w, "图片不能超过 8MB")
		return
	}
	if len(data) == 0 || !strings.HasPrefix(http.DetectContentType(data), "image/") {
		failMsg(w, "文件内容不是图片")
		return
	}
	name := fmt.Sprintf("%d%s", time.Now().UnixNano(), ext)
	if _, err := store.SaveUpload(s.Cfg.DataDir, name, bytes.NewReader(data)); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	okJSON(w, map[string]any{"path": "/uploads/" + name, "url": s.Cfg.BaseURL + "/uploads/" + name})
}
