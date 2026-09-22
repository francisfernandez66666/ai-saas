// 前端统一请求层：封装 localStorage 鉴权 token 读写、fetch 包装、角色分流与 401/403 统一处理
// 401：登录态失效→清 token 跳登录页；403：后端强改密拦截→已登录用户跳 /login?mcp=1 改密
import { MessagePlugin } from 'tdesign-react'
// P2-8c：消费自动生成的契约类型（api.d.ts 的 ApiPath / ApiResponse / ApiRoutes），使 AUTH 可按路径泛型分发响应信封
import type { ApiResp } from '../types'
import type { ApiPath, ApiResponse, ApiRoutes } from '../types/api'
// P2-8a：角色字面量统一引用单一事实源，避免与后端 model.RoleXxx 失配
import { ROLES } from './roles'

// P2-8c 按路径分发的响应信封：以 api.d.ts 的 ApiRoutes 映射为单一事实源。
// ApiRoutes 的键为 "METHOD /path"（生成器口径），调用点只写纯路径，这里用模板字面量
// 后缀匹配抽出该路径在所有方法下的 data 类型：
//   - 命中已标注 data 类型的端点 → 精确类型（同路径多方法时取联合）；
//   - 未标注（unknown）/ 路径未登记 → 退化为 any，避免 unknown 属性访问编译报错。
// 调用点写法：AUTH<ApiEnvelope<'/api/v1/customers'>>(url)
type RouteData<P extends string> = ApiRoutes[Extract<keyof ApiRoutes, `${string} ${P}`>]

// ApiEnvelope 按纯路径取响应信封：命中等价于 ApiResp<RouteData<P>>，
// 路径未登记（never）或 data 未标注（unknown）时统一退化为 ApiResp<any>，
// 保证既有调用点的属性访问在 tsc 下不报错、已标注端点获得精确类型。
export type ApiEnvelope<P extends string> = [RouteData<P>] extends [never]
  ? ApiResp<any>
  : RouteData<P> extends object
    ? ApiResp<RouteData<P>>
    : ApiResp<any>

// 重新导出契约类型，使 api.d.ts 在业务代码中被真正消费（重构前零 import）
export type { ApiPath, ApiResponse }

// 本地存储里放登录 token 的键名
// P2-87 修复：导出供 realtime.ts 复用，避免字面量重复（曾硬编码 'scrm_auth_token'）
export const TOKEN_KEY = 'scrm_auth_token'

// 业务错误码 → 用户提示（P1-4：按后端 error_code 统一 toast，去 AI 味短句）
const ERR_MSG: Record<string, string> = {
  param_error: '参数填错了，麻烦核对一下',
  unauthorized: '账号或密码不对',
  forbidden: '没有权限操作',
  not_found: '没找到对应的内容',
  rate_limited: '操作太频繁，稍后再试',
  biz_error: '操作没成功',
  internal_error: '服务开小差了，稍后再试',
}

/**
 * 按后端返回体给出友好提示（error_code 优先，其次 message）
 * @param json - 后端返回的 JSON 响应体
 * @returns 是否业务失败（code !== 0 表示失败）
 */
/** 按后端 code/message 弹出错误提示，返回是否已处理。 */
export function toastError(json: any): boolean {
  if (!json || json.code === 0 || json.code === undefined) return false
  const code = json.error_code as string
  const msg = (code && ERR_MSG[code]) || json.message || '操作没成功'
  MessagePlugin.warning(msg)
  return true
}

/**
 * 读取当前登录 token，缺失返回空串
 * @returns 登录 token 字符串
 */
/** 读取本地保存的登录 token。 */
export function getToken(): string {
  return localStorage.getItem(TOKEN_KEY) || ''
}

/**
 * 写入登录 token 到 localStorage
 * @param t - 登录 token
 */
/** 保存登录 token 到本地存储。 */
export function setToken(t: string) {
  localStorage.setItem(TOKEN_KEY, t)
  // P1 修复(2026-09-18)：换新 token 必须作废 /auth/me 60s 缓存——
  // 否则"登出→换账号登录"在缓存窗口内仍拿旧身份（角色/租户纠偏全部失效）
  invalidateSession()
}

/**
 * 清除登录 token（退出登录时调用）
 */
