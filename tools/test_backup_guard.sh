#!/bin/bash
# ============================================================
# tools/backup.sh 的 RLS 预检与"半成品归档不留盘"判据自证（2026-09-28 FIX-A 后续）
#
# 为什么必须有这个文件：迁移 030 给租户表补上 ENABLE ROW LEVEL SECURITY 之后，
# `tools/backup.sh` 在本机当场炸了——pg_dump 会 `SET row_security = off`，而 PG 对
# "受策略约束的角色请求关 RLS"直接报错。backup.sh 加了预检（判不过就 exit 1 并给出修法）
# 和"未过校验就删掉半截归档"两条守卫，但**守卫本身也可能是空转的**：
#   ① 判据写成"恒红"，则备份从此永远不做，而日志看着像"守卫生效"；
#   ② 判据写成"恒绿"（比如 psql 读数取空当成 0），则它一句真话都没说过；
#   ③ "删半成品"那步挂在 trap 上，trap 里的变量拼错就静默什么都不删。
# 本项目的老教训就摆在那儿：旧 backup.sh 在缺 flock 的机器上"打一行 WARN 就 exit 0"，
# cron 天天看到成功而备份从未真跑。**一个只会打日志并返回成功的守卫等于没有守卫。**
#
# 四组用例全部用 stub 的 psql/pg_dump/pg_restore 驱动**真实的 backup.sh**（不连库、不碰生产）：
#   ① 反向：角色不可旁路 + 库里有已 ENABLE 的租户表 → 必须 exit≠0，且报出的是预检那句话
#   ② 正向对照：角色可旁路（超级用户/BYPASSRLS）→ 预检必须放行，整条链路跑到"全部完成"
#      （缺这组，①的"红"可能只是脚本被改成恒红）
#   ③ 半成品不留盘：pg_dump 写出非零文件后失败 → 归档文件必须被删掉
#      （实测本机那份半截 dump 是 **pg_restore --list 能解开的**——所以"文件存在且可解析"
#        根本不能作为备份成功的判据，这一组就是把这条教训钉住）
#   ④ 探测失败不拦：psql 打不通（读数取空）→ 只 WARN、继续尝试 dump
#      （把"探针失败"变成"永久不备份"是更坏的结果；真失败时 pg_dump 自己会响亮地报错）
#
# 变异检验（本文件对自己做的反证，2026-09-28 实跑）：把 backup.sh 复制成四个变体、各挖掉
# 一条守卫，用 BACKUP_SCRIPT=<变体> 跑本脚本，逐个确认"该红的红了"：
#   删掉预检的 exit 1        → ①①·补 双红（第一版只红 0 条：后面 du 失败把退出码补上了，
#                              这正是给 ① 补 STUB_DUMP_BYTES 与"没开始 dump"两条断言的原因）
#   去掉「有已 ENABLE 的表」腿 → ②·补 红（单向锁：库未通电的部署会被永久拦得没备份）
#   trap 里的 rm -f 换成 :    → ③ 红
#   探针失明分支改成 exit 1   → ④ 红（恒红守卫）
# 原版实测 PASS=6 FAIL=0。
#
# 用法：bash tools/test_backup_guard.sh                    （退出码 0=判据全对，1=有 FAIL）
#       BACKUP_SCRIPT=/tmp/变异体.sh bash tools/test_backup_guard.sh   （跑变异检验）
# ============================================================
set -u

PASS=0
FAILN=0
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$(mktemp -d)"
BIN="$WORK/bin"
mkdir -p "$BIN"

ok() { echo "  PASS  $1"; PASS=$((PASS + 1)); }
bad() { echo "  FAIL  $1"; FAILN=$((FAILN + 1)); }

