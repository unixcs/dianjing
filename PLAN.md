# dianjing 重构规划（基于 2026-10-24 服务器实况调研）

> 调研对象：`wx.gxwqb.cn` 上的萤火商城 YoShop 2.0 二开版（ThinkPHP6 + UniApp）。
> 本文档所有结论均以服务器实况为准，非猜测。证据路径已标注。

---

## 一、调研结论（先于规划的事实）

### 1.1 业务真实规模

| 指标 | 数值 | 来源 |
|---|---|---|
| 用户 | 16 | `yoshop_user` |
| 服务商品 | 14（2 个分类：基础/热门） | `yoshop_goods` / `yoshop_category` |
| 订单 | 12（最早 2026-07，全部 `delivery_type=30` 无物流） | `yoshop_order` |
| 退款单 | 8（全部走虚拟支付退款） | `yoshop_order_refund` |
| 投诉/反馈 | 0 | `yoshop_user_feedback` |
| 真实业务流量 | 接近零（nginx 日志几乎全是扫描器） | `/var/log/nginx/access.log*` |
| 上传文件 | 676K | `/srv/yoshop/shared/uploads` |

**第一性判断：这个系统的瓶颈从来不是性能，而是维护面。** 61 张表服务 12 笔订单；优惠券/积分/余额充值/会员等级/多商户/物流/打印机/DIY 装修全部无数据无使用。

### 1.2 参考规划中需要纠正的事实（重要）

参考 Plan 有四处与服务器实况不符：

1. **支付通道不是普通微信支付，而是微信虚拟支付（xpay）。** 所有真实支付流水 `platform=wechat_virtual`，前端走 `wx.requestVirtualPayment`（`mode=short_series_goods`）。因为卖的是虚拟服务，iOS 端不允许 JSAPI 支付。这是全系统最核心、最难移植的模块，新后端必须 1:1 还原签名与回调链路。
2. **首页红色"交易投诉多"警示条不是后台配置的**——那是微信平台对投诉较多小程序自动注入的官方提示，代码和装修配置里都找不到它。后台可配的只有黄色公告条"观星电竞 欢迎您~"（`yoshop_page` 的 notice 项）。
3. **"投诉入口"不是微信官方交易投诉组件**，是自建的反馈系统（`pages/feedback/index` + `yoshop_user_feedback` 表 + 后台处理回复）。参考 Plan 中"调用微信官方交易投诉组件"的说法不成立。
4. **"未成年人禁止下单"角标不是前端叠加的**，是直接烙在上传的封面图里。新系统只需沿用图片，无需任何角标逻辑；下单前弹窗拦截可做成纯前端行为 + 后端配置开关。

### 1.3 必须继承的定制逻辑（老代码中逐行核实过）

| # | 逻辑 | 实现位置（老代码证据） |
|---|---|---|
| 1 | 虚拟支付签名：`paySig = HMAC-SHA256(uri & signData, appKey)`；`signature = HMAC-SHA256(signData, session_key)`；session_key 优先用本次 `wx.login` code 换新的，失败回退缓存 | `app/api/service/cashier/Payment.php:455-505`、开发总手册 §5 |
| 2 | xpay 服务端三接口：`query_order`（查单对账）、`refund_order`（退款）、`notify_provide_goods`（确认发货，**只带 access_token 不带 pay_sig** 的特例） | `app/common/library/wechat/VirtualPayment.php` |
| 3 | 订单生命周期：待支付 → 待联系 → 服务中 → 已完成，无物流 | CONTEXT.md、订单枚举 |
| 4 | 退款模式引擎：已支付+未联系 → **AUTO 自动退**；服务中 → **AUDIT 商家审核**；已完成 → **NONE 不退**；存在进行中退款单时禁止重复申请 | `app/common/model/Order.php:548-572` |
| 5 | iOS 退款查询风控：微信推送 `xpay_subscribe_ios_refund_query_notify` 消息，按服务阶段给出"建议退款/拒绝"应答，原始报文落库；风控命中时**锁定发货上报**（`ios_refund_risk_locked_before_send`） | `app/common/service/order/IosRefundRisk.php`（565 行）、`payment_ios_refund_inquiry` 表 |
| 6 | 补偿定时器：每 60s 收敛未完成的虚拟退款单 + 同步 xpay 支付状态（查单补偿） | `app/timer/controller/VirtualRefundCompensation.php`、systemd `yoshop2.0-timer.service` |
| 7 | 微信发货信息同步：订单完成时调 `upload_shipping_info` 上报（合规要求），与 xpay 确认发货是两条独立上报 | `app/common/service/order/Shipping.php` |
| 8 | 支付金额校验：`goodsPrice(分) × buyQuantity == 订单 pay_price`，商品必须绑定 `vp_product_id` + 价格快照 | `app/api/service/cashier/Payment.php:437-448` |
| 9 | 协议页：服务政策/隐私协议是后台富文本（`yoshop_help`，2 篇） | 数据库 |
| 10 | 双后台：平台 admin + 商家 store（当前仅 1 商家账号"张宇"在用） | `yoshop_store_user` |

