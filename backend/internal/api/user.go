// User endpoints that PLAN.md's API list requires beyond the order flow:
// my refund records, and feedback (意见反馈) list + submit.
package api

import (
	"encoding/json"
	"io"
	"net/http"

	"dianjing/backend/internal/app"
	"dianjing/backend/internal/store"
)

// ---- GET /refunds : my refund records (joined with order title) ----

func (s *Server) handleMyRefunds(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	rows, err := s.St.DB.Query(`SELECT r.id, r.order_id, r.refund_no, r.amount_cents,
		r.reason, r.status, r.mode, r.ios_waiting, r.created_at, o.order_no, o.title_snapshot
		FROM refunds r JOIN orders o ON o.id=r.order_id
		WHERE o.user_id=? ORDER BY r.id DESC LIMIT 50`, u.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	list := []map[string]any{}
	for rows.Next() {
		var id, orderID, amount, iosWaiting, createdAt int64
		var refundNo, reason, status, mode, orderNo, title string
		if rows.Scan(&id, &orderID, &refundNo, &amount, &reason, &status, &mode,
			&iosWaiting, &createdAt, &orderNo, &title) == nil {
			list = append(list, map[string]any{
				"id": id, "order_id": orderID, "order_no": orderNo, "title": title,
				"refund_no": refundNo, "amount_cents": amount, "reason": reason,
				"status": status, "mode": mode, "ios_waiting": iosWaiting != 0,
				"created_at": createdAt,
			})
		}
	}
	okJSON(w, map[string]any{"refunds": list})
}

// ---- GET /feedbacks, POST /feedbacks ----

func (s *Server) handleMyFeedbacks(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if r.Method == http.MethodPost {
		s.userCreateFeedback(w, r, u)
		return
	}
	rows, err := s.St.DB.Query(`SELECT id,type,content,reply,status,created_at,replied_at
		FROM feedbacks WHERE user_id=? ORDER BY id DESC LIMIT 50`, u.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	list := []map[string]any{}
	for rows.Next() {
		var id, status, createdAt, repliedAt int64
		var typ, content, reply string
		if rows.Scan(&id, &typ, &content, &reply, &status, &createdAt, &repliedAt) == nil {
			list = append(list, map[string]any{
				"id": id, "type": typ, "content": content, "reply": reply,
				"status": status, "created_at": createdAt, "replied_at": repliedAt,
			})
		}
	}
	okJSON(w, map[string]any{"feedbacks": list})
}

func (s *Server) userCreateFeedback(w http.ResponseWriter, r *http.Request, u *store.User) {
	var req struct {
		Type    string `json:"type"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&req); err != nil {
		failMsg(w, "参数非法")
		return
	}
	req.Content = app.TruncateStr(req.Content, 1000)
	if req.Content == "" {
		failMsg(w, "请填写反馈内容")
		return
	}
	if req.Type != "complaint" && req.Type != "suggestion" {
		req.Type = "complaint"
	}
	now := store.Now()
	res, err := s.St.DB.Exec(`INSERT INTO feedbacks (user_id,type,content,created_at) VALUES (?,?,?,?)`,
		u.ID, req.Type, req.Content, now)
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	id, _ := res.LastInsertId()
	s.St.Event("feedback.create", "feedback", id, "")
	okJSON(w, map[string]any{"id": id, "ok": true})
}
