#!/usr/bin/env bash
# ============================================================
# 吞写库错误静态门禁（FIX-16，2026-09-29 审计批）
#
# 钉什么：资金与线索相关包里，**写库语句的结果被整段丢弃**的形态：
#   A. `_ = tx.Create(...)`／`_ = db.DB.Model(x).Updates(...)` —— 用空标识符明着扔；
#   B. 裸语句 `db.DB.…Create/Update/Updates/Save/Delete/Exec(…)`（行首就是接收者、
#      没有 `:=`、没有 `.Error`）—— GORM 返回 *gorm.DB，不取 .Error 就是零检错。
# 为什么是这两类而不是"所有 error 都要判"：本轮审计实锤的两起事故
# （推荐奖台账 `_ = tx.Create` 吞掉 → 回收腿找不到行、钱永久收不回；
#  留资画像 Updates 吞掉 → 日志报"留资成功"、webhook 外发假事件、线索静默蒸发）
# 恰好一 A 一 B，全部落在"对外宣称成功、对内没写进去"这一族——读语句丢结果
# （Scan/Find）最坏是读到空，语义不同，不在本闸范围（裸 db.DB 总量另有 G-12 棘轮管）。
#
# 作用域（只圈钱与线索路径，不搞全仓一刀切）：
#   internal/billing/、internal/webhook/、internal/chatflow/chat_lead*.go
# 白名单：行尾注释含 `db-swallow-ok: 理由` —— 理由非空是硬要求，
#   没有理由的白名单行本身判红（防止白名单退化成"绕过按钮"）。
# 自证 --selftest：注入 A/B 两种违例必须被抓、检错写法与注释行必须放行、
#   无理由白名单必须判红——守卫先证明自己咬得住，再上岗。
# 用法：bash tools/check_swallowed_db.sh [--selftest]
# ============================================================
set -uo pipefail
cd "$(cd "$(dirname "$0")/.." && pwd)"

# scan <file...>：输出 "文件:行号:内容" 的违例清单（不含 _test.go 与说明注释行）
scan_files() {
  local f a
  for f in "$@"; do
    case "$f" in *_test.go) continue ;; esac
    [ -f "$f" ] || continue
    # A 类：空标识符丢弃写结果。先去掉整行注释再匹配（文档注释里引用旧写法不算违例）。
    a=$(grep -nE '_ = .*\.(Create|Update|Updates|Save|Delete|Exec)\(' "$f" \
        | grep -vE '^[0-9]+:[[:space:]]*(//|\*)' \
        | grep -vE 'db-swallow-ok:[[:space:]]*[^[:space:]]' || true)
    [ -n "$a" ] && printf '%s\n' "${a}" | sed "s|^|${f}:|"
    # B 类：裸写语句。锚定行首接收者；同一条链里出现 `:=`/`return`/`.Error` 的都是已检错形态。
    grep -nE '^[[:space:]]*(db\.DB|tx|gdb)\.' "$f" \
      | grep -E '\.(Create|Update|Updates|Save|Delete|Exec)\(' \
      | grep -vE '(if |return|:=|\.Error)' \
      | grep -vE '^[0-9]+:[[:space:]]*(//|\*)' \
      | grep -vE 'db-swallow-ok:[[:space:]]*[^[:space:]]' \
      | sed "s|^|${f}:|" || true
  done
}

if [ "${1:-}" = "--selftest" ]; then
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  # 造一个合成样本：4 条违例（A×2 B×2）、3 条合规（if err／注释引用／带理由白名单）、1 条无理由白名单
  cat > "$tmp/sample.go" <<'EOF'
package sample

func bad() {
	_ = tx.Create(&model.RewardClaim{Note: "x"})
	db.DB.Model(d).Updates(map[string]interface{}{
		"status": "dead",
	})
	tx.Delete(&model.RewardClaim{}, claim.ID)
	gdb.Where("id = ?", 1).Save(&x) // db-swallow-ok:
}

func good() {
	// 旧写法 `_ = tx.Create(...)` 曾在这里吞掉台账错误——说明注释不是违例
	if err := tx.Create(&row).Error; err != nil {
		return err
	}
	q := db.DB.Where("id = ? AND status = 'suspended'", t.ID).Update("status", "expired")
	db.DB.Model(d).Update("grace_period_end_at", nil) // db-swallow-ok: 展示列写失败仅滞旧值，主流程不可逆
	_ = q
}
EOF
  out=$(scan_files "$tmp/sample.go")
  n=$(printf '%s\n' "$out" | grep -c ':' || true)
  # 期望恰抓 4 条：A 类 1 条 + B 类 3 条（含 1 条"无理由白名单"按违例计）
  if [ "${n:-0}" -ne 4 ]; then
    echo "SELFTEST FAIL 合成样本应判红 4 条（A1+B3，无理由白名单计违例），实判 ${n:-0}："
    printf '%s\n' "$out"
    exit 1
  fi
  for pat in 'RewardClaim{Note' 'db\.DB\.Model(d)\.Updates' 'tx\.Delete' '\.Save(&x) // db-swallow-ok:'; do
    if ! printf '%s\n' "$out" | grep -q "$pat"; then
      echo "SELFTEST FAIL 违例 [$pat] 未被点名（正则没在数该数的东西）:"
      printf '%s\n' "$out"
      exit 1
    fi
  done
  if printf '%s\n' "$out" | grep -qE '(if err := tx.Create|说明注释不是违例|滞旧值)'; then
    echo "SELFTEST FAIL 合规写法被误伤（检错/注释/带理由白名单必须放行）:"
    printf '%s\n' "$out"
    exit 1
  fi
  echo "SELFTEST PASS 吞错检查器命中/放行双向自证（违例 4 红、合规 0 伤）"
  exit 0
fi

hits=$( {
  scan_files internal/billing/*.go
  scan_files internal/webhook/*.go
  scan_files internal/chatflow/chat_lead.go internal/chatflow/chat_lead_split.go
} 2>/dev/null | grep -v '^$' || true)

if [ -n "$hits" ]; then
  echo "FAIL 吞写库错误（FIX-16 闸）：以下写库语句的结果被丢弃且不在带理由白名单内——"
  printf '%s\n' "$hits"
  echo "     修法：取 .Error 判错；确属有意旁路（不可逆主动作后的展示列等），在行尾写"
  echo "     // db-swallow-ok: <一句话理由>，无理由的白名单行本身判红。"
  exit 1
fi
echo "PASS 吞写库错误静态门禁：billing/webhook/chat_lead 作用域内 0 处吞错（含白名单核账）"