### 1.4 明确不搬的东西

- **打手抢单结算系统（员工系统 PRD）**：PRD 已写（252 行）但代码零实现、表零建、数据零条。YAGNI，新系统不建，业务真起来再说。
- 优惠券/积分/会员等级/余额充值/购物车/物流快递/多商户/门店/打印机/DIY 装修引擎/地区表（3835 行）/评论。
- Redis、独立管理后台前端构建链、OSS/COS、Docker。

---

## 二、技术选型（轻量第一性原理）

约束：单机、个位数十用户、月十位数订单、服务器 3.6G 内存已吃紧（swap 在用）。**结论：把"常驻进程数"和"构建链"压到最低。**

| 层 | 选型 | 理由 |
|---|---|---|
| 后端 | **Go 1.23+，标准库 `net/http`（1.22+ 路由）+ chi 风格中间件**，单二进制 | 无框架魔法；go.mod 依赖预计 ≤5 个 |
| 数据库 | **SQLite（WAL 模式，modernc.org/sqlite 纯 Go 驱动）**，单文件 | 12 笔订单的量级远够；备份 = `cp` 一个文件；省掉 MySQL 常驻依赖。若未来确需多写并发再升 MySQL（表结构是普通 SQL，平移成本极低） |
| 缓存/会话 | 无 Redis。access_token 内存缓存 + 过期刷新；用户态 = HMAC 签名 token（无状态） | 每分钟万级请求才需要 Redis |
| 定时任务 | 进程内 goroutine ticker（查单补偿/退款收敛/超时关单），不单开 systemd 服务 | 少一个常驻单元 |
| 管理后台 | **Go 二进制 `embed` 内嵌极简管理页**（原生 HTML/JS，无 node 构建链），密码 + bcrypt 登录 | 商品 CRUD/订单状态/退款审核/反馈回复/公告协议编辑，共 ~10 屏，用不上 Vue 工程 |
| 小程序前端 | **微信原生小程序，不引 npm/UI 库** | 3 个 tab + 订单/详情/退款等 ~10 页，手写样式比引 Vant 更小更可控 |
| 文件上传 | 本地磁盘 `data/uploads/`，nginx 静态托管 | 676K 的体量不配用云存储 |
| 部署 | 同机 systemd 单服务，nginx 挂路径前缀 `/v2/api/` → `127.0.0.1:8096` | **复用现有域名与证书，小程序合法域名零改动** |

内存收益：Go 服务 ~20-40MB，替代掉 PHP-FPM pool + YoShop + timer + 该业务的 Redis 占用。

---

## 三、数据模型（SQLite，61 张表 → 9 张）

