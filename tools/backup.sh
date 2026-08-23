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
#           KEEP_DAYS(默认7) / BACKUP_REMOTE_CMD(异地推送,可选)
#
# 异地推送（批次三·防单点，2026-08-23）：
#   配置 BACKUP_REMOTE_CMD 后，本地备份+完整性校验通过即自动推远端；
#   {{FILE}} 占位符会被替换为本次备份文件路径。示例：
#     BACKUP_REMOTE_CMD="rclone copy {{FILE}} remote:ai-scrm-backups"
#     BACKUP_REMOTE_CMD="ossutil cp -f {{FILE}} oss://mybucket/scrm-backup/"
#     BACKUP_REMOTE_CMD="scp {{FILE}} backup@nas:/volume1/scrm-backup/"
#   推送失败仅告警不中断（本地副本仍在）；远端保留策略由远端自行管理
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

# 异地推送钩子（批次三）：BACKUP_REMOTE_CMD 配置即启用，{{FILE}} 替换为本次备份路径
if [ -n "${BACKUP_REMOTE_CMD:-}" ]; then
  REMOTE_CMD="${BACKUP_REMOTE_CMD//\{\{FILE\}\}/$FILE}"
  echo "[$(date '+%F %T')] 异地推送开始: $REMOTE_CMD"
  if bash -c "$REMOTE_CMD"; then
    echo "[$(date '+%F %T')] 异地推送成功"
  else
    echo "[$(date '+%F %T')] [WARN] 异地推送失败(本地副本完好，请检查远端配置/网络)" >&2
  fi
else
  echo "[$(date '+%F %T')] 未配置 BACKUP_REMOTE_CMD，跳过异地推送"
fi

# 保留策略：删除超期备份
find "$BACKUP_DIR" -name "ai_scrm_*.dump" -mtime +"$KEEP_DAYS" -print -delete |
  while read -r f; do echo "[$(date '+%F %T')] 清理过期备份: $f"; done

echo "[$(date '+%F %T')] 全部完成"
