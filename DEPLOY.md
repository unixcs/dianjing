# 部署（观星电竞 v2）

目标布局：`/srv/dianjing/{dianjing-server, dianjing.env, data/, backups/, backup.sh}`。

## 1. 构建

```bash
cd /mnt/vps/projects/dianjing/backend
GOSUMDB=off GOPROXY=off /usr/local/go1.27/bin/go build -o /srv/dianjing/dianjing-server ./cmd/server
```

> 本机 /usr/local/go 是 1.23.4，module 已升到 go 1.24.0，必须用 /usr/local/go1.27/bin/go。

## 2. 配置

```bash
cp backend/dianjing.env.example /srv/dianjing/dianjing.env && chmod 600 /srv/dianjing/dianjing.env
# 填 WX_APP_ID / WX_APP_SECRET / XPAY_APP_KEY / XPAY_PUSH_TOKEN / XPAY_PUSH_AES_KEY / ADMIN_PASSWORD_HASH
# XPAY_SANDBOX=1 起步（体验版沙箱联调），全量前改 0
# 管理后台密码 hash：go run ./cmd/hashpw -p '你的密码'
```

敏感项（appKey/appSecret/push key/密码 hash）只存在这份 600 文件里，不进 git。

## 3. 数据

```bash
# 全新数据目录会自动建表；从老 YoShop 迁移：
go run ./cmd/migrate-yoshop -dsn 'yoshop2_app:...@tcp(127.0.0.1:3306)/yoshop2_prod' \
    -data /srv/dianjing/data -uploads /srv/yoshop/shared/uploads
# 幂等，切换前可重跑取增量
```

## 4. systemd + nginx

```bash
cp deploy/dianjing.service /etc/systemd/system/
systemctl daemon-reload && systemctl enable --now dianjing
mkdir -p /etc/nginx/snippets && cp deploy/nginx-v2.conf /etc/nginx/snippets/
# 在 wx.gxwqb.cn.production.conf 的 443 server 块加：
#   location ^~ /v2/ { include /etc/nginx/snippets/dianjing-v2.conf; }
nginx -t && systemctl reload nginx
```

验证：`curl -s https://wx.gxwqb.cn/v2/api/home` 与 `curl -s https://wx.gxwqb.cn/v2/admin`。

## 5. 备份

```bash
install -m 700 deploy/backup.sh /srv/dianjing/backup.sh
crontab 加：30 3 * * * /srv/dianjing/backup.sh >> /var/log/dianjing-backup.log 2>&1
```

每晚 SQLite 在线备份（python3 backup API，WAL 安全），保留 14 份。

## 6. 小程序发版

1. 微信开发者工具打开 `miniapp/`（appid wxbf57e6f2ba72b712 已配在 project.config.json），上传体验版；
2. 体验版沙箱链路：登录 → 下单 → `wx.requestVirtualPayment` → confirm-pay 收敛 → 后台改状态 → 退款；
3. 全量前把 `XPAY_SANDBOX=0` 并重启服务，微信后台提审发布。
   回滚 = 微信后台版本回退（老小程序 + 老 PHP 全程未动）。

## 7. 退役老系统（稳定 2~4 周后）

停 `yoshop2.0-timer`，摘掉 PHP vhost，MySQL 备份保留 90 天。
