#!/bin/bash
# ============================================================
# 全场景全流程 UAT v3（2026-08-26）—— 功能 + 付费 + 邀请奖励充分测试
# 用法: ./tools/uat.sh 9090    （结束自动恢复全部开关）
# ============================================================
B="http://localhost:${1:-9090}"
PSQL="psql postgresql://ai_scrm:dev123@localhost/ai_scrm -tAc"
Q(){ psql postgresql://ai_scrm:dev123@localhost/ai_scrm -tAc "$1"; }
PASS=0; FAIL=0; FAILED_CASES=""

check(){ if [ "$2" = "$3" ]; then PASS=$((PASS+1)); echo "  PASS  $1";
  else FAIL=$((FAIL+1)); FAILED_CASES="$FAILED_CASES\n    ✗ $1 (期望=$2 实际=$3)"; echo "  FAIL  $1 (期望=$2 实际=$3)"; fi }
jget(){ python3 -c "
import sys,json
try:
    d=json.load(sys.stdin); print(eval(sys.argv[1],{'d':d}))
except Exception: print('')" "$1"; }
code(){ jget "d['code']"; }

echo "== 0. 准备：清强改密标记 / 放开注册限流(内测兜底口径) / 关邮箱验证 =="
$PSQL "UPDATE tenant_users SET must_change_password=false WHERE username='admin'" >/dev/null 2>&1
ADMIN_TOKEN=$(curl -s -X POST "$B/api/v1/auth/login" -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"admin123"}' | jget "d['data']['token']")
AH="Authorization: Bearer $ADMIN_TOKEN"
[ -n "$ADMIN_TOKEN" ] && check "超管登录" y y || check "超管登录" y n
curl -s -X PUT "$B/api/v1/admin/config" -H "$AH" -H "Content-Type: application/json" \
  -d '[{"category":"notify","key":"email_verify_enabled","value":"false"},{"category":"billing","key":"register_ip_daily_limit","value":"1000"},{"category":"billing","key":"register_ip_min_interval_sec","value":"0"}]' >/dev/null

TS=$(date +%s)
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
UA_TOKEN=$(curl -s -X POST "$B/api/v1/auth/login" -H "Content-Type: application/json" \
  -d "{\"tenant_code\":\"$UA_CODE\",\"username\":\"ua$TS\",\"password\":\"uat123456\"}" | jget "d['data']['token']")
[ -n "$UA_TOKEN" ] && R=y || R=n
check "甲登录" y "$R"
check "③免费桶=30万" 300000 "$($PSQL "SELECT COALESCE(free_token_balance,0) FROM tenants WHERE id=$UA_ID")"
check "免费桶有效期已设" t "$($PSQL "SELECT free_token_expires_at IS NOT NULL FROM tenants WHERE id=$UA_ID")"
INV_A=$(curl -s "$B/api/v1/admin/referral/info" -H "Authorization: Bearer $UA_TOKEN" | jget "d['data']['referral']['invite_code']")
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
UB_TOKEN=$(curl -s -X POST "$B/api/v1/auth/login" -H "Content-Type: application/json" \
  -d "{\"tenant_code\":\"$UB_CODE\",\"username\":\"ub$TS\",\"password\":\"uat123456\"}" | jget "d['data']['token']")
BH="Authorization: Bearer $UB_TOKEN"
PAID_PKG=$($PSQL "SELECT id FROM packages WHERE p_type='paid' ORDER BY sort_order LIMIT 1")
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
UC_TOKEN=$(curl -s -X POST "$B/api/v1/auth/login" -H "Content-Type: application/json" \
  -d "{\"tenant_code\":\"$UC_CODE\",\"username\":\"uc$TS\",\"password\":\"uat123456\"}" | jget "d['data']['token']")
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
$PSQL "INSERT INTO reward_claims (grant_type,tenant_id,email,note) VALUES ('referral_paid',$($PSQL "SELECT id FROM tenants LIMIT 1"),'uat-swap@t.com','占位撞库样本')" >/dev/null
SW=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/auth/email/change" -H "$BH" -H "Content-Type: application/json" \
  -d '{"new_email":"uat-swap@t.com","code":"any"}')
check "撞库邮箱换绑被拒(409)" 409 "$SW"

echo ""
echo "== 五、static_qr 人工确认链路 =="
# P1.5-UAT修复：平台键字符串值必须 JSON 引号编码（BatchUpdate 契约），否则静默跳过
curl -s -X PUT "$B/api/v1/admin/config" -H "$AH" -H "Content-Type: application/json" -d '[{"category":"notify","key":"pay_mode","value":"\"static_qr\""},{"category":"notify","key":"static_qr_image","value":"\"https://pay.uat.example.com/x.png\""}]' >/dev/null
INC_PKG=$($PSQL "SELECT id FROM packages WHERE p_type='increment' ORDER BY sort_order LIMIT 1")
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
$PSQL "INSERT INTO billing_orders (order_no,tenant_id,amount_cents,channel,status,created_at) VALUES ('BO_UAT_T2',${UB_ID},9900,'mock','pending',NOW()-INTERVAL '20 minutes')" >/dev/null
pkill -f "^\./ai-scrm"; sleep 2
python3 - << 'PY'
import subprocess
subprocess.Popen(["./ai-scrm"], stdin=subprocess.DEVNULL,
    stdout=open("ai-scrm.log","ab"), stderr=subprocess.STDOUT, start_new_session=True)
PY
for i in $(seq 1 30); do sleep 2; c=$(curl -s -o /dev/null -w "%{http_code}" --max-time 3 "$B/health" 2>/dev/null); [ "$c" = "200" ] && break; done
for U in admin dep36user dep37user; do $PSQL "UPDATE tenant_users SET must_change_password=false WHERE username='$U'" >/dev/null; done
check "僵尸单自动closed" closed "$($PSQL "SELECT status FROM billing_orders WHERE order_no='BO_UAT_T2'")"

echo ""
echo "== 八、Token三桶强制扣减级联 =="
curl -s -X PUT "$B/api/v1/admin/config" -H "$AH" -H "Content-Type: application/json" \
  -d '[{"category":"billing","key":"token_billing_enabled","value":"true"},{"category":"billing","key":"billing_enforced","value":"true"}]' >/dev/null
$PSQL "UPDATE tenants SET free_token_balance=6000, free_token_expires_at=NOW()+INTERVAL '5 days', monthly_token_quota=80000, monthly_token_used=0, token_balance=25000 WHERE id=$UB_ID" >/dev/null
CUST_RAW=$(curl -s -X POST "$B/api/v1/customers" -H "$BH" -H "Content-Type: application/json" -d '{"name":"UAT乙客户"}')
CUST_B=$(echo "$CUST_RAW" | jget "d['data']['id']")
[ -z "$CUST_B" ] && echo "    [debug] customers原始: $(echo "$CUST_RAW" | head -c 160)"
[ -n "$CUST_B" ] && R=y || R=n
check "C端建联" y "$R"
round(){ curl -s --max-time 170 -X POST "$B/api/v1/chat" -H "$BH" -H "Content-Type: application/json" -d "{\"customer_id\":$CUST_B,\"content\":\"$1\"}" >/dev/null; sleep 2; }
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
grep "三桶余额不足" ai-scrm.log | tail -1 | grep -q "降级规则话术" && R=y || R=n
check "全空→降级规则话术" y "$R"

echo ""
echo "== 九、对话分支：留资合并 + 快捷通道 =="
curl -s --max-time 170 -X POST "$B/api/v1/chat" -H "$BH" -H "Content-Type: application/json" \
  -d "{\"customer_id\":$CUST_B,\"content\":\"我叫赵铁柱，手机13912345678，明天想去店里看看\"}" >/dev/null
sleep 1
grep -E "留资检测|自动分配顾问" ai-scrm.log | tail -3 | grep -qE "." && R=y || R=n
check "留资检测+分配顾问日志" y "$R"
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
UF_TOKEN=$(curl -s -X POST "$B/api/v1/auth/login" -H "Content-Type: application/json" \
  -d "{\"tenant_code\":\"$UF_CODE\",\"username\":\"uf$TS\",\"password\":\"uat123456\"}" | jget "d['data']['token']")
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
CT=$(curl -s -o /tmp/uat_qr.png -w "%{content_type}" "$B/api/v1/admin/referral/qrcode" -H "$BH")
check "后端渲染PNG" image/png "$CT"

echo ""
echo "== 十四、UAT定稿三项+邀请记录（2026-08-26）=="
TS2=$((TS+7))
# 14.1 弱密码拒绝
R=$(curl -s -X POST "$B/api/v1/tenant/signup" -H "Content-Type: application/json" \
  -d "{\"company_name\":\"弱密户\",\"code\":\"weak$((TS2%99999))\",\"username\":\"wk$TS2\",\"password\":\"abc123\"}" | jget "d['message']")
case "$R" in *至少8位*字母*) check "弱密码拒绝(提示含强度要求)" y y;; *) check "弱密码拒绝(msg=$R)" y n;; esac
# 14.2 未知行业兜底general / 已知行业保留
C_G="uindg$((TS2%99999))"
curl -s -X POST "$B/api/v1/tenant/signup" -H "Content-Type: application/json" \
  -d "{\"company_name\":\"未知行业\",\"code\":\"$C_G\",\"username\":\"ug$TS2\",\"password\":\"uat123456\",\"industry\":\"metaverse\"}" >/dev/null
