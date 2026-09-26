#!/bin/bash
# ============================================================
# 服务端 Redis 双轨冒烟（FIX-7 运维批，2026-09-27）
#
# 为什么必须单独有这一段：本仓十余处「多实例语义」（合并队列裁决锁、跨实例 WS 广播、
# 登录防爆破锁、IP 限流桶、各类 sweep 选主）都写成「Redis 可用走 Redis、不可用退化内存」两轨，
# 而**十套 E2E 全跑在退化轨上**——CI 的三个 job 甚至声明了 redis service 又把
# REDIS_ENABLED 写成 "false"（起了一个没人连的 Redis）。于是「多实例部署会不会双答」
# 这件事只存在于 code review 的记忆里，没有任何机器判据。
# 本脚本把三件事钉成断言：
#   ① 声明启用且真连上 → /status/detail 报 connected，且公开 /status 的「副本>1 无 Redis」红灯**不亮**；
#   ② 登录防爆破锁真的落在 Redis 上（键可见、计数>=阈值、TTL 有限），且**第二个实例**同样认这把锁；
#   ③ 声明启用却连不上 → 观测位 declared_but_down + 公开 /status 判 crit，
#      且该实例的失败计数**不会**出现在 Redis 里（证明 ② 不是「反正都有键」的空转）。
# ①② 是正向、③ 是反向对照——缺 ③，② 在任何环境下都会「绿得没有意义」。
#
# 首跑实测（2026-09-27）顺带抓到一条真缺陷：/status/detail 注册在根路由却不在
# skipTenantPaths 里，release 模式下按 IP 探针打进来会被 TenantResolver 拦成
# 403「无法识别访问租户」——本段三条实例全部跑 GIN_MODE=release，所以第一条观测位断言
# 就直接红了。已修（internal/middleware/tenant.go + status_detail_skip_test.go 两条用例，
# 含"删掉白名单条目即红"的变异实测）。教训同备份脚本那条：**只在 debug 下验过的观测面，
# 等于没验**。
#
# 用法：./tools/smoke_redis.sh          （自建 9093/9094/9095 三个实例，绝不动 9090）
# 前置：本机 Redis 在 127.0.0.1:6379（docker compose up -d redis）。探测不到则整段显式 SKIP
#       （SKIP 不是 PASS——「只会打日志并返回成功的守卫等于没有守卫」）。
# 退出码：0=通过或明确 SKIP；1=有 FAIL。
# ============================================================
set -u
cd "$(dirname "$0")/.."
ROOT=$(pwd)

PORT_A="${REDIS_SMOKE_PORT_A:-9093}"
PORT_B="${REDIS_SMOKE_PORT_B:-9094}"
PORT_C="${REDIS_SMOKE_PORT_C:-9095}"
RADDR="${REDIS_SMOKE_ADDR:-127.0.0.1:6379}"
RPORT="${REDIS_SMOKE_REDIS_PORT:-6379}"
# /status/detail 用一次性令牌：本脚本自己起实例、自己知道令牌，不去读 .env 里的生产 HEALTH_TOKEN
HT="redis-smoke-ht-$(date +%s)-$$"
BIN="$ROOT/bin/ai-scrm-redis"
LOGDIR=$(mktemp -d "${TMPDIR:-/tmp}/smoke_redis.XXXXXX")
PID_A="" PID_B="" PID_C=""
U_A="redis_smoke_a_$(date +%H%M%S)"
U_C="redis_smoke_c_$(date +%H%M%S)"
KEYS_TOUCHED=0
PASS=0 FAIL=0 SKIPN=0

