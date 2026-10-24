// 订单详情：打开时后端已做惰性查单收敛（detail 接口返回最新状态）。
// 待支付：去支付 / 取消；可退款：申请退款（auto 模式直接退，audit 模式进审核）。
// "确认支付结果" 按钮：confirm-pay 让服务端向微信查单收敛。
const { request, money, fmtTime, toastError } = require('../../utils/request');

Page({
  data: {
    o: null,
    loading: true,
  },

  onLoad(options) {
    this.id = Number(options.id);
  },

  onShow() {
    this.fetch();
  },

  fetch() {
    return request({ url: '/v2/api/orders/' + this.id })
      .then((d) => {
        const o = d.order;
        this.setData({
          o: {
            ...o,
            priceText: money(o.price_cents),
            totalText: money(o.total_cents),
            timeText: fmtTime(o.created_at),
            payTimeText: fmtTime(o.pay_time),
          },
          loading: false,
        });
      })
      .catch((e) => { toastError(e); this.setData({ loading: false }); });
  },

  // 支付收敛兜底：用户点了支付但 confirm 未确认时手动触发
  onConfirmPay() {
    request({ url: '/v2/api/orders/' + this.id + '/confirm-pay', method: 'POST' })
      .then(() => { wx.showToast({ title: '支付已确认', icon: 'success' }); this.fetch(); })
      .catch((e) => { toastError(e); this.fetch(); });
  },

  onPay() {
    request({ url: '/v2/api/orders/' + this.id + '/pay', method: 'POST' })
      .then((params) => new Promise((resolve, reject) => {
        wx.requestVirtualPayment({
          ...params,
          success: resolve,
          fail: (res) => reject(Object.assign(new Error(res.errMsg || '支付未完成'), { cancelled: true })),
        });
      }))
      .then(() => request({ url: '/v2/api/orders/' + this.id + '/confirm-pay', method: 'POST' }))
      .then(() => { wx.showToast({ title: '支付成功', icon: 'success' }); this.fetch(); })
      .catch((e) => { if (!e.cancelled) toastError(e); this.fetch(); });
  },

  onCancel() {
    wx.showModal({
      title: '取消订单',
      content: '确定取消该待支付订单吗？',
      success: (res) => {
        if (!res.confirm) return;
        request({ url: '/v2/api/orders/' + this.id + '/cancel', method: 'POST' })
          .then(() => { wx.showToast({ title: '已取消', icon: 'success' }); this.fetch(); })
          .catch(toastError);
      },
    });
  },

  onRefund() {
    const o = this.data.o;
    const hint = o.refund_mode === 'audit'
      ? '退款申请将提交客服审核，审核通过后原路退回'
      : '按平台规则将直接原路退回';
    wx.showModal({
      title: '申请退款',
      content: hint,
      editable: true,
      placeholderText: '退款原因（选填）',
      success: (res) => {
        if (!res.confirm) return;
        request({
          url: '/v2/api/orders/' + this.id + '/refund', method: 'POST',
          data: { reason: res.content || '' },
        })
          .then(() => {
            wx.showToast({ title: '已提交申请', icon: 'success' });
            this.fetch();
          })
          .catch(toastError);
      },
    });
  },

  goService() {
    if (this.data.o) {
      wx.navigateTo({ url: '/pages/goods-detail/goods-detail?id=' + this.data.o.id });
    }
  },
});
