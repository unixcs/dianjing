// Package xpay implements the WeChat mini-program virtual payment
// (微信虚拟支付) server side: request signing, query_order /
// refund_order / notify_provide_goods and message-push verification.
//
// Signing formulas are ported 1:1 from the legacy PHP implementation
// (yoshop2.0 app/common/library/wechat/VirtualPayment.php and
// app/api/service/cashier/Payment.php) and must not be redesigned.
package xpay

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	envProduction = 0
	envSandbox    = 1

	modeShortSeriesGoods = "short_series_goods"
	currencyCNY          = "CNY"

	uriQueryOrder        = "/xpay/query_order"
	uriRefundOrder       = "/xpay/refund_order"
	uriNotifyProvideGood = "/xpay/notify_provide_goods"
)

// Env helpers shared with api/app packages.
func EnvStr(sandbox bool) string {
	if sandbox {
		return "1"
	}
	return "0"
}

func EnvSandboxInt(sandbox bool) int {
	if sandbox {
		return envSandbox
	}
	return envProduction
}

// Str reads a string field from a decoded xpay response map.
func Str(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}

// QueryOrderPaid inspects a query_order response for a paid state.
// xpay replies vary by field naming; accept the known variants.
func QueryOrderPaid(resp map[string]any) bool {
	for _, key := range []string{"pay_status", "state", "order_state"} {
		switch v := resp[key].(type) {
		case string:
			if strings.EqualFold(v, "SUCCESS") || v == "1" || v == "PAID" {
				return true
			}
		case float64:
			if v == 1 {
				return true
			}
		}
	}
	if order, ok := resp["order"].(map[string]any); ok {
		return QueryOrderPaid(order)
	}
	return false
}

// Client talks to api.weixin.qq.com/xpay on behalf of one miniapp.
type Client struct {
	AppID     string
	AppSecret string
	AppKey    string // xpay offer app key
	Sandbox   bool

	hc *http.Client

	mu          sync.Mutex
	accessToken string
	tokenExpAt  int64
}

func New(appID, appSecret, appKey string, sandbox bool) *Client {
	return &Client{
		AppID:     appID,
		AppSecret: appSecret,
		AppKey:    appKey,
		Sandbox:   sandbox,
		hc:        &http.Client{Timeout: 15 * time.Second},
	}
}

// ---- signing (ported formulas) ----

// PaySig computes HMAC-SHA256(uri & body, appKey) — used for xpay
// server APIs and for the client-side wx.requestVirtualPayment paySig.
func PaySig(uri, body, appKey string) string {
	mac := hmac.New(sha256.New, []byte(appKey))
	mac.Write([]byte(uri + "&" + body))
	return hex.EncodeToString(mac.Sum(nil))
}

// UserSignature computes HMAC-SHA256(signData, sessionKey) — the
// `signature` field of wx.requestVirtualPayment.
func UserSignature(signData, sessionKey string) string {
	mac := hmac.New(sha256.New, []byte(sessionKey))
	mac.Write([]byte(signData))
	return hex.EncodeToString(mac.Sum(nil))
}

// SignData is the JSON object passed to wx.requestVirtualPayment as
// `signData`. Field names follow the xpay contract exactly.
type SignData struct {
	OfferID      string `json:"offerId"`
	BuyQuantity  int    `json:"buyQuantity"`
	Env          int    `json:"env"`
	CurrencyType string `json:"currencyType"`
	ProductID    string `json:"productId"`
	GoodsPrice   int64  `json:"goodsPrice"` // 分
	OutTradeNo   string `json:"outTradeNo"`
	Attach       string `json:"attach,omitempty"`
}

// PayParams is everything the miniapp needs to call
// wx.requestVirtualPayment.
type PayParams struct {
	Mode      string `json:"mode"`
	SignData  string `json:"signData"`
	PaySig    string `json:"paySig"`
	Signature string `json:"signature"`
}

