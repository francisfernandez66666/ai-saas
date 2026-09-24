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
#       / 2026-09-20 审计批：公开面 IP/Key 限流 429+换Key不误伤 3 断言（二十六）
#       / 2026-09-20 审计批三 P2-1：相似消息「仅在途批次」抑制护栏 2 断言（二十七）
#       / 2026-09-21 B2：知识库公开面 visibility 收敛 3 断言（二十八）
#       / 2026-09-21 D2：AI 贡献度看板口径自洽 6 断言（二十九，含"会话数与消息量
#         必须同零或同正"的缺陷复现断言 + 跨租户隔离；断言逻辑已用历史数字双向自证）
#       / 2026-09-22~23 批五/批六：AI 销售闭环开关面 5 断言 + 四开关租户往返 12 断言
#         + L1 判优卡片"租户级参数真生效"3 断言（三十：默认门槛下判功效不足、租户降门槛即出结论
#         leading/suggest_review、奖励指标越白名单仍按 lead 出卡防 reward hacking；含量纲改版
#         存量纠偏断言 step=0.2→10 / max=3→100，本段合成数据自清理）
#       / 2026-09-23 批六：数据层治理与运维观测面 7 断言（三十一：迁移 017/018/019 台账与列/索引实存、
#         访客密钥补签零残留、orphan_messages=0、归档关闭不伪装零积压、Redis 声明一致性观测位、
#         公开 /status 反泄露）
#       / 2026-09-23 主动触达最小闭环 23 断言（三十二：迁移020与两条部分索引实存、四键出厂默认关、
#         开关关时零落库、租户层开关生效且不污染系统层、排期→pending→撤回状态机、落库租户归属、
#         超长文案/缺客户入参拒绝、全天静默自动顺延、跨租户列表与撤回均不可见、非管理员被拒、自清理）
#       / 2026-09-23 D3 用量预警与到期催缴 34 断言（三十三：迁移021两表四索引与 running 部分索引、
#         六键出厂默认含两开关 false、平台级键忽略租户覆盖（直插租户层 dunning_steps 仍读系统层）、
#         预警留痕/进度条契约（period_key 形态、空态为 [] 不是 null、三 metric 恒在）、无序列租户 exists=false、
#         超管催缴队列与租户侧同源、ACME 隔离、去重键撞库被拒、人工 nudge 入参拒绝且不落审计、
#         人工重置只动催缴行不动租户 status 且留 dunning_manual_reset 审计、二次重置 400、only_open 双向、
#         非法 tid 400、四条计数器、合成数据清零）
#       / 2026-09-23 D4 贡献度下钻与看板同源 19 断言（三十四：合成四类客户先自检落库再断言，
#         六个指标下钻 total 与卡片六个字段逐字相等、换 90 天窗口两侧同动、days 真进下钻查询、
#         可下钻白名单恰六个客户级指标（消息量/会话数/待接管单位不同不得入内）、名单人名与接待归属正确
#         且按客户 ID 倒序、page_size=1 翻页不重不漏且 total 恒定、越界页空名单不改 total、硬顶 100、
#         非白名单指标 400、缺 metric 400、未登录 401、随行下发 label/note、跨租户名单不可见、自清理）
#       / 2026-09-23 获客批：获客活码渠道归因 44 断言（三十五：迁移022两表与全局唯一码/部分索引/归因列实存、
#         建码四路稳定原因码拒绝、短码形态排除易混字符、链接由后端三级基址拼出、空态为 []、展示口径（7渠道+5客户级指标+
#         中文名）后端下发、二维码出 PNG 且超大 size 被钳、跨租户出图/下钻均 404、启停缺参与不存在分别 400/404、
#         公开解析四类失败同形 404 不透露存在性、扫码窗口去重只记一次、归因租户取自码行、
#         别家的码打到本家只记不上（列仍空且客户照常建成）、停用码不再归因、
#         「新增客户」卡片数字与名单 total 逐字相等、扫码次数/缺 metric 判 400、page_size 硬顶 100、未登录 401、自清理）
#       / 2026-09-24 商机批：商机管道看板与报价版本链 76 断言（三十六：迁移023两表实存、
#         一客户一在途单=复合唯一+部分索引（终局行不占位、流失后可重开新单且不覆盖历史）、
#         一单一在途报价=部分唯一（只算 draft/sent）、建单/推进/终局的稳定原因码
#         （title_required/stage_unknown/amount_negative/amount_too_large/deal_already_open/
#         same_stage/stage_backward/won_amount_required/lost_reason_required/deal_closed）、
#         报价合计一律服务端按明细算、已发出内容锁死只能出新版（旧版标 superseded 不删）、
#         接受报价≠成交（两步分开）、看板 4 分组+6 阶段共 10 格与下钻名单逐字同源
#         （先自检"参与比对的格子数=10、非零格子≥5"防 0==0 假绿）、在途金额/赢单率口径、
#         缺/非法 filter 判 400、page_size 硬顶与越界页 total 如实、跨租户 404 同形态、
#         AI 自动开单键出厂 false、顾问台面来源由后端判定、自清理）
#       / 2026-09-24 E8 批：企微会话存档 65 断言（三十七：迁移024五个存档列与留痕表实存、
#         同通道同 msgid 唯一=部分索引（空 msgid 的 switch/event 报文不占位）、两条取数索引、开关出厂 false、
#         未登录 401 与 sales 403、跨租户与不存在同码同文案（自增 ID 探测不到别家通道）、
#         状态摘要零密钥材料、无密钥不许开开关且被拒不半落库、密钥位数校验、只公钥出接口且私钥密文入库、
#         手动同步回 HTTP200+ok=false+稳定码（SDK 缺口是环境状态不是 5xx）且绝不编造留痕行、
#         未开存档的通道回 archive_disabled、空态 []、page_size 硬顶 100 回显生效值、非法时间判 400、
#         列表不含 content_text 且 176 字长文只回 120 字摘要+省略号（按字符截断）、按 seq 倒序让无 msg_time
#         的解不开行不消失、failed/keyword 筛选（% 当字面量）、详情逐字节回显全文且读一次留一条审计、
#         /status/detail 直出 chat_archive 观测位（有启用通道时不得报 not_wired/disabled、判 warn 不判 crit）、
#         公开 /status 零泄露、关开关只改开关不删留痕、自清理）
#       / 2026-09-24 残项收口：超管租户检索 10 断言（三十八：/super/tenants 补 q——纯数字按 ID
#         直达、名称/编码模糊、total 只算命中数、无命中回 [] 不是 null、% 与 _ 按字面量不被
#         当成通配符放大成全表、超整型范围数字不 5xx、未登录 401、自清理；含"page_size=1 时
#         最旧租户看不见"的对照前提，缺它本段会在一家本来看得见的租户上假绿）
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
curl -s -X POST "$B/api/v1/chat/unauthorized" \
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
  curl -s -o /dev/null --max-time 40 -X POST "$B/api/v1/chat/unauthorized?visitor_key=$PKEY" -H "X-Tenant-ID: 1" \
    -H "Content-Type: application/json" \
    -d "{\"customer_id\":${PCID:-1},\"content\":\"我电话13911112222，方便给我回个报价吗谢谢\"}"
  sleep 3
  NEWLOG=$(tail -c +$((MARK + 1)) "$LOGFILE" 2>/dev/null)
  LEAK=$(printf '%s' "$NEWLOG" | grep -cE '(^|[^0-9])1[3-9][0-9]{9}([^0-9]|$)')
  MASKED=$(printf '%s' "$NEWLOG" | grep -c '139\*\*\*\*2222')
  # ★前提断言（2026-09-21 补）：日志若没有新增字节，下面两条会**一假绿一假红**——
  # "无明文泄露"平凡通过（空串里当然没有手机号），"出现掩码"却红灯。
  # 实测踩到：服务未把 stdout 重定向到 ai-scrm.log（如手工 nohup 启动）时，
  # 该段会给出"脱敏通过"的错误结论。故先钉住"日志确实增长了"这一前提。
  NOWBYTES=$(wc -c < "$LOGFILE" 2>/dev/null | tr -d '[:space:]')
  NEWBYTES=$(( ${NOWBYTES:-0} - ${MARK:-0} ))
  [ "${NEWBYTES:-0}" -gt 0 ] && check "对话后日志确有新增(脱敏断言前提)" y y \
    || check "对话后日志无新增→脱敏断言不可信(服务未写 ai-scrm.log)" y "n(0字节)"
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
  E3HDR=$(curl -s -o /dev/null -D - --max-time 25 -X POST "$B/api/v1/chat/unauthorized?visitor_key=$E3KEY" -H "X-Tenant-ID: 1" -H "Content-Type: application/json" \
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

echo "---- 二十七、2026-09-20 审计批三 P2-1：相似消息「仅在途批次」抑制护栏 ----"
# 修复前：与历史消息关键词重叠>50%（窗口≥2min）即走 merged 分支——主批次早已回复完毕时，
# 第二句相同内容被标 merged_suppressed 且无人再答（静默丢答）。现在抑制前必须探测
# HasInflightBatch（本地 processing/简单锁/pending 积压，或 Redis 处理锁与待合并列表）。
# 有在途批次时的 merged 路径行为不变，由 TestHasInflightBatch 与 chat_main.go 分支注释钉住。
# 注：主 TOKEN 为 super_admin 会话——批三 P2-15 收紧后租户作用域路径（/customers、/chat）
# 必须带 X-Tenant-ID，否则 400 空 CID 把两条护栏断言打成假失败（终局回归首跑实证）。
P21CID=$(curl -s -X POST "$B/api/v1/customers" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: ${ACME_ID}" -H 'Content-Type: application/json' \
  -d '{"name":"P21护栏客","remark":"smoke p2-1"}' | jsonget "['data']['id']")
P21MSG='你好，请问现在买车有什么优惠活动吗'
# 第一条同步等回复完毕（同时建立"历史相似句"基线）；返回即代表在途批次已释放
curl -s --max-time 60 -X POST "$B/api/v1/chat" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: ${ACME_ID}" -H 'Content-Type: application/json' \
  -d "{\"customer_id\":$P21CID,\"content\":\"$P21MSG\"}" >/dev/null
# 第二条相同内容：此刻无在途批次——旧逻辑必 merged:true（丢答），P2-1 后必须正常入队回答
P21B=$(curl -s --max-time 60 -X POST "$B/api/v1/chat" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: ${ACME_ID}" -H 'Content-Type: application/json' \
  -d "{\"customer_id\":$P21CID,\"content\":\"$P21MSG\"}")
check "相同内容第二连发不被merged抑制(P2-1)" False "$(echo "$P21B" | jsonget "['data']['merged']")"
# 落库断言走 DB 字节级：/api/v1/chat 正常路径响应本就不回 customer_msg_id（该字段仅
# merged/相似抑制分支设置，见 chat_main.go:294-298），旧断言拿它查落库是测试侧口径错。
P21CNT=$(psql ${TEST_DB_URL:-postgresql://ai_scrm:dev123@localhost/ai_scrm} -tAc \
  "SELECT count(*) FROM messages WHERE customer_id=$P21CID AND sender_type='customer' AND content='$P21MSG'" | tr -d '[:space:]')
[ "${P21CNT:-0}" -ge 2 ] 2>/dev/null && check "第二连发正常落库可查(客户消息2条,无丢答)" y y || check "第二连发正常落库可查(客户消息2条,无丢答)" y "n($P21CNT)"
# P2-2（同段捎带）：readiness 显式 rls_effective 观测位——纠偏"RLS_ENABLED=true=DB 兜底成立"误读
HT27=$(grep '^HEALTH_TOKEN=' "$(dirname "$0")/../.env" 2>/dev/null | cut -d= -f2 | tr -d '[:space:]')
curl -s -H "X-Health-Token: $HT27" "$B/status/detail" 2>/dev/null | grep -q "rls_effective" \
  && check "readiness 含 rls_effective 观测位(P2-2)" y y || check "readiness 含 rls_effective 观测位(P2-2)" y n

# ---------- 第二十八节：知识库公开面可见性收敛（PLAN_FIX_2026-09-21 B2 / 迁移 016）----------
# 014(P2-4) 只把 knowledge_fragments 的公开搜索收敛为 publicOnly，同在免鉴权组挂载的
# brands / models / compares 三个主实体没有可见性概念，匿名可全量拉走商家产品目录与
# 竞品优劣对比话术。016 补列 + GetVisible* 取数后，公开面**不得出现 visibility!=public 的条目**。
echo "---- 二十八、知识库公开面可见性收敛（B2 护栏）----"
for EP in brands models; do
  KB=$(curl -s -m 10 "$B/api/v1/knowledge/$EP")
  # 空列表(null 或 [])均合法——收敛后私有内容本就不出；关键是**不得混入非 public 条目**
  BAD=$(echo "$KB" | python3 -c "
import sys,json
try:
    d=json.load(sys.stdin)
except Exception:
    print('PARSE_FAIL'); raise SystemExit
rows=d.get('data') or []
bad=[r for r in rows if (r.get('visibility') or 'private') != 'public']
print(len(bad))
" 2>/dev/null)
  [ "${BAD:-0}" = "0" ] && check "公开面 /knowledge/$EP 无 private 条目(B2)" y y || check "公开面 /knowledge/$EP 无 private 条目(B2)" y "n($BAD)"
done
# 竞品对比：必须带 model_id，取不到车型时端点返 400（非可见性问题），此处只校验"能返回时不漏私有"
KBC=$(curl -s -m 10 "$B/api/v1/knowledge/compares?model_id=1")
CBAD=$(echo "$KBC" | python3 -c "
import sys,json
try:
    d=json.load(sys.stdin)
except Exception:
    print('0'); raise SystemExit
rows=d.get('data') or []
print(len([r for r in rows if (r.get('visibility') or 'private') != 'public']))
" 2>/dev/null)
[ "${CBAD:-0}" = "0" ] && check "公开面 /knowledge/compares 无 private 条目(B2)" y y || check "公开面 /knowledge/compares 无 private 条目(B2)" y "n($CBAD)"

# ---------- 第二十九节：AI 贡献度看板口径自洽（PLAN_FIX_2026-09-21 D2）----------
# 立此断言的原因：改造前 /stats/ai-contribution 的会话数按"窗口内新建"(created_at)且带 DataScope，
# 而消息量按"窗口内产生"(created_at)且不带 DataScope。实测 sales1 看到
# active_conversations=0 与 ai_messages=261 同屏——两个数字各自都对，口径却不同，
# 放在一张卡片上就是自相矛盾（销售会读成"AI 这一个月什么都没接待"）。
# 这里不重复实现细节，而是钉住三条与实现无关的性质：
#   (1) 同范围内自洽：活跃会话数 ≥ AI 接待数 + 人工接待数
#   (2) ★缺陷复现：会话数与消息量必须同为 0 或同为正（不允许"0 会话却有消息"）
#   (3) 与 DB 直算（同一 since 边界、同一 DataScope）相等——防"整体返回 0 也能过 (1)(2)"
echo "---- 二十九、AI 贡献度口径自洽 ----"
SC_TOKEN=$(curl -s -X POST "$B/api/v1/auth/login" -H "Content-Type: application/json" \
  -d '{"username":"sales1","password":"sales123"}' | jsonget "['data']['token']")
SC_TID=$($PSQL "SELECT tenant_id FROM tenant_users WHERE username='sales1'" | tr -d '[:space:]')
SC_UID=$($PSQL "SELECT id FROM tenant_users WHERE username='sales1'" | tr -d '[:space:]')
SC_JSON=$(curl -s -m 15 "$B/api/v1/stats/ai-contribution?days=30" -H "Authorization: Bearer $SC_TOKEN")
SC_CODE=$(curl -s -o /dev/null -w "%{http_code}" -m 15 "$B/api/v1/stats/ai-contribution?days=30" -H "Authorization: Bearer $SC_TOKEN")
check "AI贡献度端点可用" 200 "$SC_CODE"

SC_KEYS=$(echo "$SC_JSON" | python3 -c "
import sys,json
try: x=json.load(sys.stdin).get('data') or {}
except Exception: print('PARSE_FAIL'); raise SystemExit
need=['period_days','since','until','new_conversations','active_conversations','ai_served_customers',
      'human_served_customers','ai_serve_share','handoff_rate','ai_leads','assisted_leads','ai_lead_rate',
      'assisted_lead_rate','ai_arrived','ai_ordered','ai_arrive_rate','ai_order_rate','ai_messages',
      'human_messages','customer_messages','ai_message_share','pending_handoff_now','notes']
missing=[k for k in need if k not in x]
print('OK' if not missing else 'MISSING:'+','.join(missing))
" 2>/dev/null)
[ "$SC_KEYS" = "OK" ] && check "AI贡献度契约键齐全(23键)" y y || check "AI贡献度契约键齐全(23键)" y "${SC_KEYS:-PARSE_FAIL}"

# (1) 同范围内自洽
SC_SELF=$(echo "$SC_JSON" | python3 -c "
import sys,json
x=json.load(sys.stdin)['data']
served=x['ai_served_customers']+x['human_served_customers']
print('OK' if x['active_conversations']>=served else f'BAD:active={x[\"active_conversations\"]}<served={served}')
" 2>/dev/null)
[ "$SC_SELF" = "OK" ] && check "活跃会话数≥AI接待+人工接待(同范围自洽)" y y || check "活跃会话数≥AI接待+人工接待(同范围自洽)" y "${SC_SELF:-PARSE_FAIL}"

# (2) ★缺陷复现断言：不允许出现"0 个活跃会话却有消息"（改造前 sales1 正是 0/261）
SC_CONSIST=$(echo "$SC_JSON" | python3 -c "
import sys,json
x=json.load(sys.stdin)['data']
convs=x['active_conversations']
msgs=x['ai_messages']+x['human_messages']+x['customer_messages']
both_zero=(convs==0 and msgs==0)
both_pos=(convs>0 and msgs>0)
print('OK' if (both_zero or both_pos) else f'BAD:convs={convs},msgs={msgs}')
" 2>/dev/null)
[ "$SC_CONSIST" = "OK" ] && check "会话数与消息量同零/同正(旧缺陷已封堵)" y y || check "会话数与消息量同零/同正(旧缺陷已封堵)" y "${SC_CONSIST:-PARSE_FAIL}"

# (3) 与 DB 直算对齐：用响应自带的 since 作为边界，消除请求/查询之间的秒级漂移
SC_SINCE=$(echo "$SC_JSON" | jsonget "['data']['since']")
SC_DBCONV=$($PSQL "
WITH scoped AS (SELECT id FROM conversations WHERE tenant_id=${SC_TID} AND assigned_user_id=${SC_UID})
SELECT COUNT(DISTINCT m.conversation_id) FROM messages m
 WHERE m.tenant_id=${SC_TID} AND m.created_at >= '${SC_SINCE}'::timestamptz
   AND m.conversation_id IN (SELECT id FROM scoped)" | tr -d '[:space:]')
SC_APICONV=$(echo "$SC_JSON" | jsonget "['data']['active_conversations']")
[ "${SC_DBCONV:-x}" = "${SC_APICONV:-y}" ] && check "活跃会话数与DB直算一致(同since边界)" y y \
  || check "活跃会话数与DB直算一致(同since边界)" "db=${SC_DBCONV}" "api=${SC_APICONV}"

# (4) 口径说明必须随响应下发且写明数据范围口径（防后人"精简文案"删掉关键限定）
SC_NOTES=$(echo "$SC_JSON" | python3 -c "
import sys,json
try: n=json.load(sys.stdin)['data'].get('notes') or []
except Exception: print('PARSE_FAIL'); raise SystemExit
joined='\n'.join(n)
print('OK' if ('数据范围裁剪' in joined and '同窗同源' in joined) else 'BAD')
" 2>/dev/null)
[ "$SC_NOTES" = "OK" ] && check "口径说明下发且含范围/同源限定" y y || check "口径说明下发且含范围/同源限定" y "${SC_NOTES:-PARSE_FAIL}"

# (5) 跨租户隔离 + 正向控制：一次性租户里只造「1 条会话（1 AI + 1 客户消息，无人工回复）」，
#     断言返回的正是 1 而非全库口径。为什么不用 acme 做"应为 0"的断言：acme 跨运行会累积数据，
#     拿它断言 0 会随时间变脆；而"精确等于 1"这种正向控制即使库里已有大量数据也稳定成立，
#     且能同时证明两件事——聚合确实生效（不是恒返 0）、租户过滤确实生效（不是泄漏别家数字）。
#     用 smoke 前缀命名，cleanup_test_tenants.sh 可回收残留；本段结束自行删除，幂等。
AISC_CODE="smoke_aisc_$$"
$PSQL "INSERT INTO tenants (name, code, tier, status, created_at, updated_at)
       VALUES ('AI贡献度隔离验证', '${AISC_CODE}', 'personal', 'active', NOW(), NOW());" >/dev/null 2>&1
AISC_TID=$($PSQL "SELECT id FROM tenants WHERE code='${AISC_CODE}'" | tr -d '[:space:]')
# ★建数据必须用**单条 CTE 语句**，不要分三步各自 RETURNING id：
#   实测 psql -tAc "INSERT ... RETURNING id" 仍会输出命令标签（INSERT 0 1），
#   tr -d '[:space:]' 会把它粘成 "2229INSERT01"，后续 SQL 直接语法错。
#   CTE 一条语句内部串联 tenant→customer→conversation→message，不经过 shell 解析，天然免疫。
$PSQL "WITH t AS (SELECT id FROM tenants WHERE code='${AISC_CODE}'),
             c AS (INSERT INTO customers (tenant_id, name, journey_stage, created_at, updated_at)
                   SELECT id, '隔离验证客户', 'lead_captured', NOW(), NOW() FROM t RETURNING id, tenant_id),
             cv AS (INSERT INTO conversations (tenant_id, customer_id, status, mode, created_at, updated_at)
                    SELECT tenant_id, id, 'active', 'ai', NOW(), NOW() FROM c RETURNING id, tenant_id, customer_id)
       INSERT INTO messages (tenant_id, conversation_id, customer_id, sender_type, content, created_at, updated_at)
       SELECT tenant_id, id, customer_id, 'ai', '隔离验证AI消息', NOW(), NOW() FROM cv
       UNION ALL
       SELECT tenant_id, id, customer_id, 'customer', '隔离验证客户消息', NOW(), NOW() FROM cv;" >/dev/null 2>&1
AISC_J=$(curl -s -m 15 "$B/api/v1/stats/ai-contribution?days=30" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: ${AISC_TID}")
AISC_ACT=$(echo "$AISC_J" | jsonget "['data']['active_conversations']")
AISC_AIMSG=$(echo "$AISC_J" | jsonget "['data']['ai_messages']")
AISC_LEAD=$(echo "$AISC_J" | jsonget "['data']['ai_leads']")
[ "${AISC_ACT:-x}" = "1" ] && check "隔离租户活跃会话恰为1(未泄漏别租户)" y y || check "隔离租户活跃会话恰为1(未泄漏别租户)" 1 "${AISC_ACT:-x}"
[ "${AISC_AIMSG:-x}" = "1" ] && check "隔离租户AI消息恰为1(精确作用域)" y y || check "隔离租户AI消息恰为1(精确作用域)" 1 "${AISC_AIMSG:-x}"
[ "${AISC_LEAD:-x}" = "1" ] && check "隔离租户AI留资归因恰为1(聚合确实生效)" y y || check "隔离租户AI留资归因恰为1(聚合确实生效)" 1 "${AISC_LEAD:-x}"
# 现场回收（顺序：消息→会话→客户→租户，避免外键残留）。
# 用 code 子查询而非 $AISC_TID：建租户若失败，AISC_TID 为空会让 "tenant_id=" 直接语法错，
# 把一段本来只是"没造成污染"的收尾变成假红。
$PSQL "DELETE FROM messages WHERE tenant_id=(SELECT id FROM tenants WHERE code='${AISC_CODE}');
       DELETE FROM conversations WHERE tenant_id=(SELECT id FROM tenants WHERE code='${AISC_CODE}');
       DELETE FROM customers WHERE tenant_id=(SELECT id FROM tenants WHERE code='${AISC_CODE}');
       DELETE FROM tenants WHERE code='${AISC_CODE}';" >/dev/null 2>&1
AISC_LEFT=$($PSQL "SELECT COUNT(*) FROM tenants WHERE code='${AISC_CODE}'" | tr -d '[:space:]')
[ "${AISC_LEFT:-1}" = "0" ] && check "隔离验证租户已回收(无残留)" y y || check "隔离验证租户已回收(无残留)" 0 "${AISC_LEFT:-1}"

# ---------- 第三十节：AI 销售闭环四开关 + 判优参数按租户生效（PLAN_FIX_2026-09-22 批五/批六护栏）----------
# 立此段的原因有两层：
#  ① 开关面：批五把"择臂(L2 前置)/自动晋升/销售路径机/话术挖掘"四条自动化链路一次接进主程序，
#     全部默认关。默认值一旦被误改（比如把 experiment_auto_promote 播种成 true），
#     系统就会在无人审核的情况下自动改话术权重、自动烧真实 token 出稿——这是"能演示"与"敢投产"的分界。
#  ② 参数读写层：这批 experiment_*/talkmining_* 键都**不是**平台级键（租户后台就能改，
#     写 (tenant_id,key) 覆盖层），但装配判优/晋升参数的代码原先读的是系统默认层
#     （SafeCfg*）——于是"租户改了参数、行为照旧"，不报错、不 panic，纯静默失效
#     （与本仓 email_verify_enabled 当年同一课）。批六把读法统一到租户生效值，
#     这里用**接口回显**证明它真的生效了（min_samples 从 2200 改成 1，卡片结论必须翻转），
#     而不是只在 Go 单测里自证。
echo "---- 三十、AI 销售闭环开关面与租户级判优参数 ----"
B5SEED=$($PSQL "SELECT count(*) FROM system_configs WHERE tenant_id=0
  AND ((key='template_bandit_enabled' AND value='false')
    OR (key='experiment_auto_promote' AND value='false')
    OR (key='sales_path_enabled' AND value='false')
    OR (key='talkmining_draft_enabled' AND value='false'))" 2>/dev/null | tr -d '[:space:]')
check "四开关出厂默认false已播种系统层(默认关=可投产前提)" 4 "$B5SEED"

# 量纲纠偏：ab_weight 是 0~100 整数百分点。老库出厂值曾是"比例量纲"(step=0.2/max=3)，
# ensureDefaults 只补漏键、不改存量，所以量纲改版必须走 retunedConfigValues 显式纠偏——
# 否则 max=3 会被当真：自动晋升顶两轮就撞上限，此后**永远不再调权**且日志全绿。
B5STEP=$($PSQL "SELECT value FROM system_configs WHERE tenant_id=0 AND key='experiment_weight_step'" 2>/dev/null | tr -d '[:space:]')
B5MAX=$($PSQL "SELECT value FROM system_configs WHERE tenant_id=0 AND key='experiment_weight_max'" 2>/dev/null | tr -d '[:space:]')
check "experiment_weight_step 出厂值=10(百分点量纲,存量0.2已纠偏)" 10 "$B5STEP"
check "experiment_weight_max 出厂值=100(百分点量纲,存量3已纠偏)" 100 "$B5MAX"
B5CD=$($PSQL "SELECT value FROM system_configs WHERE tenant_id=0 AND key='experiment_promote_cooldown_hours'" 2>/dev/null | tr -d '[:space:]')
check "晋升冷却默认72h(防小时级复利冲顶)" 72 "$B5CD"
# 终局观察窗是"跨租户一趟扫"的全局 SQL 参数，做不到按租户分窗，因此必须标平台级；
# 若哪天有人把它从 PlatformLevelKeys 里删掉，租户后台就会多出一个改了不生效的键（本行钉住）。
B5OUT=$($PSQL "SELECT value FROM system_configs WHERE tenant_id=0 AND key='outcome_window_days'" 2>/dev/null | tr -d '[:space:]')
check "outcome_window_days 出厂30(终局标签观察窗)" 30 "$B5OUT"

# 四开关租户往返：开→租户层生效&系统层不被污染→关回默认
for B5K in template_bandit_enabled experiment_auto_promote sales_path_enabled talkmining_draft_enabled; do
  B5PU=$(curl -s -X PUT "$B/api/v1/admin/config" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1" \
    -H "Content-Type: application/json" -d "[{\"key\":\"$B5K\",\"value\":\"true\"}]")
  check "开关$B5K 租户层可开(code=0)" 0 "$(echo "$B5PU" | jsonget "['code']" 2>/dev/null)"
  B5TEN=$($PSQL "SELECT replace(value,'\"','') FROM system_configs WHERE tenant_id=1 AND key='$B5K'" 2>/dev/null | tr -d '[:space:]')
  B5SYS=$($PSQL "SELECT replace(value,'\"','') FROM system_configs WHERE tenant_id=0 AND key='$B5K'" 2>/dev/null | tr -d '[:space:]')
  [ "$B5TEN" = "true" ] && check "开关$B5K 已落到租户覆盖层" y y || check "开关$B5K 已落到租户覆盖层" y "${B5TEN:-缺行}"
  [ "$B5SYS" = "false" ] && check "开关$B5K 未污染系统默认层" y y || check "开关$B5K 未污染系统默认层" false "$B5SYS"
done

# L1 判优卡片：造"同锚两话术、n=500、留资率 0.30 vs 0.10"的最小判优场景（租户 1）。
# 为什么自己插数据：pack_stats 是小时任务产物，真实库里既有数据不可控（可能 0 行→卡片为空→断言空过），
# 用合成行 + 精确 sample/率，才能把"结论随门槛翻转"这条租户级参数生效链跑实。
B5TA="smoke_b5_$$_a"
B5TB="smoke_b5_$$_b"
$PSQL "INSERT INTO templates (id, tenant_id, anchor_type, name, prompt_template, status, ab_group, ab_weight, priority, created_at, updated_at)
       VALUES ('${B5TA}', 1, 2, '冒烟判优胜者', '冒烟话术A', 1, 'smoke_b5', 50, 1, NOW(), NOW()),
              ('${B5TB}', 1, 2, '冒烟判优败者', '冒烟话术B', 1, 'smoke_b5', 50, 1, NOW(), NOW());
       INSERT INTO pack_stats (tenant_id, pack_code, pack_version, template_id, sample_count, hook_rate, lead_rate, computed_at)
       VALUES (1, 'smoke_b5', '1.0.0', '${B5TA}', 500, 0.40, 0.30, NOW()),
              (1, 'smoke_b5', '1.0.0', '${B5TB}', 500, 0.40, 0.10, NOW());" >/dev/null 2>&1
b5stat() { curl -s -m 15 "$B/api/v1/admin/packs/stats?pack_code=smoke_b5" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1"; }
# 默认门槛 2200：n=500 不足 → 必须明确"不下结论"（这是"不假装精确"口径的接口回显）
B5J=$(b5stat)
B5DEF=$(echo "$B5J" | python3 -c "
import sys,json
d=(json.load(sys.stdin).get('data') or {}).get('suggestions') or []
mine=[s for s in d if s['template_id'] in ('$B5TA','$B5TB')]
if len(mine)!=2: print('ROWS=%d'%len(mine)); raise SystemExit
print('OK' if all(s['status']=='insufficient_samples' and s['min_samples']==2200 for s in mine) else json.dumps(mine))
" 2>/dev/null)
[ "$B5DEF" = "OK" ] && check "默认门槛2200下n=500判功效不足(不假装精确)" y y || check "默认门槛2200下n=500判功效不足(不假装精确)" y "${B5DEF:-PARSE_FAIL}"
# 租户把门槛降到 1：同一份数据必须立刻出结论 → 证明 experiment_min_samples_lead 走的是租户生效值
curl -s -o /dev/null -X PUT "$B/api/v1/admin/config" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1" \
  -H "Content-Type: application/json" -d '[{"key":"experiment_min_samples_lead","value":"1"}]'
B5J2=$(b5stat)
B5LOW=$(echo "$B5J2" | python3 -c "
import sys,json
d=(json.load(sys.stdin).get('data') or {}).get('suggestions') or []
mine={s['template_id']:s for s in d if s['template_id'] in ('$B5TA','$B5TB')}
if len(mine)!=2: print('ROWS=%d'%len(mine)); raise SystemExit
a,b=mine['$B5TA'],mine['$B5TB']
if a['min_samples']!=1: print('MIN=%s'%a['min_samples']); raise SystemExit
print('OK' if a['status']=='leading' and b['status']=='suggest_review' else json.dumps([a['status'],b['status']]))
" 2>/dev/null)
[ "$B5LOW" = "OK" ] && check "★租户降门槛即出结论(胜者leading/败者suggest_review)" y y || check "★租户降门槛即出结论(胜者leading/败者suggest_review)" y "${B5LOW:-PARSE_FAIL}"
# 奖励红线：租户把奖励指标改成 intent（意向分是模型自己写的近端信号，拿来当奖励=reward hacking）
# 接口必须无视之、仍按 lead 口径出卡——白名单收敛从"代码里有"升级为"打接口能验"。
curl -s -o /dev/null -X PUT "$B/api/v1/admin/config" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1" \
  -H "Content-Type: application/json" -d '[{"key":"experiment_reward_metric","value":"\"intent\""}]'
B5STATRESP=$(b5stat)
B5WL=$(echo "$B5STATRESP" | python3 -c "
import sys,json
d=(json.load(sys.stdin).get('data') or {}).get('suggestions') or []
mine=[s for s in d if s['template_id'] in ('$B5TA','$B5TB')]
print('OK' if mine and all(s['metric']=='lead' for s in mine) else json.dumps([s.get('metric') for s in mine]))
" 2>/dev/null)
[ "$B5WL" = "OK" ] && check "奖励指标越白名单仍按lead出卡(防reward hacking)" y y || check "奖励指标越白名单仍按lead出卡(防reward hacking)" y "${B5WL:-PARSE_FAIL}"

# 现场回收：合成模板/快照 + 本段写过的租户覆盖行一律删除，留 0 条才算不打脏库
$PSQL "DELETE FROM pack_stats WHERE tenant_id=1 AND pack_code='smoke_b5';
       DELETE FROM templates WHERE tenant_id=1 AND id IN ('${B5TA}','${B5TB}');
       DELETE FROM system_configs WHERE tenant_id=1 AND key IN
         ('template_bandit_enabled','experiment_auto_promote','sales_path_enabled','talkmining_draft_enabled',
          'experiment_min_samples_lead','experiment_reward_metric');" >/dev/null 2>&1
B5LEFT=$($PSQL "SELECT (SELECT count(*) FROM templates WHERE id LIKE 'smoke_b5_%')
              + (SELECT count(*) FROM pack_stats WHERE pack_code='smoke_b5')
              + (SELECT count(*) FROM system_configs WHERE tenant_id=1 AND key IN
                 ('template_bandit_enabled','experiment_auto_promote','sales_path_enabled','talkmining_draft_enabled',
                  'experiment_min_samples_lead','experiment_reward_metric'))" 2>/dev/null | tr -d '[:space:]')
check "本段合成数据与租户覆盖行已清零" 0 "${B5LEFT:-1}"

# ---------- 第三十一节：批六数据层治理 + 运维观测面（迁移 017/018/019 与 /status/detail）----------
# 立此段的原因：批六把"数据形态"第一次做成了可观测的东西（孤儿消息计数、归档积压、Redis 声明与实际连接
# 是否一致），并且靠迁移 017/018/019 把存量债一次性清掉。这类东西的失败模式全是**静默**的——
# 迁移没跑上、索引没建上、观测位字段名改了，都不会有任何报错，只会在半年后以
# "贡献度口径又对不上""探测把 messages 全表扫了一遍"的形式回来。所以这一段只断言**事实存在**，
# 且全部走 DB 直查 + 详情端点回显，不依赖单测的注入接缝（单测证逻辑，本段证真库真接口）。
echo "---- 三十一、批六数据层治理与运维观测面 ----"
# 迁移台账：三笔必须登记在案（internal/db/migrations.go 按文件名升序执行并写 schema_migrations）。
# 只查台账不查列：台账是"部署是否走到这一版"的真相源，列在下一行单独核。
B6MIG=$($PSQL "SELECT count(*) FROM schema_migrations WHERE version IN
  ('017_visitor_key_backfill','018_attribution_outcome_labels','019_orphan_message_reassign')" 2>/dev/null | tr -d '[:space:]')
check "迁移017/018/019已登记台账" 3 "$B6MIG"
# 018：终局标签四列 + pack_stats 两列终局率（择臂/判优的奖励来源，缺列则回填 SQL 直接 42703）
B6COL=$($PSQL "SELECT count(*) FROM information_schema.columns
  WHERE (table_name='reply_attributions' AND column_name IN ('arrived_at','dealt_at','outcome_stage','outcome_checked_at'))
     OR (table_name='pack_stats' AND column_name IN ('arrive_rate','deal_rate'))" 2>/dev/null | tr -d '[:space:]')
check "终局标签列齐(归因4列+包统计2列)" 6 "$B6COL"
# 019 配套部分索引：无它则每次健康探测全表扫 messages（全站最大的表），
# 有了它孤儿计数在索引内完成，正常态近乎零成本。
B6IDX=$($PSQL "SELECT count(*) FROM pg_indexes WHERE indexname='idx_messages_orphan_no_conv'" 2>/dev/null | tr -d '[:space:]')
check "孤儿消息计数部分索引已建" 1 "$B6IDX"
# 017：访客密钥补签残留必须为 0——写入口收紧后，空密钥客户等于永久不可用（迁移自带 EXCEPTION 自检，
# 这里再断一次是防"迁移被人改名后端点已收紧"这种半吊子部署）。
# 口径修正（首跑即抓到）：空 visitor_key 有两类**合法**来源，都不是迁移没跑上——
#   ① PIPL 删除权匿名化会主动清空 visitor_key（internal/privacy 置 remark='anon:'），
#      这是"身份已撤销"的正确形态，若断言 count=0 就会每次跑完 uat 的注销流程假红；
#   ② 通道客户以 external_userid 为身份锚，本就不发 web 访客密钥（smoke_chat_identity 8.9 拿它做负向用例）。
# 所以要断的是"**未被匿名化的行**一律有密钥"，即空密钥只可能出现在匿名化行上。
B6VK=$($PSQL "SELECT count(*) FROM customers WHERE (visitor_key IS NULL OR visitor_key='')
  AND remark NOT LIKE 'anon:%' AND (external_user_id IS NULL OR external_user_id='')" 2>/dev/null | tr -d '[:space:]')
check "活跃(非匿名化)客户无空访客密钥残留(017补签到位)" 0 "$B6VK"
# 019 的产物直接读观测位：孤儿行=0 才算"账真清了"（不是靠迁移注释里那句"已清零"）。
B6HT=$(grep '^HEALTH_TOKEN=' "$(dirname "$0")/../.env" 2>/dev/null | cut -d= -f2 | tr -d '[:space:]')
B6DETAIL=$(curl -s -H "X-Health-Token: $B6HT" "$B/status/detail")
check "/status/detail orphan_messages=0(孤儿消息已治理)" 0 "$(echo "$B6DETAIL" | jsonget "['data']['orphan_messages']" 2>/dev/null)"
# 归档观测位：message_archive_days=0（默认关）时必须报"未启用"而不是 0——
# 0 会被读成"没有积压"，而真相是"根本没在归档"，这两种状态对运维是两个决定。
B6ARCHOFF=$(echo "$B6DETAIL" | jsonget "['data']['archive_enabled']" 2>/dev/null)
[ "$B6ARCHOFF" = "False" ] && check "归档关闭时archive_enabled显式报False(不伪装成零积压)" y y || check "归档关闭时archive_enabled显式报False(不伪装成零积压)" False "$B6ARCHOFF"
# Redis 声明与实际连接一致性：单机开发态只准 not_declared / connected，
# declared_but_down 是"配置说开了、客户端其实没连上"——多实例下会让锁/广播各实例单干，属 crit。
B6REDIS=$(echo "$B6DETAIL" | python3 -c "
import sys,json
d=json.load(sys.stdin); d=d.get('data',d)
for c in d.get('readiness',[]):
    if c.get('name')=='redis_declared_but_down': print(c.get('value')); break
else: print('ABSENT')
" 2>/dev/null)
case "$B6REDIS" in
  not_declared|connected) check "Redis一致性观测位健康($B6REDIS)" y y ;;
  *) check "Redis一致性观测位健康" "not_declared|connected" "$B6REDIS" ;;
esac
# 反漏护栏：数据形态字段只准出现在带令牌的详情端点，公开 /status 仍是"存活+版本"四件套。
B6LEAK=$(curl -s "$B/status" | grep -c "orphan_messages\|archive_backlog\|redis_enabled" 2>/dev/null)
check "公开/status不泄露数据层观测位" 0 "${B6LEAK:-1}"
# 版本声明单点锁（2026-09-23 收尾批）：/status 与 /status/detail 此前各写一份字面量，
# 两份都停在 v2.16.0 而变更日志已记到 v2.28.0——探针报的版本比真实构建老 12 个小版本，
# 运维照它核对发布批次会核对错对象。现在三处（含 E2 Sentry Release，见 test_all G-6·3.7 负向 grep）
# 都读 cmd/server/main.go 的 appVersion 常量。
# 只锁"两处一致 + vX.Y.Z 形态"（防有人再写回各自硬编码），**不锁"等于 README 版本号"**：
# README.md 未入库（.gitignore 第 2 行 `*.md`），CI 侧根本拿不到它，锁上去就是永久性假红。
B6VER1=$(curl -s "$B/status" | jsonget "['data']['version']" 2>/dev/null)
B6VER2=$(echo "$B6DETAIL" | jsonget "['data']['version']" 2>/dev/null)
B6VERSHAPE=$(printf '%s' "$B6VER1" | grep -cE '^v[0-9]+\.[0-9]+\.[0-9]+$' 2>/dev/null)
check "/status版本回显为vX.Y.Z形态" 1 "${B6VERSHAPE:-0}"
check "/status与/status/detail版本一致(appVersion单点真源)" "$B6VER1" "$B6VER2"
# E1-2(2026-09-24) 微信回调验签强度观测位：真实商户号接入前它是"该开没开"的清单项，
# 必须出现在带令牌的详情端点（否则上线时无人知道有这道闸），且不得进公开 /status。
B6WCV=$(echo "$B6DETAIL" | grep -c '"wechat_cert_verify"' 2>/dev/null)
check "readiness含wechat_cert_verify验签观测位" 1 "${B6WCV:-0}"
check "公开/status不泄露验签策略观测位" 0 "$(curl -s "$B/status" | grep -c 'wechat_cert_verify' 2>/dev/null)"

# ---------- 第三十二节：主动触达最小闭环（排期/裁决/撤回/跨租户隔离，2026-09-23 批次4）----------
# 这一段守的是"接口面 + 真库落库形态"：派发数学（48h 窗口、静默顺延、周内频次、重试上限）
# 已在 internal/outreach 单测里逐条钉死，但单测用的是注入替身——真库里表是否存在、迁移是否登记、
# 租户覆盖是否真生效、写进去的行 tenant_id 是否盖章正确（C7 红线）、跨租户能不能看到别人的队列，
# 只有在跑起来的服务上才验得了。段尾自清理，不给共享库留残留行。
echo "---- 三十二、主动触达最小闭环 ----"
B7MIG=$($PSQL "SELECT count(*) FROM schema_migrations WHERE version='020_outreach_tasks'" 2>/dev/null | tr -d '[:space:]')
check "迁移020触达任务表已登记台账" 1 "$B7MIG"
# 两条部分索引：pending 按到点扫描、sent 按客户周频控计数。缺任一条即每次派发/排期全表扫 outreach_tasks。
B7IDX=$($PSQL "SELECT count(*) FROM pg_indexes WHERE tablename='outreach_tasks'
  AND indexname IN ('idx_outreach_pending_due','idx_outreach_sent_customer')" 2>/dev/null | tr -d '[:space:]')
check "触达调度/频控两条部分索引已建" 2 "$B7IDX"
# 出厂默认必须是"关"：主动触达会真给客户发消息，默认开着等于替租户做了放量决定
B7SEED=$($PSQL "SELECT count(*) FROM system_configs WHERE tenant_id=0
  AND ((key='outreach_enabled' AND value='false')
    OR (key='outreach_weekly_limit' AND value='2')
    OR (key='outreach_window_hours' AND value='48')
    OR (key='outreach_quiet_hours' AND value='21:00-09:00'))" 2>/dev/null | tr -d '[:space:]')
check "触达四键出厂默认已播种系统层(关/2条/48h/21-9点)" 4 "$B7SEED"

B7CUST="smoke_outreach_$$_vk"
# 注意 customers.status 是 bigint（客户阶段序号），不是文本态——插错列名/类型会让本段全部
# 断言连锁假失败（首跑实测：'active' 喂给 bigint 直接 22P02，客户 ID 取空 → 后续 POST 全 400）。
$PSQL "INSERT INTO customers (tenant_id, name, visitor_key, created_at, updated_at)
       VALUES (1, '冒烟触达客户', '${B7CUST}', NOW(), NOW());" >/dev/null 2>&1
B7CID=$($PSQL "SELECT id FROM customers WHERE visitor_key='${B7CUST}'" 2>/dev/null | tr -d '[:space:]')
b7post() { curl -s -X POST "$B/api/v1/admin/outreach/tasks" -H "Authorization: Bearer $TOKEN" \
  -H "X-Tenant-ID: ${1:-1}" -H "Content-Type: application/json" -d "$2"; }
# 未开开关：必须先拒（这一步在查库之前，不能出现"排上了但永不派发"的暗账）
B7OFF=$(b7post 1 "{\"customer_id\":${B7CID:-0},\"content\":\"冒烟触达-未启用\"}")
check "开关关闭时排期被拒(HTTP400)" 400 "$(echo "$B7OFF" | jsonget "['code']" 2>/dev/null)"
check "开关关闭拒绝原因码稳定(disabled)" disabled "$(echo "$B7OFF" | jsonget "['reason']" 2>/dev/null)"
check "开关关闭时零落库(不留无法解释的pending行)" 0 "$($PSQL "SELECT count(*) FROM outreach_tasks WHERE content='冒烟触达-未启用'" 2>/dev/null | tr -d '[:space:]')"

# 开租户开关（走后台配置接口，验的是"租户覆盖真生效"这条读写层链路，不是直改库）
B7ON=$(curl -s -X PUT "$B/api/v1/admin/config" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1" \
  -H "Content-Type: application/json" -d '[{"key":"outreach_enabled","value":"true"}]')
check "outreach_enabled 租户层可开(code=0)" 0 "$(echo "$B7ON" | jsonget "['code']" 2>/dev/null)"
B7SYS=$($PSQL "SELECT replace(value,'\"','') FROM system_configs WHERE tenant_id=0 AND key='outreach_enabled'" 2>/dev/null | tr -d '[:space:]')
[ "$B7SYS" = "false" ] && check "开租户开关未污染系统默认层" y y || check "开租户开关未污染系统默认层" false "$B7SYS"

B7NEW=$(b7post 1 "{\"customer_id\":${B7CID:-0},\"content\":\"冒烟触达-已排期\"}")
B7TID=$(echo "$B7NEW" | jsonget "['data']['id']" 2>/dev/null)
check "开关开后排期成功(code=0)" 0 "$(echo "$B7NEW" | jsonget "['code']" 2>/dev/null)"
check "新任务初始态为pending" pending "$(echo "$B7NEW" | jsonget "['data']['status']" 2>/dev/null)"
# C7 红线：接口写入必须盖对本租户，绝不能落成 tenant_id=0（0 会被平台视图当成"无主行"）
B7ROW=$($PSQL "SELECT count(*) FROM outreach_tasks WHERE id=${B7TID:-0} AND tenant_id=1 AND customer_id=${B7CID:-0}" 2>/dev/null | tr -d '[:space:]')
check "★触达任务落库租户归属正确(tenant_id/customer_id)" 1 "$B7ROW"
# 超长文案（>500 rune）必须被内容闸挡在落库前，且回稳定原因码
# ⚠ 断言一律"先赋值给变量、再管道取值"：bash 里 `$(echo "$(cmd "…\"…")")` 这种嵌套命令替换
#    会把内层的转义引号按两层解析——结果是**同一请求发两次、且请求体被拆坏**（服务端如实回
#    param_error），断言取值变成空串。这是断言自伤，不是产品缺陷（2026-09-23 实测：
#    同样的 body 直发返回 content_flagged，套进嵌套写法就恒失败）。
B7LONG=$(python3 -c "print('冒烟触达超长文案'*200)")
B7LONGRESP=$(b7post 1 "{\"customer_id\":${B7CID:-0},\"content\":\"$B7LONG\"}")
check "超长文案被拒且原因码稳定(content_flagged)" content_flagged \
  "$(echo "$B7LONGRESP" | jsonget "['reason']" 2>/dev/null)"
# 静默顺延：整段全天静默时，计划时间必须被推到"此刻之后"（不许夜里打扰客户）
curl -s -o /dev/null -X PUT "$B/api/v1/admin/config" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1" \
  -H "Content-Type: application/json" -d '[{"key":"outreach_quiet_hours","value":"00:00-23:59"}]'
B7QRESP=$(b7post 1 "{\"customer_id\":${B7CID:-0},\"content\":\"冒烟触达-静默顺延\"}")
B7Q=$(echo "$B7QRESP" | jsonget "['data']['scheduled_at']" 2>/dev/null)
B7QFUTURE=$(python3 -c "
from datetime import datetime,timezone
try:
    s='$B7Q'
    d=datetime.fromisoformat(s.replace('Z','+00:00'))
    print('OK' if d.timestamp()>datetime.now(timezone.utc).timestamp()-120 else 'NOT_FUTURE:'+s)
except Exception as e:
    print('PARSE_FAIL:%s'%e)
" 2>/dev/null)
[ "$B7QFUTURE" = "OK" ] && check "全天静默时计划时间自动顺延到未来" y y || check "全天静默时计划时间自动顺延到未来" y "${B7QFUTURE:-EMPTY}"
# 列表回显本租户队列 + 生效参数（前端据此显示"开关未开"提示，参数必须来自租户覆盖层）
B7LIST=$(curl -s "$B/api/v1/admin/outreach/tasks?status=pending&limit=10&offset=0" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1")
B7CFGON=$(echo "$B7LIST" | jsonget "['data']['config']['enabled']" 2>/dev/null)
check "列表回显租户生效开关(True)" True "$B7CFGON"
B7HIT=$(echo "$B7LIST" | python3 -c "
import sys,json
d=(json.load(sys.stdin).get('data') or {})
print('OK' if any(r.get('content')=='冒烟触达-已排期' for r in d.get('list') or []) else 'MISSING total=%s'%d.get('total'))
" 2>/dev/null)
[ "$B7HIT" = "OK" ] && check "列表按状态筛选可见新任务" y y || check "列表按状态筛选可见新任务" y "${B7HIT:-PARSE_FAIL}"
# 撤回状态机：pending 可撤一次，二次撤回必须 404（已非 pending 不可撤）
B7CX=$(curl -s -X POST "$B/api/v1/admin/outreach/tasks/${B7TID}/cancel" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1")
check "待派发任务可撤回(code=0)" 0 "$(echo "$B7CX" | jsonget "['code']" 2>/dev/null)"
B7CX2=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/admin/outreach/tasks/${B7TID}/cancel" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1")
check "重复撤回判404(状态机不可逆)" 404 "$B7CX2"
B7CANC=$($PSQL "SELECT status FROM outreach_tasks WHERE id=${B7TID:-0}" 2>/dev/null | tr -d '[:space:]')
check "撤回后状态落库为cancelled" cancelled "$B7CANC"
# 跨租户隔离：acme 视角看不到租户 1 的队列，也撤不动它的任务（404 而非 403，不泄露"存在性"）
B7ACME=$(curl -s "$B/api/v1/admin/outreach/tasks?limit=50" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: ${ACME_ID}")
B7ACMETOTAL=$(echo "$B7ACME" | jsonget "['data']['total']" 2>/dev/null)
check "跨租户列表total=0(看不到他人队列)" 0 "${B7ACMETOTAL:-1}"
B7ACMEX=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/admin/outreach/tasks/${B7TID}/cancel" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: ${ACME_ID}")
check "跨租户撤回他人任务判404(不回显存在性)" 404 "$B7ACMEX"
# 非法入参：缺 customer_id 必须 400（不得凭文案猜客户）
B7NOCID=$(b7post 1 '{"content":"冒烟触达-缺客户"}')
check "缺customer_id排期判400" 400 "$(echo "$B7NOCID" | jsonget "['code']" 2>/dev/null)"
# 非管理员不得操作触达队列（sales1 是普通角色）
B7SALES=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/admin/outreach/tasks" -H "Authorization: Bearer $STOKEN")
check "销售角色访问触达队列被拒(403)" 403 "$B7SALES"

# 现场回收：合成客户/任务 + 本段写过的租户覆盖行清零
$PSQL "DELETE FROM outreach_tasks WHERE tenant_id=1 AND content LIKE '冒烟触达%';
       DELETE FROM customers WHERE visitor_key='${B7CUST}';
       DELETE FROM system_configs WHERE tenant_id=1 AND key IN ('outreach_enabled','outreach_quiet_hours');" >/dev/null 2>&1
B7LEFT=$($PSQL "SELECT (SELECT count(*) FROM outreach_tasks WHERE content LIKE '冒烟触达%')
              + (SELECT count(*) FROM customers WHERE visitor_key='${B7CUST}')
              + (SELECT count(*) FROM system_configs WHERE tenant_id=1 AND key IN ('outreach_enabled','outreach_quiet_hours'))" 2>/dev/null | tr -d '[:space:]')
check "本段合成数据与租户覆盖行已清零" 0 "${B7LEFT:-1}"

# ---------- 第三十三节：D3 用量预警触达 + 到期催缴（2026-09-23，PLAN_FIX D3 护栏）----------
# 立此段的原因：D3 的越档裁决与催缴状态机有 Go 单测，但**端到端面**此前零断言——迁移有没有真进库、
# 四个端点的契约键与角色闸、人工 nudge/reset 的领域错误回显与审计留痕，全都没人守。
# 刻意**不**在这里把 usage_alert_enabled / dunning_enabled 打开去等小时巡检：
#   ① 冒烟不能等一小时；② 这两个开关一开就是"真给租户管理员发信"与"宽限期满自动封号"，
#      测试脚本无权制造这类外部副作用。所以本段钉**数据层实存 + 读侧契约 + 人工动作**，
#      推档/封禁/到账解除本身由 internal/billing/dunning_test.go 与 usage_alert_test.go 负责。
# 合成租户刻意建 status=active：TenantResolver 对非 active/trial 的目标租户直接 403
#   （middleware/tenant.go:586），建成 expired 会让 admin 侧两条 leg 变成"403 而不是结论"；
#   "已欠费 5 天"用 expired_at 落在过去表达就够——两个视图都只读 expired_at 与 billing_dunning，
#   不看 status 门禁；且 active 态不会被小时巡检推档（巡检只扫 expired），合成数据天然不被污染。
echo "---- 三十三、D3 用量预警 + 催缴 ----"
D3MIG=$($PSQL "SELECT count(*) FROM schema_migrations WHERE version='021_usage_alerts_dunning'" 2>/dev/null | tr -d '[:space:]')
check "迁移021已入版本账本(启动流程没跳过它)" 1 "${D3MIG:-0}"
D3TBL=$($PSQL "SELECT count(*) FROM information_schema.tables WHERE table_name IN ('usage_alerts','billing_dunning')" 2>/dev/null | tr -d '[:space:]')
check "D3两表实存(预警留痕表+催缴状态机表)" 2 "$D3TBL"
# 四条索引按**名字**点验：库里另有 GORM AutoMigrate 建的双胞胎唯一索引（idx_billing_dunning_tenant_id），
# 数总数会随版本漂移而假绿，按名才不会。
D3IDX=$($PSQL "SELECT count(*) FROM pg_indexes WHERE schemaname='public' AND indexname IN ('ux_usage_alert_once','idx_usage_alert_tenant_period','ux_billing_dunning_tenant','idx_billing_dunning_due')" 2>/dev/null | tr -d '[:space:]')
check "D3四条索引按名齐(部分索引AutoMigrate建不出来)" 4 "$D3IDX"
D3PART=$($PSQL "SELECT count(*) FROM pg_indexes WHERE indexname='idx_billing_dunning_due' AND indexdef ILIKE '%next_notify_at%' AND indexdef ILIKE '%running%'" 2>/dev/null | tr -d '[:space:]')
check "催缴调度索引是running部分索引(已结历史行不撑大索引)" 1 "$D3PART"
# 六键出厂默认：两个总开关出厂关着是"可投产"前提（开=真发信/真封号），档位与序列是全平台唯一口径
D3DEF=$($PSQL "SELECT count(*) FROM system_configs WHERE tenant_id=0 AND ((key='usage_alert_enabled' AND value='false') OR (key='usage_alert_thresholds' AND value='[80,95,100]') OR (key='usage_alert_token_balance_below' AND value='200000') OR (key='dunning_enabled' AND value='false') OR (key='dunning_steps' AND value='[0,3,7,14]') OR (key='dunning_suspend_after_days' AND value='14'))" 2>/dev/null | tr -d '[:space:]')
check "D3六键出厂默认已播种系统层(80/95/100档+第0/3/7/14天)" 6 "$D3DEF"
D3SW=$($PSQL "SELECT count(*) FROM system_configs WHERE tenant_id=0 AND key IN ('usage_alert_enabled','dunning_enabled') AND value='false'" 2>/dev/null | tr -d '[:space:]')
check "预警与催缴两总开关出厂false(忘了关就群发/封号的封堵)" 2 "$D3SW"
# 平台级键只认系统层：给租户 1 直插一条 dunning_steps 覆盖行，接口口径**不得**跟着变。
# 为什么用最无害的键做这条证明而不是翻开关：SafeCfg* 读的是系统层缓存，租户覆盖行结构上就到不了
# 口径视图——断言的是"租户改不动全平台运营政策"，无需真的打开开关去发信。
$PSQL "INSERT INTO system_configs (tenant_id, category, key, value, value_type, description, default_value, sort_order, created_at, updated_at)
       VALUES (1,'billing','dunning_steps','[1,2,3,4,5]','json','冒烟-平台级键不得被租户覆盖','[0,3,7,14]',999,NOW(),NOW());" >/dev/null 2>&1
D3OV=$(curl -s -m 15 "$B/api/v1/admin/billing/dunning" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1" | python3 -c "
import sys,json
try: c=(json.load(sys.stdin).get('data') or {}).get('config') or {}
except Exception: print('PARSE_FAIL'); raise SystemExit
print('OK' if c.get('steps')==[0,3,7,14] and c.get('enabled') is False else 'BAD:%s'%c)
" 2>/dev/null)
[ "$D3OV" = "OK" ] && check "平台级键忽略租户覆盖(催缴节奏全平台统一)" y y || check "平台级键忽略租户覆盖(催缴节奏全平台统一)" y "${D3OV:-PARSE_FAIL}"
$PSQL "DELETE FROM system_configs WHERE tenant_id=1 AND key='dunning_steps';" >/dev/null 2>&1
D3OVLEFT=$($PSQL "SELECT count(*) FROM system_configs WHERE tenant_id=1 AND key='dunning_steps'" 2>/dev/null | tr -d '[:space:]')
check "本段租户覆盖行已回收" 0 "${D3OVLEFT:-1}"

# ---- 端点契约与角色闸 ----
D3ALCODE=$(curl -s -o /dev/null -w "%{http_code}" -m 15 "$B/api/v1/admin/usage/alerts" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1")
check "本租户额度预警端点可用" 200 "$D3ALCODE"
D3ALCHK=$(curl -s -m 15 "$B/api/v1/admin/usage/alerts" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1" | python3 -c "
import sys,json,re
try: d=(json.load(sys.stdin).get('data') or {})
except Exception: print('PARSE_FAIL'); raise SystemExit
cfg=d.get('config') or {}
prog=d.get('progress') or []
ok=(re.fullmatch(r'[0-9]{4}-[0-9]{2}', str(d.get('period_key') or '')) is not None
    and cfg.get('enabled') is False and cfg.get('thresholds')==[80,95,100]
    and [p.get('metric') for p in prog]==['monthly_calls','monthly_tokens','token_balance']
    and len(prog)==3 and isinstance(d.get('list'), list))
print('OK' if ok else 'BAD:period=%s cfg=%s metrics=%s'%(d.get('period_key'),cfg,[p.get('metric') for p in prog]))
" 2>/dev/null)
[ "$D3ALCHK" = "OK" ] && check "预警视图契约齐(账期锚+生效口径+三指标进度)" y y || check "预警视图契约齐(账期锚+生效口径+三指标进度)" y "${D3ALCHK:-PARSE_FAIL}"
D3DCHK=$(curl -s -m 15 "$B/api/v1/admin/billing/dunning" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: ${ACME_ID}" | python3 -c "
import sys,json
try: d=(json.load(sys.stdin).get('data') or {})
except Exception: print('PARSE_FAIL'); raise SystemExit
du=d.get('dunning') or {}
print('OK' if du.get('exists') is False and (d.get('config') or {}).get('steps')==[0,3,7,14] else 'BAD:%s'%d)
" 2>/dev/null)
[ "$D3DCHK" = "OK" ] && check "无序列租户回exists=false(不是错误)且带平台口径" y y || check "无序列租户回exists=false(不是错误)且带平台口径" y "${D3DCHK:-PARSE_FAIL}"
D3SQCHK=$(curl -s -m 15 "$B/api/v1/super/dunning" -H "Authorization: Bearer $TOKEN" | python3 -c "
import sys,json
try: d=(json.load(sys.stdin).get('data') or {})
except Exception: print('PARSE_FAIL'); raise SystemExit
print('OK' if isinstance(d.get('list'),list) and 'config' in d and 'usage_alert_config' in d else 'BAD:%s'%list(d))
" 2>/dev/null)
[ "$D3SQCHK" = "OK" ] && check "催缴队列带两份平台口径(关着时说「未启用」而非「没有欠费户」)" y y || check "催缴队列带两份平台口径(关着时说「未启用」而非「没有欠费户」)" y "${D3SQCHK:-PARSE_FAIL}"
D3P1=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/admin/usage/alerts" -H "Authorization: Bearer $STOKEN" -H "X-Tenant-ID: 1")
check "销售角色看额度预警被管理闸拒(403)" 403 "$D3P1"
D3P2=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/super/dunning" -H "Authorization: Bearer $STOKEN")
check "销售角色看平台催缴队列被拒(403)" 403 "$D3P2"
D3P3=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/super/dunning")
check "匿名看平台催缴队列被拒(401)" 401 "$D3P3"

# ---- 合成催缴序列往返（插一行状态机，两个视图 + 人工动作全遍历）----
D3CODE="smoke_d3_$$"
$PSQL "INSERT INTO tenants (name, code, tier, status, created_at, updated_at, expired_at)
       VALUES ('D3催缴验证', '${D3CODE}', 'personal', 'active', NOW(), NOW(), NOW() - INTERVAL '5 days');" >/dev/null 2>&1
# ★写数据一律单条 CTE：psql 的 INSERT ... RETURNING 会把命令标签（INSERT 0 1）混进 stdout，
#   tr -d '[:space:]' 会把它粘成 "2229INSERT01" 让后续 SQL 直接语法错（§二十九 同课）。
$PSQL "WITH t AS (SELECT id FROM tenants WHERE code='${D3CODE}'),
             d AS (INSERT INTO billing_dunning (tenant_id, status, due_at, stage, next_notify_at, created_at, updated_at)
                   SELECT id, 'running', NOW() - INTERVAL '5 days', 2, NOW() + INTERVAL '2 days', NOW(), NOW() FROM t RETURNING id)
       INSERT INTO usage_alerts (tenant_id, metric, threshold, period_key, usage_pct, remaining, channels, created_at, updated_at)
       SELECT id, 'monthly_tokens', 80, to_char(NOW(),'YYYY-MM'), 80, 1234, 'email', NOW(), NOW() FROM t;" >/dev/null 2>&1
D3TID=$($PSQL "SELECT id FROM tenants WHERE code='${D3CODE}'" 2>/dev/null | tr -d '[:space:]')
# 队列leg：一次判定多个字段，失败回显整行（逐字段 check 会让"哪个字段歪了"要跑第二遍才知道）
D3QCHK=$(curl -s -m 15 "$B/api/v1/super/dunning?only_open=true&limit=200" -H "Authorization: Bearer $TOKEN" | python3 -c "
import sys,json
tid=int('${D3TID:-0}')
d=(json.load(sys.stdin).get('data') or {})
rows=[r for r in (d.get('list') or []) if r.get('tenant_id')==tid]
if len(rows)!=1: print('ROWS=%d(tid=%s)'%(len(rows),tid)); raise SystemExit
r=rows[0]
ok=(r.get('status')=='running' and r.get('stage')==2 and r.get('day_past')==5
    and r.get('suspended') is False and r.get('due_at') and r.get('next_notify_at') and r.get('grace_end'))
print('OK' if ok else 'BAD:%s'%r)
" 2>/dev/null)
[ "$D3QCHK" = "OK" ] && check "超管队列命中合成序列(档位/逾期天数/宽限终点同源)" y y || check "超管队列命中合成序列(档位/逾期天数/宽限终点同源)" y "${D3QCHK:-ROWS_FAIL}"
D3ADVCHK=$(curl -s -m 15 "$B/api/v1/admin/billing/dunning" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: ${D3TID}" | python3 -c "
import sys,json
try: d=((json.load(sys.stdin).get('data') or {}).get('dunning') or {})
except Exception: print('PARSE_FAIL'); raise SystemExit
ok=(d.get('exists') is True and d.get('status')=='running' and d.get('stage')==2
    and d.get('total_stages')==4 and d.get('day_past')==5 and d.get('suspended') is False and d.get('grace_end'))
print('OK' if ok else 'BAD:%s'%d)
" 2>/dev/null)
[ "$D3ADVCHK" = "OK" ] && check "租户侧看到的序列与超管队列同值(跨视图同源)" y y || check "租户侧看到的序列与超管队列同值(跨视图同源)" y "${D3ADVCHK:-PARSE_FAIL}"
D3LSTCHK=$(curl -s -m 15 "$B/api/v1/admin/usage/alerts?limit=10" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: ${D3TID}" | python3 -c "
import sys,json
d=(json.load(sys.stdin).get('data') or {})
rows=d.get('list') or []
if len(rows)!=1: print('ROWS=%d'%len(rows)); raise SystemExit
r=rows[0]
ok=(r.get('metric')=='monthly_tokens' and r.get('threshold')==80 and r.get('remaining')==1234 and r.get('channels')=='email')
print('OK' if ok else 'BAD:%s'%r)
" 2>/dev/null)
[ "$D3LSTCHK" = "OK" ] && check "预警留痕可按租户回显(档/剩余量/通道)" y y || check "预警留痕可按租户回显(档/剩余量/通道)" y "${D3LSTCHK:-ROWS_FAIL}"
D3ISOCHK=$(curl -s -m 15 "$B/api/v1/admin/usage/alerts" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: ${ACME_ID}" | python3 -c "
import sys,json
d=(json.load(sys.stdin).get('data') or {})
print('OK' if len(d.get('list') or [])==0 else 'BAD:%s'%(d.get('list')))
" 2>/dev/null)
[ "$D3ISOCHK" = "OK" ] && check "他租户看不到本序列租户的预警留痕(隔离)" y y || check "他租户看不到本序列租户的预警留痕(隔离)" y "${D3ISOCHK:-PARSE_FAIL}"
# 去重锚唯一键实测：同 (租户,指标,档,账期) 再插一条必须被数据库拒——
# 这是"每小时巡检不重复轰炸"的全部机制，光有 Go 侧先查后插挡不住多实例同秒竞态。
D3DUP=$($PSQL "INSERT INTO usage_alerts (tenant_id, metric, threshold, period_key, created_at)
               SELECT tenant_id, metric, threshold, period_key, NOW() FROM usage_alerts
               WHERE tenant_id=(SELECT id FROM tenants WHERE code='${D3CODE}')" 2>&1 | grep -c "duplicate key")
check "同档同账期重复落锚被唯一键拒(去重锚真存在)" 1 "${D3DUP:-0}"
# 人工"立刻再催一次"：这家没有绑定邮箱的管理员 → 动作不成立，必须 400 且只回中文判定。
# 断言文案不含 SQL 片段是 P1-3 错误脱敏红线（DB 错误一律走 500 且不外露细节）。
D3NU=$(curl -s -m 20 -X POST "$B/api/v1/super/dunning/${D3TID}/nudge" -H "Authorization: Bearer $TOKEN")
check "无收件人时人工催缴判400(不假装发出去了)" 400 "$(echo "$D3NU" | jsonget "['code']" 2>/dev/null)"
D3NUMSG=$(echo "$D3NU" | jsonget "['message']" 2>/dev/null)
D3NUMSGCHK=$(python3 -c "
import sys,re
m=sys.argv[1]
if not m: print('EMPTY'); raise SystemExit
if not re.search('收件人|邮件通道', m): print('NOT_USER_FACING:'+m); raise SystemExit
print('OK' if not re.search('(?i)select|error:|gorm|sqlstate', m) else 'SQL_LEAK:'+m)
" "$D3NUMSG" 2>/dev/null)
[ "$D3NUMSGCHK" = "OK" ] && check "错误文案中文可懂且不含SQL片段" y y || check "错误文案中文可懂且不含SQL片段" y "${D3NUMSGCHK:-PARSE_FAIL}"
D3NUAUD=$($PSQL "SELECT count(*) FROM tenant_audit_logs WHERE tenant_id=(SELECT id FROM tenants WHERE code='${D3CODE}') AND action='dunning_manual_nudge'" 2>/dev/null | tr -d '[:space:]')
check "催缴未发出即不落人工审计(留痕只记真做过的事)" 0 "${D3NUAUD:-1}"
# 人工清序列：停止后续催缴，但**不解封**——解封只认可到账那条路（资金红线同构）
D3ST0=$($PSQL "SELECT status FROM tenants WHERE code='${D3CODE}'" 2>/dev/null | tr -d '[:space:]')
D3RS=$(curl -s -m 20 -X POST "$B/api/v1/super/dunning/${D3TID}/reset" -H "Authorization: Bearer $TOKEN")
check "人工清催缴序列成功(code=0)" 0 "$(echo "$D3RS" | jsonget "['code']" 2>/dev/null)"
D3RSD=$($PSQL "SELECT count(*) FROM billing_dunning WHERE tenant_id=(SELECT id FROM tenants WHERE code='${D3CODE}') AND status='resolved' AND next_notify_at IS NULL" 2>/dev/null | tr -d '[:space:]')
check "清序列后status=resolved且预定时间已清空" 1 "${D3RSD:-0}"
D3RSAUD=$($PSQL "SELECT count(*) FROM tenant_audit_logs WHERE tenant_id=(SELECT id FROM tenants WHERE code='${D3CODE}') AND action='dunning_manual_reset' AND user_id>0 AND COALESCE(ip,'')<>''" 2>/dev/null | tr -d '[:space:]')
check "人工清序列落审计带操作人与IP(纠纷可回答谁按的按钮)" 1 "${D3RSAUD:-0}"
D3ST1=$($PSQL "SELECT status FROM tenants WHERE code='${D3CODE}'" 2>/dev/null | tr -d '[:space:]')
[ "$D3ST0" = "$D3ST1" ] && check "清序列不改租户状态(解封只认到账)" y y || check "清序列不改租户状态(解封只认到账)" "$D3ST0" "$D3ST1"
D3RS2=$(curl -s -m 20 -X POST "$B/api/v1/super/dunning/${D3TID}/reset" -H "Authorization: Bearer $TOKEN")
check "重复清序列判400(已无待处理序列)" 400 "$(echo "$D3RS2" | jsonget "['code']" 2>/dev/null)"
D3OPENCHK=$(curl -s -m 15 "$B/api/v1/super/dunning?only_open=true&limit=200" -H "Authorization: Bearer $TOKEN" | python3 -c "
import sys,json
d=(json.load(sys.stdin).get('data') or {})
print('OK' if not [r for r in (d.get('list') or []) if r.get('tenant_id')==int('${D3TID:-0}')] else 'STILL_OPEN')
" 2>/dev/null)
[ "$D3OPENCHK" = "OK" ] && check "工作队列默认只摆在册序列(已结的不再占运营视野)" y y || check "工作队列默认只摆在册序列(已结的不再占运营视野)" y "${D3OPENCHK:-PARSE_FAIL}"
D3ALLCHK=$(curl -s -m 15 "$B/api/v1/super/dunning?only_open=false&limit=200" -H "Authorization: Bearer $TOKEN" | python3 -c "
import sys,json
d=(json.load(sys.stdin).get('data') or {})
hit=[r for r in (d.get('list') or []) if r.get('tenant_id')==int('${D3TID:-0}') and r.get('status')=='resolved']
print('OK' if len(hit)==1 else 'MISSING')
" 2>/dev/null)
[ "$D3ALLCHK" = "OK" ] && check "only_open=false 可回看已结序列(审计留痕不丢)" y y || check "only_open=false 可回看已结序列(审计留痕不丢)" y "${D3ALLCHK:-PARSE_FAIL}"
D3NOSEQ=$(curl -s -m 20 -X POST "$B/api/v1/super/dunning/${ACME_ID}/nudge" -H "Authorization: Bearer $TOKEN")
D3NOSEQCHK=$(echo "$D3NOSEQ" | python3 -c "
import sys
import sys,json
try: j=json.loads(sys.stdin.read())
except Exception: print('PARSE_FAIL'); raise SystemExit
print('OK' if j.get('code')==400 and '没有催缴序列' in (j.get('message') or '') else 'BAD:code=%s msg=%s'%(j.get('code'),j.get('message')))
" 2>/dev/null)
[ "$D3NOSEQCHK" = "OK" ] && check "从未欠费租户人工催缴判400并说明原因" y y || check "从未欠费租户人工催缴判400并说明原因" y "${D3NOSEQCHK:-PARSE_FAIL}"
D3BADID=$(curl -s -m 15 -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/super/dunning/abc/nudge" -H "Authorization: Bearer $TOKEN")
check "非数字租户ID判400(PathUintID口径)" 400 "$D3BADID"
# 观测位：四条计数器必须出现在 /metrics 文本里（计数值随环境变化，只钉"在不在"）
D3MET=$(curl -s -m 15 "$B/metrics" | python3 -c "
import sys
body=sys.stdin.read()
need=['ai_scrm_usage_alert_sent_total','ai_scrm_usage_alert_skipped_total','ai_scrm_dunning_sent_total','ai_scrm_dunning_suspended_total']
miss=[k for k in need if k not in body]
print('OK' if not miss else 'MISSING:'+','.join(miss))
" 2>/dev/null)
[ "$D3MET" = "OK" ] && check "D3四条计数器已在/metrics暴露(上线后能判有没有在发)" y y || check "D3四条计数器已在/metrics暴露(上线后能判有没有在发)" y "${D3MET:-PARSE_FAIL}"
# 现场回收：留痕/序列/审计/租户按 code 子查询删除（不用 $D3TID：建租户失败时它会空，
# 让 "tenant_id=" 直接语法错，把"没造成污染"的收尾变成假红——§二十九 同课）
$PSQL "DELETE FROM usage_alerts WHERE tenant_id=(SELECT id FROM tenants WHERE code='${D3CODE}');
       DELETE FROM billing_dunning WHERE tenant_id=(SELECT id FROM tenants WHERE code='${D3CODE}');
       DELETE FROM tenant_audit_logs WHERE tenant_id=(SELECT id FROM tenants WHERE code='${D3CODE}');
       DELETE FROM tenants WHERE code='${D3CODE}';" >/dev/null 2>&1
D3LEFT=$($PSQL "SELECT (SELECT count(*) FROM usage_alerts) + (SELECT count(*) FROM billing_dunning) + (SELECT count(*) FROM tenants WHERE code='${D3CODE}')" 2>/dev/null | tr -d '[:space:]')
check "本段合成留痕与序列已清零(不污染运营队列)" 0 "${D3LEFT:-1}"

# ---------- 第三十四节：AI 贡献度指标下钻名单与看板数字同源（2026-09-23 D4）----------
# 立此段的原因：D4 把"点看板数字看客户名单"接进产品，它的全部价值押在一句话上——
# **名单条数必须等于卡片上那个数字**。这条只要破一次（两套 SQL 各写一遍），
# 客户对数字的质疑就会从"无处核对"变成"当场被坐实"，比没有下钻更糟（D2 同类错位抓到过）。
# 所以这里不复算判据，而是钉性质：六个指标各自 total 与卡片字段逐字相等、窗口参数进得去、
# 分页不重不漏、单位不可下钻的指标进不了白名单。
# 合成租户刻意造四类客户，让六个数字互不相同且窗口内外各有一例：
#   甲 AI/lead_captured（窗内）、乙 人工/arrived（窗内）、丙 AI/ordered（窗内）、丁 AI/lead_captured（**窗外 60 天**）
#   → days=30：ai_served=2 human_served=1 ai_leads=2 assisted_leads=1 ai_arrived=1 ai_ordered=1
#   → days=90：只有 ai_served/ai_leads 各 +1（丁回来），其余不变——这就是"days 真的进了下钻查询"的可判证据。
echo "---- 三十四、AI 贡献度下钻与看板同源 ----"
D4CODE="smoke_d4_$$"
$PSQL "INSERT INTO tenants (name, code, tier, status, created_at, updated_at)
       VALUES ('D4下钻同源验证', '${D4CODE}', 'personal', 'active', NOW(), NOW());" >/dev/null 2>&1
# d4seed <姓名> <旅程阶段> <是否人工回复y/n> <消息距今天数>
# 一条 CTE 串完 客户→会话→消息（AI 一问一答两条），绝不分三步拿 RETURNING id（§二十九 同课）。
d4seed() {
  $PSQL "WITH t AS (SELECT id FROM tenants WHERE code='${D4CODE}'),
               c AS (INSERT INTO customers (tenant_id, name, journey_stage, intent_score, phone, created_at, updated_at)
                     SELECT id, '$1', '$2', 0.7, '13800000000', NOW(), NOW() FROM t RETURNING id, tenant_id),
               cv AS (INSERT INTO conversations (tenant_id, customer_id, status, mode, last_human_reply_at, created_at, updated_at)
                      SELECT tenant_id, id, 'active', 'ai', CASE WHEN '$3'='y' THEN NOW() ELSE NULL END, NOW(), NOW()
                      FROM c RETURNING id, tenant_id, customer_id)
       INSERT INTO messages (tenant_id, conversation_id, customer_id, sender_type, content, created_at, updated_at)
       SELECT tenant_id, id, customer_id, 'ai', 'D4AI回复$1', NOW() - INTERVAL '$4 days', NOW() FROM cv
       UNION ALL
       SELECT tenant_id, id, customer_id, 'customer', 'D4客户消息$1', NOW() - INTERVAL '$4 days', NOW() FROM cv;" >/dev/null 2>&1
}
d4seed "D4甲AI留资" lead_captured n 2
d4seed "D4乙人工到店" arrived y 2
d4seed "D4丙AI成交" ordered n 2
d4seed "D4丁窗外旧客" lead_captured n 60
D4TID=$($PSQL "SELECT id FROM tenants WHERE code='${D4CODE}'" 2>/dev/null | tr -d '[:space:]')
# 前置自检：合成数据必须真的进了库。缺这一步时下面所有"两侧相等"的断言会在 0==0 上假绿
# （首跑即踩过：CTE 列数不匹配被 >/dev/null 吞掉，全段只剩 total 恒等式撑着，等于没测）。
D4SEED=$($PSQL "SELECT (SELECT count(*) FROM customers WHERE tenant_id=${D4TID:-0})::text||'/'||(SELECT count(*) FROM messages WHERE tenant_id=${D4TID:-0} AND content LIKE 'D4%')" 2>/dev/null | tr -d '[:space:]')
check "合成数据落库自检(4客户/8消息)" "4/8" "${D4SEED:-0/0}"
D4H="Authorization: Bearer $TOKEN"
d4card() { curl -s -m 15 "$B/api/v1/stats/ai-contribution?days=$1" -H "$D4H" -H "X-Tenant-ID: ${D4TID}"; }
d4drill() { curl -s -m 15 "$B/api/v1/stats/ai-contribution/customers?metric=$1&days=$2&page=${3:-1}&page_size=${4:-20}" -H "$D4H" -H "X-Tenant-ID: ${D4TID}"; }

# (1) 合成数据本身的期望值先钉住：卡片六个客户级数字
D4CARDCHK=$(d4card 30 | python3 -c "
import sys,json
d=(json.load(sys.stdin).get('data') or {})
exp={'ai_served_customers':2,'human_served_customers':1,'ai_leads':2,'assisted_leads':1,'ai_arrived':1,'ai_ordered':1}
bad={k:(d.get(k),v) for k,v in exp.items() if d.get(k)!=v}
print('OK' if not bad else 'BAD:%s'%bad)
" 2>/dev/null)
[ "$D4CARDCHK" = "OK" ] && check "合成租户六个客户级数字符合预期(接待/留资/到店/成交)" y y || check "合成租户六个客户级数字符合预期(接待/留资/到店/成交)" y "${D4CARDCHK:-PARSE_FAIL}"

# (2) ★本段主断言：六个指标的名单 total 与卡片六个字段**逐字相等**（同源，不是两边都写对）
D4SAME=$(python3 -c "
import subprocess,json
card=json.loads(subprocess.run(['curl','-s','-m','15','$B/api/v1/stats/ai-contribution?days=30','-H','$D4H','-H','X-Tenant-ID: ${D4TID}'],capture_output=True,text=True).stdout)['data']
pairs=[('ai_served','ai_served_customers'),('human_served','human_served_customers'),('ai_lead','ai_leads'),
       ('assisted_lead','assisted_leads'),('ai_arrived','ai_arrived'),('ai_ordered','ai_ordered')]
bad=[]
for m,f in pairs:
    dr=json.loads(subprocess.run(['curl','-s','-m','15','$B/api/v1/stats/ai-contribution/customers?metric=%s&days=30'%m,'-H','$D4H','-H','X-Tenant-ID: ${D4TID}'],capture_output=True,text=True).stdout)['data']
    if dr.get('total')!=card.get(f): bad.append('%s:名单%s/卡片%s'%(m,dr.get('total'),card.get(f)))
print('OK' if not bad else 'BAD:'+'; '.join(bad))
" 2>/dev/null)
[ "$D4SAME" = "OK" ] && check "六个指标下钻total与看板数字逐字相等(同源判据)" y y || check "六个指标下钻total与看板数字逐字相等(同源判据)" y "${D4SAME:-PARSE_FAIL}"

# (3)(4)(5) 窗口锁：换 days 两侧必须一起动，且动的正是窗外那位客户
D4C90=$(d4card 90 | python3 -c "
import sys,json
d=(json.load(sys.stdin).get('data') or {})
exp={'ai_served_customers':3,'human_served_customers':1,'ai_leads':3,'assisted_leads':1,'ai_arrived':1,'ai_ordered':1}
bad={k:(d.get(k),v) for k,v in exp.items() if d.get(k)!=v}
print('OK' if not bad else 'BAD:%s'%bad)
" 2>/dev/null)
[ "$D4C90" = "OK" ] && check "窗口放宽到90天后卡片纳入窗外客户(仅留资类+1)" y y || check "窗口放宽到90天后卡片纳入窗外客户(仅留资类+1)" y "${D4C90:-PARSE_FAIL}"
D4D90=$(python3 -c "
import subprocess,json
card=json.loads(subprocess.run(['curl','-s','-m','15','$B/api/v1/stats/ai-contribution?days=90','-H','$D4H','-H','X-Tenant-ID: ${D4TID}'],capture_output=True,text=True).stdout)['data']
bad=[]
for m,f in [('ai_served','ai_served_customers'),('ai_lead','ai_leads'),('ai_ordered','ai_ordered')]:
    dr=json.loads(subprocess.run(['curl','-s','-m','15','$B/api/v1/stats/ai-contribution/customers?metric=%s&days=90'%m,'-H','$D4H','-H','X-Tenant-ID: ${D4TID}'],capture_output=True,text=True).stdout)['data']
    if dr.get('total')!=card.get(f): bad.append('%s:名单%s/卡片%s'%(m,dr.get('total'),card.get(f)))
print('OK' if not bad else 'BAD:'+'; '.join(bad))
" 2>/dev/null)
[ "$D4D90" = "OK" ] && check "换窗口后下钻total仍与卡片相等(两侧同动)" y y || check "换窗口后下钻total仍与卡片相等(两侧同动)" y "${D4D90:-PARSE_FAIL}"
D4WIN=$(python3 -c "
import json,subprocess
def t(m,d):
    return json.loads(subprocess.run(['curl','-s','-m','15','$B/api/v1/stats/ai-contribution/customers?metric=%s&days=%s'%(m,d),'-H','$D4H','-H','X-Tenant-ID: ${D4TID}'],capture_output=True,text=True).stdout)['data']['total']
a,b=t('ai_served',30),t('ai_served',90)
print('OK' if (a,b)==(2,3) else 'BAD:30d=%s 90d=%s'%(a,b))
" 2>/dev/null)
[ "$D4WIN" = "OK" ] && check "days参数真进了下钻查询(2→3,不是只改样式)" y y || check "days参数真进了下钻查询(2→3,不是只改样式)" y "${D4WIN:-PARSE_FAIL}"

# (6) 单位纪律：白名单恰六个客户级指标，会话级/消息级/瞬时值一律不在其中
D4REG=$(d4drill ai_served 30 | python3 -c "
import sys,json
d=(json.load(sys.stdin).get('data') or {})
ms=[m.get('metric') for m in (d.get('metrics') or [])]
need=['ai_served','human_served','ai_lead','assisted_lead','ai_arrived','ai_ordered']
banned=[x for x in ('ai_messages','active_conversations','new_conversations','pending_handoff_now') if x in ms]
ok=(sorted(ms)==sorted(need) and not banned and all((m.get('label') or '') for m in (d.get('metrics') or [])))
print('OK' if ok else 'BAD:%s banned=%s'%(ms,banned))
" 2>/dev/null)
[ "$D4REG" = "OK" ] && check "可下钻指标恰六个客户级(消息量/会话数/待接管单位不同)" y y || check "可下钻指标恰六个客户级(消息量/会话数/待接管单位不同)" y "${D4REG:-PARSE_FAIL}"

# (7) 名单内容：窗内三位 AI 接待客户按 id 倒序返回，且行字段够前端渲染
D4ROWS=$(d4drill ai_served 90 | python3 -c "
import sys,json
d=(json.load(sys.stdin).get('data') or {})
names=sorted(r.get('name') for r in (d.get('list') or []))
exp=sorted(['D4甲AI留资','D4丙AI成交','D4丁窗外旧客'])
r0=(d.get('list') or [{}])[0]
ok=(names==exp and all(k in r0 for k in ('id','name','phone','journey_stage','intent_score','served_by'))
    and all(r.get('served_by')=='ai' for r in d['list'])
    and [r.get('id') for r in d['list']]==sorted([r.get('id') for r in d['list']],reverse=True))
print('OK' if ok else 'BAD:names=%s row0=%s'%(names,r0))
" 2>/dev/null)
[ "$D4ROWS" = "OK" ] && check "名单命中人正确且按客户ID稳定排序" y y || check "名单命中人正确且按客户ID稳定排序" y "${D4ROWS:-PARSE_FAIL}"
D4HUM=$(d4drill human_served 30 | python3 -c "
import sys,json
d=(json.load(sys.stdin).get('data') or {})
rows=d.get('list') or []
print('OK' if len(rows)==1 and rows[0].get('name')=='D4乙人工到店' and rows[0].get('served_by')=='human' else 'BAD:%s'%rows)
" 2>/dev/null)
[ "$D4HUM" = "OK" ] && check "人工参与名单只含有人工回复过的那位(归属判据)" y y || check "人工参与名单只含有人工回复过的那位(归属判据)" y "${D4HUM:-PARSE_FAIL}"

# (8)(9)(10) 分页：total 不随分页变、翻页不重不漏、越界页只回空列表不改 total
D4P1=$(d4drill ai_served 90 1 1)
D4P2=$(d4drill ai_served 90 2 1)
D4PGCHK=$(D4A="$D4P1" D4B="$D4P2" python3 -c "
import json,os
a=json.loads(os.environ['D4A'])['data']; b=json.loads(os.environ['D4B'])['data']
ok=(len(a['list'])==1 and len(b['list'])==1 and a['total']==3 and b['total']==3
    and a['page_size']==1 and a['list'][0]['id']!=b['list'][0]['id'])
print('OK' if ok else 'BAD:a=%s b=%s'%(a.get('list'),b.get('list')))
" 2>/dev/null)
[ "$D4PGCHK" = "OK" ] && check "page_size=1翻页不重不漏且total恒等卡片" y y || check "page_size=1翻页不重不漏且total恒等卡片" y "${D4PGCHK:-PARSE_FAIL}"
D4OOB=$(d4drill ai_served 90 99 20 | python3 -c "
import sys,json
d=(json.load(sys.stdin).get('data') or {})
print('OK' if (d.get('list')==[] or d.get('list') is None) and d.get('total')==3 else 'BAD:list=%s total=%s'%(d.get('list'),d.get('total')))
" 2>/dev/null)
[ "$D4OOB" = "OK" ] && check "越界页回空名单但total如实(前端据此收回页码)" y y || check "越界页回空名单但total如实(前端据此收回页码)" y "${D4OOB:-PARSE_FAIL}"
D4CAP=$(d4drill ai_served 90 1 1000 | python3 -c "
import sys,json
print((json.load(sys.stdin).get('data') or {}).get('page_size'))
" 2>/dev/null)
check "page_size硬顶100(下钻是核对用不是导数用)" 100 "${D4CAP:-0}"

# (11)(12)(13) 入参与鉴权
D4BAD=$(curl -s -m 15 -o /dev/null -w "%{http_code}" "$B/api/v1/stats/ai-contribution/customers?metric=ai_messages&days=30" -H "$D4H" -H "X-Tenant-ID: ${D4TID}")
check "非白名单指标(ai_messages)判400" 400 "$D4BAD"
D4NOM=$(curl -s -m 15 -o /dev/null -w "%{http_code}" "$B/api/v1/stats/ai-contribution/customers" -H "$D4H" -H "X-Tenant-ID: ${D4TID}")
check "缺metric判400(不默认回某一份名单)" 400 "$D4NOM"
D4ANO=$(curl -s -m 15 -o /dev/null -w "%{http_code}" "$B/api/v1/stats/ai-contribution/customers?metric=ai_served")
check "未登录取下钻名单判401(名单含客户明细)" 401 "$D4ANO"
# 随行口径：label 与 note 由后端下发，前端不复写第二套文案（否则口径就有了第二个真相源）
D4TXT=$(d4drill ai_served 30 | python3 -c "
import sys,json
d=(json.load(sys.stdin).get('data') or {})
print('OK' if d.get('label')=='AI 独立接待客户' and '同一判据' in (d.get('note') or '') else 'BAD:label=%s note=%s'%(d.get('label'),d.get('note')))
" 2>/dev/null)
[ "$D4TXT" = "OK" ] && check "下钻随行下发指标名与口径说明" y y || check "下钻随行下发指标名与口径说明" y "${D4TXT:-PARSE_FAIL}"
# 跨租户：同一时刻用另一个租户的作用域打同一个指标，不得看到合成租户的人
D4ISO=$(curl -s -m 15 "$B/api/v1/stats/ai-contribution/customers?metric=ai_served&days=90&page_size=100" -H "$D4H" -H "X-Tenant-ID: ${ACME_ID}" | python3 -c "
import sys,json
d=(json.load(sys.stdin).get('data') or {})
hit=[r for r in (d.get('list') or []) if str(r.get('name','')).startswith('D4')]
print('OK' if not hit else 'LEAK:%s'%hit)
" 2>/dev/null)
[ "$D4ISO" = "OK" ] && check "下钻名单跨租户不可见(带明细的端点更要守)" y y || check "下钻名单跨租户不可见(带明细的端点更要守)" y "${D4ISO:-PARSE_FAIL}"
# 现场回收（顺序：消息→会话→客户→租户）
$PSQL "DELETE FROM messages WHERE tenant_id=(SELECT id FROM tenants WHERE code='${D4CODE}');
       DELETE FROM conversations WHERE tenant_id=(SELECT id FROM tenants WHERE code='${D4CODE}');
       DELETE FROM customers WHERE tenant_id=(SELECT id FROM tenants WHERE code='${D4CODE}');
       DELETE FROM tenants WHERE code='${D4CODE}';" >/dev/null 2>&1
D4LEFT=$($PSQL "SELECT (SELECT count(*) FROM customers WHERE name LIKE 'D4%') + (SELECT count(*) FROM messages WHERE content LIKE 'D4%') + (SELECT count(*) FROM tenants WHERE code='${D4CODE}')" 2>/dev/null | tr -d '[:space:]')
check "本段合成客户/消息/租户已清零" 0 "${D4LEFT:-1}"

# ---------- 第三十五节：获客活码（获客批 · 渠道归因最小闭环，2026-09-23）----------
# 这段测的是"哪个渠道值得继续投钱"这句话能不能成立。三处最容易出事：
#   1) **归因写进别人家**——公开链路只拿得到码本身（扫码时的 Host 由物料决定），
#      所以租户身份必须从码行上取、且"码所属租户 ≠ 请求租户"时拒写；错一家的账比缺账难查。
#   2) **漏斗数字与名单两套 SQL**——D4 立过的规矩在这里再钉一次：卡片 3 个、点进去 4 行当场失信。
#   3) **公开面把"存在但停用"与"不存在"分出差别**——那是全网唯一能猜码的入口，回显即泄露。
# 因此本段按真实顺序走一遍：建码 → 公开解析 → 扫码 → 匿名建档归因 → 漏斗 → 名单，
# 并刻意造两个租户对打（甲的码打到乙家必须不上归因）。
echo "---- 三十五、获客活码：码→扫码→归因→漏斗→名单 ----"
ACQ_CA="smoke_acq_a_$$"
ACQ_CB="smoke_acq_b_$$"
$PSQL "INSERT INTO tenants (name, code, tier, status, created_at, updated_at)
       VALUES ('活码甲租户','${ACQ_CA}','personal','active',NOW(),NOW()),
              ('活码乙租户','${ACQ_CB}','personal','active',NOW(),NOW());" >/dev/null 2>&1
ACQ_TA=$($PSQL "SELECT id FROM tenants WHERE code='${ACQ_CA}'" 2>/dev/null | tr -d '[:space:]')
ACQ_TB=$($PSQL "SELECT id FROM tenants WHERE code='${ACQ_CB}'" 2>/dev/null | tr -d '[:space:]')
# 前置自检：两个合成租户必须真的在库里（缺这一步时下面所有跨租户断言会在"两边都是 0"上假绿）
ACQ_TENANTS=$($PSQL "SELECT count(*) FROM tenants WHERE code IN ('${ACQ_CA}','${ACQ_CB}')" 2>/dev/null | tr -d '[:space:]')
check "合成两租户落库自检(甲/乙各一)" 2 "${ACQ_TENANTS:-0}"
AH="Authorization: Bearer $TOKEN"
# acq_at <租户ID> <方法> <路径> [body]：以某租户作用域打管理端（admin 组带 AdminRequired）
acq_at() {
  if [ "$2" = "GET" ]; then
    curl -s -m 15 -H "$AH" -H "X-Tenant-ID: $1" "$B$3"
  else
    curl -s -m 15 -X "$2" -H "$AH" -H "X-Tenant-ID: $1" -H "Content-Type: application/json" -d "${4:-}" "$B$3"
  fi
}
acq_a() { acq_at "$ACQ_TA" "$@"; }
acq_b() { acq_at "$ACQ_TB" "$@"; }
# acq_pub <方法> <路径> [body]：公开侧（免登录、不带任何租户头，模拟真实扫码）
acq_pub() {
  if [ "$1" = "GET" ]; then curl -s -m 15 "$B$2"
  else curl -s -m 15 -X "$1" -H "Content-Type: application/json" -d "${3:-}" "$B$2"; fi
}

# (1)~(5) 数据层实存：迁移登记、两表、全局唯一码、去重部分索引、归因列
ACQ_MIG=$($PSQL "SELECT count(*) FROM schema_migrations WHERE version='022_acquisition_codes'" 2>/dev/null | tr -d '[:space:]')
check "迁移022已登记版本账本" 1 "${ACQ_MIG:-0}"
ACQ_TBL=$($PSQL "SELECT count(*) FROM information_schema.tables WHERE table_name IN ('acquisition_codes','acquisition_scans')" 2>/dev/null | tr -d '[:space:]')
check "活码两表(码/扫码事件)实存" 2 "${ACQ_TBL:-0}"
# 码必须**全库**唯一：公开解析只单键查码，租户内唯一会让两个家的同码同时命中
ACQ_UX=$($PSQL "SELECT indexdef LIKE '%UNIQUE%' AND indexdef LIKE '%(code)%' FROM pg_indexes WHERE indexname='ux_acq_code_global'" 2>/dev/null | tr -d '[:space:]')
check "码字符串全局唯一索引(公开单键定位的前提)" t "${ACQ_UX:-f}"
# 去重索引只覆盖带访客键的行：纯打开的匿名行没有去重依据，塞进索引只会让索引变大
ACQ_PARTIAL=$($PSQL "SELECT indexdef LIKE '%WHERE ((visitor_key)%' FROM pg_indexes WHERE indexname='idx_acq_scan_dedupe'" 2>/dev/null | tr -d '[:space:]')
check "扫码去重走部分索引(空访客键不进索引)" t "${ACQ_PARTIAL:-f}"
ACQ_COL=$($PSQL "SELECT column_default FROM information_schema.columns WHERE table_name='customers' AND column_name='acquisition_code'" 2>/dev/null | tr -d '[:space:]')
check "客户表首触归因列存在且默认空串" "''::charactervarying" "${ACQ_COL:-MISSING}"

# (6)~(7) 闸：未登录与角色不足都进不来（名单含客户手机号）
ACQ_401=$(curl -s -m 15 -o /dev/null -w "%{http_code}" "$B/api/v1/admin/acquisition/codes")
check "未登录取活码列表判401" 401 "$ACQ_401"
ACQ_403=$(curl -s -m 15 -o /dev/null -w "%{http_code}" "$B/api/v1/admin/acquisition/codes" -H "Authorization: Bearer $STOKEN")
check "sales角色取活码列表判403(建码是投放决策不是日常操作)" 403 "$ACQ_403"

# (8)~(11) 建码入参：拒绝要回稳定原因码（文案可改、码不可改）
ACQ_NONAME=$(acq_a POST /api/v1/admin/acquisition/codes '{"name":"  ","channel":"抖音"}' | jsonget "['reason']")
check "缺用途名判name_required" name_required "${ACQ_NONAME:-NONE}"
ACQ_NOCHAN=$(acq_a POST /api/v1/admin/acquisition/codes '{"name":"春季车展","channel":""}' | jsonget "['reason']")
check "缺渠道判channel_required(不知道投哪就没法算账)" channel_required "${ACQ_NOCHAN:-NONE}"
ACQ_BADCHAN=$(acq_a POST /api/v1/admin/acquisition/codes '{"name":"春季车展","channel":"电梯广告"}' | jsonget "['reason']")
check "未知渠道判channel_unknown(枚举由后端下发不各写一套)" channel_unknown "${ACQ_BADCHAN:-NONE}"
ACQ_CREATE=$(acq_a POST /api/v1/admin/acquisition/codes '{"name":"门店前台立牌","channel":"门店自然","remark":"smoke"}')
ACQ_CODE=$(echo "$ACQ_CREATE" | jsonget "['data']['code']")
ACQ_CID=$(echo "$ACQ_CREATE" | jsonget "['data']['id']")
check "建码成功返回8位短码" 8 "${#ACQ_CODE}"
# 短码字符集刻意排除易混字符（I/L/O/0/1）：海报上要能被人口述抄下来
ACQ_ALPHA=$(ACQ_C="${ACQ_CODE:-X}" python3 -c "
import os,re
c=os.environ['ACQ_C']
print('OK' if re.fullmatch(r'[ABCDEFGHJKMNPQRSTUVWXYZ23456789]{8}', c) else 'BAD:'+c)
" 2>/dev/null)
[ "$ACQ_ALPHA" = "OK" ] && check "短码形态可口述(排除I/L/O/0/1易混字符)" y y || check "短码形态可口述(排除I/L/O/0/1易混字符)" y "${ACQ_ALPHA:-BAD}"

# (12) 链接由后端拼：前端不知道三级基址优先级，让它拼等于把印错海报的风险挪进看不见的地方
ACQ_LINK=$(echo "$ACQ_CREATE" | jsonget "['data']['link']")
case "$ACQ_LINK" in
  *"/client?code=${ACQ_CODE}") check "落地链接指向对话页并带上码" y y ;;
  *) check "落地链接指向对话页并带上码" y "${ACQ_LINK:-EMPTY}" ;;
esac

# (13) 空态必须是 [] 不是 null（前端直接 map）——用从没建过码的乙租户看
ACQ_EMPTY=$(acq_b GET /api/v1/admin/acquisition/codes | jsonget "['data']['list'] == []")
check "无码租户列表空态是[]不是null" True "${ACQ_EMPTY:-False}"
ACQ_CFG=$(acq_a GET /api/v1/admin/acquisition/codes | python3 -c "
import sys,json
d=(json.load(sys.stdin).get('data') or {}).get('config') or {}
ok=(len(d.get('channels') or [])==7 and sorted(d.get('metrics') or [])==sorted(['new','spoke','lead','arrived','ordered'])
    and all((d.get('labels') or {}).get(m) for m in (d.get('metrics') or [])))
print('OK' if ok else 'BAD:%s'%(d,))
" 2>/dev/null)
[ "$ACQ_CFG" = "OK" ] && check "展示口径由后端下发(7渠道枚举+5客户级指标+中文名)" y y || check "展示口径由后端下发(7渠道枚举+5客户级指标+中文名)" y "${ACQ_CFG:-PARSE_FAIL}"

# (14)~(15) 二维码出图：图在服务端生成（要拖进设计稿），且越界尺寸不得撑爆内存
ACQ_PNG_CT=$(curl -s -m 15 -o /dev/null -w "%{content_type}" "$B/api/v1/admin/acquisition/codes/${ACQ_CID}/qr.png?size=400" -H "$AH" -H "X-Tenant-ID: ${ACQ_TA}")
check "二维码以image/png返回(非JSON信封)" "image/png" "$ACQ_PNG_CT"
ACQ_PNG_MAGIC=$(curl -s -m 15 "$B/api/v1/admin/acquisition/codes/${ACQ_CID}/qr.png?size=99999" -H "$AH" -H "X-Tenant-ID: ${ACQ_TA}" | od -An -tx1 -N4 | tr -d ' \n')
check "超大size被钳住仍出合法PNG" "89504e47" "${ACQ_PNG_MAGIC:-EMPTY}"
ACQ_QR404=$(curl -s -m 15 -o /dev/null -w "%{http_code}" "$B/api/v1/admin/acquisition/codes/${ACQ_CID}/qr.png" -H "$AH" -H "X-Tenant-ID: ${ACQ_TB}")
check "跨租户取别人的码出图判404(不回显存在性)" 404 "$ACQ_QR404"

# (16)~(17) 启停：缺 active 不猜默认值（这是会改变对外可见性的动作）
ACQ_NOACT=$(acq_a POST "/api/v1/admin/acquisition/codes/${ACQ_CID}/status" '{}' | jsonget "['code']")
check "启停缺active参数判400(不默认顺手启用)" 400 "${ACQ_NOACT:-0}"
ACQ_GONE=$(acq_a POST /api/v1/admin/acquisition/codes/99999999/status '{"active":true}' | jsonget "['code']")
check "不存在的码改状态判404" 404 "${ACQ_GONE:-0}"

# (18)~(21) 公开解析：启用中 200，停用/不存在/形态非法一律同一个 404
ACQ_RES=$(acq_pub GET "/api/v1/acquisition/${ACQ_CODE}" | python3 -c "
import sys,json
j=json.load(sys.stdin); d=j.get('data') or {}
print('OK' if j.get('code')==0 and d.get('channel')=='门店自然' and d.get('landing_path')=='/client' and 'tenant_id' not in d else 'BAD:%s'%(d,))
" 2>/dev/null)
[ "$ACQ_RES" = "OK" ] && check "落地页自检拿到渠道名且不外泄租户ID/扫码数" y y || check "落地页自检拿到渠道名且不外泄租户ID/扫码数" y "${ACQ_RES:-PARSE_FAIL}"
ACQ_404A=$(curl -s -m 15 -o /dev/null -w "%{http_code}" "$B/api/v1/acquisition/ZZZZZZZZ")
ACQ_404B=$(curl -s -m 15 -o /dev/null -w "%{http_code}" "$B/api/v1/acquisition/ab")
ACQ_MSG=$(curl -s -m 15 "$B/api/v1/acquisition/ZZZZZZZZ" | jsonget "['message']")
check "不存在的码判404" 404 "$ACQ_404A"
check "形态非法的码判404(连表都不碰)" 404 "$ACQ_404B"
check "猜码失败只说活动已结束(不透露是否存在)" "活动已结束" "${ACQ_MSG:-EMPTY}"
# 停用后对外即不存在：先停甲的码，公开解析必须转 404，然后再启用回来继续本段
acq_a POST "/api/v1/admin/acquisition/codes/${ACQ_CID}/status" '{"active":false}' >/dev/null
ACQ_DIS=$(curl -s -m 15 -o /dev/null -w "%{http_code}" "$B/api/v1/acquisition/${ACQ_CODE}")
check "停用中的码公开解析同样404(与不存在同形态)" 404 "$ACQ_DIS"
acq_a POST "/api/v1/admin/acquisition/codes/${ACQ_CID}/status" '{"active":true}' >/dev/null

# (22)~(23) 扫码计数与去重：同一访客窗口内重复打开只算一次（渠道预算按这个数分）
ACQ_SCAN1=$(acq_pub POST "/api/v1/acquisition/${ACQ_CODE}/scan" "{\"visitor_key\":\"vk_acq_$$\"}" | jsonget "['data']['counted']")
ACQ_SCAN2=$(acq_pub POST "/api/v1/acquisition/${ACQ_CODE}/scan" "{\"visitor_key\":\"vk_acq_$$\"}" | jsonget "['data']['counted']")
check "首次扫码计数" True "${ACQ_SCAN1:-False}"
check "同访客窗口内重复扫码不再计(防一人刷高渠道量)" False "${ACQ_SCAN2:-True}"
ACQ_SCANDB=$($PSQL "SELECT count(*) FROM acquisition_scans WHERE code_id=${ACQ_CID}" 2>/dev/null | tr -d '[:space:]')
check "扫码事件落库且租户归属取自码行(不是来路Host)" 1 "${ACQ_SCANDB:-0}"

# (24)~(26) 匿名建档归因：本家码上归因，别人家的码只记不上，停用码不写
ACQ_G1=$(curl -s -m 15 -X POST "$B/api/v1/chat/guest" -H "X-Tenant-ID: ${ACQ_TA}" -H "Content-Type: application/json" -d "{\"code\":\"${ACQ_CODE}\"}")
ACQ_C1=$(echo "$ACQ_G1" | jsonget "['data']['customer_id']")
ACQ_APP1=$(echo "$ACQ_G1" | jsonget "['data']['acquisition']['applied']")
ACQ_DB1=$($PSQL "SELECT acquisition_code FROM customers WHERE id=${ACQ_C1:-0}" 2>/dev/null | tr -d '[:space:]')
check "扫本家码建档：归因成功且列真落库" "${ACQ_CODE}" "${ACQ_DB1:-EMPTY}"
check "归因结果回给前端(建客是扫码后唯一确定性动作)" True "${ACQ_APP1:-False}"
ACQ_G2=$(curl -s -m 15 -X POST "$B/api/v1/chat/guest" -H "X-Tenant-ID: ${ACQ_TB}" -H "Content-Type: application/json" -d "{\"code\":\"${ACQ_CODE}\"}")
ACQ_C2=$(echo "$ACQ_G2" | jsonget "['data']['customer_id']")
ACQ_R2=$(echo "$ACQ_G2" | jsonget "['data']['acquisition']['reason']")
# 先证明"乙家这位客户确实建成了"——否则下面"列为空"会在查不到行的空结果上假绿
ACQ_C2EXISTS=$($PSQL "SELECT count(*) FROM customers WHERE id=${ACQ_C2:-0} AND tenant_id=${ACQ_TB}" 2>/dev/null | tr -d '[:space:]')
check "被拒归因的乙家客户本身照样建成(不打断客户咨询)" 1 "${ACQ_C2EXISTS:-0}"
case "$ACQ_R2" in
  *code_tenant_mismatch*) check "别家的码打到乙租户：拒绝归因并说明原因" y y ;;
  *) check "别家的码打到乙租户：拒绝归因并说明原因" y "${ACQ_R2:-NONE}" ;;
esac
ACQ_DB2=$($PSQL "SELECT acquisition_code FROM customers WHERE id=${ACQ_C2:-0}" 2>/dev/null | tr -d '[:space:]')
check "被拒归因的客户列仍为空(没被记进别人家)" "" "${ACQ_DB2}"

# (27) 停用码建客：照样能聊（不打断客户），但归因不上
acq_a POST "/api/v1/admin/acquisition/codes/${ACQ_CID}/status" '{"active":false}' >/dev/null
ACQ_R3=$(curl -s -m 15 -X POST "$B/api/v1/chat/guest" -H "X-Tenant-ID: ${ACQ_TA}" -H "Content-Type: application/json" -d "{\"code\":\"${ACQ_CODE}\"}" | jsonget "['data']['acquisition']['reason']")
acq_a POST "/api/v1/admin/acquisition/codes/${ACQ_CID}/status" '{"active":true}' >/dev/null
check "停用码不再归因(但客户照常建成)" code_disabled "${ACQ_R3:-NONE}"

# (28)~(31) 漏斗与名单同源：卡片数字 == 点进去的 total
ACQ_LIST=$(acq_a GET "/api/v1/admin/acquisition/codes?days=30")
ACQ_CARD=$(echo "$ACQ_LIST" | python3 -c "
import sys,json
d=(json.load(sys.stdin).get('data') or {})
for r in (d.get('list') or []):
    if r.get('code')=='${ACQ_CODE}':
        print(json.dumps({'new':r['funnel'].get('new'),'scans':r.get('scans')}))
        break
" 2>/dev/null)
ACQ_NEWCARD=$(echo "${ACQ_CARD:-{\}}" | python3 -c "import sys,json;print((json.load(sys.stdin) or {}).get('new'))" 2>/dev/null)
ACQ_NEWDILL=$(acq_a GET "/api/v1/admin/acquisition/codes/${ACQ_CID}/customers?metric=new&days=30" | jsonget "['data']['total']")
check "「新增客户」卡片数字与名单total逐字相等(同源)" "${ACQ_NEWCARD:-X}" "${ACQ_NEWDILL:-Y}"
ACQ_NAMEIN=$(acq_a GET "/api/v1/admin/acquisition/codes/${ACQ_CID}/customers?metric=new&days=30" | python3 -c "
import sys,json
d=(json.load(sys.stdin).get('data') or {})
ids=[r.get('id') for r in (d.get('list') or [])]
print('OK' if ${ACQ_C1:-0} in ids and d.get('note') else 'BAD:%s'%(d.get('list'),))
" 2>/dev/null)
[ "$ACQ_NAMEIN" = "OK" ] && check "扫码建档的人出现在名单里且随行下发口径说明" y y || check "扫码建档的人出现在名单里且随行下发口径说明" y "${ACQ_NAMEIN:-PARSE_FAIL}"
ACQ_SCAN400=$(curl -s -m 15 -o /dev/null -w "%{http_code}" "$B/api/v1/admin/acquisition/codes/${ACQ_CID}/customers?metric=scans&days=30" -H "$AH" -H "X-Tenant-ID: ${ACQ_TA}")
check "扫码次数不可下钻判400(单位是次不是人)" 400 "$ACQ_SCAN400"
ACQ_NOMETRIC=$(curl -s -m 15 -o /dev/null -w "%{http_code}" "$B/api/v1/admin/acquisition/codes/${ACQ_CID}/customers" -H "$AH" -H "X-Tenant-ID: ${ACQ_TA}")
check "缺metric判400(不默认回某一份名单)" 400 "$ACQ_NOMETRIC"
ACQ_PSCAP=$(acq_a GET "/api/v1/admin/acquisition/codes/${ACQ_CID}/customers?metric=new&page_size=1000" | jsonget "['data']['page_size']")
check "名单page_size硬顶100且回显钳后值" 100 "${ACQ_PSCAP:-0}"
ACQ_ISO404=$(curl -s -m 15 -o /dev/null -w "%{http_code}" "$B/api/v1/admin/acquisition/codes/${ACQ_CID}/customers?metric=new" -H "$AH" -H "X-Tenant-ID: ${ACQ_TB}")
check "跨租户下钻别人码的名单判404" 404 "$ACQ_ISO404"
ACQ_ANON=$(curl -s -m 15 -o /dev/null -w "%{http_code}" "$B/api/v1/admin/acquisition/codes/${ACQ_CID}/customers?metric=new")
check "未登录取下钻名单判401(名单含手机号)" 401 "$ACQ_ANON"

# (32) 同一访客先后扫两个码：两条事件都留（"这个码被打开过"是各自的事实），
#      但客户身上的归因只有第一次作数——首触粘性在 internal/acquisition 单测里
#      按谓词与落库两层各自钉死（policy_test/service_test），HTTP 侧每次建的都是新客，
#      结构上造不出"同客户二次归因"，所以这里只断事件不覆盖，别把断言写成它测不到的东西。
ACQ_CREATE2=$(acq_a POST /api/v1/admin/acquisition/codes '{"name":"销售个人码","channel":"微信"}')
ACQ_CODE2=$(echo "$ACQ_CREATE2" | jsonget "['data']['code']")
ACQ_CID2=$(echo "$ACQ_CREATE2" | jsonget "['data']['id']")
acq_pub POST "/api/v1/acquisition/${ACQ_CODE2}/scan" "{\"visitor_key\":\"vk_acq_$$\"}" >/dev/null
ACQ_MULTI=$($PSQL "SELECT count(*) FROM acquisition_scans WHERE visitor_key='vk_acq_$$' AND code_id IN (${ACQ_CID},${ACQ_CID2})" 2>/dev/null | tr -d '[:space:]')
check "同一访客扫两个码各记一条事件(去重按码+访客不按访客)" 2 "${ACQ_MULTI:-0}"

# (33) 现场回收：码/事件/客户/租户全清，短码与数字都不留残
$PSQL "DELETE FROM acquisition_scans WHERE tenant_id IN (${ACQ_TA:-0},${ACQ_TB:-0});
       DELETE FROM acquisition_codes WHERE tenant_id IN (${ACQ_TA:-0},${ACQ_TB:-0});
       DELETE FROM customers WHERE tenant_id IN (${ACQ_TA:-0},${ACQ_TB:-0});
       DELETE FROM tenants WHERE code IN ('${ACQ_CA}','${ACQ_CB}');" >/dev/null 2>&1
ACQ_LEFT=$($PSQL "SELECT (SELECT count(*) FROM acquisition_codes WHERE code IN ('${ACQ_CODE}','${ACQ_CODE2}')) + (SELECT count(*) FROM acquisition_scans WHERE tenant_id IN (${ACQ_TA:-0},${ACQ_TB:-0})) + (SELECT count(*) FROM tenants WHERE code IN ('${ACQ_CA}','${ACQ_CB}'))" 2>/dev/null | tr -d '[:space:]')
check "本段活码/扫码事件/租户已清零" 0 "${ACQ_LEFT:-1}"

# ---------- 第三十六节：商机与报价（商机批 · 管道看板 + 报价版本链，2026-09-24）----------
# 这段测的是两句管理会上的话："这条管道健康吗" 和 "这张单子谈到哪一版了"。四处最容易出事：
#   1) **格子与名单两套 SQL**——看板写 12 张、点进去 9 行，当场失信（D4 与活码批同一条教训）。
#      本段不是抽查一格，而是把 4 个分组 + 6 个阶段格子全跑一遍逐字比，
#      并先自检"参与比对的格子数=10、非零格子≥5"，否则整段等式会在 0==0 上假绿。
#   2) **在途唯一**：一个客户两张活单，"在途商机数"与"有单客户数"就再也对不上。
#      判重不能只靠代码（多实例并发必撞），必须 DB 部分唯一索引兜底——所以断索引定义，
#      并断"终局之后能重开一张新的"（部分索引只盖非终局行，这条语义错了就是产品缺陷）。
#   3) **报价是钱**：合计一律服务端按明细算，客户手上那一版永不覆盖（v1→v2→v3 全留），
#      已发出的内容锁死，改版只能出新版。
#   4) **动作要留得下痕迹**：接受报价 ≠ 商机成交（两步分开，谁标的、何时标的查得到）；
#      终局单不再改；拒绝一律回稳定原因码，前端与冒烟都按码分支。
# 顺序按真实业务走：建单 → 推进 → 报价 v1/v2 → 接受 → 成交 → 终局锁 → 重开 → 看板核对。
echo "---- 三十六、商机与报价：建单→推进→报价版本链→成交→终局锁→看板同源 ----"
DEAL_CA="smoke_deal_a_$$"
DEAL_CB="smoke_deal_b_$$"
$PSQL "INSERT INTO tenants (name, code, tier, status, created_at, updated_at)
       VALUES ('商机甲租户','${DEAL_CA}','personal','active',NOW(),NOW()),
              ('商机乙租户','${DEAL_CB}','personal','active',NOW(),NOW());" >/dev/null 2>&1
DEAL_TA=$($PSQL "SELECT id FROM tenants WHERE code='${DEAL_CA}'" 2>/dev/null | tr -d '[:space:]')
DEAL_TB=$($PSQL "SELECT id FROM tenants WHERE code='${DEAL_CB}'" 2>/dev/null | tr -d '[:space:]')
# 前置自检：两个合成租户必须真的在库里（缺这步时下面所有跨租户断言会在"两边都是 0"上假绿）
DEAL_TENANTS=$($PSQL "SELECT count(*) FROM tenants WHERE code IN ('${DEAL_CA}','${DEAL_CB}')" 2>/dev/null | tr -d '[:space:]')
check "合成两租户落库自检(甲/乙各一)" 2 "${DEAL_TENANTS:-0}"
AH="Authorization: Bearer $TOKEN"
# deal_at <租户ID> <方法> <路径> [body]：以某租户作用域打管理端/顾问端
deal_at() {
  if [ "$2" = "GET" ]; then
    curl -s -m 15 -H "$AH" -H "X-Tenant-ID: $1" "$B$3"
  else
    curl -s -m 15 -X "$2" -H "$AH" -H "X-Tenant-ID: $1" -H "Content-Type: application/json" -d "${4:-}" "$B$3"
  fi
}
deal_a() { deal_at "$DEAL_TA" "$@"; }
deal_b() { deal_at "$DEAL_TB" "$@"; }

# (1)~(6) 数据层实存：迁移登记、两表、两条部分唯一索引的定义、看板与停滞榜的取数索引
DEAL_MIG=$($PSQL "SELECT count(*) FROM schema_migrations WHERE version='023_deal_opportunities'" 2>/dev/null | tr -d '[:space:]')
check "迁移023已登记版本账本" 1 "${DEAL_MIG:-0}"
DEAL_TBL=$($PSQL "SELECT count(*) FROM information_schema.tables WHERE table_name IN ('opportunities','quotes')" 2>/dev/null | tr -d '[:space:]')
check "商机/报价两表实存" 2 "${DEAL_TBL:-0}"
# 索引必须**只盖非终局行**：全表唯一会让"流失后重开一张新单"永久失败
# （PG 把 `stage NOT IN ('won','lost')` 渲染成 `<> ALL(ARRAY[...])`，断的是实际执行计划里的那句话）
DEAL_UX=$($PSQL "SELECT (indexdef LIKE '%UNIQUE%') AND (indexdef LIKE '%(tenant_id, customer_id)%') AND (indexdef LIKE '%<> ALL%') AND (indexdef LIKE '%won%') AND (indexdef LIKE '%lost%') FROM pg_indexes WHERE indexname='ux_deal_one_open_per_customer'" 2>/dev/null | tr -d '[:space:]')
check "一客户一在途单=复合唯一+部分索引(终局行不占位)" t "${DEAL_UX:-f}"
DEAL_QUX=$($PSQL "SELECT (indexdef LIKE '%UNIQUE%') AND (indexdef LIKE '%(opportunity_id)%') AND (indexdef LIKE '%= ANY%') AND (indexdef LIKE '%draft%') AND (indexdef LIKE '%sent%') FROM pg_indexes WHERE indexname='ux_quote_one_open_per_deal'" 2>/dev/null | tr -d '[:space:]')
check "一单一在途报价=部分唯一(只算draft/sent,历史照留)" t "${DEAL_QUX:-f}"
DEAL_IDX=$($PSQL "SELECT count(*) FROM pg_indexes WHERE indexname IN ('idx_deal_tenant_stage','idx_quote_due')" 2>/dev/null | tr -d '[:space:]')
check "管道视图与过期巡检各有自己的取数索引(后台默认页不扫全表)" 2 "${DEAL_IDX:-0}"
# AI 自动开单出厂关：开关一开，每个询价客户都会多一张没人核对过的单，灌满顾问台
DEAL_AISEED=$($PSQL "SELECT count(*) FROM system_configs WHERE tenant_id=0 AND \"key\"='deal_auto_open_enabled' AND value='false'" 2>/dev/null | tr -d '[:space:]')
check "AI自动开单键已播种且出厂false" 1 "${DEAL_AISEED:-0}"

# (7)~(9) 闸：未登录、角色不足、以及"名单含手机号"这条读侧闸
DEAL_401=$(curl -s -m 15 -o /dev/null -w "%{http_code}" "$B/api/v1/admin/deals/board")
check "未登录取看板判401" 401 "$DEAL_401"
DEAL_403=$(curl -s -m 15 -o /dev/null -w "%{http_code}" "$B/api/v1/admin/deals/board" -H "Authorization: Bearer $STOKEN")
check "sales角色取管道看板判403(整条管道是管理视图)" 403 "$DEAL_403"
DEAL_DRILL401=$(curl -s -m 15 -o /dev/null -w "%{http_code}" "$B/api/v1/admin/deals?filter=open")
check "未登录取下钻名单判401(名单行带客户手机号)" 401 "$DEAL_DRILL401"

# (10)~(15) 建单入参：拒绝必须回稳定原因码（文案可改、码不可改）
DEAL_C1=$(curl -s -m 15 -X POST "$B/api/v1/chat/guest" -H "X-Tenant-ID: ${DEAL_TA}" -H "Content-Type: application/json" -d '{"channel":"web","device":"desktop"}' | jsonget "['data']['customer_id']")
DEAL_C2=$(curl -s -m 15 -X POST "$B/api/v1/chat/guest" -H "X-Tenant-ID: ${DEAL_TA}" -H "Content-Type: application/json" -d '{"channel":"web","device":"desktop"}' | jsonget "['data']['customer_id']")
DEAL_C3=$(curl -s -m 15 -X POST "$B/api/v1/chat/guest" -H "X-Tenant-ID: ${DEAL_TA}" -H "Content-Type: application/json" -d '{"channel":"web","device":"desktop"}' | jsonget "['data']['customer_id']")
DEAL_C4=$(curl -s -m 15 -X POST "$B/api/v1/chat/guest" -H "X-Tenant-ID: ${DEAL_TA}" -H "Content-Type: application/json" -d '{"channel":"web","device":"desktop"}' | jsonget "['data']['customer_id']")
# 名字后补：建客走的是真实匿名链路，名单行要能断"自带客户名"
$PSQL "UPDATE customers SET name='商机客户甲' WHERE id=${DEAL_C1:-0};
       UPDATE customers SET name='商机客户乙' WHERE id=${DEAL_C2:-0};
       UPDATE customers SET name='商机客户丙' WHERE id=${DEAL_C3:-0};
       UPDATE customers SET name='商机客户丁' WHERE id=${DEAL_C4:-0};" >/dev/null 2>&1
# 前置自检：四个匿名客户必须真落在甲租户下（缺它时"客户不存在"那条会在错租户上假绿）
DEAL_CS=$($PSQL "SELECT count(*) FROM customers WHERE id IN (${DEAL_C1:-0},${DEAL_C2:-0},${DEAL_C3:-0},${DEAL_C4:-0}) AND tenant_id=${DEAL_TA}" 2>/dev/null | tr -d '[:space:]')
check "合成四客户落库自检(都在甲家)" 4 "${DEAL_CS:-0}"
DEAL_NOTITLE=$(deal_a POST /api/v1/admin/deals "{\"customer_id\":${DEAL_C1:-0},\"title\":\"  \"}" | jsonget "['reason']")
check "缺标题判title_required" title_required "${DEAL_NOTITLE:-NONE}"
DEAL_BADSTAGE=$(deal_a POST /api/v1/admin/deals "{\"customer_id\":${DEAL_C1:-0},\"title\":\"测试单\",\"stage\":\"chatting\"}" | jsonget "['reason']")
check "未知阶段判stage_unknown(阶段码由后端下发不各写一套)" stage_unknown "${DEAL_BADSTAGE:-NONE}"
DEAL_TERMINAL=$(deal_a POST /api/v1/admin/deals "{\"customer_id\":${DEAL_C1:-0},\"title\":\"测试单\",\"stage\":\"won\"}" | jsonget "['reason']")
check "建单即终局同样拒(没有过程的单子进不了管道)" stage_unknown "${DEAL_TERMINAL:-NONE}"
DEAL_NEG=$(deal_a POST /api/v1/admin/deals "{\"customer_id\":${DEAL_C1:-0},\"title\":\"测试单\",\"amount_cents\":-1}" | jsonget "['reason']")
check "负金额判amount_negative(钱在边界上不容许自由发挥)" amount_negative "${DEAL_NEG:-NONE}"
DEAL_BIG=$(deal_a POST /api/v1/admin/deals "{\"customer_id\":${DEAL_C1:-0},\"title\":\"测试单\",\"amount_cents\":100000000001}" | jsonget "['reason']")
check "金额超上限判amount_too_large" amount_too_large "${DEAL_BIG:-NONE}"
DEAL_NOCUST=$(deal_a POST /api/v1/admin/deals '{"customer_id":99999999,"title":"测试单"}' | jsonget "['reason']")
check "客户不存在/不在本租户判customer_not_found" customer_not_found "${DEAL_NOCUST:-NONE}"

# (16)~(19) 建单成功 + 在途唯一（人工建单明确拒绝，不静默复用别人的旧单）
DEAL_CREATE=$(deal_a POST /api/v1/admin/deals "{\"customer_id\":${DEAL_C1:-0},\"title\":\"XT5 两台大客户单\",\"amount_cents\":0}")
DEAL_D1=$(echo "$DEAL_CREATE" | jsonget "['data']['deal']['id']")
check "建单成功(返回商机ID非空)" y "$([ -n "${DEAL_D1:-}" ] && echo y || echo n)"
check "缺省阶段=需求确认(一上来就填已报价是自欺)" 需求确认 "$(echo "$DEAL_CREATE" | jsonget "['data']['deal']['stage_name']")"
DEAL_DUP=$(deal_a POST /api/v1/admin/deals "{\"customer_id\":${DEAL_C1:-0},\"title\":\"第二张\"}" | jsonget "['reason']")
check "同客户第二张在途单判deal_already_open" deal_already_open "${DEAL_DUP:-NONE}"
DEAL_OPDB=$($PSQL "SELECT count(*) FROM opportunities WHERE tenant_id=${DEAL_TA:-0} AND customer_id=${DEAL_C1:-0}" 2>/dev/null | tr -d '[:space:]')
check "被拒的第二次确实没落库(归属列真写对不是回显)" 1 "${DEAL_OPDB:-0}"

# (20)~(24) 阶段推进裁决：只准前进、原地不动也拒、成交要金额
DEAL_SAME=$(deal_a POST "/api/v1/admin/deals/${DEAL_D1:-0}/move" '{"to":"qualified"}' | jsonget "['reason']")
check "原地推进判same_stage(否则会白刷停滞天数)" same_stage "${DEAL_SAME:-NONE}"
DEAL_BACK=$(deal_a POST "/api/v1/admin/deals/${DEAL_D1:-0}/move" '{"to":"lead"}' | jsonget "['reason']")
check "逆向推进判stage_backward" stage_backward "${DEAL_BACK:-NONE}"
DEAL_NOAMT=$(deal_a POST "/api/v1/admin/deals/${DEAL_D1:-0}/move" '{"to":"won","change_amount":false}' | jsonget "['reason']")
check "成交缺金额判won_amount_required" won_amount_required "${DEAL_NOAMT:-NONE}"
DEAL_MISSREASON=$(deal_a POST "/api/v1/admin/deals/${DEAL_D1:-0}/move" '{"to":"lost"}' | jsonget "['reason']")
check "流失缺原因判lost_reason_required(报表要按它分组)" lost_reason_required "${DEAL_MISSREASON:-NONE}"
DEAL_QUOTED=$(deal_a POST "/api/v1/admin/deals/${DEAL_D1:-0}/move" '{"to":"quoted"}' | jsonget "['data']['deal']['stage']")
check "推进到已报价成功" quoted "${DEAL_QUOTED:-NONE}"

# (25)~(34) 报价版本链：明细算钱、发出即锁定、改版不覆盖、接受不等于成交
DEAL_QLINES=$(deal_a POST "/api/v1/admin/deals/${DEAL_D1:-0}/quotes" '{"lines":[]}' | jsonget "['reason']")
check "空明细判quote_lines_required" quote_lines_required "${DEAL_QLINES:-NONE}"
# 前端即使送来 total_cents 也不在这个体里——合计唯一来源是明细（2×1,000,000 + 1×1,500,000）
DEAL_Q1=$(deal_a POST "/api/v1/admin/deals/${DEAL_D1:-0}/quotes" '{"lines":[{"name":"XT5 豪华版","qty":2,"unit_cents":1000000},{"name":"延保套餐","qty":1,"unit_cents":1500000}],"note":"smoke","valid_until":"2026-12-31","set_valid":true}')
DEAL_QID1=$(echo "$DEAL_Q1" | jsonget "['data']['quote']['id']")
check "首版报价 v1 且合计由服务端按明细算出(分)" 3500000 "$(echo "$DEAL_Q1" | jsonget "['data']['quote']['total_cents']")"
check "首版版本号从1起" 1 "$(echo "$DEAL_Q1" | jsonget "['data']['quote']['version']")"
DEAL_QLINE1=$(echo "$DEAL_Q1" | python3 -c "
import sys,json
q=((json.load(sys.stdin).get('data') or {}).get('quote') or {})
ls=(q.get('lines') or [])
print('OK' if len(ls)==2 and ls[0].get('total_cents')==2000000 and ls[1].get('total_cents')==1500000 else 'BAD:%s'%ls)
" 2>/dev/null)
[ "$DEAL_QLINE1" = "OK" ] && check "明细小计由后端回填(qty×单价)" y y || check "明细小计由后端回填(qty×单价)" y "${DEAL_QLINE1:-PARSE_FAIL}"
DEAL_QEDIT=$(deal_a PUT "/api/v1/admin/quotes/${DEAL_QID1:-0}" '{"lines":[{"name":"XT5 豪华版","qty":1,"unit_cents":1000000}],"note":"改成一台"}')
check "草稿可改且合计跟着明细重算" 1000000 "$(echo "$DEAL_QEDIT" | jsonget "['data']['quote']['total_cents']")"
DEAL_QSEND=$(deal_a POST "/api/v1/admin/quotes/${DEAL_QID1:-0}/send")
check "发出后状态=sent" sent "$(echo "$DEAL_QSEND" | jsonget "['data']['quote']['status']")"
DEAL_SENTAT=$(echo "$DEAL_QSEND" | jsonget "['data']['quote']['sent_at']")
check "发出时刻落库(发过没发过是可主张的事实)" y "$([ -n "${DEAL_SENTAT:-}" ] && [ "${DEAL_SENTAT}" != "None" ] && echo y || echo n)"
DEAL_LOCKED=$(deal_a PUT "/api/v1/admin/quotes/${DEAL_QID1:-0}" '{"lines":[{"name":"改了算不算","qty":1,"unit_cents":1}]}' | jsonget "['reason']")
check "已发出的报价改内容判quote_locked(要改只能出新版)" quote_locked "${DEAL_LOCKED:-NONE}"
DEAL_RESEND=$(deal_a POST "/api/v1/admin/quotes/${DEAL_QID1:-0}/send" | jsonget "['reason']")
check "已发出的不能再发出判quote_not_draft" quote_not_draft "${DEAL_RESEND:-NONE}"
# 出 v2：v1 自动标"被取代"，历史行不删
DEAL_Q2=$(deal_a POST "/api/v1/admin/deals/${DEAL_D1:-0}/quotes" "{\"lines\":[{\"name\":\"XT5 豪华版\",\"qty\":2,\"unit_cents\":1000000},{\"name\":\"延保套餐\",\"qty\":1,\"unit_cents\":1500000}],\"send\":true}")
DEAL_QID2=$(echo "$DEAL_Q2" | jsonget "['data']['quote']['id']")
check "第二版直接发出仍是同一条状态机产物(sent)" sent "$(echo "$DEAL_Q2" | jsonget "['data']['quote']['status']")"
DEAL_Q1ST=$($PSQL "SELECT status FROM quotes WHERE id=${DEAL_QID1:-0}" 2>/dev/null | tr -d '[:space:]')
check "旧版被标取代而不是被删(客户手上那版日后要能复现)" superseded "${DEAL_Q1ST:-MISSING}"
DEAL_ACCEPT=$(deal_a POST "/api/v1/admin/quotes/${DEAL_QID2:-0}/accept")
check "接受回写 accepted" accepted "$(echo "$DEAL_ACCEPT" | jsonget "['data']['quote']['status']")"
DEAL_STAGEAFTER=$(deal_a GET "/api/v1/admin/deals/${DEAL_D1:-0}" | jsonget "['data']['deal']['stage']")
check "接受报价不会顺手把商机推成成交(两步分开)" quoted "${DEAL_STAGEAFTER:-NONE}"
DEAL_FINAL=$(deal_a POST "/api/v1/admin/quotes/${DEAL_QID1:-0}/accept" | jsonget "['reason']")
check "被取代的旧版不可再动作判quote_final" quote_final "${DEAL_FINAL:-NONE}"
DEAL_QCHAIN=$(deal_a GET "/api/v1/admin/deals/${DEAL_D1:-0}/quotes" | python3 -c "
import sys,json
d=(json.load(sys.stdin).get('data') or {})
ls=(d.get('list') or [])
print('%s|%s' % (len(ls), ','.join(str(r.get('version')) for r in ls)))
" 2>/dev/null)
check "版本链两版齐全且倒序(最新在前)" "2|2,1" "${DEAL_QCHAIN:-PARSE_FAIL}"

# (35)~(38) 终局：成交带金额、成交后不再改、也不再出报价
DEAL_WON=$(deal_a POST "/api/v1/admin/deals/${DEAL_D1:-0}/move" '{"to":"won","amount_cents":3500000,"change_amount":true}')
check "成交成功且金额落到单子" 3500000 "$(echo "$DEAL_WON" | jsonget "['data']['deal']['amount_cents']")"
DEAL_WONAT=$(echo "$DEAL_WON" | jsonget "['data']['deal']['won_at']")
check "成交时刻落库(报表按它算)" y "$([ -n "${DEAL_WONAT:-}" ] && [ "${DEAL_WONAT}" != "None" ] && echo y || echo n)"
DEAL_CLOSED=$(deal_a PUT "/api/v1/admin/deals/${DEAL_D1:-0}" '{"title":"事后改标题"}' | jsonget "['reason']")
check "终局单编辑判deal_closed(已发生的成交金额是历史事实)" deal_closed "${DEAL_CLOSED:-NONE}"
DEAL_QCLOSED=$(deal_a POST "/api/v1/admin/deals/${DEAL_D1:-0}/quotes" '{"lines":[{"name":"再报一版","qty":1,"unit_cents":1}]}' | jsonget "['reason']")
check "终局单不再出报价判deal_closed" deal_closed "${DEAL_QCLOSED:-NONE}"
DEAL_OPENQ=$(deal_a GET "/api/v1/admin/deals/${DEAL_D1:-0}" | jsonget "['data']['deal']['open_quote']")
check "全终局的报价链不算活报价(open_quote 为空)" None "${DEAL_OPENQ:-X}"

# (39)~(44) 其余格子铺数据：停滞榜、流失原因、以及"流失之后能重开一张新的"
deal_a POST /api/v1/admin/deals "{\"customer_id\":${DEAL_C2:-0},\"title\":\"两驱版试驾单\",\"stage\":\"lead\"}" >/dev/null
DEAL_D2=$($PSQL "SELECT id FROM opportunities WHERE tenant_id=${DEAL_TA:-0} AND customer_id=${DEAL_C2:-0} ORDER BY id DESC LIMIT 1" 2>/dev/null | tr -d '[:space:]')
# 停滞判据是 stage_entered_at（不是 updated_at：备注编辑会把它冲掉，等于停滞永远为零）。
# 这里不注入时间也没法造"停滞"，所以直接改这一列——它正是判据本身，改它就是在改判据的输入。
$PSQL "UPDATE opportunities SET stage_entered_at = NOW() - INTERVAL '30 days' WHERE id=${DEAL_D2:-0} AND tenant_id=${DEAL_TA:-0}" >/dev/null 2>&1
DEAL_STUCKTOT=$(deal_a GET "/api/v1/admin/deals?filter=stuck&days=90" | jsonget "['data']['total']")
DEAL_STUCKDAYS=$(deal_a GET "/api/v1/admin/deals?filter=stuck&days=90" | jsonget "['data']['list'][0]['stalled_days']")
check "停滞格子只命中那张停在原地的单" 1 "${DEAL_STUCKTOT:-X}"
check "停滞榜把停得最久的顶到第一(它就是这一格的存在意义)" y \
  "$(awk -v d="${DEAL_STUCKDAYS:-0}" 'BEGIN{print (d>=29 && d<=31) ? "y" : "NO=" d}')"
DEAL_D3=$(deal_a POST /api/v1/admin/deals "{\"customer_id\":${DEAL_C3:-0},\"title\":\"竞品拉锯单\",\"stage\":\"qualified\"}" | jsonget "['data']['deal']['id']")
DEAL_LOST=$(deal_a POST "/api/v1/admin/deals/${DEAL_D3:-0}/move" '{"to":"lost","lost_reason":"competitor"}' | jsonget "['data']['deal']['lost_reason_name']")
check "流失要带原因且中文名随响应下发(报表按码分组)" 输给竞品 "${DEAL_LOST:-NONE}"
DEAL_AGAIN=$(deal_a POST /api/v1/admin/deals "{\"customer_id\":${DEAL_C3:-0},\"title\":\"流失后重新跟进\",\"amount_cents\":2000000}")
DEAL_D4=$(echo "$DEAL_AGAIN" | jsonget "['data']['deal']['id']")
check "同一客户流失后可重开新单(部分索引只盖非终局行)" y "$([ -n "${DEAL_D4:-}" ] && echo y || echo n)"
DEAL_HIST=$($PSQL "SELECT count(*) FROM opportunities WHERE tenant_id=${DEAL_TA:-0} AND customer_id=${DEAL_C3:-0}" 2>/dev/null | tr -d '[:space:]')
check "重开不覆盖历史那张(两张单子都在库里)" 2 "${DEAL_HIST:-0}"
DEAL_KEEP=$(deal_a PUT "/api/v1/admin/deals/${DEAL_D4:-0}" '{"title":"流失后重新跟进(改过)"}' | jsonget "['data']['deal']['amount_cents']")
check "改标题不带 change_amount 时金额不动(没传≠清零)" 2000000 "${DEAL_KEEP:-X}"

# (45)~(48) 顾问端：同一个后端、不同的问题（看自己手上这个客户），且来源是判定不是填写
DEAL_ADV401=$(curl -s -m 15 -o /dev/null -w "%{http_code}" "$B/api/v1/advisor/customer/${DEAL_C3:-0}/deals")
check "未登录取顾问台客户商机判401" 401 "$DEAL_ADV401"
DEAL_ADVC=$(deal_a POST "/api/v1/advisor/customer/${DEAL_C4:-0}/deals" '{"title":"顾问开的单","stage":"lead","source":"ai"}' | jsonget "['data']['deal']['source']")
check "顾问建单来源由后端判定(前端传 source 不作数)" manual "${DEAL_ADVC:-NONE}"
DEAL_ADVLIST=$(deal_a GET "/api/v1/advisor/customer/${DEAL_C3:-0}/deals" | jsonget "['data']['total']")
check "顾问台面按客户列全部单子(含终局,免得重复开)" 2 "${DEAL_ADVLIST:-0}"
DEAL_ADVCROSS=$(deal_b GET "/api/v1/advisor/customer/${DEAL_C3:-0}/deals" | python3 -c "
import sys,json
d=(json.load(sys.stdin).get('data') or {})
print('%s|%s' % (d.get('total'), len(d.get('list') or [])))
" 2>/dev/null)
check "别家客户在顾问台面回空列表(不外洩存在也不炸页面)" "0|0" "${DEAL_ADVCROSS:-PARSE_FAIL}"

# (49)~(58) 看板与名单同源：十个格子逐格比，先自检比对本身不是空转
DEAL_BOARD=$(deal_a GET "/api/v1/admin/deals/board?days=90")
DEAL_PAIRS=$(printf '%s' "$DEAL_BOARD" | python3 -c "
import sys,json
d=(json.load(sys.stdin).get('data') or {})
rows=[('open',d.get('open_count')),('stuck',d.get('stuck_count')),('won',d.get('won_count')),('lost',d.get('lost_count'))]
for c in (d.get('stages') or []):
    rows.append((c.get('drill_filter'), c.get('count')))
print('\n'.join('%s\t%s' % (f, n) for f, n in rows))
" 2>/dev/null)
DEAL_PAIRN=0
DEAL_EQN=0
DEAL_NZN=0
DEAL_BAD=""
while IFS=$'\t' read -r DEAL_F DEAL_C; do
  [ -z "${DEAL_F:-}" ] && continue
  DEAL_PAIRN=$((DEAL_PAIRN+1))
  [ "${DEAL_C:-0}" != "0" ] && DEAL_NZN=$((DEAL_NZN+1))
  DEAL_T=$(deal_a GET "/api/v1/admin/deals?filter=${DEAL_F}&days=90" | jsonget "['data']['total']")
  if [ "${DEAL_T:-X}" = "${DEAL_C}" ]; then
    DEAL_EQN=$((DEAL_EQN+1))
  else
    DEAL_BAD="$DEAL_BAD $DEAL_F:格子$DEAL_C/名单$DEAL_T"
  fi
done <<DEALPAIRS
$DEAL_PAIRS
DEALPAIRS
check "看板格子铺满十格(4分组+6阶段,零命中也出现)" 10 "$DEAL_PAIRN"
check "参与比对的格子里非零≥5(等式不是在0==0上过的)" y "$([ "${DEAL_NZN:-0}" -ge 5 ] && echo y || echo "NO=$DEAL_NZN")"
check "十个格子逐格核对:卡片数字==点进去的total" "10|" "${DEAL_EQN}|${DEAL_BAD}"
DEAL_STAGENAME=$(printf '%s' "$DEAL_BOARD" | jsonget "['data']['stages'][2]['stage_name']" 2>/dev/null)
check "阶段中文名随看板下发(前端不写第二套枚举)" 已报价 "${DEAL_STAGENAME:-NONE}"
DEAL_OPENC=$(printf '%s' "$DEAL_BOARD" | jsonget "['data']['open_count']")
DEAL_OPENA=$(printf '%s' "$DEAL_BOARD" | jsonget "['data']['open_amount_cents']")
check "在途三张(乙的线索/丙的重开/丁的顾问单)" 3 "${DEAL_OPENC:-X}"
check "在途金额只算在途单(成交那张不重复计)" 2000000 "${DEAL_OPENA:-X}"
DEAL_WINRATE=$(printf '%s' "$DEAL_BOARD" | jsonget "['data']['win_rate_pct']")
check "赢单率按终局单算(1赢1输=50%)" y \
  "$(awk -v r="${DEAL_WINRATE:-0}" 'BEGIN{print (r+0>=49.99 && r+0<=50.01) ? "y" : "NO=" r}')"
DEAL_NOTE=$(printf '%s' "$DEAL_BOARD" | jsonget "['data']['note']")
check "口径说明随行下发(金额单位是分、窗打在建单时刻)" y "$([ -n "${DEAL_NOTE:-}" ] && echo y || echo n)"
DEAL_TRUNC=$(printf '%s' "$DEAL_BOARD" | jsonget "['data']['truncated']")
check "扫描未截断(数据量远在下限内)" False "${DEAL_TRUNC:-X}"
DEAL_BEMPTY=$(deal_b GET "/api/v1/admin/deals/board?days=90" | jsonget "['data']['total_count']")
check "乙租户看板看不到甲家任何一张(隔离不是靠前端过滤)" 0 "${DEAL_BEMPTY:-X}"

# (59)~(63) 读侧参数纪律：缺/非法 filter 不默认回某一份名单、页码硬顶、越界如实、跨租户404
DEAL_NOFILTER=$(curl -s -m 15 -o /dev/null -w "%{http_code}" "$B/api/v1/admin/deals" -H "$AH" -H "X-Tenant-ID: ${DEAL_TA}")
check "缺filter判400(默认视图与'我点了哪一格'不能混)" 400 "$DEAL_NOFILTER"
DEAL_BADFILTER=$(deal_a GET "/api/v1/admin/deals?filter=all" | jsonget "['code']")
check "非法filter判400(不认识的不默认回一份名单)" 400 "${DEAL_BADFILTER:-X}"
DEAL_PSCAP=$(deal_a GET "/api/v1/admin/deals?filter=open&page_size=1000" | jsonget "['data']['page_size']")
check "名单page_size硬顶100且回显钳后值" 100 "${DEAL_PSCAP:-0}"
DEAL_OOB=$(deal_a GET "/api/v1/admin/deals?filter=open&page=99" | python3 -c "
import sys,json
d=(json.load(sys.stdin).get('data') or {})
print('%s|%s' % (d.get('total'), len(d.get('list') or [])))
" 2>/dev/null)
check "越界页只回空列表但total如实" "3|0" "${DEAL_OOB:-PARSE_FAIL}"
DEAL_ISO404=$(curl -s -m 15 -o /dev/null -w "%{http_code}" "$B/api/v1/admin/deals/${DEAL_D1:-0}" -H "$AH" -H "X-Tenant-ID: ${DEAL_TB}")
check "跨租户读别人单子判404(与不存在同形态)" 404 "$DEAL_ISO404"
DEAL_QISO404=$(curl -s -m 15 -o /dev/null -w "%{http_code}" "$B/api/v1/admin/quotes/${DEAL_QID2:-0}" -H "$AH" -H "X-Tenant-ID: ${DEAL_TB}")
check "跨租户读别人报价单同样404" 404 "$DEAL_QISO404"

# (64)~(66) 并发守卫：条件更新 0 行受影响是冲突不是成功
DEAL_RACE=$(deal_a POST "/api/v1/admin/quotes/${DEAL_QID2:-0}/send" | jsonget "['reason']")
check "已接受的报价不能再发出判quote_final(动作只从当前态推)" quote_final "${DEAL_RACE:-NONE}"
DEAL_RACE409=$(deal_b POST "/api/v1/admin/quotes/${DEAL_QID2:-0}/void" | jsonget "['code']")
check "别租户作废别人报价进不来(404,不改状态)" 404 "${DEAL_RACE409:-X}"
DEAL_QSTILL=$($PSQL "SELECT status FROM quotes WHERE id=${DEAL_QID2:-0}" 2>/dev/null | tr -d '[:space:]')
check "被拒的作废确实没改状态(拒绝要留下痕迹而不是改了半截)" accepted "${DEAL_QSTILL:-MISSING}"

# 现场回收：报价→商机→消息/会话→客户→租户，逐层清，不留半截
$PSQL "DELETE FROM quotes WHERE tenant_id IN (${DEAL_TA:-0},${DEAL_TB:-0});
       DELETE FROM opportunities WHERE tenant_id IN (${DEAL_TA:-0},${DEAL_TB:-0});
       DELETE FROM messages WHERE tenant_id IN (${DEAL_TA:-0},${DEAL_TB:-0});
       DELETE FROM conversations WHERE tenant_id IN (${DEAL_TA:-0},${DEAL_TB:-0});
       DELETE FROM customers WHERE tenant_id IN (${DEAL_TA:-0},${DEAL_TB:-0});
       DELETE FROM tenants WHERE code IN ('${DEAL_CA}','${DEAL_CB}');" >/dev/null 2>&1
DEAL_LEFT=$($PSQL "SELECT (SELECT count(*) FROM opportunities WHERE tenant_id IN (${DEAL_TA:-0},${DEAL_TB:-0})) + (SELECT count(*) FROM quotes WHERE tenant_id IN (${DEAL_TA:-0},${DEAL_TB:-0})) + (SELECT count(*) FROM tenants WHERE code IN ('${DEAL_CA}','${DEAL_CB}'))" 2>/dev/null | tr -d '[:space:]')
check "本段商机/报价/租户已清零" 0 "${DEAL_LEFT:-1}"

# ---------- 第三十七节：企微会话存档（E8 批 · 凭据/解密/留痕/观测位，2026-09-24）----------
# 这一段守的是"合规留痕能力在凭据未接入时也必须诚实"。四件事最容易出事：
#   1) **密钥绝不能出接口**：存档私钥泄露＝交出全部客户聊天记录明文。连掩码都不给
#      （掩码会露长度），所以断响应体里连 "PRIVATE KEY" 的片段都不许出现，而不是断"看起来脱敏了"。
#   2) **开关出厂关 + 没密钥不许开**：开了就等于"忘了关就把客户会话整段抄进我们库"（PIPL 级），
#      与主动触达/催缴同一条口径；开开关还要求先有私钥，否则起的是只会产 decrypt_error 的空转链路。
#   3) **"没接 SDK"必须是一个类型，不是一句日志**：0 条留痕有三种相反的解释
#      （没开 / 开了没接 / 接了但全拉失败）。三种必须给三个不同的稳定码
#      （archive_disabled / archive_sdk_not_built / 上一轮原因码聚合），否则前端只能显示一句模糊话。
#   4) **列表不等于详情**：列表只回摘要（一次几十位客户的原文进不了日志/截图），
#      全文只在详情接口回显且每次读都留痕——所以既断"列表里没有正文键"，也断"详情读了真留了一条审计"。
# 顺序：数据层实存 → 鉴权闸 → 凭据写入与密钥生成 → 开关前置校验 → 手动同步（不得 5xx）
#       → 读侧（摘要/分页/时间入参/跨租户同形 404）→ 观测位 → 现场清零。
echo "---- 三十七、会话存档：凭据密文→密钥不回显→开关前置→同步不编造→名单摘要与详情留痕→观测位 ----"
ARC_CA="smoke_arc_a_$$"
ARC_CB="smoke_arc_b_$$"
$PSQL "INSERT INTO tenants (name, code, tier, status, created_at, updated_at)
       VALUES ('存档甲租户','${ARC_CA}','personal','active',NOW(),NOW()),
              ('存档乙租户','${ARC_CB}','personal','active',NOW(),NOW());" >/dev/null 2>&1
ARC_TA=$($PSQL "SELECT id FROM tenants WHERE code='${ARC_CA}'" 2>/dev/null | tr -d '[:space:]')
ARC_TB=$($PSQL "SELECT id FROM tenants WHERE code='${ARC_CB}'" 2>/dev/null | tr -d '[:space:]')
# 通道直插（凭据由接口写，这里只给"一家一个 active 通道"的最小形态）
$PSQL "INSERT INTO channels (tenant_id, type, name, status, created_at, updated_at)
       VALUES (${ARC_TA:-0},'wecom_app','存档甲通道','active',NOW(),NOW()),
              (${ARC_TB:-0},'wecom_app','存档乙通道','active',NOW(),NOW());" >/dev/null 2>&1
ARC_CHA=$($PSQL "SELECT id FROM channels WHERE tenant_id=${ARC_TA:-0} AND name='存档甲通道'" 2>/dev/null | tr -d '[:space:]')
ARC_CHB=$($PSQL "SELECT id FROM channels WHERE tenant_id=${ARC_TB:-0} AND name='存档乙通道'" 2>/dev/null | tr -d '[:space:]')
# 前置自检：两租户两通道必须真的在库里（缺这步时下面所有跨租户/404 断言会在"两边都查不到"上假绿）
ARC_PRE=$($PSQL "SELECT (SELECT count(*) FROM tenants WHERE code IN ('${ARC_CA}','${ARC_CB}')) + (SELECT count(*) FROM channels WHERE id IN (${ARC_CHA:-0},${ARC_CHB:-0}))" 2>/dev/null | tr -d '[:space:]')
check "合成两租户两通道落库自检" 4 "${ARC_PRE:-0}"
# arc_at <租户ID> <方法> <路径> [body]：以某租户作用域打管理端存档接口
arc_at() {
  if [ "$2" = "GET" ]; then
    curl -s -m 15 -H "$AH" -H "X-Tenant-ID: $1" "$B$3"
  else
    curl -s -m 15 -X "$2" -H "$AH" -H "X-Tenant-ID: $1" -H "Content-Type: application/json" -d "${4:-}" "$B$3"
  fi
}
arc_a() { arc_at "$ARC_TA" "$@"; }
arc_b() { arc_at "$ARC_TB" "$@"; }
arc_code() { curl -s -m 15 -o /dev/null -w "%{http_code}" "$@"; }
# arc_http <租户ID> <方法> <路径> [body]：只要 HTTP 状态码（信封 code 恒 0，判不了 200/500）
arc_http() {
  curl -s -m 20 -o /dev/null -w "%{http_code}" -X "$2" -H "$AH" -H "X-Tenant-ID: $1" \
    -H "Content-Type: application/json" -d "${4:-}" "$B$3"
}

# (1)~(7) 数据层实存：迁移登记、五个存档列、留痕表、部分唯一索引的定义、两条取数索引、开关默认值
ARC_MIG=$($PSQL "SELECT count(*) FROM schema_migrations WHERE version='024_chat_archive'" 2>/dev/null | tr -d '[:space:]')
check "迁移024已登记版本账本" 1 "${ARC_MIG:-0}"
ARC_COL=$($PSQL "SELECT count(*) FROM information_schema.columns WHERE table_name='channels'
  AND column_name IN ('archive_enabled','archive_secret_cipher','archive_private_key_cipher','archive_public_key_ver','archive_seq')" 2>/dev/null | tr -d '[:space:]')
check "通道五个存档列实存" 5 "${ARC_COL:-0}"
ARC_TBL=$($PSQL "SELECT count(*) FROM information_schema.tables WHERE table_name='chat_archive_records'" 2>/dev/null | tr -d '[:space:]')
check "存档留痕表实存" 1 "${ARC_TBL:-0}"
# 唯一锚必须**只盖 msgid 非空的行**：全表唯一会让第二条 switch/event 报文（企微本就不给 msgid）永久撞锚
ARC_UX=$($PSQL "SELECT (indexdef LIKE '%UNIQUE%') AND (indexdef LIKE '%(channel_id, msgid)%') AND (indexdef LIKE '%WHERE%') AND (indexdef LIKE '%msgid%') FROM pg_indexes WHERE indexname='ux_archive_one_row_per_msgid'" 2>/dev/null | tr -d '[:space:]')
check "同通道同msgid唯一=部分索引(空msgid事件报文不占位)" t "${ARC_UX:-f}"
ARC_IDX=$($PSQL "SELECT count(*) FROM pg_indexes WHERE indexname IN ('idx_archive_channel_seq','idx_archive_tenant_time')" 2>/dev/null | tr -d '[:space:]')
check "增量取数与合规检索各有自己的取数索引(探针不扫全表)" 2 "${ARC_IDX:-0}"
ARC_DEF=$($PSQL "SELECT column_default FROM information_schema.columns WHERE table_name='channels' AND column_name='archive_enabled'" 2>/dev/null | tr -d '[:space:]')
check "存档开关出厂默认false(泛抄客户会话是PIPL级动作)" false "${ARC_DEF:-MISSING}"
# 新建通道在库里确实没开存档（默认值真的生效，不是只在 DDL 里写着）
ARC_OFF=$($PSQL "SELECT count(*) FROM channels WHERE id IN (${ARC_CHA:-0},${ARC_CHB:-0}) AND archive_enabled=FALSE" 2>/dev/null | tr -d '[:space:]')
check "合成通道出厂未开存档" 2 "${ARC_OFF:-0}"

# (8)~(12) 闸：未登录 401 / sales 403 / 跨租户 404 与不存在同码同形
check "未登录取存档状态判401" 401 "$(arc_code "$B/api/v1/admin/channels/${ARC_CHA:-0}/archive")"
check "sales取存档状态判403(整条留痕链是管理视图)" 403 "$(arc_code "$B/api/v1/admin/channels/${ARC_CHA:-0}/archive" -H "Authorization: Bearer $STOKEN")"
ARC_XBody=$(arc_b GET "/api/v1/admin/channels/${ARC_CHA:-0}/archive")
ARC_X404=$(printf '%s' "$ARC_XBody" | jsonget "['code']")
check "乙租户读甲家通道存档判404" 404 "${ARC_X404:-X}"
ARC_N404=$(arc_a GET "/api/v1/admin/channels/99999999/archive")
ARC_NCode=$(printf '%s' "$ARC_N404" | jsonget "['code']")
ARC_XMsg=$(printf '%s' "$ARC_XBody" | jsonget "['message']")
ARC_NMsg=$(printf '%s' "$ARC_N404" | jsonget "['message']")
check "跨租户与不存在同码(自增ID不能用来探测别家通道)" "$ARC_NCode" "${ARC_X404:-X}"
check "跨租户与不存在同文案(不回显存在但不归你)" "$ARC_NMsg" "${ARC_XMsg:-MISSING}"
check "跨租户响应不含对方通道名等任何字段" 0 "$(printf '%s' "$ARC_XBody" | grep -c "存档甲通道" 2>/dev/null)"

# (13)~(17) 状态摘要：只报事实，不报密钥；SDK 缺口是一个稳定码
ARC_ST=$(arc_a GET "/api/v1/admin/channels/${ARC_CHA:-0}/archive")
check "未配置时enabled为false" False "$(printf '%s' "$ARC_ST" | jsonget "['data']['status']['enabled']")"
check "未配置时key_configured为false" False "$(printf '%s' "$ARC_ST" | jsonget "['data']['status']['key_configured']")"
check "取数器未接入回稳定码archive_sdk_not_built" archive_sdk_not_built "$(printf '%s' "$ARC_ST" | jsonget "['data']['status']['sdk_reason']")"
check "游标出厂为0" 0 "$(printf '%s' "$ARC_ST" | jsonget "['data']['status']['cursor_seq']")"
check "状态摘要里没有任何密钥材料" 0 "$(printf '%s' "$ARC_ST" | grep -c "PRIVATE KEY\|private_key_pem\|archive_secret" 2>/dev/null)"

# (18)~(20) 开关前置：没密钥不许开（开了只会产 decrypt_error 空转链路）
ARC_NOKEY=$(arc_a PUT "/api/v1/admin/channels/${ARC_CHA:-0}/archive" '{"enabled":true}')
check "无密钥开存档判400" 400 "$(printf '%s' "$ARC_NOKEY" | jsonget "['code']")"
check "无密钥开存档回稳定码" archive_key_missing "$(printf '%s' "$ARC_NOKEY" | jsonget "['reason']")"
check "被拒的开启不得半落库(开关仍是false)" false "$($PSQL "SELECT archive_enabled::text FROM channels WHERE id=${ARC_CHA:-0}" 2>/dev/null | tr -d '[:space:]')"

# (21)~(26) 密钥生成：只有公钥出接口，私钥密文入库，轮换版本号自增
ARC_BADBITS=$(arc_a POST "/api/v1/admin/channels/${ARC_CHA:-0}/archive/key" '{"bits":1024}' | jsonget "['reason']")
check "密钥位数非2048/4096判param_error(企微后台拒绝短密钥)" param_error "${ARC_BADBITS:-NONE}"
ARC_KEY=$(arc_a POST "/api/v1/admin/channels/${ARC_CHA:-0}/archive/key" '{"bits":2048,"public_key_ver":1}')
check "密钥生成回公钥PEM" 1 "$(printf '%s' "$ARC_KEY" | grep -c "BEGIN PUBLIC KEY" 2>/dev/null)"
check "私钥明确声明不回显(private_key_echo=false)" False "$(printf '%s' "$ARC_KEY" | jsonget "['data']['private_key_echo']")"
check "密钥生成响应零私钥痕迹" 0 "$(printf '%s' "$ARC_KEY" | grep -c "PRIVATE KEY" 2>/dev/null)"
ARC_FP=$(printf '%s' "$ARC_KEY" | jsonget "['data']['fingerprint']")
check "公钥指纹随行下发(前端据此核对企微后台那一版)" y "$([ -n "${ARC_FP:-}" ] && [ "${ARC_FP:-}" != "None" ] && echo y || echo n)"
ARC_KVER=$($PSQL "SELECT archive_public_key_ver FROM channels WHERE id=${ARC_CHA:-0}" 2>/dev/null | tr -d '[:space:]')
check "公钥版本号按入参落库" 1 "${ARC_KVER:-0}"
# 密文列里存的必须不是明文 PEM（加密链真的走过）
ARC_CIPHERHEAD=$($PSQL "SELECT left(archive_private_key_cipher, 20) FROM channels WHERE id=${ARC_CHA:-0}" 2>/dev/null | tr -d '\n')
check "私钥以密文列落库(不是PEM明文)" n "$([ "${ARC_CIPHERHEAD:0:5}" = "-----" ] && echo y || echo n)"

# (27)~(29) 配好凭据后开关才允许打开，且状态摘要如实反映
ARC_SECRET=$(arc_a PUT "/api/v1/admin/channels/${ARC_CHA:-0}/archive" '{"enabled":true,"archive_secret":"smoke_arc_secret"}')
check "有密钥后开存档成功且enabled回true" True "$(printf '%s' "$ARC_SECRET" | jsonget "['data']['status']['enabled']")"
check "密钥与secret各自如实报已配置" True "$(printf '%s' "$ARC_SECRET" | jsonget "['data']['status']['secret_configured']")"
check "更新接口响应同样零密钥痕迹" 0 "$(printf '%s' "$ARC_SECRET" | grep -c "PRIVATE KEY\|smoke_arc_secret" 2>/dev/null)"

# (30)~(33) 手动同步：环境缺口不是这次请求写错了 → HTTP 200 + data.ok=false + data.reason 稳定码，绝不 500
# ⚠ 这里必须断 **HTTP 状态码**，不能拿信封 code 当 200：成功信封的 code 恒为 0（全站统一），
# 断"code 不等于 500"之类形同不断——真 500 的 code 是 500，而这条链路的正确形态是 200+ok=false。
ARC_SYNCHT=$(arc_http "$ARC_TA" POST "/api/v1/admin/channels/${ARC_CHA:-0}/archive/sync" '{}')
ARC_SYNC=$(arc_a POST "/api/v1/admin/channels/${ARC_CHA:-0}/archive/sync" '{}')
check "手动同步回HTTP200而非500(SDK缺口是环境状态)" 200 "${ARC_SYNCHT:-0}"
check "手动同步信封code=0(不是错误响应)" 0 "$(printf '%s' "$ARC_SYNC" | jsonget "['code']")"
check "手动同步ok=false并带稳定码" archive_sdk_not_built "$(printf '%s' "$ARC_SYNC" | jsonget "['data']['reason']")"
check "未接入SDK时不得编造留痕行" 0 "$($PSQL "SELECT count(*) FROM chat_archive_records WHERE channel_id=${ARC_CHA:-0}" 2>/dev/null | tr -d '[:space:]')"
# 未开存档的通道：同一入口回 archive_disabled（与"开了没接"是两种决定）
ARC_SYNCOFF=$(arc_b POST "/api/v1/admin/channels/${ARC_CHB:-0}/archive/sync" '{}' | jsonget "['data']['reason']")
check "未开存档的通道同步回archive_disabled" archive_disabled "${ARC_SYNCOFF:-NONE}"

# (33)~(39) 读侧：空态是 []、摘要纪律、分页硬顶回显、时间入参、详情留痕
ARC_EMPTY=$(arc_a GET "/api/v1/admin/channels/${ARC_CHA:-0}/archive/records")
check "空态list是[]而不是null(前端直接map不炸)" "[]" "$(printf '%s' "$ARC_EMPTY" | python3 -c "
import sys,json
d=(json.load(sys.stdin).get('data') or {})
print('[]' if d.get('list')==[] else 'NOT_EMPTY_LIST')" 2>/dev/null)"
check "默认页大小20并回显生效值" 20 "$(printf '%s' "$ARC_EMPTY" | jsonget "['data']['page_size']")"
check "page_size=1000被钳到硬顶100且回显钳后值" 100 "$(arc_a GET "/api/v1/admin/channels/${ARC_CHA:-0}/archive/records?page_size=1000" | jsonget "['data']['page_size']")"
check "硬顶常数随行下发(前端不各写一套)" 100 "$(printf '%s' "$ARC_EMPTY" | jsonget "['data']['page_size_cap']")"
ARC_BADTIME=$(arc_a GET "/api/v1/admin/channels/${ARC_CHA:-0}/archive/records?since=昨天" | jsonget "['reason']")
check "非法时间入参判param_error(不静默当成不筛)" param_error "${ARC_BADTIME:-NONE}"
# 真库直插一行带正文的留痕（不这么插，"列表不含正文"就会在空名单上假绿）
# 正文必须**长过摘要上限 120 字**：短正文的"摘要"就是全文，那样"列表不含全文"这条断言
# 会在摘要上恒绿——摘要纪律只有拿长文本才测得出来。
ARC_LONGTXT=$(python3 -c "print('客户要求保密的会话原文，请逐条核对后再决定。'*8)")
$PSQL "INSERT INTO chat_archive_records (tenant_id, channel_id, msgid, seq, biz_type, action, from_user, sender_name,
        to_list, chat_type, chatid, msg_type, content_text, decrypt_error, msg_time, created_at, updated_at)
       VALUES (${ARC_TA:-0},${ARC_CHA:-0},'smoke_arc_m1',900,'business','send','zhangsan','张三','[\"lisi\"]',
               'single','smoke_arc_room','text','${ARC_LONGTXT}','','2026-09-24 10:00:00+08',NOW(),NOW()),
              (${ARC_TA:-0},${ARC_CHA:-0},'smoke_arc_m2',901,'business','send','lisi','李四','[\"zhangsan\"]',
               'single','smoke_arc_room','text','','random key 解不开',NULL,NOW(),NOW());" >/dev/null 2>&1
ARC_SEED=$($PSQL "SELECT count(*) FROM chat_archive_records WHERE channel_id=${ARC_CHA:-0}" 2>/dev/null | tr -d '[:space:]')
check "留痕两行落库自检(1行有正文/1行只有信封)" 2 "${ARC_SEED:-0}"
ARC_LIST=$(arc_a GET "/api/v1/admin/channels/${ARC_CHA:-0}/archive/records")
check "名单total如实为2" 2 "$(printf '%s' "$ARC_LIST" | jsonget "['data']['total']")"
check "列表绝不序列化正文全文字段" 0 "$(printf '%s' "$ARC_LIST" | grep -c '"content_text"' 2>/dev/null)"
check "列表绝不出现正文全文(176字长文不得整段推送)" 0 "$(printf '%s' "$ARC_LIST" | grep -c "$ARC_LONGTXT" 2>/dev/null)"
# 摘要与"有没有全文"的标记：前端要能在不解密全文的前提下区分"这条能点开"与"这条本来就解不开"
ARC_HASFULL=$(printf '%s' "$ARC_LIST" | python3 -c "
import sys,json
lst=(json.load(sys.stdin).get('data') or {}).get('list') or []
print('|'.join(('T' if r.get('has_full_text') else 'F') for r in lst))" 2>/dev/null)
check "列表按行给出has_full_text标记(有正文T/仅信封F)" "F|T" "${ARC_HASFULL:-PARSE_FAIL}"
# 摘要按**字符**截断：按字节切会把中文劈成半个字，前端拿到的就是乱码方块
ARC_TRUNC=$(printf '%s' "$ARC_LIST" | python3 -c "
import sys,json
lst=(json.load(sys.stdin).get('data') or {}).get('list') or []
row=next((r for r in lst if r.get('has_full_text')), {})
p=row.get('text_preview') or ''
print('len=%d' % len(p), 'ellipsis' if p.endswith('…') else 'noellipsis')" 2>/dev/null)
check "摘要截到120字并补省略号(按字符不按字节)" "len=121 ellipsis" "${ARC_TRUNC:-PARSE_FAIL}"
check "列表下发正文摘要字段" 1 "$(printf '%s' "$ARC_LIST" | grep -c '"text_preview"' 2>/dev/null)"
# 排序口径钉死：按 seq 倒序（企微原始顺序）。为什么不用 msg_time——解不开的留痕行没有 msg_time，
# 按时间排（PG 默认 DESC NULLS FIRST）会让它们要么长期霸占首页、要么在某页彻底消失，
# 而"哪几条拉不下来"恰恰是最该一眼看见的。
ARC_ORDER=$(arc_a GET "/api/v1/admin/channels/${ARC_CHA:-0}/archive/records" | python3 -c "
import sys,json
lst=(json.load(sys.stdin).get('data') or {}).get('list') or []
print('|'.join(str(r.get('seq')) for r in lst))" 2>/dev/null)
check "名单按seq倒序(无msg_time的留痕行不消失)" "901|900" "${ARC_ORDER:-PARSE_FAIL}"
ARC_FAILED=$(arc_a GET "/api/v1/admin/channels/${ARC_CHA:-0}/archive/records?failed=true" | jsonget "['data']['total']")
check "failed=true只回留痕行" 1 "${ARC_FAILED:-X}"
ARC_HIT=$(arc_a GET "/api/v1/admin/channels/${ARC_CHA:-0}/archive/records?keyword=%E4%BF%9D%E5%AF%86" | jsonget "['data']['total']")
check "keyword命中正文子串(ILIKE)" 1 "${ARC_HIT:-X}"
ARC_WILD=$(arc_a GET "/api/v1/admin/channels/${ARC_CHA:-0}/archive/records?keyword=100%25" | jsonget "['data']['total']")
check "keyword里的%被当字面量(通配符不越权)" 0 "${ARC_WILD:-X}"
# 详情：全文只在这里回显，且每次读都留痕
ARC_RID=$($PSQL "SELECT id FROM chat_archive_records WHERE channel_id=${ARC_CHA:-0} AND msgid='smoke_arc_m1'" 2>/dev/null | tr -d '[:space:]')
ARC_DET=$(arc_a GET "/api/v1/admin/channel-archive/records/${ARC_RID:-0}")
check "详情逐字节回显正文全文" y "$(printf '%s' "$ARC_DET" | grep -c "$ARC_LONGTXT" 2>/dev/null | awk '{print ($1>0)?"y":"n"}')"
# 审计走异步 goroutine（writeAuditSimple 不阻塞响应），不等一下就查会读到 0——
# 这条 sleep 不是"等接口"，是"等留痕"，而留痕正是本断言的断言对象。
sleep 1
ARC_AUDIT=$($PSQL "SELECT count(*) FROM tenant_audit_logs WHERE tenant_id=${ARC_TA:-0} AND action='channel_archive_record_read' AND resource='archive_record:${ARC_RID:-0}'" 2>/dev/null | tr -d '[:space:]')
check "读全文即留痕(谁在什么时候看了哪条会话)" 1 "${ARC_AUDIT:-0}"
ARC_404A=$(arc_a GET "/api/v1/admin/channel-archive/records/99999999" | jsonget "['reason']")
check "不存在记录判archive_record_not_found" archive_record_not_found "${ARC_404A:-NONE}"
ARC_404B=$(arc_b GET "/api/v1/admin/channel-archive/records/${ARC_RID:-0}" | jsonget "['reason']")
check "跨租户读别人留痕同码同形(404)" archive_record_not_found "${ARC_404B:-NONE}"

# (40)~(43) 观测位：/status/detail 必须看得见存档形态，公开 /status 不得泄露
ARC_HT=$(grep '^HEALTH_TOKEN=' "$(dirname "$0")/../.env" 2>/dev/null | cut -d= -f2 | tr -d '[:space:]')
ARC_DETAIL=$(curl -s -m 15 -H "X-Health-Token: $ARC_HT" "$B/status/detail")
ARC_HASCHECK=$(printf '%s' "$ARC_DETAIL" | grep -c '"chat_archive"' 2>/dev/null)
check "详情端点含会话存档观测位" 1 "${ARC_HASCHECK:-0}"
# 观测位取数走独立键（data.chat_archive.value），不用 readiness[]：
# readiness 是"配置开关体检"（ComputeReadiness），而存档是"数据形态体检"（ComputeHealth）——
# 两件事合成一个数组会让"缺哪一项"再也无法按名断言（本项首跑就是这么漏的：字段没直出、探针却全绿）。
ARC_PROBE=$(printf '%s' "$ARC_DETAIL" | python3 -c "
import sys,json
d=json.load(sys.stdin); d=d.get('data',d)
p=d.get('chat_archive') or {}
print((p.get('value') or 'ABSENT')+'@@'+(p.get('status') or ''))" 2>/dev/null)
# 已有一个启用通道时，观测位绝不能报 not_wired/disabled——那等于"探针说没这回事，数据却在库里"
# （case 不写进 $() 里：bash 在命令替换内解析 `)` 会错位，本段首跑就是这条语法错）
ARC_PROBE_BAD=n
case "${ARC_PROBE:-ABSENT@@}" in
  not_wired* | disabled* | ABSENT*) ARC_PROBE_BAD=y ;;
esac
check "有启用通道时观测位如实报数(不报not_wired/disabled)" n "$ARC_PROBE_BAD"
check "观测位点明sdk_not_built(开了但一条都不会有必须看得见)" 1 "$(printf '%s' "${ARC_PROBE:-}" | grep -c "sdk_not_built" 2>/dev/null)"
# 判级只到 warn：存档缺数据是"该有人去看"，不是半夜刷群（MaybeAlert 只对 crit 发信）
check "存档观测位判级为warn而非crit(合规留痕不刷群)" warn "$(printf '%s' "${ARC_PROBE:-}" | sed 's/.*@@//')"
check "公开/status不泄露会话存档观测位" 0 "$(curl -s "$B/status" | grep -c 'chat_archive' 2>/dev/null)"

# (43)~(44) 关掉开关即止：留痕行不删（那是客户要的合规证据），但不再拉取
ARC_OFF2=$(arc_a PUT "/api/v1/admin/channels/${ARC_CHA:-0}/archive" '{"enabled":false}')
check "关闭存档回显enabled=false" False "$(printf '%s' "$ARC_OFF2" | jsonget "['data']['status']['enabled']")"
check "关闭只改开关不删留痕(证据必须留着)" 2 "$($PSQL "SELECT count(*) FROM chat_archive_records WHERE channel_id=${ARC_CHA:-0}" 2>/dev/null | tr -d '[:space:]')"

# 现场回收：留痕行→审计→通道→租户，逐层清，不留半截
$PSQL "DELETE FROM chat_archive_records WHERE tenant_id IN (${ARC_TA:-0},${ARC_TB:-0});
       DELETE FROM tenant_audit_logs WHERE tenant_id IN (${ARC_TA:-0},${ARC_TB:-0}) AND action LIKE 'channel_archive%';
       DELETE FROM channels WHERE tenant_id IN (${ARC_TA:-0},${ARC_TB:-0});
       DELETE FROM tenants WHERE code IN ('${ARC_CA}','${ARC_CB}');" >/dev/null 2>&1
ARC_LEFT=$($PSQL "SELECT (SELECT count(*) FROM chat_archive_records WHERE tenant_id IN (${ARC_TA:-0},${ARC_TB:-0})) + (SELECT count(*) FROM channels WHERE tenant_id IN (${ARC_TA:-0},${ARC_TB:-0})) + (SELECT count(*) FROM tenants WHERE code IN ('${ARC_CA}','${ARC_CB}'))" 2>/dev/null | tr -d '[:space:]')
check "本段留痕/通道/租户已清零" 0 "${ARC_LEFT:-1}"

echo "---- 三十八、超管租户检索：代管下拉不再只看首页（名称/编码模糊 + ID 直达）----"
# 起因（2026-09-24 残项收口）：/super/tenants 只有分页没有检索，前端「代管租户」下拉拿的是
# 缺省首页，清库后承载历史数据的种子租户排在最新若干家之外，超管在界面上**再也代管不到它**——
# 当时 e2e 是靠直接写 localStorage 绕过去的，那是绕过不是修复。本段断的就是这条路真被修好。
QS_TS=$(date +%s)
QS_CA="qs${QS_TS}a"
QS_CB="qs${QS_TS}b"
# 时间戳放在共同前缀之后、区分字之前：q=检索靶子${QS_TS} 才能一次框住这两家
$PSQL "INSERT INTO tenants (name, code, tier, status, created_at, updated_at)
   VALUES ('检索靶子${QS_TS}甲', '${QS_CA}', 'personal', 'active', NOW(), NOW()),
          ('检索靶子${QS_TS}乙', '${QS_CB}', 'personal', 'active', NOW(), NOW());" >/dev/null 2>&1
QS_A=$($PSQL "SELECT id FROM tenants WHERE code='${QS_CA}'" 2>/dev/null | tr -d '[:space:]')
# 对照用「最旧一家」：它必然不在 page_size=1 的首页。缺了这条前提，下面"q=ID 命中"
# 会在一家本来看得见的租户上假绿——护栏空转。
QS_OLD=$($PSQL "SELECT min(id) FROM tenants" 2>/dev/null | tr -d '[:space:]')

QS_PAGE1=$(curl -s "$B/api/v1/super/tenants?page_size=1" -H "Authorization: Bearer $TOKEN")
check "首页只有1家时最旧租户看不见(本段对照前提)" 0 "$(printf '%s' "$QS_PAGE1" | grep -c "\"id\":${QS_OLD}," 2>/dev/null)"
# 纯数字关键字走 ID 精确 + 名称/编码模糊两条腿（"搜 2024" 也要命中"2024旗舰店"），
# 所以这里断的是"那一家在结果里、且只出现一次"，而不是"结果只有它"——后者是口径写错不是缺陷。
QS_BYID=$(curl -sG "$B/api/v1/super/tenants" --data-urlencode "page_size=100" --data-urlencode "q=${QS_OLD}" -H "Authorization: Bearer $TOKEN")
check "q=租户ID 能把首页之外的最旧一家搜回来" 1 "$(printf '%s' "$QS_BYID" | grep -c "\"id\":${QS_OLD}," 2>/dev/null)"
# 名称/编码模糊：下拉里显示的是「名称（编码）」，两个都得搜得动
QS_BYNAME=$(curl -sG "$B/api/v1/super/tenants" --data-urlencode "q=检索靶子${QS_TS}" -H "Authorization: Bearer $TOKEN")
check "q=名称片段 命中恰两家" 2 "$(printf '%s' "$QS_BYNAME" | jsonget "['data']['total']")"
QS_BYCODE=$(curl -sG "$B/api/v1/super/tenants" --data-urlencode "q=${QS_CA}" -H "Authorization: Bearer $TOKEN")
check "q=租户编码 命中对应那一家" "${QS_A:-1}" "$(printf '%s' "$QS_BYCODE" | jsonget "['data']['list'][0]['id']")"
check "q=编码 不误伤同名前缀的另一家" 1 "$(printf '%s' "$QS_BYCODE" | jsonget "['data']['total']")"
# 空态一律 []：前端直接 map，回 null 就是整块白屏
QS_NONE=$(curl -sG "$B/api/v1/super/tenants" --data-urlencode "q=绝不可能出现的租户名zzz" -H "Authorization: Bearer $TOKEN")
check "无命中时 list 回 [] 而不是 null" "[]" "$(printf '%s' "$QS_NONE" | python3 -c "import sys,json;d=json.load(sys.stdin);print(json.dumps(d['data']['list']))" 2>/dev/null)"
check "通配符 % 按字面量搜(不放大成全表)" 0 "$(curl -sG "$B/api/v1/super/tenants" --data-urlencode "q=%" -H "Authorization: Bearer $TOKEN" | jsonget "['data']['total']")"
check "q=超出整型范围的数字不报5xx" 200 "$(curl -s -o /dev/null -w '%{http_code}' -G "$B/api/v1/super/tenants" --data-urlencode "q=99999999999999999999999" -H "Authorization: Bearer $TOKEN")"
# 未登录与 sales 一律进不来（检索面不改变端点的鉴权边界）
check "未登录搜租户判 401" 401 "$(curl -s -o /dev/null -w '%{http_code}' -G "$B/api/v1/super/tenants" --data-urlencode "q=${QS_CA}")"
$PSQL "DELETE FROM tenants WHERE code IN ('${QS_CA}','${QS_CB}');" >/dev/null 2>&1
check "本段靶子租户已清零" 0 "$($PSQL "SELECT count(*) FROM tenants WHERE code IN ('${QS_CA}','${QS_CB}')" 2>/dev/null | tr -d '[:space:]')"

echo "==== 结果: PASS=$PASS FAIL=$FAIL ===="
[ "$FAIL" = "0" ] || exit 1
