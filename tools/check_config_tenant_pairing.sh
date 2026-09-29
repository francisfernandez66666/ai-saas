#!/usr/bin/env bash
# ============================================================
# 配置读写配对门禁（FIX-3，2026-09-29 审计批二）
#
# 钉什么：登记表内的键，写侧 admin/config（BatchUpdateForTenant）会落进
#   **租户覆盖层**（它们全部不在 PlatformLevelKeys）；读侧却存在只查系统层缓存的
#   GetInt/GetFloat/GetBool/GetString/GetInterval 与 SafeCfg* —— 于是"租户在后台
#   改了参数、判定代码永远读系统默认值"，配置挂着但永不生效。本门禁把这类读法判红。
# 为什么用结构而不是纪律：这类错位靠 review 抓不住——读写两侧隔着五六个包，
#   函数名只差一个 ForTenant 后缀；且系统层往往也有值，功能"看起来是好的"，
#   只有租户真的去覆盖时才暴露。审计复核批的同型教训（RLS 只 FORCE 不 ENABLE）
#   说的就是：**通电判据必须是机器对账，不是"我记得两侧一致"**。
# 登记表口径：只登记"本轮审计逐一核过写侧可达租户层、且读点已迁 *ForTenant"的键；
#   新键要进表，先迁读点再进表，勿先进表把门禁卡死。
# 例外写法：确属"平台级一次性读取"的行，在行尾加 `cfg-system-ok:<理由>` 注记，
#   门禁认理由不认沉默——与 G-12 裸 db.DB 白名单同一纪律。
# 用法：./tools/check_config_tenant_pairing.sh [--selftest]
# ============================================================
set -euo pipefail
cd "$(cd "$(dirname "$0")/.." && pwd)"

# 登记表：租户作用域键（写侧可落 tenant_id>0 覆盖层）
KEYS="reply_delay_mode simple_msg_delay store_visit_first_delay store_visit_second_delay \
merge_window_seconds human_timeout_seconds assigned_lead_ai_timeout assigned_lead_ai_auto_reply \
stage_step_enabled stage_max_increment hook_rate_stage1_threshold force_stage0_attempts \
theta_trust theta_rounds theta_hook_rate_crit theta_l3_intent theta_l3_rounds \
theta_urgency_l1 theta_urgency_l2 theta_hookrate_low theta_silent"

# 扫描面：cmd/ 与 internal/ 非测试 .go，排除 internal/runtimecfg（该包是取值层提供方，
#   GetInt/GetInterval 本体就在这里，计入等于让门禁红在自己身上）
scan_files() {
  grep -rlE '\.(Get(Int|Float|Bool|String|Interval)|SafeCfg(Int|Float|Bool|String))\(' \
    --include='*.go' cmd internal 2>/dev/null \
    | grep -v '_test\.go$' | grep -v '^internal/runtimecfg/' || true
}

# 只查系统层的读法（*ForTenant 第二参才是键名，不会被本正则命中）
plain_read_re='(Get(Int|Float|Bool|String|Interval)|SafeCfg(Int|Float|Bool|String))\([[:space:]]*"(KEY)"'

scan_hits() { # $1: 要扫描的文件清单（换行分隔）
  local files="$1" key re
  for key in $KEYS; do
    re=${plain_read_re//KEY/$key}
    # 行号输出供报告；随后滤掉注释行与带理由的豁免行
    # -H 强制单文件也带路径前缀，使输出格式恒为 `path:num:content`（注释过滤与报告都依赖它）
    # shellcheck disable=SC2086
    grep -nHE "$re" $files 2>/dev/null \
      | grep -vE '^[^:]+:[0-9]+:[[:space:]]*(//|\*)' \
      | grep -v 'cfg-system-ok:[^[:space:]]' || true
  done
}

if [ "${1:-}" = "--selftest" ]; then
  # 自证：六形态样本必须"该红的恰红、该绿的零红"。
  # 检查器与语言形态失配时会**静默返回空清单**、门禁恒绿——所以正向命中必须先证能抓到。
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  mkdir -p "$tmp/internal/x"
  cat > "$tmp/internal/x/sample.go" <<'EOF'
package x

func bad() {
	runtimecfg.DefaultSystemConfigService.GetInt("human_timeout_seconds", 180)
	timeout := runtimecfg.SafeCfgInt("theta_trust", 0)
	_ = runtimecfg.DefaultSystemConfigService.GetInterval("store_visit_second_delay", 25, 45)
}

func good() {
	runtimecfg.DefaultSystemConfigService.GetIntForTenant(tid, "human_timeout_seconds", 180)
	runtimecfg.DefaultSystemConfigService.GetStringForTenant(tid, "reply_delay_mode", "normal")
	runtimecfg.DefaultSystemConfigService.GetIntervalForTenant(tid, "store_visit_second_delay", 25, 45)
	runtimecfg.SafeCfgInt("kb_rerank_candidates", 8)
	runtimecfg.SafeCfgInt("human_timeout_seconds", 0) // cfg-system-ok:超管平台视图确属"只读系统层默认"的一次性读取
	// runtimecfg.DefaultSystemConfigService.GetString("reply_delay_mode", "normal") 这行是注释，不该红
}
EOF
  # 用自造清单驱动 scan_hits（不走 scan_files，避免自证依赖真实树）
  hits=$(scan_hits "$tmp/internal/x/sample.go" | sed -E 's#^[^:]+:[0-9]+:##; s/^[[:space:]]+//' | grep -v '^$' || true)
  n=$(printf '%s\n' "$hits" | grep -c . || true)
  if [ "${n:-0}" -ne 3 ]; then
    echo "SELFTEST FAIL 样本应恰抓 3 红（GetInt/SafeCfgInt/GetInterval 各一），实抓 $n："
    printf '%s\n' "$hits"
    exit 1
  fi
  echo "$hits" | grep -q 'human_timeout_seconds' || { echo "SELFTEST FAIL 未抓到 GetInt(human_timeout_seconds)"; exit 1; }
  echo "$hits" | grep -q 'SafeCfgInt("theta_trust' || { echo "SELFTEST FAIL 未抓到 SafeCfgInt(theta_trust)"; exit 1; }
  echo "$hits" | grep -q 'GetInterval("store_visit_second_delay' || { echo "SELFTEST FAIL 未抓到 GetInterval(store_visit_second_delay)"; exit 1; }
  # 反向：四条合法形态（*ForTenant / 未登记键 / 带理由豁免 / 注释行）不得混进红单
  if printf '%s\n' "$hits" | grep -qE 'ForTenant|kb_rerank_candidates|cfg-system-ok'; then
    echo "SELFTEST FAIL 绿样本被误判红"
    exit 1
  fi
  echo "SELFTEST PASS 配对门禁命中/放行双向自证（3 红 0 误伤）"
  exit 0
fi

files=$(scan_files)
if [ -z "$files" ]; then
  echo "FAIL 扫描面为空：口径失配会让门禁恒绿，这里按缺陷处理"
  exit 1
fi
hits=$(scan_hits "$files")
if [ -n "$hits" ]; then
  echo "FAIL 登记表内的租户作用域键存在「只查系统层」的读点（租户覆盖永不被咨询）："
  echo "$hits" | sed 's/^/  /'
  echo "     修法：改走 GetXxxForTenant(tenantID, …)（lookupTenant 自动回落系统层，行为对未覆盖租户零变化）；"
  echo "     确属平台级一次性读取，行尾加 cfg-system-ok:<理由>。"
  exit 1
fi
echo "PASS 配置读写配对：登记表 $(echo $KEYS | wc -w | tr -d ' ') 键无非 ForTenant 系统层读点"
