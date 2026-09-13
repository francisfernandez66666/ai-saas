#!/bin/bash
# C5 容量基线（2026-09-13）：用本地 ab + Node WebSocket 辅助产出可复跑的 HTTP/WS 基线报告。
# 默认不跑真实 /chat/test AI 压测，避免误烧 token；设置 LOAD_TEST_AI=true 才跑一轮小样本同步链路。
# 用法：
#   ./tools/loadtest.sh                     # 轻量基线：/health + /status + /metrics + /chat/welcome + WS
#   LOAD_TEST_AI=true ./tools/loadtest.sh   # 追加 /chat/test 同步链路小样本（建议 AI_MOCK_MODE=true 或测试额度内）
#   REPORT_FILE=BENCH_2026-09.md ./tools/loadtest.sh
#
# 前置：服务已启动；默认端口 9090。/chat/guest、/chat/welcome、/ws/client 有 IP 固定窗口限流，
# 脚本会自动等待窗口重置，避免把 429 当成容量结果。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PORT="${SERVER_PORT:-9090}"
BASE="http://localhost:${PORT}"
REPORT="${REPORT_FILE:-$ROOT/BENCH_2026-09.md}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

HEALTH_N="${HEALTH_N:-1000}"
HEALTH_C="${HEALTH_C:-50}"
STATUS_N="${STATUS_N:-500}"
STATUS_C="${STATUS_C:-25}"
METRICS_N="${METRICS_N:-300}"
METRICS_C="${METRICS_C:-20}"
WELCOME_N="${WELCOME_N:-20}"
WELCOME_C="${WELCOME_C:-5}"
CHAT_N="${CHAT_N:-10}"
CHAT_C="${CHAT_C:-2}"
WS_N="${WS_N:-20}"
WS_HOLD_MS="${WS_HOLD_MS:-1000}"
LOAD_TEST_AI="${LOAD_TEST_AI:-false}"

if ! command -v ab >/dev/null 2>&1; then
  echo "缺少 ab。macOS: xcode-select --install 或 brew install httpd；Linux: apt-get install apache2-utils"
  exit 1
fi

if ! curl -s -o /dev/null -m 2 "$BASE/health"; then
  echo "服务未启动：$BASE"
  exit 1
fi

json_get() {
  python3 -c 'import sys,json; d=json.load(sys.stdin); print(eval("d" + sys.argv[1]))' "$1" 2>/dev/null || true
}

parse_ab() {
  local f="$1" label="$2" out="$3"
  local conc reqs failed non2xx rps p50 p95 p99 max
  conc=$(awk '/Concurrency Level:/ {print $3}' "$f" || true)
  reqs=$(awk '/Complete requests:/ {print $3}' "$f" || true)
  failed=$(awk '/Failed requests:/ {print $3}' "$f" || true)
  non2xx=$(awk '/Non-2xx responses:/ {print $3}' "$f" || echo 0)
  rps=$(awk '/Requests per second:/ {print $4}' "$f" || true)
  p50=$(awk '/^  50%/ {print $2}' "$f" || true)
  p95=$(awk '/^  95%/ {print $2}' "$f" || true)
  p99=$(awk '/^  99%/ {print $2}' "$f" || true)
  max=$(awk '/^Max:/ {print $2}' "$f" || true)
  if [ -z "$conc" ] || [ -z "$reqs" ]; then
    echo "$label 解析 ab 输出失败" >&2
    cat "$f" >&2
    return 1
  fi
  printf "| %s | %s | %s | %s | %s | %s | %s | %s | %s |\n" \
    "$label" "$conc" "$reqs" "${failed:-0}" "${non2xx:-0}" "${rps:-?}" "${p50:-?}" "${p95:-?}" "${p99:-?}" >> "$out"
}

ab_non2xx() {
  awk '/Non-2xx responses:/ {print $3; exit}' "$1" 2>/dev/null || echo 1
}

run_ab_clean() {
  local f="$1" label="$2" out="$3" max_attempts="${4:-3}"
  shift 4
  local i nn
  for i in $(seq 1 "$max_attempts"); do
    ab -q "$@" > "$f" 2>&1 || true
    nn=$(ab_non2xx "$f")
    [ -z "$nn" ] && nn=0
    if [ "$nn" = "0" ]; then
      parse_ab "$f" "$label" "$out"
      return 0
    fi
    if [ "$i" -lt "$max_attempts" ]; then
      sleep 65
    fi
  done
  parse_ab "$f" "${label}（疑似限流/异常，保留原结果）" "$out"
  return 1
}

