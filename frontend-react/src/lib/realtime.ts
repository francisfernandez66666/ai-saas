// 实时推送 hook（P1-2，2026-08-29）
// WS 仅推送"有新消息"信号，收到即触发已有拉取；既有 5s 轮询保留为兜底，保证最终一致。
import { useEffect, useRef } from 'react'

// 根据当前页面协议拼接 ws 地址（站同域，SPA 托管）
function wsURL(path: string): string {
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${proto}//${location.host}${path}`
}

type EvHandler = (ev: any) => void

// 顾问端 WS：token 取自 localStorage
export function useAdvisorWS(onEvent: EvHandler) {
  const ref = useRef(onEvent)
  ref.current = onEvent
  useEffect(() => {
    const token = localStorage.getItem('scrm_auth_token') || ''
    if (!token) return
    let ws: WebSocket
    try {
      ws = new WebSocket(wsURL(`/api/v1/ws/advisor?token=${encodeURIComponent(token)}`))
    } catch {
      return
    }
    ws.onmessage = (e) => {
      try {
        ref.current(JSON.parse(e.data))
      } catch {
        /* 忽略坏帧 */
      }
    }
    // 断线不主动重连（轮询兜底）；onclose 仅释放
    return () => {
      try {
        ws.close()
      } catch {
        /* noop */
      }
    }
  }, [])
}

// C 端 WS：visitor_key + customer_id 双重身份（防越权）
export function useClientWS(customerId: number | null, visitorKey: string | null, onEvent: EvHandler) {
  const ref = useRef(onEvent)
  ref.current = onEvent
  useEffect(() => {
    if (!customerId || !visitorKey) return
    let ws: WebSocket
    try {
      ws = new WebSocket(
        wsURL(`/api/v1/ws/client?customer_id=${customerId}&visitor_key=${encodeURIComponent(visitorKey)}`)
      )
    } catch {
      return
    }
    ws.onmessage = (e) => {
      try {
        ref.current(JSON.parse(e.data))
      } catch {
        /* 忽略坏帧 */
      }
    }
    return () => {
      try {
        ws.close()
      } catch {
        /* noop */
      }
    }
  }, [customerId, visitorKey])
}
