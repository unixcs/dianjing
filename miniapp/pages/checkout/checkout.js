// 确认订单：创建订单 → POST /orders/:id/pay 拿签名参数 → wx.requestVirtualPayment
// → 成功回执 POST /orders/:id/confirm-pay（服务端查单确认后才算支付成功）。
// 查单未确认时跳订单详情页轮询（60s ticker 兜底收敛）。
const { request, money, toastError } = require('../../utils/request');

Page({
  data: {
    svc: null,
    quantity: 1,
    contact: '',
    submitting: false,
    payStatus: '', // '' | 'paying' | 'verifying'
  },

  onLoad(options) {
    this.serviceId = Number(options.service_id);
    this.fetch();
  },

  fetch() {
    request({ url: '/v2/api/services/' + this.serviceId, noAuth: true })
      .then((d) => {
        const svc = d.service;
        this.setData({
          svc: {
            ...svc,
            priceText: money(svc.price_cents),
            totalText: money(svc.price_cents * this.data.quantity),
          },
        });
        this.updateTotal();
      })
      .catch(toastError);
  },

  updateTotal() {
    const { svc, quantity } = this.data;
    if (!svc) return;
    this.setData({ 'svc.totalText': money(svc.price_cents * quantity) });
  },

  onQuantity(e) {
    const v = Math.min(10, Math.max(1, Number(e.detail.value) || 1));
    this.setData({ quantity: v });
    this.updateTotal();
  },

  onContact(e) {
    this.setData({ contact: e.detail.value });
  },

  submit() {
    if (this.data.submitting || this.data.payStatus) return;
    const { svc, quantity, contact } = this.data;
    if (!svc) return;
    this.setData({ submitting: true });
    let orderId = 0;
    request({
      url: '/v2/api/orders', method: 'POST',
      data: { service_id: svc.id, quantity, contact, client_token: this.clientToken() },
    })
      .then((d) => {
        orderId = d.order_id;
        return request({ url: '/v2/api/orders/' + orderId + '/pay', method: 'POST' });
      })
      .then((params) => {
        this.setData({ submitting: false, payStatus: 'paying' });
        return new Promise((resolve, reject) => {
          wx.requestVirtualPayment({
            ...params,
            success: resolve,
            fail: (res) => reject(Object.assign(new Error(res.errMsg || '支付未完成'), { cancelled: true })),
          });
        });
      })
      .then(() => {
        this.setData({ payStatus: 'verifying' });
        return request({ url: '/v2/api/orders/' + orderId + '/confirm-pay', method: 'POST' });
      })
      .then(() => {
        wx.showToast({ title: '支付成功', icon: 'success' });
        wx.redirectTo({ url: '/pages/order-detail/order-detail?id=' + orderId });
      })
      .catch((e) => {
        this.setData({ payStatus: '', submitting: false });
        if (e.cancelled) {
          // 用户取消：订单已创建（待支付），去详情页可再次支付或取消
          if (orderId) {
            wx.showModal({
              title: '支付未完成',
              content: '订单已创建，可稍后在订单中继续支付',
              showCancel: false,
              success: () => wx.redirectTo({ url: '/pages/order-detail/order-detail?id=' + orderId }),
            });
          }
          return;
        }
        toastError(e);
        if (orderId) {
          // 支付参数正常但 confirm-pay 查单未确认等情况：跳详情页等待收敛
          wx.showModal({
            title: '支付状态确认中',
            content: (e.message || '') + '，可到订单详情查看进度',
            showCancel: false,
            success: () => wx.redirectTo({ url: '/pages/order-detail/order-detail?id=' + orderId }),
          });
        }
      });
  },

  // 防重复下单：每次进入页面生成一次
  clientToken() {
    if (!this._ct) {
      const hex = () => Math.floor(Math.random() * 0xffff).toString(16).padStart(4, '0');
      this._ct = [hex(), hex(), hex(), hex()].join('');
    }
    return this._ct;
  },

  goPolicy() {
    wx.navigateTo({ url: '/pages/docs/docs?key=service-policy&title=服务政策' });
  },
});
