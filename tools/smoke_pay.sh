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
# P0-1b 后 wechat 分支走 V3 协议：无 Wechatpay-Timestamp 头 → 403（gateway 式 JSON 报文直接被时间窗闸拒）
H9=$(echo "$R9" >/dev/null; curl -s -o /dev/null -w "%{http_code}" -H "Content-Type: application/json" -X POST "$B/api/v1/billing/webhook/wechat" \
  -d "{\"order_no\":\"$ORD3_NO\",\"trade_status\":\"TRADE_SUCCESS\",\"timestamp\":\"$TS4\",\"nonce\":\"$NONCE5\",\"sign\":\"$SIG5\"}")
check "wechat渠道拒gateway式报文(403)" 403 "$H9"
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

# ============================================================
# P0-1 原生渠道 E2E（2026-09-15）：微信支付 V3 / 支付宝当面付 回调全链路
#   - 微信：配置测试 APIv3Key → DB 直插 channel=wechat 订单 → node 构造 AES-256-GCM
#     加密 resource（ciphertext=密文+tag，与 Go Seal/Open 对齐）→ POST 回调 → 幂等到账+台账行
#     负向：篡改密文 403 / nonce 重放 409
#   - 支付宝：node 生成测试 RSA 密钥对 → 平台公钥入系统配置 → form 通知 RSA2 签名
#     → POST 回调 → 到账；负向：篡改金额 403
#   - 测试用一次性租户（避免污染 acme 余额），cleanup 级联回收
# ============================================================
WTAG="pw$RANDOM"
WORK=$(mktemp -d /tmp/smokepay.XXXXXX)
# 一次性租户（tenants 必填 name/code；status=active 免被 TenantResolver 拦）
# 注意：psql -c INSERT...RETURNING 会附带 "INSERT 0 1" 状态行，取首行数字即可
WT_ID=$($PSQL "INSERT INTO tenants (name,code,status,created_at,updated_at) VALUES ('paywebhook-smoke','${WTAG}','active',NOW(),NOW()) RETURNING id" | head -1 | tr -d '[:space:]')
# trap 扩展：恢复微信/支付宝测试密钥配置 + 级联回收一次性租户
WT_ORIG_V3=$($PSQL "SELECT value FROM system_configs WHERE tenant_id=0 AND key='pay_wechat_apiv3_key'" | tr -d '[:space:]')
WT_ORIG_PUB=$($PSQL "SELECT value FROM system_configs WHERE tenant_id=0 AND key='pay_alipay_public_key'" | tr -d '[:space:]')
# P0-1 复核批(2026-09-15)：alipay 回调新增 app_id 归属必核（未配置=403 fail-closed），测试资产同步配置
WT_ORIG_AAPP=$($PSQL "SELECT value FROM system_configs WHERE tenant_id=0 AND key='pay_alipay_app_id'" | tr -d '[:space:]')
trap '
  [ -n "$ORIG_PAYMODE" ] && $PSQL "UPDATE system_configs SET value='"'"'$ORIG_PAYMODE'"'"' WHERE tenant_id=0 AND key='"'"'pay_mode'"'"'" >/dev/null 2>&1
  if [ -n "$ORIG_GKEY" ]; then $PSQL "UPDATE system_configs SET value='"'"'$ORIG_GKEY'"'"' WHERE tenant_id=0 AND key='"'"'pay_gateway_key'"'"'" >/dev/null 2>&1; else $PSQL "DELETE FROM system_configs WHERE tenant_id=0 AND key='"'"'pay_gateway_key'"'"'" >/dev/null 2>&1; fi
  if [ -n "$WT_ORIG_V3" ]; then $PSQL "UPDATE system_configs SET value='"'"'$WT_ORIG_V3'"'"' WHERE tenant_id=0 AND key='"'"'pay_wechat_apiv3_key'"'"'" >/dev/null 2>&1; else $PSQL "DELETE FROM system_configs WHERE tenant_id=0 AND key='"'"'pay_wechat_apiv3_key'"'"'" >/dev/null 2>&1; fi
  if [ -n "$WT_ORIG_PUB" ]; then $PSQL "UPDATE system_configs SET value='"'"'$WT_ORIG_PUB'"'"' WHERE tenant_id=0 AND key='"'"'pay_alipay_public_key'"'"'" >/dev/null 2>&1; else $PSQL "DELETE FROM system_configs WHERE tenant_id=0 AND key='"'"'pay_alipay_public_key'"'"'" >/dev/null 2>&1; fi
  if [ -n "$WT_ORIG_AAPP" ]; then $PSQL "UPDATE system_configs SET value='"'"'$WT_ORIG_AAPP'"'"' WHERE tenant_id=0 AND key='"'"'pay_alipay_app_id'"'"'" >/dev/null 2>&1; else $PSQL "DELETE FROM system_configs WHERE tenant_id=0 AND key='"'"'pay_alipay_app_id'"'"'" >/dev/null 2>&1; fi
  [ -n "$WT_ID" ] && $PSQL "DELETE FROM reward_claims WHERE tenant_id=$WT_ID OR ref_id IN (SELECT id FROM billing_orders WHERE tenant_id=$WT_ID); DELETE FROM billing_orders WHERE tenant_id=$WT_ID; DELETE FROM tenants WHERE id=$WT_ID" >/dev/null 2>&1
  rm -rf "$WORK" >/dev/null 2>&1
  echo "  [trap] 已恢复 pay_mode/pay_gateway_key/微信支付宝密钥 + 回收一次性租户"