/** 清理本地登录 token。 */
export function clearToken() {
  localStorage.removeItem(TOKEN_KEY)
  invalidateSession() // P1-50：清会话缓存，防登出后残留旧 me 身份
}

// 访客身份键（C 端客户本地持久化，退出登录不得清除——P2-85）
export const VISITOR_KEY = 'scrm_visitor_key'

// E4 修复(2026-09-14)：super_admin 进 /admin 需带 X-Tenant-ID（租户作用域路径强制显式租户）。
// 超管在 Admin 入口的"租户选择器"选定租户后写入此键，apiFetch 统一注入；退出/切换时清除。
export const IMPERSONATE_TENANT_KEY = 'scrm_impersonate_tenant'
/** 读取当前超管选定的代管租户 ID（无则空串）。 */
export function getImpersonateTenant(): string {
  return localStorage.getItem(IMPERSONATE_TENANT_KEY) || ''
}
/** 写入/清除代管租户 ID（传空值即清除，退出或切回平台视图时调用）。 */
export function setImpersonateTenant(id: string | number) {
  if (id === '' || id == null) localStorage.removeItem(IMPERSONATE_TENANT_KEY)
  else localStorage.setItem(IMPERSONATE_TENANT_KEY, String(id))
}

// authHeaders E4 修复(2026-09-14)：统一鉴权请求头 helper——
// 供 Admin 各 Tab 裸 fetch 复用，自动附带 token 与（超管代管时）X-Tenant-ID，
// 避免逐处手写 Authorization 而漏带租户头导致 400。
export function authHeaders(extra: Record<string, string> = {}): Record<string, string> {
  const h: Record<string, string> = { Authorization: 'Bearer ' + getToken(), ...extra }
  const imp = getImpersonateTenant()
  if (imp && localStorage.getItem('role') === ROLES.superAdmin) h['X-Tenant-ID'] = imp
  return h
}

/**
 * P2-85 修复：统一退出登录 helper。
 * 原来 Admin/SuperAdmin/Advisor 直接 localStorage.clear() 连 C 端 visitor_key 一起清，
 * 导致访客身份丢失。改为白名单清除：只删登录相关键，保留访客身份。
 */
/** 退出登录并跳转到角色对应的登录入口。 */
export function logoutAndRedirect() {
  clearToken()
  // 按需清理其它登录态相关键（如有），绝不碰 visitor_key
  localStorage.removeItem('remember_username')
  location.href = '/login'
}

/**
 * 401 统一处理：登录态失效（token 过期/被踢）时清掉本地 token 并跳登录页
 * 注意：已在登录/注册页时不重复跳转
 */
/** 统一处理 401：清 token 并跳回登录页。 */
function handleUnauthorized() {
  clearToken()
  // 避免重复跳转（已在登录/注册页时不跳）
  // P1 修复(2026-09-18)：移动端 SPA（/app/*）401 应回 /app/login 而非桌面 /login，
  // 旧逻辑一律跳 /login 会把 App 用户甩出移动端路由树
  if (location.pathname.startsWith('/app')) {
    if (!location.pathname.startsWith('/app/login')) location.href = '/app/login'
  } else if (location.pathname !== '/login') {
    location.href = '/login'
  }
}

/**
 * 底层请求封装：自动拼接 JSON 头，并在存在 token 时附加 Authorization: Bearer
 * 同时处理 401（登录态失效）和 403（强制改密拦截）状态码
 * @param url - 请求地址
 * @param opts - fetch 配置项
 * @returns 原始 Response 对象
 */
// isPlatformPath 判断是否平台级路径（super 无需 X-Tenant-ID 的豁免路径），与后端 isPlatformSuperPath 对齐。
function isPlatformPath(url: string): boolean {
  const p = url.split('?')[0]
  if (p.startsWith('/api/v1/super') || p.startsWith('/api/v1/auth')) return true
  if (p.startsWith('/api/v1/admin/config') && p !== '/api/v1/admin/config/rollback') return true
  return false
}

// P1-13 修复(2026-09-15)：默认无超时的 fetch 在后端挂死（网关抖动/AI 长阻塞）时
// 会让按钮永远转圈、loading 态永不复位。统一加 30s 超时；
// 长任务（如聊天等待合并窗口）可传 timeoutMs:0 关闭，或调大。
export type ApiRequestInit = RequestInit & { timeoutMs?: number }
const DEFAULT_TIMEOUT_MS = 30000

