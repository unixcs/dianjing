// WeChat message-push endpoint (微信消息推送): GET URL verification plus
// the three xpay events, ported from app/api/service/Notify.php:
//   - xpay_goods_deliver_notify            => payment success
//   - xpay_refund_notify                   => refund result
//   - xpay_subscribe_ios_refund_query_notify => Apple refund inquiry
//
// Security: plaintext mode verifies sha1(token,timestamp,nonce[,body]);
// secure mode decrypts AES and requires encrypted replies for non-empty
// bodies (iOS inquiry). Unknown events and failures reply ErrCode:1 so
// WeChat retries; the iOS inquiry always answers a decision, defaulting
// to safe-reject (result_code=1) on any failure.
package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"dianjing/backend/internal/app"
	"dianjing/backend/internal/xpay"
)

const (
	virtualEventPay        = "xpay_goods_deliver_notify"
	virtualEventRefund     = "xpay_refund_notify"
	virtualEventIOSInquiry = "xpay_subscribe_ios_refund_query_notify"
)

func (s *Server) pushToken() string  { return s.Cfg.XPayPushToken }
func (s *Server) pushAESKey() string { return s.Cfg.XPayPushAESKey }
func (s *Server) pushAppID() string  { return s.Cfg.WxAppID }

// handleWxMsg is mounted at POST+GET /wx/msg (nginx: /wx/ = this server).
func (s *Server) handleWxMsg(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.handleWxMsgVerify(w, r)
		return
	}
	s.handleWxMsgPost(w, r)
}

// GET verification: echo echostr after signature check.
func (s *Server) handleWxMsgVerify(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	sig, ts, nonce, echo := q.Get("signature"), q.Get("timestamp"), q.Get("nonce"), q.Get("echostr")
	if s.pushToken() == "" || sig == "" || ts == "" || nonce == "" || echo == "" {
		http.Error(w, "参数不完整", http.StatusBadRequest)
		return
	}
	if !xpay.VerifyPlaintextSig(s.pushToken(), sig, ts, nonce) {
		http.Error(w, "签名校验失败", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	io.WriteString(w, echo)
}

func (s *Server) handleWxMsgPost(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ErrCode": 1, "ErrMsg": "FAIL"})
		return
	}
	q := r.URL.Query()
	ts, nonce := q.Get("timestamp"), q.Get("nonce")

	// Decode body: JSON or XML-ish; secure mode carries an Encrypt field.
	data := decodePushBody(string(raw))
	if data == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ErrCode": 1, "ErrMsg": "报文异常"})
		return
	}

	encrypted := ""
	if v, ok := data["Encrypt"].(string); ok {
		encrypted = v
	}

	var payload map[string]any
	if encrypted != "" {
		// Secure mode: msg_signature over the encrypt blob, AES decrypt.
		mp := &xpay.MessagePush{AppID: s.pushAppID(), Token: s.pushToken(), AESKey: s.pushAESKey()}
		plain, err := mp.Decrypt(encrypted, q.Get("msg_signature"), ts, nonce)
		if err != nil {
			s.St.Event("push.decrypt_fail", "", 0, fmt.Sprintf(`{"error":%q}`, err.Error()))
			writeJSON(w, http.StatusOK, map[string]any{"ErrCode": 1, "ErrMsg": "解密失败"})
			return
		}
		payload = decodePushBody(string(plain))
	} else {
		// Plaintext mode: signature over token/timestamp/nonce only — the
		// body is NOT covered, so replay resistance comes from a ±5min
		// timestamp window plus a nonce cache (a captured triple cannot be
		// replayed later with a forged body).
		if !s.verifyPushSource(q.Get("signature"), ts, nonce) {
			writeJSON(w, http.StatusOK, map[string]any{"ErrCode": 1, "ErrMsg": "签名校验失败"})
			return
		}
		if tsi, err := strconv.ParseInt(ts, 10, 64); err != nil ||
			abs64(time.Now().Unix()-tsi) > 300 {
			s.St.Event("push.stale_ts", "", 0, ts)
			writeJSON(w, http.StatusOK, map[string]any{"ErrCode": 1, "ErrMsg": "时间戳过期"})
			return
		}
		if s.pushNonceSeen(ts + "|" + nonce) {
			// WeChat retries with the same nonce after non-zero ErrCode
			// replies; acknowledge without reprocessing (ticker converges).
			writeJSON(w, http.StatusOK, map[string]any{"ErrCode": 0, "ErrMsg": "success"})
			return
		}
		payload = data
	}
	if payload == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ErrCode": 1, "ErrMsg": "报文异常"})
		return
	}

	event, _ := payload["Event"].(string)
	s.St.Event("push."+event, "", 0, app.TruncateStr(string(raw), 500))

	switch event {
	case virtualEventPay:
		s.onPayNotify(payload)
		replyPush(w, map[string]any{"ErrCode": 0, "ErrMsg": "success"})
	case virtualEventRefund:
		s.onRefundNotify(payload)
		replyPush(w, map[string]any{"ErrCode": 0, "ErrMsg": "success"})
	case virtualEventIOSInquiry:
		code, info, evidence := s.onIOSInquiry(payload)
		// iOS inquiry replies carry a body; secure mode must encrypt it.
		body := map[string]any{"result_code": code, "result_info": info, "evidence": evidence}
		if encrypted != "" {
			mp := &xpay.MessagePush{AppID: s.pushAppID(), Token: s.pushToken(), AESKey: s.pushAESKey()}
			enc, err := mp.Encrypt(mustJSON(body), nonce, fmt.Sprintf("%d", time.Now().Unix()))
			if err != nil {
				// Fail closed: plain rejection rather than no reply.
				replyPush(w, map[string]any{"result_code": 1, "result_info": "建议拒绝退款", "evidence": "回包加密失败"})
				return
			}
			replyPush(w, enc)
			return
		}
		replyPush(w, body)
	default:
		s.St.Event("push.unknown", "", 0, app.TruncateStr(event, 100))
		writeJSON(w, http.StatusOK, map[string]any{"ErrCode": 1, "ErrMsg": "未知事件"})
	}
}

