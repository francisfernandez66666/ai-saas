#!/bin/bash
# ============================================================
# test_all.sh —— 自动化测试统一编排入口（能力基建，2026-09-05）
#
# 解决的历史欠账：
#   1. 五套 E2E 脚本共享"改全局开关再恢复"模式，无并发护栏——
#      本入口用 flock 文件锁强制单实例，并跑即拒绝（防互相踩配置）；
#   2. 单测/E2E/前端各自为战，无统一汇总——这里串行编排并输出
#      PASS/FAIL 总账，任一环节失败 exit=1（CI 可直接消费）。
#
# 用法：
#   ./tools/test_all.sh            # 完整回归（含 uat，耗时约 20-40 分钟）
#   ./tools/test_all.sh --fast     # 快回归：单测+构建+smoke/org/saas，跳过 uat
#   ./tools/test_all.sh --unit     # 仅单元测试层（go test -cover + 前端 vitest）
#   ./tools/test_all.sh --capacity # 单测+构建+T7契约+C5 双实例 WS/Redis 广播矩阵（本地环境）
#   SERVER_PORT=9090 ./tools/test_all.sh   # 指定服务端口（默认 9090）
#
# 阶段顺序：单元层 → 构建 → E2E 层（五套） → 汇总。每阶段失败继续跑后续
#（除非 --failfast），最终以总账定 exit code。
# ============================================================
set -u

# ---- 并发护栏：整仓测试单实例（锁文件在项目根，避免 /tmp 被清）----
LOCK_FILE="$(dirname "$0")/../.test_all.lock"
acquire_lock() {
  # 跨平台：Linux(CI)=flock，macOS 默认无 flock 用 shlock（原子文件锁）
  if command -v flock >/dev/null 2>&1; then
    exec 9>"$LOCK_FILE"
    flock -n 9 || return 1
  elif command -v shlock >/dev/null 2>&1; then
    shlock -f "$LOCK_FILE" -p $$ || return 1
  else
    # 兜底：mkdir 原子性作简易锁
    mkdir "$LOCK_FILE" 2>/dev/null || return 1
  fi
  return 0
}
if ! acquire_lock; then
  echo "[test_all] ✗ 已有测试在跑（$LOCK_FILE 被占）——五套 E2E 共享全局开关，禁止并发，请等待其结束。"
  exit 1
fi
release_lock() {
  if command -v flock >/dev/null 2>&1; then :; elif command -v shlock >/dev/null 2>&1; then rm -f "$LOCK_FILE"; else rmdir "$LOCK_FILE" 2>/dev/null; fi
}
trap 'release_lock' EXIT

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
PORT="${SERVER_PORT:-9090}"
MODE="full"
[ "${1:-}" = "--fast" ] && MODE="fast"
[ "${1:-}" = "--unit" ] && MODE="unit"
[ "${1:-}" = "--capacity" ] && MODE="capacity"
[ "${1:-}" = "--failfast" ] && FAILFAST=1 || FAILFAST=0

PASS=0; FAIL=0
step() { printf "\n=== [test_all] %s ===\n" "$1"; }
verdict() { # verdict <名称> <退出码>
  if [ "$2" -eq 0 ]; then echo "  PASS  $1"; PASS=$((PASS+1)); else echo "  FAIL  $1"; FAIL=$((FAIL+1)); [ "$FAILFAST" = "1" ] && exit 1; fi
}

# ---------- 阶段零：静态断言（G-12） ----------
step "静态断言：裸 db.DB 用法回归检查"
# 排除：_test.go、tx = db.DB 兜底、db.TenantFilter、注释行、cdp/gdb() 包级封装
BARE_DB=$(grep -rn --include="*.go" 'db\.DB\.' internal/ | grep -v '_test.go' | grep -v 'tx = db\.DB' | grep -v 'db\.TenantFilter' | grep -v 'db\.RQ\|db\.PQ' | grep -v '//' | grep -v 'cdp/' | grep -v 'gdb()' | grep -v 'db\.DB == nil' | grep -v 'db\.DB = ' | grep -v 'db\.DB;' | grep -v 'func gdb' | wc -l | tr -d ' ')
if [ "$BARE_DB" -gt 0 ]; then
  echo "  WARN  G-12: 发现 $BARE_DB 处裸 db.DB 用法（可能绕过租户隔离）："
  grep -rn --include="*.go" 'db\.DB\.' internal/ | grep -v '_test.go' | grep -v 'tx = db\.DB' | grep -v 'db\.TenantFilter' | grep -v 'db\.RQ\|db\.PQ' | grep -v '//' | grep -v 'cdp/' | grep -v 'gdb()' | grep -v 'db\.DB == nil' | grep -v 'db\.DB = ' | grep -v 'db\.DB;' | grep -v 'func gdb' | head -10
  verdict "G-12 裸 db.DB 检查（警告）" 0
