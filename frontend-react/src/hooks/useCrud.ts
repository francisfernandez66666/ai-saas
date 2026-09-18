// F1 通用 CRUD 请求状态层：列表加载/分页/增删改/行内操作统一收口。
// 仅处理后端 RespOK 信封，业务 toast 沿用 lib/api.ts，避免各 Tab 重复样板。
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { AUTH } from '../lib/api'

/** CRUD 行数据宽松形态：后端各列表字段不一，单元格渲染按列 spec 取值 */
export type CrudRow = Record<string, any>

/** useCrud 可选项：分页/筛选/自动加载/列表 URL 构造器（默认 base?page=&page_size= 口径） */
export type UseCrudOptions = {
  pageSize?: number
  initialFilters?: Record<string, any>
  autoLoad?: boolean
  buildListUrl?: (base: string, page: number, pageSize: number, filters: Record<string, any>) => string
  parseList?: (json: any) => { rows: CrudRow[]; total: number }
}

/** 默认列表响应解析器，兼容 rows/data 两类后端分页结构。 */
function defaultParseList(json: any): { rows: CrudRow[]; total: number } {
  const data = json?.data
  if (Array.isArray(data)) return { rows: data, total: data.length }
  const rows = Array.isArray(data?.list) ? data.list : []
  return { rows, total: Number(data?.total ?? rows.length) }
}

/** 给列表接口拼接页码、分页大小和筛选条件。 */
function withQuery(base: string, page: number, pageSize: number, filters: Record<string, any>) {
  const q = new URLSearchParams()
  if (page > 1 || filters.page === undefined) q.set('page', String(page))
  q.set('page_size', String(pageSize))
  Object.entries(filters || {}).forEach(([k, v]) => {
    if (v !== '' && v !== undefined && v !== null) q.set(k, String(v))
  })
  const joiner = base.includes('?') ? '&' : '?'
  return base + joiner + q
}

/** 通用 CRUD Hook：管理列表、分页、筛选、表单和增删改查请求。 */
export function useCrud(base: string, opts: UseCrudOptions = {}) {
  const { pageSize: initialPageSize = 20, initialFilters = {}, autoLoad = true } = opts
  const parseList = opts.parseList || defaultParseList
  const buildListUrl = opts.buildListUrl || withQuery
  const [rows, setRows] = useState<CrudRow[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = useState(initialPageSize)
  const [filters, setFilters] = useState<Record<string, any>>(initialFilters)
  const [loading, setLoading] = useState(false)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  // P1-13 修复(2026-09-15)：请求代际守卫——快速切筛选/翻页时并发多个 load，
  // 旧请求后返回会把新结果覆盖回旧数据（列表闪烁/与筛选条件错位）。
  // 只有"最后一次发起"的请求才允许落状态。
  const seqRef = useRef(0)

  const load = useCallback(async (nextFilters = filters, nextPage = page, nextPageSize = pageSize) => {
    const mySeq = ++seqRef.current
    setLoading(true)
    setError('')
    const j = await AUTH(buildListUrl(base, nextPage, nextPageSize, nextFilters))
    if (mySeq !== seqRef.current) return // 已有更新请求在途/完成，本响应作废
    setLoading(false)
    if (j?.code !== 0) {
      setError(j?.message || '加载失败')
      return
    }
    const parsed = parseList(j)
    setRows(parsed.rows)
    setTotal(parsed.total)
  }, [base, buildListUrl, filters, page, pageSize, parseList])

  useEffect(() => {
    if (autoLoad) void load(filters, page, pageSize)
  }, [autoLoad, base, page, pageSize, filters, load])

  const changePage = useCallback((nextPage: number, nextSize = pageSize) => {
    setPage(nextPage)
    setPageSize(nextSize)
  }, [pageSize])

  const changePageSize = useCallback((nextSize: number) => {
    setPage(1)
    setPageSize(nextSize)
  }, [])

  const setFilter = useCallback((k: string, v: any) => {
    setFilters((prev) => ({ ...prev, [k]: v }))
    setPage(1)
  }, [])

  const resetFilters = useCallback((nextFilters: Record<string, any> = {}) => {
    setFilters(nextFilters)
    setPage(1)
  }, [])

  const create = useCallback(async (body: CrudRow, path = base, method = 'POST') => {
    setSaving(true)
    const j = await AUTH(path, { method, body })
    setSaving(false)
    if (j?.code === 0) await load()
    return j
  }, [base, load])

  const update = useCallback(async (id: number | string, body: CrudRow, path?: string) => {
    setSaving(true)
    const j = await AUTH(path || `${base}/${id}`, { method: 'PUT', body })
    setSaving(false)
    if (j?.code === 0) await load()
    return j
  }, [base, load])

  const remove = useCallback(async (id: number | string, path?: string) => {
    setSaving(true)
    const j = await AUTH(path || `${base}/${id}`, { method: 'DELETE' })
    setSaving(false)
    if (j?.code === 0) await load()
    return j
  }, [base, load])

  const action = useCallback(async (path: string, method = 'POST', body?: CrudRow) => {
    setSaving(true)
    const j = await AUTH(path, { method, body })
    setSaving(false)
    if (j?.code === 0) await load()
    return j
  }, [load])

  const rowMap = useMemo(() => new Map(rows.map((r) => [String(r.id ?? r.code ?? r.key ?? r.name), r])), [rows])
  return {
    rows, total, page, pageSize, changePage, changePageSize, filters, setFilter, resetFilters,
    loading, saving, error, reload: load, create, update, remove, action, rowMap,
  }
}
