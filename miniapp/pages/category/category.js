const { request, money, toastError } = require('../../utils/request');

Page({
  data: {
    categories: [],
    activeCat: 0, // 0 = 全部
    services: [],
    loading: false,
  },

  onLoad() {
    // 首次进入 + 每次返回本页都由 onShow 统一刷新
  },

  onShow() {
    request({ url: '/v2/api/categories', noAuth: true })
      .then((d) => this.setData({ categories: d.categories || [] }))
      .catch(() => {});
    this.fetch();
  },

  fetch() {
    this.setData({ loading: true });
    const q = this.data.activeCat ? '?category_id=' + this.data.activeCat + '&limit=50' : '?limit=50';
    return request({ url: '/v2/api/services' + q, noAuth: true })
      .then((d) => {
        this.setData({
          services: (d.services || []).map((s) => ({ ...s, priceText: money(s.price_cents) })),
          loading: false,
        });
      })
      .catch((e) => { this.setData({ loading: false }); toastError(e); });
  },

  onPick(e) {
    const id = Number(e.currentTarget.dataset.id);
    if (id === this.data.activeCat) return;
    this.setData({ activeCat: id });
    this.fetch();
  },

  goDetail(e) {
    wx.navigateTo({ url: '/pages/goods-detail/goods-detail?id=' + e.currentTarget.dataset.id });
  },
});
