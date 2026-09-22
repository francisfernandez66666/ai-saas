#!/usr/bin/env bash
# G-12 白名单棘轮门禁（P2-6，2026-09-22）：裸 db.DB 用法「白名单外计数」只降不升。
#
# 背景：原 test_all.sh 内联 G-12 段两个分支均硬编码 verdict 0，结构上不可能失败（假绿门禁）。
# 本脚本配合 tools/classify_bare_db.py 的白名单分类（A–F + g12:platform 豁免），
# 对「白名单外计数」做基线棘轮：超过基线即 FAIL（新增违规），等于或低于基线 PASS。
#
# 用法：
#   tools/check_bare_db.sh                    # 门禁检查（CI/阶段零用）
#   tools/check_bare_db.sh --update-baseline  # 收紧基线为当前计数（人工确认后执行）
#   tools/check_bare_db.sh --selftest         # 先跑分类脚本自证，再进门禁
set -u

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
BASELINE_FILE="$SCRIPT_DIR/../.bare_db_baseline"

# 先自证分类逻辑本身可用（防假绿门禁重演：分类器坏了门禁等于没装）
if ! python3 "$SCRIPT_DIR/classify_bare_db.py" --selftest >/dev/null 2>&1; then
  echo "  FAIL  G-12: classify_bare_db.py --selftest 未通过（分类器异常）"
  exit 1
fi

COUNT=$(python3 "$SCRIPT_DIR/classify_bare_db.py" 2>/dev/null | tail -1 | /usr/bin/grep -oE '[0-9]+' | head -1)
if [ -z "$COUNT" ]; then
  echo "  FAIL  G-12: 无法从 classify_bare_db.py 取得白名单外计数"
  exit 1
fi

if [ "${1:-}" = "--update-baseline" ]; then
  printf '%s\n' "$COUNT" > "$BASELINE_FILE"
  echo "  OK    G-12 基线已更新为 $COUNT"
  exit 0
fi

BASE=$(cat "$BASELINE_FILE" 2>/dev/null | tail -1)
if [ -z "$BASE" ]; then
  echo "  FAIL  G-12: 缺少基线文件 .bare_db_baseline（先运行 --update-baseline 落初始基线）"
  exit 1
fi

if [ "$COUNT" -gt "$BASE" ]; then
  echo "  FAIL  G-12: 白名单外裸 db.DB $COUNT 处 > 基线 ${BASE}（新增违规，禁止合入）"
  echo "        明细：python3 tools/classify_bare_db.py 查看分类；确属合法用法请归入白名单类别或加 // g12:platform 豁免"
  exit 1
fi
echo "  PASS  G-12（白名单外 $COUNT / 基线 ${BASE}，只降不升）"