```sql
-- 1. 用户
CREATE TABLE users (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  openid TEXT NOT NULL UNIQUE,
  session_key TEXT,                -- 仅服务端签名用
  nickname TEXT DEFAULT '',
  avatar TEXT DEFAULT '',
  mobile TEXT DEFAULT '',
  status INTEGER DEFAULT 1,        -- 1正常 0封禁
  created_at INTEGER, last_login_at INTEGER
);

-- 2. 分类
CREATE TABLE categories (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL, sort INTEGER DEFAULT 100, status INTEGER DEFAULT 1
);

-- 3. 服务商品（含 xpay 绑定）
CREATE TABLE services (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  category_id INTEGER REFERENCES categories(id),
  title TEXT NOT NULL,
  cover TEXT DEFAULT '',
  price_cents INTEGER NOT NULL,          -- 分
  sales_base INTEGER DEFAULT 0,          -- 后台填的起始已售
  sales_actual INTEGER DEFAULT 0,        -- 真实成交
  content TEXT DEFAULT '',               -- 富文本/MD
  xpay_product_id TEXT NOT NULL,         -- 虚拟支付商品ID
  xpay_env INTEGER DEFAULT 0,            -- 0正式 1沙箱
  status INTEGER DEFAULT 1, sort INTEGER DEFAULT 100,
  created_at INTEGER, updated_at INTEGER
);

-- 4. 订单（状态机主表）
CREATE TABLE orders (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  order_no TEXT NOT NULL UNIQUE,         -- 商户单号 out_trade_no
  user_id INTEGER NOT NULL REFERENCES users(id),
  service_id INTEGER NOT NULL,
  title_snapshot TEXT NOT NULL, cover_snapshot TEXT DEFAULT '',
  price_cents INTEGER NOT NULL, quantity INTEGER NOT NULL DEFAULT 1,
  total_cents INTEGER NOT NULL,
  status INTEGER NOT NULL DEFAULT 0,     -- 0待支付 1待联系 2服务中 3已完成 4已取消 5退款中 6已退款
  pay_order_id TEXT,                     -- 微信侧 pay_order_id（回调后回填）
  transaction_id TEXT,
  pay_snapshot TEXT,                     -- 最近一次支付参数+查单结果 JSON（审计）
  provide_goods_status TEXT DEFAULT '',  -- none/sending/success/failed/locked
  risk_status INTEGER DEFAULT 0,         -- iOS 退款风控: 0无 1命中锁定 2已放行
  risk_source TEXT DEFAULT '',
  cancel_reason TEXT DEFAULT '',
  pay_time INTEGER, complete_time INTEGER,
  created_at INTEGER, updated_at INTEGER
);
CREATE INDEX idx_orders_user ON orders(user_id, status, created_at DESC);

-- 5. 退款单
CREATE TABLE refunds (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  order_id INTEGER NOT NULL REFERENCES orders(id),
  refund_no TEXT NOT NULL UNIQUE,
  amount_cents INTEGER NOT NULL,
  reason TEXT DEFAULT '',
  status TEXT NOT NULL,                  -- applying/approved/refunded/rejected/failed
  mode TEXT NOT NULL,                    -- auto/audit（申请时服务端判定并快照）
  audit_note TEXT DEFAULT '',
  created_at INTEGER, updated_at INTEGER
);

-- 6. iOS 退款查询推送（风控证据链）
CREATE TABLE refund_inquiries (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  order_id INTEGER REFERENCES orders(id),
  pay_order_id TEXT, fingerprint TEXT UNIQUE,   -- 去重
  request_reason TEXT, service_stage TEXT, suggestion TEXT,
  raw TEXT NOT NULL,                     -- 原始报文
  created_at INTEGER
);

-- 7. 反馈/投诉（自建，替代"投诉入口"）
CREATE TABLE feedbacks (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL,
  type TEXT DEFAULT 'complaint',         -- complaint/advice/other
  content TEXT NOT NULL, images TEXT DEFAULT '',
  reply TEXT DEFAULT '', status INTEGER DEFAULT 0,  -- 0待处理 1已回复
  created_at INTEGER, replied_at INTEGER
);

-- 8. 站点设置（公告/Banner/协议/虚拟支付配置）
CREATE TABLE settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL                    -- JSON
);

-- 9. 事件流水（xpay 回调/发货上报/风控动作，审计用）
CREATE TABLE events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  kind TEXT NOT NULL,                    -- xpay_notify/provide_goods/refund_sync/...
  ref_type TEXT, ref_id INTEGER,
  payload TEXT NOT NULL,
  created_at INTEGER
);
```

