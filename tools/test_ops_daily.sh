#!/bin/bash
# ============================================================
# ops_daily.sh ①段（备份产物真实性）判据自证（FIX-7 运维批，2026-09-27）
#
# 为什么要有这个文件：ops_daily.sh 是"每日运维自检"，本批的教训原话是
# **「只会打日志并返回成功的守卫等于没有守卫」**。把"备份不存在/过期/截断/解不开"判成红之后，
# 如果不配反向用例，就没有任何东西证明这四条 bad 真的会响——它们完全可能因为
# "变量取空当成 0"、"stat 语法不对静默失败"这类取值缺陷而永不触发，改动的语义只剩注释。
# 更要防的是**反向的自伤**：判据被改成"恒红"同样毫无意义（第④组正向用例就是防这个）。
#
# 五组合成 BACKUP_DIR 喂给真 ops_daily.sh（只跑 --backup-only，不碰调度器、不连生产）：
#   ① 目录不存在        → FAIL「备份目录不存在」
#   ② 目录里零个 .dump  → FAIL「没有任何 .dump」
#   ③ 最新备份距今 30h  → FAIL「最新备份已 30 小时前」（cron 没跑/天天失败，产物校验本身全绿）
#   ④ 当天但只有 10 字节 → FAIL「只有 10 字节」（旧 backup.sh 那类"打了文件但没内容"的形态）
#   ⑤ 当天真 dump（本机 PG 可用时）→ 三条腿一条都不许红（防"把闸改成恒红"的假严格）
#
# 全程 OPS_NOTIFY_DISABLED=1：判红也只落 stdout，**不会往运维群推消息**
# （否则第一次跑这套用例的人会在真群里收到五条来历不明的告警）。
#
# 用法：bash tools/test_ops_daily.sh      （退出码 0=判据全对，1=有 FAIL）
# ============================================================
set -u
cd "$(dirname "$0")/.."

WORK=$(mktemp -d "${TMPDIR:-/tmp}/ops_daily_selftest.XXXXXX")
trap 'rm -rf "$WORK"' EXIT
export OPS_NOTIFY_DISABLED=1

PASS=0
FAIL=0
ok() { echo "  PASS  $1"; PASS=$((PASS + 1)); }
bad() { echo "  FAIL  $1"; FAIL=$((FAIL + 1)); }

# run_ops <backup_dir> → stdout 落在 $OUT，退出码落在 $RC
# 刻意不带 --quiet：--quiet 会连 FAIL 明细一起吞掉（bad() 走的也是 say），
# 而本用例断的正是"红项文案里出现了那一条判据的话术"——只拿退出码无法区分"红对了"与"红错了事"。
OUT=""
RC=0
run_ops() {
  OUT=$(BACKUP_DIR="$1" bash tools/ops_daily.sh --backup-only 2>&1)
  RC=$?
}

# expect_fail_with <用例名> <期望出现的红项关键字> —— 关键字取自 ops_daily 自己的 bad 文案
expect_fail_with() { # $1=名 $2=关键字
  if [ "$RC" != 1 ]; then
    bad "$1：应判 FAIL（退出码 1），实得 rc=$RC"
    printf '%s\n' "$OUT" | sed 's/^/        /'
    return
  fi
  if printf '%s' "$OUT" | grep -q "$2"; then
    ok "$1（红项文案含「$2」且整体判 1）"
  else
    bad "$1：退出码虽为 1，但找不到预期红项「$2」——红的是别的事，本条判据仍可能没牙"
    printf '%s\n' "$OUT" | grep -E 'FAIL|结果' | sed 's/^/        /'
  fi
}

# ① 目录不存在
run_ops "$WORK/never_created"
expect_fail_with "① 备份目录不存在 → FAIL" "备份目录"

# ② 目录存在但一个 .dump 都没有
mkdir -p "$WORK/empty"
run_ops "$WORK/empty"
expect_fail_with "② 零个 .dump → FAIL" "没有任何 .dump"

# ③ 有一份"当天之外"的旧备份：大小、可解析都过关，唯独时效不过关。
#    这一组专门证明时效判据是独立的一条腿——只靠②那种"整体没数据"是照不出它的。
mkdir -p "$WORK/stale"
STALE="$WORK/stale/ai_scrm_20260101_000000.dump"
if command -v pg_dump >/dev/null 2>&1 && pg_dump -Fc -f "$WORK/made.dump" "${TEST_DB_URL:-postgresql://ai_scrm:dev123@localhost/ai_scrm}" >/dev/null 2>&1; then
  cp "$WORK/made.dump" "$STALE"
else
  # 没有真 dump 时也要能测时效：造一份 >1KB 的内容只让"大小"绿、"可解析"红，
  # 时效那条腿照样要求它单独红——所以这里断的是「时效红出现」，不管解析红是否同时出现。
  head -c 4096 /dev/urandom >"$STALE"
fi
# mtime 拨到 30 小时前（BSD/GNU 两套语法都试，都不行则本组 SKIP 不伪装绿）
if touch -t "$(date -v-30H +%Y%m%d%H%M.%S 2>/dev/null || echo 202601010000.00)" "$STALE" 2>/dev/null; then
  :
else
  touch -d "30 hours ago" "$STALE" 2>/dev/null || true
fi
run_ops "$WORK/stale"
expect_fail_with "③ 最新备份距今 30h → FAIL" "最新备份已"

# ④ 当天、但只有 10 字节（截断/空归档）
mkdir -p "$WORK/tiny"
head -c 10 /dev/zero >"$WORK/tiny/ai_scrm_$(date +%Y%m%d_%H%M%S).dump"
run_ops "$WORK/tiny"
expect_fail_with "④ 备份只有 10 字节 → FAIL" "字节"

# ⑤ 正向：当天、非零、pg_restore --list 可解析 —— 三条腿一条都不许红
if command -v pg_dump >/dev/null 2>&1 && [ -s "$WORK/made.dump" ]; then
  mkdir -p "$WORK/good"
  cp "$WORK/made.dump" "$WORK/good/ai_scrm_$(date +%Y%m%d_%H%M%S).dump"
  run_ops "$WORK/good"
  for kw in "最新备份已" "只有" "解析失败" "没有任何 .dump" "备份目录"; do
    if printf '%s' "$OUT" | grep -q "FAIL.*$kw"; then
      bad "⑤ 正向组不该报「$kw」红，却报了——判据被改成恒红时①~④会绿得毫无意义"
    else
      ok "⑤ 正向组不报「$kw」（该绿的不红）"
    fi
  done
  if [ "$RC" = 0 ]; then
    ok "⑤ 正向组整体退出码 0"
  else
    bad "⑤ 正向组整体退出码应为 0（rc=$RC）"
    printf '%s\n' "$OUT" | grep -E 'FAIL|结果' | sed 's/^/        /'
  fi
else
  echo "  SKIP  ⑤ 正向组：本机 pg_dump 不可用或建 dump 失败，未验证「该备份判绿」这一半"
fi

echo ""
echo "==== 结果: PASS=$PASS FAIL=$FAIL ===="
[ "$FAIL" = "0" ] || exit 1
exit 0
