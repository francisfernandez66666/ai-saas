#!/bin/bash
# 测试租户清理（C7，2026-09-12）：删除 UAT/权限冒烟/退款 E2E/单测遗留的临时租户及其级联业务数据。
# 安全：默认 dry-run 只报告；--apply 才真删。**绝不删 acme / 生产 / 默认租户**（仅匹配 uat/perm/rfd/channel_smoke/unit_test/e2e-e2e/e2etest 等测试前缀）。
# 周报/CI 收尾手动调用，非自动删（避免误伤并行调试中的租户）。
# 用法: ./tools/cleanup_test_tenants.sh [--apply]
set -euo pipefail
DBURL="${TEST_DB_URL:-postgresql://ai_scrm:dev123@localhost/ai_scrm}"
APPLY=0
[ "${1:-}" = "--apply" ] && APPLY=1

# 匹配测试租户前缀（含进程后缀，如 uata79365 / unit_test_tenant 复用 code；e2e 来自 smoke_saas
# 与调试脚本 e2e-<随机> 形态，统一用 e2e% 兜住，合法租户不会以 e2e 起头）。
# 以 ~ 开头的条目按 POSIX 正则匹配（build_where 转 code ~ '...'），其余按 LIKE 前缀。
# 2026-09-18 实测批扩充：单测租户 code 形如 p<pid>x<hex>_unit_test_tenant/_exp_soon（前缀并非
# unit_test_tenant，旧模式永远追不上），以及 uat/smoke/调试脚本历年遗留的 dpa/uindg/inv/px 等族。
# 2026-09-21 根治（实测发现旧条目只是"枚举后缀"，追不上新语义码）：
#   internal/testutil 的 code 恒为 processSuffix + "_" + 语义码，其中
#   processSuffix = p%06dx<hex>（crypto/rand 失败时退化为 p%06d），语义码由调用方自定
#   （unit_test_tenant/exp_soon/rls_a/iso_a/utt_none/kw_edu… 数量开放）。
#   故按**命名约定泛化**而非枚举：^p[0-9]{6}(x[0-9a-f]+)?_[a-z0-9_]+$
#   —— 实测残留 38 个（utt_auto/utt_edu/utt_none/utt_rox/kw_edu/kw_auto）即旧模式漏网。
#   再加一条名称语义判别器「单元测试租户-%」（testutil 固定 Name 前缀），双保险防新形态漏网。
PATTERNS=(
  "uat%" "perm%" "rfd%" "chan_smoke%" "unit_test_tenant%" "rls_a%" "rls_b%" "e2e%"
  "dpa%" "uindg%" "uinde%" "dlimit%" "verifytest%" "invitedemo%" "rlttest%" "b2btest%"
  "trd%" "ref%" "dbg-%" "revtest"
  # 2026-09-23 批六收尾：历史调试脚本留下的两族一次性租户（旧 "dbg-%" 用了连字符，追不上
  # 下划线形态的 dbg3_66924「调试租户」；p11_66861「P11复现租户」是短进程号形态，
  # 泛化式 ^p[0-9]{6}... 也追不上）。两者均 0 用户/0 客户，纯复现现场。
  "dbg%" "~^p[0-9]+_[0-9]+$"
  "单元测试租户-%"
  "~^p[0-9]{6}(x[0-9a-f]+)?_[a-z0-9_]+$"
  "~^(ind|edu|dup|rec|man|inv[0-9]*|px|pp|pw|id[ab]|nt|t)[0-9]+$"
)

build_where() {
  local i=0 cond=""
  for p in "${PATTERNS[@]}"; do
    [ $i -gt 0 ] && cond="$cond OR "
    if [ "${p:0:1}" = "~" ]; then
      cond="${cond}code ~ '${p:1}'"
    else
      cond="${cond}code LIKE '$p'"
    fi
    i=$((i+1))
  done
  echo "$cond"
}
WHERE=$(build_where)

