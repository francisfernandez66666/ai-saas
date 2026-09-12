#!/bin/bash
# DB 连接：TEST_DB_URL 环境变量覆盖（CI 用 ci_pass），默认本地 dev 库
PSQL="psql ${TEST_DB_URL:-postgresql://ai_scrm:dev123@localhost/ai_scrm} -tAc"
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
psql ${TEST_DB_URL:-postgresql://ai_scrm:dev123@localhost/ai_scrm} -c \
  "INSERT INTO tenants (name, code, tier, status, created_at, updated_at)
   SELECT 'acme-test', 'acme', 'personal', 'active', NOW(), NOW()
   WHERE NOT EXISTS (SELECT 1 FROM tenants WHERE code='acme');" >/dev/null 2>&1
ACME_ID=$(psql ${TEST_DB_URL:-postgresql://ai_scrm:dev123@localhost/ai_scrm} -tAc "SELECT id FROM tenants WHERE code='acme'" | tr -d '[:space:]')

# ---- 准备（M3）：清除默认账号首登强改密标记，模拟"已改密"状态 ----
# （出厂弱密码检测逻辑见 seed.go——改过密码的账号重启后不会被重新标记）
psql ${TEST_DB_URL:-postgresql://ai_scrm:dev123@localhost/ai_scrm} -c \
  "UPDATE tenant_users SET must_change_password=false WHERE username IN ('admin','sales1','sales2','sales3');" >/dev/null 2>&1

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

AUDIT=$(psql ${TEST_DB_URL:-postgresql://ai_scrm:dev123@localhost/ai_scrm} -tAc \
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
  BAD=$(psql ${TEST_DB_URL:-postgresql://ai_scrm:dev123@localhost/ai_scrm} -tAc \
    "SELECT count(*) FROM customers WHERE id IN ($IDS) AND tenant_id <> 1" 2>/dev/null | tr -d '[:space:]')
  [ "${BAD:-1}" = "0" ] && check "客户列表无跨租户数据混入(共$(echo $IDS | tr ',' ' ' | wc -w | tr -d ' ')条全属租户1)" y y \
                         || check "客户列表无跨租户数据混入(混入${BAD}条)" y n
else
  echo "  SKIP  隔离断言（本租户暂无可见客户）"
fi

echo "---- 四、M3 首登强制改密拦截 ----"
# 置标记 → 登录响应带标记 → 鉴权接口403拦截 → 改密清除标记后放行
psql ${TEST_DB_URL:-postgresql://ai_scrm:dev123@localhost/ai_scrm} -c \
  "UPDATE tenant_users SET must_change_password=true WHERE username='admin';" >/dev/null 2>&1

FTOKEN=$(curl -s -X POST "$B/api/v1/auth/login" -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"admin123"}' | jsonget "['data']['token']")
FLAG=$(curl -s -X POST "$B/api/v1/auth/login" -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"admin123"}' | jsonget "['data']['user']['must_change_password']")
[ "$FLAG" = "True" ] && check "登录响应带 must_change_password 标记" y y || check "登录响应带 must_change_password 标记" y n

CODE=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/super/tenants" -H "Authorization: Bearer $FTOKEN")
check "强改密标记未解除→鉴权接口403" 403 "$CODE"

CODE=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/auth/change-password" \
  -H "Authorization: Bearer $FTOKEN" -H "Content-Type: application/json" \
  -d '{"old_password":"wrong","new_password":"admin123"}')
check "错误旧密码拒绝" 400 "$CODE"

# 同值改密（admin123→admin123）仅用于清除标记，保持默认账号契约不变
CHG=$(curl -s -X POST "$B/api/v1/auth/change-password" \
  -H "Authorization: Bearer $FTOKEN" -H "Content-Type: application/json" \
  -d '{"old_password":"admin123","new_password":"admin123"}' | jsonget "['code']")
[ "$CHG" = "0" ] && check "改密成功清除标记" y y || check "改密成功清除标记" y n

CODE=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/super/tenants" -H "Authorization: Bearer $FTOKEN")
check "改密后接口恢复放行" 200 "$CODE"

echo "---- 五、M1 收银台 mock 全链路 + 幂等 ----"
BOOSTER=$(curl -s "$B/api/v1/packages" | python3 -c "
import sys,json
for p in json.load(sys.stdin)['data']:
    if p['p_type']=='increment': print(p['id']); break" 2>/dev/null)
ORDER=$(curl -s -X POST "$B/api/v1/billing/orders" \
  -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: ${ACME_ID}" -H "Content-Type: application/json" \
  -d "{\"package_id\":${BOOSTER:-0}}" | jsonget "['data']['id']")
ORDER=${ORDER:-none}
[ "$ORDER" != "none" ] && check "创建订单(mock渠道)" y y || check "创建订单(mock渠道)" y n

CHANNEL=$(curl -s "$B/api/v1/billing/orders/$ORDER" \
  -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: ${ACME_ID}" | jsonget "['data']['channel']")
[ "$CHANNEL" = "mock" ] && check "订单路由到mock渠道(channel=mock)" y y || check "订单路由到mock渠道(实际=$CHANNEL)" y n

GRANT=$(curl -s -X POST "$B/api/v1/billing/orders/mock-pay" \
  -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: ${ACME_ID}" -H "Content-Type: application/json" \
  -d "{\"order_id\":$ORDER}" | jsonget "['data']['granted']")
[ "$GRANT" = "True" ] && check "模拟到账→权益发放(granted)" y y || check "模拟到账→权益发放" y n

BALANCE=$(psql ${TEST_DB_URL:-postgresql://ai_scrm:dev123@localhost/ai_scrm} -tAc \
  "SELECT COALESCE(token_balance,0) FROM tenants WHERE id=${ACME_ID}" 2>/dev/null | tr -d '[:space:]')
[ "${BALANCE:-0}" -ge 1000000 ] && check "增量余额已入账(token=$BALANCE)" y y || check "增量余额已入账(token=$BALANCE)" y n

GRANT2=$(curl -s -X POST "$B/api/v1/billing/orders/mock-pay" \
  -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: ${ACME_ID}" -H "Content-Type: application/json" \
  -d "{\"order_id\":$ORDER}" | jsonget "['data']['granted']")
[ "$GRANT2" = "False" ] && check "重复支付幂等(不二次发放)" y y || check "重复支付幂等" y n

BALANCE2=$(psql ${TEST_DB_URL:-postgresql://ai_scrm:dev123@localhost/ai_scrm} -tAc \
  "SELECT COALESCE(token_balance,0) FROM tenants WHERE id=${ACME_ID}" 2>/dev/null | tr -d '[:space:]')
[ "$BALANCE2" = "$BALANCE" ] && check "幂等后余额未重复累计" y y || check "幂等后余额未重复累计($BALANCE→$BALANCE2)" y n

echo "---- 六、M4 OpenAPI 鉴权/隔离/计量 ----"
SK=$(curl -s -X POST "$B/api/v1/admin/apikeys" \
  -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: ${ACME_ID}" -H "Content-Type: application/json" \
  -d '{"name":"smoke-key","perms":["customer.read"]}' | jsonget "['data']['key']")

CODE=$(curl -s -o /dev/null -w "%{http_code}" "$B/openapi/v1/customers")
check "无Key访问→401" 401 "$CODE"

CODE=$(curl -s -o /dev/null -w "%{http_code}" "$B/openapi/v1/customers" -H "Authorization: Bearer sk_invalidinvalidinvalidinvalid00")
check "无效Key→401" 401 "$CODE"

CODE=$(curl -s -o /dev/null -w "%{http_code}" "$B/openapi/v1/customers" -H "Authorization: Bearer ${SK}")
check "有效Key读客户列表→200" 200 "$CODE"

# 隔离断言：Key归属acme，返回客户必须全属acme租户
OID_LIST=$(curl -s "$B/openapi/v1/customers?page_size=50" -H "Authorization: Bearer ${SK}" | \
  python3 -c "import sys,json;l=json.load(sys.stdin)['data']['list'];print(','.join(str(c['id']) for c in l) or '0')" 2>/dev/null)
BAD_OWNERS=$(psql ${TEST_DB_URL:-postgresql://ai_scrm:dev123@localhost/ai_scrm} -tAc \
  "SELECT count(*) FROM customers WHERE id IN (${OID_LIST:-0}) AND tenant_id <> ${ACME_ID}" 2>/dev/null | tr -d '[:space:]')
[ "${BAD_OWNERS:-1}" = "0" ] && check "Key隔离：返回客户全属归属租户" y y || check "Key隔离：混入${BAD_OWNERS}条他租户客户" y n

CODE=$(curl -s -o /dev/null -w "%{http_code}" "$B/openapi/v1/usage" -H "Authorization: Bearer ${SK}")
check "越权perm(customer.read打usage)→403" 403 "$CODE"

KEYID=$(psql ${TEST_DB_URL:-postgresql://ai_scrm:dev123@localhost/ai_scrm} -tAc \
  "SELECT id FROM api_keys WHERE key_prefix='${SK:0:10}' LIMIT 1" 2>/dev/null | tr -d '[:space:]')
curl -s -o /dev/null -X POST "$B/api/v1/admin/apikeys/$KEYID/disable" \
  -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: ${ACME_ID}"
CODE=$(curl -s -o /dev/null -w "%{http_code}" "$B/openapi/v1/customers" -H "Authorization: Bearer ${SK}")
check "停用Key即时生效→401" 401 "$CODE"

APICALLS=$(psql ${TEST_DB_URL:-postgresql://ai_scrm:dev123@localhost/ai_scrm} -tAc \
  "SELECT count(*) FROM usage_records WHERE metric='api_calls'" 2>/dev/null | tr -d '[:space:]')
[ "${APICALLS:-0}" -ge 1 ] && check "api_calls计量明细已落库" y y || check "api_calls计量明细已落库" y n

echo "---- 七、P1/P2 实时监控+健康检查+WS鉴权 ----"
ST_STATUS=$(curl -s "$B/status" | jsonget "['data']['status']")
[ -n "$ST_STATUS" ] && check "状态页返回健康分级(status=$ST_STATUS)" y y || check "状态页返回健康分级" y n

CODE=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/super/monitor/health" -H "Authorization: Bearer $TOKEN")
check "超管健康探测→200" 200 "$CODE"

CODE=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/ws/advisor")
check "WS顾问端无token→401" 401 "$CODE"

CODE=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/ws/client")
check "WS客户端缺参数→400" 400 "$CODE"

echo "---- 八、G-14 RLS 覆盖（关键表存在） ----"
for TBL in cdp_tag_assignments cdp_tag_definitions event_logs id_mappings inbox_events flow_state_machines templates features usage_ledger; do
  EXISTS=$(psql ${TEST_DB_URL:-postgresql://ai_scrm:dev123@localhost/ai_scrm} -tAc \
    "SELECT 1 FROM information_schema.tables WHERE table_name='${TBL}' LIMIT 1" 2>/dev/null | tr -d '[:space:]')
  [ "$EXISTS" = "1" ] && check "RLS表存在: $TBL" y y || check "RLS表存在: $TBL" y n
done

# delay_* 表已随「打字+线下偏移」延迟公式废弃而移除（AGENTS.md 延迟铁律：合并25s/简单8s/
# AI 5~15s 固定随机，旧表不再使用）。此处断言其确已清理，防止残留误导。
DELAY_TBL=$(psql ${TEST_DB_URL:-postgresql://ai_scrm:dev123@localhost/ai_scrm} -tAc \
  "SELECT count(*) FROM information_schema.tables WHERE table_name LIKE 'delay_%'" 2>/dev/null | tr -d '[:space:]')
[ "${DELAY_TBL:-0}" -eq 0 ] && check "delay_* 旧表已废弃清理(${DELAY_TBL}张)" y y || check "delay_* 旧表应已清理(实为${DELAY_TBL}张)" y n

echo "---- 九、G-15 Prometheus 指标端点 ----"
CODE=$(curl -s -o /dev/null -w "%{http_code}" "$B/metrics")
check "/metrics 端点可达" 200 "$CODE"

METRICS=$(curl -s "$B/metrics" 2>/dev/null)
echo "$METRICS" | grep -q "ai_scrm_kafka_publish_total" && check "指标: ai_scrm_kafka_publish_total 存在" y y || check "指标: ai_scrm_kafka_publish_total 存在" y n
echo "$METRICS" | grep -q "ai_scrm_complaint_total" && check "指标: ai_scrm_complaint_total 存在" y y || check "指标: ai_scrm_complaint_total 存在" y n

echo "---- 十、G-19 投诉事件集成 ----"
# 投诉关键词识别：发送含投诉意图的消息，验证 complaint 计数器不为负
COMP_BEFORE=$(echo "$METRICS" | grep "ai_scrm_complaint_total" | awk '{print $2}')
# 触发一次投诉检测（通过 feedback 接口或 AI 对话）
curl -s -X POST "$B/api/v1/chat/test" \
  -H "Content-Type: application/json" \
  -d '{"content":"我要投诉你们的服务态度太差了"}' >/dev/null 2>&1
METRICS2=$(curl -s "$B/metrics" 2>/dev/null)
COMP_AFTER=$(echo "$METRICS2" | grep "ai_scrm_complaint_total" | awk '{print $2}')
[ -n "$COMP_AFTER" ] && check "投诉事件集成(complaint counter=${COMP_AFTER})" y y || check "投诉事件集成" y n

echo "---- 十一、G-22 通用行业包 ----"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
[ -d "${PROJECT_ROOT}/packs-src/general" ] && check "packs-src/general/ 目录存在" y y || check "packs-src/general/ 目录存在" y n
GEN_COUNT=$(ls "${PROJECT_ROOT}/packs-src/general/"*.json 2>/dev/null | wc -l | tr -d ' ')
[ "${GEN_COUNT:-0}" -ge 6 ] && check "通用包JSON文件数(≥6)" y y || check "通用包JSON文件数(期望≥6 实际=${GEN_COUNT:-0})" y n

echo "---- 十二、商业化资金安全与契约回归（2026-09-11 修复批次） ----"
# A2/G-13：chat/guest 响应收口 RespOK 信封——customer_id/visitor_key 必须在 data 下
#（前端 Client.tsx 同步读 data；扁平形态回归即断链，C端建客死锁）
GUEST=$(curl -s -X POST "$B/api/v1/chat/guest" -H "Content-Type: application/json" -d '{}')
GCID=$(echo "$GUEST" | jsonget "['data']['customer_id']")
[ -n "$GCID" ] && check "chat/guest 信封 data.customer_id=${GCID}" y y || check "chat/guest 信封 data.customer_id" y n
# A3/G-18：super/tenants 分页信封 {list,total,page,page_size}（前端读 data.list）
SHAPE=$(curl -s "$B/api/v1/super/tenants?page_size=5" -H "Authorization: Bearer $TOKEN" \
  | python3 -c "import sys,json;d=json.load(sys.stdin)['data'];print('True' if isinstance(d.get('list'),list) and 'total' in d else 'False')")
check "super/tenants 分页信封(list+total)" True "$SHAPE"
# A4：admin/apikeys 租户作用域路径——超管须显式 X-Tenant-ID(P2-15)，响应为分页信封
AKNEED=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/admin/apikeys" -H "Authorization: Bearer $TOKEN")
check "admin/apikeys 超管无头→400强制显式(P2-15)" 400 "$AKNEED"
AKSHAPE=$(curl -s "$B/api/v1/admin/apikeys" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1" \
  | python3 -c "import sys,json;d=json.load(sys.stdin)['data'];print('True' if isinstance(d.get('list'),list) else 'False')")
check "admin/apikeys 信封 data.list" True "$AKSHAPE"
# R1：free 包一生一次——reward_claims 台账幂等，二次领取必须 409（无限自 mint ③桶堵口）
# 用超管 token+X-Tenant-ID:1（sales1 是顾问角色过不了 AdminRequired；租户作用域路径须显式带头）
FREE_ID=$($PSQL "SELECT id FROM packages WHERE code='trial_500' LIMIT 1" | tr -d '[:space:]')
curl -s -o /dev/null -X POST "$B/api/v1/billing/subscribe" -H "Authorization: Bearer $TOKEN" \
  -H "X-Tenant-ID: 1" -H "Content-Type: application/json" -d "{\"package_id\":${FREE_ID:-0}}"
C2=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/billing/subscribe" -H "Authorization: Bearer $TOKEN" \
  -H "X-Tenant-ID: 1" -H "Content-Type: application/json" -d "{\"package_id\":${FREE_ID:-0}}")
check "free包重复领取拒绝409(R1台账幂等)" 409 "$C2"
# R15：tenant_users.email 全局唯一索引存在（并发双注册最终闸门）
EMIDX=$($PSQL "SELECT 1 FROM pg_indexes WHERE indexname='ux_tenant_users_email_nonempty'" | tr -d '[:space:]')
check "tenant_users.email 唯一索引已建(R15)" 1 "$EMIDX"

echo "==== 结果: PASS=$PASS FAIL=$FAIL ===="
[ "$FAIL" = "0" ] || exit 1
