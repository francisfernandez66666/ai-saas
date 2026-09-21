#!/bin/bash
# ============================================================
# 顾问工作台（AI Advisor）端到端字节级 UAT
# 用法: ./tools/uat_advisor.sh [端口]   默认 9090
# 前置: 服务已启动；默认租户(1)存在 admin/sales1/sales2 账号
# 覆盖: 顾问域 24 个端点全遍历 + 写操作「API 返回 ↔ DB 落库」字节级比对
#       + 四级数据范围越权负向 + 前后端契约（前端实际调用路径全打一遍）
# 说明: 与 uat.sh/smoke.sh 一样会写库（客户/标签/跟进/试驾/消息），
#       但不动全局开关，可与其它脚本串行跑（勿并发）。
# ============================================================
PORT="${1:-9090}"
B="http://localhost:${PORT}"
PSQL="psql ${TEST_DB_URL:-postgresql://ai_scrm:dev123@localhost/ai_scrm} -tAc"
PASS=0
FAIL=0

check() { if [ "$2" = "$3" ]; then echo "  PASS  $1 ($3)"; PASS=$((PASS+1)); else echo "  FAIL  $1 期望=[$2] 实际=[$3]"; FAIL=$((FAIL+1)); fi; }
jget() { python3 -c "import sys,json;d=json.load(sys.stdin);print(eval('d'+sys.argv[1]))" "$1" 2>/dev/null; }
strval() { python3 -c "import sys,json;d=json.load(sys.stdin);v=eval('d'+sys.argv[1]);print('' if v is None else v)" "$1" 2>/dev/null; }

echo "==== 顾问工作台(AI Advisor) 字节级 UAT @ $B ===="

# ---------- 准备：清强改密标记 ----------
$PSQL "UPDATE tenant_users SET must_change_password=false WHERE username IN ('admin','sales1','sales2');" >/dev/null 2>&1
$PSQL "UPDATE tenant_users SET status=1 WHERE username IN ('sales1','sales2');" >/dev/null 2>&1

# ---------- 登录三角色 ----------
AT=$(curl -s -m 10 -X POST "$B/api/v1/auth/login" -H "Content-Type: application/json" -d '{"username":"admin","password":"admin123"}' | jget "['data']['token']")
S1=$(curl -s -m 10 -X POST "$B/api/v1/auth/login" -H "Content-Type: application/json" -d '{"username":"sales1","password":"sales123"}' | jget "['data']['token']")
S2=$(curl -s -m 10 -X POST "$B/api/v1/auth/login" -H "Content-Type: application/json" -d '{"username":"sales2","password":"sales123"}' | jget "['data']['token']")
check "admin 登录" y "$([ ${#AT} -gt 50 ] && echo y || echo n)"
check "sales1 登录" y "$([ ${#S1} -gt 50 ] && echo y || echo n)"
check "sales2 登录" y "$([ ${#S2} -gt 50 ] && echo y || echo n)"
U1=$($PSQL "SELECT id FROM tenant_users WHERE username='sales1' AND tenant_id=1" | tr -d '[:space:]')
U2=$($PSQL "SELECT id FROM tenant_users WHERE username='sales2' AND tenant_id=1" | tr -d '[:space:]')
echo "  (sales1=$U1 sales2=$U2)"

AH="Authorization: Bearer $AT"
S1H="Authorization: Bearer $S1"
S2H="Authorization: Bearer $S2"

# ---------- 造数：C 端访客 + 会话 ----------
echo "---- 一、造数：访客 / 会话 ----"
G=$(curl -s -m 10 -X POST "$B/api/v1/chat/guest" -H "Content-Type: application/json" -H "X-Tenant-ID: 1" -d '{}')
CID=$(echo "$G" | jget "['data']['customer_id']")
VK=$(echo "$G" | jget "['data']['visitor_key']")
check "访客建联返回 customer_id" y "$([ -n "$CID" ] && echo y || echo n)"
W=$(curl -s -m 15 -X POST "$B/api/v1/chat/welcome?visitor_key=$VK" -H "Content-Type: application/json" -H "X-Tenant-ID: 1" -d "{\"customer_id\":$CID}")
CONV=$(echo "$W" | jget "['data']['conversation_id']")
check "客户建会话返回 conversation_id" y "$([ -n "$CONV" ] && echo y || echo n)"

