#!/bin/bash
# ============================================================
# G-2 角色权限矩阵负向断言（T1 重写 2026-09-12，对齐 2026-09-11 后新契约）
# 覆盖：sales×admin→403 / readonly×写→403 / dept_admin×super→403 /
#       P2-15 平台路径白名单（/super/* 无头=200）与租户作用域路径（无头=400）
# 契约同步说明：
#   - T1 修订：建号改用默认租户 1（试用租户有用户数配额，且入驻链路归 smoke_saas 管），
#     不再动邮箱验证等全局开关；setup 仅清 admin 首登强改密标记（Q3，seed 重启会重标）。
#   - 旧断言"super/tenants 无头→400"是 P2-15 之前契约，现行白名单为 200（本次改断言）。
#   - 新增租户作用域路径（/admin/apikeys）无头→400、带 X-Tenant-ID→200 的正反断言。
# 用法: bash tools/smoke_perm.sh 9090
# ============================================================
B="http://localhost:${1:-9090}"
PSQL="psql ${TEST_DB_URL:-postgresql://ai_scrm:dev123@localhost/ai_scrm} -tAc"
PASS=0; FAIL=0
check(){ if [ "$2" = "$3" ]; then echo "  PASS  $1 ($3)"; PASS=$((PASS+1)); else echo "  FAIL  $1 期望=$2 实际=$3"; FAIL=$((FAIL+1)); fi }
jsonget(){ python3 -c "import sys,json;d=json.load(sys.stdin);print(eval('d'+sys.argv[1]))" "$1" 2>/dev/null; }

echo "== G-2 角色权限矩阵负向断言 @ $B =="

# ---- 0. 超管登录 + Q3: 清首登强改密标记（seed 重启会把出厂弱密码账号重标 true）----
$PSQL "UPDATE tenant_users SET must_change_password=false WHERE username='admin'" >/dev/null 2>&1
AT=$(curl -s -X POST "$B/api/v1/auth/login" -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"admin123"}' | jsonget "['data']['token']")
[ -n "$AT" ] && check "超管登录" y y || { check "超管登录" y n; echo "无法继续"; exit 1; }
AH="Authorization: Bearer $AT"

# ---- 1. 在默认租户(tenant 1)建三种角色账号 ----
# T1 修订(2026-09-12)：不再新租户入驻建号——试用/个人版有用户数配额（"用户数已达套餐上限"403），
# 且本脚本职责是权限矩阵而非入驻链路（后者由 smoke_saas 覆盖）。统一挂 tenant 1 根部门。
DEPT_ID=1

mkuser(){ # mkuser <用户名> <密码> <角色>
  # P2-15：admin 为 super_admin 平台身份，写租户作用域路径必须显式 X-Tenant-ID
  curl -s -o /dev/null -X POST "$B/api/v1/org/users" -H "$AH" -H "X-Tenant-ID: 1" -H "Content-Type: application/json" \
    -d "{\"username\":\"$1\",\"password\":\"$2\",\"real_name\":\"$3测试\",\"role\":\"$3\",\"department_id\":$DEPT_ID}"
  curl -s -X POST "$B/api/v1/auth/login" -H "Content-Type: application/json" \
    -d "{\"username\":\"$1\",\"password\":\"$2\"}" | jsonget "['data']['token']"
}
TAG="perm$RANDOM"
VT=$(mkuser "viewer_$TAG" "View1234!" readonly)
ST=$(mkuser "sales_$TAG" "Sale1234!" user)
DT=$(mkuser "dept_$TAG" "Dept1234!" dept_admin)
[ -n "$VT" ] && [ -n "$ST" ] && [ -n "$DT" ] && R=y || R=n
check "三角色账号建立并登录" y "$R"
VH="Authorization: Bearer $VT"; SH="Authorization: Bearer $ST"; DH="Authorization: Bearer $DT"
TID=1

