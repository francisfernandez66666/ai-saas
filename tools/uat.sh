#!/bin/bash
# ============================================================
# 全场景全流程 UAT v3（2026-08-26）—— 功能 + 付费 + 邀请奖励充分测试
# 用法: ./tools/uat.sh 9090    （结束自动恢复全部开关）
# ============================================================
B="http://localhost:${1:-9090}"
PSQL="psql ${TEST_DB_URL:-postgresql://ai_scrm:dev123@localhost/ai_scrm} -tAc"
Q(){ psql ${TEST_DB_URL:-postgresql://ai_scrm:dev123@localhost/ai_scrm} -tAc "$1"; }
PASS=0; FAIL=0; FAILED_CASES=""

check(){ if [ "$2" = "$3" ]; then PASS=$((PASS+1)); echo "  PASS  $1";
  else FAIL=$((FAIL+1)); FAILED_CASES="$FAILED_CASES\n    ✗ $1 (期望=$2 实际=$3)"; echo "  FAIL  $1 (期望=$2 实际=$3)"; fi }
jget(){ python3 -c "
import sys,json
try:
    d=json.load(sys.stdin); print(eval(sys.argv[1],{'d':d}))
except Exception: print('')" "$1"; }
code(){ jget "d['code']"; }

# uat_login <登录请求体JSON> → 原样打出服务端响应（调用方自己 jget 取 token）
# 为什么要重试而不是直接 curl：/auth/login 挂 IPRateLimit("login", 20, 1min)，而 test_all 是顺序跑
# 十套脚本共用同一实例、同一出口 IP，前序冒烟的登录足以把这个桶打满。更要命的是这个窗口是
# 「首个请求 + TTL」而不是自然分钟，脚本在自己的窗口里连打 20 次就整窗被拒。
# 现场：uat 单跑 98/98 全绿，夹在 test_all 里 PASS=50 FAIL=48——第一条红就是「甲登录」（响应 429），
# token 空 → 邀请码 0 位 → 后续所有带 Bearer 的断言集体 401。被测的是登录之后的业务链，
# 把限流当成产品缺陷报会让 20 条下游断言一起变红且看不出根因，故等窗口过去再试。
# 4 次仍拒则原样返回最后一次响应，让下游断言如实报红（不掩盖真故障）。
uat_login(){ local body="$1" resp i
  for i in 1 2 3 4; do
    resp=$(curl -s -X POST "$B/api/v1/auth/login" -H "Content-Type: application/json" -d "$body")
    case "$resp" in
      *'"code":429'*) echo "    [retry] /auth/login 命中 IP 限流桶，等 61s 后重试（第 $i 次）" >&2; sleep 61 ;;
      *) break ;;
    esac
  done
  printf '%s' "$resp"
}

echo "== 0. 准备：清强改密标记 / 放开注册限流(内测兜底口径) / 关邮箱验证 =="
$PSQL "UPDATE tenant_users SET must_change_password=false WHERE username='admin'" >/dev/null 2>&1
ADMIN_TOKEN=$(uat_login '{"username":"admin","password":"admin123"}' | jget "d['data']['token']")
AH="Authorization: Bearer $ADMIN_TOKEN"
[ -n "$ADMIN_TOKEN" ] && check "超管登录" y y || check "超管登录" y n

# G-4 修复：trap EXIT 确保配置恢复——原手动恢复在脚本中断/失败时不执行，
# 邮箱验证/pay_mode/token双开关/注册限流等开关残留为脏状态影响运行中的服务。
# 先读取原值，EXIT 时统一恢复。
orign(){ curl -s "$B/api/v1/admin/config?category=$1" -H "$AH" 2>/dev/null | python3 -c "import sys,json;d=json.load(sys.stdin);print(next((x['value'] for x in d.get('data',[]) if x['key']=='$2'),'$3'))" 2>/dev/null || echo "$3"; }
OE=$(orign notify email_verify_enabled true); OPL=$(orign notify pay_mode '"mock"'); OTB=$(orign billing token_billing_enabled false); OBE=$(orign billing billing_enforced false); OIL=$(orign billing register_ip_daily_limit 3); OII=$(orign billing register_ip_min_interval_sec 60)
# 八节 Token 级联测试需强制走 AI 回复路径（否则真实 AI 低信任度会把会话路由到 pending_human，
# 关闭 AI 回复→后续轮次不产生扣减→级联断言随机失败）。捕获原阈值，trap 统一恢复。
OTT=$(orign strategy theta_trust 0.3); OTH=$(orign strategy theta_hook_rate_crit 0.2); OTL=$(orign strategy theta_l3_intent 0.8)
trap 'curl -s -X PUT "$B/api/v1/admin/config" -H "$AH" -H "Content-Type: application/json" -d "[{\"category\":\"notify\",\"key\":\"email_verify_enabled\",\"value\":\"$OE\"},{\"category\":\"notify\",\"key\":\"pay_mode\",\"value\":$OPL},{\"category\":\"billing\",\"key\":\"token_billing_enabled\",\"value\":\"$OTB\"},{\"category\":\"billing\",\"key\":\"billing_enforced\",\"value\":\"$OBE\"},{\"category\":\"billing\",\"key\":\"register_ip_daily_limit\",\"value\":\"$OIL\"},{\"category\":\"billing\",\"key\":\"register_ip_min_interval_sec\",\"value\":\"$OII\"},{\"category\":\"strategy\",\"key\":\"theta_trust\",\"value\":\"$OTT\"},{\"category\":\"strategy\",\"key\":\"theta_hook_rate_crit\",\"value\":\"$OTH\"},{\"category\":\"strategy\",\"key\":\"theta_l3_intent\",\"value\":\"$OTL\"}]" >/dev/null; echo "  [trap] 已恢复全部开关"' EXIT

curl -s -X PUT "$B/api/v1/admin/config" -H "$AH" -H "Content-Type: application/json" \
  -d '[{"category":"notify","key":"email_verify_enabled","value":"false"},{"category":"billing","key":"register_ip_daily_limit","value":"1000"},{"category":"billing","key":"register_ip_min_interval_sec","value":"0"}]' >/dev/null

TS=$(date +%s)
TS2=$((TS+7))
# 14.3 重复邮箱注册409
EM="dup$TS2@t.com"
curl -s -X POST "$B/api/v1/tenant/signup" -H "Content-Type: application/json" \
  -d "{\"company_name\":\"首注邮箱\",\"code\":\"dpa$((TS2%99999))\",\"username\":\"dp$TS2\",\"password\":\"uat123456\",\"admin_email\":\"$EM\"}" >/dev/null
sleep 1
DUP=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/tenant/signup" -H "Content-Type: application/json" \
  -d "{\"company_name\":\"重注\",\"code\":\"dpb$((TS2%99999))\",\"username\":\"dq$TS2\",\"password\":\"uat123456\",\"admin_email\":\"$EM\"}")
check "重复邮箱注册409" 409 "$DUP"
RUN=$TS
UA_CODE="uata$((TS%100000))"; UB_CODE="uatb$((TS%100000))"; UC_CODE="uatc$((TS%100000))"; UE_CODE="uate$((TS%100000))"; UF_CODE="uatf$((TS%100000))"
UA_USER="ua$TS"; UB_USER="ub$TS"; UC_USER="uc$TS"; UE_USER="ue$TS"; UF_USER="uf$TS"
# 清理历史 UAT 租户及从属（演示环境卫生；跨轮 LIKE 撞旧数据根除）
for TID in $($PSQL "SELECT id FROM tenants WHERE code LIKE 'uat%' ORDER BY id DESC LIMIT 30"); do
  $PSQL "DELETE FROM reward_claims WHERE tenant_id=$TID OR ref_id=$TID" >/dev/null 2>&1
  $PSQL "DELETE FROM api_keys WHERE tenant_id=$TID" >/dev/null 2>&1
  $PSQL "DELETE FROM billing_orders WHERE tenant_id=$TID" >/dev/null 2>&1
  $PSQL "DELETE FROM tenant_pack_bindings WHERE tenant_id=$TID" >/dev/null 2>&1
  $PSQL "DELETE FROM dept_pack_bindings WHERE tenant_id=$TID" >/dev/null 2>&1
  $PSQL "DELETE FROM kb_feedback_materials WHERE tenant_id=$TID" >/dev/null 2>&1
  $PSQL "DELETE FROM knowledge_fragments WHERE tenant_id=$TID AND category='企业知识'" >/dev/null 2>&1
  $PSQL "DELETE FROM tenant_users WHERE tenant_id=$TID" >/dev/null 2>&1
  $PSQL "DELETE FROM tenants WHERE id=$TID" >/dev/null 2>&1
done
echo "  历史 UAT 租户已清理(本轮后缀 RUN=$RUN)"
echo ""
echo "== 一、入驻与注册赠送（无ref也发桶）=="
UA_CODE="uata$((TS%100000))"
R=$(curl -s -X POST "$B/api/v1/tenant/signup" -H "Content-Type: application/json" \
  -d "{\"company_name\":\"UAT甲\",\"code\":\"$UA_CODE\",\"username\":\"ua$TS\",\"password\":\"uat123456\"}")
check "甲注册(code=0)" 0 "$(echo "$R"|code)"
UA_ID=$($PSQL "SELECT id FROM tenants WHERE code='$UA_CODE'")
UA_LOGIN=$(uat_login "{\"tenant_code\":\"$UA_CODE\",\"username\":\"ua$TS\",\"password\":\"uat123456\"}")
UA_TOKEN=$(echo "$UA_LOGIN" | jget "d['data']['token']")
[ -n "$UA_TOKEN" ] && R=y || R=n
check "甲登录" y "$R"
# 取不到 token 时把服务端原话打出来：历史上这一项在 test_all 顺序跑（十套脚本共用同一实例、
# 共享"改全局开关再恢复"）时空白失败，20 条下游断言跟着红却看不出是哪一步拒的登录。
# 单跑必绿、连跑偶发红的东西最难查，留下这一行比留下猜测有用。
[ -n "$UA_TOKEN" ] || echo "    （诊断）甲登录响应原文：$(echo "$UA_LOGIN" | head -c 300)"
check "③免费桶=30万" 300000 "$($PSQL "SELECT COALESCE(free_token_balance,0) FROM tenants WHERE id=$UA_ID")"
check "免费桶有效期已设" t "$($PSQL "SELECT free_token_expires_at IS NOT NULL FROM tenants WHERE id=$UA_ID")"
INV_A=$(curl -s "$B/api/v1/advisor/referral/info" -H "Authorization: Bearer $UA_TOKEN" | jget "d['data']['referral']['invite_code']")
check "邀请码生成(8位)" 8 "${#INV_A}"

echo ""
echo "== 二、邀请深度：带ref注册 / 双向奖励 / 首绑唯一 / 无效ref静默 =="
UB_CODE="uatb$((TS%100000))"
R=$(curl -s -X POST "$B/api/v1/tenant/signup" -H "Content-Type: application/json" \
  -d "{\"company_name\":\"UAT乙\",\"code\":\"$UB_CODE\",\"username\":\"ub$TS\",\"password\":\"uat123456\",\"ref\":\"$INV_A\"}")
check "乙带ref注册" 0 "$(echo "$R"|code)"
UB_ID=$($PSQL "SELECT id FROM tenants WHERE code='$UB_CODE'")
check "首绑关系(乙←甲)" t "$($PSQL "SELECT invited_by_tenant_id=$UA_ID FROM tenants WHERE id=$UB_ID")"
check "乙得30万" 300000 "$($PSQL "SELECT COALESCE(free_token_balance,0) FROM tenants WHERE id=$UB_ID")"
check "甲邀友奖+30万(累计60万)" 600000 "$($PSQL "SELECT COALESCE(free_token_balance,0) FROM tenants WHERE id=$UA_ID")"
UC_CODE="uatc$((TS%100000))"
curl -s -X POST "$B/api/v1/tenant/signup" -H "Content-Type: application/json" \
  -d "{\"company_name\":\"UAT丙\",\"code\":\"$UC_CODE\",\"username\":\"uc$TS\",\"password\":\"uat123456\",\"ref\":\"$INV_A\"}" >/dev/null
UC_ID=$($PSQL "SELECT id FROM tenants WHERE code='$UC_CODE'")
check "丙也绑定甲(多邀多得)" t "$($PSQL "SELECT invited_by_tenant_id=$UA_ID FROM tenants WHERE id=$UC_ID")"
UE_CODE="uate$((TS%100000))"
R=$(curl -s -X POST "$B/api/v1/tenant/signup" -H "Content-Type: application/json" \
  -d "{\"company_name\":\"UAT丁无效ref\",\"code\":\"$UE_CODE\",\"username\":\"ue$TS\",\"password\":\"uat123456\",\"ref\":\"NOTEXIST0\"}")
check "无效ref仍注册成功(静默)" 0 "$(echo "$R"|code)"
UE_ID=$($PSQL "SELECT id FROM tenants WHERE code='$UE_CODE'")
check "无效ref不产生绑定" "$($PSQL "SELECT COALESCE(invited_by_tenant_id,0) FROM tenants WHERE id=$UE_ID")" "0"

echo ""
echo "== 三、付费×邀请：paid订阅→邀请人永久token（多邀累计）=="
UB_TOKEN=$(uat_login "{\"tenant_code\":\"$UB_CODE\",\"username\":\"ub$TS\",\"password\":\"uat123456\"}" | jget "d['data']['token']")
BH="Authorization: Bearer $UB_TOKEN"
# 按 code 精确锁定 seed 包（此前 ORDER BY sort_order LIMIT 1 会被单测残留的 ut_* 包
# 抢占——它们 sort_order=0 且非确定性，导致月度额度断言在 300万/100万 间随机漂移）
PAID_PKG=$($PSQL "SELECT id FROM packages WHERE code='starter_1000' ORDER BY id LIMIT 1")
SUB_RAW=$(curl -s -X POST "$B/api/v1/billing/subscribe" -H "$BH" -H "Content-Type: application/json" \
  -d "{\"package_id\":$PAID_PKG}")
ORDER_B=$(echo "$SUB_RAW" | jget "d['data']['order']['id']")
[ -z "$ORDER_B" ] && echo "    [debug] subscribe原始: $(echo "$SUB_RAW" | head -c 200)"
PAY_RAW=$(curl -s -X POST "$B/api/v1/billing/orders/mock-pay" -H "$BH" -H "Content-Type: application/json" \
  -d "{\"order_id\":$ORDER_B}")
GR=$(echo "$PAY_RAW" | jget "d['data']['granted']")
[ "$GR" != "True" ] && echo "    [debug] 乙mock-pay原始: $(echo "$PAY_RAW" | head -c 200)"
check "乙paid订阅到账发放" True "$GR"
check "乙①月度额度=300万" 3000000 "$($PSQL "SELECT monthly_token_quota FROM tenants WHERE id=$UB_ID")"
A_BAL=$($PSQL "SELECT COALESCE(token_balance,0) FROM tenants WHERE id=$UA_ID")
check "甲得乙付费永久奖+50万" 500000 "$A_BAL"
# 丙也付费 → 甲再+50万（多邀累计）
UC_TOKEN=$(uat_login "{\"tenant_code\":\"$UC_CODE\",\"username\":\"uc$TS\",\"password\":\"uat123456\"}" | jget "d['data']['token']")
CH="Authorization: Bearer $UC_TOKEN"
ORDER_C=$(curl -s -X POST "$B/api/v1/billing/subscribe" -H "$CH" -H "Content-Type: application/json" \
  -d "{\"package_id\":$PAID_PKG}" | jget "d['data']['order']['id']")
[ -n "$ORDER_C" ] && R=y || R=n
check "丙下单" y "$R"
PAYC_RAW=$(curl -s -X POST "$B/api/v1/billing/orders/mock-pay" -H "$CH" -H "Content-Type: application/json" -d "{\"order_id\":$ORDER_C}")
GR_C=$(echo "$PAYC_RAW" | jget "d['data']['granted']")
[ "$GR_C" != "True" ] && echo "    [debug] 丙mock-pay原始: $(echo "$PAYC_RAW" | head -c 200)"
check "丙mock-pay发放" True "$GR_C"
A_BAL2=$($PSQL "SELECT COALESCE(token_balance,0) FROM tenants WHERE id=$UA_ID")
check "丙付费→甲再+50万(累计100万·多邀多得)" 1000000 "$A_BAL2"
O2=$(curl -s -X POST "$B/api/v1/billing/subscribe" -H "$BH" -H "Content-Type: application/json" -d "{\"package_id\":$PAID_PKG}" | jget "d['data']['order']['id']")
curl -s -X POST "$B/api/v1/billing/orders/mock-pay" -H "$BH" -H "Content-Type: application/json" -d "{\"order_id\":$O2}" >/dev/null
A_BAL3=$($PSQL "SELECT COALESCE(token_balance,0) FROM tenants WHERE id=$UA_ID")
check "同受邀重复付费不再发奖" 1000000 "$A_BAL3"

echo ""
echo "== 四、换绑撞库（防薅v2）=="
$PSQL "INSERT INTO reward_claims (grant_type,tenant_id,email,note) VALUES ('referral_paid',$UB_ID,'uat-swap@t.com','占位撞库样本')" >/dev/null
SW=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/auth/email/change" -H "$BH" -H "Content-Type: application/json" \
  -d '{"new_email":"uat-swap@t.com","code":"any","old_password":"uat123456"}')
# B4(2026-09-15)：换绑需旧密码二次确认，缺 old_password 会先被 binding 拦成 400；
# 带上正确旧密码方能走到"撞库邮箱"分支断言 409（本用例验证的是防薅不变量，非改密链）。
check "撞库邮箱换绑被拒(409)" 409 "$SW"

echo ""
echo "== 五、static_qr 人工确认链路 =="
# P1.5-UAT修复：平台键字符串值必须 JSON 引号编码（BatchUpdate 契约），否则静默跳过
curl -s -X PUT "$B/api/v1/admin/config" -H "$AH" -H "Content-Type: application/json" -d '[{"category":"notify","key":"pay_mode","value":"\"static_qr\""},{"category":"notify","key":"static_qr_image","value":"\"https://pay.uat.example.com/x.png\""}]' >/dev/null
INC_PKG=$($PSQL "SELECT id FROM packages WHERE code='booster_1000' ORDER BY id LIMIT 1")
R=$(curl -s -X POST "$B/api/v1/billing/orders" -H "$BH" -H "Content-Type: application/json" -d "{\"package_id\":$INC_PKG}")
ORD_SQ=$(echo "$R" | jget "d['data']['id']")
check "下单回收款码" True "$(echo "$R" | jget "bool(d['data'].get('qr_content'))")"
[ -z "$(echo "$R" | jget "d['data'].get('channel','')")" ] && echo "    [debug] 下单原始: $(echo "$R"|head -c 160)"
curl -s -X POST "$B/api/v1/billing/manual-confirm" -H "$BH" -H "Content-Type: application/json" -d "{\"order_id\":$ORD_SQ}" >/dev/null
PEND=$(curl -s "$B/api/v1/super/orders/pending" -H "$AH" | jget "len([o for o in d['data'] if o['id']==$ORD_SQ])")
check "进入待确认列表" 1 "$PEND"
TB0=$($PSQL "SELECT token_balance FROM tenants WHERE id=$UB_ID")
CONF_RAW=$(curl -s -X POST "$B/api/v1/super/orders/$ORD_SQ/confirm" -H "$AH")
CST=$(echo "$CONF_RAW" | jget "d['data']['status']")
[ "$CST" != "paid" ] && echo "    [debug] confirm原始: $(echo "$CONF_RAW" | head -c 200)"
check "超管确认→状态paid" paid "$CST"
TB1=$($PSQL "SELECT token_balance FROM tenants WHERE id=$UB_ID")
check "increment +300万入②桶" "$((TB0+3000000))" "$TB1"
CST2=$(curl -s -X POST "$B/api/v1/super/orders/$ORD_SQ/confirm" -H "$AH" | jget "d['data']['status']")
check "重复确认幂等(仍paid)" paid "$CST2"
check "幂等不二次入账" "$TB1" "$($PSQL "SELECT token_balance FROM tenants WHERE id=$UB_ID")"

echo ""
echo "== 六、sdk拒绝 与 生产保护 =="
curl -s -X PUT "$B/api/v1/admin/config" -H "$AH" -H "Content-Type: application/json" -d '[{"category":"notify","key":"pay_mode","value":"\"sdk\""}]' >/dev/null
SDKMSG=$(curl -s -X POST "$B/api/v1/billing/orders" -H "$BH" -H "Content-Type: application/json" -d "{\"package_id\":$INC_PKG}" | jget "str(d.get('message',''))")
case "$SDKMSG" in *尚未开通*|*sdk*) check "sdk模式明确报错" y y;; *) check "sdk模式明确报错(msg=$SDKMSG)" y n;; esac
curl -s -X PUT "$B/api/v1/admin/config" -H "$AH" -H "Content-Type: application/json" -d '[{"category":"notify","key":"pay_mode","value":"\"mock\""}]' >/dev/null
HTTP=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/billing/orders/mock-pay" -H "$BH" -H "Content-Type: application/json" -d '{"order_id":999999}')
case "$HTTP" in 400|404) check "mock-pay不存在单保护" y y;; *) check "mock-pay不存在单保护" y "$HTTP";; esac

