#!/bin/bash
# ============================================================
# 独立字节级 UAT 复核脚本（2026-09-22 第三方视角，不复用项目既有断言）
#
# 目的：不信任仓库自带脚本里"自己的断言"，用独立实现重新验证三类链路：
#   A. BE↔BE  字节级：API 写入 → psql 直读原文 → 逐字节比对；psql 直写 → API 读回归
#   B. FE↔BE  字节级：服务端托管产物 md5 vs 构建产物 md5
#   C. 分支/异常字节级：401/403/404/400 与统一响应信封
#
# 隔离策略：临时 SQL 造一个一次性租户 + 一个 tenant_admin（复用 admin 的密码哈希），
#          全程只在该租户内操作，绝不触碰种子租户与既有测试数据；结束整租户回收。
#          用 SQL 造租户而非 /tenant/signup，是因为注册链路受 email_verify_enabled 管辖，
#          会让复核脚本依赖邮件验证码，不适合作为独立基线。
#
# 用法： ./tools/indep_byte_uat.sh [port]
# ============================================================
set -u
PORT="${1:-9090}"
B="http://localhost:${PORT}"
DBURL="${TEST_DB_URL:-postgresql://ai_scrm:dev123@localhost/ai_scrm}"
Q(){ psql "$DBURL" -tAc "$1" 2>&1; }
PASS=0; FAIL=0; declare -a FAILED
ck(){ if [ "$2" = "$3" ]; then PASS=$((PASS+1)); echo "  PASS  $1";
  else FAIL=$((FAIL+1)); FAILED+=("$1 | 期望=[$2] 实际=[$3]"); echo "  FAIL  $1"; printf '        期望=[%s]\n' "$2"; printf '        实际=[%s]\n' "$3"; fi }
