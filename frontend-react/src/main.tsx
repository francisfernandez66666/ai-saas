// 应用入口：挂载 React 根节点，提供全局 BrowserRouter（路由）与 BrandingProvider（按域名拉取租户白标）
// ErrorBoundary 兜底渲染期异常，避免整页白屏
import React from 'react'
import ReactDOM from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
import App from './App'
import { BrandingProvider } from './lib/branding'
import { I18nProvider } from './i18n/I18nProvider'
import { ErrorBoundary } from './components/ErrorBoundary'
import { installGlobalErrorHandlers, sentryBridge } from './lib/errorReport'
import 'tdesign-react/es/style/index.css'
import './index.css'

installGlobalErrorHandlers()
// E2(2026-09-19)：VITE_SENTRY_DSN 配置时才拉 Sentry chunk（内置全局 error/unhandledrejection
// 采集）；未配置 sentryBridge() 返回 null，本行等价 no-op，主轨 /client-errors 不变
void sentryBridge()

// 应用入口：挂载 React 根节点，外层包 BrowserRouter（路由）与 BrandingProvider（按域名拉取租户白标）
// ErrorBoundary 兜底渲染异常，避免整页白屏
ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    {/* BrowserRouter：全局路由提供者，所有页面共享同一路由上下文 */}
    <BrowserRouter>
      {/* I18nProvider：语言单一事实源（localStorage > navigator > 出厂中文），全站 t() 经此下发 */}
      <I18nProvider>
        {/* BrandingProvider：按当前域名拉取租户白标配置（品牌名/Logo/主题色等），通过 Context 下发 */}
        <BrandingProvider>
          {/* ErrorBoundary：捕获渲染期异常，展示兜底 UI 而非整页白屏 */}
          <ErrorBoundary>
            <App />
          </ErrorBoundary>
        </BrandingProvider>
      </I18nProvider>
    </BrowserRouter>
  </React.StrictMode>,
)