// onPayNotify converges a paid order (xpay_goods_deliver_notify).
// OutTradeNo == our order_no.
func (s *Server) onPayNotify(payload map[string]any) {
	outTradeNo := firstPushString(payload, "OutTradeNo", "out_trade_no")
	if outTradeNo == "" {
		return
	}
	o, err := s.App.OrderByNo(outTradeNo)
	if err != nil {
		s.St.Event("push.pay_unknown_order", "", 0, app.TruncateStr(outTradeNo, 100))
		return
	}
	if !envMatches(s.Cfg.XPaySandbox, payload) {
		s.St.Event("push.env_mismatch", "order", o.ID, "")
		return
	}
	txnID := ""
	if info, ok := payload["WeChatPayInfo"].(map[string]any); ok {
		txnID = firstPushString(info, "TransactionId")
	}
	txnID = app.FirstNonEmpty(txnID, firstPushString(payload, "TransactionId"))
	if err := s.App.MarkPaid(o.ID, firstPushString(payload, "PayOrderId"), txnID); err != nil {
		s.St.Event("push.pay_converge_fail", "order", o.ID, fmt.Sprintf(`{"error":%q}`, err.Error()))
	}
}

// onRefundNotify converges an xpay refund result (xpay_refund_notify).
// MchOrderId == our order_no. Only RetCode==0 counts as success
// (ported isSuccessfulVirtualRefundNotify: strict "0").
func (s *Server) onRefundNotify(payload map[string]any) {
	orderNo := firstPushString(payload, "MchOrderId", "mch_order_id", "OutTradeNo")
	if orderNo == "" {
		return
	}
	o, err := s.App.OrderByNo(orderNo)
	if err != nil {
		s.St.Event("push.refund_unknown_order", "", 0, app.TruncateStr(orderNo, 100))
		return
	}
	retCode, hasCode := payload["RetCode"]
	success := hasCode && (retCode == float64(0) || retCode == "0")
	if !success {
		s.St.Event("push.refund_nonzero", "order", o.ID, fmt.Sprintf(`{"ret":%v}`, retCode))
		return
	}
	if !envMatches(s.Cfg.XPaySandbox, payload) {
		s.St.Event("push.env_mismatch", "order", o.ID, "")
		return
	}
	s.App.MarkRefundedFromPush(o.OrderNo)
}

