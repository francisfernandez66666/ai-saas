#!/bin/bash
# ============================================================
# AI-SCRM 四级组织架构冒烟测试（P2 组织树验收）
# 用法: ./tools/smoke_org.sh [端口]   默认 9090
# 覆盖: 部门创建/配额、dept_admin 子树 fail-closed、readonly 写拦截、
#       角色分配合法性、根部门保护、部门移动与子树路径重写（FIX-8 配套批，HTTP 层）
# ============================================================

PORT="${1:-9090}"
B="http://localhost:${PORT}"
PASS=0; FAIL=0
check() { if [ "$2" = "$3" ]; then echo "  PASS  $1 ($3)"; PASS=$((PASS+1)); else echo "  FAIL  $1 期望=$2 实际=$3"; FAIL=$((FAIL+1)); fi; }
jsonget() { python3 -c "import sys,json;d=json.load(sys.stdin);print(eval('d'+sys.argv[1]))" "$1" 2>/dev/null; }
PSQL="psql ${TEST_DB_URL:-postgresql://ai_scrm:dev123@localhost/ai_scrm} -tAc"

echo "==== 组织架构冒烟测试 @ $B ===="
RUN_TAG="$(date +%H%M%S)$RANDOM"
# 起手复位首登强改密标记（与 smoke/smoke_perm/smoke_pay 同口径）：seed 每次启动都会把出厂
# 弱密码账号重新置 must_change_password=true，MustChangePasswordGuard 随即对除 change-password/
# auth/me 外的全部路径回 403——本脚本以前只靠"排在别人后面跑"蹭到别人清过的标记，
# 单跑（或刚重启过服务）时得到的是一整屏 403，看着像组织模块坏了。
$PSQL "UPDATE tenant_users SET must_change_password=false WHERE username='admin'" >/dev/null 2>&1
# 历史残留回收：上一轮中途失败会留下 烟测部A_*/B_* 部门，且里面还挂着当轮没删掉的
# smoke_da_* 测试账号（本脚本只停用、不删人）。这些行既污染 /org 树的读数，也会让
# "本轮部门计数=4"这类自检在别人的行上成立。回收范围严格锁在本脚本的命名空间内
# （烟测部* 的部门 + smoke_% 的账号），不动真实成员。
$PSQL "UPDATE tenant_users SET department_id=NULL WHERE username LIKE 'smoke\_%' AND department_id IN (SELECT id FROM departments WHERE name LIKE '烟测部%')" >/dev/null 2>&1
$PSQL "DELETE FROM departments WHERE name LIKE '烟测部%' AND NOT EXISTS (SELECT 1 FROM tenant_users u WHERE u.department_id=departments.id AND u.username NOT LIKE 'smoke\_%')" >/dev/null 2>&1

TOKEN=$(curl -s -X POST "$B/api/v1/auth/login" -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"admin123"}' | jsonget "['data']['token']")

echo "---- 一、部门树基础 ----"
TREE=$(curl -s "$B/api/v1/org/departments/tree" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1")
ROOT_ID=$(echo "$TREE" | python3 -c "
import sys,json
d=json.load(sys.stdin)['data']
print(d[0]['id'] if d else '')")
[ -n "$ROOT_ID" ] && check "获取根部门ID(root=$ROOT_ID)" y y || check "获取根部门ID" y n

CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/org/departments" \
  -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1" -H "Content-Type: application/json" \
  -d "{\"name\":\"烟测部A_$RUN_TAG\",\"parent_id\":$ROOT_ID}")
check "超管创建子部门A" 200 "$CODE"

# 按「本轮 RUN_TAG 的全名」精确取 id，不用前缀匹配：历史批次留下的 烟测部A_*/B_* 行只要
# 没清干净，startswith('烟测部A_') 就会捞到别人的部门——后面「排除自己那一个」的清理计数、
# 以及「越权往兄弟部门建用户」用的部门 ID 全都会指错行，报出来的是假红。
DEPT_A=$(curl -s "$B/api/v1/org/departments/tree" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1" | python3 -c "
import sys,json
want='烟测部A_$RUN_TAG'
def find(ns):
    for n in ns:
        if n['name'] == want: return str(n['id'])
        r=find(n.get('children') or [])
        if r: return r
    return ''
d=json.load(sys.stdin).get('data') or []
print(find(d))")
[ -n "$DEPT_A" ] && check "子部门A已入树(id=$DEPT_A)" y y || check "子部门A已入树" y n

CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/org/departments" \
  -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1" -H "Content-Type: application/json" \
  -d "{\"name\":\"烟测部B_$RUN_TAG\",\"parent_id\":$ROOT_ID}")
check "创建兄弟部门B" 200 "$CODE"

echo "---- 二、dept_admin 子树 fail-closed ----"
DA_USER="smoke_da_$(date +%s)"
CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/org/users" \
  -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1" -H "Content-Type: application/json" \
  -d "{\"username\":\"$DA_USER\",\"password\":\"da123456\",\"real_name\":\"烟测部门管理员\",\"role\":\"dept_admin\",\"department_id\":$DEPT_A}")
