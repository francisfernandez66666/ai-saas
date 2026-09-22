#!/usr/bin/env bash
# Go 覆盖率棘轮门禁（P1-5，2026-09-22）：总覆盖率只升不降。
#
# 背景：全仓 go test 覆盖率 22.4%（2026-09-22 实测），纯逻辑包高（80–96%）而骨架包低
# （api 4.4% / llm 4.7% 等）。没有棘轮，覆盖率回升后必然回吐。本门禁与仓库既有的
# .as_any_baseline / .doc_comments_baseline / .bare_db_baseline 同口径：基线只降不升。
#
# 用法：
#   tools/check_coverage_ratchet.sh                    # 门禁检查（CI/阶段零用）
#   tools/check_coverage_ratchet.sh --update-baseline  # 覆盖率提升后收紧基线
#
# 注意：跑全量 go test 需要编译全仓，耗时约 1–2 分钟；置于 test_all.sh 阶段零末尾
# 与单测阶段合并感知成本。本机 toybox/BSD grep 兼容坑：正则一律 -E，空白类用 [[:space:]]。
set -u

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
BASELINE_FILE="$SCRIPT_DIR/../.coverage_baseline"
COV_OUT="${TMPDIR:-/tmp}/ai_scrm_coverage.out"

# 复用模式：test_all.sh 单测阶段已经用 -coverprofile 跑过全量 go test，
# 通过 COV_PROFILE=/tmp/ai_scrm_coverage.out 环境变量直接复用产物，避免重复跑 1–2 分钟。
if [ -n "${COV_PROFILE:-}" ] && [ -f "${COV_PROFILE}" ]; then
  COV_OUT="$COV_PROFILE"
else
  # 独立模式：自己跑全量单测并收集覆盖率（不跑 E2E）
  if ! go test -count=1 -coverprofile="$COV_OUT" ./... >/dev/null 2>&1; then
    echo "  FAIL  覆盖率棘轮: go test ./... 未全绿，先修测试再谈覆盖率"
    go test -count=1 ./... 2>&1 | /usr/bin/grep -E '^(FAIL|ok.*FAIL)' | head -10
    exit 1
  fi
fi

TOTAL=$(go tool cover -func="$COV_OUT" 2>/dev/null | tail -1 | /usr/bin/grep -oE '[0-9]+\.?[0-9]*%' | tr -d '%')
if [ -z "$TOTAL" ]; then
  echo "  FAIL  覆盖率棘轮: 无法解析总覆盖率（go tool cover 输出异常）"
  exit 1
fi

if [ "${1:-}" = "--update-baseline" ]; then
  printf '%s\n' "$TOTAL" > "$BASELINE_FILE"
  echo "  OK    覆盖率基线已更新为 $TOTAL%"
  exit 0
fi

BASE=$(cat "$BASELINE_FILE" 2>/dev/null | tail -1)
if [ -z "$BASE" ]; then
  echo "  FAIL  覆盖率棘轮: 缺少基线文件 .coverage_baseline"
  exit 1
fi

# 浮点比较用 python3（避免依赖 bc）
if ! python3 -c "import sys; sys.exit(0 if float('$TOTAL') >= float('$BASE') else 1)"; then
  echo "  FAIL  覆盖率棘轮: 总覆盖率 $TOTAL% < 基线 $BASE%（覆盖率回吐，禁止合入）"
  exit 1
fi
echo "  PASS  覆盖率棘轮（$TOTAL% / 基线 $BASE%，只升不降）"
