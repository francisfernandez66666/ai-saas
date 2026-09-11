#!/bin/bash
# ============================================================
# G-2 角色权限矩阵负向断言
# 覆盖：sales×admin组→403 / readonly×写→403 / tenant_admin×super→403
# 用法: ./tools/smoke_perm.sh 9090
# ============================================================
B="http://localhost:${1:-9090}"
PASS=0; FAIL=0
check(){ if [ "$2" = "$3" ]; then echo "  PASS  $1 ($3)"; PASS=$((PASS+1)); else echo "  FAIL  $1 期望=$2 实际=$3"; FAIL=$((FAIL+1)); fi }

jsonget(){ python3 -c "import sys,json;d=json.load(sys.stdin);print(eval('d'+sys.argv[1]))" "$1" 2>/dev/null; }

echo "== G-2 角色权限矩阵负向断言 @ $B =="

# ---- 0. 创建测试租户 + 测试用户 ----
AT=$(curl -s -X POST "$B/api/v1/auth/login" -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"admin123"}' | jsonget "['data']['token']")
[ -n "$AT" ] && check "超管登录" y y || { check "超管登录" y n; echo "无法继续"; exit 1; }

# 创建租户
TAG="perm$RANDOM"
RS=$(curl -s -X POST "$B/api/v1/tenant/signup" -H "Content-Type: application/json" \
  -d "{\"company_name\":\"权限测试\",\"code\":\"$TAG\",\"username\":\"boss_$TAG\",\"password\":\"boss123456\",\"contact_name\":\"测试\",\"admin_email\":\"$TAG@test.com\"}")
TID=$(echo "$RS" | jsonget "['data']['tenant_id']")
echo "  测试租户ID: $TID"

# 超管登录新租户
LT=$(curl -s -X POST "$B/api/v1/auth/login" -H "Content-Type: application/json" \
  -d "{\"username\":\"boss_$TAG\",\"password\":\"boss123456\"}" | jsonget "['data']['token']")
LH="Authorization: Bearer $LT"

# 获取部门ID
DEPT_ID=$(curl -s "$B/api/v1/org/departments/tree" -H "$LH" | python3 -c "
import sys,json
d=json.load(sys.stdin)
def find_id(nodes):
    for n in nodes:
        if n.get('id'): return n['id']
        r=find_id(n.get('children',[]))
        if r: return r
    return 1
print(find_id(d.get('data',[])) or 1)" 2>/dev/null || echo "1")

# 创建 readonly 用户
curl -s -o /dev/null -X POST "$B/api/v1/org/users" -H "$LH" -H "Content-Type: application/json" \
  -d "{\"username\":\"viewer_$TAG\",\"password\":\"View1234!\",\"real_name\":\"只读\",\"role\":\"readonly\",\"department_id\":$DEPT_ID}"
VT=$(curl -s -X POST "$B/api/v1/auth/login" -H "Content-Type: application/json" \
  -d "{\"username\":\"viewer_$TAG\",\"password\":\"View1234!\"}" | jsonget "['data']['token']")
VH="Authorization: Bearer $VT"

# 创建 user(sales) 用户
curl -s -o /dev/null -X POST "$B/api/v1/org/users" -H "$LH" -H "Content-Type: application/json" \
  -d "{\"username\":\"sales_$TAG\",\"password\":\"Sale1234!\",\"real_name\":\"销售\",\"role\":\"user\",\"department_id\":$DEPT_ID}"
ST=$(curl -s -X POST "$B/api/v1/auth/login" -H "Content-Type: application/json" \
  -d "{\"username\":\"sales_$TAG\",\"password\":\"Sale1234!\"}" | jsonget "['data']['token']")
SH="Authorization: Bearer $ST"

# 创建 dept_admin 用户
curl -s -o /dev/null -X POST "$B/api/v1/org/users" -H "$LH" -H "Content-Type: application/json" \
  -d "{\"username\":\"dept_$TAG\",\"password\":\"Dept1234!\",\"real_name\":\"部门管\",\"role\":\"dept_admin\",\"department_id\":$DEPT_ID}"
DT=$(curl -s -X POST "$B/api/v1/auth/login" -H "Content-Type: application/json" \
  -d "{\"username\":\"dept_$TAG\",\"password\":\"Dept1234!\"}" | jsonget "['data']['token']")
DH="Authorization: Bearer $DT"

# ---- 1. sales(user) × admin 端点 → 403 ----
echo ""
echo "-- sales(user) × admin 端点 --"
R=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/admin/config" -H "$SH")
check "GET /admin/config (sales→403)" 403 "$R"
R=$(curl -s -o /dev/null -w "%{http_code}" -X PUT "$B/api/v1/admin/config" -H "$SH" -H "Content-Type: application/json" \
  -d '[{"category":"reply_speed","key":"enabled","value":"true"}]')
check "PUT /admin/config (sales→403)" 403 "$R"

# ---- 2. sales(user) × super 端点 → 403 ----
echo ""
echo "-- sales(user) × super 端点 --"
R=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/super/tenants" -H "$SH")
check "GET /super/tenants (sales→403)" 403 "$R"

# ---- 3. readonly × 写操作 → 403 ----
echo ""
echo "-- readonly × 写操作 --"
R=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/org/users" -H "$VH" -H "Content-Type: application/json" \
  -d "{\"username\":\"x_$TAG\",\"password\":\"X1234567!\",\"role\":\"user\",\"department_id\":$DEPT_ID}")
check "POST /org/users (readonly→403)" 403 "$R"
R=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/org/departments" -H "$VH" -H "Content-Type: application/json" \
  -d "{\"name\":\"非法部门\",\"parent_id\":1}")
check "POST /org/departments (readonly→403)" 403 "$R"
R=$(curl -s -o /dev/null -w "%{http_code}" -X PUT "$B/api/v1/admin/config" -H "$VH" -H "Content-Type: application/json" \
  -d '[{"category":"reply_speed","key":"enabled","value":"true"}]')
check "PUT /admin/config (readonly→403)" 403 "$R"

# ---- 4. readonly × 读操作 → 200 ----
echo ""
echo "-- readonly × 读操作 --"
R=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/admin/config" -H "$VH")
check "GET /admin/config (readonly→200)" 200 "$R"
R=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/org/users" -H "$VH")
check "GET /org/users (readonly→200)" 200 "$R"

# ---- 5. dept_admin × super 端点 → 403 ----
echo ""
echo "-- dept_admin × super 端点 --"
R=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/super/tenants" -H "$DH")
check "GET /super/tenants (dept_admin→403)" 403 "$R"
R=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/admin/config" -H "$DH")
check "GET /admin/config (dept_admin→403)" 403 "$R"

# ---- 6. user × org 管理端点 → 403 ----
echo ""
echo "-- user × org 管理端点 --"
R=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/org/users" -H "$SH" -H "Content-Type: application/json" \
  -d "{\"username\":\"x2_$TAG\",\"password\":\"X1234567!\",\"role\":\"user\",\"department_id\":$DEPT_ID}")
check "POST /org/users (user→403)" 403 "$R"

# ---- 7. 跨租户场景：super_admin 无 X-Tenant-ID → 400 ----
echo ""
echo "-- super_admin 跨租户 --"
R=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/super/tenants" -H "Authorization: Bearer $AT")
check "GET /super/tenants (无X-Tenant-ID→400)" 400 "$R"

echo ""
echo "==== G-2 结果: PASS=$PASS FAIL=$FAIL ===="
[ "$FAIL" = "0" ] || exit 1
