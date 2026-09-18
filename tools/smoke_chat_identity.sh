#!/bin/bash
# ============================================================
# G-3 /chat/history 身份防线负向断言（T2 重写 2026-09-12）
# 覆盖：正确 VK→200 / 错误 VK→403 / 匿名无 VK→403 / A 的 VK 访问 B→403 /
#       登录态数据范围门禁（sales 访问他人客户→403，P1-15）/ 超管全量可读 /
#       /chat/clear-delay 身份闸四路矩阵+跨租户404（2026-09-18 浏览器实测批护栏，ace130d）
# 契约同步说明（旧脚本 1/6 全灭根因）：
#   - /chat/test 升级后强制 visitor_key（A1-A4 契约收口），旧脚本裸调 → 客户根本没建成，
#     后续全部拿 customer_id='' 打出 400。现改经 /chat/guest 建客户（响应信封 data.*）。
#   - CheckVisitorKey 只认 query visitor_key（旧脚本用 X-Visitor-Key 头，永远匹配不上）。
#   - 登录态放行不等于跨范围放行：P1-15 后 sales 只见名下客户。
# 用法: bash tools/smoke_chat_identity.sh 9090
# ============================================================
B="http://localhost:${1:-9090}"
PSQL="psql ${TEST_DB_URL:-postgresql://ai_scrm:dev123@localhost/ai_scrm} -tAc"
PASS=0; FAIL=0
check(){ if [ "$2" = "$3" ]; then echo "  PASS  $1 ($3)"; PASS=$((PASS+1)); else echo "  FAIL  $1 期望=$2 实际=$3"; FAIL=$((FAIL+1)); fi }
jget(){ python3 -c "import sys,json;d=json.load(sys.stdin);print(eval('d'+sys.argv[1]))" "$1" 2>/dev/null; }

echo "== G-3 /chat/history 身份缺口负向断言 @ $B =="

# ---- 0. 登录（Q3：清首登强改密标记，seed 重启会重标 admin）----
$PSQL "UPDATE tenant_users SET must_change_password=false WHERE username='admin'" >/dev/null 2>&1
AT=$(curl -s -X POST "$B/api/v1/auth/login" -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"admin123"}' | jget "['data']['token']")
[ -n "$AT" ] && check "超管登录" y y || { check "超管登录" y n; exit 1; }

# ---- 1. 经 /chat/guest 建两个访客客户（信封 data.customer_id/visitor_key）----
GA=$(curl -s -X POST "$B/api/v1/chat/guest" -H "X-Tenant-ID: 1" -H "Content-Type: application/json" -d '{}')
CID_A=$(echo "$GA" | jget "['data']['customer_id']"); VK_A=$(echo "$GA" | jget "['data']['visitor_key']")
GB=$(curl -s -X POST "$B/api/v1/chat/guest" -H "X-Tenant-ID: 1" -H "Content-Type: application/json" -d '{}')
CID_B=$(echo "$GB" | jget "['data']['customer_id']"); VK_B=$(echo "$GB" | jget "['data']['visitor_key']")
[ -n "$CID_A" ] && [ -n "$VK_A" ] && [ -n "$CID_B" ] && [ "$CID_A" != "$CID_B" ] && R=y || R=n
check "双访客客户建立(A=$CID_A B=$CID_B)" y "$R"

# ---- 2. A 给自己发一条消息（/chat/test 带 visitor_key），轮询历史落库 ----
curl -s -o /dev/null --max-time 120 -X POST "$B/api/v1/chat/test?visitor_key=$VK_A" -H "X-Tenant-ID: 1" \
  -H "Content-Type: application/json" -d "{\"customer_id\":$CID_A,\"content\":\"G-3 身份断言测试消息\"}"
HAS_MSG=n
for _ in $(seq 1 15); do
  N=$(curl -s "$B/api/v1/chat/history?customer_id=$CID_A&visitor_key=$VK_A" | python3 -c "import sys,json;d=json.load(sys.stdin);print(len(d.get('data') or []))" 2>/dev/null || echo 0)
  [ "${N:-0}" -gt 0 ] && HAS_MSG=y && break
  sleep 4
done
check "A 消息落库可查（发送链路通）" y "$HAS_MSG"

