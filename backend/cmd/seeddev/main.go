// Command seeddev seeds a dev database with a full set of test data:
// categories, services (with covers + rich text), a test user, home
// notice/banners and the two policy docs. It also drops generated PNG
// covers into the uploads dir so the miniapp renders real images.
//
// It is a developer tool for local E2E testing, not part of the server.
// Idempotent: re-running wipes and reseeds catalog/users/settings.
package main

import (
	"bytes"
	"database/sql"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: seeddev <data dir>   (contains dianjing.db + uploads/)")
		os.Exit(1)
	}
	dataDir := os.Args[1]
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir, "dianjing.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		panic(err)
	}
	defer db.Close()

	// Generated solid-color covers so the UI has real images to render.
	upDir := filepath.Join(dataDir, "uploads")
	os.MkdirAll(upDir, 0o755)
	covers := map[string]color.RGBA{
		"cover-basic.png": {R: 91, G: 108, B: 255, A: 255},
		"cover-hot.png":   {R: 255, G: 107, B: 129, A: 255},
		"cover-vip.png":   {R: 46, G: 204, B: 158, A: 255},
		"banner-dev.png":  {R: 244, G: 164, B: 46, A: 255},
	}
	for name, c := range covers {
		if err := writePNG(filepath.Join(upDir, name), 480, 270, c); err != nil {
			panic(err)
		}
	}

	tx, err := db.Begin()
	if err != nil {
		panic(err)
	}
	defer tx.Rollback()
	for _, t := range []string{"services", "categories", "users", "settings"} {
		if _, err := tx.Exec("DELETE FROM " + t); err != nil {
			panic(err)
		}
	}
	now := int64(1761400000)
	ins := func(q string, args ...any) int64 {
		res, err := tx.Exec(q, args...)
		if err != nil {
			panic(err)
		}
		id, _ := res.LastInsertId()
		return id
	}

	c1 := ins(`INSERT INTO categories (name,sort,status) VALUES ('基础',100,1)`)
	c2 := ins(`INSERT INTO categories (name,sort,status) VALUES ('热门',90,1)`)
	svc := []struct {
		cat     int64
		title   string
		cover   string
		price   int64
		xpay    string
		sales   int64
		content string
	}{
		{c1, "竞技技巧一对一指导（基础）", "/uploads/cover-basic.png", 9900, "svc_basic_1", 32,
			"<p>资深导师 1v1 指导，时长 60 分钟。</p><ul><li>段位诊断与复盘</li><li>定制训练计划</li></ul>"},
		{c2, "上分护航 · 热门套餐", "/uploads/cover-hot.png", 18800, "svc_hot_2", 156,
			"<p>高手双人组队护航上分，胜率保障。</p><p><strong>服务时间：</strong>每日 19:00–24:00</p>"},
		{c2, "VIP 专属教练包月", "/uploads/cover-vip.png", 69800, "svc_vip_3", 8,
			"<p>包月专属教练，无限次咨询 + 每周 4 次陪练。</p>"},
	}
	for i, s := range svc {
		ins(`INSERT INTO services (category_id,title,cover,price_cents,sales_base,content,xpay_product_id,xpay_env,status,sort,created_at,updated_at)
			VALUES (?,?,?,?,?,?,?,?,1,?,?,?)`,
			s.cat, s.title, s.cover, s.price, s.sales, s.content, s.xpay, 1, (i+1)*10, now, now)
	}

	uid := ins(`INSERT INTO users (openid,session_key,nickname,avatar,mobile,status,created_at,last_login_at)
		VALUES ('dev-openid-1','dev-session-key','测试用户','/uploads/cover-vip.png','13800001234',1,?,?)`, now, now)

	setting := func(k, v string) {
		ins(`INSERT INTO settings (key,value) VALUES (?,?)`, k, v)
	}
	setting("notice", "观星电竞测试公告：本地 E2E 环境专用")
	setting("banners", `[{"image":"/uploads/banner-dev.png","url":"/pages/goods-detail/goods-detail?id=1"}]`)
	setting("docs.privacy", "<h3>隐私政策（测试）</h3><p>本政策仅用于本地 E2E 测试。</p>")
	setting("docs.service-policy", "<h3>服务政策（测试）</h3><p>服务政策仅用于本地 E2E 测试。</p>")

	if err := tx.Commit(); err != nil {
		panic(err)
	}
	fmt.Printf("seeded: 2 categories, %d services, user id=%d, 4 settings, 4 pngs -> %s\n",
		len(svc), uid, upDir)
}

// writePNG renders a solid-color PNG — no external deps.
func writePNG(path string, w, h int, c color.RGBA) error {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}