' EXIT

echo ""
echo "---- 11. 微信支付 V3 回调（P0-1b）----"
# 11.1 配置测试 APIv3Key（32 字节）并等待热加载（响应回显便于诊断）
# 注意：body 必须先落变量再传 -d——若在 echo"...$(curl -d "[{\"..\"]}]" )" 里内联转义，
# \" 会被外层双引号上下文吞掉，花括号扩展把 body 拆成多个"URL"导致 curl 重复发碎片请求
WX_KEY="wxsmokekey_0123456789abcdef_$WTAG"; WX_KEY="${WX_KEY:0:32}"
WV3_NO="BO${WTAG}W1"
WX_CFG_BODY="[{\"category\":\"billing\",\"key\":\"pay_wechat_apiv3_key\",\"value\":\"$WX_KEY\"}]"
echo "  [wx] 配置写入响应: $(curl -s -X PUT "$B/api/v1/admin/config" -H "$AH" -H "Content-Type: application/json" -d "$WX_CFG_BODY")"
sleep 1
# 11.2 DB 直插 wechat 渠道 pending 订单 + 一次性租户的包引用
$PSQL "INSERT INTO billing_orders (order_no,tenant_id,package_id,amount_cents,period,channel,status,created_at,updated_at) VALUES ('${WV3_NO}',${WT_ID},${PKG},100,'once','wechat','pending',NOW(),NOW())" >/dev/null
# 11.3 node 构造 V3 resource：AES-256-GCM(APIv3Key, AAD=transaction)，ciphertext=密文‖tag（Go Seal 口径）
cat > "$WORK/wxres.js" <<'EOF'
const c=require('crypto');
// V3 规范：nonce 是 12 字节 ASCII 随机串原文（非 base64），GCM 直接取字节
const ALPHA='ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789';
const nb=c.randomBytes(12);
const nonce=Array.from(nb,b=>ALPHA[b%ALPHA.length]).join('');
const key=Buffer.from(process.env.WX_KEY,'utf8');
const aead=c.createCipheriv('aes-256-gcm',key,Buffer.from(nonce,'utf8'));
aead.setAAD(Buffer.from('transaction'));
const plain=JSON.stringify({out_trade_no:process.env.WX_NO,trade_state:'SUCCESS'});
const ct=Buffer.concat([aead.update(plain,'utf8'),aead.final(),aead.getAuthTag()]);
process.stdout.write(JSON.stringify({resource:{ciphertext:ct.toString('base64'),nonce:nonce,associated_data:'transaction'}}));
EOF
WX_BODY=$(WX_KEY="$WX_KEY" WX_NO="$WV3_NO" node "$WORK/wxres.js")
R11=$(curl -s -X POST "$B/api/v1/billing/webhook/wechat" \
  -H "Content-Type: application/json" \
  -H "Wechatpay-Timestamp: $(date +%s)" \
  -H "Wechatpay-Nonce: wn_${WTAG}_$$" \
  -d "$WX_BODY")
check "V3解密回调 code=0" 0 "$(echo "$R11" | jget "d.get('code')")"
check "wechat订单转paid" paid "$($PSQL "SELECT status FROM billing_orders WHERE order_no='$WV3_NO'" | tr -d '[:space:]')"
check "wechat发放台账落库" 1 "$($PSQL "SELECT COUNT(*) FROM reward_claims WHERE grant_type='order_entitlement' AND ref_id=(SELECT id FROM billing_orders WHERE order_no='$WV3_NO')" | tr -d '[:space:]')"
# 11.4 篡改密文（翻转首字符）→ 解密失败 403
WT_BODY=$(WX_KEY="$WX_KEY" WX_NO="$WV3_NO" node "$WORK/wxres.js" | python3 -c "import sys,json;d=json.load(sys.stdin);ct=d['resource']['ciphertext'];d['resource']['ciphertext']=('B' if ct[0]!='B' else 'C')+ct[1:];print(json.dumps(d))")
WX_TS2=$(date +%s)
H11=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/billing/webhook/wechat" \
  -H "Content-Type: application/json" -H "Wechatpay-Timestamp: $WX_TS2" -H "Wechatpay-Nonce: wn2_${WTAG}_$$" \
  -d "$WT_BODY")
