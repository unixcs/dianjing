/**
 * e2e-miniapp.js — 小程序 UI 全功能自动化走查（微信开发者工具 automator 驱动）
 *
 * 前置：
 *   1) 本地后端已起（127.0.0.1:8096，backend/dianjing.env.dev，已 seeddev）
 *   2) devtools 已开自动化端口：
 *      cd /d/Program/soft/wechattools && cli.bat auto --project "D:/mnt/Projects/dianjing/miniapp" --auto-port 9420
 *   3) MINI_TOKEN=<tokenforge -u <seedUserId> 铸的 token>
 *      （app.js 按 envVersion=develop 自动切 127.0.0.1:8096，勿手改线上域名）
 *
 * 与 e2e-api.mjs 相同的「假微信双通道」：用户态走 tokenforge 注入；
 * 资金态走 /wx/msg 明文签名推送（pay/refund 收敛）。
 * 断言只用可观察状态（路由 / 页面 data / API 终态），不依赖 tap() 返回值；
 * 阻断型原生弹窗与 requestVirtualPayment 全部 mock（沙箱面板不保证回调）。
 *
 * 运行：node tools/e2e-miniapp.js
 * 产物：tools/artifacts/miniapp-<ts>/*.png + miniapp-e2e-report.json
 */

const path = require('path');
const fs = require('fs');
const crypto = require('crypto');
const automator = require('miniprogram-automator');

const WS_ENDPOINT = process.env.WS_ENDPOINT || 'ws://127.0.0.1:9420';
const API = process.env.API_BASE || 'http://127.0.0.1:8096';
const TOKEN = process.env.MINI_TOKEN || '';
const ART_ROOT = path.join(__dirname, 'artifacts');
if (!TOKEN) { console.error('missing MINI_TOKEN (tokenforge)'); process.exit(2); }

// ---- 推送签名密钥：从 dev env 文件运行时读取（dev dummy 值，不硬编码进脚本）----
function pushTokenFromEnvFile() {
  const f = path.join(__dirname, '..', 'backend', 'dianjing.env.dev');
  const m = fs.readFileSync(f, 'utf8').match(/^XPAY_PUSH_TOKEN=(.+)$/m);
  if (!m) throw new Error('XPAY_PUSH_TOKEN not found in backend/dianjing.env.dev');
  return m[1].trim();
}
const PUSH_TOKEN = pushTokenFromEnvFile();

const results = [];
let shotDir = '';
let seq = 0;

function record(name, ok, detail) {
  results.push({ name, ok, detail: detail || '' });
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}${detail ? '  — ' + detail : ''}`);
}

async function shot(mini, name) {
  try {
    const file = path.join(shotDir, `${String(++seq).padStart(2, '0')}-${name}.png`);
    await mini.screenshot({ path: file });
    return file;
  } catch (e) {
    console.log(`WARN screenshot ${name}: ${e.message}`);
    return '';
  }
}

async function waitPageData(mini, key, timeoutMs, pred) {
  const deadline = Date.now() + timeoutMs;
  let page = await mini.currentPage();
  while (Date.now() < deadline) {
    const data = await page.data();
    if (pred(data[key])) return data[key];
    await new Promise((r) => setTimeout(r, 400));
    page = await mini.currentPage(); // 页面可能被重定向，重取
  }
  throw new Error(`等待 ${key} 超时(${timeoutMs}ms)`);
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// ---- 后端 API 直连（Node 侧核对终态）----
async function api(method, p, body) {
  const res = await fetch(API + p, {
    method,
    headers: { Authorization: 'Bearer ' + TOKEN, 'Content-Type': 'application/json' },
    body: body ? JSON.stringify(body) : undefined,
  });
  let j = null;
  try { j = await res.json(); } catch (e) { /* ignore */ }
  return { status: res.status, json: j };
}

// ---- /wx/msg 明文签名推送（与 e2e-api.mjs 同口径）----
function signPush(ts, nonce) {
  return crypto.createHash('sha1').update([PUSH_TOKEN, ts, nonce].sort().join('')).digest('hex');
}
async function pushPush(obj) {
  const ts = String(Math.floor(Date.now() / 1000));
  const nonce = crypto.randomBytes(8).toString('hex');
  const sig = signPush(ts, nonce);
  const res = await fetch(`${API}/wx/msg?signature=${sig}&timestamp=${ts}&nonce=${nonce}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(obj),
  });
  const text = await res.text();
  return { status: res.status, text };
}
const payPush = (orderNo, extra = {}) => pushPush({
  Event: 'xpay_goods_deliver_notify', OutTradeNo: orderNo, Env: 1,
  PayOrderId: 'po_' + orderNo,
  WeChatPayInfo: { TransactionId: 'txn_' + orderNo },
  ...extra,
});
const refundOkPush = (orderNo, extra = {}) => pushPush({
  Event: 'xpay_refund_notify', MchOrderId: orderNo, RetCode: 0, Env: 1, ...extra,
});

