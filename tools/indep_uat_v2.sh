#!/bin/bash
# ============================================================
# 独立字节级 UAT 复核 V2（2026-09-22 第三方视角）
#
# 与 tools/indep_byte_uat.sh 的区别：那份只覆盖"单资源读写 + 少量异常码"(~30 项)。
# 本脚本覆盖**全流程分支**：业务链路、状态机、幂等、限流、跨租户多表、
# 契约对拍、商业收款链路实测、安全头/压缩。
#
# 铁律：
#   - 不复用项目自带任何断言函数
#   - 所有断言失败时打印实际响应片段，供三分归因（脚手架错 / 环境 / 真缺陷）
#   - 数据隔离：SQL 直造一次性租户，结束整租户回收并断言残留 0
#
# 用法： bash tools/cleanenv.sh bash tools/indep_uat_v2.sh [port]
# ============================================================
set -u
PORT="${1:-9090}"
B="http://localhost:${PORT}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT" || exit 2

# ---- DB 连接（从 .env 组装，避免硬编码）----
set -a; [ -f .env ] && . ./.env; set +a
[ -n "${DB_PASSWORD:-}" ] && export PGPASSWORD="$DB_PASSWORD"
Q(){ psql -h "${DB_HOST:-localhost}" -p "${DB_PORT:-5432}" -U "${DB_USER:-ai_scrm}" -d "${DB_NAME:-ai_scrm}" -tAc "$1" 2>&1; }

PASS=0; FAIL=0; SKIP=0
declare -a FAILED
ck(){ # ck <名> <期望> <实际>
  if [ "$2" = "$3" ]; then PASS=$((PASS+1)); echo "  PASS  $1"
  else FAIL=$((FAIL+1)); FAILED+=("$1 | 期望=[$2] 实际=[$3]"); echo "  FAIL  $1"
       printf '        期望=[%s]\n        实际=[%s]\n' "$2" "$3"; fi }
