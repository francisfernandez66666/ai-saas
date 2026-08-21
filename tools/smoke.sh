#!/bin/bash
# ============================================================
# AI-SCRM SaaS 安全红线冒烟测试（Phase S 验收脚本）
# 用法: ./tools/smoke.sh [端口]   默认 9090
# 前置: 服务已启动（./start.sh 或 go run cmd/server/main.go）
# 覆盖: 租户解析 fail-closed / debug 兜底 / JWT↔Host 一致性 /
#       超管跨租户显式指定+审计 / C端租户归属 / 基础数据隔离
# ============================================================

PORT="${1:-9090}"
B="http://localhost:${PORT}"
PASS=0
FAIL=0

check() { # check <名称> <期望码> <实际码>
  if [ "$2" = "$3" ]; then
    echo "  PASS  $1 ($3)"; PASS=$((PASS+1))
  else
    echo "  FAIL  $1 期望=$2 实际=$3"; FAIL=$((FAIL+1))
  fi
}

jsonget() { python3 -c "import sys,json;d=json.load(sys.stdin);print(eval('d'+sys.argv[1]))" "$1" 2>/dev/null; }

echo "==== AI-SCRM SaaS 安全冒烟测试 @ $B ===="

# ---- 准备：确保存在第二个测试租户 acme ----
psql postgresql://ai_scrm:dev123@localhost/ai_scrm -c \
  "INSERT INTO tenants (name, code, tier, status, created_at, updated_at)
   SELECT 'acme-test', 'acme', 'personal', 'active', NOW(), NOW()
   WHERE NOT EXISTS (SELECT 1 FROM tenants WHERE code='acme');" >/dev/null 2>&1
ACME_ID=$(psql postgresql://ai_scrm:dev123@localhost/ai_scrm -tAc "SELECT id FROM tenants WHERE code='acme'" | tr -d '[:space:]')

echo "---- 一、租户解析 fail-closed ----"
CODE=$(curl -s -o /dev/null -w "%{http_code}" "$B/health")
check "健康检查白名单放行" 200 "$CODE"

CODE=$(curl -s -o /dev/null -w "%{http_code}" -H "Host: nosuch.example.com" -X POST "$B/api/v1/chat/guest")
check "未知域名访问API被拒(fail-closed)" 403 "$CODE"

CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/chat/guest")
check "localhost debug 兜底可用(仅debug模式)" 200 "$CODE"

echo "---- 二、登录与超管跨租户 ----"
TOKEN=$(curl -s -X POST "$B/api/v1/auth/login" -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"admin123"}' | jsonget "['data']['token']")
[ ${#TOKEN} -gt 50 ] && check "admin 登录获取token" y y || check "admin 登录获取token" y n

CODE=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/admin/config" -H "Authorization: Bearer $TOKEN")
check "超管未显式指定→落默认租户" 200 "$CODE"

CODE=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/admin/config" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 999")
check "超管指定不存在租户→拒绝" 403 "$CODE"

CODE=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/admin/config" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: ${ACME_ID}")
check "超管显式切换到acme租户" 200 "$CODE"

AUDIT=$(psql postgresql://ai_scrm:dev123@localhost/ai_scrm -tAc \
  "SELECT count(*) FROM tenant_audit_logs WHERE action='super_admin_access'" 2>/dev/null | tr -d '[:space:]')
[ "${AUDIT:-0}" -ge 1 ] && check "超管访问审计日志已落库" y y || check "超管访问审计日志已落库" y n

echo "---- 三、JWT↔Host 一致性 + 数据隔离 ----"
STOKEN=$(curl -s -X POST "$B/api/v1/auth/login" -H "Content-Type: application/json" \
  -d '{"username":"sales1","password":"sales123"}' | jsonget "['data']['token']")
[ ${#STOKEN} -gt 50 ] && check "sales1 登录获取token" y y || check "sales1 登录获取token" y n

# 先在 acme 租户造一条客户数据（C端访客），再做隔离断言
CODE=$(curl -s -o /dev/null -w "%{http_code}" -H "Host: acme.example.com" -X POST "$B/api/v1/chat/guest")
check "acme域名C端访客注册正常" 200 "$CODE"

CODE=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/advisor/customers" -H "Authorization: Bearer $STOKEN")
check "销售访问本租户客户列表" 200 "$CODE"

CODE=$(curl -s -o /dev/null -w "%{http_code}" -H "Host: acme.example.com" \
  "$B/api/v1/advisor/customers" -H "Authorization: Bearer $STOKEN")
check "销售token打其他租户域名→一致性拦截" 403 "$CODE"

# 隔离断言：接口返回的每条客户记录都必须属于默认租户(1)
IDS=$(curl -s "$B/api/v1/advisor/customers?page_size=100" -H "Authorization: Bearer $STOKEN" | \
  python3 -c "import sys,json;l=json.load(sys.stdin)['data']['list'];print(','.join(str(c['id']) for c in l))" 2>/dev/null)
if [ -n "$IDS" ]; then
  BAD=$(psql postgresql://ai_scrm:dev123@localhost/ai_scrm -tAc \
    "SELECT count(*) FROM customers WHERE id IN ($IDS) AND tenant_id <> 1" 2>/dev/null | tr -d '[:space:]')
  [ "${BAD:-1}" = "0" ] && check "客户列表无跨租户数据混入(共$(echo $IDS | tr ',' ' ' | wc -w | tr -d ' ')条全属租户1)" y y \
                         || check "客户列表无跨租户数据混入(混入${BAD}条)" y n
else
  echo "  SKIP  隔离断言（本租户暂无可见客户）"
fi

echo "==== 结果: PASS=$PASS FAIL=$FAIL ===="
[ "$FAIL" = "0" ] || exit 1