echo ""
echo "== 七、订单超时15分钟自动关闭 =="
# Q2 修复(2026-09-12)：固定 order_no='BO_UAT_T2' 重跑必撞唯一索引 idx_billing_orders_order_no
# （上轮的同名单已 closed 仍在库）→ 改随机后缀 + 预清理历史 BO_UAT_T2% 残留，保证幂等可重复跑
STALE_NO="BO_UAT_T2_$$_$RANDOM"
$PSQL "DELETE FROM billing_orders WHERE order_no LIKE 'BO_UAT_T2%'" >/dev/null
$PSQL "INSERT INTO billing_orders (order_no,tenant_id,amount_cents,channel,status,created_at) VALUES ('${STALE_NO}',${UB_ID},9900,'mock','pending',NOW()-INTERVAL '20 minutes')" >/dev/null
for _ in 1 2 3; do pkill -x ai-scrm 2>/dev/null; sleep 2; done; pgrep -x ai-scrm >/dev/null && kill -9 $(pgrep -x ai-scrm) 2>/dev/null
python3 - << 'PY'
import subprocess
subprocess.Popen(["./ai-scrm"], stdin=subprocess.DEVNULL,
    stdout=open("ai-scrm.log","ab"), stderr=subprocess.STDOUT, start_new_session=True)