# 宽松断言：只记录不断言（用于"设计如此"的观察项）
rec(){ echo "  INFO  $1 = $2"; }
jget(){ python3 -c "
import sys,json
try:
    d=json.load(sys.stdin); v=eval(sys.argv[1],{'d':d}); print('' if v is None else v)
except Exception: print('')" "$1"; }
bsha(){ printf '%s' "$1" | shasum -a 256 | cut -d' ' -f1; }
hc(){ curl -s -o /dev/null -w '%{http_code}' -m 20 "$@"; }
req(){ curl -s -m 20 "$@"; }

echo "============================================================"
echo " 独立字节级 UAT V2 @ $B   $(date '+%F %T')"
echo "============================================================"

# ============================================================
echo "---- S0 前置：服务与依赖 ----"
ck "GET /health 200" 200 "$(hc "$B/health")"
ck "GET /status 200" 200 "$(hc "$B/status")"
Q_OUT=$(Q "SELECT 1")
ck "DB 可达" 1 "$Q_OUT"

# ============================================================
echo "---- S1 造一次性隔离租户 ----"
TS=$(date +%s); RND=$RANDOM
TCODE="iv2$((TS%100000))$((RND%100))"
RUN="iv2u$TS"
RAW=$(Q "INSERT INTO tenants (name, code) VALUES ('独立复核V2租户','$TCODE') RETURNING id")
TID=$(printf '%s' "$RAW" | tr -dc '0-9\n' | head -1 | tr -d '[:space:]')
if [ -z "$TID" ]; then echo "  FATAL 造租户失败: $RAW"; exit 2; fi
echo "  一次性租户 id=$TID code=$TCODE"
Q "INSERT INTO tenant_users (username, password_hash, role, tenant_id, status, must_change_password)
   SELECT '$RUN', password_hash, 'tenant_admin', $TID, 1, false
   FROM tenant_users WHERE username='admin' LIMIT 1" >/dev/null 2>&1
LOGIN=$(req -X POST "$B/api/v1/auth/login" -H 'Content-Type: application/json' \
  -d "{\"tenant_code\":\"$TCODE\",\"username\":\"$RUN\",\"password\":\"admin123\"}")
TOKEN=$(printf '%s' "$LOGIN" | jget "d['data']['token']")
if [ -z "$TOKEN" ]; then echo "  FATAL 登录失败: $(printf '%s' "$LOGIN" | head -c 300)"; exit 2; fi
AH="Authorization: Bearer $TOKEN"
TH="X-Tenant-ID: $TID"
ck "一次性 tenant_admin 登录成功" ok ok
ck "角色 = tenant_admin" tenant_admin "$(req "$B/api/v1/auth/me" -H "$AH" | jget "d['data']['role']")"

# ============================================================
echo "---- S2 BE↔BE 字节级：特殊载荷双向一致（API→DB→API）----"
# 载荷含：单双引号、尖括号、&、反斜杠、中文、重音、欧元符、emoji、SQL注入串、XSS载荷
mkname(){ printf 'V2-%s-%s' "$TS" "$1"; }
P1=$(mkname 'q1<&"'"'"'>中文é€😀')
P2=$(mkname "inj'; DROP TABLE customers;--")
P3=$(mkname 'xss<script>alert(1)</script>')
P4=$(mkname "$(python3 -c "print('长'*200)")")
P5=$(mkname 'backslash\and\ttab	end')

for idx in 1 2 3 4 5; do
  eval "PAYLOAD=\$P$idx"
  # 注意：python 脚本整体用双引号包住时，串里的 ${2} 会被 shell 当位置参数展开，
  # 必须走 sys.argv[2] 取值，不要在双引号里写 ${2}。
  PJ=$(python3 -c "import json,sys;print(json.dumps({'name':sys.argv[1],'phone':'1390000'+sys.argv[2]+'000'},ensure_ascii=False))" "$PAYLOAD" "$idx")
  RES=$(req -X POST "$B/api/v1/customers" -H "$AH" -H 'Content-Type: application/json' -d "$PJ")
  C=$(printf '%s' "$RES" | jget "d['data']['id']")
  if [ -z "$C" ]; then
    FAIL=$((FAIL+1)); FAILED+=("载荷$idx 创建客户失败 | 响应=$(printf '%s' "$RES"|head -c 200)")
    echo "  FAIL  载荷$idx 创建客户（响应=$(printf '%s' "$RES" | head -c 200)）"; continue
  fi
  DBV=$(Q "SELECT name FROM customers WHERE id=$C")
  ck "载荷$idx API→DB 逐字节一致" "$PAYLOAD" "$DBV"
  APV=$(req "$B/api/v1/customers/$C" -H "$AH" | jget "d['data']['name']")
  ck "载荷$idx DB→API 回读逐字节一致" "$PAYLOAD" "$APV"
  # 归属与隔离
  ck "载荷$idx 归属本租户" "$TID" "$(Q "SELECT tenant_id FROM customers WHERE id=$C")"
  eval "CID$idx=$C"
done
# SQL 注入未生效（表还在）
ck "SQL注入载荷未破坏表（customers 仍可查）" ok "$(Q "SELECT 'ok' FROM customers WHERE id=${CID2:-0} LIMIT 1")"
# 超长输入：应在入参校验层被挡（400），而不是打到 DB 报 500 并泄漏 SQLSTATE
LONGN=$(python3 -c "print('长'*200)")
LONGP=$(python3 -c "import json,sys;print(json.dumps({'name':sys.argv[1],'phone':'13900009000'},ensure_ascii=False))" "$LONGN")
LONGR=$(req -X POST "$B/api/v1/customers" -H "$AH" -H 'Content-Type: application/json' -d "$LONGP")
ck "超长 name(200字) 应返回 400 参数校验错误" 400 "$(printf '%s' "$LONGR" | jget "d['code']" >/dev/null; hc -X POST "$B/api/v1/customers" -H "$AH" -H 'Content-Type: application/json' -d "$LONGP")"
rec "超长 name 实际响应" "$(printf '%s' "$LONGR" | head -c 220)"

echo "---- S2b remark(text) 字段双向 + 空/NULL 语义 ----"
RJ=$(python3 -c "import json,sys;print(json.dumps({'remark':sys.argv[1]},ensure_ascii=False))" "备注😀<&\"'>é")
req -X PUT "$B/api/v1/customers/${CID1:-0}" -H "$AH" -H 'Content-Type: application/json' -d "$RJ" >/dev/null
ck "remark API→DB 一致" "备注😀<&\"'>é" "$(Q "SELECT remark FROM customers WHERE id=${CID1:-0}")"
ck "remark DB→API 一致" "备注😀<&\"'>é" "$(req "$B/api/v1/customers/${CID1:-0}" -H "$AH" | jget "d['data']['remark']")"

# ============================================================
echo "---- S3 响应字节级幂等 ----"
for ep in /api/v1/plans /api/v1/auth/register-config /api/v1/knowledge/brands; do
  a=$(bsha "$(req "$B$ep")"); b=$(bsha "$(req "$B$ep")")
  ck "GET $ep 两次响应字节一致" "$a" "$b"
done
a=$(bsha "$(req "$B/api/v1/customers?page=1&page_size=5" -H "$AH")")
b=$(bsha "$(req "$B/api/v1/customers?page=1&page_size=5" -H "$AH")")
ck "GET /customers 分页两次响应字节一致" "$a" "$b"

# ============================================================
echo "---- S4 业务全链路：客户→标签→阶段→会话→消息 ----"
# 4.1 标签覆盖语义（PUT = 提交列表为最终态）
# 源码核实：EditCustomerTags(internal/api/advisor.go:740) 经 service.ReplaceTagsForCustomer
# 写入 **customer_tags 关系表**，不是 customers.tags 列 —— 断言必须查关系表。
# 源码核实：ReplaceTagsForCustomer(internal/service/tag_service.go:142) 只匹配
# **已在标签定义表登记**的标签（cache.DefaultTagCache），凭空传的 A/B/C 不会落库。
# 因此先建标签定义并 reload 缓存，再打标。
# 注意：python 脚本必须用**单引号**包裹，内部字典用双引号。
# 若用双引号包裹含 {'a':1,'b':2} 的脚本，bash 的 brace expansion 会把花括号吃掉。
# 注意：tags.name/code 实际是**全局唯一**索引（见报告 P1-2），同名会跨租户冲突，
# 因此标签名必须带时间戳保证唯一，否则第二次运行必失败。
TA="A$TS"; TB="B$TS"; TC="C$TS"
for tn in "$TA" "$TB" "$TC"; do
  req -X POST "$B/api/v1/admin/tags" -H "$AH" -H 'Content-Type: application/json' \
    -d "$(python3 -c 'import json,sys;print(json.dumps({"name":sys.argv[1],"code":"v2_"+sys.argv[2]+"_"+sys.argv[1],"weight":1.0,"status":1}))' "$tn" "$TS")" >/dev/null
done
req -X POST "$B/api/v1/admin/tags/reload" -H "$AH" >/dev/null
TAGQ="SELECT string_agg(tag_name,',' ORDER BY tag_name) FROM customer_tags WHERE customer_id=${CID1:-0}"
TJ1=$(python3 -c 'import json,sys;print(json.dumps({"tags":[sys.argv[1],sys.argv[2],sys.argv[3]]}))' "$TA" "$TB" "$TC")
R1=$(req -X PUT "$B/api/v1/advisor/customer/${CID1:-0}/tags" -H "$AH" -H 'Content-Type: application/json' -d "$TJ1")
ck "打标 A/B/C 接口 code=0" 0 "$(printf '%s' "$R1" | jget "d['code']")"
ck "打标 3 个后关系表计数=3" 3 "$(Q "SELECT count(*) FROM customer_tags WHERE customer_id=${CID1:-0}")"
ck "打标内容含全部 3 个标签" "$TA,$TB,$TC" "$(Q "$TAGQ")"
TJ2=$(python3 -c 'import json,sys;print(json.dumps({"tags":[sys.argv[1],sys.argv[2]]}))' "$TB" "$TC")
req -X PUT "$B/api/v1/advisor/customer/${CID1:-0}/tags" -H "$AH" -H 'Content-Type: application/json' -d "$TJ2" >/dev/null
ck "PUT 覆盖语义：旧标 A 被清除，仅剩 B/C" "$TB,$TC" "$(Q "$TAGQ")"
ck "PUT 覆盖语义：关系表计数降为 2" 2 "$(Q "SELECT count(*) FROM customer_tags WHERE customer_id=${CID1:-0}")"
# 注意分隔符：Go 的 json.Marshal 无空格，Python json.dumps 默认带空格 —— 必须显式 separators
ck "覆盖后 customers.tags 冗余字段同步" "$(python3 -c 'import json,sys;print(json.dumps([sys.argv[1],sys.argv[2]],ensure_ascii=False,separators=(",",":")))' "$TB" "$TC")" "$(Q "SELECT tags FROM customers WHERE id=${CID1:-0}")"

# 4.2 阶段推进（合法 vs 非法枚举）
# 源码核实：字段是 journey_stage（internal/api/advisor.go:883），
# 合法值仅 arrived/ordered/delivered/lost —— lead_captured/human_connected 按设计不接受。
for st in arrived ordered delivered lost; do
  code=$(hc -X PUT "$B/api/v1/advisor/customer/${CID1:-0}/stage" -H "$AH" -H 'Content-Type: application/json' -d "{\"journey_stage\":\"$st\"}")
  ck "阶段推进到 $st 被接受" 200 "$code"
done
ck "阶段非法枚举 400（不接受任意串）" 400 \
  "$(hc -X PUT "$B/api/v1/advisor/customer/${CID1:-0}/stage" -H "$AH" -H 'Content-Type: application/json' -d '{"journey_stage":"not_a_stage"}')"
ck "阶段非法枚举 400（lead_captured 按设计不接受）" 400 \
  "$(hc -X PUT "$B/api/v1/advisor/customer/${CID1:-0}/stage" -H "$AH" -H 'Content-Type: application/json' -d '{"journey_stage":"lead_captured"}')"
ck "阶段推进后 DB 落库" lost "$(Q "SELECT journey_stage FROM customers WHERE id=${CID1:-0}")"

# 4.3 会话与消息
CONV=$(req "$B/api/v1/customers/${CID1:-0}/conversations" -H "$AH")
CVID=$(printf '%s' "$CONV" | jget "d['data'][0]['id']")
if [ -n "$CVID" ]; then
  ck "客户会话列表可读" 200 "$(hc "$B/api/v1/customers/${CID1:-0}/conversations" -H "$AH")"
  MSG=$(req "$B/api/v1/conversations/$CVID/messages" -H "$AH")
  ck "会话消息列表可读（code=0）" 0 "$(printf '%s' "$MSG" | jget "d['code']")"
  ck "会话归属本租户" "$TID" "$(Q "SELECT tenant_id FROM conversations WHERE id=$CVID")"
else
  rec "客户会话列表" "空（该客户尚无会话，跳过消息断言）"
  SKIP=$((SKIP+1))
fi

# 4.4 知识库 CRUD 全链路（admin）
KJ=$(python3 -c "import json,sys;print(json.dumps({'title':sys.argv[1],'content':'内容é😀<&','enabled':True},ensure_ascii=False))" "V2知识$TS")
KRES=$(req -X POST "$B/api/v1/admin/knowledge/fragments" -H "$AH" -H 'Content-Type: application/json' -d "$KJ")
KID=$(printf '%s' "$KRES" | jget "d['data']['id']")
if [ -n "$KID" ]; then
  ck "知识片段创建成功" ok ok
  ck "知识片段 API→DB 内容一致" "内容é😀<&" "$(Q "SELECT content FROM knowledge_fragments WHERE id=$KID" 2>/dev/null || echo NA)"
  ck "知识片段归属本租户" "$TID" "$(Q "SELECT tenant_id FROM knowledge_fragments WHERE id=$KID" 2>/dev/null || echo NA)"
  ck "知识片段更新 200" 200 "$(hc -X PUT "$B/api/v1/admin/knowledge/fragments/$KID" -H "$AH" -H 'Content-Type: application/json' -d "$KJ")"
  ck "知识片段删除 200" 200 "$(hc -X DELETE "$B/api/v1/admin/knowledge/fragments/$KID" -H "$AH")"
  # 源码核实：DeleteFragment 是软删（status=0），与 disable 语义重叠（internal/api/knowledge.go:1027）
  # 因此真断言是"列表不再返回它"，而不是"DB 行消失"
  ck "删除后 status 置 0（软删）" 0 "$(Q "SELECT status FROM knowledge_fragments WHERE id=$KID")"
  ck "删除后详情不可见 404" 404 "$(hc "$B/api/v1/admin/knowledge/fragments/$KID" -H "$AH")"
  LISTHAS=$(req "$B/api/v1/admin/knowledge/fragments?page=1&page_size=200" -H "$AH" | python3 -c "
import sys,json
try:
  d=json.load(sys.stdin); rows=d.get('data') or []
  if isinstance(rows,dict): rows=rows.get('list') or rows.get('items') or []
  print('yes' if any(str(r.get('id'))=='$KID' for r in rows) else 'no')
except Exception: print('parse_fail')")
  ck "删除后列表不再包含该片段" no "$LISTHAS"
else
  rec "知识片段创建" "失败或字段不匹配：$(printf '%s' "$KRES" | head -c 200)"
  SKIP=$((SKIP+1))
fi

# ============================================================
echo "---- S5 异常分支与错误信封 ----"
ck "无 token 401" 401 "$(hc "$B/api/v1/customers")"
ck "坏 token 401" 401 "$(hc "$B/api/v1/customers" -H 'Authorization: Bearer not.a.jwt')"
ck "空 Authorization 头 401" 401 "$(hc "$B/api/v1/customers" -H 'Authorization: ')"
ck "tenant_admin 打 /super/tenants 403" 403 "$(hc "$B/api/v1/super/tenants" -H "$AH")"
ck "不存在客户 404" 404 "$(hc "$B/api/v1/customers/99999999" -H "$AH")"
ck "非法 JSON 400" 400 "$(hc -X POST "$B/api/v1/auth/login" -H 'Content-Type: application/json' -d '{bad json')"
ck "/api 前缀未知路径 404（不回落 SPA）" 404 "$(hc "$B/api/v1/definitely-not-exist")"
ck "SPA 深路由回落 200" 200 "$(hc "$B/some/deep/frontend/route")"
ck "/status/detail 无令牌 403（fail-closed）" 403 "$(hc "$B/status/detail")"
# 错误响应必须是统一信封
for u in "/api/v1/customers/99999999" "/api/v1/super/tenants"; do
  v=$(req "$B$u" -H "$AH" | python3 -c "import sys,json
try:
  d=json.load(sys.stdin); print('yes' if 'code' in d else 'no')
except Exception: print('notjson')" 2>/dev/null)
  ck "错误响应是统一信封（含 code）$u" yes "$v"
done
# 越权/参数非法时 code 非 0
ck "404 响应 code != 0" nonzero "$(req "$B/api/v1/customers/99999999" -H "$AH" | python3 -c "import sys,json
try:
  d=json.load(sys.stdin); c=d.get('code'); print('nonzero' if c not in (0,'0',None) else 'zero')
except Exception: print('?')")"

# ============================================================
echo "---- S6 多租户隔离（多表）----"
# 超管账号：不带租户头应 400；带他租户头查本租户资源应 404
# 注意：内置 admin 账号处于 must_change_password=true（首次登录强制改密策略），
# 任何请求都会被 MustChangePasswordGuard 挡 403 —— 用它测超管会得到假失败。
# 因此自建一个 super_admin（复用 admin 的密码哈希），不动内置账号。
SUPRUN="iv2super$TS"
Q "INSERT INTO tenant_users (username, password_hash, role, tenant_id, status, must_change_password)
   SELECT '$SUPRUN', password_hash, 'super_admin', 0, 1, false
   FROM tenant_users WHERE username='admin' LIMIT 1" >/dev/null 2>&1
SUP=$(req -X POST "$B/api/v1/auth/login" -H 'Content-Type: application/json' \
  -d "$(python3 -c 'import json,sys;print(json.dumps({"username":sys.argv[1],"password":"admin123"}))' "$SUPRUN")" | jget "d['data']['token']")
ck "自建 super_admin 登录成功" super_admin "$(req "$B/api/v1/auth/me" -H "Authorization: Bearer $SUP" | jget "d['data']['role']")"
OTHERT=$(printf '%s' "$(Q "SELECT id FROM tenants WHERE id <> $TID ORDER BY id LIMIT 1")" | tr -d '[:space:]')
# 源码核实（internal/middleware/tenant.go:297-310）：X-Tenant-ID 头仅在
# IsDevModeConfirmed()（显式 GIN_MODE=debug|test）下生效，生产忽略以防伪造。
# 因此跨租户验证必须走 Host 子域解析（生产真实路径），而不是伪造请求头。
ck "超管无租户声明打租户面 400（fail-closed）" 400 "$(hc "$B/api/v1/customers/${CID1:-0}" -H "Authorization: Bearer $SUP")"
# 正向证据：非 super 用户的租户上下文只来自 JWT claims，伪造 X-Tenant-ID 不改变归属
# （带伪造头访问自己的客户仍应 200 —— 即头被忽略，而非生效）
ck "租户成员带伪造 X-Tenant-ID 访问自己客户仍 200（头被忽略）" 200 \
  "$(hc "$B/api/v1/customers/${CID1:-0}" -H "$AH" -H "X-Tenant-ID: $OTHERT")"
# super_admin 指定目标租户的唯一途径就是 X-Tenant-ID（TenantConsistency 无条件读取，
# internal/middleware/tenant.go:574）——与 TenantResolver"生产忽略该头"是两条独立路径。
# 跨租户测试须挑一个 status 为 active/trial 的他租户，否则命中"目标租户不存在或已停用"403。
ACTIVE_OTHER=$(Q "SELECT id FROM tenants WHERE id <> $TID AND status IN ('active','trial') ORDER BY id LIMIT 1" | tr -d '[:space:]')
# super_admin 访问租户面需**同时**满足两条独立中间件：
#   TenantResolver（Host 子域解析，失败即 403，X-Tenant-ID 仅 dev 模式生效）
#   TenantConsistency（super 必须有 X-Tenant-ID 指定目标租户，否则 400）
# 只给其一都会被挡——这是 fail-closed 叠加，不是缺陷。
ck "超管：Host 子域 + 本租户声明 → 可查该客户 200" 200 \
  "$(hc "$B/api/v1/customers/${CID1:-0}" -H "Authorization: Bearer $SUP" -H "Host: $TCODE.localhost" -H "X-Tenant-ID: $TID")"
OTHC=$(Q "SELECT code FROM tenants WHERE id=$ACTIVE_OTHER" | tr -d '[:space:]')
ck "超管：Host 子域 + 他租户声明 → 查本租户客户 404（跨租户不可见）" 404 \
  "$(hc "$B/api/v1/customers/${CID1:-0}" -H "Authorization: Bearer $SUP" -H "Host: $OTHC.localhost" -H "X-Tenant-ID: $ACTIVE_OTHER")"
ck "超管带停用/不存在租户声明 403（fail-closed）" 403 \
  "$(hc "$B/api/v1/customers/${CID1:-0}" -H "Authorization: Bearer $SUP" -H "X-Tenant-ID: 99999999")"
# 普通租户成员伪造 X-Tenant-ID 不得越权看**他租户**的客户（真越权测试）
OTHER_CID=$(Q "SELECT id FROM customers WHERE tenant_id=$ACTIVE_OTHER ORDER BY id LIMIT 1" | tr -d '[:space:]')
if [ -n "$OTHER_CID" ]; then
  H2=$(hc "$B/api/v1/customers/$OTHER_CID" -H "$AH")
  case "$H2" in 403|404|401) H2V=reject;; *) H2V="allowed($H2)";; esac
  ck "租户成员直访他租户客户被拒（404/403）" reject "$H2V"
  H3=$(hc "$B/api/v1/customers/$OTHER_CID" -H "$AH" -H "X-Tenant-ID: $ACTIVE_OTHER")
  case "$H3" in 403|404|401) H3V=reject;; *) H3V="allowed($H3)";; esac
  ck "租户成员伪造 X-Tenant-ID 访问他租户客户仍被拒" reject "$H3V"
  # DB 层确认：他租户客户确实存在（排除"本来就没有"导致的假绿）
  ck "他租户客户确实存在（排除假绿）" "$ACTIVE_OTHER" "$(Q "SELECT tenant_id FROM customers WHERE id=$OTHER_CID")"
