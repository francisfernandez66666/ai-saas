#!/usr/bin/env bash
# ============================================================
# check_ai_decoupling.sh — FIX-14 结构锁：internal/ai 的依赖闭包不得含 service
#
# 为什么要有这个门禁（而不是一次修完就算）：
# internal/ai 是 AI 底座层，历史上因 prompt_builder import 了
# internal/service 的 7 个行业语义读取函数，把 service → mq/notify/metrics
# 整棵业务编排树拖进了它的依赖闭包（六层分层里底座层反向认识业务层）。
# FIX-14(2026-09-29) 已把真源抽到叶子包 internal/industrycfg，ai 与 service
# 各自单向 import 那里。但"方向回正"这种性质没有任何编译器强制——
# 下一个人图省事在 prompt_builder 里再写一行 `service.XXX`，闭包立刻复原，
# 而且功能照样跑得通，只有分层被悄悄蛀穿。所以把它钉成机器判据：
#   `go list -deps ./internal/ai` 的结果中出现 `ai-scrm/internal/service` 即 FAIL。
#
# 判据选择说明：用 `go list -deps`（传递闭包）而不是只 grep ai 包源码的 import——
# 直接 import 只是闭包的一条腿，经 cache/notify 这类中转的环同样把业务层拖进来；
# 闭包判据对"新写直接引用"和"引了一个背后挂着 service 的新包"两种倒退都判红。
#
# 守卫自证（反证组，不往仓库里植入坏代码）：检查逻辑抽成纯函数 check_deps()，
# 自证用合成 deps 文本喂给它——含 service 行必须被点名、干净闭包必须放行。
# 缺这组的话，"grep 写错永远不命中"这种空转守卫将无法被发现（阶段零惯例）。
#
# 用法：bash tools/check_ai_decoupling.sh [--selftest]
# 退出码：0=通过 / 非0=违反结构锁或自证失败
# ============================================================
set -euo pipefail
cd "$(dirname "$0")/.."

# check_deps <deps 文本>：命中 ai-scrm/internal/service 输出违规并返回 1，否则返回 0
check_deps() {
	local deps="$1"
	if printf '%s\n' "$deps" | grep -qx 'ai-scrm/internal/service'; then
		echo "FAIL: internal/ai 依赖闭包中出现 ai-scrm/internal/service" >&2
		echo "  → AI 底座层反向依赖业务编排层（违反六层分层/FIX-14 结构锁）" >&2
		echo "  → 行业语义/包 prompt 读取请走叶子包 internal/industrycfg，勿直接 import service" >&2
		return 1
	fi
	return 0
}

if [ "${1:-}" = "--selftest" ]; then
	# 反证组①：合成闭包里埋一行 service，检查器必须点名（缺它=grep 写错也永远绿的空转守卫）
	if check_deps $'ai-scrm/internal/model\nai-scrm/internal/service\nai-scrm/internal/ai' 2>/dev/null; then
		echo "SELFTEST FAIL: 合成违规闭包未被点名（检查器失明）"
		exit 1
	fi
	# 反证组②：干净合成闭包必须放行（防把守卫写成恒红——恒红的锁同样等于没有锁）
	if ! check_deps $'ai-scrm/internal/model\nai-scrm/internal/industrycfg\nai-scrm/internal/ai' 2>/dev/null; then
		echo "SELFTEST FAIL: 干净闭包被误判违规（恒红锁）"
		exit 1
	fi
	echo "SELFTEST PASS=2 FAIL=0"
	exit 0
fi

# 前置自检：go list 必须先真的产出闭包文本——空读（如 go 环境坏）会让 grep 永不命中、
# 在空集上假绿，这是阶段零反复钉过的"落库前置自检"同一形态。
deps="$(go list -deps ./internal/ai 2>/dev/null || true)"
if [ -z "$deps" ]; then
	echo "FAIL: go list -deps ./internal/ai 无输出（环境/编译错误），判据不可信"
	exit 1
fi
# 自检第二条：闭包必须含 ai 自己，否则拿到的不是目标包的依赖集
if ! printf '%s\n' "$deps" | grep -qx 'ai-scrm/internal/ai'; then
	echo "FAIL: 闭包中不含 ai-scrm/internal/ai 本体，go list 读数不可信"
	exit 1
fi

if check_deps "$deps"; then
	echo "PASS: internal/ai 依赖闭包不含 service（FIX-14 结构锁在场）"
	exit 0
fi
exit 1
