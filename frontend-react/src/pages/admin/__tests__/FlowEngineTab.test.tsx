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
    authMock.mockImplementation(async (url: string) => {
      if (url.includes('/flows/instances')) {
        return { code: 0, data: [{ id: 1, flow_def_id: 10, customer_id: 7, conversation_id: 8, current_node_id: 'cond', status: 'running', started_at: '2026-09-13', state_json: JSON.stringify({ executed_nodes: ['start'] }) }] }
      }
      if (url.includes('/flows?page')) {
        return { code: 0, data: { list: [{ id: 10, code: 'default_chat_flow', name: '默认对话流程', status: 1, is_default: true, start_node_id: 'start', nodes_json: JSON.stringify([{ id: 'start', name: '开始' }, { id: 'cond', name: '条件判断' }]), edges_json: JSON.stringify([{ from: 'start', to: 'cond' }]) }], total: 1 } }
      }
      if (url.includes('/customers')) return { code: 0, data: { list: [{ id: 7, name: '张三' }], total: 1 } }
      return { code: 0, data: [] }
    })
    render(<FlowEngineTab configs={[]} />)
    await waitFor(() => expect(authMock).toHaveBeenCalledWith('/api/v1/flows/instances'))
    expect(await screen.findByText('default_chat_flow')).toBeTruthy()
    expect(screen.getAllByText('条件判断').length).toBeGreaterThan(0)
    expect(screen.getByText('running')).toBeTruthy()
  })
})