# ---------- A. 测试用户清理（2026-09-21 新增）----------
# 背景：smoke/smoke_perm/uat 每跑一轮就在主租户里留一批账号，实测主租户累积 166 个
# （smoke_da_* 83 / smoke_ro_* 83）＋ smoke_perm 的 *_perm* 三族 174 个，且长期无人回收——
# 租户清理只管租户主行，管不到"合法租户内的测试账号"。这里按账号命名族系回收。
# 安全性：实测这些测试用户**没有任何业务数据绑定**（customers/conversations 的
# assigned_user_id 命中数为 0），故删除不会留悬空引用；仍保守地在删前把语义引用归零，
# 以防将来出现绑定（无外键约束，删用户不会被拦截，必须自己兜住）。
USER_PATTERNS=(
  "smoke_%"
  "dept_perm%" "viewer_perm%" "sales_perm%"
  "dbg_ro_%"
  "~^(dp|ue|ug|ua|ub|uc|man|boss_tx|boss_d)[0-9]+$"
  "~^e2e[-_a-z0-9]+$"
)
build_user_where() {
  local i=0 cond=""
  for p in "${USER_PATTERNS[@]}"; do
    [ $i -gt 0 ] && cond="$cond OR "
    if [ "${p:0:1}" = "~" ]; then
      cond="${cond}username ~ '${p:1}'"
    else
      cond="${cond}username LIKE '$p'"
    fi
    i=$((i+1))
  done
  echo "$cond"
}
UWHERE=$(build_user_where)
UIDS=$(psql "$DBURL" -tAc "SELECT id FROM tenant_users WHERE $UWHERE ORDER BY id;" | tr -d '\r' | grep -c . || true)
if [ "${UIDS:-0}" -gt 0 ]; then
  echo "匹配到 $UIDS 个测试用户（跨租户），示例："
  psql "$DBURL" -c "SELECT tenant_id, username, role FROM tenant_users WHERE $UWHERE ORDER BY tenant_id, id LIMIT 6;" 2>/dev/null
  if [ "$APPLY" = "1" ]; then
    # 先中和语义引用（无外键，删用户不会报错，但会留悬空 id，故显式归零）
    psql "$DBURL" -q -c "
      UPDATE conversations SET assigned_user_id=0 WHERE assigned_user_id IN (SELECT id FROM tenant_users WHERE $UWHERE);
      UPDATE customers     SET assigned_user_id=0 WHERE assigned_user_id IN (SELECT id FROM tenant_users WHERE $UWHERE);
      UPDATE follow_ups    SET user_id=0           WHERE user_id           IN (SELECT id FROM tenant_users WHERE $UWHERE);
      UPDATE messages      SET sender_id=0         WHERE sender_id         IN (SELECT id FROM tenant_users WHERE $UWHERE);" >/dev/null 2>&1 || true
    psql "$DBURL" -tAc "DELETE FROM tenant_users WHERE $UWHERE;" >/dev/null 2>&1 || true
    LEFT=$(psql "$DBURL" -tAc "SELECT COUNT(*) FROM tenant_users WHERE $UWHERE;" | tr -d '[:space:]')
    echo "已清理测试用户（残留 ${LEFT:-?} 个）。"
  else
    echo "[dry-run] 测试用户未删除（加 --apply 生效）。"
  fi
fi

