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
# 五组用例全部用 stub 的 psql/pg_dump/pg_restore 驱动**真实的 backup.sh**（不连库、不碰生产）：
#   ① 反向：角色不可旁路 + 库里有已 ENABLE 的租户表 → 必须 exit≠0，且报出的是预检那句话
#   ② 正向对照：角色可旁路（超级用户/BYPASSRLS）→ 预检必须放行，整条链路跑到"全部完成"
#      （缺这组，①的"红"可能只是脚本被改成恒红）
#   ③ 半成品不留盘：pg_dump 写出非零文件后失败 → 归档文件必须被删掉
#      （实测本机那份半截 dump 是 **pg_restore --list 能解开的**——所以"文件存在且可解析"
#        根本不能作为备份成功的判据，这一组就是把这条教训钉住）
#   ④ 探测失败不拦：psql 打不通（读数取空）→ 只 WARN、继续尝试 dump
#      （把"探针失败"变成"永久不备份"是更坏的结果；真失败时 pg_dump 自己会响亮地报错）
#   ⑤ 凭据快照随归档留存（.env 丢失批 2026-09-28）：归档里的通道凭据是用 JWT_SECRET 派生的
#      AES 密钥加密的，"只备份库、不备份 .env"恢复出来的就是一库解不开的密文——本机 09-28
#      正是这么丢的（旧密钥无处可寻，那一行旧密文永久作废）。五条断言各钉一件事：
#      留了 / 恰一份 / 权限 0600 / 内容与源逐字相同 / 值零回显；
#      ⑤反 源 .env 不在场时必须只 WARN 并跑完（缺快照不该把唯一能救命的 dump 一起判红），
#      且**不许**留下空快照（0 字节的 .env 会让人以为有）；
#      ③·补 顺带钉住"快照排在完整性校验之后"：dump 失败的那一轮连凭据都不许单独落盘。
#
# 变异检验（本文件对自己做的反证，2026-09-28 实跑）：把 backup.sh 复制成六个变体、各挖掉
# 一条守卫，用 BACKUP_SCRIPT=<变体> 跑本脚本，逐个确认"该红的红了"：
#   删掉预检的 exit 1        → ①①·补 双红（第一版只红 0 条：后面 du 失败把退出码补上了，
#                              这正是给 ① 补 STUB_DUMP_BYTES 与"没开始 dump"两条断言的原因）
#   去掉「有已 ENABLE 的表」腿 → ②·补 红（单向锁：库未通电的部署会被永久拦得没备份）
#   trap 里的 rm -f 换成 :    → ③ 红
#   探针失明分支改成 exit 1   → ④ 红（恒红守卫）
#   快照落盘换成 `if :`（只打"已留存"日志）→ ⑤·补 红 3 条，而⑤那条**照绿**——
#                              这条最说明问题：日志文案不能作为备份做了的判据，
#                              所以 ⑤ 的重量全压在"盘上有几个文件/什么权限/字节是否相同"上
#   chmod 600 改成 644        → ⑤·补 权限那条红（备份目录常被别的账号或备份代理读走）
# 原版实测 PASS=14 FAIL=0。
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