PY
for i in $(seq 1 30); do sleep 2; c=$(curl -s -o /dev/null -w "%{http_code}" --max-time 3 "$B/health" 2>/dev/null); [ "$c" = "200" ] && break; done
$PSQL "UPDATE tenant_users SET must_change_password=false WHERE tenant_id IN (SELECT id FROM tenants WHERE code LIKE 'uat%')" >/dev/null
# UAT修复(2026-08-31)：重启后 seed 会把仍用出厂弱密码的 admin 重标 must_change_password=true
# （M3 首登强改密），导致第八节用重启前旧 token 改开关被 MustChangePasswordGuard 拦成 403，
# token_billing 双开关从未生效 → 三桶扣减全绿不扣。重启后补清默认租户 admin 标记并刷新超管 token。
$PSQL "UPDATE tenant_users SET must_change_password=false WHERE username='admin'" >/dev/null 2>&1
ADMIN_TOKEN=$(uat_login '{"username":"admin","password":"admin123"}' | jget "d['data']['token']")
AH="Authorization: Bearer $ADMIN_TOKEN"
STALE=$($PSQL "SELECT status FROM billing_orders WHERE order_no='${STALE_NO}'"); check "僵尸单已写入待小时巡检ExpireCheck关闭" closed "$STALE"

echo ""
echo "== 八、Token三桶强制扣减级联 =="
curl -s -X PUT "$B/api/v1/admin/config" -H "$AH" -H "Content-Type: application/json" \
  -d '[{"category":"billing","key":"token_billing_enabled","value":"true"},{"category":"billing","key":"billing_enforced","value":"true"}]' >/dev/null
