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
#   - G-23(2026-09-25) 新增成员只读子集段：/advisor/kb/my 可读且读到同一份数据、
#     /admin/kb/* 写面仍 403、匿名 401（现 33 项）。
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
# P1-8 契约钉桩(2026-09-20 审计批)：收银台下单/读单走 AdminRequired（只认 super/tenant_admin/admin），
# 前端 AppLayout「收银台」入口集合同步去掉 dept_admin——dept_admin 看不到入口的后端依据即此 403。
R=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/billing/orders" -H "$DH")
check "GET /billing/orders (dept_admin→403，收银台 AdminRequired 口径)" 403 "$R"
# 对照正向：dept_admin 属组织管理岗，/org 面（OrgManageRequired）应放行——收口只针对 AdminRequired 集
R=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/org/departments/tree" -H "$DH")
check "GET /org/departments/tree (dept_admin→200，org 面照常)" 200 "$R"
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

# ---- 4.5 审计批一 P1-2 护栏：策略模板写三路角色闸（2026-09-19）----
# 背景：/strategy/templates POST/PUT/DELETE 此前对全部登录角色开放且保存即 ReloadData——
# 低权限可改写全租户 AI 话术。现收进 AdminRequired 子组；GET 读面全员保持。
echo ""; echo "-- P1-2 策略模板写闸 --"
TPL_ID="perm_abt_$TAG"
TPL_BODY="{\"id\":\"$TPL_ID\",\"anchor_type\":6,\"name\":\"perm护栏模板\",\"prompt_template\":\"t\",\"status\":0}"
R=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/strategy/templates" -H "$SH" -H "X-Tenant-ID: 1" -H "Content-Type: application/json" -d "$TPL_BODY")
check "POST /strategy/templates (sales→403)" 403 "$R"
R=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/strategy/templates" -H "$VH" -H "X-Tenant-ID: 1" -H "Content-Type: application/json" -d "$TPL_BODY")
check "POST /strategy/templates (readonly→403)" 403 "$R"
R=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/strategy/templates" -H "$DH" -H "X-Tenant-ID: 1" -H "Content-Type: application/json" -d "$TPL_BODY")
check "POST /strategy/templates (dept_admin→403)" 403 "$R"
R=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/strategy/templates" -H "$AH" -H "X-Tenant-ID: 1" -H "Content-Type: application/json" -d "$TPL_BODY")
check "POST /strategy/templates (超管带租户头→200)" 200 "$R"
# 撞主键 409（P1-2 附带：原直出 500）
R=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/strategy/templates" -H "$AH" -H "X-Tenant-ID: 1" -H "Content-Type: application/json" -d "$TPL_BODY")
check "POST /strategy/templates (重复ID→409)" 409 "$R"
R=$(curl -s -o /dev/null -w "%{http_code}" -X DELETE "$B/api/v1/strategy/templates/$TPL_ID" -H "$AH" -H "X-Tenant-ID: 1")
check "DELETE /strategy/templates/:id (超管→200 清理)" 200 "$R"
R=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/strategy/templates" -H "$SH" -H "X-Tenant-ID: 1")
check "GET /strategy/templates (sales→200 读面保持)" 200 "$R"

# ---- 4.6 G-23 护栏：移动端成员只读子集（企业知识库）（2026-09-25）----
# 背景：/app 设置页此前对成员整块隐藏知识库（打了 403 才藏），销售在手机上看不到公司资料。
# 现口径：后端在顾问组（仅需登录 + 只读写闸）开 GET /advisor/kb/my 只读子集，
# 上传/删除/注销仍只在 /admin 组。三段各断一次：
#   ① 成员可读且读到的是**同一份数据面**（超管刚写的片段必须出现在成员列表里——
#      只断 200 会得到"开了个永远返回空的新端点"这种假绿）；
#   ② 写面没有被顺手放开（sales 上传/删除仍 403）；
#   ③ 匿名打不开（新端点确实挂在 JWTAuth 之后，不是漏在公开组）。
echo ""; echo "-- G-23 成员知识库只读子集 --"
KBT="perm_g23_$TAG"
UP=$(curl -s -X POST "$B/api/v1/admin/kb/upload" -H "$AH" -H "X-Tenant-ID: 1" -H "Content-Type: application/json" \
  -d "{\"title\":\"$KBT\",\"content\":\"成员只读子集护栏用片段\",\"category\":\"企业知识\"}")
check "POST /admin/kb/upload ($KBT 写入成功)" 0 "$(echo "$UP" | jsonget "['code']")"
CODE=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/advisor/kb/my?page=1&page_size=100" -H "$SH")
check "GET /advisor/kb/my (sales→200)" 200 "$CODE"
ML=$(curl -s "$B/api/v1/advisor/kb/my?page=1&page_size=100" -H "$SH")
SEES=$(echo "$ML" | python3 -c "import sys,json;d=json.load(sys.stdin);print(sum(1 for x in d.get('data',{}).get('list',[]) if '$KBT' in (x.get('title') or '')))" 2>/dev/null)
check "sales 只读列表里看得见超管刚写的片段" 1 "${SEES:-0}"
CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/admin/kb/upload" -H "$SH" -H "X-Tenant-ID: 1" -H "Content-Type: application/json" \
  -d "{\"title\":\"$KBT-w\",\"content\":\"x\",\"category\":\"企业知识\"}")
check "POST /admin/kb/upload (sales→403 写面未放开)" 403 "$CODE"
KID=$(echo "$ML" | python3 -c "import sys,json;d=json.load(sys.stdin);print(next((str(x['id']) for x in d.get('data',{}).get('list',[]) if '$KBT' in (x.get('title') or '')),''))" 2>/dev/null)
CODE=$(curl -s -o /dev/null -w "%{http_code}" -X DELETE "$B/api/v1/admin/kb/my/$KID" -H "$SH")
check "DELETE /admin/kb/my/:id (sales→403)" 403 "$CODE"
CODE=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/advisor/kb/my")
check "GET /advisor/kb/my (匿名→401)" 401 "$CODE"
if [ -n "$KID" ]; then
  CODE=$(curl -s -o /dev/null -w "%{http_code}" -X DELETE "$B/api/v1/admin/kb/my/$KID" -H "$AH" -H "X-Tenant-ID: 1")
  check "DELETE /admin/kb/my/:id (超管清理→200)" 200 "$CODE"
fi

# ---- 6. 清理：停用测试租户角色账号（部门/租户保留供人工核查，对齐 smoke_org 惯例）----
$PSQL "UPDATE tenant_users SET status=0 WHERE username IN ('viewer_$TAG','sales_$TAG','dept_$TAG')" >/dev/null

echo ""; echo "==== G-2 结果: PASS=$PASS FAIL=$FAIL ===="
[ "$FAIL" = "0" ] || exit 1