# 合成"形如 .env"的凭据文件（⑤ 用）：值是本用例自造的假密钥，唯一作用是能被"零回显"断言抓到。
FAKE_JWT="STUBJWTDONTLEAK0123456789"
printf 'JWT_SECRET=%s\nDB_HOST=stub-host-only\n' "$FAKE_JWT" >"$WORK/fake.env"

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
    # 凭据来源默认指向不存在的 .env（读数只来自显式注入的环境变量）；
    # ⑤ 那两组用 CASE_ENV_FILE 递一份"形如 .env"的合成文件，测的是"快照随归档留存"这条新守卫。
    export BACKUP_ENV_FILE="${CASE_ENV_FILE:-$WORK/no-such.env}"
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
env_count() { ls "$BACKUP_DIR"/*.env 2>/dev/null | wc -l | tr -d ' '; }
# 快照权限（FIX-13，2026-09-29）：改用 stat 的**数字模式**直接比对，不再拿 `ls -l` 的权限串做等值。
# 现场教训（本反证组自己把自己判红过一次）：macOS 上带 xattr 的文件 `ls -l` 权限串末尾多一个 '@'，
# 真 0600 的快照读成 '-rw-------@' ≠ '-rw-------'，守卫对着**正确交付**报"把密钥群发了"。
# 这属检查器假红，但**判据不许退化为字符串包含**——stat 数字模式照样是逐位等值（600 vs 644 必红），
# 只是把"串形态"这个与权限无关的自由度剥掉。BSD/GNU 两种 stat 方言按可用性回落，两者都没有则回空串
# （空串 ≠ 600，宁可判红也不放行"读不出权限"）。
env_mode() {
  local f
  f=$(ls "$BACKUP_DIR"/*.env 2>/dev/null | head -1)
  [ -z "$f" ] && return 0
  stat -f %Lp "$f" 2>/dev/null || stat -c %a "$f" 2>/dev/null || true
}

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
#    这一组同时带着 CASE_ENV_FILE：凭据快照排在完整性校验**之后**，dump 失败的那一轮
#    必须连快照都不留——目录里孤零零一份 .env 配上"没有可恢复的归档"，是下一次误删时的双倍损失。
CASE_ENV_FILE="$WORK/fake.env" STUB_ROLE_FLAGS=t STUB_DUMP_BYTES=100 STUB_DUMP_RC=1 run_case c_partial
if [ "$RC" -ne 0 ] && [ "$(dump_count)" = "0" ]; then
  ok "③ pg_dump 中途失败 → 非零半截归档被删掉、整体判红（exit=${RC}）"
else
  bad "③ 半截归档留在盘上（$(dump_count) 个，exit=${RC}）——「文件存在且可解析」会被当成备份成功，而它其实缺数据"
fi
if [ "$(env_count)" = "0" ]; then
  ok "③·补 dump 失败时凭据快照也不留盘（快照只在归档通过校验之后才写）"
else
  bad "③·补 归档没成功却留下了 $(env_count) 份凭据快照——密钥单独躺在备份目录里，而它配不到任何可恢复的库"
fi

# ④ 探测失败不拦：psql 打不通（非零退出且零输出）→ 只 WARN，继续尝试 dump
STUB_PSQL_RC=1 STUB_DUMP_BYTES=2048 run_case d_probeblind
if [ "$RC" -eq 0 ] && grep -q "探测失败" "$OUT" && grep -q "全部完成" "$OUT"; then
  ok "④ 探针取不到读数 → 只 WARN 不拦，仍跑完（把探针失败变成永久不备份是更坏的结果）"
else
  bad "④ 探针失败的处理口径不对（exit=${RC}）——既不能静默放行，也不能一律判红"
fi

# ⑤ 凭据快照随归档留存（.env 丢失批，2026-09-28）：
#    归档里的通道凭据是 JWT_SECRET 派生密钥加密的，只备份库不备份 .env，恢复出来就是一库
#    解不开的密文——本机 2026-09-28 正是这么丢的。四条断言各钉一件事：
#    留了 / 只留一份 / 权限 0600 / 内容与源逐字相同，外加一条"值零回显"。
CASE_ENV_FILE="$WORK/fake.env" STUB_ROLE_FLAGS=t STUB_RLS_TABLES=0 STUB_DUMP_BYTES=2048 run_case e_envsnap
if [ "$RC" -eq 0 ] && grep -q "凭据快照已随归档留存" "$OUT"; then
  ok "⑤ 校验通过的归档配一份凭据快照（exit=${RC}）"
else
  bad "⑤ 凭据快照没随归档落盘（exit=${RC}）——只备份密文库、不备份密钥，恢复后通道凭据全部作废"
fi
if [ "$(env_count)" = "1" ]; then
  ok "⑤·补 快照恰一份（同名同戳，与本次归档配对）"
else
  bad "⑤·补 快照份数异常（$(env_count) 个）——多份密钥文件会让「恢复时该配哪一份」重新变成猜"
fi
if [ "$(env_mode)" = "600" ]; then
  ok "⑤·补 快照权限 0600（备份目录常被别的账号/备份代理读走）"
else
  bad "⑤·补 快照权限是 '$(env_mode)'，不是 600——把库备份的口径扩大到把密钥也群发了"
fi
if cmp -s "$WORK/fake.env" "$BACKUP_DIR"/*.env 2>/dev/null; then
  ok "⑤·补 快照内容与源 .env 逐字相同（不是截断或空文件）"
else
  bad "⑤·补 快照内容与源不一致——恢复时 JWT_SECRET 差一个字符就全库解不开"
fi
if ! grep -q "$FAKE_JWT" "$OUT" && ! grep -rq "$FAKE_JWT" "$BACKUP_DIR"/*.dump 2>/dev/null; then
  ok "⑤·补 密钥值零回显（日志与归档里都不出现该值本体）"
else
  bad "⑤·补 日志或归档里出现了密钥值——快照本意是防丢凭据，不能反手把凭据抄进日志"
fi

# ⑤反 源 .env 不在场：只 WARN、不判红（备份是主目标；把"没有快照"变成"整轮备份失败"
# 等于用一条新守卫把唯一能救命的 dump 也废掉——与 ④ 同一口径）。
STUB_ROLE_FLAGS=t STUB_RLS_TABLES=0 STUB_DUMP_BYTES=2048 run_case f_noenv
if [ "$RC" -eq 0 ] && grep -q "不在场：本次归档没有配套凭据快照" "$OUT"; then
  ok "⑤反 缺 .env → 只 WARN 并如实说明后果，整体仍跑完（exit=${RC}）"
else
  bad "⑤反 缺 .env 的处置口径不对（exit=${RC}）——既不能静默成功，也不能把备份本身判红"
fi
if [ "$(env_count)" = "0" ]; then
  ok "⑤反·补 缺源文件时目录里不留下半份或空快照（宁缺毋滥：0 字节的 .env 会让人以为有）"
else
  bad "⑤反·补 源 .env 不在场却写出了 $(env_count) 份快照——空快照比没有更危险"
fi

# ⑥ 读法自证（FIX-13，2026-09-29 审计批）：⑤·补 那条权限判据自己也得有判别力。
# 现场教训有两层：第一版用 `ls -l` 的权限串做等值，macOS 上带 xattr 的真 0600 文件会读成
# '-rw-------@'，于是守卫对着**正确交付**判红（本批唯一一次回归红就是它）；而"改成字符串包含"
# 是把守卫弱化成没有——所以这里钉的是**读法本身**：
#   ⑥·a 带 xattr 的 0600 必须仍读成 600（假红不再复发生）
#   ⑥·b 同一个文件 chmod 644 后必须读成 644（等值锁仍然认得出走漏的密钥）
# 两条一起才说明"数字模式"既没被形态干扰、也没被放宽。
SAVE_BACKUP_DIR="$BACKUP_DIR"
PC_DIR="$WORK/permcheck"
mkdir -p "$PC_DIR"
printf 'JWT_SECRET=permcheck-not-a-real-secret\n' > "$PC_DIR/x.dump.env"
chmod 600 "$PC_DIR/x.dump.env"
if command -v xattr >/dev/null 2>&1; then
  xattr -w com.apple.test 1 "$PC_DIR/x.dump.env" 2>/dev/null || true
elif command -v setfattr >/dev/null 2>&1; then
  setfattr -n user.test -v 1 "$PC_DIR/x.dump.env" 2>/dev/null || true
fi
BACKUP_DIR="$PC_DIR"
if [ "$(env_mode)" = "600" ]; then
  ok "⑥·a 权限读法：带 xattr 的 0600 仍读成 600（ls -l 串形态不再参与判据）"
else
  bad "⑥·a 权限读法把带 xattr 的 0600 读成 '$(env_mode)'——⑤·补 会在正确交付上假红"
fi
chmod 644 "$PC_DIR/x.dump.env"
if [ "$(env_mode)" = "644" ]; then
  ok "⑥·b 权限读法：同一文件放宽到 644 必须读成 644（等值锁没被放宽成字符串包含）"
else
  bad "⑥·b 权限读法把 644 读成 '$(env_mode)'——判据失效，密钥 0644 也会被判绿"
fi
BACKUP_DIR="$SAVE_BACKUP_DIR"

rm -rf "$WORK"
echo "==== 结果: PASS=$PASS FAIL=$FAILN ===="
[ "$FAILN" -eq 0 ] || exit 1
exit 0