const money = (cents) => '¥' + ((cents || 0) / 100).toFixed(2);

// devtools automator 已知抖动：导航/求值偶发 rawPath null（webview 竞态）——重试即可
async function retry(label, fn, n = 3) {
  let lastErr;
  for (let i = 0; i < n; i++) {
    try { return await fn(); } catch (e) {
      lastErr = e;
      console.log(`WARN retry ${label} (${i + 1}/${n}): ${e.message}`);
      await sleep(1500);
    }
  }
  throw lastErr;
}

let mini;

async function main() {
  shotDir = path.join(ART_ROOT, 'miniapp-' + new Date().toISOString().replace(/[:.]/g, '-').slice(0, 19));
  fs.mkdirSync(shotDir, { recursive: true });
  console.log(`artifacts → ${shotDir}`);

  // 种子基准：home 聚合（页面应与之逐字段一致）
  const home = await api('GET', '/v2/api/home');
  if (home.status !== 200) { console.error('home API 不可达，先起后端'); process.exit(2); }
  const H = home.json;
  const svc = H.services[0];
  const cats = H.category;
  const perCat = {};
  for (const s of H.services) perCat[s.category_id] = (perCat[s.category_id] || 0) + 1;
  console.log(`seed: services=${H.services.length} cats=${cats.map((c) => c.name).join('/')} svc0=#${svc.id} ${svc.title} ${money(svc.price_cents)}`);

  // user id：用 token 反查（orders 即可，任何鉴权接口都能证明 token 有效）
  const who = await api('GET', '/v2/api/orders');
  if (who.status !== 200) { console.error('MINI_TOKEN 无效（orders 401）'); process.exit(2); }

  mini = await automator.connect({ wsEndpoint: WS_ENDPOINT });
  record('automator.connect', true, WS_ENDPOINT);
  const sys = await mini.systemInfo();
  console.log(`SDKVersion=${sys.SDKVersion} platform=${sys.platform}`);
  await sleep(1500); // 等 IDE webview 稳定，压 rawPath 抖动

  // ---- mock：阻断型弹窗自动确认；虚拟支付按 __payMockMode 可控成败 ----
  await retry('mock showToast', () => mini.mockWxMethod('showToast', {}));
  await retry('mock showModal', () => mini.mockWxMethod('showModal',
    () => ({ confirm: true, cancel: false, content: '自动化测试退款原因' })));
  await retry('mock requestVirtualPayment', () => mini.mockWxMethod('requestVirtualPayment', (opt) => {
    try {
      const a = getApp();
      a.__lastPay = opt;
      if (a.__payMockMode === 'cancel') return { errMsg: 'requestVirtualPayment:fail cancel' };
    } catch (e) { /* ignore */ }
    return { errMsg: 'requestVirtualPayment:ok' };
  }));

  // ---- token 注入（tokenforge）：storage + 单飞 promise + globalData.user ----
  const inj = await retry('inject baseUrl', () => mini.evaluate((token) => {
    wx.setStorageSync('token', token);
    const app = getApp();
    app._loginPromise = Promise.resolve();
    app._reloginPromise = null;
    return { baseUrl: app.globalData.baseUrl };
  }, TOKEN));
  record('baseUrl 按 envVersion 切本地', inj.baseUrl === 'http://127.0.0.1:8096', `baseUrl=${inj.baseUrl}`);
  const me = await retry('inject user', () => mini.evaluate(() => {
    const app = getApp();
    app.globalData.user = { token: wx.getStorageSync('token'), user_id: 2, nickname: '测试用户' };
    return app.globalData.user;
  }));
  record('token 注入', !!me.token, `user_id=${me.user_id}`);

  // ================= ① 首页 =================
  let page = await retry('reLaunch index', () => mini.reLaunch('/pages/index/index'));
  await page.waitFor(800);
  await waitPageData(mini, 'loading', 8000, (v) => v === false);
  const idx = await (await mini.currentPage()).data();
  record('首页公告', idx.notice === H.notice, `notice=${idx.notice}`);
  record('首页 banner', idx.banners.length === H.banners.length && idx.banners[0].image === H.banners[0].image,
    `banners=${idx.banners.length}`);
  record('首页分类位', idx.categories.length === cats.length, `cats=${idx.categories.map((c) => c.name).join('/')}`);
  record('首页服务卡', idx.services.length === H.services.length
    && idx.services[0].id === svc.id
    && idx.services[0].priceText === money(svc.price_cents),
    `services=${idx.services.length} first=${idx.services[0].title}/${idx.services[0].priceText}`);
  await shot(mini, 'index');

  // 首页服务卡真实 tap → 详情页
  const firstCard = await (await mini.currentPage()).$('.svc');
  await firstCard.tap();
  await sleep(1200);
  page = await mini.currentPage();
  record('首页卡片跳详情', page.path === 'pages/goods-detail/goods-detail', `path=${page.path}`);
  await waitPageData(mini, 'svc', 8000, (v) => !!v);
  const gd = await (await mini.currentPage()).data();
  record('详情页字段', gd.svc.id === svc.id && gd.svc.priceText === money(svc.price_cents)
    && typeof gd.svc.content === 'string' && gd.svc.content.length > 20,
    `svc=#${gd.svc.id} ${gd.svc.title} content=${gd.svc.content.length}字`);
  await shot(mini, 'goods-detail');

  // ================= ② 下单 happy path =================
  const buyBtn = await (await mini.currentPage()).$('.buy-btn');
  await buyBtn.tap();
  await sleep(1200);
  page = await mini.currentPage();
  record('详情→确认订单', page.path === 'pages/checkout/checkout', `path=${page.path}`);
  await waitPageData(mini, 'svc', 8000, (v) => !!v);
  let ck = await (await mini.currentPage()).data();
  record('订单页快照价', ck.svc.id === svc.id && ck.svc.priceText === money(svc.price_cents), `price=${ck.svc.priceText}`);

  // 联系方式走真实输入（bindinput）；数量 bindblur 用 evaluate 直调
  const inputs = await (await mini.currentPage()).$$('.field-input');
  await inputs[1].input('13800001234');
  await mini.evaluate(() => {
    const p = getCurrentPages()[getCurrentPages().length - 1];
    p.setData({ quantity: 2 });
    p.updateTotal();
    return true;
  });
  ck = await (await mini.currentPage()).data();
  record('数量×2 合计联动', ck.svc.totalText === money(svc.price_cents * 2) && ck.contact === '13800001234',
    `total=${ck.svc.totalText} contact=${ck.contact}`);
  await shot(mini, 'checkout');

  const t0 = Date.now();
  await (await (await mini.currentPage()).$('.buy-btn')).tap();
  // create→pay 参数→mock 支付→confirm-pay(502 安全回退)→modal 自动确认→跳详情
  let paidOrder = null;
  const deadline = Date.now() + 15000;
  while (Date.now() < deadline && !paidOrder) {
    const r = await api('GET', '/v2/api/orders?status=pending');
    const hit = (r.json.orders || []).find((o) => o.quantity === 2 && o.total_cents === svc.price_cents * 2);
    if (hit) paidOrder = hit;
    else await sleep(500);
  }
  record('下单创建(数量2)', !!paidOrder,
    paidOrder ? `#${paidOrder.id} ${paidOrder.order_no} ${paidOrder.status_text}` : '15s 内未见 pending 订单');

  // 断言传给 wx.requestVirtualPayment 的参数（mock 捕获；订单创建先于 /pay 返回，轮询等捕获）
  let lastPay = null;
  const payDeadline = Date.now() + 8000;
  while (Date.now() < payDeadline) {
    lastPay = await mini.evaluate(() => (getApp().__lastPay || null));
    if (lastPay && lastPay.paySig) break;
    await sleep(400);
  }
  const payOk = !!lastPay && !!lastPay.paySig && !!lastPay.signature
    && lastPay.mode === 'short_series_goods'
    && String(lastPay.signData || '').indexOf(paidOrder ? paidOrder.order_no : '@@') >= 0;
  let sdEnv = null;
  try { sdEnv = JSON.parse(lastPay.signData).env; } catch (e) { /* ignore */ }
  record('虚拟支付参数(paySig/signature/orderNo)', payOk,
    lastPay ? `paySig=${String(lastPay.paySig).slice(0, 12)}… sig=${String(lastPay.signature).slice(0, 12)}… mode=${lastPay.mode} env=${sdEnv}` : '未捕获');

  // 模拟微信侧支付成功推送 → 待联系
  const pushRes = await payPush(paidOrder.order_no);
  record('支付推送收敛', pushRes.status === 200, `push=${pushRes.status} ${pushRes.text.slice(0, 40)}`);
  await waitPageData(mini, 'o', 10000, (v) => v && v.status_text === '待联系');
  const od1 = (await (await mini.currentPage()).data()).o;
  record('支付后订单态=待联系', od1.id === paidOrder.id && od1.can_refund === true && od1.refund_mode === 'auto',
    `#${od1.id} ${od1.status_text} canRefund=${od1.can_refund} mode=${od1.refund_mode}`);
  record('页面跳转订单详情', (await mini.currentPage()).path === 'pages/order-detail/order-detail',
    `path=${(await mini.currentPage()).path}`);
  await shot(mini, 'order-paid');

  // ================= ③ 支付取消 + 用户取消订单 =================
  page = await retry('reLaunch goods-detail(取消流)', () => mini.reLaunch(`/pages/goods-detail/goods-detail?id=${svc.id}`));
  await waitPageData(mini, 'svc', 8000, (v) => !!v);
  await (await (await mini.currentPage()).$('.buy-btn')).tap();
  await sleep(1000);
  await waitPageData(mini, 'svc', 8000, (v) => !!v);
  const inputs2 = await (await mini.currentPage()).$$('.field-input');
  await inputs2[1].input('138000099999');
  await mini.evaluate(() => {
    const a = getApp();
    a.__payMockMode = 'cancel';
    const p = getCurrentPages()[getCurrentPages().length - 1];
    p.setData({ quantity: 1 });
    p.updateTotal();
    return true;
  });
  await (await (await mini.currentPage()).$('.buy-btn')).tap();
  await waitPageData(mini, 'o', 15000, (v) => v && v.status_text === '待支付');
  const od2 = (await (await mini.currentPage()).data()).o;
  record('支付取消→订单保留待支付', od2.can_pay === true && od2.can_cancel === true,
    `#${od2.id} ${od2.status_text} canPay=${od2.can_pay}`);
  await mini.evaluate(() => { getApp().__payMockMode = 'ok'; return true; });

  // 取消订单（onCancel→modal 自动确认→已取消）
  await mini.evaluate(() => {
    getCurrentPages()[getCurrentPages().length - 1].onCancel();
    return true;
  });
  await waitPageData(mini, 'o', 8000, (v) => v && v.status_text === '已取消');
  const od3 = (await (await mini.currentPage()).data()).o;
  record('用户取消订单→已取消', od3.id === od2.id && od3.can_pay === false, `#${od3.id} ${od3.status_text}`);
  await shot(mini, 'order-cancelled');

  // ================= ④ 申请退款（auto 直批→推送→已退款）=================
  page = await retry('reLaunch order-detail(退款流)', () => mini.reLaunch(`/pages/order-detail/order-detail?id=${paidOrder.id}`));
  await waitPageData(mini, 'o', 8000, (v) => v && v.status_text === '待联系');
  await mini.evaluate(() => {
    getCurrentPages()[getCurrentPages().length - 1].onRefund();
    return true;
  });
  await waitPageData(mini, 'o', 8000, (v) => v && v.status_text === '退款中');
  record('申请退款(auto)→退款中', true, `#${paidOrder.id} 退款中`);
  const rpush = await refundOkPush(paidOrder.order_no);
  await mini.evaluate(() => {
    getCurrentPages()[getCurrentPages().length - 1].fetch();
    return true;
  });
  await waitPageData(mini, 'o', 8000, (v) => v && v.status_text === '已退款');
  const od4 = (await (await mini.currentPage()).data()).o;
  record('退款推送→已退款', rpush.status === 200 && od4.status_text === '已退款' && od4.totalText === money(od4.total_cents),
    `push=${rpush.status} #${od4.id} ${od4.status_text} ${od4.totalText}`);
  await shot(mini, 'order-refunded');

  // ================= ⑤ 订单列表 + 状态筛选 =================
  page = await retry('reLaunch order-list', () => mini.reLaunch('/pages/order/order'));
  await sleep(800);
  await waitPageData(mini, 'loading', 8000, (v) => v === false);
  let ol = await (await mini.currentPage()).data();
  record('订单列表加载', ol.orders.length >= 2, `orders=${ol.orders.length} 首条=${ol.orders[0] && ol.orders[0].status_text}`);
  record('列表金额格式化', ol.orders[0] && ol.orders[0].totalText === money(ol.orders[0].total_cents),
    `${ol.orders[0] && ol.orders[0].totalText}`);
  // tab 真实 tap：已完成(含已退款)；tap 后先等 active 翻转，未翻转则 evaluate 直调兜底
  const tabs = await (await mini.currentPage()).$$('.tab');
  await tabs[4].tap();
  const tabDeadline = Date.now() + 4000;
  let active = '';
  while (Date.now() < tabDeadline) {
    active = (await (await mini.currentPage()).data()).active;
    if (active === 'done') break;
    await sleep(300);
  }
  if (active !== 'done') {
    await mini.evaluate(() => {
      getCurrentPages()[getCurrentPages().length - 1].onTab({ currentTarget: { dataset: { key: 'done' } } });
      return true;
    });
    await sleep(800);
    active = (await (await mini.currentPage()).data()).active;
  }
  await waitPageData(mini, 'loading', 8000, (v) => v === false);
  ol = await (await mini.currentPage()).data();
  const doneOk = active === 'done' && ol.orders.length >= 1
    && ol.orders.every((o) => ['已完成', '已退款'].includes(o.status_text))
    && ol.orders.some((o) => o.status_text === '已退款');
  record('tab=已完成 含已退款', doneOk, `active=${active} orders=${ol.orders.map((o) => o.status_text).join(',')}`);
  await (await (await mini.currentPage()).$$('.tab'))[0].tap();
  await sleep(600);
  await shot(mini, 'order-list');

  // ================= ⑥ 退款记录页 =================
  page = await retry('navigate refund', () => mini.navigateTo('/pages/refund/refund'));
  await page.waitFor(800);
  await waitPageData(mini, 'loading', 8000, (v) => v === false);
  const rf = await (await mini.currentPage()).data();
  record('退款记录', rf.list.length >= 1 && rf.list[0].statusText === '已退款'
    && rf.list[0].amountText === money(rf.list[0].amount_cents),
    `list=${rf.list.map((r) => r.statusText + r.amountText).join(',')}`);
  await shot(mini, 'refund-list');
  await mini.navigateBack();

  // ================= ⑦ 意见反馈 =================
  page = await retry('navigate feedback', () => mini.navigateTo('/pages/feedback/feedback'));
  await page.waitFor(800);
  const fb0 = (await (await mini.currentPage()).data()).list.length;
  await mini.evaluate(() => {
    const p = getCurrentPages()[getCurrentPages().length - 1];
    p.setData({ showForm: true, type: 'suggestion', content: '自动化测试反馈：UI 走查提交，请忽略' });
    p.submit();
    return true;
  });
  await sleep(1500);
  const fbd = await (await mini.currentPage()).data();
  record('反馈提交+列表落地', fbd.list.length === fb0 + 1
    && fbd.list[0].content.indexOf('自动化测试反馈') === 0
    && fbd.list[0].type === 'suggestion',
    `list=${fbd.list.length} latest=${fbd.list[0] && fbd.list[0].content.slice(0, 12)}`);
  await shot(mini, 'feedback');
  await mini.navigateBack();

  // ================= ⑧ 协议文档 =================
  const privApi = await api('GET', '/v2/api/docs/privacy');
  page = await retry('navigate docs', () => mini.navigateTo('/pages/docs/docs?key=privacy'));
  await page.waitFor(800);
  await waitPageData(mini, 'content', 8000, (v) => v && v !== '加载中...' && v !== '加载失败');
  const dc = await (await mini.currentPage()).data();
  record('隐私政策文档', dc.title === '隐私政策'
    && dc.content === (privApi.json.content || '暂无内容'),
    `title=${dc.title} ${dc.content.length}字 与API一致=${dc.content === (privApi.json.content || '暂无内容')}`);
  await shot(mini, 'docs-privacy');
  await mini.navigateBack();

  // ================= ⑨ 我的 =================
  page = await retry('reLaunch user', () => mini.reLaunch('/pages/user/user'));
  await page.waitFor(1000);
  await waitPageData(mini, 'user', 8000, (v) => !!v);
  const ud = await (await mini.currentPage()).data();
  record('我的-登录态资料', ud.user && ud.user.nickname === '测试用户', `nickname=${ud.user && ud.user.nickname}`);
  await shot(mini, 'user');

  // ---- 收尾：回首页 + 报告 ----
  await mini.reLaunch('/pages/index/index').catch(() => {});
  const pass = results.filter((r) => r.ok).length;
  const fail = results.length - pass;
  const report = {
    suite: 'miniapp-ui-e2e', when: new Date().toISOString(),
    ws: WS_ENDPOINT, api: API, total: results.length, pass, fail,
    cases: results,
  };
  fs.writeFileSync(path.join(ART_ROOT, 'miniapp-e2e-report.json'), JSON.stringify(report, null, 2));
  console.log(`\n==== miniapp e2e: ${pass}/${results.length} pass, ${fail} fail ====`);
  console.log(`report → ${path.join(ART_ROOT, 'miniapp-e2e-report.json')}`);
  await mini.disconnect();
  process.exit(fail ? 1 : 0);
}

main().catch((e) => {
  console.error('FATAL', e);
  const pass = results.filter((r) => r.ok).length;
  fs.writeFileSync(path.join(ART_ROOT, 'miniapp-e2e-report.json'),
    JSON.stringify({ suite: 'miniapp-ui-e2e', fatal: String(e), total: results.length, pass, fail: results.length - pass, cases: results }, null, 2));
  process.exit(1);
});
