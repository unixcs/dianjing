# dianjing

观星电竞小程序重构版：Go + SQLite 后端 + 微信原生小程序前端。设计文档见 [PLAN.md](PLAN.md)。

## 目录

```
backend/   Go 后端（单二进制，net/http + modernc.org/sqlite）
miniapp/   微信原生小程序
deploy/    systemd / nginx / 备份脚本
PLAN.md    调研结论 + 重构规划（2026-10-24 基于服务器实况）
```

## 后端开发

```bash
cd backend
go build ./...            # 构建
go run ./cmd/server -config dianjing.env   # 本地运行（先 cp dianjing.env.example dianjing.env）
curl localhost:8096/v2/api/home            # 冒烟
```

里程碑进度（详见 PLAN.md §七）：

- [x] M1 后端骨架 + SQLite + 登录/首页/商品/文档只读接口
- [ ] M2 xpay 虚拟支付链路（签名/下单/查单/状态机/provide_goods）
- [ ] M3 退款引擎 + iOS 风控推送 + 补偿 ticker
- [ ] M4 管理后台（embed 单页）
- [ ] M5 原生小程序 + 迁移脚本 migrate-yoshop