parse_ws_json() {
  python3 - "$1" <<'PY'
import json, sys
d = json.loads(sys.argv[1])
def s(k):
    v = d.get(k)
    return "?" if v is None else v
print(f"| WS /api/v1/ws/client 建连(无广播) | {s('requested')} | {s('opened')} | {s('failed')} | 0 | ? | {s('p50_ms')} | {s('p95_ms')} | {s('p99_ms')} |")
PY
}

ws_failed() {
  _WS="$1" python3 - <<'PY'
import json, os
print(json.loads(os.environ["_WS"]).get("failed", 1))
PY
}

queue_after() {
  local i v
  v="?"
  for i in $(seq 1 30); do
    v=$(curl -s "$BASE/metrics" | awk '/^ai_scrm_merge_queue_active /{print $2}' | tail -1 || true)
    [ "$v" = "0" ] && break
    sleep 2
  done
  echo "$v"
}

echo "运行 C5 容量基线 → $REPORT"
echo "| 场景 | 并发 | 完成请求 | 失败 | 非2xx | RPS | P50(ms) | P95(ms) | P99(ms) |" > "$TMP/table.md"
echo "|---|---:|---:|---:|---:|---:|---:|---:|---:|" >> "$TMP/table.md"

run_ab_clean "$TMP/health.ab" "/health 只读健康检查" "$TMP/table.md" 3 -n "$HEALTH_N" -c "$HEALTH_C" "$BASE/health"
run_ab_clean "$TMP/status.ab" "/status 结构化健康探针(DB/队列)" "$TMP/table.md" 3 -n "$STATUS_N" -c "$STATUS_C" "$BASE/status"
run_ab_clean "$TMP/metrics.ab" "/metrics Prometheus 文本(loopback)" "$TMP/table.md" 3 -n "$METRICS_N" -c "$METRICS_C" "$BASE/metrics"

GUEST_URL="$BASE/api/v1/chat/guest"
CID=""
VK=""
for _ in $(seq 1 75); do
  GUEST_CODE=$(curl -s -o "$TMP/guest.out" -w '%{http_code}' -m 5 -X POST "$GUEST_URL" -H "X-Tenant-ID: 1" -H "Content-Type: application/json" -d '{}' || true)
  if [ "$GUEST_CODE" = "200" ]; then
    CID=$(json_get "['data']['customer_id']" < "$TMP/guest.out")
    VK=$(json_get "['data']['visitor_key']" < "$TMP/guest.out")
    break
  fi
  if [ "$GUEST_CODE" != "429" ]; then
    break
  fi
  sleep 1
done

if [ -z "$CID" ] || [ "$CID" = "None" ] || [ -z "$VK" ] || [ "$VK" = "None" ]; then
  GUEST_NOTE="未运行：/chat/guest 创建失败或仍处 IP 限流窗口。"
else
  echo '{"customer_id":'"$CID"'}' > "$TMP/welcome.json"
  if run_ab_clean "$TMP/welcome.ab" "/chat/welcome 会话初始化(无AI)" "$TMP/table.md" 3 \
    -n "$WELCOME_N" -c "$WELCOME_C" -H "X-Tenant-ID: 1" -T application/json -p "$TMP/welcome.json" \
    "$BASE/api/v1/chat/welcome?visitor_key=$VK"; then
    GUEST_NOTE="运行参数：n=${WELCOME_N} c=${WELCOME_C}；已按 IP 固定窗口限流预留窗口等待。"
  else
    GUEST_NOTE="运行参数：n=${WELCOME_N} c=${WELCOME_C}；多次重试后仍有非 2xx，结果保留原始 ab 输出。"
  fi
fi