// BuildPayParams assembles the client payment parameters. paySig uses
// the midas signature URI (fixed by WeChat regardless of our HTTP path).
func BuildPayParams(c *Client, sd SignData, sessionKey string) (*PayParams, string, error) {
	if sessionKey == "" {
		return nil, "", fmt.Errorf("session_key 为空，无法签名，请重新登录")
	}
	body, err := json.Marshal(sd)
	if err != nil {
		return nil, "", err
	}
	signData := string(body)
	const midasSigURI = "requestVirtualPayment" // legacy: no leading slash (Payment.php VIRTUAL_PAYMENT_URI)
	return &PayParams{
		Mode:      modeShortSeriesGoods,
		SignData:  signData,
		PaySig:    PaySig(midasSigURI, signData, c.AppKey),
		Signature: UserSignature(signData, sessionKey),
	}, signData, nil
}

// ---- access token ----

func (c *Client) AccessToken() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.accessToken != "" && time.Now().Unix() < c.tokenExpAt-120 {
		return c.accessToken, nil
	}
	url := fmt.Sprintf("https://api.weixin.qq.com/cgi-bin/token?grant_type=client_credential&appid=%s&secret=%s",
		c.AppID, c.AppSecret)
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		ErrCode     int    `json:"errcode"`
		ErrMsg      string `json:"errmsg"`
	}
	if err := c.getJSON(url, &out); err != nil {
		return "", err
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("获取 access_token 失败(%d): %s", out.ErrCode, out.ErrMsg)
	}
	c.accessToken = out.AccessToken
	c.tokenExpAt = time.Now().Unix() + int64(out.ExpiresIn)
	return c.accessToken, nil
}

// ---- signed xpay posts ----

// APIError is a WeChat xpay error reply.
type APIError struct {
	ErrCode int    `json:"errcode"`
	ErrMsg  string `json:"errmsg"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("xpay(%d): %s", e.ErrCode, e.ErrMsg)
}

func (c *Client) signedPost(uri string, payload any, out any) error {
	tok, err := c.AccessToken()
	if err != nil {
		return err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if err := c.postOnce(uri, tok, body, out); err != nil {
		return err
	}
	// retry once on invalid access_token (40001) like the PHP port
	return nil
}

func (c *Client) postOnce(uri, token string, body []byte, out any) error {
	url := fmt.Sprintf("https://api.weixin.qq.com%s?access_token=%s&pay_sig=%s",
		uri, token, PaySig(uri, string(body), c.AppKey))
	resp, err := c.hc.Post(url, "application/json", strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("xpay 响应解析失败: %s", truncate(raw, 300))
	}
	var ae APIError
	_ = json.Unmarshal(raw, &ae)
	if ae.ErrCode != 0 && ae.ErrCode != 2000 { // 2000 = xpay "ok" code family guard
		return &ae
	}
	return nil
}

// QueryOrder calls /xpay/query_order to reconcile an order by out_trade_no.
func (c *Client) QueryOrder(outTradeNo, envStr string) (map[string]any, error) {
	payload := map[string]any{"out_trade_no": outTradeNo, "env": envStr}
	var out map[string]any
	if err := c.signedPost(uriQueryOrder, payload, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// RefundOrder calls /xpay/refund_order.
func (c *Client) RefundOrder(payOrderID string, refundFee int64) (map[string]any, error) {
	payload := map[string]any{
		"pay_order_id": payOrderID,
		"refund_fee":   refundFee, // 分
	}
	var out map[string]any
	if err := c.signedPost(uriRefundOrder, payload, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// NotifyProvideGoods calls /xpay/notify_provide_goods — the exception
// that carries access_token only, no pay_sig (ported behaviour).
func (c *Client) NotifyProvideGoods(env int, payOrderID, outTradeNo string) (map[string]any, error) {
	tok, err := c.AccessToken()
	if err != nil {
		return nil, err
	}
	payload := map[string]any{
		"env":          env,
		"pay_order_id": payOrderID,
		"out_trade_no": outTradeNo,
	}
	body, _ := json.Marshal(payload)
	url := fmt.Sprintf("https://api.weixin.qq.com%s?access_token=%s", uriNotifyProvideGood, tok)
	resp, err := c.hc.Post(url, "application/json", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("notify_provide_goods 响应解析失败: %s", truncate(raw, 300))
	}
	return out, nil
}

func truncate(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n]) + "..."
	}
	return string(b)
}

func (c *Client) getJSON(url string, out any) error {
	resp, err := c.hc.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return json.Unmarshal(raw, out)
}
