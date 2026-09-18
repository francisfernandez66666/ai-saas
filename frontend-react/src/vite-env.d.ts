// Vite 客户端类型声明入口：引入 vite/client 以支持 import.meta.env 类型提示。
// 新增构建期环境变量（如 E2 的 VITE_SENTRY_DSN）在此补 interface，保证前端取值有类型。
/// <reference types="vite/client" />

interface ImportMetaEnv {
  /** Sentry/GlitchTip 上报 DSN；空=不加载 SDK，前端异常仅走 /client-errors 自建通道 */
  readonly VITE_SENTRY_DSN?: string
  /** 构建注入的应用版本，用于异常上报的 release 标签 */
  readonly VITE_APP_VERSION?: string
}

interface ImportMeta {
  readonly env: ImportMetaEnv
}