# 强制 AI 回复路径：信任/接钩率/L3 三项转人工判据全部关闭（theta_trust=0 恒不触发信任转人工，
# theta_hook_rate_crit=0 恒不触发接钩率转人工，theta_l3_intent=2.0 高于任何意向分）。
# 目的是隔离"扣减引擎"行为，避免真实 AI 路由把会话切到 pending_human 后不再扣费（非计费 bug）。
curl -s -X PUT "$B/api/v1/admin/config" -H "$AH" -H "Content-Type: application/json" \
  -d '[{"category":"strategy","key":"theta_trust","value":"0"},{"category":"strategy","key":"theta_hook_rate_crit","value":"0"},{"category":"strategy","key":"theta_l3_intent","value":"2.0"}]' >/dev/null
sleep 1
$PSQL "UPDATE tenants SET free_token_balance=6000, free_token_expires_at=NOW()+INTERVAL '5 days', monthly_token_quota=80000, monthly_token_used=0, token_balance=25000 WHERE id=$UB_ID" >/dev/null
CUST_RAW=$(curl -s -X POST "$B/api/v1/customers" -H "$BH" -H "Content-Type: application/json" -d '{"name":"UAT乙客户"}')
CUST_B=$(echo "$CUST_RAW" | jget "d['data']['id']")
[ -z "$CUST_B" ] && echo "    [debug] customers原始: $(echo "$CUST_RAW" | head -c 160)"
[ -n "$CUST_B" ] && R=y || R=n
check "C端建联" y "$R"
# C7 租户盖章回归护栏(2026-09-15)：配额分支建客必须落到本租户（tenant_id=$UB_ID），
# 绝不能因事务丢 context 而盖成 tenant_id=0（跨租户泄露 + 本租户 chat 反查不到自己客户）。
CUST_TID=$($PSQL "SELECT tenant_id FROM customers WHERE id=$CUST_B")
check "建客租户归属正确(非tenant_id=0)" "$UB_ID" "$CUST_TID"
round(){ curl -s --max-time 170 -X POST "$B/api/v1/chat" -H "$BH" -H "Content-Type: application/json" -d "{\"customer_id\":$CUST_B,\"content\":\"$1\"}" >/dev/null; sleep 15; }
round "极石01空间大吗"
F1=$($PSQL "SELECT free_token_balance FROM tenants WHERE id=$UB_ID"); M1=$($PSQL "SELECT monthly_token_used FROM tenants WHERE id=$UB_ID"); B1=$($PSQL "SELECT token_balance FROM tenants WHERE id=$UB_ID")
[ "$F1" -lt 6000 ] && [ "$F1" -ge 1000 ] && [ "$M1" -eq 0 ] && [ "$B1" -eq 25000 ] && R=y || R=n
check "第一轮仅扣③(6000>$F1≥1000,①②未动)" y "$R"
$PSQL "UPDATE tenants SET free_token_expires_at=NOW()-INTERVAL '1 hour' WHERE id=$UB_ID" >/dev/null
round "内饰配置怎么样"
M2=$($PSQL "SELECT monthly_token_used FROM tenants WHERE id=$UB_ID"); F2=$($PSQL "SELECT free_token_balance FROM tenants WHERE id=$UB_ID")
[ "$M2" -gt 0 ] && [ "$F2" -eq 0 ] && R=y || R=n
check "③过期→扣①且过期清零" y "$R"
round "安全配置给我讲讲"
B3=$($PSQL "SELECT token_balance FROM tenants WHERE id=$UB_ID")
[ "$B3" -eq 25000 ] && R=y || R=n
check "①余量充足不动②" y "$R"
$PSQL "UPDATE tenants SET monthly_token_used=monthly_token_quota WHERE id=$UB_ID" >/dev/null
round "智能座舱说说"
B4=$($PSQL "SELECT token_balance FROM tenants WHERE id=$UB_ID")
[ "$B4" -lt 25000 ] && [ "$B4" -ge 15000 ] && R=y || R=n
check "①耗尽→扣②余额" y "$R"
$PSQL "UPDATE tenants SET token_balance=0 WHERE id=$UB_ID" >/dev/null
round "全空降级测试"
grep "三桶余额不足" ai-scrm.log | tail -1 | grep -q "降级规则话术" && R=y || R=n
check "全空→降级规则话术" y "$R"
# M1(2026-09-22 批三)字节级护栏：三桶全空时旧实现"挂账行已 DELETE、扣减却没发生"=静默吞账。
# 现口径是哨兵错误回滚整笔结算，欠账必须以挂账行形态**留在自己租户名下**。
# 两条断言各守一侧：① 余额不得被扣成负数（负账与吞账同样是账目失真）；
# ② 后台补写的挂账行必须带 tenant_id（C7 事务内写租户表红线：漏盖章＝跨租户可见）。
NEG_OK=$($PSQL "SELECT (free_token_balance>=0 AND token_balance>=0 AND monthly_token_used<=monthly_token_quota)::text FROM tenants WHERE id=$UB_ID" | tr -d '[:space:]')
check "全空后三桶未被扣成负数/超用(M1账目守恒)" true "$NEG_OK"
DEBT_UNSTAMPED=$($PSQL "SELECT count(*) FROM usage_flush_retry WHERE tenant_id=0 OR tenant_id IS NULL" | tr -d '[:space:]')
check "挂账行无tenant_id=0漏盖章(C7红线)" 0 "$DEBT_UNSTAMPED"
# A1(2026-09-22 批四)双入口对齐的第二条腿：人工锁定态裁决已收进 chatflow.HumanTakeoverDecide，
# smoke_chat_identity §8.10 证的是免登录链(/chat/unauthorized)，本段在**正式链(/chat, 登录态)**上
# 复现同一条"判定即落库"断言——两条链必须同判，否则 reply_attributions 同样本不可比（批五择臂会在噪声上学）。
# 此刻三桶全空，重开 AI 后只会走降级话术，不产生真实模型调用（零 token 成本），断言只查库。
CONV_B=$($PSQL "SELECT id FROM conversations WHERE tenant_id=$UB_ID AND customer_id=$CUST_B AND status='active' ORDER BY id DESC LIMIT 1" | tr -d '[:space:]')
if [ -n "$CONV_B" ]; then
  $PSQL "UPDATE conversations SET mode='human', is_human_locked=true, is_ai_reply_enabled=false,
         pending_handoff=true, last_human_reply_at=now()-interval '400 seconds' WHERE id=$CONV_B" >/dev/null
  round "A1 正式链超时重开断言"
  A1_MAIN=$(Q "SELECT (mode='ai' AND is_human_locked=false AND is_ai_reply_enabled=true AND pending_handoff=false)::text FROM conversations WHERE id=$CONV_B" | tr -d '[:space:]')
  check "正式链锁定超时同样落库解除(A1双入口)" true "$A1_MAIN"
  $PSQL "UPDATE conversations SET mode='ai', is_human_locked=false, is_ai_reply_enabled=true,
         pending_handoff=false WHERE id=$CONV_B" >/dev/null
