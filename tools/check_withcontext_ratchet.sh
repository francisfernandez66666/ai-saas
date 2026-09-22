#!/usr/bin/env bash
# ============================================================
# 显式 DB ctx 透传棘轮（AI 链 ctx 批，2026-09-23 批六）
#
# 钉什么：业务代码里"裸 DB 句柄 + 显式 WithContext"的透传点数只升不降。
#   口径严格说是 **ctx 是否被显式传给 DB 句柄**（不论来源是请求 ctx、
#   后台任务的派生 ctx，还是 db.WithTenant 组出来的租户 ctx），
#   不区分种类——区分种类会让基线随主观判断漂移，测不出回退。
# 为什么用棘轮而不是断言某个值：请求侧 ctx 透传只能渐进
#   （一次全改会撞上 GORM 回调自动盖章链，且 AI/计费链路明确要求
#   "客户端断连不得拖死进行中的写入"，见 AGENTS §E3：那些点位**不该**透请求 ctx），
#   故"新增允许、回退禁止"——谁把 `db.DB.WithContext(x)` 改回裸 `db.DB.`，本门禁红。
# 与 G-12（.bare_db_baseline 裸 db.DB 白名单只降不升）的关系：
#   那把尺量"裸句柄总数（要降）"，这把尺量"其中显式带 ctx 的透传点（要升）"，
#   两者同向：把裸句柄换成 ctx 版会让两个门禁都变好，不会互相顶牛。
#   （SPEC 明写"只做 ctx 不做租户改写"，正是为避免与 G-12 打架。）
#
# 计数口径：cmd/ 与 internal/ 下非测试 .go 文件里的 `db.DB.WithContext(`，
#   排除 internal/db/（该包是句柄与 ctx 的提供方，计入会让基线失真）。
#   当前 4 点 = 3 处"后台任务显式租户 ctx"+1 处 mq 落库请求 ctx，
#   均为"无 ctx 就会丢归属/丢取消信号"的实证点位，非凑数。
# 用法：./tools/check_withcontext_ratchet.sh [--update-baseline] [--selftest]
# ============================================================
set -euo pipefail
cd "$(cd "$(dirname "$0")/.." && pwd)"
BASELINE_FILE=".withcontext_baseline"

# count 输出当前透传点总数（grep -c 逐文件相加；BSD grep 无 -oP，故按行计数）
count_ctx_points() {
  local total=0 n
  while IFS= read -r f; do
    [ -z "$f" ] && continue
    n=$(grep -cE '(^|[^[:alnum:]_])db\.DB\.WithContext\(' "$f" || true)
    [ -z "$n" ] && n=0
    total=$((total + n))
  done < <(grep -rlE '(^|[^[:alnum:]_])db\.DB\.WithContext\(' --include='*.go' cmd internal 2>/dev/null | grep -v '^internal/db/' | grep -v '_test\.go$' || true)
  echo "$total"
}

if [ "${1:-}" = "--selftest" ]; then
  # 自证：先按真实树计数，再断言"删掉一处即红、加回一处即绿"（不改动工作树，用临时副本）
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  mkdir -p "$tmp/cmd" "$tmp/internal"
  cp internal/api/chat_main.go "$tmp/internal/chat_main_copy.go"
  before=$(grep -cE '(^|[^[:alnum:]_])db\.DB\.WithContext\(' "$tmp/internal/chat_main_copy.go" || true)
  if [ "${before:-0}" -lt 1 ]; then
    echo "SELFTEST FAIL 取样文件不含透传点，自证无意义（口径已失效）"
    exit 1
  fi
  sed -i.bak 's/db\.DB\.WithContext(/db.DB.WhateverX(/g' "$tmp/internal/chat_main_copy.go"
  after=$(grep -cE '(^|[^[:alnum:]_])db\.DB\.WithContext\(' "$tmp/internal/chat_main_copy.go" || true)
  if [ "${after:-0}" -ge "$before" ]; then
    echo "SELFTEST FAIL 破坏样例后计数未下降（正则没在数该数的东西）"
    exit 1
  fi
  rm -f "$tmp/internal/chat_main_copy.go.bak"
  echo "SELFTEST PASS 透传点计数随删减下降（before=$before after=$after）"
  exit 0
fi

current=$(count_ctx_points)
if [ "${1:-}" = "--update-baseline" ]; then
  echo "$current" > "$BASELINE_FILE"
  echo "已写入基线 $BASELINE_FILE = $current"
  exit 0
fi

if [ ! -f "$BASELINE_FILE" ]; then
  echo "SKIP 无 $BASELINE_FILE 基线文件（首轮请 --update-baseline）"
  exit 0
fi
base=$(tr -cd '0-9' < "$BASELINE_FILE")
# 空基线按"未建立"处理（与覆盖率棘轮同一口径：缺数不得恒真放行）
if [ -z "$base" ]; then
  echo "FAIL 基线文件为空：请跑 ./tools/check_withcontext_ratchet.sh --update-baseline"
  exit 1
fi
if [ "$current" -lt "$base" ]; then
  echo "FAIL 显式 DB ctx 透传点数回退：当前 $current < 基线 $base"
  echo "     透传点只准增加（把裸 db.DB 换成带请求 ctx 的写法），不得回退。"
  echo "     确属合理重构请显式收紧基线：./tools/check_withcontext_ratchet.sh --update-baseline"
  exit 1
fi
echo "PASS 显式 DB ctx 透传棘轮：当前 $current >= 基线 $base（只升不降）"
