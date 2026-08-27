// 本地存储键名：鉴权 token（用于 localStorage 持久化登录态）
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

// 底层请求封装：自动拼接 JSON 头，并在存在 token 时附加 Authorization: Bearer
export async function apiFetch(url: string, opts: RequestInit = {}): Promise<Response> {
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    ...(opts.headers as Record<string, string>),
  }
  const tk = getToken()
  if (tk) headers['Authorization'] = 'Bearer ' + tk
  return fetch(url, { ...opts, headers })
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