else
  check "乙租户活跃会话存在(A1正式链前置)" y n
fi

echo ""
echo "== 九、对话分支：留资合并 + 快捷通道 =="
# D1 回归护栏(2026-09-16B，见 AUDIT_UAT_VERIFY_2026-09-16B)：原断言只 grep"留资检测"日志串——
# 失败路径(customers 表被塞 conversations 专有列 pending_handoff → SQLSTATE 42703 整行 UPDATE
# 中止且 error 未检查)照样打"留资成功"日志，76/76 全绿下真实丢数据。乙租户 signup 只有
# tenant_admin 无 sales 用户，恰是 bug 触发态（=入驻默认态），故本用例即无销售变体覆盖。
# 改为字节级 DB 断言：手机号/阶段/接管列必须真实落库。
$PSQL "UPDATE customers SET phone=NULL, journey_stage='ai_connected', assigned_user_id=0 WHERE id=$CUST_B" >/dev/null
curl -s --max-time 170 -X POST "$B/api/v1/chat" -H "$BH" -H "Content-Type: application/json" \
  -d "{\"customer_id\":$CUST_B,\"content\":\"我叫赵铁柱，手机13912345678，明天想去店里看看\"}" >/dev/null
LP=""
for _ in 1 2 3 4 5; do
  LP=$($PSQL "SELECT COALESCE(phone,'') FROM customers WHERE id=$CUST_B" | tr -d '[:space:]')
  [ "$LP" = "13912345678" ] && break
  sleep 1
done
check "留资手机号真实落库-无销售租户(D1护栏)" 13912345678 "$LP"
check "留资阶段推进lead_captured" lead_captured "$($PSQL "SELECT journey_stage FROM customers WHERE id=$CUST_B" | tr -d '[:space:]')"
check "待接管落会话列(非customers表)" t "$($PSQL "SELECT pending_handoff FROM conversations WHERE customer_id=$CUST_B AND status='active' ORDER BY updated_at DESC LIMIT 1" | tr -d '[:space:]')"
check "留资线索FollowUp落库" True "$([ "$($PSQL "SELECT COUNT(*) FROM follow_ups WHERE customer_id=$CUST_B AND result='lead_captured'" | tr -d '[:space:]')" -gt 0 ] && echo True || echo False)"
QRAW=$(curl -s --max-time 60 -w "\nUAT_HTTP=%{http_code}" -X POST "$B/api/v1/chat" -H "$BH" -H "Content-Type: application/json" \
  -d "{\"customer_id\":$CUST_B,\"content\":\"好的\"}")
CODE=$(echo "$QRAW" | grep -oE "UAT_HTTP=[0-9]+" | cut -d= -f2)
[ "$CODE" != "200" ] && echo "    [debug] 快捷原始: $(echo "$QRAW" | head -c 220)"
check "简单消息快捷通道200" 200 "$CODE"

echo ""
IND=$($PSQL "SELECT id FROM industry_packs WHERE code='auto' AND status='active' ORDER BY id DESC LIMIT 1")
ENT=$($PSQL "SELECT id FROM industry_packs WHERE code='auto_rox' AND status='active' ORDER BY id DESC LIMIT 1")
curl -s -X POST "$B/api/v1/admin/packs/bind" -H "$BH" -H "Content-Type: application/json" \
  -d "{\"industry_pack_id\":$IND,\"enterprise_pack_id\":$ENT}" >/dev/null
echo "== 十、KB双层 / 行业包视图 / 素材池 抽样 =="
check "KB上传" 0 "$(curl -s -X POST "$B/api/v1/admin/kb/upload" -H "$BH" -H "Content-Type: application/json" -d '{"title":"UAT知识","content":"极石01支持对外放电3.3千瓦。"}' | code)"
check "行业包当前绑定(bound)" True "$(curl -s "$B/api/v1/admin/packs/current" -H "$BH" | jget "d['data']['bound']")"
# D9 包效果归因闭环断言：绑定 auto_rox 后走测试通道触发策略回复，端点和底表都应能看到该包。
$PSQL "UPDATE tenants SET free_token_balance=100000, free_token_expires_at=NOW()+INTERVAL '5 days', monthly_token_used=0, token_balance=100000 WHERE id=$UB_ID" >/dev/null
ATTR_VK=$($PSQL "SELECT visitor_key FROM customers WHERE id=$CUST_B" | tr -d '[:space:]')
ATTR_HTTP=$(curl -s --max-time 170 -o /tmp/uat_pack_attr.json -w "%{http_code}" -X POST "$B/api/v1/chat/test?visitor_key=$ATTR_VK" -H "X-Tenant-ID: $UB_ID" -H "Content-Type: application/json" -d "{\"customer_id\":$CUST_B,\"content\":\"极石01后备箱容量多大\"}")
[ "$ATTR_HTTP" != "200" ] && echo "    [debug] D9归因测试原始: $(cat /tmp/uat_pack_attr.json 2>/dev/null | head -c 220)"
ATTR_ROWS=0
for _ in 1 2 3 4 5 6 7 8 9 10; do
  ATTR_ROWS=$($PSQL "SELECT COUNT(*) FROM reply_attributions WHERE tenant_id=$UB_ID AND pack_code='auto_rox'" | tr -d '[:space:]')
  [ "${ATTR_ROWS:-0}" -gt 0 ] && break
  sleep 1
done
check "D9归因行落库(reply_attributions)" True "$([ "${ATTR_ROWS:-0}" -gt 0 ] && echo True || echo False)"
check "D9包效果端点含pack_code字段" y "$(curl -s "$B/api/v1/admin/packs/stats?days=30&pack_code=auto_rox" -H "$BH" | grep -q auto_rox && echo y || echo n)"
$PSQL "UPDATE tenant_users SET must_change_password=false WHERE username='admin'" >/dev/null 2>&1
check "素材池列表可达" 0 "$(curl -s "$B/api/v1/super/materials?page=1" -H "$AH" | code)"

