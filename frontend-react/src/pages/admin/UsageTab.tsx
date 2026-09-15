// 用量看板 Tab（F1 从 Admin.tsx 拆出）：近 30 天用量汇总。
import { useEffect, useState } from 'react'
import { authHeaders } from '../../lib/api'
import type { TableRowData } from '../../types'

/** 用量看板 Tab：展示 Token、请求和成本趋势。 */
export function UsageTab() {
  const [data, setData] = useState<TableRowData | null>(null)
  // E6 修复(2026-09-14)：失败态不再永久"加载中"——网络断/非 0 码给出错误提示与重试
  const [err, setErr] = useState('')
  // 拉取近 30 天 Token 用量汇总（E6：失败落错误态并提供"重试"按钮）
  const load = () => {
    setErr('')
    fetch('/api/v1/admin/usage/summary?days=30', { headers: authHeaders() })
      .then((r) => r.json()).then((j) => { if (j?.code === 0) setData(j.data); else setErr(j?.message || '加载失败') }).catch(() => setErr('网络异常，加载失败'))
  }
  useEffect(() => { load() }, [])
  if (err) return <p style={{ color: '#e53e3e', padding: 40, textAlign: 'center' }}>{err}　<button onClick={load} style={{ background: 'none', border: '1px solid #e53e3e', borderRadius: 6, padding: '2px 10px', cursor: 'pointer' }}>重试</button></p>
  if (!data) return <p style={{ color: '#9ca3af', padding: 40, textAlign: 'center' }}>加载中...</p>
  const entries = Object.entries(data).filter(([, v]) => typeof v === 'number' || typeof v === 'string')
  return (
    <div className="grid grid-cols-2 md:grid-cols-4 gap-4">
      {entries.map(([k, v]) => (
        <div key={k} className="bg-white rounded-lg shadow-sm p-4">
          <p style={{ fontSize: 12, color: '#9ca3af', marginBottom: 4 }}>{k}</p>
          <b style={{ fontSize: 20, color: '#4f46e5' }}>{String(v)}</b>
        </div>
      ))}
    </div>
  )
}
