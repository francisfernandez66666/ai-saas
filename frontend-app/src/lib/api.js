/**
 * API 请求统一封装
 * ----------------------------------------------------------------------------
 * - 自动拼接 API 基座：默认同源 /api/v1；
 *   Capacitor APK（模式B·内嵌壳）通过 public/config.js 注入 window.__API_BASE__
 *   指向部署服务器；模式A（远程壳）整站由服务端直出，无需本变量。
 * - 自动附带 Authorization: Bearer <token>（localStorage 共享键）。
 * - 401 统一处理：清除本地 token 并跳转登录页（防失效token循环请求）。
 */

const TOKEN_KEY = 'scrm_auth_token'

/** 读取本地 token */
export const getToken = () => localStorage.getItem(TOKEN_KEY) || ''

/** 写入/清除 token（传空串即清除） */
export const setToken = (t) => t ? localStorage.setItem(TOKEN_KEY, t) : localStorage.removeItem(TOKEN_KEY)

/**
 * 发起后端 API 请求
 * @param {string} path   以 / 开头的接口路径（不含 /api/v1 前缀）
 * @param {object} opts   { method, body } —— body 为对象时自动 JSON 序列化
 * @returns 后端原始响应结构 { code, message, data }
 */
export async function api(path, { method = 'GET', body } = {}) {
  // API 基座注入点：APK 内嵌模式下由 config.js 提供目标服务器地址
  const base = (typeof window !== 'undefined' && window.__API_BASE__) || ''
  const r = await fetch(base + '/api/v1' + path, {
    method,
    headers: {
      'Content-Type': 'application/json',
      ...(getToken() ? { Authorization: 'Bearer ' + getToken() } : {}),
    },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  })
  // 响应体解析失败时构造兜底结构，保证调用方拿到的永远是对象
  const j = await r.json().catch(() => ({ code: r.status, message: 'HTTP ' + r.status }))
  // 401 统一登出：清 token 回登录页（避免每个页面重复处理）
  if (r.status === 401 && getToken()) {
    setToken('')
    location.hash = '#/login'
  }
  return j
}

/** 手机号掩码展示：138****5678（非11位原样返回） */
export const maskPhone = (p) => p && p.length === 11 ? p.slice(0,3)+'****'+p.slice(7) : (p||'')