else
  verdict "G-12 裸 db.DB 检查" 0
fi

# ---------- 阶段零：静态断言（G-6 防回潮） ----------
step "静态断言：G-6 防回潮回归检查"
G6_FAIL=0
# 1. 注释吞码：sb.WriteString 在注释中（P2-36 修复防回潮）
if grep -rn --include="*.go" '^\s*//.*sb\.WriteString' internal/ | grep -v '_test.go' | grep -q .; then
  echo "  FAIL  G-6: 注释中 sb.WriteString 残留"; grep -rn --include="*.go" '^\s*//.*sb\.WriteString' internal/ | grep -v '_test.go' | head -5; G6_FAIL=1
fi
# 2. 脏常量：uint(2) 用于分配语义（P2-24 修复防回潮）
if grep -rn --include="*.go" 'uint(2)' internal/ | grep -v '_test.go' | grep -v '//' | grep -q .; then
  echo "  FAIL  G-6: uint(2) 脏常量残留"; grep -rn --include="*.go" 'uint(2)' internal/ | grep -v '_test.go' | grep -v '//' | head -5; G6_FAIL=1
fi
# 3. gorm:query_option 废弃 API（P1-23 修复防回潮）
if grep -rn --include="*.go" 'gorm:query_option' internal/ | grep -v '_test.go' | grep -v '//' | grep -q .; then
  echo "  FAIL  G-6: gorm:query_option 废弃 API 残留"; grep -rn --include="*.go" 'gorm:query_option' internal/ | grep -v '_test.go' | grep -v '//' | head -5; G6_FAIL=1
fi
verdict "G-6 防回潮断言" $G6_FAIL

# ---------- 阶段一：单元测试层 ----------
step "单元测试层：go vet + go test -cover（含 DB 依赖用例，连不上自动跳过）"
go vet ./... >/tmp/test_all_vet.log 2>&1
verdict "go vet ./..." $?
go test -cover ./... >/tmp/test_all_go.log 2>&1
verdict "go test ./...（覆盖率见下方）" $?
grep -E "^(ok|FAIL|---)" /tmp/test_all_go.log | tail -20 || true

step "单元测试层：前端 vitest"
( cd frontend-react && npm run test >/tmp/test_all_fe.log 2>&1 )
FE_RC=$?
verdict "frontend vitest" $FE_RC
if [ "$FE_RC" != "0" ]; then tail -20 /tmp/test_all_fe.log || true; fi

if [ "$MODE" = "unit" ]; then
  echo "==== [test_all] 汇总: PASS=$PASS FAIL=${FAIL}（unit 模式）===="
  [ "$FAIL" = "0" ] && exit 0 || exit 1
fi

# ---------- 阶段二：构建层 ----------
step "构建层：后端编译 + 前端 build + typecheck"
go build -o ai-scrm ./cmd/server >/tmp/test_all_build.log 2>&1
verdict "go build ./cmd/server" $?
( cd frontend-react && npx tsc --noEmit >/tmp/test_all_tsc.log 2>&1 )
verdict "前端 tsc --noEmit" $?
( cd frontend-react && npm run build >/tmp/test_all_febuild.log 2>&1 )
verdict "前端 vite build" $?

# ---------- 阶段二.5：契约层（T7 codegen 防漂移） ----------
step "契约层：T7 apidump golden + api.d.ts + FE 路径孤儿 + as-any 基线"
./tools/check_api_contract.sh >/tmp/test_all_contract.log 2>&1
CONTRACT_RC=$?
verdict "check_api_contract.sh" $CONTRACT_RC
if [ "$CONTRACT_RC" != "0" ]; then tail -20 /tmp/test_all_contract.log || true; fi

