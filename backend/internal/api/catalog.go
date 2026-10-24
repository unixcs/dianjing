package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
)

func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }

// ---- models ----

type category struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type service struct {
	ID          int64  `json:"id"`
	CategoryID  int64  `json:"category_id"`
	Title       string `json:"title"`
	Cover       string `json:"cover"`
	PriceCents  int64  `json:"price_cents"`
	Sales       int64  `json:"sales"`
	Content     string `json:"content,omitempty"`
	Status      int    `json:"-"`
	Sort        int    `json:"-"`
	XPayProduct string `json:"-"`
}

func (s *Server) serviceCoverURL(cover string) string {
	if cover == "" {
		return ""
	}
	if cover[0] == '/' {
		return s.Cfg.BaseURL + cover
	}
	return cover
}

// ---- GET /home : single aggregate request for the home page ----

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	notice, _ := s.St.GetSetting("notice")
	bannersJSON, _ := s.St.GetSetting("banners")
	cats := s.categories(r)
	svcs := s.listServices(r, 0, 20, "")
	okJSON(w, map[string]any{
		"notice":   notice,
		"banners":  s.banners(bannersJSON),
		"category": cats,
		"services": svcs,
	})
}

// banners decodes the admin-editable banner JSON and absolutizes image
// paths against BaseURL — same treatment as service covers (relative
// /uploads/... paths render as blank swipers on the device).
func (s *Server) banners(raw string) []map[string]any {
	out := []map[string]any{}
	if raw == "" {
		return out
	}
	var items []struct {
		Image string `json:"image"`
		URL   string `json:"url"`
	}
	if jsonUnmarshal(raw, &items) != nil {
		return out
	}
	for _, it := range items {
		out = append(out, map[string]any{"image": s.serviceCoverURL(it.Image), "url": it.URL})
	}
	return out
}

// ---- GET /categories ----

func (s *Server) handleCategories(w http.ResponseWriter, r *http.Request) {
	okJSON(w, map[string]any{"categories": s.categories(r)})
}

func (s *Server) categories(r *http.Request) []category {
	rows, err := s.St.DB.Query(`SELECT id,name FROM categories WHERE status=1 ORDER BY sort, id`)
	if err != nil {
		return []category{}
	}
	defer rows.Close()
	cats := []category{}
	for rows.Next() {
		var c category
		if rows.Scan(&c.ID, &c.Name) == nil {
			cats = append(cats, c)
		}
	}
	return cats
}

// ---- GET /services, GET /services/:id ----

func (s *Server) handleServices(w http.ResponseWriter, r *http.Request) {
	limit := intQuery(r, "limit", 20)
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	catID := intQuery(r, "category_id", 0)
	kw := r.URL.Query().Get("keyword")
	okJSON(w, map[string]any{"services": s.listServices(r, catID, limit, kw)})
}

func (s *Server) listServices(r *http.Request, catID, limit int, keyword string) []service {
	q := `SELECT id,category_id,title,cover,price_cents,sales_base+sales_actual FROM services WHERE status=1`
	var args []any
	if catID > 0 {
		q += ` AND category_id=?`
		args = append(args, catID)
	}
	if keyword != "" {
		q += ` AND title LIKE ?`
		args = append(args, "%"+keyword+"%")
	}
	q += ` ORDER BY sort, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.St.DB.Query(q, args...)
	if err != nil {
		return []service{}
	}
	defer rows.Close()
	list := []service{}
	for rows.Next() {
		var v service
		if rows.Scan(&v.ID, &v.CategoryID, &v.Title, &v.Cover, &v.PriceCents, &v.Sales) == nil {
			v.Cover = s.serviceCoverURL(v.Cover)
			list = append(list, v)
		}
	}
	return list
}

func (s *Server) handleServiceDetail(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var v service
	var cover string
	err := s.St.DB.QueryRow(`SELECT id,category_id,title,cover,price_cents,sales_base+sales_actual,content
		FROM services WHERE id=? AND status=1`, id).
		Scan(&v.ID, &v.CategoryID, &v.Title, &cover, &v.PriceCents, &v.Sales, &v.Content)
	if isNoRows(err) {
		fail(w, http.StatusNotFound, "服务不存在或已下架")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	v.Cover = s.serviceCoverURL(cover)
	okJSON(w, map[string]any{"service": v})
}

// ---- GET /docs/:key : privacy | service-policy (admin-editable rich text) ----

func (s *Server) handleDoc(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if key != "privacy" && key != "service-policy" {
		fail(w, http.StatusNotFound, "未知文档")
		return
	}
	settingKey := "docs." + key
	v, _ := s.St.GetSetting(settingKey)
	if v == "" {
		v = "暂无内容"
	}
	writeJSON(w, http.StatusOK, map[string]string{"key": key, "content": v})
}

// ---- small helpers ----

func intQuery(r *http.Request, name string, def int) int {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}