check "在A部门创建dept_admin" 200 "$CODE"

DA_TOKEN=$(curl -s -X POST "$B/api/v1/auth/login" -H "Content-Type: application/json" \
  -d "{\"username\":\"$DA_USER\",\"password\":\"da123456\"}" | jsonget "['data']['token']")
[ ${#DA_TOKEN} -gt 50 ] && check "dept_admin 登录" y y || check "dept_admin 登录" y n

TOTAL=$(curl -s "$B/api/v1/advisor/customers?page_size=100" -H "Authorization: Bearer $DA_TOKEN" | jsonget "['data']['total']")
check "空子树管理员看不到任何客户(fail-closed)" 0 "${TOTAL:-ERR}"

echo "---- 三、角色分配合法性 ----"
RO_B=$(psql ${TEST_DB_URL:-postgresql://ai_scrm:dev123@localhost/ai_scrm} -tAc \
  "SELECT id FROM departments WHERE name='烟测部B_$RUN_TAG'" 2>/dev/null | tr -d '[:space:]')
[ -n "$RO_B" ] && check "本轮B部门ID可直查(id=$RO_B)" y y || check "本轮B部门ID可直查" y n
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
  -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1" -H "Content-Type: application/json" \
  -d "{\"username\":\"$RO_USER\",\"password\":\"ro123456\",\"role\":\"readonly\",\"department_id\":$ROOT_ID}"
RO_TOKEN=$(curl -s -X POST "$B/api/v1/auth/login" -H "Content-Type: application/json" \
  -d "{\"username\":\"$RO_USER\",\"password\":\"ro123456\"}" | jsonget "['data']['token']")
CID=$(psql ${TEST_DB_URL:-postgresql://ai_scrm:dev123@localhost/ai_scrm} -tAc \
  "SELECT id FROM customers WHERE tenant_id=1 AND assigned_user_id>0 ORDER BY id DESC LIMIT 1" 2>/dev/null | tr -d '[:space:]')
CODE=$(curl -s -o /dev/null -w "%{http_code}" -X PUT "$B/api/v1/advisor/customer/$CID/tags" \
  -H "Authorization: Bearer $RO_TOKEN" -H "Content-Type: application/json" \
  -d '{"tags":["测试"]}')
check "readonly 写操作被拦截(403)" 403 "$CODE"
CODE=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/advisor/customer/$CID" \
  -H "Authorization: Bearer $RO_TOKEN")
check "readonly 挂根可只读查看" 200 "$CODE"

echo "---- 五、部门移动与子树重写（FIX-8 配套，2026-09-29 审计批）----"
# 为什么这一段必须在 HTTP 层跑而不是只留单测：`CONCAT(?, …)` 的未定型参数在 PG 扩展协议下
# 报 42P18，于是这条移动链路**自写入起一直 500**、前端 /org 的拖拽入口从没真的成功过，
# 而单测走的是简化句柄、黄金问答集与其余九套冒烟都不碰这条腿——只有真接口打一发才看得见。
# 断言口径：路径与深度都按**库里的实际取值链**逐字比（不是"看起来变小了"），
# 且先自检四行真在库里（缺这步整段会在"零行 vs 零行"上空转）。
DEPT_C=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/org/departments" \
  -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1" -H "Content-Type: application/json" \
  -d "{\"name\":\"烟测部C_$RUN_TAG\",\"parent_id\":$DEPT_A}")
check "在A下建子部门C" 200 "$DEPT_C"
DBU=${TEST_DB_URL:-postgresql://ai_scrm:dev123@localhost/ai_scrm}
DEPT_D=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/org/departments" \
  -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1" -H "Content-Type: application/json" \
  -d "{\"name\":\"烟测部D_$RUN_TAG\",\"parent_id\":$RO_B}")
check "在B下建子部门D（给子树重写留一条后代）" 200 "$DEPT_D"

ROWCNT=$(psql $DBU -tAc "SELECT count(*) FROM departments WHERE name IN ('烟测部A_$RUN_TAG','烟测部B_$RUN_TAG','烟测部C_$RUN_TAG','烟测部D_$RUN_TAG')" 2>/dev/null | tr -d '[:space:]')
check "前置自检：本轮四个烟测部门真在库里（A/B/C/D）" 4 "${ROWCNT:-0}"
# 移动段的 ID 一律按「本轮全名」直查，不复用前面 HTTP 反查的结果（同一轮内也重取一次，
# 免得某处变量被上一段覆盖后，下面的路径/深度等式打在别人的行上）
RO_B=$(psql $DBU -tAc "SELECT id FROM departments WHERE name='烟测部B_$RUN_TAG'" 2>/dev/null | tr -d '[:space:]')
DEPT_C_ID=$(psql $DBU -tAc "SELECT id FROM departments WHERE name='烟测部C_$RUN_TAG'" 2>/dev/null | tr -d '[:space:]')
DEPT_D_ID=$(psql $DBU -tAc "SELECT id FROM departments WHERE name='烟测部D_$RUN_TAG'" 2>/dev/null | tr -d '[:space:]')
A_PATH=$(psql $DBU -tAc "SELECT path FROM departments WHERE id=$DEPT_A" 2>/dev/null | tr -d '[:space:]')
B_ORIG_DEPTH=$(psql $DBU -tAc "SELECT depth FROM departments WHERE id=$RO_B" 2>/dev/null | tr -d '[:space:]'); B_ORIG_DEPTH=${B_ORIG_DEPTH:-0}
# 前提自检必须在**移动前**读：A 与 B 都是挂在根下的兄弟部门，所以深度相等、B 的父是根。
# 缺这两条，"移动后 B 深度 = A 深度 + 1"会在上一轮把根链改歪之后等式两边一起漂移仍判绿；
# 而 A 自己的深度也在这里先存一份（A_DEPTH0），因为下面拿 A_DEPTH 比对时 A 已被路过一次
# 子树重写——若实现把父部门自己的深度也顺带平移（正是今天抓到的缺陷形态），只有这份移动前
# 的读数能把它抓住。
A_DEPTH0=$(psql $DBU -tAc "SELECT depth FROM departments WHERE id=$DEPT_A" 2>/dev/null | tr -d '[:space:]'); A_DEPTH0=${A_DEPTH0:-0}
SAME_LEVEL=$(psql $DBU -tAc "SELECT CASE WHEN $A_DEPTH0=$B_ORIG_DEPTH THEN 'y' ELSE 'n' END" 2>/dev/null | tr -d '[:space:]')
check "前提自检：移动前 B 与 A 同层（深度相等 $A_DEPTH0=$B_ORIG_DEPTH）" y "${SAME_LEVEL:-ERR}"
B_PARENT0=$(psql $DBU -tAc "SELECT parent_id FROM departments WHERE id=$RO_B" 2>/dev/null | tr -d '[:space:]')
check "前提自检：移动前 B 挂在根下（否则移进 A 是空操作，等式在原地漂移）" "$ROOT_ID" "${B_PARENT0:-ERR}"
D_DEPTH0=$(psql $DBU -tAc "SELECT depth FROM departments WHERE id=$DEPT_D_ID" 2>/dev/null | tr -d '[:space:]'); D_DEPTH0=${D_DEPTH0:-0}

CODE=$(curl -s -o /tmp/org_move.$$ -w "%{http_code}" -X PUT "$B/api/v1/org/departments/$RO_B" \
  -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1" -H "Content-Type: application/json" \
  -d "{\"new_parent_id\":$DEPT_A}")
check "把B移到A下→200（42P18 现场：这条腿此前恒 500）" 200 "$CODE"
[ "$CODE" = "200" ] || echo "     响应体: $(head -c 200 /tmp/org_move.$$)"
rm -f /tmp/org_move.$$

B_PATH=$(psql $DBU -tAc "SELECT path FROM departments WHERE id=$RO_B" 2>/dev/null | tr -d '[:space:]')
B_DEPTH=$(psql $DBU -tAc "SELECT depth FROM departments WHERE id=$RO_B" 2>/dev/null | tr -d '[:space:]'); B_DEPTH=${B_DEPTH:-0}
A_DEPTH=$(psql $DBU -tAc "SELECT depth FROM departments WHERE id=$DEPT_A" 2>/dev/null | tr -d '[:space:]'); A_DEPTH=${A_DEPTH:-0}
check "B 的新路径逐字等于「A路径+B ID/」（不是包含关系）" "$A_PATH$RO_B/" "$B_PATH"
check "B 的深度=A深度+1" "$((A_DEPTH+1))" "${B_DEPTH:-0}"
# 父部门自己不该被子树重写波及：批量 UPDATE 的前缀 LIKE 会把被移动节点自己也算进去，
# 于是 depth 在显式值之上又被加一次（今天实测 B 从 2 变 4 的现场）。这条锁把"只平移后代"钉死。
check "A 的深度不被自己孩子的移动波及" "$A_DEPTH0" "$A_DEPTH"
check "移动把 B 的深度净增 1" "$B_ORIG_DEPTH" "$((B_DEPTH-1))"
D_PATH=$(psql $DBU -tAc "SELECT path FROM departments WHERE id=$DEPT_D_ID" 2>/dev/null | tr -d '[:space:]')
D_DEPTH=$(psql $DBU -tAc "SELECT depth FROM departments WHERE id=$DEPT_D_ID" 2>/dev/null | tr -d '[:space:]'); D_DEPTH=${D_DEPTH:-0}
check "后代D 的路径随子树整体搬走（只改父不重写=这条红）" "$B_PATH$DEPT_D_ID/" "$D_PATH"
check "后代D 的深度也跟着 +1" "$((D_DEPTH0+1))" "$D_DEPTH"
C_PATH=$(psql $DBU -tAc "SELECT path FROM departments WHERE id=$DEPT_C_ID" 2>/dev/null | tr -d '[:space:]')
check "A 的另一个孩子C 不被误伤（前缀匹配不能把兄弟一起重写）" "${A_PATH}${DEPT_C_ID}/" "$C_PATH"
C_DEPTH=$(psql $DBU -tAc "SELECT depth FROM departments WHERE id=$DEPT_C_ID" 2>/dev/null | tr -d '[:space:]'); C_DEPTH=${C_DEPTH:-0}
check "A 的孩子C 的深度同样不被波及（深度和路径一起不动才算没误伤）" "$((A_DEPTH0+1))" "$C_DEPTH"

CODE=$(curl -s -o /dev/null -w "%{http_code}" -X PUT "$B/api/v1/org/departments/$DEPT_A" \
  -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1" -H "Content-Type: application/json" \
  -d "{\"new_parent_id\":$DEPT_D_ID}")
check "把A移到自己的后代D下→400（防环校验）" 400 "$CODE"
CODE=$(curl -s -o /dev/null -w "%{http_code}" -X PUT "$B/api/v1/org/departments/$DEPT_C_ID" \
  -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1" -H "Content-Type: application/json" \
  -d '{"new_parent_id":999999999}')
check "移到不存在的父→404（前置校验那腿）" 404 "$CODE"

# 按本轮 tag 清部门：叶子先删，否则 DELETE 的"仅空部门"会挡回来。
# ⚠ A 里挂着第二段的 dept_admin 测试账号（本脚本只停用、不删人），所以 A 必须在
#    第六段（人都清完）之后才删得掉——这条判据按下不报，改到脚本末尾统一收口。
for ID in $DEPT_C_ID $DEPT_D_ID $RO_B; do
  curl -s -o /dev/null -X DELETE "$B/api/v1/org/departments/$ID" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1"
done
LEFT=$(psql $DBU -tAc "SELECT count(*) FROM departments WHERE name IN ('烟测部B_$RUN_TAG','烟测部C_$RUN_TAG','烟测部D_$RUN_TAG')" 2>/dev/null | tr -d '[:space:]')
check "C/D/B 三个部门已删（A 因挂着测试账号留到末尾）" 0 "${LEFT:-ERR}"

echo "---- 六、清理烟测账号 ----"
for U in "$DA_USER" "$RO_USER"; do
  UID_=$(psql ${TEST_DB_URL:-postgresql://ai_scrm:dev123@localhost/ai_scrm} -tAc \
    "SELECT id FROM tenant_users WHERE username='$U'" 2>/dev/null | tr -d '[:space:]')
  [ -n "$UID_" ] && curl -s -o /dev/null -X PUT "$B/api/v1/org/users/$UID_" \
    -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1" -H "Content-Type: application/json" -d '{"status":0}'
done
echo "  （账号已停用）"
# 部门本体此刻才删得掉：A 里挂着本轮的 dept_admin（接口只停用、不删人，而 DELETE 部门
# 的"仅空部门"约束会一直挡着）。这里按**本轮自己建的两个用户名**摘掉部门归属再删——
# 命名空间是本轮独有的，不会碰别人的成员。
$PSQL "UPDATE tenant_users SET department_id=NULL WHERE username IN ('$DA_USER','$RO_USER')" >/dev/null 2>&1
DELA=$(curl -s -o /dev/null -w "%{http_code}" -X DELETE "$B/api/v1/org/departments/${DEPT_A:-0}" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1")
check "A 部门在成员摘掉后可删（200 而非 409）" 200 "${DELA:-ERR}"
LEFT2=$(psql $DBU -tAc "SELECT count(*) FROM departments WHERE name IN ('烟测部A_$RUN_TAG','烟测部B_$RUN_TAG','烟测部C_$RUN_TAG','烟测部D_$RUN_TAG')" 2>/dev/null | tr -d '[:space:]')
check "本轮烟测部门全部清零（不留历史部门给下一轮的自检）" 0 "${LEFT2:-ERR}"

echo "==== 结果: PASS=$PASS FAIL=$FAIL ===="
[ "$FAIL" = "0" ] || exit 1