管理员账号不做表：单个 bcrypt 哈希放配置文件。

---

## 四、API 清单（前缀 `/v2/api`）

### 用户端（小程序）
```
POST /auth/login            code 换 openid，签发 token
POST /auth/phone            手机号快速验证组件 code 解密绑定（可选）
GET  /home                  聚合：公告+banner+分类+推荐服务（1 个请求渲染首页）
GET  /categories
GET  /services?category_id=&keyword=&cursor=
GET  /services/:id
POST /orders                创建订单（含防重 client_token；下单拦截弹窗由前端做，后端只管风控字段）
POST /orders/:id/pay        返回 {mode, signData, paySig, signature}（服务端生成全部签名）
POST /orders/:id/cancel     仅待支付可取消
GET  /orders?status=&cursor=（status: all/pending/contact/serving/done）
GET  /orders/:id
POST /orders/:id/refund     申请退款（服务端按状态机判定 auto/audit/none）
GET  /refunds               我的退款单
GET  /feedbacks  POST /feedbacks
GET  /docs/:key             key = privacy | service-policy（渲染富文本）
```

### 微信回调/推送
```
POST /wx/msg                微信消息推送入口（含 xpay_subscribe_ios_refund_query_notify：
                            验签→AES解密→按服务阶段生成"建议退款/拒绝"应答→落 refund_inquiries→风控动作）
```
> xpay 的查单与确认发货是服务端主动调用 `api.weixin.qq.com/xpay/*`，无回调地址；支付成功靠前端回执 + 查单补偿收敛。

### 管理端（`/v2/api/admin`，bcrypt 登录 + 签名 cookie）
```
POST /login
CRUD /services  /categories           商品与分类
GET  /orders?status=   PUT /orders/:id/status     标记：开始服务/完成/取消
GET  /refunds  POST /refunds/:id/decision      退款审核（同意→调 xpay refund_order；拒绝→回滚状态）
PUT  /feedbacks/:id/reply
GET/PUT /settings                     公告、banner、协议、下单拦截开关、未成年人提示文案
POST /upload                          封面上传（multipart，限图）
GET  /stats                           今日/累计订单与收入（可选）
```

### 状态机（与老系统 1:1）
```
0待支付 ──支付成功──▶ 1待联系 ──商家"开始服务"──▶ 2服务中 ──商家"完成"──▶ 3已完成
   │                    │                          │
   └─超时30min/用户取消─▶ 4已取消                    │
                        ├─申请退款→AUTO直接退─▶ 5退款中─▶ 6已退款
                        └─申请退款→AUDIT──────▶ 5退款中─(同意→6 / 拒绝→回2)
3已完成：不可退款（NONE）
风控：refund_inquiry 命中 → 禁止 provide_goods 上报（locked），人工放行
```

支付成功收敛路径（三条，任一到达即幂等收敛）：
1. 前端支付成功回执 → 服务端 `query_order` 确认；
2. 每 60s ticker 对 `status=0 且有 pay_order_id` 或退款未收敛的订单查单补偿；
3. 订单详情被用户打开时惰性查单。

---

## 五、仓库结构

```
dianjing/
├── PLAN.md  API.md  DEPLOY.md
├── backend/
│   ├── cmd/server/main.go          # 单入口：HTTP + ticker + embed admin
│   ├── cmd/migrate-yoshop/main.go  # 一次性迁移脚本（MySQL → SQLite）
│   └── internal/
│       ├── api/        # 用户端 handler
│       ├── admin/      # 管理端 handler + embed 静态页
│       ├── xpay/       # 虚拟支付：签名、query_order/refund_order/notify_provide_goods、消息推送验签解密
│       ├── store/      # sqlite 访问层
│       └── app/        # 状态机、退款模式引擎、补偿 ticker
├── miniapp/                        # 原生小程序（pages/index|category|user|order|order-detail|goods-detail|checkout|refund|feedback|docs）
└── deploy/
    ├── dianjing.service            # systemd
    ├── nginx-v2.conf               # /v2/api/ 反代片段
    └── backup.sh                   # 每晚 sqlite3 .backup + rclone/本地留存
```

