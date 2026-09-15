#!/bin/bash
# ============================================================
# §W 支付回调验签 + 防重放 E2E（C6 资金安全收口，2026-09-14）
# 聚焦 uat.sh 未覆盖的到账回调核心安全边界：
#   BillingWebhook HMAC-SHA256 V2 口径（order_no|status|timestamp|nonce）
#   + ±5min 时间窗 + nonce 去重 + 渠道一致性 + 幂等，以及 mock-webhook 测试资产路由。
# 用法: ./tools/smoke_pay.sh 9090
# 前置: 本地非 release 模式（pay_gateway_key 未配置时 mock 渠道回落固定开发密钥）。
# ============================================================
B="http://localhost:${1:-9090}"
PSQL="psql ${TEST_DB_URL:-postgresql://ai_scrm:dev123@localhost/ai_scrm} -tAc"
DEV_KEY="mock-webhook-dev-key"   # billing.BillingWebhook：channel=mock 且非 release 的回落密钥
PASS=0; FAIL=0
check(){ if [ "$2" = "$3" ]; then echo "  PASS  $1 ($3)"; PASS=$((PASS+1)); else echo "  FAIL  $1 期望=$2 实际=$3"; FAIL=$((FAIL+1)); fi }
jget(){ python3 -c "import sys,json;d=json.load(sys.stdin);print($1)" 2>/dev/null; }

TAG="pay$RANDOM"
echo "==== 支付回调验签/防重放 E2E @ $B ===="

# ---- 0. 登录超管（确认/代发用）；回调端点公开无鉴权，测试订单经 DB 直插隔离于生产 ----
# 测试环境收口：admin 首登强改密标记会拦所有 /super 请求（MustChangePasswordGuard 白名单仅改密/me）。
# 与 uat.sh 同口径，脚本内清该标记（仅 dev DB，trap 不恢复安全标记）。
$PSQL "UPDATE tenant_users SET must_change_password=false WHERE username='admin'" >/dev/null 2>&1
AH_TOKEN=$(curl -s -X POST $B/api/v1/auth/login -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"admin123"}' | jget "d['data']['token']")
AH="Authorization: Bearer $AH_TOKEN"
UB_ID=$($PSQL "SELECT id FROM tenants WHERE code='acme' ORDER BY id LIMIT 1" | tr -d '[:space:]')

# ---- 1. 保证 mock 模式 + 干净密钥环境（trap 恢复）----
ORIG_PAYMODE=$($PSQL "SELECT value FROM system_configs WHERE tenant_id=0 AND key='pay_mode'" | tr -d '[:space:]')
ORIG_GKEY=$($PSQL "SELECT value FROM system_configs WHERE tenant_id=0 AND key='pay_gateway_key'" | tr -d '[:space:]')
trap '
  [ -n "$ORIG_PAYMODE" ] && $PSQL "UPDATE system_configs SET value='"'"'$ORIG_PAYMODE'"'"' WHERE tenant_id=0 AND key='"'"'pay_mode'"'"'" >/dev/null 2>&1
  if [ -n "$ORIG_GKEY" ]; then $PSQL "UPDATE system_configs SET value='"'"'$ORIG_GKEY'"'"' WHERE tenant_id=0 AND key='"'"'pay_gateway_key'"'"'" >/dev/null 2>&1; else $PSQL "DELETE FROM system_configs WHERE tenant_id=0 AND key='"'"'pay_gateway_key'"'"'" >/dev/null 2>&1; fi
  echo "  [trap] 已恢复 pay_mode/pay_gateway_key"
' EXIT
# 清空 pay_gateway_key 触发 mock 渠道回落开发密钥（仅非 release）
$PSQL "DELETE FROM system_configs WHERE tenant_id=0 AND key='pay_gateway_key'" >/dev/null 2>&1

# ---- 2. 造两条同租户 mock 渠道 pending 订单（金额无关，测回调+幂等+防重放）----
PKG=$($PSQL "SELECT id FROM packages WHERE code='booster_1000' AND enabled ORDER BY id LIMIT 1" | tr -d '[:space:]')
ORD1_NO="BO${TAG}X01"; ORD2_NO="BO${TAG}X02"
$PSQL "INSERT INTO billing_orders (order_no,tenant_id,package_id,amount_cents,period,channel,status,created_at,updated_at) VALUES ('${ORD1_NO}',${UB_ID},${PKG},100,'once','mock','pending',NOW(),NOW()),('${ORD2_NO}',${UB_ID},${PKG},100,'once','mock','pending',NOW(),NOW())" >/dev/null
echo "  测试订单: $ORD1_NO / $ORD2_NO (tenant $UB_ID, channel mock)"

