#!/bin/bash
# ============================================================
# 独立字节级 UAT 复核脚本（2026-09-27，第三方视角）
#
# 原则：不 import 项目既有断言函数，全部从零实现同样的验证目标。
# 覆盖：
#   A. BE↔BE   API 写→DB 直读 / DB 直写→API 读回，双向逐字节
#   B. FE↔BE   服务端托管产物 vs dist 构建产物 md5 逐文件
#   C. BE↔BE   分支码矩阵 401/403/404/400 与统一错误信封
#   D. BE↔BE   多租户隔离（另造两个一次性租户）
#   E. 运行时语义：SPA 回落、gzip 作用域、trace 回显、metrics 覆盖面
#   F. 清算不变量：新增 defects/非预期不会影响其它租户
#
# 用法：bash tools/cleanenv.sh ./tools/indep_byte_uat_20260927.sh [port]
# 注意：必须在 test_all 未运行时跑（共用同一套全局开关与测试库）
# ============================================================
set -u
PORT="${1:-9090}"
B="http://localhost:${PORT}"
# 与 tools/smoke.sh 等既有回归脚本同一口径：TEST_DB_URL 优先，缺省落到本地开发模板串
# （deploy_preflight 把该口令判为"模板值、公网机器上等于开门"，所以它只在开发机成立；
#   真正跑生产对账时必须显式导出 TEST_DB_URL）
DBURL="${TEST_DB_URL:-postgresql://ai_scrm:dev123@localhost/ai_scrm}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
Q(){ psql "$DBURL" -tAc "$1" 2>&1; }
PASS=0; FAIL=0; declare -a FAILED
ck(){ if [ "$2" = "$3" ]; then PASS=$((PASS+1)); echo "  PASS  $1";
  else FAIL=$((FAIL+1)); FAILED+=("§$SEC | $1 | 期望=[$2] 实际=[$3]"); echo "  FAIL  $1";
       printf '        期望=[%s]\n' "$2"; printf '        实际=[%s]\n' "$3"; fi }
# 断言前置自检：先证明「样本真的存在」，否则等式会在 0==0 上假绿
pre(){  # pre <名> <期望> <实际>
  if [ "$2" = "$3" ]; then echo "  PRE   $1 = $3（对照前提成立）";
  else echo "  PRE!  $1 前提不成立 期望=[$2] 实际=[$3]——后续该段判定不可信"; fi }