# ---------- 阶段二.6：C5 容量矩阵（本地双实例 Redis 广播，可选模式） ----------
if [ "$MODE" = "capacity" ]; then
  step "容量矩阵：C5 双实例 WS ${MATRIX_N:-1000} 建连 + Redis 跨实例广播"
  MATRIX_N="${MATRIX_N:-1000}" ./tools/capacity_matrix.sh >/tmp/test_all_capacity.log 2>&1
  CAP_RC=$?
  verdict "capacity_matrix.sh" $CAP_RC
  if [ "$CAP_RC" != "0" ]; then tail -40 /tmp/test_all_capacity.log || true; fi
  echo "==== [test_all] 汇总: PASS=$PASS FAIL=${FAIL}（capacity 模式）===="
  [ "$FAIL" = "0" ] && exit 0 || exit 1
fi

# ---------- 阶段三：E2E 层（五套断言脚本） ----------
step "E2E 层：起服务（端口 ${PORT}）"
./stop.sh >/dev/null 2>&1 || true
# 兜底清端口：stop.sh 依赖 .pid 文件，nohup 直启未写时会残留旧进程占端口
pkill -f '^\./ai-scrm' >/dev/null 2>&1 || true
sleep 1
nohup ./ai-scrm > ai-scrm.log 2>&1 &
echo $! > .pid
for i in $(seq 1 60); do sleep 2; curl -s -o /dev/null -m 2 "http://localhost:$PORT/health" && break; done
psql ${TEST_DB_URL:-postgresql://ai_scrm:dev123@localhost/ai_scrm} -tAc \
  "UPDATE tenant_users SET must_change_password=false WHERE username='admin'" >/dev/null 2>&1 || true

step "E2E 层：smoke.sh（83 项）"
./tools/smoke.sh "$PORT" >/tmp/test_all_smoke.log 2>&1; verdict "smoke.sh" $?; tail -2 /tmp/test_all_smoke.log

step "E2E 层：smoke_perm.sh（角色权限矩阵 17 项）"
./tools/smoke_perm.sh "$PORT" >/tmp/test_all_perm.log 2>&1; verdict "smoke_perm.sh" $?; tail -2 /tmp/test_all_perm.log

step "E2E 层：smoke_chat_identity.sh（聊天身份缺口 9 项）"
./tools/smoke_chat_identity.sh "$PORT" >/tmp/test_all_identity.log 2>&1; verdict "smoke_chat_identity.sh" $?; tail -2 /tmp/test_all_identity.log

step "E2E 层：smoke_org.sh（11 项）"
./tools/smoke_org.sh "$PORT" >/tmp/test_all_org.log 2>&1; verdict "smoke_org.sh" $?; tail -2 /tmp/test_all_org.log

step "E2E 层：smoke_saas.sh（注册漏斗 8 项）"
./tools/smoke_saas.sh "$PORT" >/tmp/test_all_saas.log 2>&1; verdict "smoke_saas.sh" $?; tail -2 /tmp/test_all_saas.log

step "E2E 层：smoke_pay.sh（§W 支付回调验签+防重放 14 项，C6 资金安全）"
./tools/smoke_pay.sh "$PORT" >/tmp/test_all_pay.log 2>&1; verdict "smoke_pay.sh" $?; tail -2 /tmp/test_all_pay.log

step "E2E 层：smoke_channel.sh（企微/公众号通道 E2E 32 项，自建 9091+mockwx）"
./tools/smoke_channel.sh >/tmp/test_all_channel.log 2>&1; verdict "smoke_channel.sh" $?; tail -2 /tmp/test_all_channel.log

if [ "$MODE" != "fast" ]; then
  step "E2E 层：uat.sh（67 断言全场景，较长）"
  # uat 含真实 AI 调用与长时间等待，默认纳入 full 模式；CI 建议 --fast
  ./tools/uat.sh "$PORT" >/tmp/test_all_uat.log 2>&1; verdict "uat.sh" $?; tail -3 /tmp/test_all_uat.log
else
  echo "  [test_all] fast 模式：跳过 uat.sh（用 --full 跑全场景）"
fi

# ---------- 收尾：恢复现场 + 总账 ----------
./stop.sh >/dev/null 2>&1 || true
echo ""
echo "=========================================================="
echo " [test_all] 自动化测试总账: PASS=$PASS FAIL=${FAIL}（mode=${MODE}）"
echo "=========================================================="
[ "$FAIL" = "0" ] && exit 0 || exit 1
