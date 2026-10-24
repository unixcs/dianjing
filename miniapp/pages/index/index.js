const { request, money } = require('../../utils/request');

Page({
  data: {
    notice: '',
    banners: [],
    categories: [],
    services: [],
    loading: true,
  },

  onLoad() {
    this.fetch();
  },

  onPullDownRefresh() {
    this.fetch().then(() => wx.stopPullDownRefresh());
  },

  fetch() {
    return request({ url: '/v2/api/home', noAuth: true })
      .then((d) => {
        const services = (d.services || []).map((s) => ({
          ...s,
          priceText: money(s.price_cents),
        }));
        this.setData({
          notice: d.notice || '',
          banners: d.banners || [],
          categories: d.category || [],
          services,
          loading: false,
        });
      })
      .catch(() => this.setData({ loading: false }));
  },

  onBannerTap(e) {
    const url = e.currentTarget.dataset.url;
    if (url) wx.setClipboardData({ data: url });
  },

  goCategory() {
    wx.switchTab({ url: '/pages/category/category' });
  },

  goDetail(e) {
    wx.navigateTo({ url: '/pages/goods-detail/goods-detail?id=' + e.currentTarget.dataset.id });
  },

  goDocs() {
    wx.navigateTo({ url: '/pages/docs/docs?key=service-policy&title=服务政策' });
  },
});
