// F7/T6 流程引擎页冒烟测试：实例列表与推进按钮渲染。
import { render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { FlowEngineTab } from '../FlowEngineTab'

vi.mock('../../../lib/api', () => ({
  AUTH: vi.fn(),
}))

import { AUTH } from '../../../lib/api'
const authMock = AUTH as ReturnType<typeof vi.fn>

beforeEach(() => {
  localStorage.setItem('scrm_auth_token', 'test-token')
  authMock.mockReset()
})

describe('FlowEngineTab', () => {
  it('展示流程实例和状态', async () => {
    // 命名夹具：mock 与断言共用一份；节点中文名/状态都由夹具字段推导（不复制渲染读数）
    const NODE_START = { id: 'start', name: '开始' }
    const NODE_COND = { id: 'cond', name: '条件判断' }
    const DEF = { id: 10, code: 'default_chat_flow', name: '默认对话流程', status: 1, is_default: true, start_node_id: 'start', nodes_json: JSON.stringify([NODE_START, NODE_COND]), edges_json: JSON.stringify([{ from: 'start', to: 'cond' }]) }
    const INSTANCE = { id: 1, flow_def_id: 10, customer_id: 7, conversation_id: 8, current_node_id: NODE_COND.id, status: 'running', started_at: '2026-09-13', state_json: JSON.stringify({ executed_nodes: [NODE_START.id] }) }
    authMock.mockImplementation(async (url: string) => {
      if (url.includes('/flows/instances')) return { code: 0, data: [INSTANCE] }
      if (url.includes('/flows?page')) return { code: 0, data: { list: [DEF], total: 1 } }
      if (url.includes('/customers')) return { code: 0, data: { list: [{ id: 7, name: '张三' }], total: 1 } }
      return { code: 0, data: [] }
    })
    render(<FlowEngineTab configs={[]} />)
    await waitFor(() => expect(authMock).toHaveBeenCalledWith('/api/v1/flows/instances'))
    // 原来弱在哪：三条 toBeTruthy/length>0 各查各的，分不清这些字是不是同一行的；
    // 现在按流程编码定位实例行，行内钉「当前节点=nodes_json 的中文名」「状态=回包 status 原文」「客户=id」
    const instRow = (await screen.findByText(DEF.code)).closest('tr')
    if (!instRow) throw new Error('实例表格行未渲染：流程编码单元格不在 <tr> 内')
    expect(instRow).toHaveTextContent(NODE_COND.name)
    expect(instRow).toHaveTextContent(INSTANCE.status)
    expect(instRow).toHaveTextContent(String(INSTANCE.customer_id))
  })
})
