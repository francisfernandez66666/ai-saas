// PIPL 可携带副本的前端取数回归（FIX-10，2026-09-29 端到端审计批）。
// 后端单页只给 5000 条、其余靠游标续取，所以前端这份循环有三个必须成立的判据：
//   ① 多页必须**合并且不重不漏**（漏页=当事人以为拿到了全量）；
//   ② 任一页失败就整体失败，不许把已拿到的那几页照样下载（后端对读不到归档表
//      的策略是 500 且不给半成品，前端若在最后一米绕过就等于护栏没建）；
//   ③ 游标不推进必须报错停手（否则会"重复同一页"或"静默当成取完"）。
// 这三条在页面上都要靠真数据才看得见，故取数抽成纯函数在这里逐条钉。
import { describe, expect, it, vi } from 'vitest'
import { fetchPortableCopy, PortableCopyError, type PortablePage } from '../portability'

// 造一页响应：消息 id 从 start 起共 n 条，truncated/next 由调用方指定
function page(ids: number[], truncated: boolean, next: number): PortablePage {
  return {
    generated_at: '2026-09-29T10:00:00Z',
    customer: { id: 7, name: '张三', phone: '13800001111' },
    conversations: [{ id: 100 + ids[0], status: 'closed' }],
    messages: ids.map((id) => ({ id, content: `msg-${id}`, sender_type: 'customer' })),
    page: { row_cap: 2, limit: 2, after_id: ids[0] - 1, next_after_id: next, truncated },
  }
}

describe('fetchPortableCopy 游标翻页', () => {
  it('两页合并后消息逐条齐全、顺序按后端返回、翻页数如实记入 export_coverage', async () => {
    const post = vi.fn()
      .mockResolvedValueOnce(page([1, 2], true, 2))
      .mockResolvedValueOnce(page([3], false, 3))
    const doc = await fetchPortableCopy(7, 'vk_test', post)
    expect(doc.messages?.map((m) => m.id)).toEqual([1, 2, 3])
    // 反向对照：只断言消息会把"会话清单只留了第一页"这种漏法放过去
    expect(doc.conversations?.length).toBe(2)
    expect(doc.export_coverage).toEqual({ pages: 2, message_count: 3, row_cap: 2, complete: true })
  })

  it('每一页都带 after_id 续取，且身份参数逐页照传（后端每页都要重新认人）', async () => {
    const post = vi.fn()
      .mockResolvedValueOnce(page([1, 2], true, 2))
      .mockResolvedValueOnce(page([3], false, 3))
    await fetchPortableCopy(7, 'vk_test', post)
    expect(post.mock.calls[0][0]).toEqual({ customer_id: 7, visitor_key: 'vk_test', after_id: 0 })
    expect(post.mock.calls[1][0]).toEqual({ customer_id: 7, visitor_key: 'vk_test', after_id: 2 })
  })

  it('中间页失败：整体抛错且不返回任何消息（绝不下半截副本）', async () => {
    const post = vi.fn()
      .mockResolvedValueOnce(page([1, 2], true, 2))
      .mockResolvedValueOnce(null) // 后端 500 / 空 data 的形态
    await expect(fetchPortableCopy(7, 'vk', post)).rejects.toBeInstanceOf(PortableCopyError)
  })

  it('游标不推进（truncated=true 而 next_after_id 没变大）必须报错停手，不能无限重复同一页', async () => {
    const post = vi.fn().mockResolvedValue(page([1, 2], true, 2)) // 永远回同一页、永远说"还有"
    await expect(fetchPortableCopy(7, 'vk', post)).rejects.toThrow(/游标/)
    // 只多打一枪就停：这条断言防的是"改成重试到死"，那会把页面永远卡在导出中
    expect(post).toHaveBeenCalledTimes(2)
  })

  it('后端说没截断即停：不会多发一次"确认到底还有没有"的请求', async () => {
    const post = vi.fn().mockResolvedValue(page([1], false, 1))
    const doc = await fetchPortableCopy(7, 'vk', post)
    expect(post).toHaveBeenCalledTimes(1)
    expect(doc.messages?.length).toBe(1)
  })
})