WS_NOTE="未运行：缺少 customer_id/visitor_key。"
if [ -n "$CID" ] && [ "$CID" != "None" ] && [ -n "$VK" ] && [ "$VK" != "None" ]; then
  if ! command -v node >/dev/null 2>&1; then
    WS_NOTE="未运行：缺少 node，无法执行 wsbench.mjs。"
  else
    WS_JSON=""
    for _ in $(seq 1 3); do
      WS_JSON=$(CUSTOMER_ID="$CID" VISITOR_KEY="$VK" WS_N="$WS_N" WS_HOLD_MS="$WS_HOLD_MS" node tools/wsbench.mjs 2>"$TMP/ws.err" || true)
      if [ -n "$WS_JSON" ] && [ "$(ws_failed "$WS_JSON")" = "0" ]; then
        parse_ws_json "$WS_JSON" >> "$TMP/table.md"
        WS_NOTE=$(_WS="$WS_JSON" python3 - <<'PY'
import json, os
d = json.loads(os.environ["_WS"])
print(f"运行参数：n={d.get('requested')} hold={d.get('hold_ms')}ms opened={d.get('opened')} failed={d.get('failed')}；当前 /ws/client 默认 20/min，超过 20 会先被限流。")
PY
)
        break
      fi
      sleep 65
    done
    if [ "$WS_JSON" = "" ] || [ "$(ws_failed "$WS_JSON")" != "0" ]; then
      if [ -n "$WS_JSON" ]; then
        parse_ws_json "$WS_JSON" >> "$TMP/table.md"
        WS_NOTE="多次重试后仍有失败连接，可能是 /ws/client IP 限流或文件句柄限制；结果保留原始 JSON。"
      else
        WS_NOTE="未运行：$(cat "$TMP/ws.err" 2>/dev/null | tail -1 || true)"
      fi
    fi
  fi
fi

CHAT_NOTE="未运行。默认跳过真实/模拟 AI 同步链路；如需容量基线，设 LOAD_TEST_AI=true，并优先在 AI_MOCK_MODE=true 或测试额度下跑。"
if [ "$LOAD_TEST_AI" = "true" ]; then
  if [ -z "$CID" ] || [ "$CID" = "None" ] || [ -z "$VK" ] || [ "$VK" = "None" ]; then
    CHAT_NOTE="未能创建 /chat/guest 访客，跳过 /chat/test 场景。"
  else
    printf '{"customer_id":%s,"content":"你好，你们有什么车型可以推荐？"}' "$CID" > "$TMP/chat.json"
    if run_ab_clean "$TMP/chat.ab" "/chat/test 同步链路(含合并/AI/延迟)" "$TMP/table.md" 3 \
      -n "$CHAT_N" -c "$CHAT_C" -H "X-Tenant-ID: 1" -T application/json -p "$TMP/chat.json" \
      "$BASE/api/v1/chat/test?visitor_key=$VK"; then
      CHAT_NOTE="运行参数：n=${CHAT_N} c=${CHAT_C}；该场景受合并窗口、模型降级链和模拟延迟影响，小样本仅用于趋势观测。"
    else
      CHAT_NOTE="运行参数：n=${CHAT_N} c=${CHAT_C}；多次重试后仍有非 2xx，结果保留原始 ab 输出。"
    fi
  fi
fi

Q_AFTER=$(queue_after || true)
cat <<EOF > "$REPORT"
# C5 容量基线报告（2026-09-13）

- 目标服务：\`$BASE\`
- 运行时间：$(date -Iseconds)
- 负载工具：\`ab\` + \`tools/wsbench.mjs\`。当前环境未装 k6/hey/wrk，脚本先以 ApacheBench 建立可复跑 HTTP 基线，并用 Node 原生 http upgrade 做 WS 建连基线；1000 连接与跨实例广播仍受本机文件句柄/单实例限流约束，后续需专用工具补齐。
- 队列回落：跑完后 \`ai_scrm_merge_queue_active=$Q_AFTER\`。
- 访客链路说明：$GUEST_NOTE
- WebSocket 说明：$WS_NOTE
- 同步链路说明：$CHAT_NOTE

## 结果

$(cat "$TMP/table.md")

## 部署硬约束

1. Nginx/LB 到本服务的 \`/api/v1/chat/test\`、\`/api/v1/chat\` 读超时建议 \`proxy_read_timeout >= 90s\`；同步链路包含合并窗口(默认 25s)、AI 调用(预算约 110s)和模拟延迟，短超时会把正常回复掐断。
2. \`/status\` 的 \`db_ok\` 与 \`/metrics\` 的 \`ai_scrm_merge_queue_active\` 是最低容量观测面：压测后活跃合并队列应回落到 0，否则检查 Redis/多实例锁、AI 端点、后台 worker。
3. 多实例部署必须共享 Redis 锁/配置缓存版本；本地单实例基线不代表多实例吞吐。
4. \`AI_MOCK_MODE=false\` 时 \`/chat/test\` 会调用真实模型并产生用量；容量基线优先使用 mock 模式或独立测试预算。
5. \`/chat/guest\`、\`/chat/welcome\`、\`/chat/test\`、\`/ws/client\` 当前是单实例内存固定窗口限流：10/min、30/min、20/min、20/min。多实例部署时每个实例独立计数，容量压测要按实例数折算或切到全局分布式限流。
EOF

echo "完成：$REPORT"