# ---- 3. 正确 VK → 200 ----
R=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/chat/history?customer_id=$CID_A&visitor_key=$VK_A")
check "正确VK访问自身→200" 200 "$R"

# ---- 4. 错误 VK → 403 ----
R=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/chat/history?customer_id=$CID_A&visitor_key=fake_key_abcdef123456")
check "错误VK→403" 403 "$R"

# ---- 5. 匿名无 VK → 403 ----
R=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/chat/history?customer_id=$CID_A")
check "匿名无VK→403" 403 "$R"

# ---- 6. A 的 VK 访问 B → 403（VK 与客户强绑定，不可横向漂移）----
R=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/chat/history?customer_id=$CID_B&visitor_key=$VK_A")
check "A的VK访问B→403" 403 "$R"

# ---- 7. 登录态数据范围（P1-15）：sales 拉非名下客户 → 403 ----
$PSQL "UPDATE tenant_users SET must_change_password=false WHERE username LIKE 'sales%'" >/dev/null 2>&1
ST=$(curl -s -X POST "$B/api/v1/auth/login" -H "Content-Type: application/json" \
  -d '{"username":"sales1","password":"sales123"}' | jget "['data']['token']")
if [ -n "$ST" ]; then
  R=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/chat/history?customer_id=$CID_A&limit=5" -H "Authorization: Bearer $ST")
  check "sales拉非名下客户历史→403(P1-15)" 403 "$R"
else
  check "sales1登录（可用则断言，不可用计败）" y n
fi

# ---- 8. 超管全量：tenant 1 作用域内可读 A ----
R=$(curl -s "$B/api/v1/chat/history?customer_id=$CID_A&limit=5" -H "Authorization: Bearer $AT" -H "X-Tenant-ID: 1")
C=$(echo "$R" | python3 -c "import sys,json;d=json.load(sys.stdin);print(len(d.get('data') or []))" 2>/dev/null || echo 0)
[ "${C:-0}" -gt 0 ] && R2=y || R2=n
check "超管带租户头读取→含消息" y "$R2"

# ---- 8.5 B1 会话列表按客户归属过滤（scopeConversationsByCustomer 负向）----
# CID_A 为访客客户未分配到 sales1 名下：sales1 会话列表不应出现其会话，直读其消息应 403。
if [ -n "$ST" ]; then
  CONV_A=$($PSQL "SELECT id FROM conversations WHERE customer_id=$CID_A ORDER BY id DESC LIMIT 1" | tr -d '[:space:]')
  LEAK=$(curl -s "$B/api/v1/conversations?page=1&page_size=200" -H "Authorization: Bearer $ST" -H "X-Tenant-ID: 1" \
    | python3 -c "import sys,json;d=json.load(sys.stdin);rows=d.get('data') or {};rows=rows.get('list') if isinstance(rows,dict) else rows;rows=rows or [];print(any(str(r.get('customer_id'))==str('$CID_A') for r in rows))" 2>/dev/null || echo "err")
  check "sales1会话列表不泄露他人客户(CID_A)" False "$LEAK"
  if [ -n "$CONV_A" ]; then
    R=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/conversations/$CONV_A/messages" -H "Authorization: Bearer $ST" -H "X-Tenant-ID: 1")
    check "sales1直读他人会话消息→404(B1不泄露存在性)" 404 "$R"
    # 反证：超管可读同一会话（证明 403 源于数据范围而非路径不存在）
    ROK=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/conversations/$CONV_A/messages" -H "Authorization: Bearer $AT" -H "X-Tenant-ID: 1")
    check "超管读同一会话→200(反证)" 200 "$ROK"
  fi
fi