Go 依赖预算：`modernc.org/sqlite`、`golang.org/x/crypto/bcrypt`，其余标准库。预计后端 2.5~3k 行。

---

## 六、数据迁移（一次性，`migrate-yoshop`）

| 老库 | 新库 | 说明 |
|---|---|---|
| `yoshop_user` + `yoshop_user_oauth` | users | openid 取 oauth 表；mobile 若空则置空（进新版引导绑定） |
| `yoshop_category`（2） | categories | 直迁 |
| `yoshop_goods` + `goods_sku` + `goods_image` + vp_* 字段 | services | price 取 SKU 价；cover 取首图；`vp_product_id`/`vp_price_snapshot` 必须带上，否则虚拟支付拉不起来 |
| `yoshop_order` + `order_goods`（12 单） | orders | 状态映射：pay_status=10→0待支付；pay_status=20 → order_status 10→1待联系 / 20(未发货)→1或2 按 delivery_status / 已收货→3；已退款单 → 6。带上 `pay_order_id`（payment_trade.trade_no）与 payload 快照，**退款重放依赖它** |
| `yoshop_order_refund`（8） | refunds | 全部映射为终态 refunded |
| `payment_ios_refund_inquiry`（2） | refund_inquiries | 保留证据链 |
| `yoshop_help`（2） | settings.docs | privacy / service-policy |
| `yoshop_page` 的 notice 项 | settings.notice | 公告文案 |
| `/srv/yoshop/shared/uploads`（676K） | data/uploads/ | 整目录拷贝，路径前缀重写 |

迁移脚本幂等，可反复跑；切换前最后执行一次取增量。

---

## 七、上线与回滚（零影响）

1. **并行运行**：老 PHP 不动。新 Go 服务监听 `127.0.0.1:8096`，nginx 挂 `location /v2/api/`。同域名同证书，小程序合法域名无需任何改动。
2. **开发期**：原生小程序用体验版，`env=1` 沙箱 + 低价商品走通全链路：登录 → 浏览 → 下单 → `wx.requestVirtualPayment` 拉起 → 查单收敛 → 待联系 → 后台改状态 → 退款（auto/audit 两路）→ xpay refund_order → 已退款 → 发货上报 + provide_goods 上报。
3. **风控回归**：模拟 iOS 退款查询推送（老代码里有 `VirtualPaymentLocalE2e`/`SandboxCheck` 命令可参照造报文），验证 locked 逻辑。
4. **切换** = 微信后台全量发布新版小程序。回滚 = 微信后台版本回退（老小程序 + 老 PHP 原样在线，秒级可退）。
5. **退役**：新版稳定 2~4 周后，停 `yoshop2.0-timer`、下线 PHP vhost（保留数据库备份 90 天）。

里程碑建议（均为可独立验证的交付物）：
- M1 后端骨架 + SQLite + 登录/首页/商品只读接口（curl 验收）
- M2 虚拟支付链路：签名参数 + 查单补偿 + 状态机 + provide_goods（沙箱 env=1 真机验收）
- M3 退款引擎 + iOS 风控推送 + 补偿 ticker（E2E 报文验收）
- M4 管理后台 embed 页 + 设置/协议/上传
- M5 原生小程序全部页面 + 迁移脚本 + 试运行
- M6 体验版灰度 → 全量

---

## 八、风险与对策

| 风险 | 对策 |
|---|---|
| 虚拟支付签名/session_key 细节出错导致拉不起支付 | 签名公式与字段名严格照抄老实现（已在 §1.3 固化）；先沙箱 env=1 用 1 元商品联调 |
| offerId/appKey 等敏感配置 | 只放服务器配置文件（600 权限），不进 git；.env 模式沿用 |
| SQLite 并发写 | WAL + 单进程写入（所有写走同一服务）；量级下无风险 |
| 迁移丢支付单号导致老订单无法退款 | order_no/pay_order_id 双向核对脚本，迁移后对 8 笔历史退款做只读比对 |
| 微信平台对类目/虚拟支付的审核 | 小程序端不改经营类目，纯技术重构，提审说明沿用现有资质 |
