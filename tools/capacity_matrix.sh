#!/bin/bash
# C5 多实例 WS 容量矩阵：本机启动两个服务实例，跑 N 个 WS 建连 + 硬边界聊天跨实例广播验证。
# 默认 1000 连接（500/实例）、9091/9092 端口；共享本地 PostgreSQL 与 Redis，AI 强制 mock。
# 用法：MATRIX_N=1000 ./tools/capacity_matrix.sh
# 报告：BENCH_WS_MATRIX.md；JSON：BENCH_WS_MATRIX.json
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

BIN="${BIN:-$ROOT/ai-scrm}"
MATRIX_N="${MATRIX_N:-1000}"
PORT_A="${CAP_PORT_A:-9091}"
PORT_B="${CAP_PORT_B:-9092}"
TENANT_ID="${TENANT_ID:-1}"
HOLD_MS="${HOLD_MS:-1000}"
CONNECT_CONCURRENCY="${CONNECT_CONCURRENCY:-100}"
REPORT="${REPORT_FILE:-BENCH_WS_MATRIX.md}"
JSON_REPORT="${JSON_FILE:-BENCH_WS_MATRIX.json}"
TMP="$(mktemp -d)"
PIDS=()
STARTED_PID=0

cleanup() {
  if [ "${#PIDS[@]}" -gt 0 ]; then
    local pid
    for pid in "${PIDS[@]}"; do
      kill "$pid" >/dev/null 2>&1 || true
      wait "$pid" >/dev/null 2>&1 || true
    done
  fi
  rm -rf "$TMP"
}
trap cleanup EXIT

if ! command -v node >/dev/null 2>&1; then
  echo "缺少 node，无法执行 WS 容量矩阵" >&2
  exit 1
fi
if ! redis-cli ping >/dev/null 2>&1; then
  echo "Redis 未运行（容量矩阵需 REDIS_ENABLED=true 才能验证跨实例广播）" >&2
  exit 1
fi

echo "[capacity] 编译服务"
go build -o "$BIN" ./cmd/server

start_instance() {
  local port="$1" log="$2" i
  nohup env \
    SERVER_PORT="$port" \
    REDIS_ENABLED=true \
    AI_MOCK_MODE=true \
    GIN_MODE=debug \
    WS_ADVISOR_IP_RATE_LIMIT="${WS_MATRIX_ADVISOR_LIMIT:-10}" \
    WS_CLIENT_IP_RATE_LIMIT="${WS_MATRIX_CLIENT_LIMIT:-0}" \
    "$BIN" >"$log" 2>&1 &
  STARTED_PID=$!
  PIDS+=("$STARTED_PID")
  for i in $(seq 1 60); do
    sleep 2
    if curl -s -o /dev/null -m 2 "http://127.0.0.1:${port}/health"; then
      return 0
    fi
    if ! kill -0 "$STARTED_PID" >/dev/null 2>&1; then
      echo "实例 :${port} 启动失败，日志：" >&2
      tail -50 "$log" >&2 || true
      return 1
    fi
  done
  echo "实例 :${port} 启动超时，日志：" >&2
  tail -50 "$log" >&2 || true
  return 1
}

echo "[capacity] 启动双实例 :${PORT_A} / :${PORT_B}"
start_instance "$PORT_A" "$TMP/a.log"
PID_A="$STARTED_PID"
start_instance "$PORT_B" "$TMP/b.log"
PID_B="$STARTED_PID"

echo "[capacity] 创建访客（tenant_id=${TENANT_ID}）"
GUEST=""
CID=""
VK=""
for _ in $(seq 1 75); do
  GUEST=$(curl -s -m 5 -X POST "http://127.0.0.1:${PORT_A}/api/v1/chat/guest" \
    -H "X-Tenant-ID: ${TENANT_ID}" -H "Content-Type: application/json" -d '{}' || true)
  CID=$(printf '%s' "$GUEST" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("data",{}).get("customer_id",""))' 2>/dev/null || true)
  VK=$(printf '%s' "$GUEST" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("data",{}).get("visitor_key",""))' 2>/dev/null || true)
  if [ -n "$CID" ] && [ -n "$VK" ]; then
    break
  fi
  sleep 1
done
if [ -z "$CID" ] || [ -z "$VK" ]; then
  echo "访客创建失败：${GUEST}" >&2
  exit 1
fi

echo "[capacity] 跑 ${MATRIX_N} 连接 / 双实例广播矩阵"
MATRIX_BASES="http://127.0.0.1:${PORT_A},http://127.0.0.1:${PORT_B}" \
MATRIX_N="$MATRIX_N" \
CUSTOMER_ID="$CID" \
VISITOR_KEY="$VK" \
TENANT_ID="$TENANT_ID" \
HOLD_MS="$HOLD_MS" \
CONNECT_CONCURRENCY="$CONNECT_CONCURRENCY" \
node tools/ws_capacity_matrix.mjs > "$TMP/matrix.json"

cp "$TMP/matrix.json" "$JSON_REPORT"
python3 - "$TMP/matrix.json" "$REPORT" "$PORT_A" "$PORT_B" "$MATRIX_N" "$HOLD_MS" <<'PY'
import json, sys
path, report, pa, pb, n, hold = sys.argv[1:7]
d = json.load(open(path, encoding='utf8'))
def cell(v):
    return '?' if v is None else str(v)
text = f"""# C5 WS 多实例容量矩阵（2026-09-14）

- 实例：`http://127.0.0.1:{pa}`、`http://127.0.0.1:{pb}`
- 连接数：`{n}`（本机双实例对半分配，`MATRIX_BASES` 轮询）
- 广播验证：在 `{pa}` 触发 `/api/v1/chat/test` 硬边界事件，要求本实例与 `{pb}` 均收到 marker
- 保持时长：`{hold}ms`
- 建连并发：`{d.get('connect_concurrency', '?')}`
- 握手耗时：P50 `{cell(d.get('handshake_p50_ms'))}ms` / P95 `{cell(d.get('handshake_p95_ms'))}ms` / P99 `{cell(d.get('handshake_p99_ms'))}ms`
- 广播延迟：`{cell(d.get('broadcast', {}).get('latency_ms'))}ms`

## 结果

| 实例 | 请求连接 | 成功 | 失败 |
|---|---:|---:|---:|
"""
for base, opened in d.get('opened_by_base', {}).items():
    text += f"| `{base}` | {opened + d.get('failed_by_base', {}).get(base, 0)} | {opened} | {d.get('failed_by_base', {}).get(base, 0)} |\n"
b = d.get('broadcast', {})
text += f"""
| 场景 | 结果 | 说明 |
|---|---|---|
| 多实例 WS 建连 | {'PASS' if d.get('handshake_passed') else 'FAIL'} | opened={d.get('opened')}/{d.get('requested')} failed={d.get('failed')} |
| Redis 跨实例广播 | {'PASS' if b.get('passed') else 'FAIL'} | local={b.get('local_ok')} remote={b.get('remote_ok')} HTTP={b.get('http_status')} |

## 约束

1. 这是 macOS 本机回环容量矩阵，受文件句柄、Node 事件循环与本地 PostgreSQL 连接池影响，不能替代公网 LB 压测。
2. `tools/ws_capacity_matrix.mjs` 使用原生 HTTP Upgrade + 手写 WS 帧解析，避免引入 `ws` 依赖。
3. 矩阵脚本仅使用测试端口，不触碰默认 9090；结束会回收自启实例，不删除数据库访客。
"""
open(report, 'w', encoding='utf8').write(text)
PY
echo "[capacity] 完成：$REPORT / $JSON_REPORT"
