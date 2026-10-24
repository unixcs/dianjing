// 观星电竞 — 原生小程序入口。
// 登录策略：启动时若本地无 token 则静默 wx.login 换 token；
// token 失效（401）由 request 统一触发重登后重放一次。
const { request } = require('./utils/request');

App({
  globalData: {
    // 与后端 BASE_URL 同域；小程序合法域名只需配这一个。
    // baseUrl 留空表示同域相对路径（部署时 nginx 把 /v2/api 挂在同域名下）。
    baseUrl: '',
    user: null,
  },

  onLaunch() {
    this.ensureLogin();
  },

  // 静默登录：resolve 后 this.globalData.user 可用。
  ensureLogin() {
    if (this._loginPromise) return this._loginPromise;
    const token = wx.getStorageSync('token');
    if (token) {
      this._loginPromise = Promise.resolve();
      return this._loginPromise;
    }
    this._loginPromise = new Promise((resolve, reject) => {
      wx.login({
        success: (res) => {
          if (!res.code) { reject(new Error('wx.login 无 code')); return; }
          request({
            url: '/v2/api/auth/login', method: 'POST', noAuth: true,
            data: { code: res.code },
          }).then((d) => {
            wx.setStorageSync('token', d.token);
            this.globalData.user = d;
            resolve(d);
          }).catch(reject);
        },
        fail: () => reject(new Error('wx.login 失败')),
      });
    });
    this._loginPromise.catch(() => { this._loginPromise = null; });
    return this._loginPromise;
  },

  // 登录态失效时由 request 调用：清 token 重新静默登录。
  relogin() {
    wx.removeStorageSync('token');
    this._loginPromise = null;
    return this.ensureLogin();
  },
});
