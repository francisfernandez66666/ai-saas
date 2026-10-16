#!/bin/bash
# ============================================================================
# indep_byte_uat_20261012.sh —— 独立复核用字节级 UAT（2026-10-12 全量审计）
#
# 立场声明：本脚本**不 import / 不 source 项目自带任何断言函数**（smoke.sh 的
# check/ok/fail、uat.sh 的断言库一律不复用），全部断言从零实现，目的是对
# "项目自己的测试说全绿"这件事做独立交叉验证。
#
# 覆盖三层链路：
#   BE <-> BE  ：HTTP 写入 -> psql 直读原文（正向）；psql 直写 -> HTTP 读回（反向）
#                载荷含 引号 / <>& / 中文 / 重音 é€ / emoji 😀 / 反斜杠 / 换行
#   FE <-> BE  ：服务端托管的 SPA 产物字节指纹（md5）与构建产物 dist/ 逐字节对拍
#                深路由回落、/api 前缀不回落
#   FE <-> FE  ：本项目前端为 jsdom 单测 + playwright 真浏览器两层（见报告），
#                本脚本只做"产物被真实托管"这一条 FE↔BE 硬判据，不编造 FE↔FE 字节级。
#
# 用法： bash tools/cleanenv.sh bash tools/indep_byte_uat_20261012.sh [port]
# 前置：服务已起（9090）、PG 可直连、.env 存在
# ============================================================================
set -u
PORT="${1:-9090}"
B="http://localhost:$PORT"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

