// 统一请求封装：自动带 Bearer token，401 时重登并重放一次。
const app = getApp();

function rawRequest(opts) {
  return new Promise((resolve, reject) => {
    const header = { 'Content-Type': 'application/json' };
    const token = wx.getStorageSync('token');
    if (token) header.Authorization = 'Bearer ' + token;
    wx.request({
      url: (app.globalData.baseUrl || '') + opts.url,
      method: opts.method || 'GET',
      data: opts.data || {},
      header,
      success(res) {
        if (res.statusCode >= 200 && res.statusCode < 300) {
          resolve(res.data);
        } else if (res.statusCode === 401 && !opts.noAuth) {
          app.relogin()
            .then(() => rawRequest({ ...opts, _retried: true }))
            .then(resolve)
            .catch(reject);
        } else {
          const msg = (res.data && res.data.msg) || ('请求失败(' + res.statusCode + ')');
          reject(new Error(msg));
        }
      },
      fail() {
        reject(new Error('网络异常，请稍后再试'));
      },
    });
  });
}

function request(opts) {
  // 登录后才能调的接口先等静默登录完成；noAuth 的直接发。
  if (opts.noAuth) return rawRequest(opts);
  return app.ensureLogin().then(() => rawRequest(opts));
}

function toastError(err) {
  wx.showToast({ title: (err && err.message) || '操作失败', icon: 'none' });
}

// 分 → "¥188.00"
function money(cents) {
  return '¥' + ((cents || 0) / 100).toFixed(2);
}

// 秒级时间戳 → "2026-10-24 12:00"
function fmtTime(ts) {
  if (!ts) return '';
  const d = new Date(ts * 1000);
  const p = (n) => (n < 10 ? '0' + n : '' + n);
  return d.getFullYear() + '-' + p(d.getMonth() + 1) + '-' + p(d.getDate()) + ' ' + p(d.getHours()) + ':' + p(d.getMinutes());
}

module.exports = { request, toastError, money, fmtTime };
