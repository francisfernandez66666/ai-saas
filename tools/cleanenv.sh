#!/bin/bash
# cleanenv.sh —— 干净 PATH 执行环境包装器（复核专用）
#
# 用途：本项目所在机器通过命令执行环境跑命令时，PATH 头部会被注入
#   /Applications/WorkBuddy.app/.../shim/brokered-bin
# 其中的 grep/sed 是 toybox 实现（不支持 -P / \s / \|，且**静默返回 0 命中**），
# 会让"扫描类"断言假绿/假红。本包装器把 PATH 重置为可信系统路径后再执行目标命令。
#
# 用法：  bash tools/cleanenv.sh <命令> [参数...]
# 例：    bash tools/cleanenv.sh ./tools/smoke.sh 9090
set -u
# 动态探测 node 版本目录，避免写死 22.22.2-3 在本机只有 22.22.2-6 时让 node/npm/npx 全部消失（FIXPLAN_2026-10-13 P2-12）
NODE_BIN=$(ls -d /Users/zhangzifei/.workbuddy/binaries/node/versions/*/bin 2>/dev/null | sort -V | tail -1)
if [ -z "${NODE_BIN:-}" ]; then
  NODE_BIN="/opt/homebrew/bin"  # 兜底：系统 homebrew node
fi
export PATH="/usr/local/go/bin:/opt/homebrew/bin:/opt/homebrew/sbin:${NODE_BIN}:/Users/zhangzifei/.workbuddy/binaries/python/versions/3.13.12/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
export LANG=en_US.UTF-8
export LC_ALL=en_US.UTF-8
cd "$(dirname "$0")/.."
exec "$@"
