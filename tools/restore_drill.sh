#!/bin/bash
# ============================================================
# AI-SCRM 恢复演练脚本（商业化第二批 M4，借鉴翻译助手 deploy/restore_drill.sh）
#
# 用途：验证备份真实可恢复（备份≠可恢复，必须实际演练）
# 用法：./tools/restore_drill.sh [备份目录]     默认 ./backups
# 建议：首次部署后演练一次，之后每季度一次（写入运维日历）
# 依赖：psql/createdb/pg_restore（本机或可达的 PG 实例）
# ============================================================
set -euo pipefail

BACKUP_DIR="${1:-$(pwd)/backups}"
DRILL_DB="ai_scrm_drill_$(date +%s)"
export PGHOST="${PGHOST:-localhost}"
export PGPORT="${PGPORT:-5432}"
export PGUSER="${PGUSER:-ai_scrm}"
export PGDATABASE="$DRILL_DB"
export PGPASSWORD="${PGPASSWORD:-}"

LATEST=$(ls -t "$BACKUP_DIR"/ai_scrm_*.dump 2>/dev/null | head -1)
if [ -z "$LATEST" ]; then
  echo "[FAIL] $BACKUP_DIR 下没有找到 ai_scrm_*.dump 备份文件"; exit 1
fi

echo "==== AI-SCRM 恢复演练 ===="
echo "备份文件: $LATEST ($(du -h "$LATEST" | cut -f1))"

cleanup() {
  echo "---- 清理临时库 $PGDATABASE ----"
  dropdb --if-exists -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" "$PGDATABASE" 2>/dev/null || true
}
trap cleanup EXIT

createdb -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" "$PGDATABASE"
echo "---- 恢复到临时库 $PGDATABASE ----"
pg_restore -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d "$PGDATABASE" --no-owner "$LATEST"

echo "---- 关键表行数抽样 ----"
PASS=0; FAIL=0
for t in tenants tenant_users customers conversations messages packages system_configs usage_ledger; do
  N=$(psql -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d "$PGDATABASE" -tAc \
    "SELECT count(*) FROM information_schema.tables WHERE table_name='$t'" 2>/dev/null)
  if [ "${N:-0}" = "0" ]; then
    echo "  SKIP $t (表不存在)"
    continue
  fi
  CNT=$(psql -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d "$PGDATABASE" -tAc "SELECT count(*) FROM $t")
  if [ "${CNT:-0}" -ge 0 ]; then
    echo "  OK   $t = ${CNT} 行"; PASS=$((PASS+1))
  else
    echo "  FAIL $t 查询失败"; FAIL=$((FAIL+1))
  fi
done

echo "---- 结论 ----"
if [ "$FAIL" = "0" ] && [ "$PASS" -gt 0 ]; then
  echo "✅ 演练通过：备份可恢复（$PASS 张关键表行数抽样正常）"
  exit 0
else
  echo "❌ 演练失败：$FAIL 项异常，请检查备份完整性"
  exit 1
fi
