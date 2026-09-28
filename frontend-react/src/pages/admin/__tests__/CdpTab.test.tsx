// F8/T6 CDP 画像页冒烟测试。
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import CdpTab from '../CdpTab'

vi.mock('../../../lib/api', () => ({
  AUTH: vi.fn(),
}))

import { AUTH } from '../../../lib/api'
const authMock = AUTH as ReturnType<typeof vi.fn>

beforeEach(() => {
  localStorage.setItem('scrm_auth_token', 'test-token')
  authMock.mockReset()
})

describe('CdpTab', () => {
  it('展示标签字典并查询 OneID 画像', async () => {
    authMock.mockImplementation(async (url: string) => {
      if (url.includes('/tag-defs')) {
        return { code: 0, data: [{ id: 1, code: 'beh_lead_captured', name: '已留资', category: 'behavior', weight_default: 2, is_active: true }] }
      }
      if (url.includes('/customers')) return { code: 0, data: { list: [{ id: 123, name: '张三' }], total: 1 } }
      if (url.includes('/segments')) return { code: 0, data: { tag: 'beh_lead_captured', total: 1, one_ids: ['c:123'] } }
      if (url.includes('/profiles/')) return { code: 0, data: { one_id: 'c:123', name: '张三', status: 1, event_count: 8, tags: { beh_lead_captured: 'yes' } } }
      return { code: 0, data: [] }
    })
    render(<CdpTab />)
    await waitFor(() => expect(authMock).toHaveBeenCalledWith('/api/v1/cdp/tag-defs'))
    // 原来弱在哪：truthy 只证明"已留资"三个字在场——出现在字典表还是别处、行里
    // 编码/权重对不对都不管。现在钉：命中的是标签字典表那行 <tr>，且同带着夹具的
    // code 与 weight_default（CdpTab.tsx 列定义 code/name/…/weight_default）。
    const tagNameCell = await screen.findByText('已留资')
    expect(tagNameCell).toHaveTextContent('已留资')
    const defRow = tagNameCell.closest('tr') as HTMLElement
    expect(defRow).toHaveTextContent('beh_lead_captured')
    expect(defRow).toHaveTextContent('2')
    fireEvent.click(screen.getByRole('button', { name: '圈选' }))
    await waitFor(() => expect(authMock).toHaveBeenCalledWith('/api/v1/cdp/segments?tag=beh_lead_captured'))
    // one_ids 夹具只有一条 'c:123'：等值钉"恰好一个 Tag"，不是"页面上有这串字"
    const idTags = await screen.findAllByText('c:123')
    expect(idTags).toHaveLength(1)
    expect(idTags[0]).toHaveTextContent('c:123')
    fireEvent.click(screen.getByRole('button', { name: '123 张三' }))
    // 「事件数」格与值同容器（画像卡每格是 <div><div>标签</div><div>值</div></div>），
    // 值=夹具 event_count=8；旧断言只证明页面上飘着"事件数"三个字。
    const evLabel = await screen.findByText('事件数')
    expect(evLabel.parentElement).toHaveTextContent('事件数8')
    // 画像标签表：profileTags 由 tags 对象 + 字典联名而来——值格 'yes' 与名称格
    // '已留资'、编码格必须在同一 <tr>（错行=联表挂错）。
    const yesCell = await screen.findByText('yes')
    expect(yesCell).toHaveTextContent('yes')
    expect(yesCell.closest('tr')).toHaveTextContent('beh_lead_captured')
  })
})
