// 协议/政策文档：GET /docs/:key（privacy | service-policy），后端返回纯文本。
const { request, toastError } = require('../../utils/request');

Page({
  data: {
    title: '协议',
    content: '加载中...',
  },

  onLoad(options) {
    const key = options.key === 'privacy' ? 'privacy' : 'service-policy';
    const title = options.title || (key === 'privacy' ? '隐私政策' : '服务政策');
    this.setData({ title });
    wx.setNavigationBarTitle({ title });
    request({ url: '/v2/api/docs/' + key, noAuth: true })
      .then((d) => this.setData({ content: d.content || '暂无内容' }))
      .catch((e) => {
        this.setData({ content: '加载失败' });
        toastError(e);
      });
  },
});
