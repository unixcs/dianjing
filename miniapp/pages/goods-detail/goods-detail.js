const { request, money, toastError } = require('../../utils/request');

Page({
  data: {
    svc: null,
    loading: true,
    buying: false,
  },

  onLoad(options) {
    this.id = Number(options.id);
    this.fetch();
  },

  fetch() {
    request({ url: '/v2/api/services/' + this.id, noAuth: true })
      .then((d) => {
        this.setData({
          svc: { ...d.service, priceText: money(d.service.price_cents) },
          loading: false,
        });
      })
      .catch((e) => { toastError(e); this.setData({ loading: false }); });
  },

  buy() {
    if (this.data.buying) return;
    this.setData({ buying: true });
    const app = getApp();
    // 下单需要登录；静默登录失败（如开发者工具里无真实 code）时提示。
    app.ensureLogin()
      .then(() => {
        this.setData({ buying: false });
        wx.navigateTo({ url: '/pages/checkout/checkout?service_id=' + this.id });
      })
      .catch((e) => {
        this.setData({ buying: false });
        toastError(e);
      });
  },
});
