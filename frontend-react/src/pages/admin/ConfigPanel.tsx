// 配置分类面板（F1 从 Admin.tsx 拆出）：通用配置项渲染 + AI 链路专属布局。
import { Button, Input, InputNumber, Switch, Tag, Textarea } from 'tdesign-react'
import { resolveJsonMode } from '../../lib/jsonMode'
import { MODEL_FALLBACK_NAMES } from '../../lib/modelNames'
import { useI18n } from '../../i18n/useI18n'
import type { Cfg } from './shared'

/** JSON 配置编辑器，提供格式校验与错误提示。 */
function JsonEditor({ cfg: _cfg, value, onChange }: { cfg: Cfg; value: string; onChange: (v: string) => void }) {
  const { t } = useI18n()
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
        <Button size="small" variant="outline" theme="primary" onClick={() => onChange(JSON.stringify([...parsed, 0]))}>+ {t('admin.config.addItem')}</Button>
      </div>
    )
  }
  if (mode === 'stringArray' && parsed) {
    return (
      <div className="flex flex-wrap items-center gap-2">
        {parsed.map((s: string | number, i: number) => (
          <span key={i} className="inline-flex items-center bg-emerald-50 text-emerald-700 rounded px-2 py-1 text-sm">
            {s}
            <button className="ml-1 text-emerald-400 hover:text-emerald-700" onClick={() => { const arr = parsed.filter((_: number | string, j: number) => j !== i); onChange(JSON.stringify(arr)) }}>✕</button>
          </span>
        ))}
        <Button size="small" variant="outline" theme="primary" onClick={() => { const nv = prompt(t('admin.config.addItemPrompt')); if (nv) onChange(JSON.stringify([...parsed, nv])) }}>+ {t('admin.config.addItem')}</Button>
      </div>
    )
  }
  return <Textarea value={value} onChange={(v) => onChange(v)} autosize={{ minRows: 2, maxRows: 6 }} />
}

/** AI 链路配置面板：维护模型、阈值与降级参数。 */
function AIChainPanel({ cfgs, edits, setEdits }: { cfgs: Cfg[]; edits: Record<string, string>; setEdits: (k: string, v: string) => void }) {
  const priority = cfgs.find((c) => c.key === 'model_priority')
  const mock = cfgs.find((c) => c.key === 'mock_mode')
  const { t } = useI18n()
  let models: string[] = []
  try { models = JSON.parse(edits[priority?.key || 'model_priority'] || priority?.value || '[]') } catch { models = [] }
  // 上移/下移降级链中的一个模型条目（dir=-1 上移，dir=1 下移）
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
          <h3 className="text-sm font-semibold text-gray-800">{t('admin.config.modelPriorityTitle')}</h3>
          <p className="text-xs text-gray-400 mt-0.5">{t('admin.config.modelPriorityHint')}</p>
          <ul className="mt-3 space-y-2">
            {models.map((id, i) => (
              <li key={id} className="flex items-center bg-white border border-gray-200 rounded-lg px-4 py-3">
                <span className="text-gray-400 mr-3 font-mono text-xs">{i + 1}</span>
                <span className="text-sm font-medium text-gray-800 flex-1">{MODEL_FALLBACK_NAMES[id] || id}</span>
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
            <p className="text-xs text-orange-500 mt-2">{t('admin.config.mockWarning')}</p>
          </div>
          <Switch value={(edits[mock.key] ?? mock.value) === 'true'} onChange={(val) => setEdits(mock.key, val ? 'true' : 'false')} />
        </div>
      )}
    </div>
  )
}

/** 系统配置面板组：按分类渲染可编辑配置项。 */
export function ConfigPanels({ cfgs, edits, setEdits }: { cfgs: Cfg[]; edits: Record<string, string>; setEdits: (k: string, v: string) => void }) {
  const { t } = useI18n()
  if (cfgs[0]?.category === 'ai_chain') {
    return <AIChainPanel cfgs={cfgs} edits={edits} setEdits={setEdits} />
  }
  const cards = cfgs.map((c) => {
    const v = edits[c.key] ?? c.value
    let control: React.ReactNode
    if (c.key === 'reply_delay_mode') {
      const instant = v === 'instant'
      control = (
        <div className="flex items-center gap-3">
          <Tag theme={instant ? 'warning' : 'success'}>{instant ? t('admin.config.instantMode') : t('admin.config.normalDelay')}</Tag>
          <span className="text-xs text-gray-400">{t('admin.config.delayHint')}</span>
        </div>
      )
    } else if (c.key === 'static_qr_image') {
      // §W：静态收款码（pay_mode=static_qr 主收款通道）——URL 或 base64，给平台运营一个所见即所得预览位，
      // 避免盲填字符串后到租户收银台才发现码错。空值时退化为普通输入。
      const qr = String(v || '').trim()
      control = (
        <div>
          <Textarea value={v} onChange={(val) => setEdits(c.key, val)} autosize={{ minRows: 1, maxRows: 3 }} placeholder={t('admin.config.qrPlaceholder')} />
          {qr && (
            <div className="mt-2 flex items-center gap-2">
              <img src={qr} alt={t('admin.config.qrPreviewAlt')} style={{ width: 96, height: 96, objectFit: 'contain', border: '1px solid #e2e8f0', borderRadius: 6, background: '#fff' }}
                onError={(e) => { (e.currentTarget as HTMLImageElement).style.display = 'none' }} />
              <span className="text-xs text-gray-400">{t('admin.config.qrPreviewHint')}</span>
            </div>
          )}
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
        {c.default_value ? <p className="text-xs text-gray-400 mt-2">{t('admin.config.defaultValue')}: {c.default_value}</p> : null}
      </div>
    )
  })
  return <div className="grid grid-cols-1 md:grid-cols-2 gap-4">{cards}</div>
}
