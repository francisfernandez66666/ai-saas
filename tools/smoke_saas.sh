#!/bin/bash
# ============================================================
# SaaS 注册漏斗 + 组织管理 E2E 验证
# 覆盖：套餐查询→入驻→企业码登录→组织管理→超管封禁/恢复
# ============================================================
B=http://localhost:9090
PSQL="psql ${TEST_DB_URL:-postgresql://ai_scrm:dev123@localhost/ai_scrm} -tAc"
PASS=0; FAIL=0
check(){ if [ "$2" = "$3" ]; then echo "  PASS  $1 ($3)"; PASS=$((PASS+1)); else echo "  FAIL  $1 期望=$2 实际=$3"; FAIL=$((FAIL+1)); fi }

TAG="e2e$RANDOM"
echo "==== 注册漏斗 E2E @ $B ===="

# 0. 前置：超管登录，临时关邮箱验证+放开注册IP限流（对齐 uat.sh 口径，脚本无法收邮件）
#    修改原因：tenant/signup 邮箱验证批次升级后强制 email_code，脚本契约过期导致入驻 400
AT0=$(curl -s -X POST $B/api/v1/auth/login -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"admin123"}' | python3 -c "import sys,json;print(json.load(sys.stdin)['data']['token'])" 2>/dev/null)
curl -s -o /dev/null -X PUT "$B/api/v1/admin/config" -H "Authorization: Bearer $AT0" -H "Content-Type: application/json" \
  -d '[{"category":"notify","key":"email_verify_enabled","value":"false"},{"category":"billing","key":"register_ip_daily_limit","value":"99"},{"category":"billing","key":"register_ip_min_interval_sec","value":"0"}]'

# 1. 入驻新租户
RESP=$(curl -s -X POST $B/api/v1/tenant/signup -H "Content-Type: application/json" \
  -d "{\"company_name\":\"E2E测试公司\",\"code\":\"e2e-$TAG\",\"username\":\"boss_$TAG\",\"password\":\"boss123456\",\"contact_name\":\"测试老板\",\"admin_email\":\"boss+$TAG@e2e.com\"}")
CODE=$(echo "$RESP" | python3 -c "import sys,json;print(json.load(sys.stdin).get('code'))" 2>/dev/null)
check "租户入驻开通试用" 0 "$CODE"
TENANT_ID=$($PSQL "SELECT id FROM tenants WHERE code='e2e-$TAG'" | tr -d '[:space:]')
echo "  新租户ID: $TENANT_ID (trial)"

# 2. 企业码登录新租户管理员
LT=$(curl -s -X POST $B/api/v1/auth/login -H "Content-Type: application/json" \
  -d "{\"username\":\"boss_$TAG\",\"password\":\"boss123456\",\"tenant_code\":\"e2e-$TAG\"}" \
  | python3 -c "import sys,json;print(json.load(sys.stdin)['data']['token'])" 2>/dev/null)
[ ${#LT} -gt 50 ] && check "企业码登录租户管理员" y y || check "企业码登录" y n

# 3a. 个人版配额：第二个根部门应被拦截（max_departments=1）
ROOT_ID=$(curl -s $B/api/v1/org/departments/tree -H "Authorization: Bearer $LT" \
  | python3 -c "import sys,json;print(json.load(sys.stdin)['data'][0]['id'])")
QUOTA=$(curl -s -o /dev/null -w "%{http_code}" -X POST $B/api/v1/org/departments \
  -H "Authorization: Bearer $LT" -H "Content-Type: application/json" \
  -d '{"name":"超额根部门"}')
check "个人版第二根部门被配额拦截" 403 "$QUOTA"
# 3b. 个人版语义：max_departments=1 根部门已占满 → 子部门同样拦截（升级企业版解锁）
CHILD=$(curl -s -X POST $B/api/v1/org/departments \
  -H "Authorization: Bearer $LT" -H "Content-Type: application/json" \
  -d "{\"name\":\"华东销售组\",\"parent_id\":$ROOT_ID}" | python3 -c "import sys,json;print(json.load(sys.stdin).get('code'))" 2>/dev/null)
check "个人版子部门受配额限制(403)" 403 "$CHILD"
# 3c. 超管显式切到该租户后不受其套餐约束？——不：配额按租户计仍应 403；改用企业版租户验证放开逻辑
# （此处仅验证个人版闭环，企业版放开由 smoke_org 的租户1场景覆盖）

# 4. 错误企业码登录被拒（同名账号防串站）
BAD=$(curl -s -X POST $B/api/v1/auth/login -H "Content-Type: application/json" \
  -d "{\"username\":\"boss_$TAG\",\"password\":\"boss123456\",\"tenant_code\":\"acme\"}" \
  | python3 -c "import sys,json;print(json.load(sys.stdin).get('code'))" 2>/dev/null)
check "错误企业码登录被拒" 40101 "$BAD"

# 5. 超管登录 → 列表含新租户 → 停用后该租户域名访问被拒 → 恢复
AT=$(curl -s -X POST $B/api/v1/auth/login -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"admin123"}' | python3 -c "import sys,json;print(json.load(sys.stdin)['data']['token'])")
FOUND=$(curl -s $B/api/v1/super/tenants -H "Authorization: Bearer $AT" \
  | python3 -c "import sys,json;print(any(t['code']=='e2e-$TAG' for t in json.load(sys.stdin)['data']))")
check "超管列表含新租户" True "$FOUND"

curl -s -o /dev/null -X PUT "$B/api/v1/super/tenants/$TENANT_ID/status" \
  -H "Authorization: Bearer $AT" -H "Content-Type: application/json" -d '{"status":"suspended"}'
sleep 31  # 租户解析缓存 TTL=30s
GUEST=$(curl -s -o /dev/null -w "%{http_code}" -H "Host: e2e-$TAG.example.com" -X POST $B/api/v1/chat/guest)
check "停用租户域名访问被拒(suspended)" 403 "$GUEST"

curl -s -o /dev/null -X PUT "$B/api/v1/super/tenants/$TENANT_ID/status" \
  -H "Authorization: Bearer $AT" -H "Content-Type: application/json" -d '{"status":"active"}'
sleep 31
GUEST2=$(curl -s -o /dev/null -w "%{http_code}" -H "Host: e2e-$TAG.example.com" -X POST $B/api/v1/chat/guest)
check "恢复后域名访问正常" 200 "$GUEST2"

# 6. 恢复现场：邮箱验证/IP限流回默认（与 uat.sh 恢复口径一致）
curl -s -o /dev/null -X PUT "$B/api/v1/admin/config" -H "Authorization: Bearer $AT0" -H "Content-Type: application/json" \
  -d '[{"category":"notify","key":"email_verify_enabled","value":"true"},{"category":"billing","key":"register_ip_daily_limit","value":"3"},{"category":"billing","key":"register_ip_min_interval_sec","value":"60"}]'
echo "  已恢复: 邮箱验证/注册IP限流默认值"

echo "==== 结果: PASS=$PASS FAIL=$FAIL ===="
[ "$FAIL" = "0" ] || exit 1