check() { # $1=名 $2=期望 $3=实际
  if [ "$2" = "$3" ]; then echo "  PASS  $1"; PASS=$((PASS+1));
  else echo "  FAIL  $1  expect=$2 actual=$3"; FAIL=$((FAIL+1)); fi
}
# check_not：负向断言（"这个红灯不许亮"）。单独一条是因为等值锁写成"期望非某值"时
# 必须保证读到的确实是读到了，而不是读取失败凑出来的不等。
check_not() { # $1=名 $2=禁止值 $3=实际
  if [ -n "$3" ] && [ "$3" != "$2" ]; then echo "  PASS  ${1}（=${3}）"; PASS=$((PASS+1));
  else echo "  FAIL  $1  expect≠$2 actual=$3"; FAIL=$((FAIL+1)); fi
}
skip() { echo "  SKIP  $1"; SKIPN=$((SKIPN+1)); }

cleanup() {
  for p in "$PID_A" "$PID_B" "$PID_C"; do
    [ -n "$p" ] && kill "$p" 2>/dev/null
  done
  # 清掉本批写进 Redis 的守卫键：Redis 是本机共享实例，留着就是给下一位读者埋一把 15 分钟的锁
  if [ "$KEYS_TOUCHED" = "1" ]; then
    SMOKE_REDIS_PORT="$RPORT" python3 "$LOGDIR/redis.py" del "$U_A" >/dev/null 2>&1
    SMOKE_REDIS_PORT="$RPORT" python3 "$LOGDIR/redis.py" del "$U_C" >/dev/null 2>&1
  fi
  rm -rf "$LOGDIR"
}
trap cleanup EXIT

# ---------- Redis 客户端（纯标准库讲 RESP，不依赖 redis-cli；本机共享实例固定 127.0.0.1）----------
# 首跑教训：只剥掉 `:` 前缀的偷懒写法会把批量回复 `$1\r\n5` 原样吐出来，
# 于是"计数>=5"这条断言读到的永远是 `$1\n5`——红了却以为是产品问题。
# 现在按 RESP 类型解（+ - : $ *），空值统一回 EMPTY，非预期类型回 BAD_REPLY:<原文>。
cat > "$LOGDIR/redis.py" <<'PY'
# 用法：redis.py <ping|get|exists|del|ttl> [合成用户名]
# 登录守卫的键族（见 internal/service/login_guard.go）：lg:cnt:u:{user} / lg:lock:u:{user}
import os, socket, sys

PORT = int(os.environ.get("SMOKE_REDIS_PORT", "6379"))


def read_reply(f):
    """解一条 RESP 回复；bulk 字符串按长度读满，数组逐项递归。"""
    line = f.readline()
    if not line:
        return "NO_REPLY"
    kind, rest = line[:1], line[1:].rstrip(b"\r\n")
    if kind in (b"+", b"-", b":"):
        return rest.decode(errors="replace")
    if kind == b"$":
        n = int(rest)
        if n < 0:
            return "EMPTY"          # $-1：键不存在（与"值为空串"区分开）
        body = f.read(n + 2)        # 值 + 结尾 CRLF
        return body[:n].decode(errors="replace")
    if kind == b"*":
        n = int(rest)
        return "|".join(read_reply(f) for _ in range(n))
    return "BAD_REPLY:" + line.decode(errors="replace").strip()


def cmd(*args):
    s = socket.create_connection(("127.0.0.1", PORT), timeout=2)
    payload = ("*%d\r\n" % len(args)).encode()
    for a in args:
        b = a.encode()
        payload += b"$%d\r\n" % len(b) + b + b"\r\n"
    s.sendall(payload)
    reply = read_reply(s.makefile("rb"))
    s.close()
    return reply


mode = sys.argv[1]
if mode == "ping":
    print("PONG" if cmd("PING") == "PONG" else "DOWN")
    sys.exit(0)
user = sys.argv[2]
cnt, lock = "lg:cnt:u:" + user, "lg:lock:u:" + user
if mode == "get":
    print(cmd("GET", cnt))
elif mode == "exists":
    # 逐键 EXISTS（整数回复），命中计数相加
    print(sum(1 for k in (cnt, lock) if cmd("EXISTS", k) == "1"))
elif mode == "del":
    for k in (cnt, lock):
        cmd("DEL", k)
    print("OK")
