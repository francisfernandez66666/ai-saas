// 用量看板 Tab（F1 从 Admin.tsx 拆出）：近 30 天用量汇总。
import { useEffect, useState } from 'react'
import { getToken } from '../../lib/api'
import type { TableRowData } from '../../types'

export function UsageTab() {
  const [data, setData] = useState<TableRowData | null>(null)
  useEffect(() => {
    fetch('/api/v1/admin/usage/summary?days=30', { headers: { Authorization: 'Bearer ' + getToken() } })
      .then((r) => r.json()).then((j) => { if (j.code === 0) setData(j.data) }).catch(() => {})
  }, [])
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
