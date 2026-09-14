// C6 前端异常采集：轻量自建上报，不引入商业 SDK。
// 采样规则：同一错误指纹只上报一次；普通错误 10% 采样；登录态/关键路由错误优先全量。
import { TOKEN_KEY } from './api';
export type ClientErrorPayload = {
  message: string;
  stack: string;
  route: string;
  user_agent: string;
  page: string;
  app: string;
  timestamp: number;
};

const SENT_KEY = 'scrm_error_reports_sent';
const SAMPLE_RATE = 0.1;

/** 为异常生成稳定指纹，用于采样与去重。 */
export function errorFingerprint(err: unknown): string {
  const msg = err instanceof Error ? err.message : String(err || '');
  return msg.slice(0, 160);
}

/** 读取已上报异常指纹集合，兼容 localStorage 不可用场景。 */
function sentFingerprints(): Set<string> {
  try {
    const raw = window.sessionStorage.getItem(SENT_KEY);
    const arr = raw ? JSON.parse(raw) : [];
    return new Set(Array.isArray(arr) ? arr.filter((x): x is string => typeof x === 'string') : []);
  } catch {
    return new Set();
  }
}

/** 记录已上报异常指纹，避免同一错误刷屏。 */
function rememberSent(fp: string) {
  try {
    const s = sentFingerprints();
    s.add(fp);
    window.sessionStorage.setItem(SENT_KEY, JSON.stringify([...s].slice(-50)));
  } catch {
    // sessionStorage 不可用（隐私模式）时退化为不上报
  }
}

/** 按采样率、去重指纹和路由信息判断是否上报前端异常。 */
export function shouldReportError(err: unknown, route: string, rand = Math.random): boolean {
  const fp = errorFingerprint(err);
  if (!fp || sentFingerprints().has(fp)) return false;
  const critical = /token|session|unauthorized|forbidden|billing|payment|admin|super/i.test(route + ' ' + fp);
  const keep = critical || rand() < SAMPLE_RATE;
  if (keep) rememberSent(fp);
  return keep;
}

/** 组装 C6 前端异常上报 payload。 */
export function buildClientErrorPayload(err: unknown, route: string, componentStack = ''): ClientErrorPayload {
  const message = err instanceof Error ? err.message : String(err || 'Unknown error');
  const rawStack = err instanceof Error && err.stack ? err.stack : componentStack;
  const stack = [rawStack, componentStack].filter(Boolean).join('\n');
  return {
    message,
    stack,
    route,
    user_agent: navigator.userAgent || '',
    page: location.href || '',
    app: location.pathname.startsWith('/app') ? 'mobile' : 'desktop',
    timestamp: Date.now(),
  };
}

/** 异步上报前端异常，失败时静默吞掉避免二次噪声。 */
export async function reportClientError(err: unknown, route: string, componentStack = ''): Promise<void> {
  if (typeof window === 'undefined') return;
  if (!shouldReportError(err, route)) return;
  const body = buildClientErrorPayload(err, route, componentStack);
  try {
    const headers: Record<string, string> = { 'Content-Type': 'application/json' };
    const token = localStorage.getItem(TOKEN_KEY) || '';
    if (token) headers.Authorization = 'Bearer ' + token;
    await fetch('/api/v1/client-errors', { method: 'POST', headers, body: JSON.stringify(body) });
  } catch {
    // 上报失败不能继续触发异常，静默丢弃
  }
}

/** 安装全局 error/unhandledrejection 监听器，接入异常上报。 */
export function installGlobalErrorHandlers(): void {
  if (typeof window === 'undefined') return;
  window.addEventListener('error', (event) => {
    const err = event.error || event.message;
    void reportClientError(err, location.pathname);
  });
  window.addEventListener('unhandledrejection', (event) => {
    void reportClientError(event.reason, location.pathname);
  });
}