# ---- stub 三件套：读数全部由环境变量注入，脚本本体不含任何真库调用 ----
cat >"$BIN/psql" <<'STUB'
#!/bin/bash
# 两次探测分别对应 backup.sh 的两条 SQL：角色属性 / 已 ENABLE 的租户表数。
# STUB_PSQL_RC≠0 表示"psql 本身打不通"——这时**一个字节都不输出**，
# 因为 backup.sh 用 `$(psql … || echo "")` 取数，边输出边失败会读到一个脏值而不是空。
if [ "${STUB_PSQL_RC:-0}" != "0" ]; then exit "$STUB_PSQL_RC"; fi
q="$*"
case "$q" in
  *rolsuper*) printf '%s\n' "${STUB_ROLE_FLAGS:-f}" ;;
  *relrowsecurity*) printf '%s\n' "${STUB_RLS_TABLES:-14}" ;;
  *) printf '\n' ;;
esac
exit 0
STUB

cat >"$BIN/pg_dump" <<'STUB'
#!/bin/bash
# 解析 --file=... 并按注入的字节数/退出码模拟"流式写了一半就失败"
out=""
for a in "$@"; do
  case "$a" in --file=*) out="${a#--file=}" ;; esac
done
if [ -n "$out" ] && [ "${STUB_DUMP_BYTES:-0}" -gt 0 ]; then
  head -c "${STUB_DUMP_BYTES}" /dev/zero >"$out"
fi
exit "${STUB_DUMP_RC:-0}"
STUB

cat >"$BIN/pg_restore" <<'STUB'
#!/bin/bash
exit "${STUB_RESTORE_RC:-0}"
STUB

chmod +x "$BIN/psql" "$BIN/pg_dump" "$BIN/pg_restore"

# run_case <用例名> 之后由调用方自己断言 $OUT/$RC；BACKUP_DIR 每次换新目录防陈旧产物干扰
BACKUP_DIR=""
OUT=""
RC=0
run_case() {
  BACKUP_DIR="$WORK/bd_$1"
  mkdir -p "$BACKUP_DIR"
  OUT="$WORK/out_$1"
  # HOME 指到空目录：防开发机上的 ~/.pgpass 或 ~/.env 影响判据；BACKUP_ENV_FILE 指向不存在的
  # .env，让本用例的读数**只来自显式注入的环境变量**（stub 的默认值就是注入值）。
  (
    cd "$ROOT" || exit 9
    PATH="$BIN:/usr/bin:/bin:/usr/sbin:/sbin"
    export HOME="$WORK/emptyhome"
    export BACKUP_ENV_FILE="$WORK/no-such.env"
    export BACKUP_DIR PGHOST=127.0.0.1 PGPORT=5432 PGUSER=stub PGDATABASE=stub_db
    export OPS_NOTIFY_DISABLED=1 BACKUP_NOTIFY_WEBHOOK=
    export STUB_ROLE_FLAGS="${STUB_ROLE_FLAGS:-f}"
    export STUB_RLS_TABLES="${STUB_RLS_TABLES:-14}"
    export STUB_PSQL_RC="${STUB_PSQL_RC:-0}"
    export STUB_DUMP_RC="${STUB_DUMP_RC:-0}"
    export STUB_DUMP_BYTES="${STUB_DUMP_BYTES:-0}"
    export STUB_RESTORE_RC="${STUB_RESTORE_RC:-0}"
    bash "${BACKUP_SCRIPT:-./tools/backup.sh}" >"$OUT" 2>&1
  )
  RC=$?
}

