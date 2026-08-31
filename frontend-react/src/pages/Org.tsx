/**
 * Org.tsx：组织架构管理页
 * 部门树（增删改/移动）与成员列表（启停/新增），按当前角色展示可操作按钮
 * 依赖接口：/api/v1/org/departments/*、/api/v1/org/users
 */
import { useState, useEffect, useMemo } from 'react'
import { Dialog, Input, Select, Button, Tag, MessagePlugin } from 'tdesign-react'
import { useBrand } from '../lib/branding'
import { getToken } from '../lib/api'

// 部门树节点类型（含子节点，递归结构）
type Dept = { id: number; name: string; depth: number; path: string; user_count: number; children?: Dept[] }
// 组织架构成员类型
type User = { id: number; username: string; real_name?: string; role: string; department_id?: number; dept_name?: string; status: number }

// 组织架构接口鉴权头
const AUTH = (): any => ({ headers: { Authorization: 'Bearer ' + getToken(), 'Content-Type': 'application/json' } })
// 当前用户角色（来自 localStorage，决定可执行的部门/成员操作）
const ROLE = localStorage.getItem('role') || ''
// 角色中文映射
const ROLE_CN: Record<string, string> = { super_admin: '平台超管', tenant_admin: '租户管理员', dept_admin: '部门管理员', user: '成员', readonly: '只读' }

/**
 * 组织架构管理页组件
 * 左右分栏布局：
 * - 左侧：部门树（递归渲染，支持展开/折叠、新建子部门、重命名、移动、删除）
 * - 右侧：成员列表（按部门筛选，支持启停、新增成员）
 * 操作按钮按当前用户角色显示/隐藏
 */
