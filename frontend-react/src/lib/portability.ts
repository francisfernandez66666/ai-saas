// PIPL 可携带副本的前端取数单点（FIX-10，2026-09-29 端到端审计批）。
//
// 为什么单独成文件而不是把 fetch 写在 Client.tsx 里：
// 后端 `/privacy/my-data` 单次只给 `portableRowCap`（5000）条，**多出来的必须靠游标续取**，
// 而"游标循环"这件事的正确性要害（漏页、游标不推进、把半截副本当全量下载）
// 在没有真数据的单测里根本看不见——挂在组件里就只能靠真浏览器点一次才算验过。
// 抽成纯取数函数后，翻页合并、失败即中止、游标停滞这些判据都能用假响应逐条钉住。
//
// 三条口径与后端逐字对齐，任何一条松掉都会得到一份"看起来给完了"的假副本：
//  1. **任一页失败就整体失败**：后端对"归档表读不到"的策略是 500 且不给半成品，
//     前端如果把已经拿到的那几页照样下载，等于把后端的红线在最后一米绕过；
//  2. **游标必须推进**：`truncated=true` 而 `next_after_id` 不大于上一次时立即报错停手，
//     绝不能"再试一次同一页"或干脆当作取完；
//  3. **拉完才下载**：循环的终止条件只有"后端说没截断"，没有"翻够几页就先给"。

// 单页最多翻多少轮：5000 条/页 × 200 轮 = 一百万条，远超真实个人数据量。
// 它不是业务上限，只是防止后端游标实现出问题（或响应被中间层改写）时前端死循环。
const MAX_PAGES = 200

/** 后端一页响应的 data 形态（只声明本文件用到的键，其余原样透传进副本） */
export interface PortablePage {
  generated_at?: string
  customer?: Record<string, unknown>
  conversations?: Record<string, unknown>[]
  messages?: Record<string, unknown>[]
  page?: { row_cap?: number; limit?: number; after_id?: number; next_after_id?: number; truncated?: boolean }
}

/** 聚合成一份的完整副本（下载即此对象的 JSON） */
export interface PortableDoc extends PortablePage {
  /** 前端补充的取数覆盖说明：翻了几页、共多少条消息。缺了它，当事人无从判断这份是不是全量 */
  export_coverage: { pages: number; message_count: number; row_cap: number; complete: true }
}

/** 取数失败（带面向本人的中文原因，调用方直接展示，不再自己猜语义） */
export class PortableCopyError extends Error {}

/**
 * 游标翻页拉全量本人副本。
 *
 * @param customerId 目标客户 ID（C 端即自己的访客身份）
 * @param visitorKey 访客密钥自证；登录态可留空，后端按 user_id + 数据范围放行
 * @param post 注入点：默认走 Client.tsx 传进来的真实请求，单测传假响应
 */
export async function fetchPortableCopy(
  customerId: number,
  visitorKey: string,
  post: (body: Record<string, unknown>) => Promise<PortablePage | null>,
): Promise<PortableDoc> {
  let afterId = 0
  let pages = 0
  let rowCap = 5000
  const messages: Record<string, unknown>[] = []
  const conversations: Record<string, unknown>[] = []
  let customer: Record<string, unknown> | undefined
  let generatedAt: string | undefined

  for (; ; ) {
    if (pages >= MAX_PAGES) {
      // 到上限还"没取完"= 判定依据已经不成立，宁可报错让人来查，也不交一份说不清全不全的副本
      throw new PortableCopyError('副本数据量异常，已停止导出以避免给出不完整结果')
    }
    // limit 不显式传：后端口径是"只允许调低"，缺省即单页配额上限，翻页次数最少
    const data = await post({ customer_id: customerId, visitor_key: visitorKey, after_id: afterId })
    if (!data) throw new PortableCopyError('副本读取失败，请稍后重试')
    pages += 1
    if (!customer) customer = data.customer
    if (!generatedAt) generatedAt = data.generated_at
    rowCap = data.page?.row_cap ?? rowCap
    messages.push(...(data.messages || []))
    conversations.push(...(data.conversations || []))

    const truncated = data.page?.truncated === true
    if (!truncated) break
    const next = Number(data.page?.next_after_id ?? 0)
    if (!(next > afterId)) {
      // 反向对照：这一条真出事时表现为"重复同一页拉到天荒地老"或"静默当成取完"，
      // 两者都会给出一份**看起来完整**的副本，所以必须在这里报错停手
      throw new PortableCopyError('副本分页游标未推进，已停止导出')
    }
    afterId = next
  }

  return {
    generated_at: generatedAt,
    customer,
    conversations,
    messages,
    page: { row_cap: rowCap, limit: rowCap, after_id: afterId, next_after_id: afterId, truncated: false },
    export_coverage: { pages, message_count: messages.length, row_cap: rowCap, complete: true },
  }
}

/**
 * 把副本落成文件下载（Blob + a[download]）。
 * 单列出来只为让"文件名带日期、JSON 缩进可读、用完释放 objectURL"这三件事有个落点；
 * 真正的完整性判据全在 fetchPortableCopy 里，这里不做任何取舍。
 */
export function downloadPortableDoc(doc: PortableDoc, filenamePrefix = 'my-data-copy'): void {
  const blob = new Blob([JSON.stringify(doc, null, 2)], { type: 'application/json' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  const stamp = new Date().toISOString().slice(0, 10)
  a.href = url
  a.download = `${filenamePrefix}-${stamp}.json`
  document.body.appendChild(a)
  a.click()
  a.remove()
  // 立即 revoke 会让部分浏览器来不及取数据，交给下一轮事件循环
  setTimeout(() => URL.revokeObjectURL(url), 0)
}
