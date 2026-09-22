#!/usr/bin/env bash
# ============================================================
# 工作树卫生护栏（O2 配套，2026-09-23 批六）
#
# 为什么存在：本轮开发实际踩到三类"仓库级"事故，全都不会被 go test / 冒烟脚本抓到，
# 却会在下一次接手时把人带偏（本会话就因此对同一处代码重复施工一轮）：
#   1. 交付物未跟踪 —— 新建的 tools/*.sh、*.go 停在 git untracked 状态，
#      文档里写着"已接入回归门禁"，仓库里却查无此物；
#   2. 批量编辑中间产物残留 —— `.bak.19700101000000` 这类备份文件留在树里，
#      其中是被证伪的旧实现（含 panic 行），留着就是误导源；
#   3. 异常权限 —— 并发生成的源文件落成 0600，换用户/CI 检出后读不到。
#
# 口径：只检查"可机器判定的仓库事实"，不做风格评判；任何一条命中即 FAIL 并列出清单。
# 依赖：git（缺失时 SKIP 退出 0——缺工具不得伪装成代码问题，与覆盖率棘轮同一口径）。
# 用法：./tools/check_worktree_hygiene.sh [--allow-untracked]
#   --allow-untracked 只跑 2/3 两项（本轮批量施工期临时用，收口前必须去掉）。
# ============================================================
set -uo pipefail
cd "$(cd "$(dirname "$0")/.." && pwd)"

rc=0
if ! command -v git >/dev/null 2>&1; then
  echo "SKIP 未找到 git，工作树卫生检查无法执行（非代码问题）"
  exit 0
fi

# ---------- 1. 未跟踪的可交付文件（*.go / tools/*.sh / 迁移 SQL / compose 编排） ----------
# 只看"会被编译或执行"的产物类型；文档与本地笔记（*.md 已在 .gitignore）不计。
untracked=$(git ls-files --others --exclude-standard 2>/dev/null | grep -E '(\.go|\.sh|\.sql|\.yml|\.yaml|\.json|\.mjs|\.ts|\.tsx)$' | grep -v '^frontend-react/node_modules/' || true)
if [ "${1:-}" = "--allow-untracked" ]; then
  n=$(printf '%s' "$untracked" | grep -c . || true)
  echo "INFO 跳过未跟踪检查（--allow-untracked），当前未跟踪可交付 ${n:-0} 个"
else
  if [ -n "$untracked" ]; then
    echo "FAIL 存在未跟踪的可交付文件（工具/代码/编排写了却没入库，等于不存在）："
    printf '%s\n' "$untracked" | sed 's/^/  - /'
    echo "     请 git add 上述文件，或确属本地产物则移出仓库目录。"
    rc=1
  else
    echo "OK   未跟踪可交付文件：0"
  fi
fi

# ---------- 2. 批量编辑中间产物残留 ----------
# .bak / 带时间戳的 .bak.* / .orig / .rej / editor 交换文件：一律视为事故残留。
junk=$(git ls-files --others --exclude-standard 2>/dev/null | grep -E '(\.bak(\.[^/]*)?$|\.orig$|\.rej$|^\.#|\.swp$|~$)' || true)
tracked_junk=$(git ls-files 2>/dev/null | grep -E '(\.bak(\.[^/]*)?$|\.orig$|\.rej$)' || true)
if [ -n "$junk$tracked_junk" ]; then
  echo "FAIL 工作树残留批量编辑中间产物（内含被证伪的旧代码，留着即误导源）："
  printf '%s\n' "$junk$tracked_junk" | sed 's/^/  - /'
  rc=1
else
  echo "OK   批量编辑中间产物残留：0"
fi

# ---------- 3. 源文件权限 ----------
# 源码/脚本必须是同组可读（0644/0755 族）：0600 通常是并发写的事故痕迹，CI 换用户即读不到。
# 谓词写成"缺少 g+r 或缺少 o+r"的并集（`! -perm -X` 是"该位未设"），
# 切勿写成 `-perm ,g=r,o=r`（那是"任一位已设"的取反语义陷阱，0600 反而漏过——已实跑封堵）。
perm=$(find cmd internal pkg config tools migrations frontend-react/src -type f \
  \( -name '*.go' -o -name '*.sh' -o -name '*.ts' -o -name '*.tsx' -o -name '*.sql' -o -name '*.py' -o -name '*.mjs' \) \
  ! -path '*/node_modules/*' \( ! -perm -g+r -o ! -perm -o+r \) 2>/dev/null | head -40 || true)
if [ -n "$perm" ]; then
  echo "FAIL 以下源文件权限缺少组/其他读位（并发写事故痕迹，换用户即读不到）："
  printf '%s\n' "$perm" | sed 's/^/  - /'
  echo "     修法：chmod 644 <文件>（可执行脚本用 755）"
  rc=1
else
  echo "OK   源文件权限位：全部至少 0644"
fi

if [ $rc -eq 0 ]; then
  echo "PASS 工作树卫生护栏"
else
  echo "FAIL 工作树卫生护栏（见上方清单）"
fi
exit $rc
