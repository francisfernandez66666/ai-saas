// F3 租户资料上传页：JSON/MD/TXT 文件解析后上传到 /admin/kb/upload，并列出/删除租户自有片段；P2-14 文案迁移至 i18n
import { useCallback, useMemo, useRef, useState } from 'react'
import { confirmDialog } from '../../lib/confirm'
import { Button, Input, MessagePlugin, Table, Tag, Textarea } from 'tdesign-react'
import { useCrud } from '../../hooks/useCrud'
import { AUTH } from '../../lib/api'
import { useI18n } from '../../i18n/useI18n'
import { parseKBFile, type KBUpsertPayload } from '../../lib/tenantKb'
import { KB_DEFAULT_CATEGORY } from '../../lib/kb'
import type { CellProps, TableRowData } from '../../types'
import type { Cfg } from './shared'

const MAX_FILE_BYTES = 2 * 1024 * 1024
const ACCEPT_EXT = ['json', 'md', 'markdown', 'txt']

/** 读取文件名扩展名，用于上传类型判断。 */
function extOf(name: string) {
  return name.split('.').pop()?.toLowerCase() || ''
}

/** 根据片段向量化状态渲染标签。 */
function vectorReady(row: TableRowData): boolean {
  return row.vectorized === true || String(row.embedding_json || '').length > 0
}

// TenantKBTab 后台「我的知识库」页：以通用 CRUD 组件维护本租户私有知识条目。
export default function TenantKBTab({ configs = [] }: { configs?: Cfg[] }) {
  const { t } = useI18n()
  const crud = useCrud('/api/v1/admin/kb/my', { pageSize: 20 })
  const fileRef = useRef<HTMLInputElement>(null)
  const [uploading, setUploading] = useState(false)
  const [manual, setManual] = useState({ title: '', content: '' })

  const vectorCfg = useMemo(() => configs.find((c) => c.key === 'kb_vector_search'), [configs])
  const vectorOn = vectorCfg?.value !== 'false'

  // 批量上传知识片段（F3）：POST 后按返回码聚合成功/失败并刷新列表
  const uploadPayloads = useCallback(async (items: KBUpsertPayload[]) => {
    if (items.length === 0) {
      MessagePlugin.warning(t('admin.kb.warnEmpty'))
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
      MessagePlugin.success(t('admin.kb.uploadOk', { ok, total: items.length }))
      await crud.reload()
    }
    return ok > 0
  }, [crud, t])

  // 处理本地文件选择：逐个读取文本内容，转成知识片段 payload 后批量上传
  const onFiles = async (files: FileList | null) => {
    if (!files || files.length === 0) return
    const accepted = Array.from(files).filter((f) => ACCEPT_EXT.includes(extOf(f.name)))
    if (accepted.length !== files.length) MessagePlugin.warning(t('admin.kb.warnExt'))
    const items: KBUpsertPayload[] = []
    for (const f of accepted) {
      if (f.size > MAX_FILE_BYTES) {
        MessagePlugin.warning(t('admin.kb.warnSize', { name: f.name }))
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
      MessagePlugin.warning(t('admin.kb.warnTitle'))
      return
    }
    const ok = await uploadPayloads([{ title: manual.title.trim(), content: manual.content, category: KB_DEFAULT_CATEGORY }])
    if (ok) setManual({ title: '', content: '' })
  }

  // 删除单个租户知识片段（二次确认，复用 crud.remove）
  const remove = async (id: number) => {
    if (!(await confirmDialog(t('admin.kb.confirmDel')))) return
    const j = await crud.remove(id)
    if (j?.code === 0) MessagePlugin.success(t('admin.kb.deleted'))
  }

  const cols = [
    { colKey: 'id', title: 'ID', width: 70 },
    { colKey: 'title', title: t('common.title'), width: 180 },
    { colKey: 'category', title: t('common.category'), width: 110 },
    { colKey: 'content', title: t('common.content'), ellipsis: true },
    { colKey: 'vectorized', title: t('admin.kb.colVector'), width: 110, cell: (p: CellProps) => <Tag theme={vectorReady(p.row) ? 'success' : 'warning'}>{vectorReady(p.row) ? t('admin.kb.vecReady') : t('admin.kb.vecPending')}</Tag> },
    { colKey: 'created_at', title: t('common.createdAt'), width: 170, cell: (p: CellProps) => p.row.created_at ? new Date(p.row.created_at).toLocaleString('zh-CN') : '-' },
    { colKey: 'op', title: t('common.op'), width: 90, fixed: 'right' as const, cell: (p: CellProps) => <Button size="small" variant="text" theme="danger" onClick={() => remove(p.row.id)}>{t('admin.openapi.del')}</Button> },
  ]

  return (
    <div className="space-y-4">
      <div className="bg-white rounded-lg shadow-sm p-5">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div>
            <h3 className="text-base font-bold text-gray-800">{t('admin.kb.title')}</h3>
            <p className="text-xs text-gray-400 mt-1">{t('admin.kb.desc')}</p>
          </div>
          <Tag theme={vectorOn ? 'success' : 'warning'}>{vectorOn ? t('admin.kb.vecOn') : t('admin.kb.vecOff')}</Tag>
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
            <Button theme="primary" variant="outline" disabled={uploading} onClick={() => fileRef.current?.click()}>{t('admin.kb.chooseFile')}</Button>
          </div>
          <div className="md:col-span-3">
            <Input value={manual.title} onChange={(v) => setManual((s) => ({ ...s, title: v }))} placeholder={t('admin.kb.phTitle')} />
          </div>
          <div className="md:col-span-3">
            <Textarea value={manual.content} onChange={(v) => setManual((s) => ({ ...s, content: v }))} autosize={{ minRows: 4, maxRows: 12 }} placeholder={t('admin.kb.phContent')} />
          </div>
          <div className="md:col-span-3">
            <Button theme="primary" loading={uploading} disabled={uploading} onClick={submitManual}>{t('admin.kb.upload')}</Button>
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
            <h3 className="text-base font-bold text-gray-800">{t('admin.kb.uploadedTitle')}</h3>
            <p className="text-xs text-gray-400 mt-1">{t('admin.kb.total', { n: crud.total })}</p>
          </div>
          <Button size="small" variant="outline" onClick={() => crud.reload()}>{t('admin.kb.refresh')}</Button>
        </div>
        <Table
          rowKey="id"
          data={crud.rows as TableRowData[]}
          columns={cols}
          loading={crud.loading}
          size="small"
          empty={t('admin.kb.empty')}
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
