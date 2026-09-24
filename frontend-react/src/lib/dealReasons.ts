// 商机稳定原因码 → 中文话术（商机批 · 前端批3，2026-09-24 从 DealsTab 提出，后台看板与顾问台共用）。
//
// 为什么要共用一份：同一个 `stage_backward` 在后台叫「阶段不能往回退」、在顾问台叫「不能回退」，
// 顾问就会以为这是两条不同的规则。后端只发码不发话（码是契约、话是文案），
// 所以文案表必须在前端也收成单点，改文案改这里，改判据去改后端。
//
// 分支纪律：**永远按 reason 分支，永远不要按 message 匹配**——
// 后端改一次措辞，按 message 写的 if 就会静默走到兜底分支，页面看起来"提示不明显"，实际是丢了判据。
//
// 码表镜像 internal/deal/policy.go 的 Reason* 常量（27 个，一个不漏）。
// 漏一个的后果不是空白：调用方会回落到后端 message，而 message 是给人看的句子、不是文案口径，
// 于是"页面偶尔说的是另一种话"。新增拒绝码时必须同步这里。

const REASON_TEXT: Record<string, string> = {
  title_required: '请填商机标题',
  title_too_long: '商机标题太长了，说清这一单是什么就够',
  stage_unknown: '阶段码不认识，请刷新页面重取一次阶段口径',
  same_stage: '目标阶段和当前阶段一样，这一推什么都没发生',
  stage_backward: '阶段不能往回退。要重开请先让这张单终局（成交或流失），再新建一张',
  deal_closed: '这张单已经终局了，不能再改。要重新跟就再开一张',
  deal_not_found: '商机不存在，或已被删除',
  won_amount_required: '标记成交必须带金额——成交多少钱是要进报表的数',
  lost_reason_required: '标记流失必须选一个流失原因，否则复盘时这一单说不清',
  lost_reason_unknown: '流失原因不在可选清单里，请刷新页面重取一次',
  amount_negative: '金额不能是负数',
  amount_too_large: '金额超出允许上限（单条商机不做天文数字）',
  customer_not_found: '客户不存在，或不属于当前门店/组织',
  source_unknown: '商机来源码不认识',
  tenant_required: '缺少租户语境，请重新登录后再试',
  quote_locked: '报价单已发出，内容锁死，只能另出一版',
  quote_not_draft: '只有草稿态的报价能改、能发出',
  quote_not_sent: '这一版还没发给客户，接受/拒绝只对已发出的报价有效',
  quote_final: '报价单已是终局态（已接受或已拒绝），不能再操作',
  quote_not_found: '报价单不存在，或已被新版取代',
  quote_lines_required: '报价至少要有明细行',
  quote_too_many_lines: '明细行数超出上限（报价不是配置单，一行一项）',
  quote_line_name: '明细项目名不能为空，也别超过长度上限',
  quote_line_qty: '明细数量非法（必须是正整数）',
  quote_line_unit: '明细单价非法（必须是 0 以上的整数分）',
  quote_total_too_large: '报价合计超出上限，请拆单',
}

/** 商机拒绝码清单（表键的投影）：单测用它断"码表没漏格"，别靠手数。 */
export const DEAL_REASON_CODES: string[] = Object.keys(REASON_TEXT)

/** 取原因码话术；码不在表里时回落到调用方给的兜底文案（后端新增码时页面不至于空白）。 */
export function dealReasonText(reason: string | undefined, fallback: string): string {
  return (reason && REASON_TEXT[reason]) || fallback
}
