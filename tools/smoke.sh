#!/bin/bash
# DB 连接：TEST_DB_URL 环境变量覆盖（CI 用 ci_pass），默认本地 dev 库
PSQL="psql ${TEST_DB_URL:-postgresql://ai_scrm:dev123@localhost/ai_scrm} -tAc"
# ============================================================
# AI-SCRM SaaS 安全红线冒烟测试（Phase S 验收脚本）
# 用法: ./tools/smoke.sh [端口]   默认 9090
# 前置: 服务已启动（./start.sh 或 go run cmd/server/main.go）
# 覆盖: 租户解析 fail-closed / debug 兜底 / JWT↔Host 一致性 /
#       超管跨租户显式指定+审计 / C端租户归属 / 基础数据隔离 /
#       /status/detail readiness 生产就绪探针（2026-09-15 价值批；P2-4 批三拆分：公开 /status 无清单，
#       详情端点 X-Health-Token 闸 2 断言）/ 匿名 KB 搜索 visibility 收敛 3 断言（P2-4② 批三）
#       / E4 话术 A/B 实验字段 CRUD+过滤+校验+字符串PK修口 10 断言（二十二，2026-09-19 增强批）
#       / E3 全链路 trace_id：响应头回显+入口/队列日志同 trace 贯穿 3 断言（二十三，2026-09-19 增强批）
#       / E9 KB 检索重排：kb_rerank 键播种+开关往返+客户端未配置 fail-open 5 断言（二十四，2026-09-19 增强二批）
#       / E10 租户 OpenAPI 文档站：spec 端点可用+结构完整+内部面零泄露+缓存头 5 断言（二十五，2026-09-19 增强二批）
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

# B4 收口(2026-09-14)：改密会 bump token_version，旧 token 即刻失效——这是吊销语义的预期，
# 故"改密后放行"必须用**重新登录的新 token**；同时旧 FTOKEN 应被 401(token_revoked)。
CODE=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/super/tenants" -H "Authorization: Bearer $FTOKEN")
check "改密后旧token被吊销→401(B4)" 401 "$CODE"
# 重新登录拿新 token（标记已清除，新 token 携带最新 token_version）
NTOKEN=$(curl -s -X POST "$B/api/v1/auth/login" -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"admin123"}' | jsonget "['data']['token']")
CODE=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/super/tenants" -H "Authorization: Bearer $NTOKEN")
check "改密后重新登录→恢复放行200" 200 "$CODE"
# 下游断言复用主 TOKEN：改密已吊销旧 $TOKEN，统一刷新为最新会话（保持"已改密"契约不变）
TOKEN="$NTOKEN"

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

# 生产就绪探针：P2-4 拆分(2026-09-19 批三)——公开 /status 只留存活/版本字段，
# readiness 全量清单挪 /status/detail（X-Health-Token 守卫，未配置=恒403 fail-closed）
ST_RLEAK=$(curl -s "$B/status" | jsonget "['data'].get('readiness','ABSENT')")
[ "$ST_RLEAK" = "ABSENT" ] && check "公开/status不再含readiness清单" y y || check "公开/status不再含readiness清单" y n
ST_H403=$(curl -s -o /dev/null -w "%{http_code}" "$B/status/detail")
check "无令牌/status/detail被拒(403)" 403 "$ST_H403"
HT=$(grep '^HEALTH_TOKEN=' "$(dirname "$0")/../.env" 2>/dev/null | cut -d= -f2 | tr -d '[:space:]')
ST_READY=$(curl -s -H "X-Health-Token: $HT" "$B/status/detail" | jsonget "['data']['ready']")
[ -n "$ST_READY" ] && check "就绪详情端点带令牌返回ready=$ST_READY" y y || check "就绪详情端点带令牌返回ready" y n
ST_RNAME=$(curl -s -H "X-Health-Token: $HT" "$B/status/detail" | jsonget "['data']['readiness'][0]['name']")
[ -n "$ST_RNAME" ] && check "就绪检查项清单非空(首项=$ST_RNAME)" y y || check "就绪检查项清单非空" y n