// onIOSInquiry decides the Apple refund inquiry. Errors must fail closed
// (result_code=1) — including panics.
func (s *Server) onIOSInquiry(payload map[string]any) (code int, info, evidence string) {
	code, info, evidence = 1, "建议拒绝退款", "服务端暂时无法完成订单、审核与服务状态校验，建议本次拒绝并等待后续认证问询重试"
	defer func() {
		if rec := recover(); rec != nil {
			s.St.Event("push.inquiry_panic", "", 0, fmt.Sprintf("%v", rec))
			code, info, evidence = 1, "建议拒绝退款", "服务端异常，建议拒绝退款"
		}
	}()
	payOrderID := firstPushString(payload, "pay_order_id", "PayOrderId", "PayOrderID", "MchOrderId", "OutTradeNo")
	code, info, evidence = s.App.HandleIOSInquiry(payOrderID, payload)
	s.St.Event("push.inquiry_decision", "", 0,
		fmt.Sprintf(`{"pay_order_id":%q,"result_code":%d}`, payOrderID, code))
	return code, info, evidence
}

func (s *Server) verifyPushSource(signature, ts, nonce string) bool {
	token := s.pushToken()
	if token == "" || signature == "" || ts == "" || nonce == "" {
		return false
	}
	return xpay.VerifyPlaintextSig(token, signature, ts, nonce)
}

// pushNonceSeen records a (timestamp|nonce) pair for 10 minutes and
// reports whether it was already seen. Single-process in-memory cache is
// sufficient: WeChat retries land on this one instance.
func (s *Server) pushNonceSeen(key string) bool {
	s.pushMu.Lock()
	defer s.pushMu.Unlock()
	if s.pushNonce == nil {
		s.pushNonce = map[string]int64{}
	}
	now := time.Now().Unix()
	for k, exp := range s.pushNonce {
		if exp < now {
			delete(s.pushNonce, k)
		}
	}
	if _, dup := s.pushNonce[key]; dup {
		return true
	}
	s.pushNonce[key] = now + 600
	return false
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// replyPush writes the JSON response body (the miniapp push config uses
// JSON; legacy XML responses are out of scope).
func replyPush(w http.ResponseWriter, body any) {
	writeJSON(w, http.StatusOK, body)
}

// envMatches checks a push Env against the configured sandbox flag.
// Missing Env is fatal (ported assertVirtualNotifyEnv): a pay/refund push
// without a verifiable env must not converge money state.
func envMatches(sandbox bool, payload map[string]any) bool {
	env, ok := payload["Env"].(float64)
	if !ok {
		return false
	}
	return int(env) == xpay.EnvSandboxInt(sandbox)
}

// Minimal XML parse: <tag>value</tag> pairs at depth 1 — enough for
// WeChat encrypted XML pushes (<xml><Encrypt>..</Encrypt></xml>).
var tagRe = regexp.MustCompile(`<([A-Za-z_][A-Za-z0-9_.-]*)>([^<]+)</[A-Za-z_][A-Za-z0-9_.-]*>`)

func decodePushBody(raw string) map[string]any {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	if strings.HasPrefix(trimmed, "{") {
		var m map[string]any
		if json.Unmarshal([]byte(trimmed), &m) == nil {
			return m
		}
		return nil
	}
	m := map[string]any{}
	for _, pair := range tagRe.FindAllStringSubmatch(trimmed, -1) {
		m[pair[1]] = pair[2]
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

func firstPushString(payload map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := payload[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