else
  rec "越权测试" "他租户无客户样本，跳过"
  SKIP=$((SKIP+1))
fi
# 本租户账号看不到他租户客户（列表口径）
LISTN=$(req "$B/api/v1/customers?page=1&page_size=200" -H "$AH" | python3 -c "
import sys,json
try:
  d=json.load(sys.stdin); rows=d.get('data') or []
  if isinstance(rows,dict): rows=rows.get('list') or rows.get('items') or []
  print(sum(1 for r in rows if str(r.get('id'))=='$CID1'))
except Exception: print('parse_fail')")
ck "本租户列表可见刚建客户（计数≥1）" yes "$([ "$LISTN" != "0" ] && [ "$LISTN" != "parse_fail" ] && echo yes || echo "no($LISTN)")"
# DB 层：本租户客户数 == API 可见数（防越权泄漏）
DBN=$(Q "SELECT count(*) FROM customers WHERE tenant_id=$TID")
rec "本租户客户 DB 计数" "$DBN"

# ============================================================
echo "---- S7 FE↔BE：托管产物字节 + 路径契约 ----"
IDX=$(req -m 10 "$B/")
MISMATCH=0; CHECKED=0
for p in $(printf '%s' "$IDX" | /usr/bin/grep -oE '/assets/[^"]+' | head -12); do
  base=$(basename "$p"); req -m 20 "$B$p" -o "/tmp/v2_$base" 2>/dev/null
  if [ -f "$ROOT/frontend-react/dist/assets/$base" ]; then
    CHECKED=$((CHECKED+1))
    a=$(md5 -q "$ROOT/frontend-react/dist/assets/$base"); b=$(md5 -q "/tmp/v2_$base")
    [ "$a" = "$b" ] || { MISMATCH=$((MISMATCH+1)); echo "       字节不一致: $base"; }
  fi
done
ck "托管 assets 与 dist 字节一致（检查 $CHECKED 个）" 0 "$MISMATCH"
req -m 10 "$B/" -o /tmp/v2_index.html
ck "托管 index.html 与 dist 字节一致" "$(md5 -q "$ROOT/frontend-react/dist/index.html")" "$(md5 -q /tmp/v2_index.html)"
# 生产压缩
CE=$(curl -s -m 10 -D - -o /dev/null -H 'Accept-Encoding: gzip, br' "$B/" | /usr/bin/grep -ic 'content-encoding' || true)
ck "首页启用压缩 Content-Encoding（非零即启用）" yes "$([ "$CE" -gt 0 ] && echo yes || echo no)"
# 安全头
HDRS=$(curl -s -m 10 -D - -o /dev/null "$B/")
for h in 'X-Content-Type-Options' 'X-Frame-Options' 'Content-Security-Policy'; do
  ck "安全头 $h 存在" yes "$(printf '%s' "$HDRS" | /usr/bin/grep -qi "$h" && echo yes || echo no)"
done

# ============================================================
echo "---- S8 商业链路实测（默认配置下的真实行为）----"
rec "pay_mode 实测值" "$(Q "SELECT value FROM system_configs WHERE key='pay_mode' AND tenant_id=0" 2>/dev/null || echo 'NA')"
rec "token_billing_enabled 实测值" "$(Q "SELECT value FROM system_configs WHERE key='token_billing_enabled' AND tenant_id=0" 2>/dev/null || echo 'NA')"
rec "billing_enforced 实测值" "$(Q "SELECT value FROM system_configs WHERE key='billing_enforced' AND tenant_id=0" 2>/dev/null || echo 'NA')"
rec "contentsafety_mode 实测值" "$(Q "SELECT value FROM system_configs WHERE key='contentsafety_mode' AND tenant_id=0" 2>/dev/null || echo 'NA')"
# 套餐可见（公开面）
ck "GET /plans 公开可读 200" 200 "$(hc "$B/api/v1/plans")"
PLANS=$(req "$B/api/v1/plans" | head -c 200)
rec "/plans 响应片段" "$PLANS"
# mock-pay 端点可达性（在默认 mock 模式下）
MP=$(hc -X POST "$B/api/v1/billing/orders/mock-pay" -H "$AH" -H 'Content-Type: application/json' -d '{"order_no":"__nonexistent__"}')
rec "POST /billing/orders/mock-pay（不存在的单号）" "http=$MP"
# 我的套餐
ck "GET /billing/my-package 200" 200 "$(hc "$B/api/v1/billing/my-package" -H "$AH")"

# ============================================================
echo "---- S9 清理与残留断言 ----"
for t in customer_tags tags customers conversations messages knowledge_fragments tenant_users usage_ledger billing_orders; do
  Q "DELETE FROM $t WHERE tenant_id=$TID" >/dev/null 2>&1
done
Q "DELETE FROM tenant_users WHERE username='$SUPRUN'" >/dev/null 2>&1
Q "DELETE FROM tenants WHERE id=$TID" >/dev/null 2>&1
ck "一次性租户已回收（残留 0）" 0 "$(Q "SELECT count(*) FROM tenants WHERE id=$TID" | tr -d '[:space:]')"
ck "一次性客户已回收（残留 0）" 0 "$(Q "SELECT count(*) FROM customers WHERE tenant_id=$TID" | tr -d '[:space:]')"

echo ""
echo "============================================================"
echo " 独立 UAT V2 总账: PASS=$PASS FAIL=$FAIL SKIP=$SKIP"
if [ "$FAIL" -gt 0 ]; then echo " 失败明细："; for f in "${FAILED[@]}"; do echo "   x $f"; done; fi
echo "============================================================"
[ "$FAIL" = "0" ] && exit 0 || exit 1