dump_count() { ls "$BACKUP_DIR"/*.dump 2>/dev/null | wc -l | tr -d ' '; }

echo "=== backup.sh 守卫自证（RLS 预检 / 半成品不留盘 / 探测失败不拦）==="

# ① 反向：不可旁路的角色 + 有已通电的表 → 预检必须拦下来
#
# ⚠ 这一组的 STUB_DUMP_BYTES=2048 是判据的一部分，不是随手给的：除了预检之外，
#   这条用例的其余环境必须「完全跑得通」。第一版这里没给字节数，于是拿"把预检的 exit 1 删掉"
#   做变异检验时，脚本照样在后面的 `du -h`（文件不存在）上失败、退出码照样非 0、①照样判
#   PASS——守卫被从中间掏空了却没人响。现在的四条断言各封一种掏空方式：
#     · 退出码非 0            → 拦不下来（恒绿守卫）
#     · 报出预检文案          → 红了，但不是因为这条闸（随便哪儿炸一下也算过不行）
#     · 输出里没有「开始备份」 → 拦点必须在 pg_dump **之前**（排在后面等于每天先 dump 坏再报错）
#     · 盘上没有归档          → 同上，从产物侧再核一遍
STUB_ROLE_FLAGS=f STUB_RLS_TABLES=14 STUB_DUMP_BYTES=2048 run_case a_block
if [ "$RC" -ne 0 ] && grep -q "既不是超级用户也没有 BYPASSRLS" "$OUT" && ! grep -q "开始备份" "$OUT"; then
  ok "① 不可旁路角色 + 14 张已 ENABLE 表 → 判红、报预检文案、且拦在 pg_dump 之前（exit=${RC}）"
else
  bad "① 预检没拦住（exit=${RC}，预检文案或拦点位置不对）——第二道闸通电后备份会天天半路炸"
fi
# 拦下来时不许已经落盘任何归档（预检排在 pg_dump 之前）
if [ "$(dump_count)" = "0" ]; then
  ok "①·补 预检拦下时 BACKUP_DIR 里没有归档文件"
else
  bad "①·补 预检失败却留下了归档（$(dump_count) 个）——说明判据排在了 dump 之后"
fi

# ② 正向对照：角色可旁路 → 预检放行，跑到"全部完成"
STUB_ROLE_FLAGS=t STUB_RLS_TABLES=14 STUB_DUMP_BYTES=2048 run_case b_allow
if [ "$RC" -eq 0 ] && grep -q "全部完成" "$OUT" && ! grep -q "既不是超级用户也没有 BYPASSRLS" "$OUT"; then
  ok "② 可旁路角色 → 放行并跑到「全部完成」（exit=${RC}）"
else
  bad "② 正向对照红（exit=${RC}）——①那条红很可能只是把守卫改成了恒红，整条备份链被误杀"
fi

# ②·补 表数为 0（库里根本没通电）也必须放行——防"只要不是超级用户就一律拦"
STUB_ROLE_FLAGS=f STUB_RLS_TABLES=0 STUB_DUMP_BYTES=2048 run_case b2_norls
if [ "$RC" -eq 0 ] && grep -q "全部完成" "$OUT"; then
  ok "②·补 未通电（已 ENABLE 表数=0）+ 普通角色 → 放行（RLS 不是拦备份的唯一条件）"
else
  bad "②·补 未通电也被拦（exit=${RC}）——预检的判据写成了单向锁，库还没通电的部署会永久没备份"
fi

# ③ 半成品不留盘：dump 写出 100 字节后失败 → 文件必须被删
STUB_ROLE_FLAGS=t STUB_DUMP_BYTES=100 STUB_DUMP_RC=1 run_case c_partial
if [ "$RC" -ne 0 ] && [ "$(dump_count)" = "0" ]; then
  ok "③ pg_dump 中途失败 → 非零半截归档被删掉、整体判红（exit=${RC}）"
else
  bad "③ 半截归档留在盘上（$(dump_count) 个，exit=${RC}）——「文件存在且可解析」会被当成备份成功，而它其实缺数据"
fi

# ④ 探测失败不拦：psql 打不通（非零退出且零输出）→ 只 WARN，继续尝试 dump
STUB_PSQL_RC=1 STUB_DUMP_BYTES=2048 run_case d_probeblind
if [ "$RC" -eq 0 ] && grep -q "探测失败" "$OUT" && grep -q "全部完成" "$OUT"; then
  ok "④ 探针取不到读数 → 只 WARN 不拦，仍跑完（把探针失败变成永久不备份是更坏的结果）"
else
  bad "④ 探针失败的处理口径不对（exit=${RC}）——既不能静默放行，也不能一律判红"
fi

rm -rf "$WORK"
echo "==== 结果: PASS=$PASS FAIL=$FAILN ===="
[ "$FAILN" -eq 0 ] || exit 1
exit 0