check "篡改V3密文被拒(403)" 403 "$H11"
# 11.5 nonce 重放 → 409（即便密文合法——订单已 paid，重放同样不二次发放）
H11B=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/billing/webhook/wechat" \
  -H "Content-Type: application/json" -H "Wechatpay-Timestamp: $(date +%s)" -H "Wechatpay-Nonce: wn_${WTAG}_$$" \
  -d "$WX_BODY")
check "wechat回调nonce重放被拒(409)" 409 "$H11B"

echo ""
echo "---- 12. 支付宝当面付 回调（P0-1c）----"
# 12.1 node 生成测试 RSA 密钥对（PKCS1 私钥 + SPKI 公钥 PEM）
cat > "$WORK/gen.js" <<'EOF'
const c=require('crypto');
const kp=c.generateKeyPairSync('rsa',{modulusLength:2048});
require('fs').writeFileSync(process.env.P+'ali_priv.pem',kp.privateKey.export({type:'pkcs1',format:'pem'}));
require('fs').writeFileSync(process.env.P+'ali_pub.pem',kp.publicKey.export({type:'spki',format:'pem'}));
EOF
P="$WORK/" node "$WORK/gen.js"
# 12.2 平台公钥入系统配置（PEM 全文 JSON 编码，热加载后 webhook 验签用）
ALI_PUB_JSON=$(python3 -c "import json;print(json.dumps(open('$WORK/ali_pub.pem').read()))")
curl -s -X PUT "$B/api/v1/admin/config" -H "$AH" -H "Content-Type: application/json" \
  -d "[{\"category\":\"billing\",\"key\":\"pay_alipay_public_key\",\"value\":$ALI_PUB_JSON},{\"category\":\"billing\",\"key\":\"pay_alipay_app_id\",\"value\":\"2021smoke\"}]" >/dev/null
sleep 1
# 12.3 form 通知 RSA2 签名（排除 sign/sign_type/空值，字典序拼串——与 VerifyAlipayNotify 对齐）并回调
ALI_NO="BO${WTAG}A1"
$PSQL "INSERT INTO billing_orders (order_no,tenant_id,package_id,amount_cents,period,channel,status,created_at,updated_at) VALUES ('${ALI_NO}',${WT_ID},${PKG},100,'once','alipay','pending',NOW(),NOW())" >/dev/null
cat > "$WORK/alisign.js" <<'EOF'
const c=require('crypto');
const priv=require('fs').readFileSync(process.env.PRIV_FILE,'utf8');
const params=JSON.parse(process.env.PARAMS);
const pairs=Object.keys(params).filter(k=>k!=='sign'&&k!=='sign_type'&&params[k]!=='').sort().map(k=>k+'='+params[k]);
const sig=c.sign('sha256',Buffer.from(pairs.join('&'),'utf8'),{key:priv,padding:c.constants.RSA_PKCS1_PADDING});
params.sign=sig.toString('base64');
process.stdout.write(new URLSearchParams(params).toString());
EOF
ALI_FORM=$(PRIV_FILE="$WORK/ali_priv.pem" PARAMS="{\"app_id\":\"2021smoke\",\"out_trade_no\":\"$ALI_NO\",\"trade_status\":\"TRADE_SUCCESS\",\"total_amount\":\"1.00\",\"notify_id\":\"ni_${WTAG}_$$\"}" node "$WORK/alisign.js")
R12=$(curl -s -X POST "$B/api/v1/billing/webhook/alipay" \
  -H "Content-Type: application/x-www-form-urlencoded" --data "$ALI_FORM")
check "RSA2验签回调 code=0" 0 "$(echo "$R12" | jget "d.get('code')")"
check "alipay订单转paid" paid "$($PSQL "SELECT status FROM billing_orders WHERE order_no='$ALI_NO'" | tr -d '[:space:]')"
check "alipay发放台账落库" 1 "$($PSQL "SELECT COUNT(*) FROM reward_claims WHERE grant_type='order_entitlement' AND ref_id=(SELECT id FROM billing_orders WHERE order_no='$ALI_NO')" | tr -d '[:space:]')"
# 12.4 篡改金额（total_amount 改值）→ 验签失败 403
ALI_FORM_BAD=$(PRIV_FILE="$WORK/ali_priv.pem" PARAMS="{\"app_id\":\"2021smoke\",\"out_trade_no\":\"$ALI_NO\",\"trade_status\":\"TRADE_SUCCESS\",\"total_amount\":\"0.01\",\"notify_id\":\"ni2_${WTAG}_$$\"}" node "$WORK/alisign.js")
ALI_FORM_BAD=$(echo "$ALI_FORM_BAD" | sed 's/total_amount=0.01/total_amount=99.00/')
H12=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$B/api/v1/billing/webhook/alipay" \
  -H "Content-Type: application/x-www-form-urlencoded" --data "$ALI_FORM_BAD")
check "篡改通知金额被拒(403)" 403 "$H12"
echo "  [cleanup] 一次性租户 $WT_ID 由 trap 级联回收"

echo ""
echo "==== smoke_pay 结果: PASS=$PASS FAIL=$FAIL ===="
[ "$FAIL" = "0" ] || exit 1
