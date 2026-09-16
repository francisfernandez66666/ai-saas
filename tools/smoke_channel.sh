#!/bin/bash
# ============================================================
# AI-SCRM 通道接入端到端冒烟（W8/T4，2026-09-12）
# 自包含：起 mock 模式服务(9091) + 微信 mock 服务(9099)，端到端验证
#   通道CRUD/凭据掩码/连通测试/URL验证/入站→AI→出站投递/人工锁定不出声/死信重发。
# 用法: ./tools/smoke_channel.sh            （默认自建 9091+9099）
# 前置: 本地 PG 可用（沿用 dev 库）
# ============================================================
set -u
DBURL="${TEST_DB_URL:-postgresql://ai_scrm:dev123@localhost/ai_scrm}"
PSQL="psql ${DBURL} -tAc"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

CPORT="${CHANNEL_PORT:-9091}"
MPORT="${MOCKWX_PORT:-9099}"
B="http://localhost:${CPORT}"
MOCK="http://127.0.0.1:${MPORT}"
OUTBOX="$(mktemp /tmp/mockwx_out.XXXXXX.jsonl)"
SLOG="/tmp/smoke_channel_server.log"   # 固定路径：跑完保留，失败可事后排查（原 mktemp 被 cleanup 删除）
MLOG="/tmp/smoke_channel_mockwx.log"

PASS=0; FAIL=0
check() { if [ "$2" = "$3" ]; then echo "  PASS  $1 ($3)"; PASS=$((PASS+1)); else echo "  FAIL  $1 期望=$2 实际=$3"; FAIL=$((FAIL+1)); fi; }
jsonget() { python3 -c "import sys,json;d=json.load(sys.stdin);print(eval('d'+sys.argv[1]))" "$1" 2>/dev/null; }

SRV_PID=""; MOCK_PID=""
cleanup() {
  [ -n "$SRV_PID" ] && kill "$SRV_PID" 2>/dev/null
  [ -n "$MOCK_PID" ] && kill "$MOCK_PID" 2>/dev/null
  # 清测试通道及其派生数据（按 corpid 标记），避免污染 dev 库
  psql "$DBURL" -c "DELETE FROM channel_outbound WHERE channel_id IN (SELECT id FROM channels WHERE corpid='ww_chan_smoke');
                    DELETE FROM messages WHERE conversation_id IN (SELECT c.id FROM conversations c JOIN channel_identities ci ON ci.customer_id=c.customer_id JOIN channels ch ON ch.id=ci.channel_id WHERE ch.corpid='ww_chan_smoke');
                    DELETE FROM conversations WHERE customer_id IN (SELECT customer_id FROM channel_identities WHERE channel_id IN (SELECT id FROM channels WHERE corpid='ww_chan_smoke'));
                    DELETE FROM channel_identities WHERE channel_id IN (SELECT id FROM channels WHERE corpid='ww_chan_smoke');
                    DELETE FROM customers WHERE source LIKE 'channel:%' AND source IN (SELECT 'channel:'||id FROM channels WHERE corpid='ww_chan_smoke');
                    DELETE FROM channels WHERE corpid='ww_chan_smoke';" >/dev/null 2>&1
  rm -f "$OUTBOX" # 日志保留（固定路径），排查用
  # D5(2026-09-16)：恢复测试租户合并窗口（本脚本为其临时降窗）
  psql "$DBURL" -c "DELETE FROM system_configs WHERE tenant_id=1 AND key='merge_window_seconds';" >/dev/null 2>&1
}
trap cleanup EXIT

# D5(2026-09-16)：通道入站并入合并队列后，入站首条要等合并窗口收批——为租户 1 临时降窗到 3s
# （起服务前写入，加载器随启动读取），保持本套件既有轮询时限语义；结束 cleanup 删除恢复默认。
psql "$DBURL" -c "INSERT INTO system_configs (tenant_id,category,key,value,value_type,description,default_value,sort_order)
  VALUES (1,'reply_speed','merge_window_seconds','3','number','smoke_channel D5: 降窗保时限','25',0)
  ON CONFLICT (tenant_id,key) DO UPDATE SET value='3';" >/dev/null 2>&1

echo "==== 通道端到端冒烟 @ $B (mock wx @ $MOCK) ===="