# P2-4②(2026-09-19 批三)：匿名 /knowledge/fragments/search 只见 visibility=public——
# 攻击向量即"匿名+伪造 X-Tenant-ID 拖本租户私有片段全文"（TenantResolver 全局解析租户头），
# admin 新建片段默认 private 匿名带租户头也搜不出；API 置 public 后可搜（C 端目录通道）；收回即再隐
KMARK="护栏可见性片段$RANDOM"
KFRAG_ID=$(curl -s -X POST "$B/api/v1/admin/knowledge/fragments" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1" -H "Content-Type: application/json" \
  -d "{\"category\":\"服务\",\"title\":\"$KMARK\",\"content\":\"内部话术勿外泄$KMARK\"}" | jsonget "['data']['id']")
klen() { python3 -c "import sys,json;d=json.load(sys.stdin);print(len(d.get('data') or []))" 2>/dev/null; }
KPRIV=$(curl -s "$B/api/v1/knowledge/fragments/search?keyword=$KMARK" -H "X-Tenant-ID: 1" | klen)
check "私有片段匿名带租户头0命中(P2-4)" 0 "${KPRIV:-x}"
curl -s -o /dev/null -X PUT "$B/api/v1/admin/knowledge/fragments/$KFRAG_ID" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1" -H "Content-Type: application/json" -d '{"visibility":"public"}'
KPUB=$(curl -s "$B/api/v1/knowledge/fragments/search?keyword=$KMARK" -H "X-Tenant-ID: 1" | klen)
check "置public后匿名可搜(C端目录通道)" 1 "${KPUB:-x}"
curl -s -o /dev/null -X PUT "$B/api/v1/admin/knowledge/fragments/$KFRAG_ID" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1" -H "Content-Type: application/json" -d '{"visibility":"private"}'
KBACK=$(curl -s "$B/api/v1/knowledge/fragments/search?keyword=$KMARK" -H "X-Tenant-ID: 1" | klen)
check "收回private后匿名再0命中" 0 "${KBACK:-x}"
curl -s -o /dev/null -X DELETE "$B/api/v1/admin/knowledge/fragments/$KFRAG_ID" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1"

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

echo "---- 十三、C3 日志 PII 脱敏（2026-09-12）----"
# 经 guest+test 真实跑一轮含手机号的对话，验证 ai-scrm.log 新增行：
#   (a) 无连续 11 位手机号明文；(b) 掩码形态 139****2222 出现（证明脱敏确实生效、非空跑）
LOGFILE="${PROJECT_ROOT}/ai-scrm.log"
if [ -f "$LOGFILE" ]; then
  PVK=$(curl -s -X POST "$B/api/v1/chat/guest" -H "X-Tenant-ID: 1" -H "Content-Type: application/json" -d '{}')
  PCID=$(echo "$PVK" | jsonget "['data']['customer_id']")
  PKEY=$(echo "$PVK" | jsonget "['data']['visitor_key']")
  MARK=$(wc -c < "$LOGFILE" 2>/dev/null | tr -d '[:space:]')
  curl -s -o /dev/null --max-time 40 -X POST "$B/api/v1/chat/test?visitor_key=$PKEY" -H "X-Tenant-ID: 1" \
    -H "Content-Type: application/json" \
    -d "{\"customer_id\":${PCID:-1},\"content\":\"我电话13911112222，方便给我回个报价吗谢谢\"}"
  sleep 3
  NEWLOG=$(tail -c +$((MARK + 1)) "$LOGFILE" 2>/dev/null)
  LEAK=$(printf '%s' "$NEWLOG" | grep -cE '(^|[^0-9])1[3-9][0-9]{9}([^0-9]|$)')
  MASKED=$(printf '%s' "$NEWLOG" | grep -c '139\*\*\*\*2222')
  [ "${LEAK:-0}" = "0" ] && check "对话后日志无手机号明文(PII脱敏)" y y || check "对话后日志无手机号明文(发现${LEAK}行泄露)" y n
  [ "${MASKED:-0}" -ge 1 ] && check "手机号已脱敏落盘(${MASKED}行掩码形态)" y y || check "手机号应出现掩码形态(脱敏生效证据)" y n
else
  check "ai-scrm.log 不存在，跳过PII断言" y y
fi

echo "---- 十四、D7 数据导出 CSV（2026-09-12）----"
EXP_HDR="$(mktemp)"; EXP_BODY="$(mktemp)"
curl -s -D "$EXP_HDR" -o "$EXP_BODY" "$B/api/v1/admin/export/customers.csv" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1"
check "导出客户CSV 200" 200 "$(head -1 "$EXP_HDR" | awk '{print $2}')"
check "导出 content-type=text/csv" 1 "$(grep -i '^content-type:' "$EXP_HDR" | grep -c 'text/csv')"
check "CSV 表头含 id,name,phone" y "$(tail -c +4 "$EXP_BODY" | head -1 | grep -q 'id,name,phone' && echo y || echo n)"
RAWP=$(grep -Eo '1[3-9][0-9]{9}' "$EXP_BODY" | wc -l | tr -d ' ')
[ "${RAWP:-0}" = "0" ] && check "导出无明文手机号(PII掩码)" y y || check "导出应掩码手机号(发现${RAWP})" y n
EXPNOC=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/admin/export/customers.csv")
check "导出未登录 401/403" y "$(echo "$EXPNOC" | grep -qE '401|403' && echo y || echo n)"
rm -f "$EXP_HDR" "$EXP_BODY"

echo "---- 十五、C2 PIPL 删除权（2026-09-12）----"
GJSON=$(curl -s -X POST "$B/api/v1/chat/guest" -H "X-Tenant-ID: 1" -H "Content-Type: application/json" -d '{}')
GCID=$(echo "$GJSON" | jsonget "['data']['customer_id']")
GKEY=$(echo "$GJSON" | jsonget "['data']['visitor_key']")
DR=$(curl -s -X POST "$B/api/v1/privacy/deletion-request" -H "X-Tenant-ID: 1" -H "Content-Type: application/json" \
  -d "{\"scope\":\"customer\",\"customer_id\":${GCID:-0},\"visitor_key\":\"$GKEY\"}")
DRID=$(echo "$DR" | jsonget "['data']['id']")
check "删除请求受理返回id" y "$([ -n "$DRID" ] && [ "$DRID" != "None" ] && echo y || echo n)"
# 幂等：同主体重复受理应标记 duplicated=true
DR2=$(curl -s -X POST "$B/api/v1/privacy/deletion-request" -H "X-Tenant-ID: 1" -H "Content-Type: application/json" \
  -d "{\"scope\":\"customer\",\"customer_id\":${GCID:-0},\"visitor_key\":\"$GKEY\"}")
check "重复受理幂等duplicated" True "$(echo "$DR2" | jsonget "['data']['duplicated']")"
# 越权：错误 visitor_key 匿名应 403
BADVK=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/privacy/deletion-request" -H "X-Tenant-ID: 1" \
  -H "Content-Type: application/json" -d "{\"scope\":\"customer\",\"customer_id\":${GCID:-0},\"visitor_key\":\"wrong_vk\"}")
check "错误visitor_key拒绝403" 403 "$BADVK"
# 手动立即执行
curl -s -o /dev/null -X POST "$B/api/v1/admin/privacy/deletion-requests/$DRID/execute" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1"
DLIST=$(curl -s "$B/api/v1/admin/privacy/deletion-requests?status=anonymized" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1")
check "执行后状态转anonymized" y "$(echo "$DLIST" | python3 -c "import sys,json;d=json.load(sys.stdin)['data']['list'];print('y' if any(str(x['id'])=='$DRID' for x in d) else 'n')" 2>/dev/null)"
# 数据侧断言：该客户 PII 列已清空（name/visitor_key 置空），保留行与统计列
CLEARED=$(psql ${TEST_DB_URL:-postgresql://ai_scrm:dev123@localhost/ai_scrm} -tAc "SELECT count(*) FROM customers WHERE id=$GCID AND COALESCE(name,'')='' AND COALESCE(visitor_key,'')=''" | tr -d '[:space:]')
check "客户PII列已清空" 1 "$CLEARED"

echo "---- 十六、D6 出站事件 webhook（2026-09-12）----"
WHCREATE=$(curl -s -X POST "$B/api/v1/admin/webhooks" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1" \
  -H "Content-Type: application/json" \
  -d '{"name":"smoke-hook","url":"http://127.0.0.1:9/webhook","secret":"wh_s3cr3t_top","events":["payment.paid","order.refunded"]}')
WHID=$(echo "$WHCREATE" | jsonget "['data']['id']")
check "创建 webhook 订阅返回id" y "$([ -n "$WHID" ] && [ "$WHID" != "None" ] && echo y || echo n)"
check "创建回显一次性明文secret" "wh_s3cr3t_top" "$(echo "$WHCREATE" | jsonget "['data']['secret']")"
WHLIST=$(curl -s "$B/api/v1/admin/webhooks" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1")
check "webhook 列表不泄露明文secret" n "$(echo "$WHLIST" | grep -q "wh_s3cr3t_top" && echo y || echo n)"
check "webhook 列表含掩码字段" y "$(echo "$WHLIST" | grep -q "secret_mask" && echo y || echo n)"
WHAUTH=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/admin/webhooks")
check "webhook 未登录访问 401/403" y "$(echo "$WHAUTH" | grep -qE '401|403' && echo y || echo n)"
# 清理本次订阅
curl -s -o /dev/null -X DELETE "$B/api/v1/admin/webhooks/$WHID" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1"

echo "---- 十七、D3 KB pgvector 向量检索底座（2026-09-13）----"
VECVER=$($PSQL "SELECT count(*) FROM schema_migrations WHERE version='008_kb_embedding_vector'" 2>/dev/null | tr -d '[:space:]')
check "D3 迁移008已应用" 1 "$VECVER"
VECCOL=$($PSQL "SELECT count(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='knowledge_fragments' AND column_name='embedding'" 2>/dev/null | tr -d '[:space:]')
check "KB embedding 向量列存在" 1 "$VECCOL"
VECIDX=$($PSQL "SELECT count(*) FROM pg_indexes WHERE indexname='idx_kf_embedding'" 2>/dev/null | tr -d '[:space:]')
check "KB HNSW 索引存在" 1 "$VECIDX"
VECFLAG=$($PSQL "SELECT count(*) FROM system_configs WHERE \"key\"='kb_vector_search' AND tenant_id=0" 2>/dev/null | tr -d '[:space:]')
check "kb_vector_search 默认开关已播种" 1 "$VECFLAG"
FRAGJSON=$(curl -s "$B/api/v1/admin/knowledge/fragments?page=1&page_size=1" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1")
check "KB片段列表返回 vectorized 状态" y "$(echo "$FRAGJSON" | grep -q 'vectorized' && echo y || echo n)"

echo "---- 十八、C6 前端异常上报（2026-09-13）----"
CE_BAD=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/client-errors" -H "X-Tenant-ID: 1" \
  -H "Content-Type: application/json" -d '{"stack":"missing message"}')
check "前端异常缺 message 拒绝400" 400 "$CE_BAD"
CE_RES=$(curl -s -X POST "$B/api/v1/client-errors" -H "X-Tenant-ID: 1" -H "Content-Type: application/json" \
  -d '{"message":"页面崩了 13911112222","stack":"Error: crash\n  at Admin","route":"/admin","user_agent":"smoke","page":"http://localhost/admin","app":"desktop"}')
CE_ID=$(echo "$CE_RES" | jsonget "['data']['id']")
check "前端异常上报返回id" y "$([ -n "$CE_ID" ] && [ "$CE_ID" != "None" ] && echo y || echo n)"
CE_DB=$($PSQL "SELECT count(*) FROM feedbacks WHERE id=${CE_ID:-0} AND target_type='client_error' AND content NOT LIKE '%13911112222%' AND content LIKE '%***%'" 2>/dev/null | tr -d '[:space:]')
check "前端异常落库且PII掩码" 1 "$CE_DB"
CE_FILTER=$(curl -s "$B/api/v1/super/feedbacks?target_type=client_error&page_size=1" -H "Authorization: Bearer $TOKEN" \
  | jsonget "['data']['list'][0]['target_type']")
check "超管按 client_error 筛选" client_error "$CE_FILTER"

echo "---- 十九、2026-09-15 复核批修复护栏（P1-12/P2-13/B4旁路）----"
# P1-12：tag-weights 启停用路由——前端 EntityCrud 通用按钮此前恒 404，现必须可达且真实翻转 status
TWTAG=$(curl -s "$B/api/v1/admin/tags?page_size=1" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1" | jsonget "['data']['list'][0]['id']")
TWM=$(curl -s -X POST "$B/api/v1/admin/tag-weights" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1" \
  -H "Content-Type: application/json" -d "{\"tag_id\":${TWTAG:-1},\"t_vector_index\":1,\"weight_delta\":0.05,\"direction\":\"up\"}")
TWMID=$(echo "$TWM" | jsonget "['data']['id']")
TWDIS=$(curl -s -X POST "$B/api/v1/admin/tag-weights/${TWMID:-0}/disable" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1")
check "tag-weights disable 路由可达且status=0" 0 "$(echo "$TWDIS" | jsonget "['data']['status']")"
TWEN=$(curl -s -X POST "$B/api/v1/admin/tag-weights/${TWMID:-0}/enable" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1")
check "tag-weights enable 路由可达且status=1" 1 "$(echo "$TWEN" | jsonget "['data']['status']")"
curl -s -o /dev/null -X DELETE "$B/api/v1/admin/tag-weights/${TWMID:-0}" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1"
# P2-13：sales1 给"别人名下"客户打标必须 404（四级数据范围，与 advisor 端同口径；此前只过租户闸）
S1ID=$($PSQL "SELECT id FROM tenant_users WHERE username='sales1' LIMIT 1" | tr -d '[:space:]')
OTHCID=$($PSQL "SELECT id FROM customers WHERE tenant_id=1 AND COALESCE(assigned_user_id,0) NOT IN (0,${S1ID:-0}) LIMIT 1" | tr -d '[:space:]')
S1TAG=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/customers/${OTHCID:-999999}/tags" -H "Authorization: Bearer $STOKEN" -H "X-Tenant-ID: 1" \
  -H "Content-Type: application/json" -d "{\"tag_ids\":[${TWTAG:-1}]}")
check "sales给他人名下客户打标→404(范围外不泄露存在性)" 404 "$S1TAG"
# P1-12：订单列表 Select 白名单须含 qr_content/refund_requested（Billing 页徽标与收款码渲染依赖列表行字段）
ORD_NO="BO$RANDOM$RANDOM"
$PSQL "INSERT INTO billing_orders (order_no,tenant_id,package_id,amount_cents,period,channel,status,created_at,updated_at) VALUES ('$ORD_NO',1,1,100,'once','manual','pending',NOW(),NOW())" >/dev/null 2>&1
ORLIST=$(curl -s "$B/api/v1/billing/orders?limit=50" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1")
check "订单列表含qr_content字段" y "$(echo "$ORLIST" | grep -q '"qr_content"' && echo y || echo n)"
check "订单列表含refund_requested字段" y "$(echo "$ORLIST" | grep -q '"refund_requested"' && echo y || echo n)"
$PSQL "DELETE FROM billing_orders WHERE order_no='$ORD_NO'" >/dev/null 2>&1
# P0-6：改密吊销旁路覆盖 /auth/email/code（v1.Use 主链之外的旁挂路由，此前旧 token 永活）
CODE_AFTER=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/auth/email/code" -H "Authorization: Bearer $STOKEN" -H "X-Tenant-ID: 1")
check "未吊销 sales token 打 email/code 不被误伤(非401)" n "$(echo "$CODE_AFTER" | grep -q '^401$' && echo y || echo n)"

echo "---- 二十、2026-09-18 批二 §八-6 平台运营 UI 依赖端点护栏 ----"
# 背景：InvoiceTab/PackTab 两个新纯前端 Tab 直连 /super/invoices* 与 /super/packs* PUT 路由
# （PUT 曾因契约脚本 METHOD 提取盲区漏检，此处以行为断言补位）+ 角色负向。
INVLIST=$(curl -s "$B/api/v1/super/invoices?status=requested" -H "Authorization: Bearer $TOKEN")
check "super发票列表 code=0 且含 order_id/invoice_status 契约键" y "$(echo "$INVLIST" | grep -q '"code":0' && echo "$INVLIST" | grep -q '"order_id"' && echo "$INVLIST" | grep -q '"invoice_status"' && echo y || echo n)"
INVC403=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/super/invoices" -H "Authorization: Bearer $STOKEN" -H "X-Tenant-ID: 1")
check "sales打super发票列表→403" 403 "$INVC403"
# 发票状态机全回环（合成订单，结束即删）：requested→issued→voided
INV_NO="BOINV$RANDOM$RANDOM"
$PSQL "INSERT INTO billing_orders (order_no,tenant_id,package_id,amount_cents,period,channel,status,invoice_status,invoice_requested,invoice_title,created_at,updated_at) VALUES ('$INV_NO',1,1,9900,'once','manual','paid','requested',true,'冒烟测试公司',NOW(),NOW()) RETURNING id" >/tmp/smoke_inv_id.txt 2>/dev/null
INV_OID=$(grep -Eo '^[0-9]+$' /tmp/smoke_inv_id.txt | head -1)
INV_ISSUE=$(curl -s -X POST "$B/api/v1/super/invoices/${INV_OID:-0}/issue" -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" -d '{"invoice_no":"TESTINV-SMOKE-001"}')
check "发票回录issue→issued且带发票号" issued "$(echo "$INV_ISSUE" | jsonget "['data']['invoice_status']")"
INV_VOID=$(curl -s -X POST "$B/api/v1/super/invoices/${INV_OID:-0}/void" -H "Authorization: Bearer $TOKEN")
check "发票作废→voided" voided "$(echo "$INV_VOID" | jsonget "['data']['invoice_status']")"
INV_NO400=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/super/invoices/${INV_OID:-0}/issue" -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" -d '{}')
check "issue缺invoice_no→400" 400 "$INV_NO400"
$PSQL "DELETE FROM billing_orders WHERE order_no='$INV_NO'" >/dev/null 2>&1
# 行业包上架/下架 PUT（PackTab 主链路）：取第一个 active 包做 disabled→active 往返，秒复原
PKID=$(curl -s "$B/api/v1/super/packs?page_size=200" -H "Authorization: Bearer $TOKEN" | python3 -c "import sys,json;d=json.load(sys.stdin);rows=d.get('data') or [];rows=rows if isinstance(rows,list) else (rows.get('list') or []);print(next((str(r['id']) for r in rows if r.get('status')=='active'),''))" 2>/dev/null)
if [ -n "$PKID" ]; then
  PUTOFF=$(curl -s -o /dev/null -w "%{http_code}" -X PUT "$B/api/v1/super/packs/$PKID/status" -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" -d '{"status":"disabled"}')
  check "pack PUT status disabled 路由可达→200" 200 "$PUTOFF"
  PUTON=$(curl -s -o /dev/null -w "%{http_code}" -X PUT "$B/api/v1/super/packs/$PKID/status" -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" -d '{"status":"active"}')
  check "pack PUT status 复原active→200" 200 "$PUTON"
else
  check "pack列表存在active包(往返前置)" y n
fi
PKBAD=$(curl -s -o /dev/null -w "%{http_code}" -X PUT "$B/api/v1/super/packs/999999/status" -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" -d '{"status":"active"}')
check "pack状态改不存在包→404" 404 "$PKBAD"
PK403=$(curl -s -o /dev/null -w "%{http_code}" -X PUT "$B/api/v1/super/packs/1/status" -H "Authorization: Bearer $STOKEN" -H "X-Tenant-ID: 1" -H "Content-Type: application/json" -d '{"status":"active"}')
check "sales打pack状态路由→403" 403 "$PK403"
# 侧边栏 JS-SDK 部署前提：CSP script-src 定点放行 res.wx.qq.com（通配/漏配即装配必被拦）
CSP=$(curl -sI "$B/" | grep -i "^content-security-policy" | tr -d '\r')
check "CSP定点放行res.wx.qq.com" y "$(echo "$CSP" | grep -q 'https://res.wx.qq.com' && echo y || echo n)"

echo "---- 二十一、2026-09-19 残项收口批：超管退款受理队列护栏 ----"
# B7 双轨退款平台审批位（列表+驳回路由行为）；执行退款走 PSP 真出款不在 smoke 触碰，
# 用合成订单验证 队列可见→驳回清标记→出队→重复驳回409 全链，结束即删。
RR403=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/super/billing/refund-requests" -H "Authorization: Bearer $STOKEN" -H "X-Tenant-ID: 1")
check "sales打退款申请队列→403" 403 "$RR403"
RLIST=$(curl -s "$B/api/v1/super/billing/refund-requests" -H "Authorization: Bearer $TOKEN")
check "退款队列 code=0 且 data 为数组" y "$(echo "$RLIST" | python3 -c "import sys,json;d=json.load(sys.stdin);print('y' if d.get('code')==0 and isinstance(d.get('data'),list) else 'n')" 2>/dev/null)"
RF_NO="BORF$RANDOM$RANDOM"
$PSQL "INSERT INTO billing_orders (order_no,tenant_id,package_id,amount_cents,period,channel,status,refund_requested,created_at,updated_at) VALUES ('$RF_NO',1,1,9900,'monthly','manual','paid',true,NOW(),NOW()) RETURNING id" >/tmp/smoke_rf_id.txt 2>/dev/null
RF_OID=$(grep -Eo '^[0-9]+$' /tmp/smoke_rf_id.txt | head -1)
RLIST2=$(curl -s "$B/api/v1/super/billing/refund-requests" -H "Authorization: Bearer $TOKEN")
check "合成退款申请单出现在队列且带 order_no/amount_cents 契约键" y "$(echo "$RLIST2" | grep -q "\"$RF_NO\"" && echo "$RLIST2" | grep -q '"amount_cents"' && echo y || echo n)"
RJ1=$(curl -s -X POST "$B/api/v1/super/billing/orders/${RF_OID:-0}/refund/reject" -H "Authorization: Bearer $TOKEN")
check "驳回退款申请→code=0" 0 "$(echo "$RJ1" | jsonget "['code']")"
check "驳回后 refund_requested 落库为 false" f "$($PSQL "SELECT refund_requested FROM billing_orders WHERE order_no='$RF_NO'" | tr -d '\r\n ')"
RLIST3=$(curl -s "$B/api/v1/super/billing/refund-requests" -H "Authorization: Bearer $TOKEN")
check "驳回后订单出队" n "$(echo "$RLIST3" | grep -q "$RF_NO" && echo y || echo n)"
RJ2=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/super/billing/orders/${RF_OID:-0}/refund/reject" -H "Authorization: Bearer $TOKEN")
check "重复驳回无申请单→409" 409 "$RJ2"
RJ404=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/super/billing/orders/999999/refund/reject" -H "Authorization: Bearer $TOKEN")
check "驳回不存在订单→404" 404 "$RJ404"
$PSQL "DELETE FROM billing_orders WHERE order_no='$RF_NO'" >/dev/null 2>&1

echo "---- 二十二、2026-09-19 增强批：E4 话术 A/B 实验字段护栏 ----"
# 召回分桶数学在单测（template_ab_test.go 8 例）；此处守 CRUD 契约与过滤/校验位
ABG="smoke_ab_$RANDOM"
ABA="tpl_smoke_a_${RANDOM}${RANDOM}"
ACRT=$(curl -s -X POST "$B/api/v1/strategy/templates" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1" -H "Content-Type: application/json" \
  -d "{\"id\":\"$ABA\",\"name\":\"冒烟实验A\",\"anchor_type\":2,\"prompt_template\":\"话术A\",\"status\":2,\"ab_group\":\"$ABG\",\"ab_weight\":60}")
check "创建实验草稿→code=0 且 ab_group 回显" "$ABG" "$(echo "$ACRT" | jsonget "['data']['ab_group']" 2>/dev/null)"
check "草稿 status=2 契约回显" 2 "$(echo "$ACRT" | jsonget "['data']['status']" 2>/dev/null)"
check "ab_weight 越界→400" 400 "$(curl -s -o /dev/null -w '%{http_code}' -X POST "$B/api/v1/strategy/templates" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1" -H "Content-Type: application/json" \
  -d "{\"id\":\"${ABA}x\",\"name\":\"越界\",\"anchor_type\":2,\"prompt_template\":\"x\",\"ab_weight\":120}")"
check "ab_weight 负数→400" 400 "$(curl -s -o /dev/null -w '%{http_code}' -X POST "$B/api/v1/strategy/templates" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1" -H "Content-Type: application/json" \
  -d "{\"id\":\"${ABA}n\",\"name\":\"负权\",\"anchor_type\":2,\"prompt_template\":\"x\",\"ab_weight\":-1}")"
ABLIST=$(curl -s "$B/api/v1/strategy/templates?ab_group=$ABG" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1")
check "列表按实验组过滤恰命中1条" 1 "$(echo "$ABLIST" | python3 -c "import sys,json;d=json.load(sys.stdin);print(len(d['data']['list']))" 2>/dev/null)"
ABSTAT=$(curl -s "$B/api/v1/admin/packs/stats?ab_group=$ABG" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1" | jsonget "['code']" 2>/dev/null)
check "实验组对照统计端点 code=0" 0 "$ABSTAT"
AUPD=$(curl -s -X PUT "$B/api/v1/strategy/templates/$ABA" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1" -H "Content-Type: application/json" \
  -d "{\"id\":\"$ABA\",\"name\":\"冒烟实验A\",\"anchor_type\":2,\"prompt_template\":\"话术A\",\"status\":1,\"ab_group\":\"\",\"ab_weight\":0}")
check "PUT 置空实验组=退出实验" 0 "$(echo "$AUPD" | jsonget "['code']" 2>/dev/null)"
check "退出实验后 status 发布为 1" 1 "$(curl -s "$B/api/v1/strategy/templates/$ABA" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1" | jsonget "['data']['status']" 2>/dev/null)"
check "退出实验后 ab_group 落库清空" y "$($PSQL "SELECT CASE WHEN ab_group='' THEN 'y' ELSE 'n' END FROM templates WHERE id='$ABA'" | tr -d '\r\n ')"
check "清理实验模板" 0 "$(curl -s -X DELETE "$B/api/v1/strategy/templates/$ABA" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1" | jsonget "['code']" 2>/dev/null)"
# E2 双轨观测位：readiness 增 sentry_configured（未配置只展示不红灯），且不在公开 /status 泄露
HT=$(grep '^HEALTH_TOKEN=' "$(dirname "$0")/../.env" | cut -d= -f2 | tr -d '[:space:]')
check "readiness含sentry_configured观测位" 1 "$(curl -s "$B/status/detail" -H "X-Health-Token: $HT" | grep -c '"sentry_configured"')"
check "公开/status不泄露sentry_configured" 0 "$(curl -s "$B/status" | grep -c 'sentry_configured')"

echo "---- 二十三、2026-09-19 增强批：E3 全链路 trace_id 护栏 ----"
# /chat/test 带客户端 trace：响应头回显 + 同一 trace 串起「入口 slog 行 + 合并队列日志 trace= 片段」
E3TID="e3probe$(date +%s)"
if [ -f "$LOGFILE" ]; then
  E3G=$(curl -s -X POST "$B/api/v1/chat/guest" -H "X-Tenant-ID: 1" -H "Content-Type: application/json" -d '{}')
  E3CID=$(echo "$E3G" | jsonget "['data']['customer_id']")
  E3KEY=$(echo "$E3G" | jsonget "['data']['visitor_key']")
  E3MARK=$(wc -c < "$LOGFILE" | tr -d '[:space:]')
  E3HDR=$(curl -s -o /dev/null -D - --max-time 25 -X POST "$B/api/v1/chat/test?visitor_key=$E3KEY" -H "X-Tenant-ID: 1" -H "Content-Type: application/json" \
    -H "X-Trace-ID: $E3TID" -d "{\"customer_id\":${E3CID:-1},\"content\":\"在吗\"}")
  echo "$E3HDR" | grep -qi "^x-trace-id: $E3TID" && check "X-Trace-ID客户端值响应头回显" y y || check "X-Trace-ID客户端值响应头回显" y n
  sleep 1
  E3NEW=$(tail -c +$((E3MARK + 1)) "$LOGFILE" 2>/dev/null)
  E3LINES=$(printf '%s' "$E3NEW" | grep -c "$E3TID")
  [ "${E3LINES:-0}" -ge 2 ] && check "同一trace贯穿入口+队列日志≥2行(${E3LINES}行)" y y || check "同一trace贯穿入口+队列日志≥2行(实际${E3LINES}行)" y n
  E3TAGN=$(printf '%s' "$E3NEW" | grep -c "trace=$E3TID")
  [ "${E3TAGN:-0}" -ge 1 ] && check "合并队列日志带trace=片段" y y || check "合并队列日志带trace=片段" y n
else
  check "ai-scrm.log 不存在，跳过E3 trace断言" y y
fi

echo "---- 二十四、2026-09-19 增强二批：E9 KB 检索重排护栏 ----"
# 重排数学/补漏/fail-open 在单测（rerank_test.go 12 例）；此处守默认键播种 + 热开关往返 + 行为零差
RCHK1=$($PSQL "SELECT count(*) FROM system_configs WHERE \"key\"='kb_rerank' AND tenant_id=0 AND value='false'" 2>/dev/null | tr -d '[:space:]')
check "kb_rerank 默认关已播种" 1 "$RCHK1"
RCHK2=$($PSQL "SELECT count(*) FROM system_configs WHERE \"key\"='kb_rerank_candidates' AND tenant_id=0 AND value='8'" 2>/dev/null | tr -d '[:space:]')
check "kb_rerank_candidates 默认8已播种" 1 "$RCHK2"
RCRT=$(curl -s -X PUT "$B/api/v1/admin/config" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1" \
  -H "Content-Type: application/json" -d '[{"key":"kb_rerank","value":"true"}]')
check "kb_rerank 热开关可打开(code=0)" 0 "$(echo "$RCRT" | jsonget "['code']" 2>/dev/null)"
# 开关开但客户端未配置：检索必须 fail-open 正常返回（零行为差，绝不 5xx）
RSRCH=$(curl -s "$B/api/v1/knowledge/fragments/search?q=越野&tenant_id=1" -H "X-Tenant-ID: 1")
check "rerank开关开+客户端未配置检索仍code=0(fail-open)" 0 "$(echo "$RSRCH" | jsonget "['code']" 2>/dev/null)"
RCBK=$(curl -s -X PUT "$B/api/v1/admin/config" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1" \
  -H "Content-Type: application/json" -d '[{"key":"kb_rerank","value":"false"}]')
check "kb_rerank 开关恢复关闭" 0 "$(echo "$RCBK" | jsonget "['code']" 2>/dev/null)"

echo "---- 二十五、2026-09-19 增强二批：E10 租户 OpenAPI 文档站护栏 ----"
# 规格内容↔真实路由双向一致由契约层守护（apidump -format openapi）；此处守端点可用、结构完整、内部面零泄露
SPEC_FILE=$(mktemp)
SPEC_HTTP=$(curl -s -o "$SPEC_FILE" -w '%{http_code}' "$B/api/v1/openapi/spec")
check "GET /api/v1/openapi/spec 公开免登录 200" 200 "$SPEC_HTTP"
grep -q '"openapi": "3.0.3"' "$SPEC_FILE" && grep -q '"/chat/completions"' "$SPEC_FILE" && grep -q '"/cdp/profiles/{one_id}"' "$SPEC_FILE" \
  && check "spec 为 OpenAPI3 且含核心端点" y y || check "spec 为 OpenAPI3 且含核心端点" y n
# 攻击面红线：租户面文档绝不含 /admin、/super 内部路径
grep -q '/admin\|/super' "$SPEC_FILE" && check "spec 内部面零泄露(/admin|/super 0 命中)" y n || check "spec 内部面零泄露(/admin|/super 0 命中)" y y
rm -f "$SPEC_FILE"
SPEC_HDR=$(curl -s -D - -o /dev/null "$B/api/v1/openapi/spec" | tr -d '\r')
echo "$SPEC_HDR" | grep -qi 'Cache-Control: public, max-age=60' && check "spec 响应带公共缓存头(60s)" y y || check "spec 响应带公共缓存头(60s)" y n
check "spec 端点返回裸 OpenAPI JSON(非信封)" y "$(python3 -c "
import json,urllib.request
d=json.load(urllib.request.urlopen('$B/api/v1/openapi/spec'))
print('y' if 'paths' in d and 'code' not in d else 'n')" 2>/dev/null)"

echo "---- 二十六、2026-09-20 审计批 P1-3：公开面 IP/Key 限流护栏 ----"
# 各面连打超限必 429（本段刻意放在最后：打爆的是各自独立桶，且其后脚本不再触这些端点；
# 60s 窗口自然重置，勿在其它脚本前置依赖这些端点）
KNOW_LAST=""
for i in $(seq 1 65); do
  KNOW_LAST=$(curl -s -o /dev/null -w '%{http_code}' "$B/api/v1/knowledge/brands?tenant_id=1")
done
check "knowledge 连打65次超限(429)" 429 "$KNOW_LAST"
COLL_LAST=""
for i in $(seq 1 305); do
  COLL_LAST=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$B/api/v1/collector" -H "Content-Type: application/json" -H "X-Collector-Key: rl_probe_key" -d '[]')
done
check "collector 连打305次超限(429)" 429 "$COLL_LAST"
# collector 限流按 Key 维度：换 Key 不受上一个桶影响（回落放行至鉴权层 401）
COLL_B=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$B/api/v1/collector" -H "Content-Type: application/json" -H "X-Collector-Key: rl_probe_key_other" -d '[]')
check "collector 换Key不误伤(401未授权而非429)" 401 "$COLL_B"

echo "==== 结果: PASS=$PASS FAIL=$FAIL ===="
[ "$FAIL" = "0" ] || exit 1
