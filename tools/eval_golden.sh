#!/usr/bin/env bash
# D1 发布阈值门禁（PLAN_FIX_2026-09-21）：AI 黄金问答集自动跑分 + 阈值卡口。
#
# 为什么需要它：AI 行为（路由判定 / 询价词表 / 话术评分口径）改动极易"看起来没事但实际改坏"。
# 本脚本把「改坏」变成一条可卡阈值的自动断言：通过率低于阈值即非零退出，阻断发布。
#
# 特点：**不联网、不读库、不烧 token**（只跑确定性层），可直接进每次提交的 CI 门禁。
# LLM 语义评分层需要真实模型 Key，由调用方进程内注入 golden.Judge，不在本脚本范围。
#
# 用法：
#   tools/eval_golden.sh                     # 默认阈值 95%
#   MIN_PASS=1.0 tools/eval_golden.sh        # 严格模式：全量必须通过
#   GOLDEN_SET=./my/cases tools/eval_golden.sh
#   REPORT=/tmp/g.md tools/eval_golden.sh
set -uo pipefail
cd "$(dirname "$0")/.."

MIN_PASS="${MIN_PASS:-0.95}"
REPORT="${REPORT:-GOLDEN_REPORT.md}"
GOLDEN_SET="${GOLDEN_SET:-}"

echo "==> AI 黄金问答集门禁（阈值通过率 ≥ ${MIN_PASS}）"

# grep 方言自证（PLAN_FIX_2026-09-21 A3）：本机命令执行环境可能注入 toybox grep，
# 它不支持 -P 且对不支持的正则静默返回 0 命中。这里用已知样本自证，不通就回退系统 grep。
probe_grep() {
  printf 'abc\n' | "$1" -E -o 'a|b' 2>/dev/null | grep -q a 2>/dev/null
}
GREP=grep
if ! probe_grep "$GREP"; then
  for cand in /usr/bin/grep /bin/grep; do
    if [ -x "$cand" ] && probe_grep "$cand"; then GREP="$cand"; break; fi
  done
fi

# ---- 0. 黄金集规模哨兵：用例数不得低于下限（防"删用例换绿色"）----
if [ -z "$GOLDEN_SET" ]; then
  COUNT="$(go run ./cmd/goldeneval -list 2>/dev/null | $GREP -cE '^[A-Z]+-[0-9]+')"
  COUNT="${COUNT:-0}"
  echo "    用例数：$COUNT"
  if [ "$COUNT" -lt 70 ]; then
    echo "    ✗ 黄金集仅 $COUNT 条，低于下限 70：守卫规模被削减" >&2
    exit 1
  fi
fi

# ---- 1. 跑分 + 阈值门禁 ----
set +e
GOFLAGS= go run ./cmd/goldeneval -min-pass "$MIN_PASS" -out "$REPORT" ${GOLDEN_SET:+-set "$GOLDEN_SET"}
RC=$?
set -e

case "$RC" in
  0) echo "    ✓ 门禁通过（报告：${REPORT}）" ;;
  1) echo "    ✗ 通过率低于阈值，阻断发布（报告：${REPORT}）" >&2 ;;
  2) echo "    ✗ 黄金集自身不合法，先修用例（守卫已失效）" >&2 ;;
  *) echo "    ✗ 跑分过程异常退出（RC=${RC}）" >&2 ;;
esac
exit "$RC"
