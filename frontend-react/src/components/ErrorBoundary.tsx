// C6 前端异常边界：捕获 React 子树渲染错误，兜底展示并上报，避免整页白屏无痕迹。
import { Component, type ErrorInfo, type ReactNode } from 'react';
import { reportClientError } from '../lib/errorReport';

type Props = { children: ReactNode; fallback?: ReactNode };
type State = { error?: Error };

export class ErrorBoundary extends Component<Props, State> {
  state: State = {};

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo): void {
    void reportClientError(error, window.location.pathname, info.componentStack || '');
  }

  render(): ReactNode {
    if (this.state.error) {
      if (this.props.fallback) return this.props.fallback;
      return (
        <div style={{ minHeight: '100vh', display: 'flex', alignItems: 'center', justifyContent: 'center', background: '#f5f7fa', padding: 24 }}>
          <div style={{ maxWidth: 520, background: '#fff', border: '1px solid #e5e7eb', borderRadius: 12, padding: 28, textAlign: 'center' }}>
            <div style={{ fontSize: 34, marginBottom: 8 }}>😅</div>
            <h2 style={{ fontSize: 18, fontWeight: 700, color: '#111827', margin: '0 0 8px' }}>页面刚才没渲染出来</h2>
            <p style={{ color: '#6b7280', margin: '0 0 18px', lineHeight: 1.6 }}>
              刷新通常就能恢复。如果反复出现，把这个页面的地址发给运营。
            </p>
            <button
              onClick={() => window.location.reload()}
              style={{ border: '1px solid #d1d5db', background: '#f9fafb', borderRadius: 8, padding: '8px 14px', cursor: 'pointer' }}
            >
              刷新页面
            </button>
          </div>
        </div>
      );
    }
    return this.props.children;
  }
}
