#!/bin/bash
# 干净 PATH 包装器：剥离 WorkBuddy shim 目录（其中的 grep 是 toybox 实现，
# 不支持 BRE 交替 / -P / \s，会静默返回 0 命中或直接挂死），
# 以真实 BSD grep 语义运行 test_all.sh，保证 UAT 结论可信。
set -u
export PATH="/usr/local/go/bin:/opt/homebrew/bin:/Users/zhangzifei/.workbuddy/binaries/node/versions/22.22.2-3/bin:/Users/zhangzifei/.workbuddy/binaries/python/versions/3.13.12/bin:/usr/bin:/bin:/usr/sbin:/sbin:/Users/zhangzifei/.local/bin"
cd "$(dirname "$0")/.."
echo "[run_uat_clean] grep = $(command -v grep) / $(grep --version 2>&1 | head -1)"
echo "[run_uat_clean] go   = $(command -v go)"
echo "[run_uat_clean] args = $*"
exec ./tools/test_all.sh "$@"