export default function Org() {
  const brand = useBrand()
  // 部门树数据
  const [tree, setTree] = useState<Dept[]>([])
  // 部门平铺列表（用于父部门下拉与成员部门名映射）
  const [depts, setDepts] = useState<Dept[]>([])
  // 成员列表
  const [users, setUsers] = useState<User[]>([])
  // 当前选中的部门 ID
  const [sel, setSel] = useState<number | null>(null)
  // 当前选中部门名称（用于标题展示）
  const [curDeptName, setCurDeptName] = useState('全部')
  // 部门操作弹窗状态
  const [dlg, setDlg] = useState<{ mode: string; id?: number | null; title: string } | null>(null)
  // 部门表单：名称和父部门 ID
  const [fName, setFName] = useState('')
  const [fParent, setFParent] = useState<number | null>(null)
  // 新增成员弹窗状态
  const [userDlg, setUserDlg] = useState(false)
  // 新增成员表单数据
  const [u, setU] = useState({ username: '', password: '', real_name: '', role: 'user', department_id: 0 })

  /**
   * 加载部门树数据，并展平为平铺列表
   * 平铺列表用于：父部门下拉选择、成员部门名映射
   */
  async function loadTree() {
    const r = await fetch('/api/v1/org/departments/tree', AUTH())
    const j = await r.json()
    const t = j.data || []
    setTree(t)
    // 递归展平部门树
    const flat: Dept[] = []
    // 递归遍历部门树，把所有节点压平到 flat 数组（供下拉选择/列表展示）
    const walk = (ns: Dept[]) => ns.forEach((n) => { flat.push(n); if (n.children) walk(n.children) })
    walk(t)
    setDepts(flat)
  }

  /**
   * 加载成员列表
   * 若已选中部门则只显示该部门成员（前端筛选）
   */
  async function loadUsers() {
    const r = await fetch('/api/v1/org/users', AUTH())
    const j = await r.json()
    let list: User[] = j.data || []
    if (sel) list = list.filter((x) => x.department_id === sel)
    setUsers(list)
  }

  // 路由守卫：无 token 跳登录；否则加载部门树与成员列表
  useEffect(() => {
    if (!getToken()) { location.href = '/login'; return }
    loadTree(); loadUsers()
  }, [])
  // 选中部门变化时重新加载成员列表
  useEffect(() => { loadUsers() }, [sel])

  /**
   * 生成父部门下拉选项
   * 按层级缩进展示；exceptId 用于编辑时排除自身避免挂到自己下面
   */
  const deptOptions = (exceptId?: number) => depts.filter((d) => d.id !== exceptId).map((d) => ({ label: '　'.repeat((d.depth || 1) - 1) + d.name, value: d.id }))

  /**
   * 打开部门操作弹窗
   * @param mode - 操作类型：add（新建）、rename（重命名）、move（移动）
   * @param id - 部门 ID（新建子部门时传父部门 ID）
   * @param name - 部门名称（重命名时传当前名称）
   */
  function openDept(mode: string, id?: number | null, name?: string) {
    setDlg({ mode, id, title: mode === 'add' ? (id ? '新建子部门' : '新建根部门') : mode === 'rename' ? '重命名部门' : '移动部门到…' })
    setFName(name || '')
    // 疑点：setFParent(id ? null : null) 恒为 null，新建子部门时未预置父部门，用户需手动在下拉里选父级
    setFParent(id ? null : null)
  }

  /**
   * 提交部门弹窗：按当前模式分别走新建/重命名/移动接口
   */
  async function submitDept() {
    const name = fName.trim()
    if (dlg?.mode === 'add') await fetch('/api/v1/org/departments', { method: 'POST', headers: AUTH(), body: JSON.stringify({ name, parent_id: fParent }) })
    else if (dlg?.mode === 'rename') await fetch('/api/v1/org/departments/' + dlg.id, { method: 'PUT', headers: AUTH(), body: JSON.stringify({ name }) })
    else if (dlg?.mode === 'move') await fetch('/api/v1/org/departments/' + dlg.id, { method: 'PUT', headers: AUTH(), body: JSON.stringify({ new_parent_id: fParent }) })
    setDlg(null); loadTree()
  }

  /**
   * 删除空部门（带确认）
   * 成功后刷新部门树
   */
  async function delDept(id: number) {
    if (!confirm('删除该空部门？')) return
    const r = await fetch('/api/v1/org/departments/' + id, { method: 'DELETE', headers: AUTH() })
    const j = await r.json(); MessagePlugin.info(j.message || ''); loadTree()
  }

  /**
   * 启用/停用成员账号（状态取反）
   * 仅管理员类角色可见此按钮
   */
  async function toggleU(id: number, st: number) {
    await fetch('/api/v1/org/users/' + id, { method: 'PUT', headers: AUTH(), body: JSON.stringify({ status: st === 1 ? 0 : 1 }) })
    loadUsers()
  }

  /** 打开新增成员弹窗并重置表单 */
  function openUser() { setU({ username: '', password: '', real_name: '', role: 'user', department_id: 0 }); setUserDlg(true) }

  /**
   * 提交新增成员
   * 成功后关闭弹窗并刷新成员与部门树（更新成员计数）
   */
  async function submitUser() {
    const r = await fetch('/api/v1/org/users', { method: 'POST', headers: AUTH(), body: JSON.stringify(u) })
    const j = await r.json(); MessagePlugin.info(j.message || '')
    if (j.code === 0) { setUserDlg(false); loadUsers(); loadTree() }
  }

  // 未登录时不渲染
  if (!getToken()) return null

  return (
    <div style={{ background: '#f5f7fa', padding: 22, minHeight: '100vh', color: '#2d3748' }}>
      {/* 左右分栏：部门树 + 成员列表 */}
      <div style={{ display: 'grid', gridTemplateColumns: 'minmax(280px,38%) 1fr', gap: 18, maxWidth: 1200, margin: '0 auto' }}>
        {/* 左侧：部门树面板 */}
        <div style={panel}>
          <div style={top}><h3>部门树</h3><div><Button size="small" theme="primary" onClick={() => openDept('add')}>＋新建根部门</Button>　<button onClick={() => location.href = '/'} style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--pri)' }}>首页</button></div></div>
          <div>{renderNodes(tree, 0)}</div>
        </div>
        {/* 右侧：成员列表面板 */}
        <div style={panel}>
          <div style={top}><h3>成员 · <span>{curDeptName}</span></h3><Button size="small" theme="primary" onClick={openUser}>＋添加成员</Button></div>
          <table style={{ width: '100%', borderCollapse: 'collapse' }}>
            <thead><tr style={{ borderBottom: '1px solid #edf2f7' }}><th style={th}>账号</th><th style={th}>姓名</th><th style={th}>角色</th><th style={th}>部门</th><th style={th}>状态</th></tr></thead>
            <tbody>
              {users.length === 0 && <tr><td colSpan={5} style={{ ...td, color: '#a0aec0' }}>暂无成员</td></tr>}
              {users.map((x) => (
                <tr key={x.id}>
                  <td style={td}>{x.username}</td><td style={td}>{x.real_name || '-'}</td><td style={td}>{ROLE_CN[x.role] || x.role}</td><td style={td}>{x.dept_name || '-'}</td>
                  <td style={td}>{x.status === 1 ? '正常' : <span style={{ color: '#e53e3e' }}>禁用</span>}
                    {/* 仅管理员/超管/部门管理员可见启停按钮，普通成员与只读无此权限 */}
                    {(ROLE === 'tenant_admin' || ROLE === 'super_admin' || ROLE === 'dept_admin') && <button style={miniBtn} onClick={() => toggleU(x.id, x.status)}>{x.status === 1 ? '停用' : '启用'}</button>}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>

      {/* 部门操作弹窗：新建/重命名/移动 */}
      <Dialog header={dlg?.title} visible={!!dlg} onClose={() => setDlg(null)} onConfirm={submitDept} confirmBtn="确 定">
        {dlg?.mode === 'move'
          ? <><label style={lab}>目标父部门</label><Select value={fParent ?? undefined} options={deptOptions(dlg.id ?? undefined)} onChange={(v) => setFParent(v as number)} placeholder="选择父部门" /></>
          : <><label style={lab}>部门名称</label><Input value={fName} onChange={(v) => setFName(v)} placeholder="部门名称" />
            {dlg?.mode === 'add' && !ROLE.includes('dept_admin') && <><label style={lab}>父部门（留空=根）</label><Select value={fParent ?? undefined} options={[{ label: '— 根 —', value: 0 }, ...deptOptions()]} onChange={(v) => setFParent(v === 0 ? null : (v as number))} placeholder="根部门" /></>}</>}
      </Dialog>

      {/* 新增成员弹窗 */}
      <Dialog header="添加成员" visible={userDlg} onClose={() => setUserDlg(false)} onConfirm={submitUser} confirmBtn="创 建">
        <div style={{ display: 'grid', gap: 10 }}>
          <Input label="用户名" value={u.username} onChange={(v) => setU({ ...u, username: v })} />
          <Input label="初始密码" type="password" value={u.password} onChange={(v) => setU({ ...u, password: v })} />
          <Input label="姓名" value={u.real_name} onChange={(v) => setU({ ...u, real_name: v })} />
          <Select label="角色" value={u.role} options={[{ label: '普通成员', value: 'user' }, { label: '部门管理员', value: 'dept_admin' }, ...(ROLE !== 'dept_admin' ? [{ label: '只读', value: 'readonly' }] : [])]} onChange={(v) => setU({ ...u, role: v as string })} />
          <Select label="所属部门" value={u.department_id || undefined} options={[{ label: '未分配', value: 0 }, ...depts.map((d) => ({ label: d.name, value: d.id }))]} onChange={(v) => setU({ ...u, department_id: v as number })} />
        </div>
      </Dialog>
    </div>
  )

  /**
   * 递归渲染部门树节点
   * 点击切换选中状态、按角色展示增删改移动按钮
   * @param ns - 当前层级部门列表
   * @param depth - 当前层级深度（控制缩进）
   */
  function renderNodes(ns: Dept[], depth: number) {
    return ns.map((n) => (
      <div key={n.id}>
        <div onClick={() => { setSel(n.id); setCurDeptName(n.name); }} style={{ padding: '6px 8px', borderRadius: 6, display: 'flex', alignItems: 'center', gap: 8, cursor: 'pointer', marginLeft: depth * 20, background: sel === n.id ? '#e6ecff' : undefined }}>
          <span style={{ flex: 1 }}>{n.name}{depth === 0 && ' 🏠'}</span>
          <span style={{ color: '#718096', fontSize: 12 }}>{n.user_count}人</span>
          <span style={{ display: 'flex', gap: 4 }}>
            <button style={op} title="添加子部门" onClick={(e) => { e.stopPropagation(); openDept('add', n.id) }}>＋子</button>
            <button style={op} title="重命名" onClick={(e) => { e.stopPropagation(); openDept('rename', n.id, n.name) }}>✎</button>
            {/* 移动按钮仅超管和租户管理员可见 */}
            {(ROLE === 'tenant_admin' || ROLE === 'super_admin') && <button style={op} title="移动" onClick={(e) => { e.stopPropagation(); openDept('move', n.id) }}>⇄</button>}
            <button style={op} title="删除" onClick={(e) => { e.stopPropagation(); delDept(n.id) }}>🗑</button>
          </span>
        </div>
        {/* 递归渲染子部门 */}
        {n.children && n.children.length > 0 && renderNodes(n.children, depth + 1)}
      </div>
    ))
  }
}

// 面板容器样式
const panel: React.CSSProperties = { background: '#fff', borderRadius: 10, padding: 18, boxShadow: '0 3px 14px rgba(0,0,0,.06)' }
// 面板标题行样式（标题 + 操作按钮左右排布）
const top: React.CSSProperties = { display: 'flex', justifyContent: 'space-between', marginBottom: 14 }
// 表头单元格样式
const th: React.CSSProperties = { padding: '8px 10px', fontSize: 13, textAlign: 'left', borderBottom: '1px solid #edf2f7' }
// 表格数据单元格样式
const td: React.CSSProperties = { padding: '8px 10px', fontSize: 13, textAlign: 'left', borderBottom: '1px solid #edf2f7' }
// 部门树节点上的小操作按钮样式
const op: React.CSSProperties = { marginLeft: 4, border: 'none', background: 'none', cursor: 'pointer', fontSize: 12, color: 'var(--pri)' }
// 成员行的启停小按钮样式
const miniBtn: React.CSSProperties = { marginLeft: 8, border: 'none', background: 'none', color: 'var(--pri)', cursor: 'pointer' }
// 弹窗内字段标签样式
const lab: React.CSSProperties = { display: 'block', fontSize: 13, margin: '10px 0 5px' }
