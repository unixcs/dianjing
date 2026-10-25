package xpay

import (
	"encoding/json"
	"testing"
)

// 签名公式回归锚点：期望值由独立实现（Python hmac/hashlib）预计算，
// 与老 PHP 实现逐字节对齐——公式或字段顺序一变就会红。
func TestPaySigFormula(t *testing.T) {
	got := PaySig("requestVirtualPayment",
		`{"offerId":"1450568898","buyQuantity":1,"env":1,"currencyType":"CNY","productId":"svc_1","goodsPrice":9900,"outTradeNo":"17700000000000001"}`,
		"test-app-key")
	want := "00654ba81b79076d8658279a2636972d146f2b444b0fdcfeb72328d6707c7a5c"
	if got != want {
		t.Fatalf("PaySig = %s, want %s", got, want)
	}
}

func TestUserSignatureFormula(t *testing.T) {
	got := UserSignature(
		`{"offerId":"1450568898","buyQuantity":1,"env":1,"currencyType":"CNY","productId":"svc_1","goodsPrice":9900,"outTradeNo":"17700000000000001"}`,
		"test-session-key")
	want := "e329b16e72a007c944f797f76a5177f09ddf53ce9af169a5fc83145c23ab9b9a"
	if got != want {
		t.Fatalf("UserSignature = %s, want %s", got, want)
	}
}

// BuildPayParams 的 signData 字段顺序必须与 SignData 结构体声明一致
// （老 PHP 对 signData 做全串 HMAC，字段顺序变了签名就错）。
func TestBuildPayParamsSignDataOrder(t *testing.T) {
	c := New("wx1", "s", "test-app-key", true)
	p, signData, err := BuildPayParams(c, SignData{
		OfferID: "1450568898", BuyQuantity: 1, Env: 1, CurrencyType: "CNY",
		ProductID: "svc_1", GoodsPrice: 9900, OutTradeNo: "17700000000000001",
	}, "test-session-key")
	if err != nil {
		t.Fatal(err)
	}
	if p.Mode != "short_series_goods" {
		t.Fatalf("mode = %s", p.Mode)
	}
	if signData != p.SignData || p.PaySig != PaySig("requestVirtualPayment", signData, "test-app-key") {
		t.Fatal("paySig 与 signData 不自洽")
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(signData), &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"offerId", "buyQuantity", "env", "currencyType", "productId", "goodsPrice", "outTradeNo"} {
		if _, ok := m[k]; !ok {
			t.Fatalf("signData 缺字段 %s", k)
		}
	}
	if _, ok := m["attach"]; ok {
		t.Fatal("attach 为空时不应出现在 signData")
	}
	if _, _, err := BuildPayParams(c, SignData{}, ""); err == nil {
		t.Fatal("空 session_key 应报错而非返回参数")
	}
}

func TestVerifyPlaintextSig(t *testing.T) {
	sig := "041a070ac6b01e1fc988f620ba78241808d0a74c" // sha1(sort("1770000000","abc123","tok-e2e"))
	if !VerifyPlaintextSig("tok-e2e", sig, "1770000000", "abc123") {
		t.Fatal("合法签名被拒")
	}
	if VerifyPlaintextSig("tok-e2e", sig, "1770000001", "abc123") {
		t.Fatal("时间戳变了签名不应通过")
	}
	if VerifyPlaintextSig("other", sig, "1770000000", "abc123") {
		t.Fatal("token 变了签名不应通过")
	}
}

func TestQueryOrderPaid(t *testing.T) {
	cases := []struct {
		status any
		want   bool
	}{
		{float64(2), true}, {float64(3), true},
		{float64(5), false}, {float64(8), false}, {float64(6), false},
		{nil, false},
	}
	for _, c := range cases {
		resp := map[string]any{}
		if c.status != nil {
			resp["order"] = map[string]any{"status": c.status}
		}
		if got := QueryOrderPaid(resp); got != c.want {
			t.Fatalf("status %v: paid=%v want %v", c.status, got, c.want)
		}
	}
	if QueryOrderOrderType(map[string]any{"order": map[string]any{"order_type": float64(7)}}) != 7 {
		t.Fatal("order_type=7 (iOS) 应识别")
	}
}
