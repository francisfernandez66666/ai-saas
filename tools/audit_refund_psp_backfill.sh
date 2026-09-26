#!/usr/bin/env bash
# 存量退款单核对脚本（FIX-1 上稳定单号前的**前置**，2026-09-26）
#
# 为什么需要它：迁移 028 只加列与部分唯一索引，**刻意不回填** refund_out_no。
# 原因是历史上有过一批"用带秒级时间戳的号发给 PSP"的退款（wechat_pay/alipay/gateway 三处
# 各自拼号），它们的真实单号只有 PSP 后台知道。若脚本按新规则补一个 "RF"+订单号 进去，
# 会得到三重害：
#   ① 拿这个号去查单必然查不到（PSP 侧根本没这个号）；
#   ② 若那笔历史退款其实**没成功**，回填会把它伪装成"已受理待终态"，从人工核销队列里消失；
#   ③ 部分唯一索引 ux_order_refund_out_no 会把这个假号当成真锚，此后再也修不了。
# 所以本脚本只做一件事：把"需要财务逐笔问 PSP 后台"的单子列出来，一个字节都不改。
#
# 用法：
#   bash tools/audit_refund_psp_backfill.sh            # dry-run（默认，只读打印清单）
#   bash tools/audit_refund_psp_backfill.sh --export   # 额外导出 CSV 给财务（仍不改库）
#
# 退出码：0=清单已出（无论是否有单）；非 0=连不上库/查询失败（**不得**当成"没有存量单"）。
#   这条很重要：一个"读不到就报成功"的核对脚本，跟它要防的那个静默失败是同一个形态。
set -uo pipefail

DB_URL="${TEST_DB_URL:-postgresql://ai_scrm:dev123@localhost/ai_scrm}"
PSQL=(psql "$DB_URL" -tAc)
CSVQ=(psql "$DB_URL" --csv)
EXPORT=0
for a in "$@"; do
  case "$a" in
    --export) EXPORT=1 ;;
    --apply)  echo "本脚本不支持 --apply：存量单号只能由财务按 PSP 后台逐笔回填，脚本不得代填"; exit 2 ;;
    *) echo "未知参数：${a}（可用 --export）"; exit 2 ;;
  esac
done

cd "$(dirname "$0")/.." || exit 2

# 连通性与读表自证：先确认能读到 billing_orders，否则后面所有"0 行"都是假绿
if ! "${PSQL[0]}" "${PSQL[@]:1}" "SELECT count(*) FROM billing_orders" >/dev/null 2>&1; then
  echo "FAIL 连不上库或 billing_orders 不可读：${DB_URL}（请把 TEST_DB_URL 指向目标库）"
  exit 1
fi
TOTAL=$("${PSQL[@]}" "SELECT count(*) FROM billing_orders" | tr -d '[:space:]')
echo "库内 billing_orders 总行数：${TOTAL}（读到数字才算连通，本脚本后续判据以此为前提）"

cat <<'EOF'

==== 存量退款单核对清单（FIX-1 稳定单号前置）====
口径：账面已退款且应退金额>0，但**没有**可信退款单号锚的行。
分三类，处置方式各不相同：

EOF

# ① 有单号（新链路写的）：可直接用号查 PSP
echo "① 已有稳定单号（新链路，可直接按号查单）："
"${PSQL[@]}" "SELECT count(*) FROM billing_orders WHERE status='refunded' AND refund_amount_cents>0 AND refund_out_no<>''" | tr -d '[:space:]' | sed 's/^/   /'

# ② 无单号且无出款状态：多半是 mock/manual 渠道（无资金动作）或旧崩溃窗口，需按渠道分流
echo "② 无单号且出款状态为空（mock/manual 属正常；真实渠道单需人工核实是否发过款）："
"${PSQL[@]}" "SELECT channel||' = '||count(*) FROM billing_orders WHERE status='refunded' AND refund_amount_cents>0 AND refund_out_no='' AND (refund_psp_status IS NULL OR refund_psp_status='') GROUP BY channel ORDER BY channel" | sed 's/^/   /'
echo "   其中真实渠道（非 mock/manual）逐笔清单："
"${PSQL[@]}" "SELECT id||' | '||order_no||' | '||channel||' | 应退 '||refund_amount_cents||' 分 | 退款时间 '||COALESCE(refunded_at::text,'-') FROM billing_orders WHERE status='refunded' AND refund_amount_cents>0 AND refund_out_no='' AND channel NOT IN ('mock','manual','') ORDER BY id" | sed 's/^/     /'

