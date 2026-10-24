#!/bin/bash
# dianjing 每晚备份：SQLite 在线备份 + 保留 14 份。
# 无 sqlite3 CLI，用 python3 sqlite3 的 backup API（等价 .backup，WAL 安全）。
# crontab：30 3 * * * /srv/dianjing/backup.sh >> /var/log/dianjing-backup.log 2>&1
set -euo pipefail

DATA_DIR="${DATA_DIR:-/srv/dianjing/data}"
KEEP=14
DEST="/srv/dianjing/backups"
DB="$DATA_DIR/dianjing.db"

mkdir -p "$DEST"
STAMP=$(date +%Y%m%d-%H%M%S)
OUT="$DEST/dianjing-$STAMP.db"

python3 - "$DB" "$OUT" <<'EOF'
import sqlite3, sys
src, dst = sys.argv[1], sys.argv[2]
s = sqlite3.connect(f"file:{src}?mode=ro", uri=True)
d = sqlite3.connect(dst)
s.backup(d)
d.close(); s.close()
EOF

gzip -f "$OUT"
echo "$(date '+%F %T') backup -> $OUT.gz ($(stat -c%s "$OUT.gz") bytes)"

# 只保留最近 KEEP 份
ls -1t "$DEST"/dianjing-*.db.gz 2>/dev/null | tail -n +$((KEEP + 1)) | xargs -r rm --
