#!/bin/bash
# ============================================================
# 每日运维自检（FIX-7 运维批，2026-09-27）
#
# 这一段的由来是本批那句教训的原话：**「只会打日志并返回成功的守卫等于没有守卫」**。
# 本仓已经在备份上踩过一次——旧 backup.sh 在缺 flock 的机器上 exit 127 落进"没抢到锁"分支，
# 打一行 WARN 就 exit 0，于是备份从未真跑而 cron 天天看到成功。backup.sh 现在自己会校验产物，
# 但那只能证明"跑起来的那一次没问题"，**证明不了"今天真的跑了"**：cron 没装、机器停机、
# 镜像换了个没有 cron 的基础镜像，产物校验一次都不会被执行。
# 所以自检必须站在调度器**外面**看产物，而不是站在里面看日志。
#
# 三件事，全部只读（不改数据、不动调度器、不打印任何密钥值）：
#   ① 备份产物真实性：backups/ 里当天（或 26h 内）的 .dump 必须存在、非零、pg_restore --list 可解析；
#   ② 上线前置校验：生产机（APP_ENV=prod）跑 deploy_preflight；非生产机显式 SKIP（不伪装绿）；
#   ③ 判据自证：deploy_preflight 自己的反向用例（多实例段该红的必须红）——守卫的守卫。
#
# 失败怎么出去：任一项 FAIL 即推 webhook（OPS_NOTIFY_WEBHOOK，回落 .env 的 wecom_webhook_url /
# WECOM_WEBHOOK_URL），标题带主机名与失败清单。webhook 地址本身含密钥，**只判空不回显**。
#
# 用法：./tools/ops_daily.sh              （退出码 0=全绿，1=有 FAIL）
#       ./tools/ops_daily.sh --quiet      （只打印结论行，适合塞进别的脚本）
#       ./tools/ops_daily.sh --backup-only（只跑①备份产物段，给反向用例用，见 tools/test_ops_daily.sh）
# 装法（**故意不由本脚本自己写 crontab**——改调度器属机器级变更，须人工确认）：
#   crontab -l 2>/dev/null | grep -v ops_daily > /tmp/ct && \
#   echo '10 3 * * * cd /srv/ai-scrm-v2 && ./tools/ops_daily.sh >> /var/log/ai-scrm-ops.log 2>&1' >> /tmp/ct && \
#   crontab /tmp/ct && rm /tmp/ct
# DEPLOY_CHECKLIST 里应把这一条列为上线必做项（与 backup.sh 同日生效）。
# ============================================================
set -u
cd "$(dirname "$0")/.."

QUIET=0
BACKUP_ONLY=0
for a in "$@"; do
  case "$a" in
    --quiet) QUIET=1 ;;
    # 只跑①段：给 tools/test_ops_daily.sh 的反向用例用。整脚本跑会连带调 deploy_preflight
    # 与其 6 例自证（那些有自己的门禁位），反向用例只关心"备份产物判据有没有牙"。
    --backup-only) BACKUP_ONLY=1 ;;
    # ${a} 花括号非装饰：macOS bash 3.2 在 UTF-8 locale 下会把紧跟的多字节标点吞进变量名，set -u 下当场 unbound
    *) echo "未知参数: ${a}（可用 --quiet / --backup-only）" >&2; exit 2 ;;
  esac
done
HOST_TAG=$(hostname 2>/dev/null || echo unknown-host)
BACKUP_DIR="${BACKUP_DIR:-backups}"
PASS=0 FAIL=0 SKIPN=0
LINES_FAIL=""