# ---- 0. 构建 + 起服务 ----
echo "---- 零、构建与启动 ----"
( cd "$ROOT" && go build -o bin/ai-scrm-chan ./cmd/server ) && check "后端编译(bin)" y y || check "后端编译" y n
( cd "$ROOT" && go build -o bin/mockwx ./cmd/mockwx ) && check "mockwx 编译" y y || check "mockwx 编译" y n

"$ROOT/bin/mockwx" -listen "127.0.0.1:${MPORT}" -out "$OUTBOX" >"$MLOG" 2>&1 &
MOCK_PID=$!
AI_MOCK_MODE=true SERVER_PORT="$CPORT" GIN_MODE=debug "$ROOT/bin/ai-scrm-chan" >"$SLOG" 2>&1 &
SRV_PID=$!

# 等待健康
for i in $(seq 1 30); do
  curl -s --max-time 2 "$B/health" >/dev/null 2>&1 && break
  sleep 1
done
HC=$(curl -s -o /dev/null -w "%{http_code}" "$B/health")
check "mock模式服务已就绪(9091)" 200 "$HC"

psql "$DBURL" -c "UPDATE tenant_users SET must_change_password=false WHERE username='admin';" >/dev/null 2>&1
psql "$DBURL" -c "DELETE FROM channels WHERE corpid='ww_chan_smoke';" >/dev/null 2>&1

TOKEN=$(curl -s -X POST "$B/api/v1/auth/login" -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"admin123"}' | jsonget "['data']['token']")
check "管理员登录" y "$([ -n "$TOKEN" ] && echo y || echo n)"

