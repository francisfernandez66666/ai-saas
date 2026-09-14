// 配置分类面板（F1 从 Admin.tsx 拆出）：通用配置项渲染 + AI 链路专属布局。
import { Button, Input, InputNumber, Switch, Tag, Textarea } from 'tdesign-react'
import { resolveJsonMode } from '../../lib/jsonMode'
import type { Cfg } from './shared'

/** JSON 配置编辑器，提供格式校验与错误提示。 */
function JsonEditor({ cfg, value, onChange }: { cfg: Cfg; value: string; onChange: (v: string) => void }) {
  const mode = resolveJsonMode(value)
  let parsed: Array<number | string> | null = null
  try { parsed = JSON.parse(value) } catch { parsed = null }
  if (mode === 'objectArray') {
    return <Textarea value={value} onChange={(v) => onChange(v)} autosize={{ minRows: 4, maxRows: 12 }} />
  }
  if (mode === 'numberArray' && parsed) {
    return (
      <div className="flex flex-wrap items-center gap-2">
        {parsed.map((n, i) => (
          <span key={i} className="inline-flex items-center bg-indigo-50 text-indigo-700 rounded px-2 py-1 text-sm">
            <input
              type="number"
              className="w-14 bg-transparent text-center outline-none border-r border-indigo-200"
              value={n}
              onChange={(e) => {
                const arr = [...parsed]
                arr[i] = parseInt(e.target.value) || 0
                onChange(JSON.stringify(arr))
              }}
            />
            <button className="ml-1 text-indigo-400 hover:text-indigo-700" onClick={() => { const arr = parsed.filter((_: number | string, j: number) => j !== i); onChange(JSON.stringify(arr)) }}>✕</button>
          </span>
        ))}
        <Button size="small" variant="outline" theme="primary" onClick={() => onChange(JSON.stringify([...parsed, 0]))}>+ 添加</Button>
      </div>
    )
  }
  if (mode === 'stringArray' && parsed) {
    return (
      <div className="flex flex-wrap items-center gap-2">
        {parsed.map((s: string, i: number) => (
          <span key={i} className="inline-flex items-center bg-emerald-50 text-emerald-700 rounded px-2 py-1 text-sm">
            {s}
            <button className="ml-1 text-emerald-400 hover:text-emerald-700" onClick={() => { const arr = parsed.filter((_: number | string, j: number) => j !== i); onChange(JSON.stringify(arr)) }}>✕</button>
          </span>
        ))}
        <Button size="small" variant="outline" theme="primary" onClick={() => { const t = prompt('请输入新项'); if (t) onChange(JSON.stringify([...parsed, t])) }}>+ 添加</Button>
      </div>
    )
  }
  return <Textarea value={value} onChange={(v) => onChange(v)} autosize={{ minRows: 2, maxRows: 6 }} />
}