# 计算签名的小工具：sign=HMAC_SHA256(DEV_KEY, "order_no|status|timestamp|nonce") hex（与 VerifyGatewaySignV2 对齐）
sign(){ python3 -c "import hmac,hashlib;print(hmac.new(b'$DEV_KEY', b'$1|$2|$3|$4', hashlib.sha256).hexdigest())"; }

TS=$(date +%s)
NONCE1="n1_${TAG}_$$"
SIG1=$(sign "$ORD1_NO" "TRADE_SUCCESS" "$TS" "$NONCE1")

# ---- 3. 合法回调 → 到账 + 权益发放 ----
R3=$(curl -s -X POST "$B/api/v1/billing/webhook/mock" -H "Content-Type: application/json" \
  -d "{\"order_no\":\"$ORD1_NO\",\"trade_status\":\"TRADE_SUCCESS\",\"timestamp\":\"$TS\",\"nonce\":\"$NONCE1\",\"sign\":\"$SIG1\"}")
FLOWED=$(echo "$R3" | jget "d['data'].get('flowed')")
CODE3=$(echo "$R3" | jget "d.get('code')")
ST1=$($PSQL "SELECT status FROM billing_orders WHERE order_no='$ORD1_NO'" | tr -d '[:space:]')
check "合法签名回调 code=0" 0 "$CODE3"
check "首次回调 flowed=true" True "$FLOWED"
check "订单转 paid" paid "$ST1"

# ---- 4. C6 防重放：同一 nonce 二次回调 → 409（即便签名合法、时间新鲜）----
H4=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/billing/webhook/mock" -H "Content-Type: application/json" \
  -d "{\"order_no\":\"$ORD1_NO\",\"trade_status\":\"TRADE_SUCCESS\",\"timestamp\":\"$TS\",\"nonce\":\"$NONCE1\",\"sign\":\"$SIG1\"}")
check "重复 nonce 回调被拒(409)" 409 "$H4"
check "重放不二次改状态(仍paid)" paid "$($PSQL "SELECT status FROM billing_orders WHERE order_no='$ORD1_NO'" | tr -d '[:space:]')"

# ---- 5. C6 篡改签名 → 403 ----
TS2=$(date +%s); NONCE2="n2_${TAG}_$$"
BADSIG=$(sign "$ORD1_NO" "TRADE_SUCCESS" "$TS2" "wrong_nonce_not_matching")
H5=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/billing/webhook/mock" -H "Content-Type: application/json" \
  -d "{\"order_no\":\"$ORD1_NO\",\"trade_status\":\"TRADE_SUCCESS\",\"timestamp\":\"$TS2\",\"nonce\":\"$NONCE2\",\"sign\":\"deadbeef${BADSIG:8}\"}")
check "篡改签名回调被拒(403)" 403 "$H5"

# ---- 6. C6 过期时间戳（超 ±5min）→ 403，即便签名按该旧戳正确 ----
OLD_TS=$((TS2 - 600)); NONCE3="n3_${TAG}_$$"
SIGOLD=$(sign "$ORD2_NO" "TRADE_SUCCESS" "$OLD_TS" "$NONCE3")
H6=$(curl -s -o /dev/null -w "%{http_code}" -H "Content-Type: application/json" -X POST "$B/api/v1/billing/webhook/mock" \
  -d "{\"order_no\":\"$ORD2_NO\",\"trade_status\":\"TRADE_SUCCESS\",\"timestamp\":\"$OLD_TS\",\"nonce\":\"$NONCE3\",\"sign\":\"$SIGOLD\"}")
check "过期时间戳回调被拒(403)" 403 "$H6"
check "过期回调不改单状态(仍pending)" pending "$($PSQL "SELECT status FROM billing_orders WHERE order_no='$ORD2_NO'" | tr -d '[:space:]')"

