// 我的：登录态信息 + 入口列表（订单在 tab，退款/反馈/协议为跳转）。
Page({
  data: {
    user: null,
  },

  onShow() {
    this.refresh();
  },

  refresh() {
    const app = getApp();
    app.ensureLogin()
      .then(() => {
        this.setData({ user: app.globalData.user });
      })
      .catch(() => {
        // 静默登录失败（常见于开发者工具）：仍可浏览，下单时再重试
        this.setData({ user: null });
      });
  },

  go(e) {
    wx.navigateTo({ url: e.currentTarget.dataset.url });
  },

  goOrders() {
    wx.switchTab({ url: '/pages/order/order' });
  },

  about() {
    wx.showModal({
      title: '观星电竞',
      content: '游戏技术指导服务平台\n有任何问题请通过“意见反馈”联系我们',
      showCancel: false,
    });
  },
});
