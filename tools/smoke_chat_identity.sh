#!/bin/bash
# ============================================================
# G-3 /chat/history 身份缺口负向断言
# 覆盖：携带他人 customer_id 且 visitor_key 不匹配 → 403
#       无 visitor_key 匿名访问他人客户 → 403
#       带正确 visitor_key → 200
# 用法: ./tools/smoke_chat_identity.sh 9090
# ============================================================
B="http://localhost:${1:-9090}"
PASS=0; FAIL=0
check(){ if [ "$2" = "$3" ]; then echo "  PASS  $1 ($3)"; PASS=$((PASS+1)); else echo "  FAIL  $1 期望=$2 实际=$3"; FAIL=$((FAIL+1)); fi }

jsonget(){ python3 -c "import sys,json;d=json.load(sys.stdin);print(eval('d'+sys.argv[1]))" "$1" 2>/dev/null; }

echo "== G-3 /chat/history 身份缺口负向断言 @ $B =="

# ---- 0. 登录 ----
AT=$(curl -s -X POST "$B/api/v1/auth/login" -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"admin123"}' | jsonget "['data']['token']")
[ -n "$AT" ] && check "超管登录" y y || { check "超管登录" y n; exit 1; }

# ---- 1. 创建两个客户（通过 chat/test 产生客户） ----
echo ""
echo "-- 创建测试客户 --"
R1=$(curl -s -X POST "$B/api/v1/chat/test" -H "Content-Type: application/json" \
  -d '{"content":"客户A消息"}')
CID_A=$(echo "$R1" | jsonget "['data']['customer_id']")
echo "  客户A ID: $CID_A"

R2=$(curl -s -X POST "$B/api/v1/chat/test" -H "Content-Type: application/json" \
  -d '{"content":"客户B消息"}')
CID_B=$(echo "$R2" | jsonget "['data']['customer_id']")
echo "  客户B ID: $CID_B"

# 获取客户A的 visitor_key
VK_A=$(curl -s "$B/api/v1/chat/history?customer_id=$CID_A" \
  -H "Authorization: Bearer $AT" | jsonget "['data'][0]['visitor_key']")
# 如果 history 没返回 visitor_key，用超管查
if [ -z "$VK_A" ] || [ "$VK_A" = "None" ]; then
  VK_A=$(curl -s "$B/api/v1/org/users" -H "Authorization: Bearer $AT" | python3 -c "print('')" 2>/dev/null)
  # 通过 guest 接口获取
  VK_A=$(curl -s -X POST "$B/api/v1/chat/guest" -H "Content-Type: application/json" \
    -d "{\"customer_id\":$CID_A}" | jsonget "['data']['visitor_key']")
fi
echo "  客户A visitor_key: ${VK_A:0:16}..."

# ---- 2. 带正确 visitor_key → 200 ----
echo ""
echo "-- 正确 visitor_key 访问 --"
R=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/chat/history?customer_id=$CID_A" \
  -H "X-Visitor-Key: $VK_A")
check "正确VK访问自身→200" 200 "$R"

# ---- 3. 携带他人 customer_id + 错误 visitor_key → 403 ----
echo ""
echo "-- 错误 visitor_key 越权 --"
R=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/chat/history?customer_id=$CID_A" \
  -H "X-Visitor-Key: fake_key_abcdef123456")
check "错误VK访问他人→403" 403 "$R"

# ---- 4. 无 visitor_key 匿名访问 → 403 ----
echo ""
echo "-- 无 visitor_key 匿名越权 --"
R=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/chat/history?customer_id=$CID_A")
check "无VK匿名访问→403" 403 "$R"

# ---- 5. 正确 VK 访问自身 → 200 ----
echo ""
echo "-- 正确 VK 访问自身 --"
R=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/chat/history?customer_id=$CID_B" \
  -H "X-Visitor-Key: $VK_A")
# 注意：VK_A 是客户A的 key，客户B 应该拒绝
R2=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/chat/history?customer_id=$CID_B" \
  -H "X-Visitor-Key: $VK_A")
check "A的VK访问B→403" 403 "$R2"

# ---- 6. 登录态访问（顾问/管理员） → 200 ----
echo ""
echo "-- 登录态访问 --"
R=$(curl -s -o /dev/null -w "%{http_code}" "$B/api/v1/chat/history?customer_id=$CID_A" \
  -H "Authorization: Bearer $AT")
check "超管登录态访问任意客户→200" 200 "$R"

echo ""
echo "==== G-3 结果: PASS=$PASS FAIL=$FAIL ===="
[ "$FAIL" = "0" ] || exit 1
