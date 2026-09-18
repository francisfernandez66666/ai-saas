// 企微侧边栏 JS-SDK 装配（§八-6 D 块，2026-09-18）：按需注入 jweixin + jsconfig 企业签名 + wx.config。
// 依赖接口：GET /api/v1/channel/wecom/jsconfig?url=&corpid=（登录态，返回 {corpid,timestamp,noncestr,signature}）。
// 红线：任何失败只 console.warn 并返回 false，绝不打断页面渲染；非企微环境（含 jsdom 测试）静默跳过。
import { AUTH } from './api'

// 企微注入的 wx 对象最小声明（只用到 config，不引全量 jweixin 类型包）
declare global {
  interface Window {
    wx?: { config: (c: Record<string, unknown>) => void }
  }
}

// jsconfig 响应 data 形态（对齐 channel.JSCfgResult json tag：corpid/timestamp/noncestr/signature）
type WecomJsCfg = { corpid: string; timestamp: string; noncestr: string; signature: string }
type JsCfgResp = { code?: number; message?: string; data?: WecomJsCfg | null }

// jweixin 官方 CDN（CSP script-src 已定点放行 https://res.wx.qq.com，见 cmd/server/main.go）
const JWEIXIN_URL = 'https://res.wx.qq.com/open/js/jweixin-1.6.0.js'

// 模块级 Promise 缓存：并发/重复调用只注入一次 script
let sdkLoader: Promise<boolean> | null = null

// loadJweixin 确保 window.wx 可用：已存在直接 true；否则动态注入，onload 复查/onerror 兜底 false
function loadJweixin(): Promise<boolean> {
  if (typeof window === 'undefined') return Promise.resolve(false)
  if (window.wx) return Promise.resolve(true)
  if (!sdkLoader) {
    sdkLoader = new Promise<boolean>((resolve) => {
      const el = document.createElement('script')
      el.src = JWEIXIN_URL
      el.onload = () => resolve(Boolean(window.wx))
      // 注入失败（离线/CSP 拦截/无网络）不 reject，统一 false 由调用方降级
      el.onerror = () => resolve(false)
      document.head.appendChild(el)
    })
  }
  return sdkLoader
}

/**
 * 装配企微 JS-SDK：注入 jweixin（若缺）→ 拉 jsconfig 签名 → wx.config。
 * @param corpid - 侧边栏 URL 上下文里的企微企业 ID
 * @returns 是否装配成功；失败仅告警不抛出
 */
export async function setupWecomJsSdk(corpid: string): Promise<boolean> {
  try {
    if (!corpid) return false
    if (!(await loadJweixin())) return false
    // 签名 URL 须去掉 hash 段（与企微端计算口径一致）
    const target = location.href.split('#')[0]
    const j = await AUTH<JsCfgResp>('/api/v1/channel/wecom/jsconfig?url=' + encodeURIComponent(target) + '&corpid=' + encodeURIComponent(corpid))
    const cfg = j?.data
    if (j?.code !== 0 || !cfg?.signature || !window.wx) return false
    window.wx.config({
      beta: true, // 企微第三方/自建应用需 beta 模式才能拿到企业级接口
      appId: cfg.corpid,
      timestamp: cfg.timestamp,
      nonceStr: cfg.noncestr,
      signature: cfg.signature,
      // 侧边栏常用三件套：选外部联系人 / 发起会话 / 分享设置
      jsApiList: ['selectExternalContact', 'openEnterpriseChat', 'updateTimelineShareData'],
    })
    return true
  } catch (e) {
    // 装配属增强能力：失败不影响顾问侧边栏正常看客户/聊天
    console.warn('[wecom] JS-SDK 装配失败（非企微环境可忽略）', e)
    return false
  }
}