echo ""
echo "== 十一、OpenAPI 三态抽样 =="
AK=$(curl -s -X POST "$B/api/v1/admin/apikeys" -H "$BH" -H "Content-Type: application/json" -d '{"name":"uat-key","perms":["customer.read"]}' | jget "d['data']['key']")
[ -n "$AK" ] && R=y || R=n
check "签发明文返回" y "$R"
check "有效Key 200" 200 "$(curl -s -o /dev/null -w "%{http_code}" "$B/openapi/v1/customers" -H "Authorization: Bearer $AK")"
check "无Key 401" 401 "$(curl -s -o /dev/null -w "%{http_code}" "$B/openapi/v1/customers")"

echo ""
echo "== 十二、账号注销（次日生效+APIKey禁用+数据保留）=="
UF_CODE="uatf$((TS%100000))"
curl -s -X POST "$B/api/v1/tenant/signup" -H "Content-Type: application/json" \
  -d "{\"company_name\":\"UAT戊注销户\",\"code\":\"$UF_CODE\",\"username\":\"uf$TS\",\"password\":\"uat123456\"}" >/dev/null
UF_ID=$($PSQL "SELECT id FROM tenants WHERE code='$UF_CODE'")
UF_TOKEN=$(uat_login "{\"tenant_code\":\"$UF_CODE\",\"username\":\"uf$TS\",\"password\":\"uat123456\"}" | jget "d['data']['token']")
FH="Authorization: Bearer $UF_TOKEN"
AK2=$(curl -s -X POST "$B/api/v1/admin/apikeys" -H "$FH" -H "Content-Type: application/json" -d '{"name":"f-key","perms":["all"]}' | jget "d['data']['key']")
[ -n "$AK2" ] && R=y || R=n
check "戊签发APIKey" y "$R"
CR=$(curl -s -X POST "$B/api/v1/admin/account/cancel" -H "$FH" -H "Content-Type: application/json" -d '{"password":"uat123456"}')
check "注销受理(密码确认)" 0 "$(echo "$CR"|code)"
KEYOFF=$($PSQL "SELECT bool_and(NOT COALESCE(is_active,true)) FROM api_keys WHERE tenant_id=${UF_ID}" | tr -d '[:space:]')
echo "    [debug] keyoff=$KEYOFF"
check "APIKey已同步禁用(bool_and=t)" t "${KEYOFF:-f}"
LG=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/auth/login" -H "Content-Type: application/json" \
  -d "{\"tenant_code\":\"$UF_CODE\",\"username\":\"uf$TS\",\"password\":\"uat123456\"}")
check "当日仍可登录" 200 "$LG"
$PSQL "UPDATE tenants SET cancel_at=NOW()-INTERVAL '1 day' WHERE id=$UF_ID" >/dev/null
LG2=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/auth/login" -H "Content-Type: application/json" \
  -d "{\"tenant_code\":\"$UF_CODE\",\"username\":\"uf$TS\",\"password\":\"uat123456\"}")
check "次日登录403" 403 "$LG2"
sleep 31  # 越过租户解析正缓存TTL(30s)：SQL直改cancel_at后缓存仍持旧值
CHT=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/chat/test" -H "Content-Type: application/json" -H "X-Tenant-ID: $UF_ID" -d '{"content":"hi"}')
check "C端访问403" 403 "$CHT"
ROWEXISTS=$($PSQL "SELECT COUNT(*) FROM tenants WHERE id=${UF_ID}" | tr -d '[:space:]')
check "数据保留(tenants行仍在)" 1 "$ROWEXISTS"
AKOFF=$(curl -s -o /dev/null -w "%{http_code}" "$B/openapi/v1/customers" -H "Authorization: Bearer $AK2")
check "注销后OpenAPI Key失效(401)" 401 "$AKOFF"

echo ""
echo "== 十三、二维码内容类型 =="
CT=$(curl -s -o /tmp/uat_qr.png -w "%{content_type}" "$B/api/v1/advisor/referral/qrcode" -H "$BH")
check "后端渲染PNG" image/png "$CT"

echo ""
echo "== 十四、UAT定稿三项+邀请记录（2026-08-26）=="
# 14.1 弱密码拒绝
R=$(curl -s -X POST "$B/api/v1/tenant/signup" -H "Content-Type: application/json" \
  -d "{\"company_name\":\"弱密户\",\"code\":\"weak$((TS2%99999))\",\"username\":\"wk$TS2\",\"password\":\"abc123\"}" | jget "d['message']")
sleep 1
case "$R" in *至少8位*字母*) check "弱密码拒绝(提示含强度要求)" y y;; *) check "弱密码拒绝(msg=$R)" y n;; esac
# 14.2 未知行业兜底general / 已知行业保留
C_G="uindg$((TS2%99999))"
curl -s -X POST "$B/api/v1/tenant/signup" -H "Content-Type: application/json" \
  -d "{\"company_name\":\"未知行业\",\"code\":\"$C_G\",\"username\":\"ug$TS2\",\"password\":\"uat123456\",\"industry\":\"metaverse\"}" >/dev/null
sleep 1
G_IND=$($PSQL "SELECT industry FROM tenants WHERE code='$C_G'")
check "未知行业回落general" general "$G_IND"
Q "INSERT INTO industry_packs (code,name,industry,version,pack_level,status,file_path) VALUES ('education','教育行业包','education','1.0.0','industry','active','n/a') ON CONFLICT DO NOTHING" >/dev/null
C_E="uinde$((TS2%99999))"
curl -s -X POST "$B/api/v1/tenant/signup" -H "Content-Type: application/json" \
  -d "{\"company_name\":\"已知行业\",\"code\":\"$C_E\",\"username\":\"ue$TS2\",\"password\":\"uat123456\",\"industry\":\"education\"}" >/dev/null