# 级联删除用的表清单（动态发现"所有含 tenant_id 列的业务表"，schema 漂移免疫）。
# 计算位置必须靠前：B/C 段（孤儿 / 陈旧测试数据回收）与租户级联删除共用它，
# 且这些回收段必须在"无测试租户即提前退出"之前执行——否则当库里只剩孤儿数据、
# 却没有任何可匹配的测试租户时，脚本会在早退分支直接 exit，孤儿永远收不掉
# （2026-09-21 实测踩中：当时已清完 44 个测试租户，B/C 段因此而不可达）。
DLIST=$(psql "$DBURL" -tAc "
  SELECT tablename FROM pg_tables
  WHERE schemaname='public' AND tablename <> 'tenants'
    AND tablename IN (
      SELECT table_name FROM information_schema.columns
      WHERE table_schema='public' AND column_name='tenant_id'
    )
  ORDER BY tablename;" | tr -d '\r' | grep -vE '^(tenants)$' || true)

# ---------- B. 孤儿业务数据回收（2026-09-21 新增）----------
# 背景：历史多次删测试租户时级联不彻底，留下一批 tenant_id 指向"已不存在的租户"的死行。
# 实测残留：messages 1092 / templates 1721 / tenant_audit_logs 2406 / usage_records 447 /
# customers 97 / follow_ups 70 / billing_orders 56 / conversations 91（合计约 6000 行）。
# 为什么安全：db.RQ(c) 按 tenant_id 注入过滤，这些行在任何租户下都查不到，属于永远不可达的死数据；
# 留着只会虚增聚合口径与表体积。
# 为什么必须排除 tenant_id=0：templates(22)/tenant_audit_logs(75) 存在平台级合法行，
# tenant_id<>0 把整个平台面排除在外——这是本段唯一的"不能省"的条件。
ORPHAN_SQL="tenant_id <> 0 AND tenant_id NOT IN (SELECT id FROM tenants)"
if [ "$APPLY" = "1" ]; then
  ORPHAN_TOTAL=0
  for t in $DLIST; do
    res=$(psql "$DBURL" -tAc "DELETE FROM $t WHERE $ORPHAN_SQL;" 2>&1)
    case "$res" in
      DELETE*) ORPHAN_TOTAL=$((ORPHAN_TOTAL + ${res#DELETE })) ;;
      *) echo "  [warn] $t 孤儿回收异常：$res" ;;
    esac
  done
  echo "已回收孤儿业务数据 $ORPHAN_TOTAL 行（tenant_id 指向已删除租户）。"
else
  ORPHAN_PREVIEW=0
  for t in $DLIST; do
    n=$(psql "$DBURL" -tAc "SELECT COUNT(*) FROM $t WHERE $ORPHAN_SQL;" 2>/dev/null | tr -d '[:space:]')
    ORPHAN_PREVIEW=$((ORPHAN_PREVIEW + ${n:-0}))
  done
  echo "[dry-run] 待回收孤儿业务数据 $ORPHAN_PREVIEW 行（tenant_id 指向已删除租户）。"
fi

# ---------- B2. 悬空会话消息回收（D5 批六，2026-09-23 新增）----------
# 背景：B 段只按 tenant_id 判孤儿，收不到"租户还在、但所属会话已被硬删"的消息行——
# 这类行挂在已不存在的 conversation_id 上，会话历史按 conversation 取，因此永不可达，
# 却仍在 messages 热表里占体积、并虚增以 messages 为真相源的统计（D2 贡献度同类口径坑）。
# 实测本机库另有 15 行 conversation_id=0 的孤儿（该形态不在此段处理：迁移 019 负责回填，
# 剩余量由 /status/detail 的 orphan_messages 观测位盯住，只准降不准升）。
# 处置方式：搬进 messages_archive 而不是 DELETE——归档表与热表结构全量对齐（迁移 011），
# 且 PIPL 匿名化已同时覆盖两表；热表就此减负，数据一行不丢（可回溯取证）。
DANGLING_SQL="conversation_id <> 0 AND conversation_id NOT IN (SELECT id FROM conversations)"
if [ "$APPLY" = "1" ]; then
  MOVED=$(psql "$DBURL" -tAc "
    WITH picked AS (
      SELECT * FROM messages WHERE $DANGLING_SQL
    )
    INSERT INTO messages_archive SELECT *, now() FROM picked
    RETURNING id;" 2>/dev/null | grep -c . || true)
  if [ "${MOVED:-0}" != "0" ]; then
    psql "$DBURL" -tAc "DELETE FROM messages WHERE $DANGLING_SQL;" >/dev/null 2>&1 || true
  fi
  echo "已归档悬空会话消息 ${MOVED:-0} 行（搬入 messages_archive，热表删除）。"
else
  # dry-run：归档表不存在（全新库未跑迁移 011）时不得炸脚本，只报计数
  HAS_ARCH=$(psql "$DBURL" -tAc "SELECT count(*) FROM information_schema.tables WHERE table_name='messages_archive';" 2>/dev/null | tr -d '[:space:]')
  DANGLING=$(psql "$DBURL" -tAc "SELECT COUNT(*) FROM messages WHERE $DANGLING_SQL;" 2>/dev/null | tr -d '[:space:]')
  echo "[dry-run] 悬空会话消息 ${DANGLING:-0} 行（归档表${HAS_ARCH:-0} 存在则可搬入 messages_archive）。"
fi

# ---------- C. 陈旧测试包 / 残留行业包回收（2026-09-21 提到早退分支之前）----------
# packages 是全局目录表不挂租户；testutil.CleanupTenant 带 1h 年龄窗，长期不跑单测的环境残留
# 由此兜底——仅回收无任何订单引用的测试包，绝不触碰真实在售包。
# 行业包残留（2026-09-18 /super 行业包管理页首跑暴露）：
#   rls_ind 为 db RLS 单测每次新建（无唯一约束），education 为 uat/dbg 脚本每次裸 INSERT——
#   两类都会无限累积。回收口径：被任何绑定引用的行永不删；education 额外保留最新一条
#   （uat 选包按 id DESC 取 active，删旧留新不改行为）。
if [ "$APPLY" = "1" ]; then
  STALE_PKGS=$(psql "$DBURL" -tAc "DELETE FROM packages WHERE code LIKE 'ut\_%' AND id NOT IN (SELECT COALESCE(package_id,0) FROM billing_orders WHERE package_id IS NOT NULL) RETURNING id;" 2>/dev/null | grep -c . || true)
  echo "已回收陈旧 ut_* 测试包 $STALE_PKGS 个。"
  STALE_IPACKS=$(psql "$DBURL" -tAc "
    DELETE FROM industry_packs ip
    WHERE ip.code IN ('rls_ind','education')
      AND ip.id NOT IN (SELECT pack_id FROM tenant_pack_bindings WHERE pack_id IS NOT NULL)
      AND ip.id NOT IN (SELECT pack_id FROM dept_pack_bindings WHERE pack_id IS NOT NULL)
      AND NOT (ip.code='education' AND ip.id = (SELECT max(id) FROM industry_packs WHERE code='education'))
    RETURNING ip.id;" 2>/dev/null | grep -c . || true)
  echo "已回收残留行业包 $STALE_IPACKS 个（rls_ind/education 未引用重复行）。"
fi

# ---------- C2. 获客活码残留回收（获客批，2026-09-23 新增）----------
# 为什么必须走这里而不是测试脚本自己删：**产品侧刻意没有"删码"接口**
# （短码一旦印上物料就不可回收，删除会让历史归因变悬空外键，见 internal/model/acquisition.go）。
# 于是浏览器用例造的那个 e2e 码只能由运维脚本按命名约定回收——名字前缀是硬约定：
# `e2e活码`，用例改名必须同步改这里，否则回收静默失效（留一个启用中的假码比留一行垃圾更糟）。
ACQ_E2E=$(psql "$DBURL" -tAc "SELECT count(*) FROM acquisition_codes WHERE name LIKE 'e2e活码%'" 2>/dev/null | tr -d '[:space:]')
echo "待回收 e2e 活码：${ACQ_E2E:-0} 个"
if [ "$APPLY" = "1" ] && [ "${ACQ_E2E:-0}" != "0" ]; then
  # 先事件后本体：acquisition_scans 靠 code_id 指回来，反序删会留下指向空气的扫码行
  psql "$DBURL" -tAc "
    DELETE FROM acquisition_scans
     WHERE code_id IN (SELECT id FROM acquisition_codes WHERE name LIKE 'e2e活码%');" >/dev/null 2>&1
  psql "$DBURL" -tAc "DELETE FROM acquisition_codes WHERE name LIKE 'e2e活码%'" >/dev/null 2>&1
  echo "已回收 e2e 活码及其扫码事件 ${ACQ_E2E} 个。"
fi

# ---------- D. 测试租户本体清理 ----------
IDS=$(psql "$DBURL" -tAc "SELECT id FROM tenants WHERE $WHERE ORDER BY id;" | tr -d '\r' | paste -sd, -)
if [ -z "${IDS// /}" ]; then
  echo "无匹配的测试租户本体，无需清理（孤儿 / 陈旧测试数据回收已完成）。"
  exit 0
fi
CNT=$(echo "$IDS" | tr ',' '\n' | grep -c .)
echo "匹配到 $CNT 个测试租户：id=[$IDS]"
psql "$DBURL" -c "SELECT id,code,name,status FROM tenants WHERE id IN ($IDS) ORDER BY id;" 2>/dev/null

if [ "$APPLY" = "0" ]; then
  echo "[dry-run] 未删除。确认无误后执行：$0 --apply"
  exit 0
fi

echo "将清理的含 tenant_id 业务表：$(echo "$DLIST" | tr '\n' ' ')"
# 先删子表数据（逐条独立执行，ON_ERROR_STOP=0 容错），再删租户主行
for t in $DLIST; do
  res=$(psql "$DBURL" -tAc "DELETE FROM $t WHERE tenant_id IN ($IDS);" 2>&1)
  case "$res" in
    DELETE*) : ;;
    *) echo "  [warn] $t 删除异常：$res" ;;
  esac
done
psql "$DBURL" -c "DELETE FROM tenants WHERE id IN ($IDS);" >/dev/null 2>&1
echo "已清理 $CNT 个测试租户及其级联数据。"
