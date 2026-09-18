// 实时推送 hook（P1-2，2026-08-29）
// WS 仅推送"有新消息"信号，收到即触发已有拉取；既有 5s 轮询保留为兜底，保证最终一致。
import { useEffect, useRef } from 'react'
import { TOKEN_KEY } from './api'

/**
 * 根据当前页面协议拼接 ws 地址（站同域，SPA 托管）
 * @param path - WebSocket 路径（如 /api/v1/ws/advisor）
 * @returns 完整的 WebSocket URL（ws:// 或 wss://）
 */
/** 把相对 WS 路径转换为当前浏览器 origin 下的 WebSocket URL。 */
function wsURL(path: string): string {
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${proto}//${location.host}${path}`
}

// WS 事件处理函数类型：收到消息后回调
type EvHandler = (ev: any) => void

/**
 * P2-7(2026-09-19 审计批三)：统一重连内核——旧版注释称"自动重连"但全文件无实现
 * （断线即哑火，纯靠页面轮询兜底）。现补指数退避重连：1s→2→4→…→30s 封顶，
 * 连上即清零退避；组件卸载/页面 unload 停止重连，不留僵尸定时器。
 * 返回释放函数供 useEffect cleanup；轮询兜底保留（WS 恢复前页面不瞎）。
 */
function connectWS(url: () => string, onMessage: (data: unknown) => void): () => void {
  let ws: WebSocket | null = null
  let timer: number | null = null
  let attempt = 0
  let stopped = false

  const open = () => {
    if (stopped) return
    try {
      ws = new WebSocket(url())
    } catch {
      schedule() // 构造失败（如断网）同样进退避重连
      return
    }
    ws.onopen = () => { attempt = 0 }
    ws.onmessage = (e) => {
      try {
        onMessage(JSON.parse(e.data))
      } catch {
        /* 忽略坏帧 */
      }
    }
    ws.onclose = schedule
    ws.onerror = () => {
      try {
        ws?.close() // onerror 后必跟 onclose，统一由 onclose 走重连
      } catch {
        /* noop */
      }
    }
  }
  const schedule = () => {
    if (stopped) return
    const delay = Math.min(1000 * 2 ** attempt, 30000)
    attempt += 1
    timer = window.setTimeout(open, delay)
  }
  const stop = () => {
    stopped = true
    if (timer !== null) {
      clearTimeout(timer)
      timer = null
    }
    window.removeEventListener('pagehide', stop)
    try {
      ws?.close()
    } catch {
      /* noop */
    }
  }
  open()
  // 页面卸载（关闭/跳转）不再重连；SPA 内路由卸载走 effect cleanup
  window.addEventListener('pagehide', stop)
  return stop
}

/**
 * 顾问端 WebSocket hook：连接 /api/v1/ws/advisor，接收新消息推送
 * token 从 localStorage 读取，用于身份校验
 * 断线指数退避自动重连（P2-7），5s 轮询仍保留兜底
 * @param onEvent - 收到 WS 消息时的回调函数
 */
/** 顾问端 WS Hook：自动重连并把新消息/状态事件回抛给页面。 */
export function useAdvisorWS(onEvent: EvHandler) {
  const ref = useRef(onEvent)
  ref.current = onEvent
  useEffect(() => {
    const token = localStorage.getItem(TOKEN_KEY) || ''
    if (!token) return
    return connectWS(
      () => wsURL(`/api/v1/ws/advisor?token=${encodeURIComponent(token)}`),
      (data) => ref.current(data),
    )
  }, [])
}

/**
 * C 端客户 WebSocket hook：连接 /api/v1/ws/client，接收新消息推送
 * 使用 visitor_key + customer_id 双重身份校验（防越权）
 * 断线指数退避自动重连（P2-7），轮询仍保留兜底
 * @param customerId - 客户 ID（数字）
 * @param visitorKey - 访客密钥（字符串，用于防横向越权）
 * @param onEvent - 收到 WS 消息时的回调函数
 */
/** 客户端 WS Hook：按 customer_id 与 visitor_key 订阅自身消息。 */
export function useClientWS(customerId: number | null, visitorKey: string | null, onEvent: EvHandler) {
  const ref = useRef(onEvent)
  ref.current = onEvent
  useEffect(() => {
    if (!customerId || !visitorKey) return
    return connectWS(
      () => wsURL(`/api/v1/ws/client?customer_id=${customerId}&visitor_key=${encodeURIComponent(visitorKey)}`),
      (data) => ref.current(data),
    )
  }, [customerId, visitorKey])
}
