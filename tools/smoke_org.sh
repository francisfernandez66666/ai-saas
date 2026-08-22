#!/bin/bash
# ============================================================
# AI-SCRM 四级组织架构冒烟测试（P2 组织树验收）
# 用法: ./tools/smoke_org.sh [端口]   默认 9090
# 覆盖: 部门创建/配额、dept_admin 子树 fail-closed、readonly 写拦截、
#       角色分配合法性、根部门保护
# ============================================================

PORT="${1:-9090}"
B="http://localhost:${PORT}"
PASS=0; FAIL=0
check() { if [ "$2" = "$3" ]; then echo "  PASS  $1 ($3)"; PASS=$((PASS+1)); else echo "  FAIL  $1 期望=$2 实际=$3"; FAIL=$((FAIL+1)); fi; }
jsonget() { python3 -c "import sys,json;d=json.load(sys.stdin);print(eval('d'+sys.argv[1]))" "$1" 2>/dev/null; }
PSQL="psql postgresql://ai_scrm:dev123@localhost/ai_scrm -tAc"

echo "==== 组织架构冒烟测试 @ $B ===="
RUN_TAG="$(date +%H%M%S)$RANDOM"

TOKEN=$(curl -s -X POST "$B/api/v1/auth/login" -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"admin123"}' | jsonget "['data']['token']")

echo "---- 一、部门树基础 ----"
TREE=$(curl -s "$B/api/v1/org/departments/tree" -H "Authorization: Bearer $TOKEN")
ROOT_ID=$(echo "$TREE" | python3 -c "
import sys,json
d=json.load(sys.stdin)['data']
print(d[0]['id'] if d else '')")
[ -n "$ROOT_ID" ] && check "获取根部门ID(root=$ROOT_ID)" y y || check "获取根部门ID" y n

CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/org/departments" \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d "{\"name\":\"烟测部A_$RUN_TAG\",\"parent_id\":$ROOT_ID}")
check "超管创建子部门A" 200 "$CODE"

DEPT_A=$(curl -s "$B/api/v1/org/departments/tree" -H "Authorization: Bearer $TOKEN" | python3 -c "
import sys,json
def find(ns):
    for n in ns:
        if n['name'].startswith('烟测部A_'): return str(n['id'])
        r=find(n.get('children') or [])
        if r: return r
    return ''
d=json.load(sys.stdin).get('data') or []
print(find(d))")
[ -n "$DEPT_A" ] && check "子部门A已入树(id=$DEPT_A)" y y || check "子部门A已入树" y n

CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/org/departments" \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d "{\"name\":\"烟测部B_$RUN_TAG\",\"parent_id\":$ROOT_ID}")
check "创建兄弟部门B" 200 "$CODE"

echo "---- 二、dept_admin 子树 fail-closed ----"
DA_USER="smoke_da_$(date +%s)"
CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/org/users" \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d "{\"username\":\"$DA_USER\",\"password\":\"da123456\",\"real_name\":\"烟测部门管理员\",\"role\":\"dept_admin\",\"department_id\":$DEPT_A}")
check "在A部门创建dept_admin" 200 "$CODE"

DA_TOKEN=$(curl -s -X POST "$B/api/v1/auth/login" -H "Content-Type: application/json" \
  -d "{\"username\":\"$DA_USER\",\"password\":\"da123456\"}" | jsonget "['data']['token']")
[ ${#DA_TOKEN} -gt 50 ] && check "dept_admin 登录" y y || check "dept_admin 登录" y n

TOTAL=$(curl -s "$B/api/v1/advisor/customers?page_size=100" -H "Authorization: Bearer $DA_TOKEN" | jsonget "['data']['total']")
check "空子树管理员看不到任何客户(fail-closed)" 0 "${TOTAL:-ERR}"

echo "---- 三、角色分配合法性 ----"
RO_B=$(psql postgresql://ai_scrm:dev123@localhost/ai_scrm -tAc \
  "SELECT id FROM departments WHERE name LIKE '烟测部B_%' ORDER BY id DESC LIMIT 1" 2>/dev/null | tr -d '[:space:]')
CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/org/users" \
  -H "Authorization: Bearer $DA_TOKEN" -H "Content-Type: application/json" \
  -d "{\"username\":\"smoke_x1_${RANDOM}\",\"password\":\"x12345678\",\"role\":\"user\",\"department_id\":${RO_B:-0}}")
check "dept_admin 不能越权往管辖外建用户" 403 "$CODE"

CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/org/departments" \
  -H "Authorization: Bearer $DA_TOKEN" -H "Content-Type: application/json" -d '{"name":"根部门尝试"}')
check "dept_admin 不能创建根部门" 403 "$CODE"

echo "---- 四、readonly 写拦截 ----"
RO_USER="smoke_ro_$(date +%s)"
curl -s -o /dev/null -X POST "$B/api/v1/org/users" \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d "{\"username\":\"$RO_USER\",\"password\":\"ro123456\",\"role\":\"readonly\",\"department_id\":$ROOT_ID}"
RO_TOKEN=$(curl -s -X POST "$B/api/v1/auth/login" -H "Content-Type: application/json" \
  -d "{\"username\":\"$RO_USER\",\"password\":\"ro123456\"}" | jsonget "['data']['token']")
CID=$(psql postgresql://ai_scrm:dev123@localhost/ai_scrm -tAc \
  "SELECT id FROM customers WHERE tenant_id=1 AND assigned_user_id>0 ORDER BY id DESC LIMIT 1" 2>/dev/null | tr -d '[:space:]')
CODE=$(curl -s -o /dev/null -w "%{http_code}" -X PUT "$B/api/v1/advisor/customer/$CID/tags" \
  -H "Authorization: Bearer $RO_TOKEN" -H "Content-Type: application/json" \
  -d '{"tags":["测试"]}')
check "readonly 写操作被拦截(403)" 403 "$CODE"
CODE=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/advisor/customer/$CID" \
  -H "Authorization: Bearer $RO_TOKEN")
check "readonly 挂根可只读查看" 200 "$CODE"

echo "---- 五、清理烟测账号 ----"
for U in "$DA_USER" "$RO_USER"; do
  UID_=$(psql postgresql://ai_scrm:dev123@localhost/ai_scrm -tAc \
    "SELECT id FROM tenant_users WHERE username='$U'" 2>/dev/null | tr -d '[:space:]')
  [ -n "$UID_" ] && curl -s -o /dev/null -X PUT "$B/api/v1/org/users/$UID_" \
    -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" -d '{"status":0}'
done
echo "  （账号已停用，部门保留供人工核查）"

echo "==== 结果: PASS=$PASS FAIL=$FAIL ===="
[ "$FAIL" = "0" ] || exit 1