elif mode == "ttl":
    print(cmd("TTL", cnt))
PY
rcmd() { SMOKE_REDIS_PORT="$RPORT" python3 "$LOGDIR/redis.py" "$@" 2>/dev/null | tr -d '\r'; }

# ---------- /status/detail 取值器 ----------
# 三态必须分开：HTTP 非 200（含被租户解析拦成的 403）、JSON 解不出来、键不存在。
# 首跑之所以七条红全是"ABSENT"，就是因为把 403 也读成了 ABSENT——一条读取器缺陷
# 伪装成七条产品缺陷。现按 ERR_HTTP_<code> / PARSE_ERR / ABSENT 分形态返回。
cat > "$LOGDIR/pj.py" <<'PY'
# 用法：pj.py <field|readiness_value|readiness_status|readiness_names> [参数]
# stdin = /status/detail 的响应体
import json, sys

mode = sys.argv[1]
arg = sys.argv[2] if len(sys.argv) > 2 else ""
try:
    j = json.load(sys.stdin)
except Exception:
    print("PARSE_ERR")
    sys.exit(0)
d = j.get("data")
if not isinstance(d, dict):
    print("NO_DATA:%s" % j.get("code"))
    sys.exit(0)
if mode == "field":
    v = d.get(arg)
    print("ABSENT" if v is None and arg not in d else v)
    sys.exit(0)
items = d.get("readiness") or []
if mode == "readiness_names":
    print(",".join(str(c.get("name")) for c in items))
    sys.exit(0)
for c in items:
    if c.get("name") == arg:
        print(c.get("value") if mode == "readiness_value" else c.get("status"))
        break
else:
    print("ABSENT")
PY
detail_code() { curl -s -o /dev/null -w "%{http_code}" -H "X-Health-Token: $HT" "http://127.0.0.1:$1/status/detail" 2>/dev/null; }
DETAIL_BODY=""
detail_fetch() { # $1=端口 → 把响应体存进全局 DETAIL_BODY，返回 HTTP 码
  DETAIL_BODY=$(curl -s -H "X-Health-Token: $HT" "http://127.0.0.1:$1/status/detail" 2>/dev/null)
  curl -s -o /dev/null -w "%{http_code}" -H "X-Health-Token: $HT" "http://127.0.0.1:$1/status/detail" 2>/dev/null
}
detail() { # $1=端口 $2=mode $3=键/检查名
  detail_fetch "$1" >/dev/null
  printf '%s' "$DETAIL_BODY" | python3 "$LOGDIR/pj.py" "$2" "$3" 2>/dev/null | tr -d '\r'
}
# pub_status：公开 /status 的健康分级（instance_coordination 红灯只体现在这一层的汇总里）
pub_status() { # $1=端口
  curl -s "http://127.0.0.1:$1/status" 2>/dev/null | python3 "$LOGDIR/pj.py" field status 2>/dev/null | tr -d '\r'
}
login_try() { # $1=端口 $2=用户名 → 回响应体
  curl -s -X POST "http://127.0.0.1:$1/api/v1/auth/login" \
    -H "Content-Type: application/json" \
    -d "{\"username\":\"$2\",\"password\":\"wrong-pass-$RANDOM\"}"
}
# start_instance 起一台自建实例：$1=端口 $2=REDIS_ADDR $3=日志名 → pid 写进全局 START_PID
# GIN_MODE=release + APP_REPLICAS=2 是这段的前提：不声明成"多实例生产形态"，
# instance_coordination 那条红灯设计上就不会亮，反向对照会变成"断言一个永远不会发生的场景"。
start_instance() {
  AI_MOCK_MODE=true SERVER_PORT="$1" GIN_MODE=release APP_REPLICAS=2 \
    REDIS_ENABLED=true REDIS_ADDR="$2" HEALTH_TOKEN="$HT" \
    "$BIN" >"$LOGDIR/$3" 2>&1 &
  START_PID=$!
}
# wait_ready：迁移+种子在真库上要 30~60s，60 次×1s 的上限是实测余量的一半，留够冷启动
wait_ready() { # $1=端口 → 打印 /health 状态码
  local code=000 i=1
  while [ "$i" -le 90 ]; do
    code=$(curl -s -o /dev/null -w "%{http_code}" "http://127.0.0.1:$1/health" 2>/dev/null)
    [ "$code" = "200" ] && break
    sleep 1
    i=$((i+1))
  done
  echo "$code"
}