sleep 1
E_IND=$($PSQL "SELECT industry FROM tenants WHERE code='$C_E'")
check "已知行业保留不回落" education "$E_IND"
# 14.4 邀请记录接口（甲=既有受邀链顶层租户）
A_ID2=$($PSQL "SELECT id FROM tenants WHERE code LIKE 'uata%' ORDER BY id DESC LIMIT 1")
ATOK2=$(uat_login "{\"tenant_code\":\"$($PSQL "SELECT code FROM tenants WHERE id=$A_ID2")\",\"username\":\"$($PSQL "SELECT username FROM tenant_users WHERE tenant_id=$A_ID2 AND role='tenant_admin' LIMIT 1")\",\"password\":\"uat123456\"}" | jget "d['data']['token']")
REC=$(curl -s "$B/api/v1/advisor/referral/records" -H "Authorization: Bearer $ATOK2")
check "邀请记录接口可达且含记录" True "$(echo "$REC" | jget "len(d['data']['list'])>0")"
KEYS_OK=$(echo "$REC" | python3 -c '
import sys,json
try:
    d=json.load(sys.stdin)["data"]["list"][0]
    print("True" if all(k in d for k in ("email","paid_rewarded","signup_reward","invited_ok","paid_ok")) else "False")
except Exception:
    print("False")')
[ "$KEYS_OK" != "True" ] && echo "    [debug] records原始: $(echo "$REC" | head -c 220)"
check "记录含邮箱/支付/奖励字段" True "$KEYS_OK"

echo ""
echo "== 十五、退款 E2E（T3：零消耗增量全额退 + 包月按窗口摘除）=="
RF_CODE="uatrf$((TS%100000))"; RF_USER="rf$TS"
R=$(curl -s -X POST "$B/api/v1/tenant/signup" -H "Content-Type: application/json" \
  -d "{\"company_name\":\"UAT退款\",\"code\":\"$RF_CODE\",\"username\":\"$RF_USER\",\"password\":\"uat123456\"}")
check "退款租户注册" 0 "$(echo "$R"|code)"
RF_ID=$($PSQL "SELECT id FROM tenants WHERE code='$RF_CODE'")
RF_TOKEN=$(uat_login "{\"tenant_code\":\"$RF_CODE\",\"username\":\"$RF_USER\",\"password\":\"uat123456\"}" | jget "d['data']['token']")
RH="Authorization: Bearer $RF_TOKEN"
check "退款租户登录" y "$([ -n "$RF_TOKEN" ] && echo y || echo n)"
# 15.1 零消耗 increment：mock-pay → ②桶 +300万 → 全额退款 → 桶回收 → 重复退 409
RF_TB0=$($PSQL "SELECT COALESCE(token_balance,0) FROM tenants WHERE id=$RF_ID" | tr -d '[:space:]')
RF_ORDER=$(curl -s -X POST "$B/api/v1/billing/orders" -H "$RH" -H "Content-Type: application/json" \
  -d "{\"package_id\":$INC_PKG}" | jget "d['data']['id']")
curl -s -X POST "$B/api/v1/billing/orders/mock-pay" -H "$RH" -H "Content-Type: application/json" \
  -d "{\"order_id\":$RF_ORDER}" >/dev/null
RF_TB1=$($PSQL "SELECT COALESCE(token_balance,0) FROM tenants WHERE id=$RF_ID" | tr -d '[:space:]')
check "增量包到账后②桶+300万" "$((RF_TB0+3000000))" "$RF_TB1"
RF_AMT=$($PSQL "SELECT COALESCE(amount_cents,0) FROM billing_orders WHERE id=$RF_ORDER" | tr -d '[:space:]')
RF_REFUND_RAW=$(curl -s -X POST "$B/api/v1/billing/orders/$RF_ORDER/refund" -H "$RH")
RF_REFUND_CODE=$(echo "$RF_REFUND_RAW" | code)
RF_REFUND_AMT=$(echo "$RF_REFUND_RAW" | jget "d['data']['refund_amount_cents']")
check "零消耗增量包全额退款(code=0)" 0 "$RF_REFUND_CODE"
check "零消耗增量包退款金额=支付金额" "$RF_AMT" "$RF_REFUND_AMT"
RF_TB2=$($PSQL "SELECT COALESCE(token_balance,0) FROM tenants WHERE id=$RF_ID" | tr -d '[:space:]')
check "退款后②桶回收(无消耗则归零到到账前)" "$RF_TB0" "$RF_TB2"
RF_DUP_HTTP=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/billing/orders/$RF_ORDER/refund" -H "$RH")
check "重复退款409" 409 "$RF_DUP_HTTP"
# 15.2 包月零消耗：订阅+到账 → 月配额生效 → 按本单窗口全额退 → 到期日回退到当前、配额清零
RF_SUB_RAW=$(curl -s -X POST "$B/api/v1/billing/subscribe" -H "$RH" -H "Content-Type: application/json" \
  -d "{\"package_id\":$PAID_PKG}")
RF_SUB_ORDER=$(echo "$RF_SUB_RAW" | jget "d['data']['order']['id']")
check "包月订阅创建订单" y "$([ -n "$RF_SUB_ORDER" ] && echo y || echo n)"
RF_PAY_RAW=$(curl -s -X POST "$B/api/v1/billing/orders/mock-pay" -H "$RH" -H "Content-Type: application/json" \
  -d "{\"order_id\":$RF_SUB_ORDER}")
check "包月mock-pay到账" True "$(echo "$RF_PAY_RAW" | jget "d['data']['granted']")"
RF_QUOTA=$($PSQL "SELECT COALESCE(monthly_token_quota,0) FROM tenants WHERE id=$RF_ID" | tr -d '[:space:]')
check "包月月度配额已生效" 3000000 "$RF_QUOTA"
RF_REFUND_SUB_RAW=$(curl -s -X POST "$B/api/v1/billing/orders/$RF_SUB_ORDER/refund" -H "$RH")
RF_REFUND_SUB_CODE=$(echo "$RF_REFUND_SUB_RAW" | code)
RF_REFUND_SUB_AMT=$(echo "$RF_REFUND_SUB_RAW" | jget "d['data']['refund_amount_cents']")
RF_SUB_AMT=$($PSQL "SELECT COALESCE(amount_cents,0) FROM billing_orders WHERE id=$RF_SUB_ORDER" | tr -d '[:space:]')
check "零消耗包月全额退款" 0 "$RF_REFUND_SUB_CODE"
check "包月退款金额=支付金额" "$RF_SUB_AMT" "$RF_REFUND_SUB_AMT"
RF_AFTER=$($PSQL "SELECT monthly_token_quota, COALESCE(EXTRACT(EPOCH FROM (NOW()-expired_at)) < 120, false) FROM tenants WHERE id=$RF_ID" | tr -d '[:space:]')
check "退款后到期日回退且月配额清零" "0|t" "$RF_AFTER"
# 15.3 F1 回归(2026-09-15)：订单列表接口必须带回退款字段（修复前 Select 白名单漏列恒为 0/null）
RF_LIST_RAW=$(curl -s "$B/api/v1/billing/orders?limit=50" -H "$RH")
RF_LIST_REFUND=$(echo "$RF_LIST_RAW" | jget "sum(1 for o in d['data'] if o['id']==$RF_ORDER and o.get('refund_amount_cents')==$RF_AMT and o.get('refunded_at'))")
check "订单列表含退款金额与退款时间(F1)" 1 "$RF_LIST_REFUND"

echo ""
echo "== 十一、会话单活跃收口批（2026-09-16C，AUDIT_GAP_REALITY G1）=="
# 11.1 013 部分唯一索引存在（每租户+客户至多一条 active 会话的 DB 级不变式）
IDX13=$($PSQL "SELECT count(*) FROM pg_indexes WHERE indexname='ux_conv_one_active'")
check "013唯一索引已建(ux_conv_one_active)" 1 "$IDX13"
# 11.2 C端访客双次 welcome：必须复用同一会话（EnsureActiveConversation 收口前，
#      多实例/双路径各建一条 active 是 OneID 事故根因）
GC_RAW=$(curl -s -X POST "$B/api/v1/chat/guest" -H "Content-Type: application/json" \
  -H "X-Tenant-ID: $UA_ID" -d '{}')
GC_ID=$(echo "$GC_RAW" | jget "d['data']['customer_id']")
GC_VK=$(echo "$GC_RAW" | jget "d['data']['visitor_key']")
check "访客建联(11)" y "$([ -n "$GC_ID" ] && echo y || echo n)"
CV1=$(curl -s -X POST "$B/api/v1/chat/welcome?visitor_key=$GC_VK" -H "Content-Type: application/json" \
  -H "X-Tenant-ID: $UA_ID" -d "{\"customer_id\":$GC_ID}" | jget "d['data']['conversation_id']")
CV2=$(curl -s -X POST "$B/api/v1/chat/welcome?visitor_key=$GC_VK" -H "Content-Type: application/json" \
  -H "X-Tenant-ID: $UA_ID" -d "{\"customer_id\":$GC_ID}" | jget "d['data']['conversation_id']")
check "welcome返回会话ID(11)" y "$([ -n "$CV1" ] && echo y || echo n)"
check "welcome两次复用同一会话" "$CV1" "$CV2"
ACT11=$($PSQL "SELECT count(*) FROM conversations WHERE tenant_id=$UA_ID AND customer_id=$GC_ID AND status='active'")
check "客户active会话数=1(字节级)" 1 "$ACT11"
# 11.3 绕过应用直插第二条 active：必须被 013 索引拒绝——约束真实存在于 DB 层。
# （psql 默认不输出 SQLSTATE 数字，按"unique constraint ux_conv_one_active"文案判定）
DUP11=$(psql "${TEST_DB_URL:-postgresql://ai_scrm:dev123@localhost/ai_scrm}" \
  -c "INSERT INTO conversations (tenant_id,customer_id,status,mode,channel,created_at,updated_at) VALUES ($UA_ID,$GC_ID,'active','ai','web',NOW(),NOW())" 2>&1 | grep -cE '23505|unique constraint "ux_conv_one_active"')
check "重复active直插被013索引拒绝(23505)" 1 "$DUP11"

echo ""
echo "== 十二、复核批护栏（2026-09-18，AUDIT_VERIFY_2026-09-18）=="
# 12.1 mock-pay 归属闸：pay_mode≠mock 时模拟到账必须 403，且字节级确认"订单未到账 +
#      权益台账零行"双不变。此前 uat 六节只测了"sdk 模式下单报错"与"不存在单 400/404"，
#      真实 pending 单在非 mock 模式被 mock-pay 打穿（0元白嫖面）从未有断言。
UAD="Authorization: Bearer $UA_TOKEN"
curl -s -X PUT "$B/api/v1/admin/config" -H "$AH" -H "Content-Type: application/json" \
  -d '[{"category":"notify","key":"pay_mode","value":"\"static_qr\""}]' >/dev/null
R121=$(curl -s -X POST "$B/api/v1/billing/orders" -H "$UAD" -H "X-Tenant-ID: $UA_ID" -H "Content-Type: application/json" \
  -d "{\"package_id\":$INC_PKG}")
ORD12=$(echo "$R121" | jget "d['data']['id']")
check "非mock模式pending单创建成功(12.1)" y "$([ -n "$ORD12" ] && echo y || echo n)"
MP12=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/billing/orders/mock-pay" -H "$UAD" -H "X-Tenant-ID: $UA_ID" \
  -H "Content-Type: application/json" -d "{\"order_id\":$ORD12}")
check "非mock模式mock-pay被拒(403)" 403 "$MP12"
ST12=$($PSQL "SELECT status FROM billing_orders WHERE id=$ORD12")
check "被拒后订单仍pending(字节级)" pending "$ST12"
ENT12=$($PSQL "SELECT count(*) FROM reward_claims WHERE grant_type='order_entitlement' AND ref_id=$ORD12")
check "被拒后权益台账零行(字节级)" 0 "$ENT12"
curl -s -X PUT "$B/api/v1/admin/config" -H "$AH" -H "Content-Type: application/json" \
  -d '[{"category":"notify","key":"pay_mode","value":"\"mock\""}]' >/dev/null
# 12.2 接管→转回AI 字段级落库断言（chat_human.go:103/248/279 系整行 Save 路径的
#      回归护栏：列名/翻转语义漂移即抓——P1-2 收口前以行为锁定代防）。
TK12=$(curl -s -X POST "$B/api/v1/advisor/chat/takeover" -H "$UAD" -H "X-Tenant-ID: $UA_ID" \
  -H "Content-Type: application/json" -d "{\"conversation_id\":$CV1}" | code)
check "租户admin接管会话(12.2)" 0 "$TK12"
COL12=$($PSQL "SELECT mode||'|'||is_human_locked||'|'||pending_handoff FROM conversations WHERE id=$CV1")
check "接管后三列字节级(human|true|false)" "human|true|false" "$COL12"
TR12=$(curl -s -X POST "$B/api/v1/chat/transfer/ai" -H "$UAD" -H "X-Tenant-ID: $UA_ID" \
  -H "Content-Type: application/json" -d "{\"conversation_id\":$CV1}" | code)
check "转回AI成功(12.2)" 0 "$TR12"
COL12B=$($PSQL "SELECT mode||'|'||is_human_locked||'|'||is_ai_reply_enabled FROM conversations WHERE id=$CV1")
check "转回AI三列字节级(ai|false|true)" "ai|false|true" "$COL12B"
# 12.3 super_admin 无 X-Tenant-ID 打租户作用域路径：必须 400/403，绝不 200 带数据。
#      （P2-15 白名单口径护栏；前端 Org.tsx 裸 fetch 不带头的体验缺口另见复核文档）
ORG12=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/org/departments/tree" -H "$AH")
case "$ORG12" in 400|403) check "超管无租户头打/org被拒(12.3)" y y;; *) check "超管无租户头打/org被拒(实际=$ORG12)" y n;; esac
# 12.4 访客消息落库字节级回环：/chat/test 报文原文必须与 messages.content 逐字节一致
#      （chat_main.go:306 Create 不查 .Error——写入面唯一可观测护栏即读回比对）。
MSG12="复核UAT字节回环$RUN"
curl -s -o /dev/null -X POST "$B/api/v1/chat/test?visitor_key=$GC_VK" -H "X-Tenant-ID: $UA_ID" \
  -H "Content-Type: application/json" -d "{\"customer_id\":$GC_ID,\"content\":\"$MSG12\"}"