jget(){ python3 -c "
import sys,json
try:
    d=json.load(sys.stdin); v=eval(sys.argv[1],{'d':d}); print('' if v is None else v)
except Exception: print('')" "$1"; }
bsha(){ printf '%s' "$1" | shasum -a 256 | cut -d' ' -f1; }
hc(){ curl -s -o /dev/null -w '%{http_code}' "$@"; }
SEC="0"

echo "==== 独立字节级 UAT 复核 @ $B ===="

echo "---- §0 脚手架双向自证（已知应命中 / 已知应放行）----"
SEC="0"
ck "已知应命中：GET /status = 200" 200 "$(hc -m 5 "$B/status")"
ck "已知应放行：未知 /api 路径不得回落 SPA（≠200）" no200 \
   "$([ "$(hc -m 5 "$B/api/v1/__nope__$RANDOM")" != "200" ] && echo no200 || echo IS200)"
ck "已知应放行：伪造 Bearer 不得放行（401）" 401 \
   "$(hc -m 5 -H 'Authorization: Bearer faketoken.fake.fake' "$B/api/v1/auth/me")"

echo "---- §1 造两个一次性租户（A/B），全程不碰种子数据 ----"
SEC="1"
TS=$(date +%s)$RANDOM
newTenant(){ # newTenant <code> -> 输出 tenant_id
  local code="$1" raw tid
  raw=$(Q "INSERT INTO tenants (name, code, status) VALUES ('复核租户$code','$code',1) RETURNING id")
  tid=$(printf '%s' "$raw" | tr -dc '0-9\n' | head -1 | tr -d '[:space:]')
  printf '%s' "$tid"
}
TA="a$TS"; TB="b$TS"
TIDA=$(newTenant "$TA"); TIDB=$(newTenant "$TB")
pre "租户A 创建成功" nonempty "$([ -n "$TIDA" ] && echo nonempty || echo empty)"
pre "租户B 创建成功" nonempty "$([ -n "$TIDB" ] && echo nonempty || echo empty)"
[ -z "$TIDA" ] || [ -z "$TIDB" ] && { echo "  FATAL 租户创建失败"; exit 2; }
echo "  租户A id=$TIDA  租户B id=$TIDB"
mkuser(){ # mkuser <tenant_id> <username>
  Q "INSERT INTO tenant_users (username, password_hash, role, tenant_id, status, must_change_password)
     SELECT '$2', password_hash, 'tenant_admin', $1, 1, false FROM tenant_users WHERE username='admin' LIMIT 1" >/dev/null
}
mkuser "$TIDA" "ua$TS"; mkuser "$TIDB" "ub$TS"
login(){ # login <tenant_code> <username> -> token
  curl -s -X POST "$B/api/v1/auth/login" -H 'Content-Type: application/json' \
    -d "{\"tenant_code\":\"$1\",\"username\":\"$2\",\"password\":\"admin123\"}" | jget "d['data']['token']"
}
TOKA=$(login "$TA" "ua$TS"); TOKB=$(login "$TB" "ub$TS")
pre "A 租户管理员登录成功" nonempty "$([ -n "$TOKA" ] && echo nonempty || echo empty)"
pre "B 租户管理员登录成功" nonempty "$([ -n "$TOKB" ] && echo nonempty || echo empty)"
AHA="Authorization: Bearer $TOKA"; AHB="Authorization: Bearer $TOKB"
ck "A 身份归属租户A" "$TIDA" "$(curl -s "$B/api/v1/auth/me" -H "$AHA" | jget "d['data']['tenant_id']")"
ck "B 身份归属租户B" "$TIDB" "$(curl -s "$B/api/v1/auth/me" -H "$AHB" | jget "d['data']['tenant_id']")"

echo "---- §2 BE↔BE 正向：API 写入 → psql 直读，逐字节比对 ----"
SEC="2"
# 载荷刻意含：单双引号、尖括号与 &（XSS/HTML 转义风险）、反斜杠、中文、重音 é €、emoji、换行不能在 name 里故省略
NAME="IBV$TS 引号'双\"尖<>&反斜\\中文é€😀 end"
PJSON=$(python3 -c "import json,sys;print(json.dumps({'name':sys.argv[1],'phone':'1390000'$2},ensure_ascii=False))" "$NAME" "${TS: -4}")
CRE=$(curl -s -X POST "$B/api/v1/customers" -H "$AHA" -H 'Content-Type: application/json' -d "$PJSON")
CID=$(printf '%s' "$CRE" | jget "d['data']['id']")
pre "客户创建成功返回 id" nonempty "$([ -n "$CID" ] && echo nonempty || echo empty)"
DBNAME=$(Q "SELECT name FROM customers WHERE id=${CID:-0}")
ck "API→DB 客户名逐字节一致（引号/尖括号/中文/é€/emoji 全保留）" "$NAME" "$DBNAME"
ck "DB 里该行 tenant_id = 租户A（自动盖章正确）" "$TIDA" "$(Q "SELECT tenant_id FROM customers WHERE id=$CID")"
ck "API 回显与 DB 一致（三重同源）" "$DBNAME" "$(curl -s "$B/api/v1/customers/$CID" -H "$AHA" | jget "d['data']['name']")"
# 字节长度与十六进制，避开 shell 对不可见字符的干扰
ck "名称 UTF-8 字节数一致" "$(python3 -c "import sys;print(len(sys.argv[1].encode()))" "$NAME")" \
   "$(Q "SELECT octet_length(name) FROM customers WHERE id=$CID")"
ck "名称 SHA256（剔除 psql 尾随空格后逐字节）" \
   "$(python3 -c "import sys,hashlib;print(hashlib.sha256(sys.argv[1].encode()).hexdigest()[:16])" "$NAME")" \
   "$(python3 -c "
import subprocess,sys
out=subprocess.run(['psql','$DBURL','-tAc','SELECT encode(digest(name,\'sha256\'),\'hex\') FROM customers WHERE id=$CID'],capture_output=True,text=True).stdout.strip()
print(out[:16])")"

echo "---- §3 BE↔BE 反向：psql 直写 → API 读回，逐字节比对 ----"
SEC="3"
REV="REV$TS 反向中文é€😀<script>alert(1)</script>"
REV_ESC=$(printf '%s' "$REV" | sed "s/'/''/g")
Q "UPDATE customers SET name='$REV_ESC' WHERE id=$CID" >/dev/null
ck "DB→API 反向逐字节一致（含 <script> 原样回显，未转义也未吞字符）" "$REV" \
   "$(curl -s "$B/api/v1/customers/$CID" -H "$AHA" | jget "d['data']['name']")"
ck "API 详情实体 hashCode=0" 0 "$(curl -s "$B/api/v1/customers/$CID" -H "$AHA" | jget "d['code']")"

echo "---- §4 字节级幂等：同一 GET 两次响应体完全一致 ----"
SEC="4"
for ep in "/status" "/api/v1/plans" "/api/v1/openapi/spec"; do
  a=$(bsha "$(curl -s -m 10 "$B$ep")"); b=$(bsha "$(curl -s -m 10 "$B$ep")")
  ck "GET $ep 两次响应字节一致" "$a" "$b"
done

echo "---- §5 FE↔BE：服务端托管产物 vs dist 构建产物 md5 ----"
SEC="5"
IDX_SERVED=$(curl -s -m 8 "$B/")
ck "根路径返回 SPA 首页（含 app root 容器）" nonempty \
   "$(printf '%s' "$IDX_SERVED" | /usr/bin/grep -c 'id="\?root\|<div id=' >/dev/null 2>&1; printf '%s' "$IDX_SERVED" | python3 -c "
import sys,re
s=sys.stdin.read()
print('nonempty' if re.search(r'<(div|main)[^>]*id=[\"\x27]?root', s) else 'NOROOT')")"
ck "首页与 dist/index.html 逐字节一致" "$(md5 -q "$ROOT/frontend-react/dist/index.html")" \
   "$(printf '%s' "$IDX_SERVED" | md5 -q)"
N_ASSET=0; OK_ASSET=0
for p in $(printf '%s' "$IDX_SERVED" | /usr/bin/grep -oE '/assets/[^"]+'); do
  base=$(basename "$p"); N_ASSET=$((N_ASSET+1))
  curl -s -m 20 "$B$p" -o "/tmp/ibu_$base" 2>/dev/null
  srv=$(md5 -q "/tmp/ibu_$base" 2>/dev/null); dis=$(md5 -q "$ROOT/frontend-react/dist/assets/$base" 2>/dev/null)
  if [ "$srv" = "$dis" ] && [ -n "$srv" ]; then OK_ASSET=$((OK_ASSET+1)); else
    FAIL=$((FAIL+1)); FAILED+=("§5 | 产物 $base 字节不一致 服务端=$srv dist=$dis"); echo "  FAIL  产物 $base 字节不一致"; fi
done
pre "首页引用的 asset 数量 > 0" gt0 "$([ "$N_ASSET" -gt 0 ] && echo gt0 || echo zero)"
ck "全部托管 asset 与 dist 字节一致（${OK_ASSET}/${N_ASSET}）" "$N_ASSET" "$OK_ASSET"
# 深路由回落
ck "SPA 深路由 /app/some/deep/route 回落首页（200）" 200 "$(hc -m 8 "$B/app/some/deep/route")"
ck "SPA 深路由返回体与首页同源" "$(md5 -q "$ROOT/frontend-react/dist/index.html")" "$(curl -s -m 8 "$B/app/some/deep/route" | md5 -q)"

echo "---- §6 路由/鉴权分支码矩阵（字节级边界）----"
SEC="6"
ck "无 token 访问 /auth/me = 401" 401 "$(hc -m 5 "$B/api/v1/auth/me")"
ck "坏签名 token = 401" 401 "$(hc -m 5 -H 'Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.eyJ1aWQiOjF9.bad' "$B/api/v1/auth/me")"
ck "tenant_admin 打超管面 = 403" 403 "$(hc -m 5 -H "$AHA" "$B/api/v1/super/tenants")"
ck "不存在客户 id = 404" 404 "$(hc -m 5 -H "$AHA" "$B/api/v1/customers/999999999")"
ck "非法 JSON body = 400" 400 "$(hc -m 5 -X POST -H "$AHA" -H 'Content-Type: application/json' -d '{bad json' "$B/api/v1/customers")"
ck "未知 /api 路径不回落 SPA（返回 404 且无 HTML）" 404 "$(hc -m 5 "$B/api/v1/definitely_not_exist_$$")"
NONAPI_BODY=$(curl -s -m 5 "$B/api/v1/definitely_not_exist_$$")
ck "未知 /api 路径响应体为空（不是 HTML 首页）" empty "$([ -z "$NONAPI_BODY" ] && echo empty || echo nonempty)"

echo "---- §7 统一错误信封率（sampled）----"
SEC="7"
declare -a ERRS=()
SAMPS=0; ENVS=0
probe(){ # probe <期望码> <url> [extra header]
  local body code
  body=$(curl -s -m 8 -w '\n%{http_code}' "${@:2}")
  code=$(printf '%s' "$body" | tail -1)
  body=$(printf '%s' "$body" | head -n -1)
  SAMPS=$((SAMPS+1))
  if python3 -c "
import sys,json
try:
  d=json.loads(sys.argv[1]); sys.exit(0 if 'code' in d else 1)
except Exception: sys.exit(1)" "$body" 2>/dev/null; then ENVS=$((ENVS+1)); else
    echo "  NOTE  非信封响应：HTTP $code body=$(printf '%s' "$body" | head -c 80)"; fi
  return 0
}
probe x "$B/api/v1/auth/me"
probe x -H 'Authorization: Bearer bad.bad.bad' "$B/api/v1/auth/me"
probe x -H "$AHA" "$B/api/v1/super/tenants"
probe x -H "$AHA" "$B/api/v1/customers/999999999"
probe x -X POST -H "$AHA" -H 'Content-Type: application/json' -d '{oops' "$B/api/v1/customers"
probe x -H "$AHA" "$B/api/v1/customers/abc"
ck "错误响应统一信封率 = 100%（${ENVS}/${SAMPS}）" "$SAMPS" "$ENVS"

echo "---- §8 多租户隔离 ----"
SEC="8"
ck "租户A 可见自己刚建的客户" 200 "$(hc -m 5 -H "$AHA" "$B/api/v1/customers/$CID")"
ck "租户B 访问同一客户 id = 404（不可见）" 404 "$(hc -m 5 -H "$AHB" "$B/api/v1/customers/$CID")"
LISTA=$(curl -s -m 8 -H "$AHA" "$B/api/v1/customers?page=1&page_size=100")
CNT_A=$(printf '%s' "$LISTA" | python3 -c "
import sys,json
try:
  d=json.load(sys.stdin)
  def walk(o):
    if isinstance(o,dict):
      if 'list' in o and isinstance(o['list'],list): return sum(1 for x in o['list'] if str(x.get('id'))=='$CID')
      if 'items' in o and isinstance(o['items'],list): return sum(1 for x in o['items'] if str(x.get('id'))=='$CID')
      for v in o.values():
        r=walk(v)
        if r: return r
    return 0
  print(walk(d))
except Exception: print('ERR')")
ck "租户A 列表里能检索到该客户（count=1）" 1 "$CNT_A"
ck "租户B 列表里检索不到该客户（count=0）" 0 \
  "$(curl -s -m 8 -H "$AHB" "$B/api/v1/customers?page=1&page_size=100" | python3 -c "
import sys,json
try:
  d=json.load(sys.stdin); s=json.dumps(d)
  print(1 if '$CID' in s else 0)
except Exception: print('ERR')")"
# 跨租户写：B 改 A 的客户
ck "租户B PUT 修改租户A 客户 = 404（写隔离）" 404 \
   "$(hc -m 8 -X PUT -H "$AHB" -H 'Content-Type: application/json' -d '{"name":"越权写入"}' "$B/api/v1/customers/$CID")"
ck "越权写入未污染数据（名称仍是原样）" "$REV" "$(Q "SELECT name FROM customers WHERE id=$CID")"
ck "租户B DELETE 租户A 客户 = 404（删隔离）" 404 "$(hc -m 8 -X DELETE -H "$AHB" "$B/api/v1/customers/$CID")"

echo "---- §9 运行时语义：trace 回显 / gzip 作用域 / metrics 覆盖面 ----"
SEC="9"
T_IN="ibv-trace-$(date +%s)"
T_OUT=$(curl -s -m 8 -D - -o /dev/null -H "X-Trace-ID: $T_IN" "$B/status" | /usr/bin/grep -i '^x-trace-id' | tr -d '\r' | cut -d' ' -f2)
ck "自定义 X-Trace-ID 被复用并回显" "$T_IN" "$T_OUT"
ck "未下发 trace 时服务端生成 16 位 hex" ok \
   "$(curl -s -m 8 -D - -o /dev/null "$B/status" | /usr/bin/grep -i '^x-trace-id' | tr -d '\r' | cut -d' ' -f2 | python3 -c "
import sys,re; v=sys.stdin.read().strip(); print('ok' if re.fullmatch(r'[0-9a-f]{16}',v) else v)")"
# gzip 作用域
ASSET_PATH=$(printf '%s' "$IDX_SERVED" | /usr/bin/grep -oE '/assets/[^"]+\.js' | head -1)
ck "静态资源支持 gzip（Content-Encoding 存在）" nonempty \
   "$(curl -s -m 10 -D - -o /dev/null -H 'Accept-Encoding: gzip, br' "$B$ASSET_PATH" | /usr/bin/grep -ci '^content-encoding' | python3 -c "
import sys; v=int(sys.stdin.read().strip() or 0); print('nonempty' if v>0 else 'ABSENT')")"
ck "API 响应不套 gzip（P2-9 契约）" ABSENT \
   "$(curl -s -m 8 -D - -o /dev/null -H 'Accept-Encoding: gzip, br' -H "$AHA" "$B/api/v1/customers/1" | /usr/bin/grep -ci '^content-encoding' | python3 -c "
import sys; v=int(sys.stdin.read().strip() or 0); print('ABSENT' if v==0 else 'PRESENT')")"
# metrics 覆盖面（验证 gin 中间件注册时机语义）
MET(){ curl -s -m 8 "$B/metrics" | /usr/bin/grep -E '^ai_scrm_http_requests_total' | awk '{print $2}'; }
pre "metrics 端点可读（loopback 放行）" nonempty "$([ -n "$(MET)" ] && echo nonempty || echo empty)"
M0=$(MET)
for i in 1 2 3 4 5; do curl -s -m 8 -o /dev/null "$B/api/v1/plans"; done
M1=$(MET)
for i in 1 2 3 4 5; do curl -s -m 8 -o /dev/null "$B/app/deep/page/$RANDOM"; done
M2=$(MET)
ck "API 路径被计入请求指标（5 次 → 增量 ≥5）" yes "$([ "$((M1-M0))" -ge 5 ] && echo yes || echo "no: +$((M1-M0))")"
ck "SPA 深路由被计入请求指标（5 次 → 增量 ≥5）" yes "$([ "$((M2-M1))" -ge 5 ] && echo yes || echo "no: +$((M2-M1))（SPA 页面访问对监控不可见）")"

echo "---- §10 契约面：OpenAPI spec 与实际路由规模对拍 ----"
SEC="10"
SPEC_N=$(curl -s -m 15 "$B/api/v1/openapi/spec" | python3 -c "
import sys,json
try:
  d=json.load(sys.stdin); print(len(d.get('paths',{})))
except Exception: print('ERR')")
pre "OpenAPI spec 可解析出 paths" numeric "$(printf '%s' "$SPEC_N" | python3 -c "
import sys; v=sys.stdin.read().strip(); print('numeric' if v.isdigit() else v)")"
echo "  INFO  spec 声明 path 组数 = $SPEC_N"

echo "---- §11 清算：回收一次性租户并断言零残留 ----"
SEC="11"
Q "DELETE FROM customers WHERE tenant_id IN ($TIDA,$TIDB)" >/dev/null 2>&1
Q "DELETE FROM tenant_users WHERE tenant_id IN ($TIDA,$TIDB)" >/dev/null 2>&1
Q "DELETE FROM tenants WHERE id IN ($TIDA,$TIDB)" >/dev/null 2>&1
ck "租户回收后 customers 残留 = 0" 0 "$(Q "SELECT count(*) FROM customers WHERE tenant_id IN ($TIDA,$TIDB)")"
ck "租户回收后 tenant_users 残留 = 0" 0 "$(Q "SELECT count(*) FROM tenant_users WHERE tenant_id IN ($TIDA,$TIDB)")"
ck "租户回收后 tenants 残留 = 0" 0 "$(Q "SELECT count(*) FROM tenants WHERE id IN ($TIDA,$TIDB)")"

echo
echo "=========================================================="
echo " 独立字节级 UAT：PASS=$PASS FAIL=$FAIL"
if [ "$FAIL" -gt 0 ]; then
  echo " ---- 失败明细 ----"
  for f in "${FAILED[@]}"; do echo "  * $f"; done
fi
echo "=========================================================="
[ "$FAIL" -eq 0 ] || exit 1
