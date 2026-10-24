// 意见反馈：提交（type=投诉/建议）+ 列表展示客服回复。
const { request, fmtTime, toastError } = require('../../utils/request');

Page({
  data: {
    type: 'complaint',
    content: '',
    list: [],
    submitting: false,
    showForm: false,
  },

  onShow() {
    this.fetch();
  },

  fetch() {
    return request({ url: '/v2/api/feedbacks' })
      .then((d) => {
        this.setData({
          list: (d.feedbacks || []).map((f) => ({
            ...f,
            timeText: fmtTime(f.created_at),
            replyTimeText: fmtTime(f.replied_at),
          })),
        });
      })
      .catch(toastError);
  },

  onType(e) {
    this.setData({ type: e.currentTarget.dataset.type });
  },

  onContent(e) {
    this.setData({ content: e.detail.value });
  },

  toggleForm() {
    this.setData({ showForm: !this.data.showForm });
  },

  submit() {
    const { content, submitting } = this.data;
    if (submitting) return;
    if (!content.trim()) {
      wx.showToast({ title: '请填写反馈内容', icon: 'none' });
      return;
    }
    this.setData({ submitting: true });
    request({ url: '/v2/api/feedbacks', method: 'POST', data: { type: this.data.type, content } })
      .then(() => {
        wx.showToast({ title: '已提交', icon: 'success' });
        this.setData({ content: '', showForm: false, submitting: false });
        this.fetch();
      })
      .catch((e) => {
        this.setData({ submitting: false });
        toastError(e);
      });
  },
});
