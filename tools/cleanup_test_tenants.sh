#!/bin/bash
# 测试租户清理（C7，2026-09-12）：删除 UAT/权限冒烟/退款 E2E/单测遗留的临时租户及其级联业务数据。
# 安全：默认 dry-run 只报告；--apply 才真删。**绝不删 acme / 生产 / 默认租户**（仅匹配 uat/perm/rfd/channel_smoke/unit_test/e2e-e2e/e2etest 等测试前缀）。
# 周报/CI 收尾手动调用，非自动删（避免误伤并行调试中的租户）。
# 用法: ./tools/cleanup_test_tenants.sh [--apply]
set -euo pipefail
DBURL="${TEST_DB_URL:-postgresql://ai_scrm:dev123@localhost/ai_scrm}"
APPLY=0
[ "${1:-}" = "--apply" ] && APPLY=1

# 匹配测试租户前缀（含进程后缀，如 uata79365 / unit_test_tenant 复用 code；e2e 来自 smoke_saas）
PATTERNS=("uat%" "perm%" "rfd%" "chan_smoke%" "unit_test_tenant%" "rls_a%" "rls_b%" "e2e-e2e%" "e2etest")

build_where() {
  local i=0 cond=""
  for p in "${PATTERNS[@]}"; do
    [ $i -gt 0 ] && cond="$cond OR "
    cond="${cond}code LIKE '$p'"
    i=$((i+1))
  done
  echo "$cond"
}
WHERE=$(build_where)

IDS=$(psql "$DBURL" -tAc "SELECT id FROM tenants WHERE $WHERE ORDER BY id;" | tr -d '\r' | paste -sd, -)
if [ -z "${IDS// /}" ]; then
  echo "无匹配的测试租户，无需清理。"
  exit 0
fi
CNT=$(echo "$IDS" | tr ',' '\n' | grep -c .)
echo "匹配到 $CNT 个测试租户：id=[$IDS]"
psql "$DBURL" -c "SELECT id,code,name,status FROM tenants WHERE id IN ($IDS) ORDER BY id;" 2>/dev/null

if [ "$APPLY" = "0" ]; then
  echo "[dry-run] 未删除。确认无误后执行：$0 --apply"
  exit 0
fi

# 级联删除：动态发现"所有含 tenant_id 列的业务表"逐个删（schema 漂移免疫），最后删 tenants 行。
# 不用单一大事务：一条表结构异常不应回滚掉关键删除；逐语句独立执行 + 幂等。
DLIST=$(psql "$DBURL" -tAc "
  SELECT tablename FROM pg_tables
  WHERE schemaname='public' AND tablename <> 'tenants'
    AND tablename IN (
      SELECT table_name FROM information_schema.columns
      WHERE table_schema='public' AND column_name='tenant_id'
    )
  ORDER BY tablename;" | tr -d '\r' | grep -vE '^(tenants)$' || true)

echo "将清理的含 tenant_id 业务表：$(echo "$DLIST" | tr '\n' ' ')"

# 先删子表数据（逐条独立执行，ON_ERROR_STOP=0 容错），再删租户主行
for t in $DLIST; do
  res=$(psql "$DBURL" -tAc "DELETE FROM $t WHERE tenant_id IN ($IDS);" 2>&1)
  case "$res" in
    DELETE*) : ;;
    *) echo "  [warn] $t 删除异常：$res" ;;
  esac
done
psql "$DBURL" -c "DELETE FROM tenants WHERE id IN ($IDS);" >/dev/null 2>&1
# 陈旧 ut_* 测试包回收（packages 是全局目录表不挂租户；testutil.CleanupTenant 带 1h 年龄窗，
# 长期不跑单测的环境残留由此兜底——仅回收无任何订单引用的测试包，绝不触碰真实在售包）
STALE_PKGS=$(psql "$DBURL" -tAc "DELETE FROM packages WHERE code LIKE 'ut\_%' AND id NOT IN (SELECT COALESCE(package_id,0) FROM billing_orders WHERE package_id IS NOT NULL) RETURNING id;" 2>/dev/null | grep -c . || true)
echo "已回收陈旧 ut_* 测试包 $STALE_PKGS 个。"
echo "已清理 $CNT 个测试租户及其级联数据。"
