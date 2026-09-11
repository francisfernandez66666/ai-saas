// 前端统一请求层：封装 localStorage 鉴权 token 读写、fetch 包装、角色分流与 401/403 统一处理
// 401：登录态失效→清 token 跳登录页；403：后端强改密拦截→已登录用户跳 /login?mcp=1 改密
import { MessagePlugin } from 'tdesign-react'

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
export function getToken(): string {
  return localStorage.getItem(TOKEN_KEY) || ''
}

/**
 * 写入登录 token 到 localStorage
 * @param t - 登录 token
 */
export function setToken(t: string) {
  localStorage.setItem(TOKEN_KEY, t)
}

/**
 * 清除登录 token（退出登录时调用）
 */
export function clearToken() {
  localStorage.removeItem(TOKEN_KEY)
  invalidateSession() // P1-50：清会话缓存，防登出后残留旧 me 身份
}

// 访客身份键（C 端客户本地持久化，退出登录不得清除——P2-85）
export const VISITOR_KEY = 'scrm_visitor_key'

/**
 * P2-85 修复：统一退出登录 helper。
 * 原来 Admin/SuperAdmin/Advisor 直接 localStorage.clear() 连 C 端 visitor_key 一起清，
 * 导致访客身份丢失。改为白名单清除：只删登录相关键，保留访客身份。
 */
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
function handleUnauthorized() {
  clearToken()
  // 避免重复跳转（已在登录/注册页时不跳）
  if (location.pathname !== '/login' && !location.pathname.startsWith('/app/login')) {
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
export async function apiFetch(url: string, opts: RequestInit = {}): Promise<Response> {
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    ...(opts.headers as Record<string, string>),
  }
  const tk = getToken()
  if (tk) headers['Authorization'] = 'Bearer ' + tk
  const res = await fetch(url, { ...opts, headers })
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
export async function apiJSON<T = any>(
  url: string,
  opts: RequestInit = {},
): Promise<{ res: Response; json: T }> {
  const res = await apiFetch(url, opts)
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
export async function AUTH<T = any>(
  url: string,
  opts: { method?: string; body?: any; headers?: Record<string, string> } = {},
): Promise<T> {
  const { json } = await apiJSON<T>(url, {
    method: opts.method || 'GET',
    headers: opts.headers,
    body: opts.body ? JSON.stringify(opts.body) : undefined,
  })
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
