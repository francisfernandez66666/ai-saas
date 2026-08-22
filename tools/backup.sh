#!/bin/bash
# ============================================================
# AI-SCRM PostgreSQL 备份脚本（生产部署必挂 cron）
#
# 安装（每日 03:00 备份，保留 7 天）：
#   chmod +x tools/backup.sh
#   (crontab -l 2>/dev/null; echo "0 3 * * * $(pwd)/tools/backup.sh >> /var/log/ai-scrm-backup.log 2>&1") | crontab -
#
# 恢复演练（建议每月做一次）：
#   createdb ai_scrm_restore_test
#   pg_restore -h localhost -U ai_scrm -d ai_scrm_restore_test --no-owner <备份文件>
#   psql -h localhost -U ai_scrm -d ai_scrm_restore_test -c "SELECT COUNT(*) FROM customers;"
#
# 环境变量：BACKUP_DIR / PGHOST / PGPORT / PGUSER / PGPASSWORD / PGDATABASE
# ============================================================

set -euo pipefail

BACKUP_DIR="${BACKUP_DIR:-$(pwd)/backups}"
KEEP_DAYS="${KEEP_DAYS:-7}"
STAMP=$(date +%Y%m%d_%H%M%S)
FILE="$BACKUP_DIR/ai_scrm_$STAMP.dump"

export PGHOST="${PGHOST:-localhost}"
export PGPORT="${PGPORT:-5432}"
export PGUSER="${PGUSER:-ai_scrm}"
export PGDATABASE="${PGDATABASE:-ai_scrm}"
# PGPASSWORD 从环境或 ~/.pgpass 读取；也可用 pg_service.conf

mkdir -p "$BACKUP_DIR"

echo "[$(date '+%F %T')] 开始备份 $PGDATABASE → $FILE"
pg_dump --format=custom --compress=6 --no-owner --file="$FILE"

SIZE=$(du -h "$FILE" | cut -f1)
echo "[$(date '+%F %T')] 备份完成: $FILE ($SIZE)"

# 完整性抽检：列出归档内容头部（损坏会在此报错）
pg_restore --list "$FILE" > /dev/null && echo "[$(date '+%F %T')] 完整性校验通过"

# 保留策略：删除超期备份
find "$BACKUP_DIR" -name "ai_scrm_*.dump" -mtime +"$KEEP_DAYS" -print -delete |
  while read -r f; do echo "[$(date '+%F %T')] 清理过期备份: $f"; done

echo "[$(date '+%F %T')] 全部完成"