/** 带 token 和租户上下文发起 fetch，保留原始 Response 供上层处理。 */
export async function apiFetch(url: string, opts: ApiRequestInit = {}): Promise<Response> {
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    ...(opts.headers as Record<string, string>),
  }
  const tk = getToken()
  if (tk) headers['Authorization'] = 'Bearer ' + tk
  // E4：超管代管租户上下文——仅对**租户作用域**路径注入 X-Tenant-ID，平台路径(/super/*、/auth/me)不带
  const imp = getImpersonateTenant()
  if (imp && localStorage.getItem('role') === ROLES.superAdmin && !isPlatformPath(url)) headers['X-Tenant-ID'] = imp
  const { timeoutMs, ...fetchOpts } = opts
  let timer: ReturnType<typeof setTimeout> | undefined
  let signal = fetchOpts.signal
  if (!signal && timeoutMs !== 0) {
    const ctrl = new AbortController()
    signal = ctrl.signal
    timer = setTimeout(() => ctrl.abort(), timeoutMs || DEFAULT_TIMEOUT_MS)
  }
  let res: Response
  try {
    res = await fetch(url, { ...fetchOpts, signal, headers })
  } finally {
    if (timer) clearTimeout(timer)
  }
  if (res.status === 401) {
    handleUnauthorized()
  } else if (res.status === 403) {
    // P1-43 修复：403 来源不复原只有"必须改密"——AdminRequired/TenantConsistency/配额拦截
    // 都是 403。旧逻辑一律跳 /login?mcp=1＝误甩且丢当前页。现按响应 error_code 分流：
    //   仅 must_change_password → 跳改密页；其余 → toast 提示现在页停留。
    // 注意：未登录(无 token)时后端一般返回 401，403 通常已登录；token 存在才处理跳转。
    if (getToken()) {
      const json = await res.clone().json().catch(() => null)
      const code = json && (json.error_code as string)
      if (code === 'must_change_password') {
        const p = location.pathname
        const onAuthPage =
          p === '/login' || p === '/register' || p.startsWith('/app/login') || p.startsWith('/app/register')
        if (!onAuthPage) location.href = '/login?mcp=1'
      } else {
        toastError(json)
      }
    } else {
      toastError(null)
    }
  }
  return res
}

/**
 * 在 apiFetch 基础上解析 JSON 响应体（解析失败返回 null），返回 {res, json}
 * 泛型 T 为后端 data 字段类型，便于调用点标注 ApiResp<T>
 * @param url - 请求地址
 * @param opts - fetch 配置项
 * @returns 包含 Response 和解析后 JSON 的对象
 */
/** 发起 JSON API 请求并返回 data；错误码会转成前端异常。 */
// P1-6 修复(2026-09-20 审计批)：网络层失败（断网/DNS/abort）不再向上 reject——
// 统一返回 {res:null, json:null} 口径（同 AUTH 的 E6 网络兜底语义），
// 调用方 setLoading(false)/判错逻辑单线化，不再因 unhandled rejection 卡死在 loading。
export async function apiJSON<T = any>(
  url: string,
  opts: ApiRequestInit = {},
): Promise<{ res: Response | null; json: T | null }> {
  let res: Response
  try {
    res = await apiFetch(url, opts)
  } catch {
    // apiFetch 抛错仅剩网络层/超时一种（4xx/5xx 是正常 Response）——归一为空结果
    return { res: null, json: null }
  }
  const json = (await res.json().catch(() => null)) as T
  return { res, json }
}

/**
 * 角色分流：根据用户角色跳转到对应的工作台页面
 * - super_admin → /super（平台超管后台）
 * - tenant_admin/admin → /admin（租户管理员后台）
 * - 其他角色 → /advisor（顾问工作台）
 * @param role - 用户角色标识
 */
/** 按用户角色跳转到对应工作台，避免登录后落在错误页面。 */
export function redirectByRole(role: string) {
  if (role === 'super_admin') location.href = '/super'
  else if (role === 'tenant_admin' || role === 'admin') location.href = '/admin'
  else location.href = '/advisor'
}

