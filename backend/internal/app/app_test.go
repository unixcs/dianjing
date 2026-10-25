package app

import "testing"

// 退款模式引擎矩阵：ported from Order::getServiceRefundMode。
func TestRefundModeFor(t *testing.T) {
	cases := []struct {
		name   string
		status int
		payAt  int64
		active bool
		want   string
	}{
		{"待联系已付 → auto", OrderContact, 100, false, RefundModeAuto},
		{"服务中已付 → audit", OrderServing, 100, false, RefundModeAudit},
		{"已完成 → none", OrderDone, 100, false, RefundModeNone},
		{"待支付 → none", OrderPendingPay, 0, false, RefundModeNone},
		{"已取消 → none", OrderCancelled, 0, false, RefundModeNone},
		{"退款中 → none", OrderRefunding, 100, false, RefundModeNone},
		{"已退款 → none", OrderRefunded, 100, false, RefundModeNone},
		{"待联系但无 pay_time → none", OrderContact, 0, false, RefundModeNone},
		{"已有活跃退款 → none", OrderContact, 100, true, RefundModeNone},
	}
	for _, c := range cases {
		o := &Order{Status: c.status, PayTime: c.payAt}
		if got := RefundModeFor(o, c.active); got != c.want {
			t.Fatalf("%s: got %s want %s", c.name, got, c.want)
		}
	}
}

// 指纹规范化：字段顺序无关 + 类型稳定，重放同 payload 必须命中同指纹。
func TestInquiryFingerprintStable(t *testing.T) {
	a := map[string]any{"z": "1", "a": float64(2), "m": map[string]any{"y": true, "x": "s"}}
	b := map[string]any{"a": float64(2), "z": "1", "m": map[string]any{"x": "s", "y": true}}
	fa, fb := InquiryFingerprint(a), InquiryFingerprint(b)
	if fa == "" || fa != fb {
		t.Fatalf("指纹不稳定: %s vs %s", fa, fb)
	}
	if InquiryFingerprint(map[string]any{"a": "1"}) == InquiryFingerprint(map[string]any{"a": "2"}) {
		t.Fatal("不同 payload 指纹相同")
	}
}

// Apple 问询服务阶段映射。
func TestServiceStage(t *testing.T) {
	cases := map[int]string{
		OrderContact: StageNotStarted, OrderServing: StageInProgress,
		OrderDone: StageCompleted, OrderPendingPay: StageUnknown,
	}
	for st, want := range cases {
		if got := serviceStage(&Order{Status: st}); got != want {
			t.Fatalf("status %d: got %s want %s", st, got, want)
		}
	}
}

func TestTruncateStr(t *testing.T) {
	if TruncateStr("abcdef", 3) != "abc" {
		t.Fatal("截断错误")
	}
	if TruncateStr("中文测试", 2) != "中文" {
		t.Fatal("应按 rune 截断")
	}
	if TruncateStr("ab", 5) != "ab" {
		t.Fatal("短串不应被改")
	}
}
