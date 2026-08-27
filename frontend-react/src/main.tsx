// 应用入口：挂载 React 根节点，提供全局 BrowserRouter（路由）与 BrandingProvider（按域名拉取租户白标）
// ErrorBoundary 兜底渲染期异常，避免整页白屏
import React from 'react'
import ReactDOM from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
import App from './App'
import { BrandingProvider } from './lib/branding'
import ErrorBoundary from './lib/ErrorBoundary'
import 'tdesign-react/es/style/index.css'
import './index.css'

// 应用入口：挂载 React 根节点，外层包 BrowserRouter（路由）与 BrandingProvider（按域名拉取租户白标）
// ErrorBoundary 兜底渲染异常，避免整页白屏
ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <BrowserRouter>
      <BrandingProvider>
        <ErrorBoundary>
          <App />
        </ErrorBoundary>
      </BrandingProvider>
    </BrowserRouter>
  </React.StrictMode>,
)