# ---- 8.8 /chat/clear-delay 身份闸（浏览器实测批 2026-09-18 护栏，commit ace130d）----
# 根因回放：该路由在公开组注册（v1.Use(JWTAuth) 之前），修复前 Bearer 从不解析 →
# CheckVisitorKey 拿不到 user_id → 顾问对真实访客客户（VK 非空）点"立即回复"必 403。
# 现挂 OptionalJWTAuth：登录态注入身份放行 B 端；匿名 C 端仍走 visitor_key 自证；
# 且登录态 RQ 带租户作用域，跨租户 customer_id 由"全表命中"收口为 404。
if [ -n "$ST" ]; then
  CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/chat/clear-delay" \
    -H "Authorization: Bearer $ST" -H "Content-Type: application/json" \
    -d "{\"customer_id\":$CID_A}")
  check "登录态clear-delay无VK→200(修复回归护栏)" 200 "$CODE"
  # 匿名错误 VK → 403（C 端自证路径不因修复放宽）
  CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/chat/clear-delay?visitor_key=fake_key_abcdef123456" \
    -H "Content-Type: application/json" -d "{\"customer_id\":$CID_A}")
  check "clear-delay匿名错误VK→403" 403 "$CODE"
  # 匿名无 VK → 403
  CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/chat/clear-delay" \
    -H "Content-Type: application/json" -d "{\"customer_id\":$CID_A}")
  check "clear-delay匿名无VK→403" 403 "$CODE"
  # 匿名正确 VK → 200
  CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/chat/clear-delay?visitor_key=$VK_A" \
    -H "Content-Type: application/json" -d "{\"customer_id\":$CID_A}")
  check "clear-delay匿名正确VK→200" 200 "$CODE"
  # 跨租户负向：sales1(tenant 1) 打他租户客户 → 404（RQ 租户作用域生效；本机库无第二租户客户则跳过）
  CID_X=$($PSQL "SELECT id FROM customers WHERE tenant_id<>1 AND visitor_key<>'' ORDER BY id LIMIT 1" | tr -d '[:space:]')
  if [ -n "$CID_X" ]; then
    CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/chat/clear-delay" \
      -H "Authorization: Bearer $ST" -H "Content-Type: application/json" \
      -d "{\"customer_id\":$CID_X}")
    check "clear-delay跨租户→404(作用域收紧)" 404 "$CODE"
  fi
fi

# ---- 8.9 空 VK 通道客户护栏（审计批一 P1-1，2026-09-19）----
# 背景：channel/identity.go 通道建档客户不带 visitor_key；旧闸 `VisitorKey != "" &&`
# 前置短路令空 VK 客户对匿名请求完全不设防（任何人可清延迟/锁死人工接管）。
# 现口径：CheckVisitorKey 空串=拒一切匿名，登录态放行。合成一条空 VK 客户走四路矩阵。
CID_NVK=$($PSQL "INSERT INTO customers (tenant_id,name,source,status,created_at,updated_at) VALUES (1,'护栏空VK通道客','channel:9',1,now(),now()) RETURNING id" | head -1 | tr -d '[:space:]')
if [ -n "$CID_NVK" ]; then
  CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/chat/clear-delay" -H "Content-Type: application/json" -d "{\"customer_id\":$CID_NVK}")
  check "空VK客户匿名clear-delay→403(P1-1护栏)" 403 "$CODE"
  CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/chat/request-human" -H "Content-Type: application/json" -d "{\"customer_id\":$CID_NVK}")
  check "空VK客户匿名request-human→403(P1-1护栏)" 403 "$CODE"
  CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/chat/clear-delay" -H "Authorization: Bearer $ST" -H "Content-Type: application/json" -d "{\"customer_id\":$CID_NVK}")
  check "空VK客户登录态clear-delay→200" 200 "$CODE"
  # 登录态 request-human→403：该端点设计上 C 端专用（公开组不挂 OptionalJWTAuth，Bearer 不解析），
  # 员工转人走 /chat/transfer/human；空 VK 客户据此对登录态同样关死（口径与 clear-delay 有意不同）
  CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/chat/request-human" -H "Authorization: Bearer $ST" -H "Content-Type: application/json" -d "{\"customer_id\":$CID_NVK}")
  check "空VK客户登录态request-human→403(C端专用口径)" 403 "$CODE"
  $PSQL "DELETE FROM customers WHERE id=$CID_NVK" >/dev/null 2>&1
fi

# ---- 9. 清理：停用测试客户（数据保留供核查，对齐惯例）----
$PSQL "UPDATE customers SET status=0 WHERE id IN ($CID_A,$CID_B)" >/dev/null 2>&1

echo ""; echo "==== G-3 结果: PASS=$PASS FAIL=$FAIL ===="
[ "$FAIL" = "0" ] || exit 1