sleep 2
BYTE12=$($PSQL "SELECT count(*) FROM messages WHERE tenant_id=$UA_ID AND customer_id=$GC_ID AND sender_type='customer' AND content='$MSG12'")
check "访客消息内容DB逐字节一致(12.4)" 1 "$BYTE12"
# 现场清理：本节+十一测试客户及其会话/消息、12.1 pending 单不留库
$PSQL "DELETE FROM messages WHERE customer_id=$GC_ID AND tenant_id=$UA_ID; DELETE FROM conversations WHERE customer_id=$GC_ID AND tenant_id=$UA_ID; DELETE FROM customers WHERE id=$GC_ID; DELETE FROM billing_orders WHERE id=$ORD12" >/dev/null 2>&1

echo ""
echo "== 恢复现场 =="
curl -s -X PUT "$B/api/v1/admin/config" -H "$AH" -H "Content-Type: application/json" \
  -d '[{"category":"notify","key":"email_verify_enabled","value":"true"},{"category":"notify","key":"pay_mode","value":"\"mock\""},{"category":"billing","key":"token_billing_enabled","value":"false"},{"category":"billing","key":"billing_enforced","value":"false"},{"category":"billing","key":"register_ip_daily_limit","value":"3"},{"category":"billing","key":"register_ip_min_interval_sec","value":"60"}]' >/dev/null
echo "  已恢复: 邮箱验证/pay_mode/token双开关/IP限流默认值"

echo ""
echo "=========================================================="
echo " UAT 结果: PASS=$PASS FAIL=$FAIL"
if [ $FAIL -gt 0 ]; then echo -e "失败明细:$FAILED_CASES"; fi
echo "=========================================================="
exit $FAIL
