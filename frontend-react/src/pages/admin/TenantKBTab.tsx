// F3 租户资料上传页：JSON/MD/TXT 文件解析后上传到 /admin/kb/upload，并列出/删除租户自有片段。
import { useCallback, useMemo, useRef, useState } from 'react'
import { confirmDialog } from '../../lib/confirm'
import { Button, Input, MessagePlugin, Table, Tag, Textarea } from 'tdesign-react'
import { useCrud } from '../../hooks/useCrud'
import { AUTH } from '../../lib/api'
import { parseKBFile, type KBUpsertPayload } from '../../lib/tenantKb'
import type { CellProps, TableRowData } from '../../types'
import type { Cfg } from './shared'

const MAX_FILE_BYTES = 2 * 1024 * 1024
const ACCEPT_EXT = ['json', 'md', 'markdown', 'txt']

/** 读取文件名扩展名，用于上传类型判断。 */
function extOf(name: string) {
  return name.split('.').pop()?.toLowerCase() || ''
}

/** 根据片段向量化状态渲染标签。 */
function vectorTag(row: TableRowData) {
  const ready = row.vectorized === true || String(row.embedding_json || '').length > 0
  return <Tag theme={ready ? 'success' : 'warning'}>{ready ? '已向量化' : '待向量化'}</Tag>
}