# ---- 2. sales(user) × admin/super 端点 → 403 ----
echo ""; echo "-- sales(user) 越权 --"
R=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/admin/config" -H "$SH")
check "GET /admin/config (sales→403)" 403 "$R"
R=$(curl -s -o /dev/null -w "%{http_code}" -X PUT "$B/api/v1/admin/config" -H "$SH" -H "Content-Type: application/json" \
  -d '[{"category":"reply_speed","key":"enabled","value":"true"}]')
check "PUT /admin/config (sales→403)" 403 "$R"
R=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/super/tenants" -H "$SH")
check "GET /super/tenants (sales→403)" 403 "$R"

# ---- 3. readonly × 管理面 → 403（读写皆拦）；× 顾问侧只读 → 200 ----
# T1 契约更新(2026-09-12)：现行守卫 /admin/*=AdminRequired、/org/*=OrgManageRequired，
# readonly 连 GET 也 403；其只读域在 advisor 侧（smoke_org 已断言 200）。旧"readonly GET→200"过期。
echo ""; echo "-- readonly 管理面全拦 + 顾问侧可读 --"
R=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/org/users" -H "$VH" -H "Content-Type: application/json" \
  -d "{\"username\":\"x_$TAG\",\"password\":\"X1234567!\",\"role\":\"user\",\"department_id\":$DEPT_ID}")
check "POST /org/users (readonly→403)" 403 "$R"
R=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/org/departments" -H "$VH" -H "Content-Type: application/json" \
  -d "{\"name\":\"非法部门\",\"parent_id\":$DEPT_ID}")
check "POST /org/departments (readonly→403)" 403 "$R"
R=$(curl -s -o /dev/null -w "%{http_code}" -X PUT "$B/api/v1/admin/config" -H "$VH" -H "Content-Type: application/json" \
  -d '[{"category":"reply_speed","key":"enabled","value":"true"}]')
check "PUT /admin/config (readonly→403)" 403 "$R"
R=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/admin/config" -H "$VH")
check "GET /admin/config (readonly→403)" 403 "$R"
R=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/org/users" -H "$VH")
check "GET /org/users (readonly→403)" 403 "$R"
R=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/advisor/customers?page=1&page_size=1" -H "$VH")
check "GET /advisor/customers (readonly→200 只读域)" 200 "$R"

# ---- 4. dept_admin × super/admin → 403；user × org 管理 → 403 ----
echo ""; echo "-- dept_admin / user 越权 --"
R=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/super/tenants" -H "$DH")
check "GET /super/tenants (dept_admin→403)" 403 "$R"
R=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/admin/config" -H "$DH")
check "GET /admin/config (dept_admin→403)" 403 "$R"
R=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/org/users" -H "$SH" -H "Content-Type: application/json" \
  -d "{\"username\":\"x2_$TAG\",\"password\":\"X1234567!\",\"role\":\"user\",\"department_id\":$DEPT_ID}")
check "POST /org/users (user→403)" 403 "$R"

# ---- 5. P2-15 新契约：平台路径白名单 vs 租户作用域路径强制头 ----
echo ""; echo "-- P2-15 super_admin 路径白名单（T1 契约更新）--"
R=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/super/tenants" -H "$AH")
check "GET /super/tenants (平台路径无头→200)" 200 "$R"
R=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/admin/apikeys" -H "$AH")
check "GET /admin/apikeys (租户作用域无头→400)" 400 "$R"
R=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/admin/apikeys" -H "$AH" -H "X-Tenant-ID: $TID")
check "GET /admin/apikeys (带X-Tenant-ID→200)" 200 "$R"

# ---- 6. 清理：停用测试租户角色账号（部门/租户保留供人工核查，对齐 smoke_org 惯例）----
$PSQL "UPDATE tenant_users SET status=0 WHERE username IN ('viewer_$TAG','sales_$TAG','dept_$TAG')" >/dev/null

echo ""; echo "==== G-2 结果: PASS=$PASS FAIL=$FAIL ===="
[ "$FAIL" = "0" ] || exit 1
