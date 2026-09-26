#!/bin/bash
# ============================================================
# deploy_preflight 的判据自证（FIX-7 批，2026-09-27）
#
# 为什么单独一个脚本：preflight 本身是"只读守卫"，而本仓这批的教训原话是
# **「只会打日志并返回成功的守卫等于没有守卫」**。把"副本>1 且 Redis 未开"从 warn 改成 bad
# 之后，如果不配反向用例，就没有任何东西证明这条 bad 真的会响——它完全可能因为
# "只在 .env 显式写了 APP_REPLICAS 才判"这类取值缺陷而永远不触发，改动的语义只剩注释。
#
# 本脚本喂四组合成 (.env, compose) 给真 preflight，逐组断言"该红的红、该绿的不红"：
#   ① APP_REPLICAS=2 + Redis 未开 → FAIL「副本数=2」（旧实现能抓到这一组）
#   ② 只把 replicas: 3 写进编排、.env 不提 APP_REPLICAS → 仍须 FAIL（旧实现看不见，本批补的那半）
#   ③ 编排起了 redis 服务但应用不连 → 必须 FAIL（CI 三个 job 恰好就是这个形态）
#   ④ 单实例且编排显式 REDIS_ENABLED:"true" → 三条腿一条都不许红（防"把闸改成恒红"这种假严格）
#
# 用法：bash tools/test_deploy_preflight.sh    （纯文件判据，不启容器、不连库、退出码 0/1）
# ============================================================
set -u
cd "$(dirname "$0")/.."
ROOT=$(pwd)
WORK=$(mktemp -d "${TMPDIR:-/tmp}/preflight_selftest.XXXXXX")
trap 'rm -rf "$WORK"' EXIT

PASS=0
FAIL=0
ok() { echo "  PASS  $1"; PASS=$((PASS+1)); }
bad() { echo "  FAIL  $1"; FAIL=$((FAIL+1)); }

# 合成一份"其它段落全绿"的 .env，让退出码只反映多实例这一段判据
mk_env() { # $1=目标文件 $2=APP_REPLICAS 值（空=不写这行） $3=REDIS_ENABLED 值（空=不写）
  {
    echo "GIN_MODE=release"
    echo "APP_ENV=prod"
    echo "ALLOW_MOCK_PAY=false"
    echo "JWT_SECRET=selftest-only-0123456789abcdef0123456789abcdef"
    echo "DB_PASSWORD=selftest_strong_pw"
    echo "DB_HOST=db"
    echo "DB_NAME=ai_scrm"
    echo "DB_USER=ai_scrm"
    echo "AI_MOCK_MODE=false"
    echo "GLM_API_KEY=selftest-key"
    [ -n "${2:-}" ] && echo "APP_REPLICAS=$2"
    [ -n "${3:-}" ] && echo "REDIS_ENABLED=$3"
  } > "$1"
}

# 合成编排：三条 §5 判据（env_file / pgvector / APP_PUBLISH_PORT）都满足，
# 并把 app 服务块写成参数：$2=是否显式 REDIS_ENABLED:"true"，$3=replicas 值（空=不写），
# $4=是否声明 redis 服务
mk_compose() {
  local f=$1 redis_line="" rep="" redissvc=""
  [ "$2" = "yes" ] && redis_line='      REDIS_ENABLED: "true"'
  [ -n "$3" ] && rep="      replicas: $3"
  [ "$4" = "yes" ] && redissvc=$'  redis:\n    image: redis:7-alpine'
  {
    echo "services:"
    echo "  postgres:"
    echo "    image: pgvector/pgvector:0.8.6-pg16"
    [ -n "$redissvc" ] && printf '%s\n' "$redissvc"
    echo "  app:"
    echo "    env_file: .env"
    echo "    environment:"
    echo "      SERVER_PORT: \"8080\""
    [ -n "$redis_line" ] && echo "$redis_line"
    echo "    ports:"
    echo "      - \"\${APP_PUBLISH_PORT:-8080}:8080\""
    echo "    deploy:"
    echo "      resources:"
    echo "        limits:"
    echo "          cpus: \"2\""
    [ -n "$rep" ] && echo "$rep"
  } > "$f"
}

# run_preflight <env文件> <compose文件> → stdout 是 preflight 全文，rc 是退出码
run_preflight() {
  COMPOSE_FILE="$2" bash tools/deploy_preflight.sh "$1" 2>&1
}

expect_fail_with() { # $1=描述 $2=必须命中的子串 $3=输出 $4=退出码
  if [ "$4" != "0" ] && printf '%s' "$3" | grep -q "$2"; then
    ok "$1"
  else
    bad "${1}（期望退出码非 0 且命中「${2}」；实际 rc=${4}）"
    printf '%s\n' "$3" | sed 's/^/        /' | tail -12
  fi
}

expect_absent() { # $1=描述 $2=不许出现的子串 $3=输出
  if printf '%s' "$3" | grep -q "$2"; then
    bad "${1}（不该出现却出现了「${2}」）"
  else
    ok "$1"
  fi
}

echo "==== deploy_preflight 判据自证 ===="

# ① APP_REPLICAS=2 且 Redis 未开
E1="$WORK/c1.env"; C1="$WORK/c1.yml"
mk_env "$E1" 2 ""
mk_compose "$C1" no "" no
O1=$(run_preflight "$E1" "$C1"); R1=$?
expect_fail_with "① APP_REPLICAS=2 且未开 Redis → FAIL" "副本数=2" "$O1" "$R1"

# ② 副本只写在编排里（.env 完全不提 APP_REPLICAS）——旧实现看不见这一路
E2="$WORK/c2.env"; C2="$WORK/c2.yml"
mk_env "$E2" "" ""
mk_compose "$C2" no 3 no
O2=$(run_preflight "$E2" "$C2"); R2=$?
expect_fail_with "② 只在编排写 replicas: 3 → FAIL（本批补的可见性）" "副本数=3" "$O2" "$R2"

# ③ 编排起了 redis 服务而应用不连（CI 三个 job 的原形态：起了没人连）
E3="$WORK/c3.env"; C3="$WORK/c3.yml"
mk_env "$E3" "" "false"
mk_compose "$C3" no "" yes
O3=$(run_preflight "$E3" "$C3"); R3=$?
expect_fail_with "③ 声明 redis 服务但应用不连 → FAIL" "起了没人连" "$O3" "$R3"

# ④ 正向：单实例 + 编排显式开 Redis → 三条腿一条都不许红
#    （这一组是防"把闸改成恒红"的假严格：任何一条腿无条件响，①~③ 都会绿得毫无意义）
E4="$WORK/c4.env"; C4="$WORK/c4.yml"
mk_env "$E4" "1" ""
mk_compose "$C4" yes "" yes
O4=$(run_preflight "$E4" "$C4"); R4=$?
expect_absent "④ 单实例且编排开 Redis：不报「副本数」红" "副本数=" "$O4"
expect_absent "④ 同上不报「起了没人连」红" "起了没人连" "$O4"
if [ "$R4" = "0" ]; then
  ok "④ 正向组整体退出码 0（除多三段判据外没有别的 FAIL）"
else
  bad "④ 正向组整体退出码应为 0（rc=${R4}）"
  printf '%s\n' "$O4" | grep FAIL | sed 's/^/        /'
fi

echo ""
echo "==== 结果: PASS=$PASS FAIL=$FAIL ===="
[ "$FAIL" = "0" ] || exit 1
exit 0