/** AI 链路配置面板：维护模型、阈值与降级参数。 */
function AIChainPanel({ cfgs, edits, setEdits }: { cfgs: Cfg[]; edits: Record<string, string>; setEdits: (k: string, v: string) => void }) {
  const priority = cfgs.find((c) => c.key === 'model_priority')
  const mock = cfgs.find((c) => c.key === 'mock_mode')
  let models: string[] = []
  try { models = JSON.parse(edits[priority?.key || 'model_priority'] || priority?.value || '[]') } catch { models = [] }
  const fallbackNames: Record<string, string> = {
    siliconflow_deepseek_v4_flash: 'DeepSeek V4 Flash (硅基流动)',
    siliconflow_glm4_9b: 'GLM-4-9B (硅基流动免费)',
    zhipu_glm4_flash: 'GLM-4-Flash (智谱)',
    template_fallback: '模板兜底（不调用AI）',
  }
  const move = (i: number, dir: -1 | 1) => {
    const arr = [...models]
    const j = i + dir
    if (j < 0 || j >= arr.length) return
    ;[arr[i], arr[j]] = [arr[j], arr[i]]
    if (priority) setEdits(priority.key, JSON.stringify(arr))
  }
  return (
    <div className="space-y-6">
      {priority && (
        <div className="bg-white rounded-lg border border-gray-200 p-5">
          <h3 className="text-sm font-semibold text-gray-800">模型降级优先级</h3>
          <p className="text-xs text-gray-400 mt-0.5">调整顺序，排在前面的模型优先使用</p>
          <ul className="mt-3 space-y-2">
            {models.map((id, i) => (
              <li key={id} className="flex items-center bg-white border border-gray-200 rounded-lg px-4 py-3">
                <span className="text-gray-400 mr-3 font-mono text-xs">{i + 1}</span>
                <span className="text-sm font-medium text-gray-800 flex-1">{fallbackNames[id] || id}</span>
                <span className="text-xs text-gray-400 font-mono mr-3">{id}</span>
                <Button size="small" variant="text" onClick={() => move(i, -1)}>↑</Button>
                <Button size="small" variant="text" onClick={() => move(i, 1)}>↓</Button>
              </li>
            ))}
          </ul>
        </div>
      )}
      {mock && (
        <div className="bg-white rounded-lg border border-gray-200 p-4 flex items-center justify-between">
          <div>
            <h3 className="text-sm font-semibold text-gray-800">{mock.description}</h3>
            <p className="text-xs text-orange-500 mt-2">⚠️ Mock模式开启后AI不会真实调用模型，仅返回模拟回复</p>
          </div>
          <Switch value={(edits[mock.key] ?? mock.value) === 'true'} onChange={(val) => setEdits(mock.key, val ? 'true' : 'false')} />
        </div>
      )}
    </div>
  )
}

/** 系统配置面板组：按分类渲染可编辑配置项。 */
export function ConfigPanels({ cfgs, edits, setEdits }: { cfgs: Cfg[]; edits: Record<string, string>; setEdits: (k: string, v: string) => void }) {
  if (cfgs[0]?.category === 'ai_chain') {
    return <AIChainPanel cfgs={cfgs} edits={edits} setEdits={setEdits} />
  }
  const cards = cfgs.map((c) => {
    const v = edits[c.key] ?? c.value
    let control: React.ReactNode = null
    if (c.key === 'reply_delay_mode') {
      const instant = v === 'instant'
      control = (
        <div className="flex items-center gap-3">
          <Tag theme={instant ? 'warning' : 'success'}>{instant ? '⚡ 秒回模式' : '🕐 正常延迟'}</Tag>
          <span className="text-xs text-gray-400">使用顶部"⚡ 延迟归零"按钮切换</span>
        </div>
      )
    } else if (c.value_type === 'number') {
      control = (
        <InputNumber
          value={parseFloat(v) || 0}
          step={String(c.default_value).includes('.') ? 0.01 : 1}
          onChange={(val) => setEdits(c.key, String(val ?? 0))}
          style={{ width: '100%' }}
        />
      )
    } else if (c.value_type === 'bool') {
      control = <Switch value={v === 'true'} onChange={(val) => setEdits(c.key, val ? 'true' : 'false')} />
    } else if (c.value_type === 'json') {
      control = <JsonEditor cfg={c} value={v} onChange={(nv) => setEdits(c.key, nv)} />
    } else {
      control = <Input value={v} onChange={(val) => setEdits(c.key, val)} />
    }
    return (
      <div key={c.key} className="bg-white rounded-lg border border-gray-200 p-4">
        <h3 className="text-sm font-semibold text-gray-800">{c.description || c.key}</h3>
        <p className="text-xs text-gray-400 mt-0.5">
          {c.key} <span className="ml-2 px-1.5 py-0.5 bg-gray-100 text-gray-500 rounded text-xs">{c.value_type}</span>
        </p>
        <div className="mt-3">{control}</div>
        {c.default_value ? <p className="text-xs text-gray-400 mt-2">默认值: {c.default_value}</p> : null}
      </div>
    )
  })
  return <div className="grid grid-cols-1 md:grid-cols-2 gap-4">{cards}</div>
}