/**
 * 鉴权请求：自动带 token 与 JSON 头；body 传对象会自动 JSON.stringify
 * 泛型 T 标注后端返回的 data 类型
 * @param url - 请求地址
 * @param opts - 请求配置（method/body/headers）
 * @returns 后端返回的 JSON 响应体（已调用 toastError 处理业务错误）
 */
/** 调用需要鉴权的后台接口，并复用统一错误和登录态处理。 */
export async function AUTH<T = any>(
  url: string,
  opts: { method?: string; body?: any; headers?: Record<string, string>; timeoutMs?: number } = {},
): Promise<T> {
  // E6 修复(2026-09-14)：网络异常/超时不再让 AUTH reject——旧行为下调用方
  // `const j = await AUTH(...); j.code` 直接踩 unhandled rejection → 白屏。
  // 统一兜底成 {code:-1, message} 错误信封，让调用方的 `if (j.code === 0)` 正常走 else 分支。
  // P1-6 配套(2026-09-20)：apiJSON 已把网络层失败归一为 {res:null, json:null}（不再 throw），
  // 故按 res 是否为空区分"网络异常"与"响应解析失败"两种兜底文案。
  const { res, json: parsed } = await apiJSON<T>(url, {
    method: opts.method || 'GET',
    headers: opts.headers,
    body: opts.body ? JSON.stringify(opts.body) : undefined,
    timeoutMs: opts.timeoutMs, // P1-13：长任务可显式放宽/关闭超时
  })
  let json: T = parsed
  if (!json || typeof (json as any).code !== 'number') {
    json = { code: -1, message: res ? '响应解析失败' : '网络异常，请稍后重试' } as unknown as T
  }
  toastError(json) // P1-4：业务失败按 error_code 统一轻提示（不阻断调用方读取 json）
  return json
}

// ============================================================
// P1-50 会话校验（2026-09-09）
// localStorage role 可被篡改（App.tsx 旧守卫只查 token 存在），凭据角色必须以 /auth/me 为准。
// 60s 缓存避免每路由切换都打后端；命中缓存直接使用 me 响应里的角色/用户名（并回写 localStorage 纠偏篡改值）。
// ============================================================

// MeInfo /auth/me data 结构
export type MeInfo = {
  id: number
  username: string
  role: string
  tenant_id: number
  email?: string
  must_change_password?: boolean
}

// me 会话缓存：{ info, ts }，ts 秒级时间戳
let meCache: { info: MeInfo; ts: number } | null = null
const ME_CACHE_TTL = 60 // 秒

/**
 * 校验当前登录态并返回服务端权威身份（含角色）
 * - token 缺失 → 直接返回 null（由调用方决定跳登录）
 * - /auth/me 409/403/401 → 清 token 返回 null
 * - must_change_password 为 true → 返回 null（调用方跳改密页）
 * @returns 权威身份，未登录/失效/需改密返回 null
 */
/** 校验本地会话是否有效，并返回当前登录用户资料。 */
export async function verifySession(): Promise<MeInfo | null> {
  const tk = getToken()
  if (!tk) return null
  const now = Math.floor(Date.now() / 1000)
  if (meCache && now - meCache.ts < ME_CACHE_TTL) {
    // 命中缓存：顺便把篡改的 localStorage role/username 纠偏回权威值
    localStorage.setItem('role', meCache.info.role)
    localStorage.setItem('username', meCache.info.username)
    return meCache.info
  }
  try {
    const res = await fetch('/api/v1/auth/me', {
      headers: { Authorization: 'Bearer ' + tk },
    })
    if (res.status === 401 || res.status === 403) {
      clearToken()
      meCache = null
      return null
    }
    const json = await res.json().catch(() => null)
    if (!json || json.code !== 0 || !json.data) {
      clearToken()
      meCache = null
      return null
    }
    const info = json.data as MeInfo
    if (info.must_change_password) {
      meCache = null
      return null // 调用方跳 /login?mcp=1
    }
    // 角色/用户名以服务端为准（纠偏 localStorage）
    localStorage.setItem('role', info.role)
    localStorage.setItem('username', info.username)
    meCache = { info, ts: now }
    return info
  } catch {
    // 网络异常：保守返回 null（用户可能离网），交由调用方跳登录
    meCache = null
    return null
  }
}

/** 使 /auth/me 缓存失效（登录/登出/改密后应调用） */
export function invalidateSession() {
  meCache = null
}
