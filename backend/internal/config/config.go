// Package config loads dianjing server configuration from a simple
// key=value file or environment variables. Values in the environment
// take precedence over the file.
package config

import (
	"bufio"
	"os"
	"strings"
)

type Config struct {
	ListenAddr string // HTTP listen address, e.g. 127.0.0.1:8096
	BaseURL    string // public base URL, e.g. https://wx.gxwqb.cn

	DataDir string // sqlite db + uploads live here

	// WeChat miniapp
	WxAppID     string
	WxAppSecret string

	// xpay virtual payment
	OfferID        string
	XPayAppKey     string
	XPaySandbox    bool   // true => env=1
	XPayPushToken  string // message push token (GET verify + plaintext sig)
	XPayPushAESKey string // message push EncodingAESKey (secure mode replies)

	// Admin panel
	AdminPasswordHash string // bcrypt hash
	AdminSessionTTL   int64  // seconds

	TokenTTL int64 // user login token TTL, seconds
}

func Load(path string) (*Config, error) {
	c := &Config{
		ListenAddr:      "127.0.0.1:8096",
		BaseURL:         "http://127.0.0.1:8096",
		DataDir:         "./data",
		AdminSessionTTL: 86400 * 7,
		TokenTTL:        86400 * 30,
	}
	if path != "" {
		if err := c.loadFile(path); err != nil {
			return nil, err
		}
	}
	c.fromEnv()
	if c.AdminSessionTTL <= 0 {
		c.AdminSessionTTL = 86400 * 7
	}
	if c.TokenTTL <= 0 {
		c.TokenTTL = 86400 * 30
	}
	return c, nil
}

func (c *Config) loadFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	m := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		m[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	get := func(k string) string { return m[k] }
	c.ListenAddr = pick(get("LISTEN"), c.ListenAddr)
	c.BaseURL = pick(get("BASE_URL"), c.BaseURL)
	c.DataDir = pick(get("DATA_DIR"), c.DataDir)
	c.WxAppID = pick(get("WX_APP_ID"), "")
	c.WxAppSecret = pick(get("WX_APP_SECRET"), "")
	c.OfferID = pick(get("XPAY_OFFER_ID"), "")
	c.XPayAppKey = pick(get("XPAY_APP_KEY"), "")
	c.XPaySandbox = pick(get("XPAY_SANDBOX"), "") == "1"
	c.XPayPushToken = pick(get("XPAY_PUSH_TOKEN"), "")
	c.XPayPushAESKey = pick(get("XPAY_PUSH_AES_KEY"), "")
	c.AdminPasswordHash = pick(get("ADMIN_PASSWORD_HASH"), "")
	c.TokenTTL = parseI64(get("TOKEN_TTL"), c.TokenTTL)
	c.AdminSessionTTL = parseI64(get("ADMIN_SESSION_TTL"), c.AdminSessionTTL)
	return nil
}

func (c *Config) fromEnv() {
	c.ListenAddr = pick(os.Getenv("DJ_LISTEN"), c.ListenAddr)
	c.BaseURL = pick(os.Getenv("DJ_BASE_URL"), c.BaseURL)
	c.DataDir = pick(os.Getenv("DJ_DATA_DIR"), c.DataDir)
	c.WxAppID = pick(os.Getenv("DJ_WX_APP_ID"), c.WxAppID)
	c.WxAppSecret = pick(os.Getenv("DJ_WX_APP_SECRET"), c.WxAppSecret)
	c.OfferID = pick(os.Getenv("DJ_XPAY_OFFER_ID"), c.OfferID)
	c.XPayAppKey = pick(os.Getenv("DJ_XPAY_APP_KEY"), c.XPayAppKey)
	if os.Getenv("DJ_XPAY_SANDBOX") != "" {
		c.XPaySandbox = os.Getenv("DJ_XPAY_SANDBOX") == "1"
	}
	c.XPayPushToken = pick(os.Getenv("DJ_XPAY_PUSH_TOKEN"), c.XPayPushToken)
	c.XPayPushAESKey = pick(os.Getenv("DJ_XPAY_PUSH_AES_KEY"), c.XPayPushAESKey)
	c.AdminPasswordHash = pick(os.Getenv("DJ_ADMIN_PASSWORD_HASH"), c.AdminPasswordHash)
}

func XPayEnv(sandbox bool) int {
	if sandbox {
		return 1
	}
	return 0
}

func pick(v, def string) string {
	if v != "" {
		return v
	}
	return def
}

func parseI64(s string, def int64) int64 {
	var n int64
	var dots int
	for _, r := range s {
		if r < '0' || r > '9' {
			return def
		}
		n = n*10 + int64(r-'0')
		dots++
		if dots > 12 {
			return def
		}
	}
	if dots == 0 {
		return def
	}
	return n
}
