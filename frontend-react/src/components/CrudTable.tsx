// F1 通用 CRUD 表格壳：封装 loading/空态/错误态/分页与行操作。
// 列定义沿用 TDesign Table，具体业务列由调用方传入，避免把 CRUD 表单硬塞进通用组件。
import { Button, Table } from 'tdesign-react'
import type { PrimaryTableCol, TableRowData } from 'tdesign-react'

type CrudTableProps = {
  rowKey?: string
  data: Record<string, any>[]
  columns: PrimaryTableCol<TableRowData>[]
  loading?: boolean
  total?: number
  page?: number
  pageSize?: number
  empty?: string
  error?: string
  actions?: (row: Record<string, any>, index: number) => React.ReactNode
  onPageChange?: (page: number, pageSize: number) => void
}

export function CrudTable({
  rowKey = 'id',
  data,
  columns,
  loading,
  total = 0,
  page = 1,
  pageSize = 20,
  empty = '暂无数据',
  error = '',
  actions,
  onPageChange,
}: CrudTableProps) {
  const cols = actions ? [...columns, {
    colKey: 'actions',
    title: '操作',
    width: 220,
    fixed: 'right' as const,
    cell: (p: { row: Record<string, any>; type?: number }) => actions(p.row, p.type ?? 0),
  }] : columns

  return (
    <div className="space-y-3">
      {error ? (
        <div style={{ color: '#b91c1c', background: '#fef2f2', border: '1px solid #fecaca', borderRadius: 8, padding: '8px 10px', fontSize: 13 }}>
          {error}
        </div>
      ) : null}
      <Table
        rowKey={rowKey}
        data={data as TableRowData[]}
        columns={cols}
        loading={loading}
        empty={empty}
        size="small"
        pagination={onPageChange ? {
          current: page,
          pageSize,
          total,
          onChange: ({ current, pageSize: nextSize }: { current: number; pageSize: number }) => onPageChange(current, nextSize),
        } : undefined}
      />
    </div>
  )
}

export function RowActionButton(props: { children: React.ReactNode; onClick: () => void; danger?: boolean }) {
  return (
    <Button
      size="small"
      variant="text"
      theme={props.danger ? 'danger' : 'primary'}
      style={{ padding: '2px 6px', fontSize: 12 }}
      onClick={props.onClick}
    >
      {props.children}
    </Button>
  )
}
