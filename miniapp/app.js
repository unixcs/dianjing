// 观星电竞 — 原生小程序入口。
// 登录策略：启动时若本地无 token 则静默 wx.login 换 token；
// token 失效（401）由 request 统一触发重登后重放一次。
const { request } = require('./utils/request');

App({
  globalData: {
    // 与后端 BASE_URL 同域；小程序合法域名只需配这一个。
    // 本地联调可临时改为 http://127.0.0.1:8096，并在开发者工具
    // 「详情-本地设置」勾选"不校验合法域名"。
    baseUrl: 'https://wx.gxwqb.cn',
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
  // 单飞：并发 401 只触发一次重登，其余请求共用同一个 promise。
  relogin() {
    if (this._reloginPromise) return this._reloginPromise;
    wx.removeStorageSync('token');
    this._loginPromise = null;
    this._reloginPromise = this.ensureLogin().finally(() => {
      this._reloginPromise = null;
    });
    return this._reloginPromise;
  },

  // 支付前取新的 login_code：后端用它换取新鲜 session_key 签名
  // （session_key 会过期，直接用旧值签名会导致拉起支付失败）。
  getPayCode() {
    return new Promise((resolve, reject) => {
      wx.login({
        success: (res) => {
          if (res.code) resolve(res.code);
          else reject(new Error('微信登录失败，请重试'));
        },
        fail: () => reject(new Error('微信登录失败，请重试')),
      });
    });
  },
});
