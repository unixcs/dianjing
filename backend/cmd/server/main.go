// Command server is the dianjing backend entrypoint: HTTP API +
// compensation ticker and embedded admin panel.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"dianjing/backend/internal/api"
	"dianjing/backend/internal/app"
	"dianjing/backend/internal/config"
	"dianjing/backend/internal/store"
	"dianjing/backend/internal/xpay"
)

func main() {
	cfgPath := flag.String("config", "dianjing.env", "config file path")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		os.Exit(1)
	}
	if err := validateConfig(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(1)
	}
	st, err := store.Open(cfg.DataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open store: %v\n", err)
		os.Exit(1)
	}
	defer st.Close()

	xp := xpay.New(cfg.WxAppID, cfg.WxAppSecret, cfg.XPayAppKey, cfg.XPaySandbox)
	ap := &app.App{St: st, XPay: xp}
	srv := &api.Server{Cfg: cfg, St: st, App: ap, XPay: xp}
	tk := &app.Ticker{App: ap, Sandbox: cfg.XPaySandbox}
	tk.Start()
	defer tk.Stop()
	addr := cfg.ListenAddr
	fmt.Printf("dianjing backend listening on %s (data: %s)\n", addr, cfg.DataDir)
	// /uploads/ serves DataDir/uploads directly; nginx may take over this
	// prefix in production — the Go handler is the no-nginx fallback.
	mux := http.NewServeMux()
	mux.Handle("/uploads/", http.StripPrefix("/uploads/", noDirListing(
		http.Dir(filepath.Join(cfg.DataDir, "uploads")))))
	mux.Handle("/", srv.Routes())
	log.Fatal(http.ListenAndServe(addr, mux))
}

// validateConfig refuses to start on incomplete secrets — an empty
// XPAY_APP_KEY/WX_APP_SECRET silently degrades the user-token HMAC into
// a guessable constant, and an empty admin hash would lock out (or, in
// the wrong design, open) the admin panel.
func validateConfig(cfg *config.Config) error {
	var missing []string
	if cfg.WxAppID == "" {
		missing = append(missing, "WX_APP_ID")
	}
	if cfg.WxAppSecret == "" {
		missing = append(missing, "WX_APP_SECRET")
	}
	if cfg.OfferID == "" {
		missing = append(missing, "XPAY_OFFER_ID")
	}
	if cfg.XPayAppKey == "" {
		missing = append(missing, "XPAY_APP_KEY")
	}
	if cfg.AdminPasswordHash == "" {
		missing = append(missing, "ADMIN_PASSWORD_HASH (用 go run ./cmd/hashpw -p '密码' 生成)")
	} else if !strings.HasPrefix(cfg.AdminPasswordHash, "$2") {
		return fmt.Errorf("ADMIN_PASSWORD_HASH 不是 bcrypt hash（应以 $2 开头）——用 go run ./cmd/hashpw 生成")
	}
	if len(missing) > 0 {
		return fmt.Errorf("缺少必填配置: %s", strings.Join(missing, ", "))
	}
	if cfg.XPayPushToken == "" {
		fmt.Println("警告: XPAY_PUSH_TOKEN 未配置，微信消息推送将无法校验来源")
	}
	if cfg.XPaySandbox {
		fmt.Println("提示: XPAY_SANDBOX=1（沙箱环境），正式上线前改为 0")
	}
	return nil
}

// noDirListing serves files but refuses directory indexes.
func noDirListing(root http.FileSystem) http.Handler {
	fs := http.FileServer(root)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		fs.ServeHTTP(w, r)
	})
}