jget(){ python3 -c "
import sys,json
try:
    d=json.load(sys.stdin); v=eval(sys.argv[1],{'d':d}); print('' if v is None else v)
except Exception: print('')" "$1"; }
bsha(){ printf '%s' "$1" | shasum -a 256 | cut -d' ' -f1; }
hc(){ curl -s -o /dev/null -w '%{http_code}' "$@"; }

echo "==== 独立字节级 UAT 复核 @ $B ===="
echo "---- 0. 前置：服务可达性 ----"
ck "GET /status 200" 200 "$(hc -m 5 "$B/status")"

echo "---- 1. 造一次性隔离租户（不碰种子数据）----"
TS=$(date +%s); TCODE="ibv$((TS%100000))$((RANDOM%100))"; RUN="ibu$TS"
TSQL_ID=$(Q "INSERT INTO tenants (name, code) VALUES ('独立复核租户','$TCODE') RETURNING id")
# psql 对 INSERT...RETURNING 会同时回显 "INSERT 0 1" 状态行，只取纯数字首行
TID=$(printf '%s' "$TSQL_ID" | tr -dc '0-9\n' | head -1 | tr -d '[:space:]')
[ -z "$TID" ] && { echo "  FATAL 造租户失败：$TSQL_ID"; exit 2; }
echo "  租户 id=$TID code=$TCODE"
Q "INSERT INTO tenant_users (username, password_hash, role, tenant_id, status, must_change_password)
   SELECT '$RUN', password_hash, 'tenant_admin', $TID, 1, false FROM tenant_users WHERE username='admin'" >/dev/null
LOGIN=$(curl -s -X POST "$B/api/v1/auth/login" -H "Content-Type: application/json" \
  -d "{\"tenant_code\":\"$TCODE\",\"username\":\"$RUN\",\"password\":\"admin123\"}")
TOKEN=$(printf '%s' "$LOGIN" | jget "d['data']['token']")
AH="Authorization: Bearer $TOKEN"
[ -n "$TOKEN" ] && ck "一次性租户管理员登录拿到 token" ok ok || ck "一次性租户管理员登录拿到 token" ok "空"
ck "登录者角色 = tenant_admin" tenant_admin "$(curl -s "$B/api/v1/auth/me" -H "$AH" | jget "d['data']['role']")"

echo "---- 2. BE↔BE：POST /customers 写入 → psql 直读逐字节比对 ----"
NAME="IBV-$TS-特殊字符<&\\\"'>与中文é€😀"
PJSON=$(python3 -c "import json,sys;print(json.dumps({'name':sys.argv[1],'phone':'13900001111'},ensure_ascii=False))" "$NAME")
CRE=$(curl -s -X POST "$B/api/v1/customers" -H "$AH" -H "Content-Type: application/json" -d "$PJSON")
CID=$(printf '%s' "$CRE" | jget "d['data']['id']")
ck "创建客户 code=0" 0 "$(printf '%s' "$CRE" | jget "d['code']")"
ck "API→DB 客户名逐字节一致（含引号/emoji/重音）" "$NAME" "$(Q "SELECT name FROM customers WHERE id=${CID:-0}")"
ck "客户归属租户 = 本次新建租户" "$TID" "$(Q "SELECT tenant_id FROM customers WHERE id=${CID:-0}")"

echo "---- 3. BE↔BE：DB 直写 → API 读回逐字节比对（反向） ----"
REV="REV-$TS-反向中文é€😀"
Q "UPDATE customers SET name='$REV' WHERE id=$CID" >/dev/null 2>&1
ck "DB→API 反向逐字节一致（含 emoji/重音）" "$REV" "$(curl -s "$B/api/v1/customers/$CID" -H "$AH" | jget "d['data']['name']")"

echo "---- 4. BE↔BE：响应体字节自洽（两次同请求，字节级幂等） ----"
ck "GET /plans 两次响应字节一致" "$(bsha "$(curl -s "$B/api/v1/plans")")" "$(bsha "$(curl -s "$B/api/v1/plans")")"
ck "GET /status 两次响应字节一致" "$(bsha "$(curl -s "$B/status")")" "$(bsha "$(curl -s "$B/status")")"
ck "GET /api/v1/openapi/spec 两次响应字节一致" \
  "$(bsha "$(curl -s "$B/api/v1/openapi/spec")")" "$(bsha "$(curl -s "$B/api/v1/openapi/spec")")"

echo "---- 5. FE↔BE：服务端托管产物 vs 构建产物 字节比对 ----"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
IDX=$(curl -s -m 5 "$B/")
for p in $(printf '%s' "$IDX" | /usr/bin/grep -oE '/assets/[^"]+'); do
  base=$(basename "$p"); curl -s -m 15 "$B$p" -o "/tmp/ib_$base" 2>/dev/null
  if [ -f "$ROOT/frontend-react/dist/assets/$base" ]; then
    ck "托管产物 $base 与 dist 字节一致" \
      "$(md5 -q "$ROOT/frontend-react/dist/assets/$base")" "$(md5 -q "/tmp/ib_$base")"
  else ck "托管产物 $base 存在于 dist" yes no; fi
done
curl -s -m 5 "$B/" -o /tmp/ib_index.html
ck "托管 index.html 与 dist 字节一致" \
  "$(md5 -q "$ROOT/frontend-react/dist/index.html")" "$(md5 -q /tmp/ib_index.html)"

echo "---- 6. 分支/异常字节级：鉴权与错误码 ----"
ck "无 token GET /customers 401" 401 "$(hc "$B/api/v1/customers")"
ck "坏 token GET /customers 401" 401 "$(hc "$B/api/v1/customers" -H 'Authorization: Bearer not.a.jwt')"
ck "tenant_admin 打 /super/tenants 403" 403 "$(hc "$B/api/v1/super/tenants" -H "$AH")"
ck "tenant_admin 打本租户 /admin/audit-logs 200" 200 "$(hc "$B/api/v1/admin/audit-logs" -H "$AH")"
ck "不存在的客户 404" 404 "$(hc "$B/api/v1/customers/99999999" -H "$AH")"
ck "非法 JSON 400" 400 "$(hc -X POST "$B/api/v1/auth/login" -H 'Content-Type: application/json' -d '{bad json')"
ck "/api 前缀未知路径 404（不走 SPA）" 404 "$(hc "$B/api/v1/definitely-not-exist")"
ck "SPA 未知前台路径回落 200" 200 "$(hc "$B/some/deep/frontend/route")"
ck "/status/detail 无令牌 403（fail-closed）" 403 "$(hc "$B/status/detail")"
ck "404 响应体是统一信封（含 code 字段）" yes \
  "$(curl -s "$B/api/v1/customers/99999999" -H "$AH" | python3 -c "import sys,json;d=json.load(sys.stdin);print('yes' if 'code' in d else 'no')" 2>/dev/null || echo no)"

echo "---- 7. 多租户隔离字节级 ----"
ck "本租户上下文查本租户客户 200" 200 "$(hc "$B/api/v1/customers/$CID" -H "$AH")"
ATOK=$(curl -s -X POST "$B/api/v1/auth/login" -H "Content-Type: application/json" -d '{"username":"admin","password":"admin123"}' | jget "d['data']['token']")
OTHERT=$(printf '%s' "$(Q "SELECT id FROM tenants WHERE id <> $TID ORDER BY id LIMIT 1")" | tr -d '[:space:]')
ck "超管无 X-Tenant-ID 访问租户面 400（租户宣言 fail-closed）" 400 \
  "$(hc "$B/api/v1/customers/$CID" -H "Authorization: Bearer $ATOK")"
ck "超管切他租户上下文查本租户客户 404（跨租户不可见）" 404 \
  "$(hc "$B/api/v1/customers/$CID" -H "Authorization: Bearer $ATOK" -H "X-Tenant-ID: $OTHERT")"
ck "超管切本租户上下文查该客户 200" 200 \
  "$(hc "$B/api/v1/customers/$CID" -H "Authorization: Bearer $ATOK" -H "X-Tenant-ID: $TID")"

echo "---- 8. 清理本次一次性数据 ----"
for t in customers tenant_users tenant_pack_bindings usage_ledger conversations messages; do
  Q "DELETE FROM $t WHERE tenant_id=$TID" >/dev/null 2>&1
done
Q "DELETE FROM reward_claims WHERE tenant_id=$TID OR ref_id=$TID" >/dev/null 2>&1
Q "DELETE FROM tenants WHERE id=$TID" >/dev/null 2>&1
ck "一次性租户已回收（残留 0）" 0 "$(Q "SELECT count(*) FROM tenants WHERE id=$TID" | tr -d '[:space:]')"

echo ""
echo "=========================================================="
echo " 独立复核总账: PASS=$PASS FAIL=$FAIL"
if [ "$FAIL" -gt 0 ]; then echo " 失败明细："; for f in "${FAILED[@]}"; do echo "   ✗ $f"; done; fi
echo "=========================================================="
[ "$FAIL" = "0" ] && exit 0 || exit 1