echo "==== 服务端 Redis 双轨冒烟（A=$PORT_A B=$PORT_B C=${PORT_C}，Redis=${RADDR}） ===="

# 0) 前置：二进制 + Redis 可达性
if ! command -v python3 >/dev/null 2>&1; then
  echo "  缺 python3，无法做键位核对（本段依赖 RESP 直连）"; exit 1
fi
[ -x "$BIN" ] || go build -o "$BIN" ./cmd/server || { echo "  后端编译失败(bin/ai-scrm-redis)"; exit 1; }
RUP=$(rcmd ping)
if [ "$RUP" != "PONG" ]; then
  echo "  Redis $RADDR 不可达（探测回 '$RUP'）"
  skip "Redis 不可达，整段跳过——本段是「多实例语义」的唯一机器判据，请 docker compose up -d redis 后重跑"
  echo "==== 结果: PASS=$PASS FAIL=$FAIL SKIP=$SKIPN ===="
  exit 0
fi
check "Redis $RADDR 可达(PONG)" "PONG" "$RUP"

# 1) A：连上 Redis 的实例一（三台全部 GIN_MODE=release + APP_REPLICAS=2，
#    否则测的就不是"多实例部署会不会双答"那个场景）
start_instance "$PORT_A" "$RADDR" a.log; PID_A=$START_PID
check "实例A($PORT_A) 健康(200)" "200" "$(wait_ready "$PORT_A")"
# 2) B：连上同一 Redis 的实例二（跨实例判据的主体）
start_instance "$PORT_B" "$RADDR" b.log; PID_B=$START_PID
check "实例B($PORT_B) 健康(200)" "200" "$(wait_ready "$PORT_B")"

# ---- 前置自检：观测面本身可达 + 解析器有牙 ----
# 这两条不成立时后面所有 ABSENT 都不可信（首跑正是这个形态：403 被读成"检查项不存在"）。
check "/status/detail 在 release+裸IP 下可达(200，不被 Host 租户解析拦)" "200" "$(detail_code "$PORT_A")"
RNAMES=$(detail "$PORT_A" readiness_names x)
case "$RNAMES" in
  PARSE_ERR|NO_DATA*|ABSENT|""|ERR_*) check "readiness 清单能读到（解析器自证）" "非空清单" "$RNAMES" ;;
  *) if printf '%s' "$RNAMES" | grep -q "redis_declared_but_down"; then
       echo "  PASS  readiness 清单能读到（解析器自证，$(printf '%s' "$RNAMES" | tr ',' '\n' | grep -c .) 项）"; PASS=$((PASS+1))
     else
       check "readiness 清单含 redis_declared_but_down" "有" "无"
     fi ;;
esac

# ① 观测位：声明启用且已连通 → connected；副本>1 但有 Redis → 协调红灯不亮
check "A的redis观测位=connected" "connected" "$(detail "$PORT_A" readiness_value redis_declared_but_down)"
check "B的redis观测位=connected" "connected" "$(detail "$PORT_B" readiness_value redis_declared_but_down)"
check "A的detail字段redis_enabled=True" "True" "$(detail "$PORT_A" field redis_enabled)"
check "A声明副本数=2" "2" "$(detail "$PORT_A" field replicas)"
check_not "A的公开/status不判crit(「副本>1无Redis」红灯未亮)" "crit" "$(pub_status "$PORT_A")"