say() { [ "$QUIET" = "1" ] || echo "$@"; }
ok() { say "  PASS  $1"; PASS=$((PASS+1)); }
bad() { say "  FAIL  $1"; FAIL=$((FAIL+1)); LINES_FAIL="$LINES_FAIL
  - $1"; }
skip() { say "  SKIP  $1"; SKIPN=$((SKIPN+1)); }

env_get() { grep -E "^[[:space:]]*$1[[:space:]]*=" .env 2>/dev/null | tail -1 | cut -d= -f2- | tr -d '"' | tr -d ' '; }

# 告警通道：优先级 OPS_NOTIFY_WEBHOOK > BACKUP_NOTIFY_WEBHOOK > .env 的 wecom_webhook_url/WECOM_WEBHOOK_URL
WH="${OPS_NOTIFY_WEBHOOK:-${BACKUP_NOTIFY_WEBHOOK:-}}"
[ -z "$WH" ] && WH="$(env_get wecom_webhook_url)$(env_get WECOM_WEBHOOK_URL)"
notify() { # $1=标题后缀（只传结论，不传密钥）
  # OPS_NOTIFY_DISABLED=1 只 suppress 外发、不 suppress 判定（判据照常跑、照常红）。
  # 这一条是给**反向用例**留的口子：tools/test_ops_daily.sh 会故意造三种坏备份产物来断言本脚本
  # 判红，如果判红就真往线上运维群推消息，那"给守卫配的测试"本身就成了骚扰源（而且第一次跑
  # 测试的人不会知道会有真消息发出去）。人工在开发机上手动试跑同样该带上它。
  if [ "${OPS_NOTIFY_DISABLED:-0}" = "1" ]; then
    say "  注：OPS_NOTIFY_DISABLED=1，本次结论只落 stdout（不外发）"
    return 0
  fi
  [ -z "$WH" ] && { say "  注：未配置告警 webhook，本次结论只落 stdout（运维需主动看日志）"; return 0; }
  say "  → 已推送告警到 webhook（地址略）"
  # 用 python 做 JSON 转义：markdown 文本里有冒号/换行，手拼必炸
  printf '%s' "$1" | python3 -c '
import json, sys, os, urllib.request
text = sys.stdin.read()
url = os.environ.get("WH_FOR_OPS") or ""
if not url:
    raise SystemExit
body = json.dumps({"msgtype": "markdown", "markdown": {"content": text}}).encode()
req = urllib.request.Request(url, data=body, headers={"Content-Type": "application/json"})
try:
    urllib.request.urlopen(req, timeout=8)
except Exception as e:
    print("  告警推送失败（结论仍在本机日志里）:", type(e).__name__)
' 
}

say "==== 每日运维自检（host=${HOST_TAG}） ===="

# ---- ① 备份产物真实性（站在 cron 外面看） ----
say "-- 1) 备份产物"
if [ ! -d "$BACKUP_DIR" ]; then
  bad "备份目录 $BACKUP_DIR 不存在（backup.sh 从未跑过，或 BACKUP_DIR 口径不一致）"
else
  LATEST=$(ls -t "$BACKUP_DIR"/ai_scrm_*.dump 2>/dev/null | head -1)
  if [ -z "${LATEST:-}" ]; then
    bad "$BACKUP_DIR 里没有任何 .dump——cron 没装/没跑/跑了就失败，三者之一"
  else
    AGE_S=$(( $(date +%s) - $(stat -f %m "$LATEST" 2>/dev/null || stat -c %Y "$LATEST" 2>/dev/null || echo 0) ))
    AGE_H=$(( AGE_S / 3600 ))
    if [ "$AGE_H" -ge 26 ]; then
      bad "最新备份已 $AGE_H 小时前（$(basename "$LATEST")）——每日任务没跑或天天失败"
    else
      ok "最新备份距今 ${AGE_H}h（$(basename "$LATEST")）"
    fi
    SIZE=$(wc -c < "$LATEST" 2>/dev/null | tr -d ' ')
    if [ "${SIZE:-0}" -lt 1024 ]; then
      bad "最新备份只有 ${SIZE} 字节（<1KB 的 custom dump 不是可用备份）"
    else
      ok "最新备份大小 ${SIZE} 字节（非零、不是截断的空归档）"
    fi
    if pg_restore --list "$LATEST" >/dev/null 2>&1; then
      ok "pg_restore --list 可解析（归档结构完好，真能还原）"
    else
      bad "pg_restore --list 解析失败——这个 .dump 是废文件，恢复时会才发现"
    fi
  fi
fi

# ---- ② 上线前置校验（生产机才判，非生产机显式 SKIP） ----
if [ "$BACKUP_ONLY" = "1" ]; then
  say "-- 2)/3) --backup-only：本次跳过（反向用例只判①段）"
else
say "-- 2) 上线前置校验"
if [ "$(env_get APP_ENV)" = "prod" ]; then
  if PF_OUT=$(bash tools/deploy_preflight.sh 2>&1); then
    ok "deploy_preflight 全绿"
  else
    bad "deploy_preflight 有 FAIL 项：$(printf '%s' "$PF_OUT" | grep -c '  FAIL ') 条"
    say "$(printf '%s' "$PF_OUT" | grep '  FAIL ' | sed 's/^/        /')"
  fi
else
  skip "非生产机（APP_ENV!='$(env_get APP_ENV)'）：preflight 的闸门判据不适用，不跑不判"
fi

# ---- ③ 守卫的守卫：preflight 判据自证（与机器环境无关，恒应通过） ----
say "-- 3) preflight 判据自证"
if ST_OUT=$(bash tools/test_deploy_preflight.sh 2>&1); then
  ok "deploy_preflight 反向用例全过（多实例段该红的真的会红）"
else
  bad "deploy_preflight 反向用例失败——判据被改弱或改坏了（详见输出）"
  say "$(printf '%s' "$ST_OUT" | grep -E 'FAIL|结果' | sed 's/^/        /')"
fi
fi

say ""
# 结论行不受 --quiet 抑制：脚本头承诺的是"只打印结论行"，全静音就没有意义了
# （塞进别的脚本时至少要能 grep 到 PASS/FAIL/SKIP 计数）。
echo "==== 结果: PASS=$PASS FAIL=$FAIL SKIP=$SKIPN ===="

if [ "$FAIL" != "0" ]; then
  # 地址只在本次推送的子进程环境里存在，不落文件、不回显（webhook URL 本身含密钥）
  WH_FOR_OPS="$WH"; export WH_FOR_OPS
  notify "**AI-SCRM 运维自检失败（${HOST_TAG}）**
$(date '+%F %T')$LINES_FAIL"
  unset WH_FOR_OPS
  exit 1
fi
exit 0