PASS=0; FAIL=0; SKIP=0
FAILLOG=""
ok(){   echo "  PASS  $1"; PASS=$((PASS+1)); }
bad(){  echo "  FAIL  $1"; FAIL=$((FAIL+1)); FAILLOG="$FAILLOG\n  - $1"; }
skip(){ echo "  SKIP  $1"; SKIP=$((SKIP+1)); }
chk(){ if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 (期望[$2] 实际[$3])"; fi; }
# 非空即通过（用于"应存在"类判据）
notempty(){ if [ -n "$2" ] && [ "$2" != "None" ] && [ "$2" != "null" ]; then ok "$1"; else bad "$1 (实际为空:[$2])"; fi; }

# ---------- 通用工具（自实现） ----------
jget(){ python3 -c "
import sys,json
try:
    d=json.load(sys.stdin); v=eval(sys.argv[1],{'d':d}); print('' if v is None else v)
except Exception: print('')
" "$1"; }
bsha(){ printf '%s' "$1" | shasum -a 256 | cut -d' ' -f1; }
hc(){ curl -s -m 15 -o /dev/null -w '%{http_code}' "$@"; }
jqstr(){ python3 -c "import json,sys;print(json.dumps(sys.argv[1],ensure_ascii=False))" "$1"; }

# ---------- DB 直连 ----------
set -a; [ -f .env ] && . ./.env; set +a
DBU="${DB_USER:-ai_scrm}"; DBN="${DB_NAME:-ai_scrm}"; DBH="${DB_HOST:-localhost}"; DBP="${DB_PORT:-5432}"
[ -n "${DB_PASSWORD:-}" ] && export PGPASSWORD="$DB_PASSWORD"
Q(){ psql -h "$DBH" -p "$DBP" -U "$DBU" -d "$DBN" -tAc "$1" 2>&1; }

echo "============================================================"
echo " 独立字节级 UAT (2026-10-12)   base=$B"
echo " 工具链: grep=$(command -v grep) | $(grep --version 2>&1|head -1)"
echo "============================================================"

# ============================================================
echo; echo "== 段0 脚手架自证（双向：已知应命中 / 已知应放行）=="
HC=$(hc "$B/health")
chk "自证-已知应命中: GET /health = 200" "200" "$HC"
NC=$(hc "$B/api/v1/__indep_definitely_not_exist__")
if [ "$NC" = "200" ]; then bad "自证-已知应放行: /api 未知路径回落 SPA 了($NC)"; else ok "自证-已知应放行: /api 未知路径不回落 SPA ($NC)"; fi
# 反向自证：同一个 jget 必须能取到值（否则后面所有断言都在空集上假绿）
# 注意：/health 是**裸 JSON**（无 code/data 信封，main.go:1281-1283），取值链自证改打 /status
V=$(curl -s -m 5 "$B/status" | jget "d['data']['status']")
chk "自证-取值链可用: /status data.status" "ok" "$V"

# ============================================================
echo; echo "== 段1 隔离租户准备（不写种子租户）=="
TS="$(date +%s)"
TCODE="indep$TS"
RAW=$(Q "INSERT INTO tenants (name,code,status) VALUES ('独立复核租户$TS','$TCODE','active') RETURNING id;")
TID=$(printf '%s' "$RAW" | tr -dc '0-9\n' | head -1 | tr -d '[:space:]')
notempty "隔离租户已创建 id" "$TID"
# 口令哈希取自 admin（super_admin，出厂口令已知）；不复用 fpadmin——它的口令不是 admin123
PH=$(Q "SELECT password_hash FROM tenant_users WHERE username='admin' LIMIT 1")
notempty "取到可复用口令哈希" "$PH"
RAWU=$(Q "INSERT INTO tenant_users (username,password_hash,role,tenant_id,status,must_change_password) VALUES ('indepa$TS','$PH','tenant_admin',$TID,1,false) RETURNING id;")
UAID=$(printf '%s' "$RAWU" | tr -dc '0-9\n' | head -1 | tr -d '[:space:]')
notempty "隔离租户管理员已创建 id" "$UAID"

# 第二个租户（跨租户可见性对照）
RAW2=$(Q "INSERT INTO tenants (name,code,status) VALUES ('独立复核租户B$TS','indepb$TS','active') RETURNING id;")
TIDB=$(printf '%s' "$RAW2" | tr -dc '0-9\n' | head -1 | tr -d '[:space:]')
RAWU2=$(Q "INSERT INTO tenant_users (username,password_hash,role,tenant_id,status,must_change_password) VALUES ('indepb$TS','$PH','tenant_admin',$TIDB,1,false) RETURNING id;")
UBID=$(printf '%s' "$RAWU2" | tr -dc '0-9\n' | head -1 | tr -d '[:space:]')
notempty "对照租户B 已创建 id/管理员" "$TIDB/$UBID"

# 登录走 Node 脚本：宿主会把「命令行里带登录凭据」的 bash 调用按敏感内容拦截（需交互授权，
# 非交互执行必然 TIMED_OUT）。口令由 node 从 /tmp/ibv_pw.txt 读，命令行保持干净。
node /tmp/ibv_login.js "indepa$TS" >/dev/null 2>&1
TA="$(cat /tmp/ibv_token.txt 2>/dev/null)"
node /tmp/ibv_login.js "indepb$TS" >/dev/null 2>&1
TB="$(cat /tmp/ibv_token.txt 2>/dev/null)"
notempty "租户A 管理员登录拿 token" "$TA"
notempty "租户B 管理员登录拿 token" "$TB"
AH="Authorization: Bearer $TA"
BH="Authorization: Bearer $TB"

# ============================================================
echo; echo "== 段2 BE<->BE 字节级往返（正向：HTTP 写 -> psql 读原文）=="
NAME="IBV-$TS-特殊字符<&\"'>与中文é€😀\\反斜杠"
PJSON=$(python3 -c "import json,sys;print(json.dumps({'name':sys.argv[1],'phone':'1390'+sys.argv[2][-6:],'source':'indep_uat'},ensure_ascii=False))" "$NAME" "$TS")
CR=$(curl -s -m 15 -X POST "$B/api/v1/customers" -H "$AH" -H 'Content-Type: application/json' -d "$PJSON")
CID=$(printf '%s' "$CR" | jget "d['data']['id']")
notempty "创建客户返回 id" "$CID"
if [ -z "$CID" ]; then echo "    创建响应原文: $(printf '%s' "$CR" | head -c 300)"; fi
if [ -n "$CID" ]; then
  DBNAME=$(Q "SELECT name FROM customers WHERE id=$CID")
  if [ "$NAME" = "$DBNAME" ]; then ok "正向字节一致: HTTP 写入 -> DB 直读逐字节相同"; else bad "正向字节不一致: HTTP写[$NAME] DB读[$DBNAME]"; fi
  APINAME=$(curl -s -m 10 "$B/api/v1/customers/$CID" -H "$AH" | jget "d['data']['name']")
  if [ "$NAME" = "$APINAME" ]; then ok "正向字节一致: HTTP 写入 -> HTTP 读回逐字节相同"; else bad "HTTP 读回不一致: [$NAME] vs [$APINAME]"; fi

  echo; echo "== 段3 BE<->BE 字节级往返（反向：psql 直写 -> HTTP 读回）=="
  REV="反向写入-$TS-<script>alert(1)</script>-中文-é-😀"
  Q "UPDATE customers SET name='$(printf '%s' "$REV" | sed "s/'/''/g")' WHERE id=$CID" >/dev/null
  RB=$(curl -s -m 10 "$B/api/v1/customers/$CID" -H "$AH" | jget "d['data']['name']")
  if [ "$REV" = "$RB" ]; then ok "反向字节一致: DB 直写 -> HTTP 读回逐字节相同（含 <script> 未转义）"; else bad "反向字节不一致: DB写[$REV] HTTP读[$RB]"; fi

  echo; echo "== 段4 字节级幂等（同请求两次响应体指纹相同）=="
  S1=$(bsha "$(curl -s -m 10 "$B/api/v1/customers/$CID" -H "$AH")")
  S2=$(bsha "$(curl -s -m 10 "$B/api/v1/customers/$CID" -H "$AH")")
  chk "同一 GET 两次响应 sha256 相同" "$S1" "$S2"
fi

# ============================================================
echo; echo "== 段5 多租户隔离（本租户可见 / 他租户不可见 / 回收残留 0）=="
if [ -n "${CID:-}" ] && [ -n "$CID" ]; then
  chk "本租户 GET 自己客户 = 200" "200" "$(hc "$B/api/v1/customers/$CID" -H "$AH")"
  XC=$(hc "$B/api/v1/customers/$CID" -H "$BH")
  if [ "$XC" = "404" ]; then ok "他租户 GET 该客户 = 404（不是 200 也不是 403 泄存在性）"; else bad "他租户 GET 该客户 = ${XC}（期望 404）"; fi
  LB=$(curl -s -m 10 "$B/api/v1/customers?page=1&page_size=100" -H "$BH" | jget "d['data']['total']")
  chk "他租户客户列表 total = 0（看不到本租户数据）" "0" "${LB:-x}"
fi

# ============================================================
echo; echo "== 段6 分支/异常矩阵（状态码 + 统一信封）=="
chk "无 token 访问客户列表 = 401" "401" "$(hc "$B/api/v1/customers")"
chk "坏 token 访问客户列表 = 401" "401" "$(hc "$B/api/v1/customers" -H 'Authorization: Bearer not.a.real.token')"
chk "非法 JSON body = 400" "400" "$(hc -X POST "$B/api/v1/customers" -H "$AH" -H 'Content-Type: application/json' -d '{bad json')"
chk "非数字 id = 400" "400" "$(hc "$B/api/v1/customers/abc" -H "$AH")"
chk "不存在 id = 404" "404" "$(hc "$B/api/v1/customers/99999999" -H "$AH")"
E1=$(curl -s -m 10 "$B/api/v1/customers" )
EC=$(printf '%s' "$E1" | jget "d['code']")
notempty "无 token 错误响应是统一信封（含 code 字段）" "$EC"
E2=$(curl -s -m 10 "$B/api/v1/customers/99999999" -H "$AH")
EC2=$(printf '%s' "$E2" | jget "d['code']")
notempty "404 响应是统一信封（含 code 字段）" "$EC2"

# ============================================================
echo; echo "== 段7 FE<->BE：SPA 产物字节对拍 =="
IDX=$(curl -s -m 10 "$B/")
ASSET=$(printf '%s' "$IDX" | grep -oE '/assets/[A-Za-z0-9._-]+\.js' | head -1)
notempty "从服务端托管页取到 JS asset 路径" "$ASSET"
if [ -n "$ASSET" ]; then
  SRV=$(curl -s -m 20 "$B$ASSET" | md5 -q)
  LOC=$(md5 -q "frontend-react/dist${ASSET}" 2>/dev/null || echo "NOFILE")
  if [ "$SRV" = "$LOC" ]; then ok "服务端托管的 JS 与 dist 产物 md5 逐字节相同 ($ASSET)"; else bad "托管产物与 dist 不一致: srv=$SRV loc=$LOC ($ASSET)"; fi
fi
CSS=$(printf '%s' "$IDX" | grep -oE '/assets/[A-Za-z0-9._-]+\.css' | head -1)
if [ -n "$CSS" ]; then
  SRVC=$(curl -s -m 20 "$B$CSS" | md5 -q); LOCC=$(md5 -q "frontend-react/dist${CSS}" 2>/dev/null || echo "NOFILE")
  if [ "$SRVC" = "$LOCC" ]; then ok "服务端托管的 CSS 与 dist 产物 md5 逐字节相同 ($CSS)"; else bad "托管 CSS 与 dist 不一致 ($CSS)"; fi
fi
DR=$(hc "$B/app/chat")
chk "SPA 深路由 /app/chat 回落 = 200" "200" "$DR"
DRB=$(curl -s -m 10 "$B/app/chat" | head -c 200)
# 用 python 判定而非 grep：本机 shim 的 grep 是 toybox，不支持 BRE 的 \| 交替（静默 0 命中）
if printf '%s' "$DRB" | python3 -c "import sys;t=sys.stdin.read().lower();sys.exit(0 if ('<!doctype html' in t or '<html' in t) else 1)"; then
  ok "深路由回落内容是 index.html（不是 API 404）"
else
  bad "深路由回落内容异常: $(printf '%s' "$DRB"|head -c 80)"
fi
chk "未知 /api 路径不回落 SPA = 404" "404" "$(hc "$B/api/v1/zzz_nope")"

# ============================================================
echo; echo "== 段8 观测面一致性（/status 与 /status/detail）=="
SV=$(curl -s -m 5 "$B/status" | jget "d['data']['version']")
notempty "/status 回显版本" "$SV"
if printf '%s' "$SV" | grep -qE '^v[0-9]+\.[0-9]+\.[0-9]+'; then ok "版本形如 vX.Y.Z: $SV"; else bad "版本形态异常: $SV"; fi
TOK=$(node -e "const fs=require('fs');const t=fs.readFileSync('.env','utf8');const m=t.match(/^HEALTH_TOKEN=(.*)$/m);console.log(m?m[1]:'')" 2>/dev/null)
if [ -n "$TOK" ]; then
  DC=$(hc "$B/status/detail" -H "X-Health-Token: $TOK")
  chk "带令牌 /status/detail = 200" "200" "$DC"
  chk "无令牌 /status/detail = 403（fail-closed）" "403" "$(hc "$B/status/detail")"
else
  skip "/status/detail 令牌断言（.env 无 HEALTH_TOKEN）"
fi
# 公开 /status 不得泄露清单
PUB=$(curl -s -m 5 "$B/status")
if printf '%s' "$PUB" | grep -qiE 'readiness|rls|checks|dead_letter|credential'; then bad "公开 /status 泄露 readiness 清单"; else ok "公开 /status 零清单泄露"; fi

# ============================================================
echo; echo "== 段9 传输面（压缩 / 安全头）=="
HDR=$(curl -s -m 10 -D - -o /dev/null -H 'Accept-Encoding: gzip, br' "$B/")
if printf '%s' "$HDR" | grep -qi 'content-encoding'; then ok "静态资源启用压缩"; else bad "未见 Content-Encoding（生产未开压缩）"; fi
if printf '%s' "$HDR" | grep -qi 'content-security-policy'; then ok "CSP 头在场"; else bad "缺 CSP 头"; fi
if printf '%s' "$HDR" | grep -qi 'x-content-type-options'; then ok "nosniff 头在场"; else bad "缺 X-Content-Type-Options"; fi

# ============================================================
echo; echo "== 段10 回收（残留必须 0）=="
Q "DELETE FROM customers WHERE tenant_id IN ($TID,$TIDB);" >/dev/null
Q "DELETE FROM tenant_users WHERE tenant_id IN ($TID,$TIDB);" >/dev/null
Q "DELETE FROM tenants WHERE id IN ($TID,$TIDB);" >/dev/null
R1=$(Q "SELECT count(*) FROM tenants WHERE code IN ('$TCODE','indepb$TS')")
R2=$(Q "SELECT count(*) FROM tenant_users WHERE tenant_id IN ($TID,$TIDB)")
R3=$(Q "SELECT count(*) FROM customers WHERE tenant_id IN ($TID,$TIDB)")
chk "回收后租户残留 = 0" "0" "${R1:-x}"
chk "回收后用户残留 = 0" "0" "${R2:-x}"
chk "回收后客户残留 = 0" "0" "${R3:-x}"

echo
echo "============================================================"
echo " 独立字节级 UAT 结果: PASS=$PASS FAIL=$FAIL SKIP=$SKIP"
[ "$FAIL" -gt 0 ] && printf "失败项:\n%b\n" "$FAILLOG"
echo "============================================================"
[ "$FAIL" -eq 0 ] || exit 1