# ---------- 未分配不可见 ----------
echo "---- 二、数据范围：未分配客户对顾问不可见 ----"
$PSQL "UPDATE customers SET assigned_user_id=0 WHERE id=$CID" >/dev/null 2>&1
IDS=$(curl -s -m 10 "$B/api/v1/advisor/customers?page_size=100" -H "$S1H" | jget "['data']['list']" 2>/dev/null)
HAS=$(curl -s -m 10 "$B/api/v1/advisor/customers?page_size=100" -H "$S1H" | python3 -c "
import sys,json
try:
  l=json.load(sys.stdin)['data']['list']
  print('y' if any(str(c['id'])=='$CID' for c in l) else 'n')
except Exception: print('err')")
check "未分配客户不出现在顾问列表" n "$HAS"
# admin 用 assigned=all 应可见
HASADM=$(curl -s -m 10 "$B/api/v1/advisor/customers?page_size=100&assigned=all" -H "$AH" -H "X-Tenant-ID: 1" | python3 -c "
import sys,json
try:
  l=json.load(sys.stdin)['data']['list']
  print('y' if any(str(c['id'])=='$CID' for c in l) else 'n')
except Exception: print('err')")
check "admin(assigned=all)可见未分配客户" y "$HASADM"
# 普通顾问传 assigned=all 不得绕过
HASBYP=$(curl -s -m 10 "$B/api/v1/advisor/customers?page_size=100&assigned=all" -H "$S1H" | python3 -c "
import sys,json
try:
  l=json.load(sys.stdin)['data']['list']
  print('y' if any(str(c['id'])=='$CID' for c in l) else 'n')
except Exception: print('err')")
check "顾问传 assigned=all 不绕过(仍不可见)" n "$HASBYP"

# ---------- 分配给 sales1 ----------
$PSQL "UPDATE customers SET assigned_user_id=$U1 WHERE id=$CID" >/dev/null 2>&1

# ---------- 客户列表/详情 ----------
echo "---- 三、客户列表与详情（字段级）----"
L=$(curl -s -m 10 "$B/api/v1/advisor/customers?page_size=100&status=all" -H "$S1H")
HASF=$(echo "$L" | python3 -c "
import sys,json
d=json.load(sys.stdin)['data']
l=[c for c in d['list'] if str(c['id'])=='$CID']
need=['last_message','conv_mode','lead_status','assigned_user_name','last_message_at']
print('y' if (l and all(n in l[0] for n in need)) else 'n')")
check "列表附带 5 个扩展字段" y "$HASF"

D=$(curl -s -m 10 "$B/api/v1/advisor/customer/$CID" -H "$S1H")
DKEYS=$(echo "$D" | python3 -c "
import sys,json
d=json.load(sys.stdin)['data']
need=['customer','assigned_user_name','tags','followups','conversations','followup_stats']
print('y' if all(k in d for k in need) else 'n')")
check "详情 6 个键齐全" y "$DKEYS"
AUNAME=$(echo "$D" | strval "['data']['assigned_user_name']")
check "详情回显分配顾问姓名非空" y "$([ -n "$AUNAME" ] && echo y || echo n)"

# sales2 越权
C404=$(curl -s -o /dev/null -m 10 -w "%{http_code}" "$B/api/v1/advisor/customer/$CID" -H "$S2H")
check "sales2 看 sales1 名下客户→404(不泄露)" 404 "$C404"
C403=$(curl -s -o /dev/null -m 10 -w "%{http_code}" -X PUT "$B/api/v1/advisor/customer/$CID/info" -H "$S2H" -H "Content-Type: application/json" -d '{"name":"越权"}')
check "sales2 改 sales1 客户资料→404" 404 "$C403"

# ---------- 编辑客户资料（字节级）----------
echo "---- 四、写操作 API↔DB 字节级比对 ----"
NM="张伟_UAT"; PH="13800001111"; BD="38.5"; CT="上海"; RM="uat备注"
R=$(curl -s -m 10 -X PUT "$B/api/v1/advisor/customer/$CID/info" -H "$S1H" -H "Content-Type: application/json" \
  -d "{\"name\":\"$NM\",\"phone\":\"$PH\",\"budget\":$BD,\"city\":\"$CT\",\"remark\":\"$RM\"}")
AN=$(echo "$R" | strval "['data']['name']"); AP=$(echo "$R" | strval "['data']['phone']"); AC=$(echo "$R" | strval "['data']['city']"); AR=$(echo "$R" | strval "['data']['remark']")
check "info: name 回显一致" "$NM" "$AN"
check "info: phone 回显一致" "$PH" "$AP"
check "info: city 回显一致" "$CT" "$AC"
check "info: remark 回显一致" "$RM" "$AR"
DN=$($PSQL "SELECT name FROM customers WHERE id=$CID" | tr -d '[:space:]')
DP=$($PSQL "SELECT phone FROM customers WHERE id=$CID" | tr -d '[:space:]')
DB_=$($PSQL "SELECT budget FROM customers WHERE id=$CID" | tr -d '[:space:]')
DR=$($PSQL "SELECT remark FROM customers WHERE id=$CID" | tr -d '[:space:]')
check "info: name 落库字节一致" "$NM" "$DN"
check "info: phone 落库字节一致" "$PH" "$DP"
check "info: budget 落库字节一致" "$BD" "$DB_"
check "info: remark 落库字节一致" "$RM" "$DR"

# ---------- 标签覆盖更新 ----------
# 注：'VIP客户' 在种子标签库中不存在（ApplyTagsToCustomer 对未知名按既有口径跳过），
# 改用真实种子标 高意向/价格敏感 做集合断言（首跑实证脚本名错配）。
TR=$(curl -s -m 10 -X PUT "$B/api/v1/advisor/customer/$CID/tags" -H "$S1H" -H "Content-Type: application/json" -d '{"tags":["高意向","价格敏感"]}')
TSET=$($PSQL "SELECT count(*) FROM customer_tags WHERE customer_id=$CID AND tag_name IN ('高意向','价格敏感') AND source='manual'" | tr -d '[:space:]')
# P0(DEFECT_VERIFY_2026-09-20)已修复（批四）：ON CONFLICT 两参 MAX 坏 SQL + 错误被吞致
# 手工标零落库/恒 auto——修复后此断言必须硬绿，不再设 KNOWN 豁免。
check "tags: 覆盖更新后两标 manual 落库" 2 "$TSET"
TR2=$(curl -s -m 10 -X PUT "$B/api/v1/advisor/customer/$CID/tags" -H "$S1H" -H "Content-Type: application/json" -d '{"tags":["高意向"]}')
TSET2=$($PSQL "SELECT count(*) FROM customer_tags WHERE customer_id=$CID AND tag_name='价格敏感'" | tr -d '[:space:]')
check "tags: 二次覆盖清掉旧标签" 0 "$TSET2"
TERR=$(curl -s -o /dev/null -m 10 -w "%{http_code}" -X PUT "$B/api/v1/advisor/customer/$CID/tags" -H "$S1H" -H "Content-Type: application/json" -d '{}')
check "tags: 缺 tags 字段→400" 400 "$TERR"

# ---------- 阶段推进 ----------
SR=$(curl -s -m 10 -X PUT "$B/api/v1/advisor/customer/$CID/stage" -H "$S1H" -H "Content-Type: application/json" -d '{"journey_stage":"arrived","journey_sub_stage":"test_driven"}')
DJS=$($PSQL "SELECT journey_stage FROM customers WHERE id=$CID" | tr -d '[:space:]')
DSS=$($PSQL "SELECT journey_sub_stage FROM customers WHERE id=$CID" | tr -d '[:space:]')
DSV=$($PSQL "SELECT store_visited FROM customers WHERE id=$CID" | tr -d '[:space:]')
check "stage: journey_stage 落库" arrived "$DJS"
check "stage: sub_stage 落库" test_driven "$DSS"
check "stage: store_visited=1" 1 "$DSV"
E1=$(curl -s -o /dev/null -m 10 -w "%{http_code}" -X PUT "$B/api/v1/advisor/customer/$CID/stage" -H "$S1H" -H "Content-Type: application/json" -d '{"journey_stage":"bogus"}')
check "stage: 非法阶段→400" 400 "$E1"
E2=$(curl -s -o /dev/null -m 10 -w "%{http_code}" -X PUT "$B/api/v1/advisor/customer/$CID/stage" -H "$S1H" -H "Content-Type: application/json" -d '{"journey_stage":"arrived","journey_sub_stage":"bogus"}')
check "stage: 非法子状态→400" 400 "$E2"

# ---------- 跟进 ----------
NFA=$(python3 -c "import datetime;print((datetime.datetime.now()+datetime.timedelta(days=2)).strftime('%Y-%m-%dT10:00:00+08:00'))")
FR=$(curl -s -m 10 -X POST "$B/api/v1/advisor/customer/$CID/followup" -H "$S1H" -H "Content-Type: application/json" \
  -d "{\"customer_id\":$CID,\"method\":\"phone\",\"content\":\"uat跟进内容\",\"next_follow_at\":\"$NFA\"}")
FOK=$(echo "$FR" | jget "['code']")
check "followup: 创建成功" 0 "$FOK"
FC=$($PSQL "SELECT content FROM follow_ups WHERE customer_id=$CID ORDER BY id DESC LIMIT 1" | tr -d '[:space:]')
check "followup: content 落库字节一致" "uat跟进内容" "$FC"
FM=$($PSQL "SELECT method FROM follow_ups WHERE customer_id=$CID ORDER BY id DESC LIMIT 1" | tr -d '[:space:]')
check "followup: method 落库" phone "$FM"
FL=$(curl -s -m 10 "$B/api/v1/advisor/followups" -H "$S1H")
FLK=$(echo "$FL" | python3 -c "
import sys,json
d=json.load(sys.stdin)['data']
print('y' if (isinstance(d,list) and len(d)>0 and 'customer_name' in d[0]) else 'n')")
check "followups: 列表附带 customer_name" y "$FLK"
# 伪造 user_id 不得越权
FSP=$(curl -s -o /dev/null -m 10 -w "%{http_code}" "$B/api/v1/advisor/followups?user_id=1" -H "$S1H")
check "followups: 顾问伪造 user_id 不报错(被覆盖)" 200 "$FSP"

# ---------- 试驾单 ----------
echo "---- 五、试驾单 CRUD（字节级）----"
SCH=$(python3 -c "import datetime;print((datetime.datetime.now()+datetime.timedelta(days=3)).strftime('%Y-%m-%dT14:30:00+08:00'))")
TDR=$(curl -s -m 10 -X POST "$B/api/v1/advisor/test-drive" -H "$S1H" -H "Content-Type: application/json" \
  -d "{\"customer_id\":$CID,\"scheduled_at\":\"$SCH\",\"model_name\":\"极石01\",\"location\":\"浦东店\",\"note\":\"uat试驾\"}")
TDID=$(echo "$TDR" | jget "['data']['ID']")
if [ -z "$TDID" ] || [ "$TDID" = "None" ]; then TDID=$(echo "$TDR" | jget "['data']['id']"); fi
check "test-drive: 创建返回 ID" y "$([ -n "$TDID" ] && [ "$TDID" != "None" ] && echo y || echo n)"
TDM=$($PSQL "SELECT model_name FROM test_drives WHERE id=$TDID" | tr -d '[:space:]')
check "test-drive: model_name 落库字节一致" "极石01" "$TDM"
TDS=$($PSQL "SELECT status FROM test_drives WHERE id=$TDID" | tr -d '[:space:]')
check "test-drive: 初始状态 pending" pending "$TDS"
TDE=$(curl -s -o /dev/null -m 10 -w "%{http_code}" -X POST "$B/api/v1/advisor/test-drive" -H "$S1H" -H "Content-Type: application/json" \
  -d "{\"customer_id\":$CID,\"scheduled_at\":\"not-a-time\"}")
check "test-drive: 非法时间→400" 400 "$TDE"
TDL=$(curl -s -m 10 "$B/api/v1/advisor/test-drives?customer_id=$CID" -H "$S1H")
TDLH=$(echo "$TDL" | python3 -c "
import sys,json
d=json.load(sys.stdin)['data']
print('y' if any(str(x.get('ID') or x.get('id'))=='$TDID' for x in d) else 'n')")
check "test-drives: 列表含新建单" y "$TDLH"
TDG=$(curl -s -m 10 "$B/api/v1/advisor/test-drive/$TDID" -H "$S1H")
TDGL=$(echo "$TDG" | strval "['data']['Location']" )
if [ "$TDGL" != "浦东店" ]; then TDGL=$(echo "$TDG" | strval "['data']['location']"); fi
check "test-drive: 详情 location 一致" "浦东店" "$TDGL"
TDU=$(curl -s -m 10 -X PUT "$B/api/v1/advisor/test-drive/$TDID" -H "$S1H" -H "Content-Type: application/json" -d '{"status":"completed","result":"已到店完成"}')
TDUS=$($PSQL "SELECT status FROM test_drives WHERE id=$TDID" | tr -d '[:space:]')
TDUR=$($PSQL "SELECT result FROM test_drives WHERE id=$TDID" | tr -d '[:space:]')
check "test-drive: 状态更新落库" completed "$TDUS"
check "test-drive: result 落库字节一致" "已到店完成" "$TDUR"
# 状态枚举校验（PLAN_FIX_2026-09-21 B3 已修：非法状态不再落库，此处由缺陷探测 INFO 转真断言）
TDB=$(curl -s -o /dev/null -m 10 -w "%{http_code}" -X PUT "$B/api/v1/advisor/test-drive/$TDID" -H "$S1H" -H "Content-Type: application/json" -d '{"status":"bogus_state"}')
check "test-drive: 非法状态被拒(400)" 400 "$TDB"
# 字节级护栏：拒绝后库内状态不得被脏值污染（仍是本轮写入的 completed）
TDBS=$($PSQL "SELECT status FROM test_drives WHERE id=$TDID" | tr -d '[:space:]')
check "test-drive: 非法状态零落库" completed "$TDBS"
$PSQL "UPDATE test_drives SET status='completed' WHERE id=$TDID" >/dev/null 2>&1

# ---------- 会话：接管 / 发送 / AI 开关 ----------
echo "---- 六、会话接管 / 人工发送 / AI 开关（字节级）----"
TK=$(curl -s -m 10 -X POST "$B/api/v1/advisor/chat/takeover" -H "$S1H" -H "Content-Type: application/json" -d "{\"conversation_id\":$CONV}")
check "takeover: 成功" 0 "$(echo "$TK" | jget "['code']")"
KM=$($PSQL "SELECT mode FROM conversations WHERE id=$CONV" | tr -d '[:space:]')
KL=$($PSQL "SELECT is_human_locked FROM conversations WHERE id=$CONV" | tr -d '[:space:]')
KP=$($PSQL "SELECT pending_handoff FROM conversations WHERE id=$CONV" | tr -d '[:space:]')
check "takeover: mode=human 落库" human "$KM"
check "takeover: is_human_locked=true 落库" t "$KL"
check "takeover: pending_handoff=false 落库" f "$KP"

MSG="这是一条UAT人工消息-字节校验-Abc123"
SR2=$(curl -s -m 15 -X POST "$B/api/v1/advisor/chat/send" -H "$S1H" -H "Content-Type: application/json" \
  -d "{\"conversation_id\":$CONV,\"customer_id\":$CID,\"content\":\"$MSG\"}")
check "send: 成功" 0 "$(echo "$SR2" | jget "['code']")"
MC=$($PSQL "SELECT content FROM messages WHERE conversation_id=$CONV AND sender_type='human' ORDER BY id DESC LIMIT 1" | tr -d '\r' | sed -e 's/[[:space:]]*$//')
check "send: content 落库字节一致" "$MSG" "$MC"
MS=$($PSQL "SELECT sender_id FROM messages WHERE conversation_id=$CONV AND sender_type='human' ORDER BY id DESC LIMIT 1" | tr -d '[:space:]')
check "send: sender_id=当前顾问($U1)" "$U1" "$MS"
MCE=$($PSQL "SELECT is_ai_reply_enabled FROM conversations WHERE id=$CONV" | tr -d '[:space:]')
check "send: 发消息后 AI 自动关闭" f "$MCE"

TG=$(curl -s -m 10 -X POST "$B/api/v1/advisor/chat/toggle-ai-reply" -H "$S1H" -H "Content-Type: application/json" -d "{\"conversation_id\":$CONV,\"enabled\":true}")
TE1=$($PSQL "SELECT is_ai_reply_enabled FROM conversations WHERE id=$CONV" | tr -d '[:space:]')
TM1=$($PSQL "SELECT mode FROM conversations WHERE id=$CONV" | tr -d '[:space:]')
check "toggle: enabled=true 落库" t "$TE1"
check "toggle: mode 随开关回 ai" ai "$TM1"
curl -s -m 10 -X POST "$B/api/v1/advisor/chat/toggle-ai-reply" -H "$S1H" -H "Content-Type: application/json" -d "{\"conversation_id\":$CONV,\"enabled\":false}" >/dev/null
TE2=$($PSQL "SELECT is_ai_reply_enabled FROM conversations WHERE id=$CONV" | tr -d '[:space:]')
TM2=$($PSQL "SELECT mode FROM conversations WHERE id=$CONV" | tr -d '[:space:]')
check "toggle: 往返关闭落库" f "$TE2"
check "toggle: mode 回 human" human "$TM2"

# ---------- 聊天历史 ----------
H=$(curl -s -m 10 "$B/api/v1/chat/history?customer_id=$CID&limit=50" -H "$S1H")
HH=$(echo "$H" | python3 -c "
import sys,json
d=json.load(sys.stdin)['data']
print('y' if any(m.get('content')=='$MSG' for m in d) else 'n')")
check "history: 含刚发的人工消息(字节一致)" y "$HH"
H403=$(curl -s -o /dev/null -m 10 -w "%{http_code}" "$B/api/v1/chat/history?customer_id=$CID&limit=50" -H "$S2H")
check "history: sales2 越权→403" 403 "$H403"

# ---------- 策略推荐 / 顾问列表 / 统计 ----------
echo "---- 七、策略推荐 / 顾问列表 / 工作台统计 ----"
SR3=$(curl -s -m 20 "$B/api/v1/advisor/strategy/recommend?customer_id=$CID&conversation_id=$CONV" -H "$S1H")
SK=$(echo "$SR3" | python3 -c "
import sys,json
d=json.load(sys.stdin)['data']
need=['customer_id','intent_score','urgency_level','route_result','recommends']
print('y' if all(k in d for k in need) else 'n')")
check "strategy/recommend: 5 键齐全" y "$SK"
SR4=$(curl -s -o /dev/null -m 10 -w "%{http_code}" "$B/api/v1/advisor/strategy/recommend" -H "$S1H")
check "strategy/recommend: 缺 customer_id→400" 400 "$SR4"
AL=$(curl -s -m 10 "$B/api/v1/advisor/list" -H "$S1H")
check "advisor/list: 200" 200 "$(curl -s -o /dev/null -m 10 -w "%{http_code}" "$B/api/v1/advisor/list" -H "$S1H")"
ST=$(curl -s -m 10 "$B/api/v1/advisor/stats" -H "$S1H")
STK=$(echo "$ST" | python3 -c "
import sys,json
d=json.load(sys.stdin)['data']
print('y' if (isinstance(d,list) and len(d)>=5 and all(('label' in x and 'value' in x) for x in d)) else 'n')")
check "advisor/stats: 多指标结构" y "$STK"

# ---------- 前后端契约：前端实际调用的其余端点 ----------
echo "---- 八、前端 Advisor 页依赖端点全遍历 ----"
p() { # p <名称> <期望码> <method> <url> <token-header> [body]
  local code
  if [ -n "$6" ]; then
    code=$(curl -s -o /dev/null -m 15 -w "%{http_code}" -X "$3" "$B$4" -H "$5" -H "Content-Type: application/json" -d "$6")
  else
    code=$(curl -s -o /dev/null -m 15 -w "%{http_code}" -X "$3" "$B$4" -H "$5")
  fi
  check "$1" "$2" "$code"
}
p "GET /api/v1/billing/my-package" 200 GET "/api/v1/billing/my-package" "$S1H"
p "GET /api/v1/advisor/tags" 200 GET "/api/v1/advisor/tags" "$S1H"
p "GET /api/v1/conversations/:id/messages" 200 GET "/api/v1/conversations/$CONV/messages" "$S1H"
p "POST /api/v1/feedback/rating" 200 POST "/api/v1/feedback/rating" "$S1H" "{\"customer_id\":$CID,\"rating\":5,\"comment\":\"uat\"}"
p "POST /api/v1/chat/transfer/ai" 200 POST "/api/v1/chat/transfer/ai" "$S1H" "{\"conversation_id\":$CONV}"
p "POST /api/v1/chat/clear-delay" 200 POST "/api/v1/chat/clear-delay" "$S1H" "{\"customer_id\":$CID}"
p "POST /api/v1/feedback" 200 POST "/api/v1/feedback" "$S1H" "{\"content\":\"uat建议\",\"target_type\":\"feature\"}"
p "GET /api/v1/chat/history(匿名无 key)" 403 GET "/api/v1/chat/history?customer_id=$CID" "X-None: 1"
p "PUT /api/v1/advisor/customer/:id/info(未登录)" 401 PUT "/api/v1/advisor/customer/$CID/info" "X-None: 1" '{"name":"x"}'

# ---------- 顾问手动触发 AI 回复 ----------
echo "---- 九、顾问手动触发 AI 回复（/advisor/chat/ai-reply）----"
curl -s -m 15 -X POST "$B/api/v1/chat/guest" -H "Content-Type: application/json" -H "X-Tenant-ID: 1" -d '{}' >/dev/null
AIR=$(curl -s -m 130 -X POST "$B/api/v1/advisor/chat/ai-reply" -H "$S1H" -H "Content-Type: application/json" \
  -d "{\"conversation_id\":$CONV,\"content\":\"这款车多少钱\"}")
AIC=$(echo "$AIR" | jget "['code']")
check "ai-reply: 返回成功(code=0)" 0 "$AIC"
AISENDER=$(echo "$AIR" | strval "['data']['SenderType']")
if [ "$AISENDER" != "ai" ]; then AISENDER=$(echo "$AIR" | strval "['data']['sender_type']"); fi
check "ai-reply: sender_type=ai" ai "$AISENDER"
AICONTENT=$(echo "$AIR" | strval "['data']['Content']")
if [ -z "$AICONTENT" ]; then AICONTENT=$(echo "$AIR" | strval "['data']['content']"); fi
check "ai-reply: 回复内容非空" y "$([ -n "$AICONTENT" ] && echo y || echo n)"
# 去 AI 味铁律：回复不得含「您」
HASPOLITE=$(echo "$AICONTENT" | python3 -c "import sys;print('y' if '您' in sys.stdin.read() else 'n')")
check "ai-reply: 回复无敬语「您」(去AI味铁律)" n "$HASPOLITE"
echo "  [样本] AI回复: ${AICONTENT:0:80}"

# ---------- 清理 ----------
$PSQL "DELETE FROM messages WHERE conversation_id=$CONV" >/dev/null 2>&1
$PSQL "DELETE FROM test_drives WHERE customer_id=$CID" >/dev/null 2>&1
$PSQL "DELETE FROM follow_ups WHERE customer_id=$CID" >/dev/null 2>&1
$PSQL "DELETE FROM customer_tags WHERE customer_id=$CID" >/dev/null 2>&1
$PSQL "DELETE FROM conversations WHERE id=$CONV" >/dev/null 2>&1
$PSQL "DELETE FROM customers WHERE id=$CID" >/dev/null 2>&1

echo ""
echo "=========================================================="
echo " 顾问工作台 UAT 结果: PASS=$PASS FAIL=$FAIL KNOWN缺陷=$KNOWN"
echo "=========================================================="
[ "$FAIL" -eq 0 ]
