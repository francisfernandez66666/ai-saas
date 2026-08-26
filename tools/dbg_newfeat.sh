#!/bin/bash
# UAT 新增四项功能验证针：重复邮箱/弱密码/行业兜底/邀请记录
B=http://localhost:9090
export PGPASSWORD=dev123
Q(){ psql -h localhost -U ai_scrm -d ai_scrm -tAc "$1"; }
Q "UPDATE tenant_users SET must_change_password=false WHERE username='admin'" >/dev/null 2>&1
AT=$(curl -s -X POST $B/api/v1/auth/login -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"admin123"}' | python3 -c 'import sys,json;print(json.load(sys.stdin)["data"]["token"])')
AH="Authorization: Bearer $AT"
# 内测口径：关邮箱验证+放开IP限流（邮箱字段仍可提交用于校验）
curl -s -X PUT $B/api/v1/admin/config -H "$AH" -H "Content-Type: application/json" \
  -d '[{"category":"notify","key":"email_verify_enabled","value":"false"},{"category":"billing","key":"register_ip_daily_limit","value":"1000"},{"category":"billing","key":"register_ip_min_interval_sec","value":"0"}]' >/dev/null

TS=$(date +%s)
echo "== ① 弱密码拒绝 =="
R=$(curl -s -X POST $B/api/v1/tenant/signup -H "Content-Type: application/json" \
  -d "{\"company_name\":\"弱密\",\"code\":\"weak$((TS%99999))\",\"username\":\"wk$TS\",\"password\":\"abc123\"}")
echo "$R" | head -c 120; echo

echo "== ② 未知行业兜底 =="
C1="ind$((TS%99999))"
curl -s -X POST $B/api/v1/tenant/signup -H "Content-Type: application/json" \
  -d "{\"company_name\":\"未知行业户\",\"code\":\"$C1\",\"username\":\"in$TS\",\"password\":\"pass12345\",\"industry\":\"metaverse\"}" | python3 -c "import sys,json;print('注册code:',json.load(sys.stdin)['code'])"
TID=$(Q "SELECT id FROM tenants WHERE code='$C1'")
echo "industry=$(Q "SELECT industry FROM tenants WHERE id=$TID") (期望general)"
echo "-- 已知行业不回落 --"
C2="edu$((TS%99999))"
# 先造一个 education 行业包骨架（模拟已知行业）
Q "INSERT INTO industry_packs (code,name,industry,version,pack_level,status,file_path) VALUES ('education','教育行业包','education','1.0.0','industry','active','n/a') ON CONFLICT DO NOTHING" >/dev/null
curl -s -X POST $B/api/v1/tenant/signup -H "Content-Type: application/json" \
  -d "{\"company_name\":\"已知行业户\",\"code\":\"$C2\",\"username\":\"kn$TS\",\"password\":\"pass12345\",\"industry\":\"education\"}" >/dev/null
TID2=$(Q "SELECT id FROM tenants WHERE code='$C2'")
echo "industry=$(Q "SELECT industry FROM tenants WHERE id=$TID2") (期望education)"

echo "== ③ 重复邮箱注册拒绝 =="
EM="dup$TS@t.com"
C3="dup$((TS%99999))"
curl -s -X POST $B/api/v1/tenant/signup -H "Content-Type: application/json" \
  -d "{\"company_name\":\"首注\",\"code\":\"$C3\",\"username\":\"d1$TS\",\"password\":\"pass12345\",\"admin_email\":\"$EM\"}" >/dev/null
R=$(curl -s -o /dev/null -w "%{http_code}" -X POST $B/api/v1/tenant/signup -H "Content-Type: application/json" \
  -d "{\"company_name\":\"再注\",\"code\":\"dup2$((TS%99999))\",\"username\":\"d2$TS\",\"password\":\"pass12345\",\"admin_email\":\"$EM\"}")
echo "第二次同邮箱HTTP=$R (期望409)"

echo "== ④ 邀请记录接口与数据正确性 =="
# 甲=既有受邀链顶层；用甲码邀一个新租户并付费，然后查 records
A_ID=$(Q "SELECT id FROM tenants WHERE code LIKE 'uata%' ORDER BY id DESC LIMIT 1")
INV=$(Q "SELECT invite_code FROM tenants WHERE id=$A_ID")
C4="rec$((TS%99999))"
curl -s -X POST $B/api/v1/tenant/signup -H "Content-Type: application/json" \
  -d "{\"company_name\":\"记录验证户\",\"code\":\"$C4\",\"username\":\"rc$TS\",\"password\":\"uat123456\",\"ref\":\"$INV\"}" >/dev/null
D_ID=$(Q "SELECT id FROM tenants WHERE code='$C4'")
DTOK=$(curl -s -X POST $B/api/v1/auth/login -H "Content-Type: application/json" \
  -d "{\"tenant_code\":\"$C4\",\"username\":\"rc$TS\",\"password\":\"uat123456\"}" | python3 -c 'import sys,json;print(json.load(sys.stdin)["data"]["token"])')
PKG=$(Q "SELECT id FROM packages WHERE p_type='paid' LIMIT 1")
OID=$(curl -s -X POST $B/api/v1/billing/subscribe -H "Authorization: Bearer $DTOK" -H "Content-Type: application/json" -d "{\"package_id\":$PKG}" | python3 -c 'import sys,json;print(json.load(sys.stdin)["data"]["order"]["id"])')
curl -s -X POST $B/api/v1/billing/orders/mock-pay -H "Authorization: Bearer $DTOK" -H "Content-Type: application/json" -d "{\"order_id\":$OID}" >/dev/null
sleep 1
ATOKEN=$(curl -s -X POST $B/api/v1/auth/login -H "Content-Type: application/json" \
  -d "{\"tenant_code\":\"$(Q "SELECT code FROM tenants WHERE id=$A_ID")\",\"username\":\"$(Q "SELECT username FROM tenant_users WHERE tenant_id=$A_ID AND role='tenant_admin' LIMIT 1")\",\"password\":\"uat123456\"}" | python3 -c 'import sys,json;print(json.load(sys.stdin)["data"]["token"])')
echo "-- 甲视角 records 中 D 的记录 --"
curl -s "$B/api/v1/admin/referral/records" -H "Authorization: Bearer $ATOKEN" | python3 -c "
import sys,json
rows=json.load(sys.stdin)['data']['list']
r=[x for x in rows if x['tenant_id']==$D_ID]
print(r[0] if r else 'NOT FOUND')"
