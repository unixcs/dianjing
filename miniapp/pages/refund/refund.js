// 我的退款记录：GET /refunds（服务端 join 出订单标题）。
// ios_waiting 表示该 iOS 渠道退款正在等 App Store 处理。
const { request, money, fmtTime, toastError } = require('../../utils/request');

const STATUS_TEXT = {
  applying: '处理中',
  approved: '已审核',
  refunded: '已退款',
  rejected: '已拒绝',
  failed: '退款失败',
};

Page({
  data: {
    list: [],
    loading: true,
  },

  onShow() {
    this.fetch();
  },

  onPullDownRefresh() {
    this.fetch().then(() => wx.stopPullDownRefresh());
  },

  fetch() {
    return request({ url: '/v2/api/refunds' })
      .then((d) => {
        this.setData({
          list: (d.refunds || []).map((r) => ({
            ...r,
            amountText: money(r.amount_cents),
            timeText: fmtTime(r.created_at),
            statusText: STATUS_TEXT[r.status] || r.status,
            iosText: r.ios_waiting ? '（苹果渠道处理中）' : '',
          })),
          loading: false,
        });
      })
      .catch((e) => { toastError(e); this.setData({ loading: false }); });
  },

  goOrder(e) {
    wx.navigateTo({ url: '/pages/order-detail/order-detail?id=' + e.currentTarget.dataset.order });
  },
});