# ③ 有出款状态但无单号：旧实现（带时间戳拼号）发出去的单——这批**必须**人工核销
echo "③ 旧链路发出的单（有 psp 状态、无稳定单号；这批是三类的核心，号只能去 PSP 后台查）："
"${PSQL[@]}" "SELECT count(*) FROM billing_orders WHERE status='refunded' AND refund_amount_cents>0 AND refund_out_no='' AND refund_psp_status<>''" | tr -d '[:space:]' | sed 's/^/   /'

# ④ 长期停在 psp_ok 的单：受理未证实积压（对账器每轮回填的 gauge 是同一口径的实时值）
echo "④ 受理未证实积压（psp_ok 且已过 10 分钟避让窗；查单收敛不了就得人工收尾）："
"${PSQL[@]}" "SELECT count(*) FROM billing_orders WHERE status='refunded' AND refund_psp_status='psp_ok' AND updated_at < NOW() - INTERVAL '10 minutes'" | tr -d '[:space:]' | sed 's/^/   /'

# ⑤ 判成 psp_failed 的单：权益已回收、钱没出去——这批是财务的硬账
echo "⑤ 查单判失败（权益已回收、资金未出，必须人工出款）："
"${PSQL[@]}" "SELECT id||' | '||order_no||' | 应退 '||refund_amount_cents||' 分 | 号 '||refund_out_no FROM billing_orders WHERE status='refunded' AND refund_psp_status='psp_failed' ORDER BY id" | sed 's/^/   /'

# 一致性体检：同一单号被两单共用（部分唯一索引在，理论上恒 0；这里断的是"索引真的在"）
DUP=$("${PSQL[@]}" "SELECT count(*) FROM (SELECT refund_out_no FROM billing_orders WHERE refund_out_no<>'' GROUP BY refund_out_no HAVING count(*)>1) x" | tr -d '[:space:]')
echo "⑥ 共用同一退款单号的多行（必须为 0；非 0 说明迁移 028 的部分唯一索引没建上或被人工 SQL 绕过）：${DUP}"

IDX=$("${PSQL[@]}" "SELECT count(*) FROM pg_indexes WHERE indexname='ux_order_refund_out_no'" | tr -d '[:space:]')
echo "⑦ 部分唯一索引 ux_order_refund_out_no 实存（1=在，0=不在）：${IDX}"
if [ "${IDX:-0}" != "1" ]; then
  echo "   FAIL 索引缺失：稳定单号目前没有 DB 层兜底，先补跑迁移 028 再上生产"
  RC=1
else
  RC=0
fi

if [ "$EXPORT" = "1" ]; then
  OUT="refund_psp_audit_$(date +%Y%m%d_%H%M%S).csv"
  "${CSVQ[@]}" -c "SELECT id, order_no, tenant_id, channel, status, refund_amount_cents, refund_psp_status, refund_out_no, refunded_at FROM billing_orders WHERE status='refunded' AND refund_amount_cents>0 ORDER BY id" > "$OUT"
  echo "已导出逐笔清单：${OUT}（行数 $("wc -l" < "$OUT" | tr -d ' ')，含表头）"
fi

cat <<'EOF'

处置建议（按上面编号）：
  ②/③ 真实渠道的存量单：逐笔登录 PSP 后台按订单号查退款记录，把**真实退款单号**回填到
     refund_out_no（人工 SQL，一单一行），回填后查单器即可接管；查不到退款记录的，
     按"未出款"处理并走人工出款，不要留空、也不要填猜测值。
  ④ 长期非零：说明有渠道没接查单协议（通用 HMAC 网关属此类）或密钥轮换中装配失败，
     需要人工核销队列（不是把状态改成成功来"清库存"）。
  ⑤ 非零：真钱没出去、权益已回收，客户两头落空，优先级最高。
本脚本只读，未改动任何数据。
EOF
exit $RC
