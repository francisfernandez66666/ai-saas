import React from 'react'

// 顶层错误边界：捕获渲染期异常，避免整页白屏（生产环境给出可重试兜底）
export default class ErrorBoundary extends React.Component<
  { children: React.ReactNode },
  { hasError: boolean; message: string }
> {
  // 初始化错误状态
  constructor(props: { children: React.ReactNode }) {
    super(props)
    this.state = { hasError: false, message: '' }
  }

  // 渲染抛错时把错误收进 state，触发兜底 UI
  static getDerivedStateFromError(error: Error) {
    return { hasError: true, message: error.message || '未知错误' }
  }

  // 报错时打印到控制台，方便排查
  componentDidCatch(error: Error, info: React.ErrorInfo) {
    console.error('[ErrorBoundary] 捕获到渲染异常:', error, info)
  }

  // 出错展示可重试页面，否则正常渲染子节点
  render() {
    if (this.state.hasError) {
      return (
        <div style={{ padding: 32, textAlign: 'center', fontFamily: 'sans-serif' }}>
          <h2>页面出错了</h2>
          <p style={{ color: '#888' }}>{this.state.message}</p>
          <button
            onClick={() => location.reload()}
            style={{ padding: '8px 20px', cursor: 'pointer' }}
          >
            重新加载
          </button>
        </div>
      )
    }
    return this.props.children
  }
}
