/**
 * e2e-api.mjs — dianjing 后端 API 全链路冒烟（本地 dev 实例）
 *
 * 前置：backend/server.exe -config dianjing.env.dev 已在 127.0.0.1:8096 运行，
 *       backend/seeddev.exe ./data 已播种，user id=1。
 * 运行：node tools/e2e-api.mjs
 * 产物：tools/artifacts/api-e2e-report.json
 *
 * 断言口径：只看 HTTP 响应可观察状态（状态码/JSON 字段），不查库。
 * 支付/退款/推送用「明文签名推送模拟器」收敛资金状态（与 tx 上回归口径一致）。
 */
import { execFileSync } from 'node:child_process';
import crypto from 'node:crypto';
import fs from 'node:fs';
import path from 'node:path';

const BASE = process.env.DJ_BASE || 'http://127.0.0.1:8096';
const ADMIN_PASSWORD = process.env.DJ_ADMIN_PASSWORD || 'dev-test-123';
const PUSH_TOKEN = 'dummy-push-token-dev';
const ROOT = path.resolve(import.meta.dirname, '..');

const results = [];
function rec(name, ok, detail = '') {
  results.push({ name, ok, detail });
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}${detail ? '  — ' + detail : ''}`);
}
function check(name, cond, detail) { rec(name, !!cond, detail); return !!cond; }

async function api(method, url, { body, token, adminToken, raw } = {}) {
  const headers = {};
  if (body !== undefined) headers['Content-Type'] = 'application/json';
  if (token) headers.Authorization = 'Bearer ' + token;
  if (adminToken) headers['X-Admin-Token'] = adminToken;
  const res = await fetch(BASE + url, {
    method, headers,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  const text = await res.text();
  let json = null;
  try { json = JSON.parse(text); } catch { /* push endpoints may reply raw */ }
  return { status: res.status, json, text };
}

// ---- 微信明文推送模拟器：sha1(sort(token,ts,nonce)) ----
function signPush(ts, nonce) {
  return crypto.createHash('sha1')
    .update([PUSH_TOKEN, ts, nonce].sort().join('')).digest('hex');
}
async function pushPush(payload, { ts, nonce, sig } = {}) {
  ts = ts || String(Math.floor(Date.now() / 1000));
  nonce = nonce || crypto.randomBytes(8).toString('hex');
  sig = sig || signPush(ts, nonce);
  return api('POST', `/wx/msg?signature=${sig}&timestamp=${ts}&nonce=${nonce}`, { body: payload });
}
const payPush = (orderNo, extra = {}) => pushPush({
  Event: 'xpay_goods_deliver_notify', OutTradeNo: orderNo, Env: 1,
  PayOrderId: 'po_' + orderNo, WeChatPayInfo: { TransactionId: 'txn_' + orderNo }, ...extra,
});
const refundOkPush = (orderNo, extra = {}) => pushPush({
  Event: 'xpay_refund_notify', MchOrderId: orderNo, RetCode: 0, Env: 1, ...extra,
});

async function createOrder(token, serviceId, quantity = 1, contact = 'wx:test') {
  const ct = crypto.randomBytes(8).toString('hex');
  const r = await api('POST', '/v2/api/orders', {
    token, body: { service_id: serviceId, quantity, contact, client_token: ct },
  });
  return r.json?.order;
}
const admin = {};

async function main() {
  console.log(`target: ${BASE}`);
  let failed = 0;

  // ========== ① 公开目录 ==========
  {
    const home = await api('GET', '/v2/api/home');
    check('home 聚合：notice/category/services/banners', home.status === 200 &&
      home.json.notice && home.json.category.length === 2 && home.json.services.length === 3 &&
      Array.isArray(home.json.banners) && home.json.banners[0].image.startsWith('http'),
      `services=${home.json?.services?.length}`);

    const cats = await api('GET', '/v2/api/categories');
    check('categories 顺序（热门 sort=90 在前）', cats.status === 200 && cats.json.categories[0].name === '热门');

    const svcs = await api('GET', '/v2/api/services?category_id=2');
    check('services 按分类过滤', svcs.status === 200 && svcs.json.services.length === 2 &&
      svcs.json.services.every(s => s.category_id === 2));

    const kw = await api('GET', '/v2/api/services?keyword=VIP');
    check('services 关键词搜索', kw.status === 200 && kw.json.services.length === 1);

    const det = await api('GET', '/v2/api/services/1');
    check('服务详情含 content', det.status === 200 && det.json.service.content.includes('<p>'));

    const det404 = await api('GET', '/v2/api/services/999');
    check('服务详情 404', det404.status === 404);

    const doc = await api('GET', '/v2/api/docs/privacy');
    check('协议文档 privacy', doc.status === 200 && doc.json.content.includes('测试'));
    const doc404 = await api('GET', '/v2/api/docs/other');
    check('未知文档 404', doc404.status === 404);

    const img = await fetch(BASE + '/uploads/cover-basic.png');
    check('uploads 静态图可访问', img.status === 200 && img.headers.get('content-type').includes('image'));
    const dirIdx = await fetch(BASE + '/uploads/');
    check('uploads 目录禁止列目录', dirIdx.status === 404 || dirIdx.status === 403);
  }

  // ========== ② 鉴权 ==========
  let TOKEN;
  {
    const bad = await api('GET', '/v2/api/orders', { token: '9.9.9.badsig' });
    check('伪造 token → 401', bad.status === 401, `status=${bad.status}`);

    const noTok = await api('GET', '/v2/api/orders');
    check('无 token → 401', noTok.status === 401);

    const login = await api('POST', '/v2/api/auth/login', { body: { code: 'fake-code' } });
    check('假 code 登录 → 502（dummy secret，不 5xx 崩溃）', login.status === 502, `status=${login.status} msg=${login.json?.msg}`);

    TOKEN = execFileSync(path.join(ROOT, 'backend/tokenforge.exe'),
      ['-env', 'backend/dianjing.env.dev', '-u', '1', '-d', '1'], { encoding: 'utf8' }).trim();
    const mine = await api('GET', '/v2/api/orders', { token: TOKEN });
    check('tokenforge 令牌可用', mine.status === 200);

    const badAdmin = await api('POST', '/v2/api/admin/login', { body: { password: 'wrong' } });
    check('admin 错误密码 → 401', badAdmin.status === 401);
    const lg = await api('POST', '/v2/api/admin/login', { body: { password: ADMIN_PASSWORD } });
    check('admin 登录拿 token', lg.status === 200 && lg.json.token);
    admin.token = lg.json.token;
  }

  // ========== ③ 订单主链路 ==========
  let ORDER;
  {
    const o1 = await createOrder(TOKEN, 1, 2, 'wx:e2e-main');
    check('创建订单（数量 2，快照/总额）', o1 && o1.total_cents === 19800 && o1.status === 0 &&
      o1.title.includes('基础') && o1.can_pay && o1.can_cancel, `order_no=${o1?.order_no} total=${o1?.total_cents}`);

    // 幂等：同 client_token 再提交返回同一单
    const ct = crypto.randomBytes(8).toString('hex');
    const a = await api('POST', '/v2/api/orders', { token: TOKEN, body: { service_id: 2, quantity: 1, client_token: ct } });
    const b = await api('POST', '/v2/api/orders', { token: TOKEN, body: { service_id: 2, quantity: 1, client_token: ct } });
    check('client_token 幂等（双击防重）', a.json.order.id === b.json.order.id, `a=${a.json?.order?.id} b=${b.json?.order?.id}`);
    await api('POST', `/v2/api/orders/${a.json.order.id}/cancel`, { token: TOKEN, body: {} });

    const badQty = await api('POST', '/v2/api/orders', { token: TOKEN, body: { service_id: 1, quantity: 0 } });
    check('数量 0 拒绝', badQty.status === 400, `status=${badQty.status}`);
    const badQty2 = await api('POST', '/v2/api/orders', { token: TOKEN, body: { service_id: 1, quantity: 100 } });
    check('数量 100 拒绝', badQty2.status === 400, `status=${badQty2.status}`);

    const pay = await api('POST', `/v2/api/orders/${o1.id}/pay`, { token: TOKEN, body: {} });
    check('支付参数（paySig/signature 齐备）', pay.status === 200 && pay.json.signData && pay.json.paySig && pay.json.signature,
      Object.keys(pay.json || {}).join(','));

    const confirm = await api('POST', `/v2/api/orders/${o1.id}/confirm-pay`, { token: TOKEN, body: {} });
    check('confirm-pay 查单失败安全回退（502，不误标已付）', confirm.status === 502,
      `status=${confirm.status}`);
    const stillPending = await api('GET', `/v2/api/orders/${o1.id}`, { token: TOKEN });
    check('查单失败后订单仍待支付', stillPending.json.order.status === 0);

    // 模拟微信支付成功推送 → 待联系
    const p = await payPush(o1.order_no);
    check('支付成功推送被 ack', p.json?.ErrCode === 0, JSON.stringify(p.json));
    const after = await api('GET', `/v2/api/orders/${o1.id}`, { token: TOKEN });
    check('推送后订单 → 待联系(1)', after.json.order.status === 1 && after.json.order.pay_time > 0 &&
      after.json.order.can_refund && after.json.order.refund_mode === 'auto',
      `status=${after.json?.order?.status} mode=${after.json?.order?.refund_mode}`);
    ORDER = o1;
  }

  // ========== ④ 取消与复活 ==========
  {
    const o = await createOrder(TOKEN, 1, 1);
    const c = await api('POST', `/v2/api/orders/${o.id}/cancel`, { token: TOKEN, body: {} });
    check('取消订单 → 已取消(4)', c.json?.order?.status === 4);
    const c2 = await api('POST', `/v2/api/orders/${o.id}/cancel`, { token: TOKEN, body: {} });
    check('重复取消被拒（状态机守卫）', c2.status === 400 && /不可取消/.test(c2.json?.msg || ''), c2.json?.msg);

    // 取消后微信侧实际扣款 → money wins 复活
    await payPush(o.order_no);
    const revived = await api('GET', `/v2/api/orders/${o.id}`, { token: TOKEN });
    check('取消后被扣款 → 复活为待联系(1)（money wins）', revived.json.order.status === 1, `status=${revived.json?.order?.status}`);
    // 清理：退款收口
    await refundOkPush(o.order_no);
  }

  // ========== ⑤ 自动退款（待联系 auto）==========
  {
    const o = await createOrder(TOKEN, 2, 1);
    await payPush(o.order_no);
    const apply = await api('POST', `/v2/api/orders/${o.id}/refund`, { token: TOKEN, body: { reason: 'e2e-auto' } });
    check('auto 退款：refund approved + 订单退款中(5)', apply.status === 200 &&
      apply.json.refund.status === 'approved' && apply.json.refund.mode === 'auto' &&
      apply.json.order.status === 5, `refund=${apply.json?.refund?.status} order=${apply.json?.order?.status}`);

    const dup = await api('POST', `/v2/api/orders/${o.id}/refund`, { token: TOKEN, body: { reason: 'again' } });
    check('活跃退款存在时禁止重复申请', dup.status === 400, `status=${dup.status}`);

    const r = await refundOkPush(o.order_no);
    check('退款成功推送 ack', r.json?.ErrCode === 0);
    const done = await api('GET', `/v2/api/orders/${o.id}`, { token: TOKEN });
    check('退款推送后订单 → 已退款(6)', done.json.order.status === 6);

    const myRefunds = await api('GET', '/v2/api/refunds', { token: TOKEN });
    check('我的退款列表含记录', myRefunds.json.refunds.some(x => x.order_id === o.id && x.status === 'refunded'));
  }

  // ========== ⑥ 审核退款（服务中 audit）：造一笔 applying 待 ⑧ 审 ==========
  {
    const o = await createOrder(TOKEN, 1, 1);
    await payPush(o.order_no);
    const adv = await api('POST', `/v2/api/admin/orders/${o.id}/status`, { adminToken: admin.token, body: { status: 2 } });
    check('admin 推进服务中（audit 前置）', adv.status === 200 && adv.json.order.status === 2);
    const apply = await api('POST', `/v2/api/orders/${o.id}/refund`, { token: TOKEN, body: { reason: 'e2e-audit' } });
    check('audit 退款：applying + 订单退款中', apply.json?.refund?.status === 'applying' &&
      apply.json?.refund?.mode === 'audit' && apply.json?.order?.status === 5,
      `refund=${apply.json?.refund?.status} order=${apply.json?.order?.status}`);
  }

  // ========== ⑦ 推送安全 ==========
  {
    const ts = String(Math.floor(Date.now() / 1000));
    const nonce = crypto.randomBytes(8).toString('hex');
    const echo = await api('GET',
      `/wx/msg?signature=${signPush(ts, nonce)}&timestamp=${ts}&nonce=${nonce}&echostr=hello-e2e`);
    check('GET URL 验证回显 echostr', echo.text === 'hello-e2e', echo.text.slice(0, 40));

    const badEcho = await api('GET', `/wx/msg?signature=deadbeef&timestamp=${ts}&nonce=x&echostr=nope`);
    check('GET 验证错误签名拒绝', badEcho.status === 400);

    const stale = await pushPush({ Event: 'xpay_goods_deliver_notify', OutTradeNo: 'X', Env: 1 },
      { ts: String(Math.floor(Date.now() / 1000) - 3600) });
    check('过期时间戳推送拒绝', stale.json?.ErrMsg === '时间戳过期', JSON.stringify(stale.json));

    const badSig = await pushPush({ Event: 'xpay_goods_deliver_notify', OutTradeNo: 'X', Env: 1 },
      { sig: '00' + '0'.repeat(38) });
    check('错误签名推送拒绝', badSig.json?.ErrMsg === '签名校验失败');

    const unknown = await pushPush({ Event: 'something_else', Env: 1 });
    check('未知事件 ErrCode=1（让微信重试）', unknown.json?.ErrCode === 1);

    // nonce 重放：同 triple 二次投递 → ack 且不重复处理
    const o = await createOrder(TOKEN, 3, 1);
    const ts2 = String(Math.floor(Date.now() / 1000));
    const n2 = crypto.randomBytes(8).toString('hex');
    const sig2 = signPush(ts2, n2);
    const q = `?signature=${sig2}&timestamp=${ts2}&nonce=${n2}`;
    const p1 = await api('POST', '/wx/msg' + q, { body: { Event: 'xpay_goods_deliver_notify', OutTradeNo: o.order_no, Env: 1 } });
    const p2 = await api('POST', '/wx/msg' + q, { body: { Event: 'xpay_goods_deliver_notify', OutTradeNo: o.order_no, Env: 1 } });
    check('nonce 重放被吞 ack', p1.json?.ErrCode === 0 && p2.json?.ErrCode === 0, `${p1.json?.ErrCode}/${p2.json?.ErrCode}`);

    // 错误 Env：正式环境推送打到沙箱配置 → 拒收不收敛
    const o2 = await createOrder(TOKEN, 3, 1);
    await pushPush({ Event: 'xpay_goods_deliver_notify', OutTradeNo: o2.order_no, Env: 0 });
    const st = await api('GET', `/v2/api/orders/${o2.id}`, { token: TOKEN });
    check('Env 不匹配不收敛（仍待支付）', st.json.order.status === 0);
    // 缺 Env
    const o3 = await createOrder(TOKEN, 3, 1);
    await pushPush({ Event: 'xpay_goods_deliver_notify', OutTradeNo: o3.order_no });
    const st3 = await api('GET', `/v2/api/orders/${o3.id}`, { token: TOKEN });
    check('缺 Env 不收敛', st3.json.order.status === 0);
  }

  // ========== ⑧ 管理后台 API ==========
  {
    const unauth = await api('GET', '/v2/api/admin/overview');
    check('admin 无 token → 401', unauth.status === 401);

    const ov = await api('GET', '/v2/api/admin/overview', { adminToken: admin.token });
    check('admin overview 指标齐', ov.status === 200 && 'orders_total' in ov.json && 'revenue_cents' in ov.json && 'refunds_pending' in ov.json);

    const cat = await api('POST', '/v2/api/admin/categories', { adminToken: admin.token, body: { name: 'E2E分类', sort: 80 } });
    check('admin 建分类', cat.status === 200 && cat.json.id > 0);

    const svc = await api('POST', '/v2/api/admin/services', {
      adminToken: admin.token,
      body: { title: 'E2E临时服务', category_id: cat.json.id, price_cents: 100, xpay_product_id: 'e2e_tmp', sort: 1 },
    });
    check('admin 建服务', svc.status === 200 && svc.json.id > 0);

    const put = await api('PUT', `/v2/api/admin/services/${svc.json.id}`, { adminToken: admin.token, body: { price_cents: 200 } });
    const putGet = await api('GET', `/v2/api/services/${svc.json.id}`);
    check('admin 部分更新（只改价格，其余字段不丢）', put.status === 200 &&
      putGet.json.service.price_cents === 200 && putGet.json.service.title === 'E2E临时服务',
      `price=${putGet.json?.service?.price_cents}`);

    const del = await api('DELETE', `/v2/api/admin/services/${svc.json.id}`, { adminToken: admin.token });
    const delGet = await api('GET', `/v2/api/services/${svc.json.id}`);
    check('admin 下架（软删，公开端 404）', del.status === 200 && delGet.status === 404);

    // 订单列表 + 状态机
    const list = await api('GET', '/v2/api/admin/orders?status=1', { adminToken: admin.token });
    check('admin 订单列表（含用户联系方式）', list.status === 200 && list.json.orders.length > 0 &&
      list.json.orders.some(o => o.contact === 'wx:e2e-main' || o.user_mobile));

    const target = list.json.orders.find(o => o.contact === 'wx:e2e-main');
    // 跳态：待联系(1) 直接推 已完成(3) 必须被状态机拒绝
    const skip = await api('POST', `/v2/api/admin/orders/${target.id}/status`, { adminToken: admin.token, body: { status: 3 } });
    check('admin 跳态 待联系→已完成 被拒', skip.status === 400, skip.json?.msg);
    const adv = await api('POST', `/v2/api/admin/orders/${target.id}/status`, { adminToken: admin.token, body: { status: 2 } });
    check('admin 推进 待联系→服务中', adv.status === 200 && adv.json.order.status === 2);
    const adv2 = await api('POST', `/v2/api/admin/orders/${target.id}/status`, { adminToken: admin.token, body: { status: 3 } });
    check('admin 推进 服务中→已完成', adv2.status === 200 && adv2.json.order.status === 3);

    // 已完成订单用户不可退款
    const noRefund = await api('POST', `/v2/api/orders/${target.id}/refund`, { token: TOKEN, body: { reason: 'x' } });
    check('已完成订单退款被拒（none 模式）', noRefund.status === 400);

    // 退款审核：通过
    const rf = await api('GET', '/v2/api/admin/refunds', { adminToken: admin.token });
    const applying = rf.json.refunds.find(x => x.status === 'applying' && x.mode === 'audit');
    check('admin 退款列表有 pending 审核单', !!applying);
    const approve = await api('POST', `/v2/api/admin/refunds/${applying.id}/audit`, { adminToken: admin.token, body: { approve: true, note: 'e2e-ok' } });
    check('admin 审核通过', approve.status === 200);
    await refundOkPush(applying.order_no);
    const doneO = await api('GET', `/v2/api/orders/${applying.order_id}`, { token: TOKEN });
    check('审核通过+推送 → 已退款(6)', doneO.json.order.status === 6);

    // 退款审核：驳回 → 订单回服务中
    const o = await createOrder(TOKEN, 1, 1);
    await payPush(o.order_no);
    await api('POST', `/v2/api/admin/orders/${o.id}/status`, { adminToken: admin.token, body: { status: 2 } });
    await api('POST', `/v2/api/orders/${o.id}/refund`, { token: TOKEN, body: { reason: 'reject-me' } });
    const rf2 = await api('GET', '/v2/api/admin/refunds', { adminToken: admin.token });
    const rej = rf2.json.refunds.find(x => x.status === 'applying');
    const reject = await api('POST', `/v2/api/admin/refunds/${rej.id}/audit`, { adminToken: admin.token, body: { approve: false, note: 'e2e-reject' } });
    const backO = await api('GET', `/v2/api/orders/${o.id}`, { token: TOKEN });
    check('admin 驳回 → 订单回服务中(2)', reject.status === 200 && backO.json.order.status === 2,
      `status=${backO.json?.order?.status}`);
    await refundOkPush(o.order_no); // 清理

    // 反馈
    const fb = await api('POST', '/v2/api/feedbacks', { token: TOKEN, body: { type: 'complaint', content: 'e2e 反馈测试' } });
    check('用户提交反馈', fb.status === 200 && fb.json.id > 0);
    const fbList = await api('GET', '/v2/api/feedbacks', { token: TOKEN });
    check('用户反馈列表', fbList.json.feedbacks.some(x => x.id === fb.json.id && x.status === 0));
    const reply = await api('POST', '/v2/api/admin/feedbacks', { adminToken: admin.token, body: { id: fb.json.id, reply: '已收到（e2e）' } });
    const fbAfter = await api('GET', '/v2/api/feedbacks', { token: TOKEN });
    check('admin 回复反馈，用户侧可见', reply.status === 200 &&
      fbAfter.json.feedbacks.find(x => x.id === fb.json.id)?.reply === '已收到（e2e）');

    // settings
    const setGet = await api('GET', '/v2/api/admin/settings', { adminToken: admin.token });
    check('admin settings 读取', setGet.status === 200 && 'notice' in setGet.json);
    const setPut = await api('POST', '/v2/api/admin/settings', { adminToken: admin.token, body: { notice: 'E2E 新公告' } });
    const home2 = await api('GET', '/v2/api/home');
    check('admin settings 写入后 home 生效', setPut.status === 200 && home2.json.notice === 'E2E 新公告');
    await api('POST', '/v2/api/admin/settings', { adminToken: admin.token, body: { notice: '观星电竞测试公告：本地 E2E 环境专用' } }); // 还原

    // upload（multipart）
    const pngB64 = 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==';
    const fd = new FormData();
    fd.append('file', new Blob([Buffer.from(pngB64, 'base64')], { type: 'image/png' }), 'e2e.png');
    const up = await fetch(BASE + '/v2/api/admin/upload', { method: 'POST', headers: { 'X-Admin-Token': admin.token }, body: fd });
    const upJson = await up.json();
    check('admin 图片上传', up.status === 200 && upJson.path?.startsWith('/uploads/'), upJson.path);
    const upGet = await fetch(BASE + upJson.path);
    check('上传文件可访问', upGet.status === 200);
    const badUp = new FormData();
    badUp.append('file', new Blob([Buffer.from('not-image')], { type: 'text/plain' }), 'x.txt');
    const badUpRes = await fetch(BASE + '/v2/api/admin/upload', { method: 'POST', headers: { 'X-Admin-Token': admin.token }, body: badUp });
    check('非图片内容拒绝', badUpRes.status === 400);
  }

  // ========== ⑨ iOS 退款问询风控 ==========
  {
    const o = await createOrder(TOKEN, 1, 1);
    await payPush(o.order_no);
    const q1 = await pushPush({
      Event: 'xpay_subscribe_ios_refund_query_notify',
      OutTradeNo: o.order_no, pay_order_id: o.order_no, Env: 1,
      refund_request_reason: 'e2e-inquiry',
    });
    check('iOS 问询（待联系）→ 建议退款(0)', q1.json?.result_code === 0, JSON.stringify(q1.json));

    const blocked = await api('POST', `/v2/api/admin/orders/${o.id}/status`, { adminToken: admin.token, body: { status: 2 } });
    check('风控锁定后 admin 推进被阻断', blocked.status === 400 && /风控/.test(blocked.json?.msg || ''), blocked.json?.msg);

    // 重放同 payload → 指纹去重，回复一致
    const ts = String(Math.floor(Date.now() / 1000));
    const nonce = crypto.randomBytes(8).toString('hex');
    const sig = signPush(ts, nonce);
    const body = { Event: 'xpay_subscribe_ios_refund_query_notify', OutTradeNo: o.order_no, pay_order_id: o.order_no, Env: 1, refund_request_reason: 'e2e-inquiry' };
    const rp1 = await api('POST', `/wx/msg?signature=${sig}&timestamp=${ts}&nonce=${nonce}`, { body });
    check('iOS 问询重放去重', rp1.json?.result_code === 0);

    await refundOkPush(o.order_no);
    const fin = await api('GET', `/v2/api/orders/${o.id}`, { token: TOKEN });
    check('App Store 退款推送 → 已退款(6)', fin.json.order.status === 6);

    // 服务中订单问询 → 建议拒绝(1)
    const o2 = await createOrder(TOKEN, 1, 1);
    await payPush(o2.order_no);
    await api('POST', `/v2/api/admin/orders/${o2.id}/status`, { adminToken: admin.token, body: { status: 2 } });
    const q2 = await pushPush({
      Event: 'xpay_subscribe_ios_refund_query_notify',
      OutTradeNo: o2.order_no, pay_order_id: o2.order_no, Env: 1,
    });
    check('iOS 问询（服务中）→ 建议拒绝(1)', q2.json?.result_code === 1, JSON.stringify(q2.json));
    await refundOkPush(o2.order_no); // 清理
  }

  // ========== 汇总 ==========
  failed = results.filter(r => !r.ok).length;
  const artDir = path.join(ROOT, 'tools/artifacts');
  fs.mkdirSync(artDir, { recursive: true });
  fs.writeFileSync(path.join(artDir, 'api-e2e-report.json'), JSON.stringify({
    at: new Date().toISOString(), base: BASE, total: results.length, failed, results,
  }, null, 2));
  console.log(`\n==== API E2E: ${results.length - failed}/${results.length} passed${failed ? ' — FAILED' : ''} ====`);
  process.exit(failed ? 1 : 0);
}

main().catch(e => { console.error('FATAL', e); process.exit(2); });
