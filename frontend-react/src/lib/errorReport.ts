// C6 前端异常采集：轻量自建上报，不引入商业 SDK。
// 采样规则：同一错误指纹只上报一次；普通错误 10% 采样；登录态/关键路由错误优先全量。
// E2(2026-09-19 增强批)：可选 Sentry/GlitchTip 双轨——仅当构建期注入 VITE_SENTRY_DSN 才
// 动态 import @sentry/react；未配置时本文件行为与旧版逐字节一致（/client-errors 单轨兜底）。
import { TOKEN_KEY } from './api';

/** Sentry 懒加载桥：DSN 未设直接短路，SDK chunk 不进首屏、加载/初始化失败静默吞掉 */
let sentryPromise: Promise<unknown> | null = null
export function sentryBridge(): Promise<unknown> | null {
  const dsn = (import.meta.env.VITE_SENTRY_DSN as string | undefined) || ''
  if (!dsn) return null
  if (!sentryPromise) {
    sentryPromise = import('@sentry/react')
      .then((Sentry) => {
        Sentry.init({
          dsn,
          release: (import.meta.env.VITE_APP_VERSION as string | undefined) || 'dev',
          // PII 红线：不采集默认 PII（IP/UA 附着头），异常文本经 sanitizeMessage 脱敏
          sendDefaultPii: false,
          beforeSend: (event) => {
            if (event.message) event.message = sanitizeMessage(event.message)
            for (const ex of event.exception?.values ?? []) {
              if (ex.value) ex.value = sanitizeMessage(ex.value)
            }
            return event
          },
        })
        return Sentry
      })
      .catch(() => null) // 离线/被 CSP 拦等场景：主轨 /client-errors 不受影响
  }
  return sentryPromise
}

/** 把异常送到 Sentry 轨（DSN 未配置时为 no-op），与 C6 自建轨并行双写 */
export function reportToSentry(err: unknown): void {
  const p = sentryBridge()
  if (!p) return
  void p.then((Sentry) => {
    const mod = Sentry as { captureException?: (e: unknown) => void } | null
    mod?.captureException?.(err)
  })
}

/** C6 上报文本脱敏：手机号/visitor_key/token 截断，Sentry 与自建轨共用同一口径 */
export function sanitizeMessage(msg: string): string {
  return msg
    .replace(/(1[3-9]\d)\d{4}(\d{4})/g, '$1****$2')
    .replace(/(visitor_key|token)=([^&\s"']+)/gi, '$1=***')
}

/** C6 异常上报请求体（对应后端 /client-errors 契约），字段均已脱敏 */
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
