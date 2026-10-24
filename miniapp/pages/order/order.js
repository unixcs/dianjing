// 订单列表 tab：状态筛选；待支付订单可再次拉起支付或取消。
const { request, money, fmtTime, toastError } = require('../../utils/request');

const TABS = [
  { key: 'all', label: '全部' },
  { key: 'pending', label: '待支付' },
  { key: 'contact', label: '待联系' },
  { key: 'serving', label: '服务中' },
  { key: 'done', label: '已完成' },
];

Page({
  data: {
    tabs: TABS,
    active: 'all',
    orders: [],
    loading: false,
  },

  onShow() {
    this.fetch();
  },

  onPullDownRefresh() {
    this.fetch().then(() => wx.stopPullDownRefresh());
  },

  fetch() {
    this.setData({ loading: true });
    const q = this.data.active === 'all' ? '' : '?status=' + this.data.active;
    return request({ url: '/v2/api/orders' + q })
      .then((d) => {
        this.setData({
          orders: (d.orders || []).map((o) => ({
            ...o,
            totalText: money(o.total_cents),
            timeText: fmtTime(o.created_at),
          })),
          loading: false,
        });
      })
      .catch((e) => { this.setData({ loading: false }); toastError(e); });
  },

  onTab(e) {
    const key = e.currentTarget.dataset.key;
    if (key === this.data.active) return;
    this.setData({ active: key });
    this.fetch();
  },

  goDetail(e) {
    wx.navigateTo({ url: '/pages/order-detail/order-detail?id=' + e.currentTarget.dataset.id });
  },

  // 再次支付（待支付订单）
  onPay(e) {
    const id = e.currentTarget.dataset.id;
    const a = getApp();
    a.getPayCode()
      .then((code) => request({
        url: '/v2/api/orders/' + id + '/pay?login_code=' + code, method: 'POST',
      }))
      .then((params) => this.requestPayment(params, id))
      .catch(toastError);
  },

  requestPayment(params, id) {
    return new Promise((resolve, reject) => {
      wx.requestVirtualPayment({
        ...params,
        success: resolve,
        fail: (res) => reject(Object.assign(
          new Error(res.errMsg || '支付未完成'),
          { cancelled: /cancel/i.test(res.errMsg || '') },
        )),
      });
    })
      .then(() => request({ url: '/v2/api/orders/' + id + '/confirm-pay', method: 'POST' }))
      .then(() => {
        wx.showToast({ title: '支付成功', icon: 'success' });
        this.fetch();
      })
      .catch((e) => {
        if (e.cancelled) return;
        wx.showModal({
          title: '支付状态确认中',
          content: (e.message || '') + '，可稍后刷新订单查看进度',
          showCancel: false,
        });
        this.fetch();
      });
  },

  // 取消待支付订单
  onCancel(e) {
    const id = e.currentTarget.dataset.id;
    wx.showModal({
      title: '取消订单',
      content: '确定取消该待支付订单吗？',
      success: (res) => {
        if (!res.confirm) return;
        request({ url: '/v2/api/orders/' + id + '/cancel', method: 'POST' })
          .then(() => { wx.showToast({ title: '已取消', icon: 'success' }); this.fetch(); })
          .catch(toastError);
      },
    });
  },
});