G_IND=$($PSQL "SELECT industry FROM tenants WHERE code='$C_G'")
check "未知行业回落general" general "$G_IND"
Q "INSERT INTO industry_packs (code,name,industry,version,pack_level,status,file_path) VALUES ('education','教育行业包','education','1.0.0','industry','active','n/a') ON CONFLICT DO NOTHING" >/dev/null
C_E="uinde$((TS2%99999))"
curl -s -X POST "$B/api/v1/tenant/signup" -H "Content-Type: application/json" \
  -d "{\"company_name\":\"已知行业\",\"code\":\"$C_E\",\"username\":\"ue$TS2\",\"password\":\"uat123456\",\"industry\":\"education\"}" >/dev/null
E_IND=$($PSQL "SELECT industry FROM tenants WHERE code='$C_E'")
check "已知行业保留不回落" education "$E_IND"
# 14.3 重复邮箱注册409
EM="dup$TS2@t.com"
curl -s -X POST "$B/api/v1/tenant/signup" -H "Content-Type: application/json" \
  -d "{\"company_name\":\"首注邮箱\",\"code\":\"dpa$((TS2%99999))\",\"username\":\"dp$TS2\",\"password\":\"uat123456\",\"admin_email\":\"$EM\"}" >/dev/null
DUP=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/tenant/signup" -H "Content-Type: application/json" \
  -d "{\"company_name\":\"重注\",\"code\":\"dpb$((TS2%99999))\",\"username\":\"dq$TS2\",\"password\":\"uat123456\",\"admin_email\":\"$EM\"}")
check "重复邮箱注册409" 409 "$DUP"
# 14.4 邀请记录接口（甲=既有受邀链顶层租户）
A_ID2=$($PSQL "SELECT id FROM tenants WHERE code LIKE 'uata%' ORDER BY id DESC LIMIT 1")
ATOK2=$(curl -s -X POST "$B/api/v1/auth/login" -H "Content-Type: application/json" \
  -d "{\"tenant_code\":\"$($PSQL "SELECT code FROM tenants WHERE id=$A_ID2")\",\"username\":\"$($PSQL "SELECT username FROM tenant_users WHERE tenant_id=$A_ID2 AND role='tenant_admin' LIMIT 1")\",\"password\":\"uat123456\"}" | jget "d['data']['token']")
REC=$(curl -s "$B/api/v1/admin/referral/records" -H "Authorization: Bearer $ATOK2")
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
