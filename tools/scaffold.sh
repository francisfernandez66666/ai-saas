#!/bin/bash
# scaffold.sh —— 复核前置探活。任何测试之前先跑通这个，否则所有结论不可信。
# 用法： bash tools/cleanenv.sh bash tools/scaffold.sh [port]
set -u
PORT="${1:-9090}"
B="http://localhost:$PORT"
OK=0; BAD=0
chk(){ # chk <名> <期望> <实际>
  if [ "$2" = "$3" ]; then echo "  OK   $1 = $3"; OK=$((OK+1));
  else echo "  BAD  $1 = $3 (期望 $2)"; BAD=$((BAD+1)); fi
}

echo "=== 1. 工具链真身 ==="
echo "  PATH = $PATH"
echo "  grep = $(command -v grep)"
echo "  grep --version -> $(grep --version 2>&1 | head -1)"
echo "  go   = $(command -v go) / $(go version 2>&1)"
echo "  node = $(command -v node) / $(node -v 2>&1)"
echo "  psql = $(command -v psql)"
# grep 真身自证：BSD/GNU grep 支持 -E 的 [[:space:]]；toybox 也支持，但 -P 不支持
if printf 'a\n' | grep -qP 'a' 2>/dev/null; then GP=supported; else GP=UNSUPPORTED; fi
echo "  grep -P -> $GP (toybox 为 UNSUPPORTED，脚本中禁用)"

echo
echo "=== 2. 中间件连通 ==="
set -a; [ -f .env ] && . ./.env; set +a
if [ -n "${DB_PASSWORD:-}" ]; then export PGPASSWORD="$DB_PASSWORD"; fi
PG=$(psql -h "${DB_HOST:-localhost}" -p "${DB_PORT:-5432}" -U "${DB_USER:-ai_scrm}" -d "${DB_NAME:-ai_scrm}" -tAc "select 'up:tenants='||(select count(*) from tenants)||',customers='||(select count(*) from customers);" 2>&1 | head -1)
case "$PG" in up:*) echo "  OK   postgres = $PG"; OK=$((OK+1));; *) echo "  BAD  postgres = $PG"; BAD=$((BAD+1));; esac

RD=$(redis-cli -p "${REDIS_PORT:-6379}" ping 2>&1 | head -1)
chk "redis" "PONG" "$RD"

echo
echo "=== 3. 服务健康 ==="
HC=$(curl -s -m 5 -o /dev/null -w '%{http_code}' "$B/health" 2>/dev/null)
chk "GET /health" "200" "$HC"
if [ "$HC" = "200" ]; then
  echo "  /health 体 = $(curl -s -m 5 "$B/health" | head -c 300)"
  SC=$(curl -s -m 5 -o /dev/null -w '%{http_code}' "$B/status" 2>/dev/null)
  echo "  GET /status = $SC"
fi

echo
echo "=== 4. 断言脚手架自证（双向）==="
# 已知应命中：/health 返回 200（上面已验）
# 已知应放行：/api/v1/__definitely_not_exist__ 不该是 200
NC=$(curl -s -m 5 -o /dev/null -w '%{http_code}' "$B/api/v1/__definitely_not_exist__" 2>/dev/null)
echo "  GET /api/v1/__definitely_not_exist__ = $NC (期望非 200，验证 404 路由不回落 SPA)"
[ "$NC" != "200" ] && OK=$((OK+1)) || BAD=$((BAD+1))

echo
echo "==== scaffold: OK=$OK BAD=$BAD ===="
[ "$BAD" -eq 0 ] || exit 1