// TenantKBTab 后台「我的知识库」页：以通用 CRUD 组件维护本租户私有知识条目。
export default function TenantKBTab({ configs = [] }: { configs?: Cfg[] }) {
  const crud = useCrud('/api/v1/admin/kb/my', { pageSize: 20 })
  const fileRef = useRef<HTMLInputElement>(null)
  const [uploading, setUploading] = useState(false)
  const [manual, setManual] = useState({ title: '', content: '' })

  const vectorCfg = useMemo(() => configs.find((c) => c.key === 'kb_vector_search'), [configs])
  const vectorOn = vectorCfg?.value !== 'false'

  // 批量上传知识片段（F3）：POST 后按返回码聚合成功/失败并刷新列表
  const uploadPayloads = useCallback(async (items: KBUpsertPayload[]) => {
    if (items.length === 0) {
      MessagePlugin.warning('没有可上传的有效内容')
      return false
    }
    setUploading(true)
    let ok = 0
    for (const item of items) {
      const j = await AUTH('/api/v1/admin/kb/upload', { method: 'POST', body: item })
      if (j?.code === 0) ok++
    }
    setUploading(false)
    if (ok > 0) {
      MessagePlugin.success(`上传成功：${ok}/${items.length} 条`)
      await crud.reload()
    }
    return ok > 0
  }, [crud])

  // 处理本地文件选择：逐个读取文本内容，转成知识片段 payload 后批量上传
  const onFiles = async (files: FileList | null) => {
    if (!files || files.length === 0) return
    const accepted = Array.from(files).filter((f) => ACCEPT_EXT.includes(extOf(f.name)))
    if (accepted.length !== files.length) MessagePlugin.warning('仅支持 JSON/Markdown/TXT 文件')
    const items: KBUpsertPayload[] = []
    for (const f of accepted) {
      if (f.size > MAX_FILE_BYTES) {
        MessagePlugin.warning(`${f.name} 超过 2MB，已跳过`)
        continue
      }
      const text = await f.text()
      items.push(...parseKBFile(f.name, text))
    }
    await uploadPayloads(items)
    if (fileRef.current) fileRef.current.value = ''
  }

  // 提交手工录入的知识片段（标题/正文），成功后清空表单
  const submitManual = async () => {
    if (!manual.title.trim() || !manual.content.trim()) {
      MessagePlugin.warning('请填写标题和内容')
      return
    }
    const ok = await uploadPayloads([{ title: manual.title.trim(), content: manual.content, category: '企业知识' }])
    if (ok) setManual({ title: '', content: '' })
  }

  // 删除单个租户知识片段（二次确认，复用 crud.remove）
  const remove = async (id: number) => {
    if (!(await confirmDialog('确认删除该租户知识片段？'))) return
    const j = await crud.remove(id)
    if (j?.code === 0) MessagePlugin.success('已删除')
  }


  const cols = [
    { colKey: 'id', title: 'ID', width: 70 },
    { colKey: 'title', title: '标题', width: 180 },
    { colKey: 'category', title: '分类', width: 110 },
    { colKey: 'content', title: '内容', ellipsis: true },
    { colKey: 'vectorized', title: '向量', width: 110, cell: (p: CellProps) => vectorTag(p.row) },
    { colKey: 'created_at', title: '创建时间', width: 170, cell: (p: CellProps) => p.row.created_at ? new Date(p.row.created_at).toLocaleString('zh-CN') : '-' },
    { colKey: 'op', title: '操作', width: 90, fixed: 'right' as const, cell: (p: CellProps) => <Button size="small" variant="text" theme="danger" onClick={() => remove(p.row.id)}>删除</Button> },
  ]

  return (
    <div className="space-y-4">
      <div className="bg-white rounded-lg shadow-sm p-5">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div>
            <h3 className="text-base font-bold text-gray-800">租户资料</h3>
            <p className="text-xs text-gray-400 mt-1">上传企业自有资料，系统自动切片入库；检索时租户资料优先于行业包。</p>
          </div>
          <Tag theme={vectorOn ? 'success' : 'warning'}>{vectorOn ? '向量融合检索开启' : '向量融合检索关闭'}</Tag>
        </div>
        <div className="mt-4 grid grid-cols-1 md:grid-cols-3 gap-3">
          <div className="md:col-span-3 flex items-center gap-3">
            <input
              ref={fileRef}
              type="file"
              multiple
              accept=".json,.md,.markdown,.txt"
              className="text-sm text-gray-600 file:mr-3 file:rounded file:border-0 file:bg-indigo-50 file:px-3 file:py-2 file:text-sm file:text-indigo-700"
              onChange={(e) => onFiles((e.target as HTMLInputElement).files)}
            />
            <Button theme="primary" variant="outline" disabled={uploading} onClick={() => fileRef.current?.click()}>选择文件</Button>
          </div>
          <div className="md:col-span-3">
            <Input value={manual.title} onChange={(v) => setManual((s) => ({ ...s, title: v }))} placeholder="手动片段标题" />
          </div>
          <div className="md:col-span-3">
            <Textarea value={manual.content} onChange={(v) => setManual((s) => ({ ...s, content: v }))} autosize={{ minRows: 4, maxRows: 12 }} placeholder="也可直接粘贴一段知识内容，上传后按 400 字左右自动切片" />
          </div>
          <div className="md:col-span-3">
            <Button theme="primary" loading={uploading} disabled={uploading} onClick={submitManual}>上传内容</Button>
          </div>
        </div>
        {crud.error ? (
          <div style={{ marginTop: 12, color: '#b91c1c', background: '#fef2f2', border: '1px solid #fecaca', borderRadius: 8, padding: '8px 10px', fontSize: 13 }}>
            {crud.error}
          </div>
        ) : null}
      </div>

      <div className="bg-white rounded-lg shadow-sm p-4">
        <div className="mb-3 flex items-center justify-between">
          <div>
            <h3 className="text-base font-bold text-gray-800">已上传片段</h3>
            <p className="text-xs text-gray-400 mt-1">共 {crud.total} 条</p>
          </div>
          <Button size="small" variant="outline" onClick={() => crud.reload()}>刷新</Button>
        </div>
        <Table
          rowKey="id"
          data={crud.rows as TableRowData[]}
          columns={cols}
          loading={crud.loading}
          size="small"
          empty="暂无租户资料"
          pagination={{
            current: crud.page,
            pageSize: crud.pageSize,
            total: crud.total,
            onChange: ({ current, pageSize: nextSize }: { current: number; pageSize: number }) => crud.changePage(current, nextSize),
          }}
        />
      </div>
    </div>
  )
}
