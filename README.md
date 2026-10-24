# dianjing

观星电竞小程序重构版：Go + SQLite 后端 + 微信原生小程序前端。设计文档见 [PLAN.md](PLAN.md)，部署步骤见 [DEPLOY.md](DEPLOY.md)。

## 目录

```
backend/   Go 后端（单二进制，net/http + modernc.org/sqlite）
miniapp/   微信原生小程序（10 页，无 npm/UI 库）
deploy/    systemd / nginx 反代片段 / 备份脚本
PLAN.md    调研结论 + 重构规划（2026-10-24 基于服务器实况）
DEPLOY.md  构建/配置/迁移/systemd/nginx/备份/发版步骤
```

## 后端开发

```bash
cd backend
GOSUMDB=off GOPROXY=off /usr/local/go1.27/bin/go build ./...
GOSUMDB=off GOPROXY=off /usr/local/go1.27/bin/go run ./cmd/server   # 先 cp dianjing.env.example dianjing.env
curl localhost:8096/v2/api/home   # 冒烟
```

> module 要求 go ≥ 1.24（mysql driver），本机统一用 /usr/local/go1.27/bin/go。

## 小程序开发

微信开发者工具直接打开 `miniapp/`。appid 已写在 project.config.json。
请求基址默认同域相对路径（`app.js` 的 `globalData.baseUrl` 留空），依赖 nginx 把 `/v2/` 反代到 Go 服务同一域名。

## 功能

- 用户端：浏览 → 下单 → `wx.requestVirtualPayment`（服务端签名）→ confirm-pay 查单收敛（另有两级兜底：60s ticker、订单页惰性查单）→ 取消/退款（auto 直退、audit 审核两路）→ 意见反馈 → 协议文档
- 管理端 `/v2/admin`：bcrypt 登录，服务/分类/订单/退款审核/反馈回复/公告协议/图片上传
- 兼容：iOS App Store 退款推送（xpay_subscribe_ios_refund_query_notify）→ 风控锁定/自动应答

## 里程碑进度（详见 PLAN.md §七）

- [x] M1 后端骨架 + SQLite + 登录/首页/商品/文档只读接口
- [x] M2 xpay 虚拟支付链路（签名/下单/查单/状态机/provide_goods）
- [x] M3 退款引擎 + iOS 风控推送 + 补偿 ticker
- [x] M4 管理后台（embed 单页）
- [x] M5 原生小程序 + 迁移脚本 migrate-yoshop
- [ ] 收尾：DEPLOY.md 已备好，按步骤上线（sandbox 体验版联调 → 全量）
