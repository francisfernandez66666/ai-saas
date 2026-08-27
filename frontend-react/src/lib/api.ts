// 前端统一请求层：封装 localStorage 鉴权 token 读写、fetch 包装、角色分流与 401/403 统一处理
// 401：登录态失效→清 token 跳登录页；403：后端强改密拦截→已登录用户跳 /login?mcp=1 改密
const TOKEN_KEY = 'scrm_auth_token'

// 读取当前登录 token，缺失返回空串
export function getToken(): string {
  return localStorage.getItem(TOKEN_KEY) || ''
}
// 写入登录 token 到 localStorage
export function setToken(t: string) {
  localStorage.setItem(TOKEN_KEY, t)
}
// 清除登录 token（退出登录时调用）
export function clearToken() {
  localStorage.removeItem(TOKEN_KEY)
}

// 401 统一处理：登录态失效（token 过期/被踢）时清掉本地 token 并跳登录页
function handleUnauthorized() {
  clearToken()
  // 避免重复跳转（已在登录/注册页时不跳）
  if (location.pathname !== '/login' && !location.pathname.startsWith('/app/login')) {
    location.href = '/login'
  }
}

// 底层请求封装：自动拼接 JSON 头，并在存在 token 时附加 Authorization: Bearer
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
    // 后端 MustChangePasswordGuard 对未改密用户拦截所有受保护路由（返回403）。
    // 已登录用户被拦时，跳登录页并带 mcp=1 直接展示改密表单（change-password 在白名单内，不受拦截）。
    if (getToken()) {
      const p = location.pathname
      const onAuthPage = p === '/login' || p === '/register' || p.startsWith('/app/login') || p.startsWith('/app/register')
      if (!onAuthPage) {
        location.href = '/login?mcp=1'
      }
    }
  }
  return res
}

// 在 apiFetch 基础上解析 JSON 响应体（解析失败返回 null），返回 {res, json}
export async function apiJSON(
  url: string,
  opts: RequestInit = {},
): Promise<{ res: Response; json: any }> {
  const res = await apiFetch(url, opts)
  const json = await res.json().catch(() => null)
  return { res, json }
}

// 角色分流：与旧前端一致
export function redirectByRole(role: string) {
  if (role === 'super_admin') location.href = '/super'
  else if (role === 'tenant_admin' || role === 'admin') location.href = '/admin'
  else location.href = '/advisor'
}

// 鉴权请求：自动带 token 与 JSON 头；body 传对象会自动 JSON.stringify
export async function AUTH(
  url: string,
  opts: { method?: string; body?: any; headers?: Record<string, string> } = {},
): Promise<any> {
  const { json } = await apiJSON(url, {
    method: opts.method || 'GET',
    headers: opts.headers,
    body: opts.body ? JSON.stringify(opts.body) : undefined,
  })
  return json
}