# ---- 一、通道 CRUD + 凭据掩码 ----
echo "---- 一、通道 CRUD 与凭据安全 ----"
CREATE=$(curl -s -X POST "$B/api/v1/admin/channels" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1" \
  -H "Content-Type: application/json" \
  -d '{"type":"wecom_app","name":"冒烟企微应用","corpid":"ww_chan_smoke","agentid":"1000002","secret":"sEcrEt_TOP_999","token":"tk_smoke_001","encoding_aes_key":"jWmYm7qr5nMoAUwZRjGtBxmz3KA1tkAj3ykkR6q2B2C","config_json":"{\"mock_base_url\":\"http://127.0.0.1:'"$MPORT"'\"}"}')
CID=$(echo "$CREATE" | jsonget "['data']['channel']['id']")
CBURL=$(echo "$CREATE" | jsonget "['data']['callback_url']")
PLAIN=$(echo "$CREATE" | jsonget "['data']['plaintext']['secret']")
check "创建通道返回id" y "$([ -n "$CID" ] && [ "$CID" != "None" ] && echo y || echo n)"
check "创建一次性回显明文secret" "sEcrEt_TOP_999" "$PLAIN"
check "回调URL含通道id" y "$(echo "$CBURL" | grep -q "/channel/callback/$CID" && echo y || echo n)"

LIST=$(curl -s "$B/api/v1/admin/channels" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1")
echo "$LIST" | grep -q "sEcrEt_TOP_999" && check "列表不泄露明文secret" n y || check "列表不泄露明文secret" n n
echo "$LIST" | grep -q "secret_mask" && check "列表含掩码字段" y y || check "列表含掩码字段" y n

# ---- 二、连通性测试（gettoken via mock）----
echo "---- 二、连通性测试 ----"
VERIFY=$(curl -s -X POST "$B/api/v1/admin/channels/$CID/verify" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1")
check "verify 拉到 token 置 ok" "True" "$(echo "$VERIFY" | jsonget "['data']['ok']")"
LIST2=$(curl -s "$B/api/v1/admin/channels" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1")
check "verify 后 status=active" "active" "$(echo "$LIST2" | python3 -c "import sys,json
d=json.load(sys.stdin)['data']['list']
mine=[x for x in d if x['id']==int('$CID')]
print(mine[0]['status'] if mine else 'none')" 2>/dev/null)"

# ---- 三、URL 验证回调（GET echostr 解密回显）----
echo "---- 三、URL 验证回调 ----"
GEN=$(curl -s "$MOCK/__gen_echostr?token=tk_smoke_001&aeskey=jWmYm7qr5nMoAUwZRjGtBxmz3KA1tkAj3ykkR6q2B2C&corpid=ww_chan_smoke&echo=echo_ABC123")
ECHO=$(echo "$GEN" | jsonget "['data']['echostr']" 2>/dev/null)
ECHO=$(echo "$GEN" | python3 -c "import sys,json;print(json.load(sys.stdin)['echostr'])" 2>/dev/null)
SIG=$(echo "$GEN" | python3 -c "import sys,json;print(json.load(sys.stdin)['msg_signature'])" 2>/dev/null)
TS=$(echo "$GEN" | python3 -c "import sys,json;print(json.load(sys.stdin)['timestamp'])" 2>/dev/null)
NC=$(echo "$GEN" | python3 -c "import sys,json;print(json.load(sys.stdin)['nonce'])" 2>/dev/null)
ECHOBACK=$(curl -s -G "$B/api/v1/channel/callback/$CID" \
  --data-urlencode "echostr=$ECHO" --data-urlencode "msg_signature=$SIG" \
  --data-urlencode "timestamp=$TS" --data-urlencode "nonce=$NC")
check "GET URL验证回显明文" "echo_ABC123" "$ECHOBACK"

# ---- 四、入站→AI→出站投递 mock ----
echo "---- 四、入站消息处理与出站 ----"
GC=$(curl -s "$MOCK/__gen_callback?token=tk_smoke_001&aeskey=jWmYm7qr5nMoAUwZRjGtBxmz3KA1tkAj3ykkR6q2B2C&corpid=ww_chan_smoke&content=%E4%BD%A0%E5%A5%BD%E6%88%91%E6%83%B3%E4%BA%86%E8%A7%A3%E8%B6%8A%E9%87%8E%E8%BD%A6&from=wm_smoke_user_1")
BODY=$(echo "$GC" | python3 -c "import sys,json;print(json.load(sys.stdin)['body'])" 2>/dev/null)
SIG2=$(echo "$GC" | python3 -c "import sys,json;print(json.load(sys.stdin)['msg_signature'])" 2>/dev/null)
TS2=$(echo "$GC" | python3 -c "import sys,json;print(json.load(sys.stdin)['timestamp'])" 2>/dev/null)
NC2=$(echo "$GC" | python3 -c "import sys,json;print(json.load(sys.stdin)['nonce'])" 2>/dev/null)
# body 含换行/特殊字符，走文件避免 shell 转义问题
BF="$(mktemp /tmp/chan_body.XXXXXX)"
printf '%s' "$BODY" > "$BF"
REPL=$(curl -s -X POST "$B/api/v1/channel/callback/$CID?msg_signature=$SIG2&timestamp=$TS2&nonce=$NC2" \
  -H "Content-Type: application/xml" --data-binary "@$BF")
check "入站回调返回 success" "success" "$REPL"

# 出站是异步 worker(3s)：轮询 mock outbox 出现该客户回信（≤20s）
got=0
for i in $(seq 1 20); do
  if grep -q "wm_smoke_user_1" "$OUTBOX" 2>/dev/null; then got=1; break; fi
  sleep 1
done
check "AI 回复经出站队列投递到 mock" y "$([ "$got" = 1 ] && echo y || echo n)"

# 出站消息应带 access_token 且正文非空
HASLINE=$(grep -c "wm_smoke_user_1" "$OUTBOX" 2>/dev/null)
check "出站落 JSONL ≥1 条" y "$([ "${HASLINE:-0}" -ge 1 ] && echo y || echo n)"
HASJSON=$(grep "wm_smoke_user_1" "$OUTBOX" | head -1 | python3 -c "import sys,json;l=sys.stdin.readline();d=json.loads(l);print('y' if d.get('has_token') and d.get('content') else 'n')" 2>/dev/null)
check "出站含token且正文非空" "y" "$HASJSON"

# 站内落库：identity + customer + 双向消息
IDENT=$(psql "$DBURL" -tAc "SELECT count(*) FROM channel_identities WHERE channel_id=$CID AND external_id='wm_smoke_user_1'" | tr -d '[:space:]')
check "渠道客户映射已建(OneID桥)" "1" "$IDENT"
MSGS=$(psql "$DBURL" -tAc "SELECT count(*) FROM messages m JOIN channel_identities ci ON ci.customer_id=m.customer_id WHERE ci.channel_id=$CID AND m.sender_type='customer'" | tr -d '[:space:]')
check "入站客户消息已落库" y "$([ "${MSGS:-0}" -ge 1 ] && echo y || echo n)"
AIMSGS=$(psql "$DBURL" -tAc "SELECT count(*) FROM messages m JOIN channel_identities ci ON ci.customer_id=m.customer_id WHERE ci.channel_id=$CID AND m.sender_type='ai'" | tr -d '[:space:]')
check "AI 回复已落库" y "$([ "${AIMSGS:-0}" -ge 1 ] && echo y || echo n)"
OB=$(psql "$DBURL" -tAc "SELECT status FROM channel_outbound WHERE channel_id=$CID ORDER BY id DESC LIMIT 1" | tr -d '[:space:]')
check "出站行置 sent" "sent" "$OB"

# ---- 四b、D5 合并队列并入断言：连发 3 条只回 1 条（与网页端同语义）----
echo "---- 四b、通道连发合并只回一条（D5） ----"
send_cb() { # $1=urlencode content
  local GC BODY SIG TS NC
  GC=$(curl -s "$MOCK/__gen_callback?token=tk_smoke_001&aeskey=jWmYm7qr5nMoAUwZRjGtBxmz3KA1tkAj3ykkR6q2B2C&corpid=ww_chan_smoke&content=$1&from=wm_smoke_user_1")
  BODY=$(echo "$GC" | python3 -c "import sys,json;print(json.load(sys.stdin)['body'])" 2>/dev/null)
  SIG=$(echo "$GC" | python3 -c "import sys,json;print(json.load(sys.stdin)['msg_signature'])" 2>/dev/null)
  TS=$(echo "$GC" | python3 -c "import sys,json;print(json.load(sys.stdin)['timestamp'])" 2>/dev/null)
  NC=$(echo "$GC" | python3 -c "import sys,json;print(json.load(sys.stdin)['nonce'])" 2>/dev/null)
  printf '%s' "$BODY" > "$BF"
  curl -s -o /dev/null -X POST "$B/api/v1/channel/callback/$CID?msg_signature=$SIG&timestamp=$TS&nonce=$NC" \
    -H "Content-Type: application/xml" --data-binary "@$BF"
}
OB_BASE=$(psql "$DBURL" -tAc "SELECT count(*) FROM channel_outbound WHERE channel_id=$CID" | tr -d '[:space:]')
AI_BASE=$(psql "$DBURL" -tAc "SELECT count(*) FROM messages m JOIN channel_identities ci ON ci.customer_id=m.customer_id WHERE ci.channel_id=$CID AND m.sender_type='ai'" | tr -d '[:space:]')
send_cb "%E8%BF%99%E8%BD%A6%E6%94%AF%E6%8C%81%E5%BF%AB%E5%85%85%E5%90%97"   # 这车支持快充吗
send_cb "%E5%90%8E%E5%A4%87%E7%AE%B1%E5%A4%9A%E5%A4%A7"                    # 后备箱多大
send_cb "%E6%9C%89%E5%BA%A7%E6%A4%85%E5%8A%A0%E7%83%AD%E5%90%97"          # 有座椅加热吗
got=0
for i in $(seq 1 25); do
  OB_NOW=$(psql "$DBURL" -tAc "SELECT count(*) FROM channel_outbound WHERE channel_id=$CID" | tr -d '[:space:]')
  [ "${OB_NOW:-0}" -gt "${OB_BASE:-0}" ] && { got=1; break; }
  sleep 1
done
check "连发3条窗口内收到回复" y "$([ "$got" = 1 ] && echo y || echo n)"
sleep 4 # 让潜在的"逐条回复"尾巴露出来（合并失效时应见 3 条）
OB_DELTA=$(psql "$DBURL" -tAc "SELECT count(*) FROM channel_outbound WHERE channel_id=$CID" | tr -d '[:space:]')
OB_DELTA=$((OB_DELTA - OB_BASE))
AI_NOW=$(psql "$DBURL" -tAc "SELECT count(*) FROM messages m JOIN channel_identities ci ON ci.customer_id=m.customer_id WHERE ci.channel_id=$CID AND m.sender_type='ai'" | tr -d '[:space:]')
AI_DELTA=$((AI_NOW - AI_BASE))
check "连发3条恰好只投1条出站" 1 "$([ "$got" = 1 ] && echo "$OB_DELTA" || echo 0)"
check "连发3条恰好只落1条AI消息" 1 "$([ "$got" = 1 ] && echo "$AI_DELTA" || echo 0)"
CUST_NOW=$(psql "$DBURL" -tAc "SELECT count(*) FROM messages m JOIN channel_identities ci ON ci.customer_id=m.customer_id WHERE ci.channel_id=$CID AND m.sender_type='customer'" | tr -d '[:space:]')
check "3条客户消息全部落库(历史不丢)" y "$([ "${CUST_NOW:-0}" -ge 4 ] && echo y || echo n)"

# ---- 五、人工锁定：AI 不出声（转人工无感知）----
echo "---- 五、人工锁定态不自动回复 ----"
psql "$DBURL" -c "UPDATE conversations SET is_human_locked=true, is_ai_reply_enabled=false, mode='human' WHERE customer_id IN (SELECT customer_id FROM channel_identities WHERE channel_id=$CID);" >/dev/null 2>&1
OB_BEFORE=$(psql "$DBURL" -tAc "SELECT count(*) FROM channel_outbound WHERE channel_id=$CID" | tr -d '[:space:]')
# 再来一条入站（换个外部号需先解锁同客户；这里复用同用户）
GC2=$(curl -s "$MOCK/__gen_callback?token=tk_smoke_001&aeskey=jWmYm7qr5nMoAUwZRjGtBxmz3KA1tkAj3ykkR6q2B2C&corpid=ww_chan_smoke&content=%E5%86%8D%E9%97%AE%E4%B8%80%E4%B8%8B&from=wm_smoke_user_1")
printf '%s' "$(echo "$GC2" | python3 -c "import sys,json;print(json.load(sys.stdin)['body'])")" > "$BF"
curl -s -o /dev/null -X POST "$B/api/v1/channel/callback/$CID?msg_signature=$(echo "$GC2" | python3 -c "import sys,json;print(json.load(sys.stdin)['msg_signature'])")&timestamp=$(echo "$GC2" | python3 -c "import sys,json;print(json.load(sys.stdin)['timestamp'])")&nonce=$(echo "$GC2" | python3 -c "import sys,json;print(json.load(sys.stdin)['nonce'])")" -H "Content-Type: application/xml" --data-binary "@$BF"
sleep 2
OB_AFTER=$(psql "$DBURL" -tAc "SELECT count(*) FROM channel_outbound WHERE channel_id=$CID" | tr -d '[:space:]')
check "人工锁定态不新增出站" y "$([ "$OB_BEFORE" = "$OB_AFTER" ] && echo y || echo n)"

# ---- 六、死信可见 + 人工重发 ----
echo "---- 六、出站死信 ----"
# 造一条 failed 死信
NOW=$(psql "$DBURL" -tAc "SELECT customer_id FROM channel_identities WHERE channel_id=$CID LIMIT 1" | tr -d '[:space:]')
psql "$DBURL" -c "INSERT INTO channel_outbound (tenant_id,channel_id,customer_id,content,msg_type,status,retries,error,created_at,updated_at) VALUES (1,$CID,${NOW:-0},'死信测试','text','failed',6,'mock dead letter',NOW(),NOW());" >/dev/null 2>&1
DLQ=$(curl -s "$B/api/v1/admin/channel-dlq" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1")
DLID=$(echo "$DLQ" | python3 -c "import sys,json;d=json.load(sys.stdin)['data']['list'];print(d[0]['id'] if d else '')" 2>/dev/null)
check "死信列表可见" y "$([ -n "$DLID" ] && echo y || echo n)"
curl -s -o /dev/null -X POST "$B/api/v1/admin/channel-dlq/$DLID/retry" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1"
DLST=$(psql "$DBURL" -tAc "SELECT status FROM channel_outbound WHERE id=$DLID" | tr -d '[:space:]')
check "死信重发置 pending" "pending" "$DLST"

# ---- 七、鉴权与隔离 ----
echo "---- 七、通道接口鉴权 ----"
NOCODE=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/admin/channels")
check "未登录访问通道列表 401/403" y "$(echo "$NOCODE" | grep -qE '401|403' && echo y || echo n)"
CBBAD=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/channel/callback/999999?echostr=x")
check "不存在通道的回调 404" "404" "$CBBAD"

# ---- 八、公众号通道 E2E + 侧边栏 JS 配置（W5/W7）----
echo "---- 八、公众号通道与侧边栏 ----"
AESKEY="jWmYm7qr5nMoAUwZRjGtBxmz3KA1tkAj3ykkR6q2B2C"
CREATE_MP=$(curl -s -X POST "$B/api/v1/admin/channels" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1" \
  -H "Content-Type: application/json" \
  -d '{"type":"wechat_mp","name":"冒烟公众号","corpid":"ww_chan_smoke","appid":"wx_mp_smoke","secret":"mpS3cr3t","token":"tk_smoke_001","encoding_aes_key":"'"$AESKEY"'","config_json":"{\"mock_base_url\":\"http://127.0.0.1:'"$MPORT"'\"}"}')
MPID=$(echo "$CREATE_MP" | jsonget "['data']['channel']['id']")
check "创建公众号通道" y "$([ -n "$MPID" ] && [ "$MPID" != "None" ] && echo y || echo n)"
# 公众号需 verify 拉 token 置 active，出站 worker 才会发送
curl -s -o /dev/null -X POST "$B/api/v1/admin/channels/$MPID/verify" -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1"

# 公众号入站：receive_id=appid，用同一 token/aeskey 生成签名回调
GCMP=$(curl -s "$MOCK/__gen_callback?token=tk_smoke_001&aeskey=$AESKEY&corpid=wx_mp_smoke&content=%E5%85%AC%E4%BC%97%E5%8F%B7%E9%97%AE%E5%80%99&from=wxmp_user_1")
MPBODY=$(echo "$GCMP" | python3 -c "import sys,json;print(json.load(sys.stdin)['body'])" 2>/dev/null)
MPSIG=$(echo "$GCMP" | jsonget "['msg_signature']")
MPTS=$(echo "$GCMP" | jsonget "['timestamp']")
MPNC=$(echo "$GCMP" | jsonget "['nonce']")
MBF="$(mktemp /tmp/chan_mpbody.XXXXXX)"
printf '%s' "$MPBODY" > "$MBF"
MPREP=$(curl -s -X POST "$B/api/v1/channel/callback/$MPID?msg_signature=$MPSIG&timestamp=$MPTS&nonce=$MPNC" \
  -H "Content-Type: application/xml" --data-binary "@$MBF")
check "公众号入站回调 success" "success" "$MPREP"
got=0
for i in $(seq 1 20); do
  sleep 1
  if grep -q "wxmp_user_1" "$OUTBOX" 2>/dev/null; then got=1; break; fi
done
check "公众号 AI 回复经出站投递" y "$([ "$got" = 1 ] && echo y || echo n)"
MPIDENT=$(psql "$DBURL" -tAc "SELECT count(*) FROM channel_identities WHERE channel_id=$MPID AND external_id='wxmp_user_1'" | tr -d '[:space:]')
check "公众号客户映射已建" "1" "$MPIDENT"

# 侧边栏 JS-SDK 配置（复用企微应用通道 corpid）
JSCFG=$(curl -s "$B/api/v1/channel/wecom/jsconfig?url=https%3A%2F%2Fexample.com%2Fpage%3Fa%3D1&corpid=ww_chan_smoke" \
  -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1")
SIGLEN=$(echo "$JSCFG" | python3 -c "import sys,json;d=json.load(sys.stdin)['data'];print(len(d['signature']))" 2>/dev/null)
check "JS-SDK 签名 40 位" "40" "$SIGLEN"
JSCORP=$(echo "$JSCFG" | jsonget "['data']['corpid']")
check "JS-SDK 返回 corpid" "ww_chan_smoke" "$JSCORP"

# 侧边栏客户上下文（企微应用用户 wm_smoke_user_1 已有会话）
CTX=$(curl -s "$B/api/v1/channel/wecom/context?corpid=ww_chan_smoke&external_userid=wm_smoke_user_1" \
  -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: 1")
CXID=$(echo "$CTX" | python3 -c "import sys,json;d=json.load(sys.stdin)['data'];print('y' if int(d['customer_id'])>0 else 'n')" 2>/dev/null)
check "侧边栏客户上下文可取" "y" "$CXID"

# ---- 九、2026-09-15 复核批修复护栏（P0-4 侧边栏隔离 / P1-7 停用租户闸）----
echo "---- 九、复核批修复护栏 ----"
CHTEN=$($PSQL "SELECT tenant_id FROM channels WHERE id=$CID" | tr -d '[:space:]')
# P0-4：jsconfig/context 按 corpid 全局找通道——修复前任意登录用户知道 corpid 即可读他司
# 客户画像/消息；现必须核对"调用者租户 == 通道归属租户"。造一个他租户 admin 做负向断言。
XTEN=$($PSQL "INSERT INTO tenants (name,code,status,created_at,updated_at) VALUES ('chan-cross-smoke','chan_cross_smoke','active',NOW(),NOW()) RETURNING id" | head -1 | tr -d '[:space:]')
XUSR="chanx_admin_${RANDOM}"
$PSQL "INSERT INTO tenant_users (username,password_hash,role,tenant_id,status,created_at,updated_at) SELECT '$XUSR',password_hash,'tenant_admin',$XTEN,1,NOW(),NOW() FROM tenant_users WHERE username='admin' LIMIT 1" >/dev/null 2>&1
XTK=$(curl -s -X POST "$B/api/v1/auth/login" -H "Content-Type: application/json" \
  -d "{\"username\":\"$XUSR\",\"password\":\"admin123\"}" | jsonget "['data']['token']")
XCTX=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/channel/wecom/context?corpid=ww_chan_smoke&external_userid=wm_smoke_user_1" \
  -H "Authorization: Bearer $XTK" -H "X-Tenant-ID: $XTEN")
check "他租户取侧边栏上下文→403(P0-4)" 403 "$XCTX"
XJS=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/channel/wecom/jsconfig?url=https%3A%2F%2Fexample.com&corpid=ww_chan_smoke" \
  -H "Authorization: Bearer $XTK" -H "X-Tenant-ID: $XTEN")
check "他租户取侧边栏jsconfig→403(P0-4)" 403 "$XJS"
$PSQL "DELETE FROM tenant_users WHERE username='$XUSR'; DELETE FROM tenants WHERE id=$XTEN" >/dev/null 2>&1

# P1-7：回调路径 skip 了租户解析——修复前 suspended 租户通道照常"入站→AI→出站"烧被封禁户用量。
# 停用后回调须"回 success 止损微信重推但不落库不触发 AI"；30s 进程缓存过期后才生效。
INBOUND_BEFORE=$($PSQL "SELECT count(*) FROM channel_inbound_msgs WHERE channel_id=$CID" | tr -d '[:space:]')
$PSQL "UPDATE tenants SET status='suspended' WHERE id=$CHTEN" >/dev/null 2>&1
sleep 31
GC3=$(curl -s "$MOCK/__gen_callback?token=tk_smoke_001&aeskey=jWmYm7qr5nMoAUwZRjGtBxmz3KA1tkAj3ykkR6q2B2C&corpid=ww_chan_smoke&content=%E5%81%9C%E7%94%A8%E6%80%81%E6%B5%8B%E8%AF%95%E6%B6%88%E6%81%AF&from=wm_smoke_user_3")
BODY3=$(echo "$GC3" | python3 -c "import sys,json;print(json.load(sys.stdin)['body'])" 2>/dev/null)
SIG3=$(echo "$GC3" | python3 -c "import sys,json;print(json.load(sys.stdin)['msg_signature'])" 2>/dev/null)
TS3=$(echo "$GC3" | python3 -c "import sys,json;print(json.load(sys.stdin)['timestamp'])" 2>/dev/null)
NC3=$(echo "$GC3" | python3 -c "import sys,json;print(json.load(sys.stdin)['nonce'])" 2>/dev/null)
BF3="$(mktemp /tmp/chan_body.XXXXXX)"
printf '%s' "$BODY3" > "$BF3"
REPL3=$(curl -s -X POST "$B/api/v1/channel/callback/$CID?msg_signature=$SIG3&timestamp=$TS3&nonce=$NC3" \
  -H "Content-Type: application/xml" --data-binary "@$BF3"; rm -f "$BF3")
check "停用租户回调仍回success止损重推" "success" "$REPL3"
INBOUND_AFTER=$($PSQL "SELECT count(*) FROM channel_inbound_msgs WHERE channel_id=$CID" | tr -d '[:space:]')
check "停用租户入站不落库不烧AI(P1-7)" y "$([ "$INBOUND_BEFORE" = "$INBOUND_AFTER" ] && echo y || echo n)"
$PSQL "UPDATE tenants SET status='active' WHERE id=$CHTEN" >/dev/null 2>&1

echo "==== 结果: PASS=$PASS FAIL=$FAIL ===="
[ "$FAIL" = "0" ] || exit 1