# ---- 7. 缺 timestamp/nonce → 403（V2 口径拒绝旧式裸 HMAC 报文）----
H7=$(curl -s -o /dev/null -w "%{http_code}" -H "Content-Type: application/json" -X POST "$B/api/v1/billing/webhook/mock" \
  -d "{\"order_no\":\"$ORD2_NO\",\"trade_status\":\"TRADE_SUCCESS\",\"sign\":\"whatever\"}")
check "缺 timestamp/nonce 回调被拒(403)" 403 "$H7"

# ---- 8. 合法回调第二单（不同 nonce，新鲜戳）→ 到账，验证非单笔偶发 ----
TS3=$(date +%s); NONCE4="n4_${TAG}_$$"
SIG4=$(sign "$ORD2_NO" "TRADE_SUCCESS" "$TS3" "$NONCE4")
R8=$(curl -s -H "Content-Type: application/json" -X POST "$B/api/v1/billing/webhook/mock" \
  -d "{\"order_no\":\"$ORD2_NO\",\"trade_status\":\"TRADE_SUCCESS\",\"timestamp\":\"$TS3\",\"nonce\":\"$NONCE4\",\"sign\":\"$SIG4\"}")
check "第二单合法回调到账" paid "$($PSQL "SELECT status FROM billing_orders WHERE order_no='$ORD2_NO'" | tr -d '[:space:]')"
check "第二单 flowed=true" True "$(echo "$R8" | jget "d['data'].get('flowed')")"

# ---- 9. 渠道一致性：订单 channel=mock，却投 wechat 渠道回调 → 应被拒（防跨渠道冒领到账）----
TS4=$(date +%s); NONCE5="n5_${TAG}_$$"
# 先造第三单
ORD3_NO="BO${TAG}X03"
$PSQL "INSERT INTO billing_orders (order_no,tenant_id,package_id,amount_cents,period,channel,status,created_at,updated_at) VALUES ('${ORD3_NO}',${UB_ID},${PKG},100,'once','mock','pending',NOW(),NOW())" >/dev/null
SIG5=$(sign "$ORD3_NO" "TRADE_SUCCESS" "$TS4" "$NONCE5")
R9=$(curl -s -H "Content-Type: application/json" -X POST "$B/api/v1/billing/webhook/wechat" \
  -d "{\"order_no\":\"$ORD3_NO\",\"trade_status\":\"TRADE_SUCCESS\",\"timestamp\":\"$TS4\",\"nonce\":\"$NONCE5\",\"sign\":\"$SIG5\"}")
# wechat 渠道无 key → 503（未配置密钥），绝不因 mock 回落密钥而放行
ST3=$($PSQL "SELECT status FROM billing_orders WHERE order_no='$ORD3_NO'" | tr -d '[:space:]')
check "跨渠道回调不放行到账(mock单仍pending)" pending "$ST3"

# ---- 10. mock-webhook 测试资产路由可用（超管代发，非 release + mock 渠道双闸门）----
ORD4_NO="BO${TAG}X04"
$PSQL "INSERT INTO billing_orders (order_no,tenant_id,package_id,amount_cents,period,channel,status,created_at,updated_at) VALUES ('${ORD4_NO}',${UB_ID},${PKG},100,'once','mock','pending',NOW(),NOW())" >/dev/null
ORD4_ID=$($PSQL "SELECT id FROM billing_orders WHERE order_no='$ORD4_NO'" | tr -d '[:space:]')
MW_CODE=$(curl -s -X POST "$B/api/v1/super/billing/orders/$ORD4_ID/mock-webhook" -H "$AH" | jget "d.get('code')")
MW_ST=$($PSQL "SELECT status FROM billing_orders WHERE order_no='$ORD4_NO'" | tr -d '[:space:]')
check "mock-webhook 路由已注册且到账" 0 "$MW_CODE"
check "mock-webhook 订单转 paid" paid "$MW_ST"

# ---- 清理测试订单 ----
$PSQL "DELETE FROM billing_orders WHERE order_no IN ('$ORD1_NO','$ORD2_NO','$ORD3_NO','$ORD4_NO')" >/dev/null 2>&1
echo "  [cleanup] 测试订单已回收"

echo ""
echo "==== smoke_pay 结果: PASS=$PASS FAIL=$FAIL ===="
[ "$FAIL" = "0" ] || exit 1