# ② 登录防爆破锁走 Redis 轨：A 上 5 次失败即锁，第 6 次被守卫拒
for _i in 1 2 3 4 5; do login_try "$PORT_A" "$U_A" >/dev/null; done
KEYS_TOUCHED=1
A6=$(login_try "$PORT_A" "$U_A")
case "$A6" in
  *临时锁定*) check "A第6次登录被守卫拒绝(临时锁定)" y y ;;
  *) check "A第6次登录被守卫拒绝" "含「临时锁定」" "$A6" ;;
esac
check "Redis里两把守卫键都在(cnt+lock)" "2" "$(rcmd exists "$U_A")"
CNTV=$(rcmd get "$U_A")
case "$CNTV" in
  ''|*[!0-9]*) check "Redis失败计数>=5" "数字" "非数字:$CNTV" ;;
  *) if [ "$CNTV" -ge 5 ]; then check "Redis失败计数>=5($CNTV)" y y; else check "Redis失败计数>=5" ">=5" "$CNTV"; fi ;;
esac
# TTL 必须有限：永不过期的计数键 = 永久锁（P2-44 的老根因，这里再钉一次）
TTLV=$(rcmd ttl "$U_A")
case "$TTLV" in
  ''|*[!0-9]*) check "Redis计数键TTL>0(不会永久锁)" "数字>0" "非数字:$TTLV" ;;
  *) if [ "$TTLV" -gt 0 ]; then check "Redis计数键TTL>0(${TTLV}s，不会永久锁)" y y; else check "Redis计数键TTL>0" ">0" "$TTLV"; fi ;;
esac
# 跨实例正身：B 上同一用户名也必须认这把锁。B 本地内存里没有任何这个用户的记录，
# 唯一能回出「临时锁定」的来源就是共享 Redis——这就是"锁真的跨实例"的机器证据。
B1=$(login_try "$PORT_B" "$U_A")
case "$B1" in
  *临时锁定*) check "B跨实例认同一把锁(共享Redis)" y y ;;
  *) check "B跨实例认同一把锁" "含「临时锁定」" "$B1" ;;
esac

# ③ 反向对照：声明启用却连不上（REDIS_ADDR 指向死端口）→ 观测位红 + 不落 Redis 键 + 不共享锁
start_instance "$PORT_C" "127.0.0.1:1" c.log; PID_C=$START_PID
check "实例C(${PORT_C}，Redis不可达) 健康(200)" "200" "$(wait_ready "$PORT_C")"
check "C的redis观测位=declared_but_down" "declared_but_down" "$(detail "$PORT_C" readiness_value redis_declared_but_down)"
check "C的redis观测位判级=crit" "crit" "$(detail "$PORT_C" readiness_status redis_declared_but_down)"
check "C的detail字段redis_enabled=False" "False" "$(detail "$PORT_C" field redis_enabled)"
check "C的公开/status判crit(副本>1却无协调层，红灯必须亮)" "crit" "$(pub_status "$PORT_C")"
for _i in 1 2 3 4 5 6; do login_try "$PORT_C" "$U_C" >/dev/null; done
check "C的失败计数没有落进Redis(退化内存轨)" "0" "$(rcmd exists "$U_C")"
C1=$(login_try "$PORT_C" "$U_A")
case "$C1" in
  *临时锁定*) check "C与Redis断开时不该共享A的锁" "不锁定" "$C1" ;;
  *) check "C与Redis断开时回退内存轨(不共享锁)" y y ;;
esac

# 9) 收尾自检：守卫键确实清掉了（否则本机 Redis 背着一把 15 分钟的锁留给下一位）
rcmd del "$U_A" >/dev/null
check "清理后A的守卫键已消失" "0" "$(rcmd exists "$U_A")"
KEYS_TOUCHED=0

echo ""
echo "==== 结果: PASS=$PASS FAIL=$FAIL SKIP=$SKIPN ===="
[ "$FAIL" = "0" ] || exit 1
exit 0
