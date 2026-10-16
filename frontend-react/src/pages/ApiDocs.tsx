/**
 * ApiDocs.tsx：租户开放 API 文档站（E10，2026-09-19）
 * 数据源为后端规格端点 GET /api/v1/openapi/spec（OpenAPI 3.0.3 JSON，公开无鉴权）。
 * 该 JSON 非 {code,message,data} 信封，故必须走 apiJSON（裸 fetch/AUTH 分别踩 P2-6 棘轮与信封误判）。
 * 页面自渲染端点卡片（方法/路径/参数/请求体/响应/curl 示例），不引 redoc/swagger-ui 三方包；
 * 内容由契约护栏保证与真实 /openapi/v1 路由双向一致（cmd/apidump -format openapi）。
 */
import { useEffect, useState } from 'react'
import { useBrand } from '../lib/branding'
import { apiJSON } from '../lib/api'
import { useI18n } from '../i18n/useI18n'

// ---- 以下为 OpenAPI 3.0 文档结构的窄类型（仅声明本页面渲染用到的字段） ----

// 参数对象（query/path 入参）
type SpecParam = { name: string; in: string; required?: boolean; description?: string; schema?: { type?: string; default?: string | number; description?: string } }
// 请求体/响应里的媒体类型对象
type SpecMedia = { schema?: { properties?: Record<string, { type?: string; description?: string }>; required?: string[] }; example?: unknown; description?: string }
// 单个响应状态码定义
type SpecResp = { description?: string; content?: Record<string, SpecMedia> }
// 单个端点操作（method 级）
type SpecOp = { summary?: string; description?: string; parameters?: SpecParam[]; requestBody?: { required?: boolean; content?: Record<string, SpecMedia> }; responses?: Record<string, SpecResp> }
// 规格文档根结构
type Spec = {
  openapi: string
  info: { title: string; version: string; description?: string }
  servers?: { url: string; description?: string }[]
  components?: { securitySchemes?: Record<string, { scheme?: string; bearerFormat?: string; description?: string }> }
  paths: Record<string, Record<string, SpecOp>>
}

// 端点展示顺序（按对接常用度排；规格新增端点时自动追加到末尾，不丢渲染）
const ORDER = ['/chat/completions', '/customers', '/customers/{id}/conversations', '/cdp/profiles/{one_id}', '/usage']

// 路径占位符 → curl 示例里的替身值
const PATH_SAMPLES: Record<string, string> = { id: '1', one_id: 'oneid_demo' }

/** 方法徽章配色：GET 绿 / POST 蓝，其余回退灰 */
function methodColor(m: string): string {
  return m === 'get' ? '#00a870' : m === 'post' ? '#0052d9' : '#6b7280'
}

/** 把含 `反引号代码` 的短文本渲染成 code 标签序列（说明文字里的字段名可读性） */
function renderInline(text: string) {
  return text.split('`').map((seg, i) => (i % 2 === 1
    ? <code key={i} style={{ background: '#edf2f7', padding: '1px 5px', borderRadius: 4, fontSize: 12 }}>{seg}</code>
    : <span key={i}>{seg}</span>))
}

/** 由规格与当前来源拼装 curl 示例：GET 带 query 样例，POST 带 requestBody 示例 */
function buildCurl(origin: string, base: string, path: string, method: string, op: SpecOp): string {
  // 占位符替换（{id} → 1）
  const p = path.replace(/\{(\w+)\}/g, (_, k) => PATH_SAMPLES[k] || k)
  const lines: string[] = [`curl -X ${method.toUpperCase()} '${origin}${base}${p}'`]
  lines.push(`  -H 'Authorization: Bearer sk_YOUR_KEY'`)
  if (method === 'post') {
    const body = op.requestBody?.content?.['application/json']?.example
    lines.push(`  -H 'Content-Type: application/json'`)
    if (body) lines.push(`  -d '${JSON.stringify(body)}'`)
  } else {
    // GET：带上非必填 query 参数示例，便于直接改造
    const qs = (op.parameters || []).filter((x) => x.in === 'query').slice(0, 3).map((x) => `${x.name}=${x.schema?.default ?? 'x'}`).join('&')
    if (qs) lines[0] = `curl -X GET '${origin}${base}${p}?${qs}'`
  }
  return lines.join(' \\\n')
}

/**
 * 开放 API 文档页组件
 * 1. 拉取公开规格 /api/v1/openapi/spec（含鉴权说明与端点列表）
 * 2. 概览区：标题/版本/总体说明/鉴权方式
 * 3. 端点卡片区：按 ORDER 排序逐端点渲染参数表/请求体/响应/curl
 */
export default function ApiDocs() {
  const { t } = useI18n()
  const brand = useBrand()
  const [spec, setSpec] = useState<Spec | null>(null)
  const [err, setErr] = useState('')

  useEffect(() => {
    // 规格端点返回裸 OpenAPI JSON（无 code 信封），apiJSON 只负责解析不判业务码
    apiJSON<Spec>('/api/v1/openapi/spec').then(({ res, json }) => {
      // P1-6(2026-09-20)：apiJSON 网络层失败归一为 res=null（不再 reject），res 判空先行
      if (!res || !res.ok || !json || !json.paths) { setErr(t('apiDocs.errSpec')); return }
      setSpec(json)
    }).catch(() => setErr(t('apiDocs.errNetwork')))
  }, [t])

  // 加载中 / 失败占位
  if (err) {
    return (
      <div style={{ minHeight: '100vh', display: 'flex', alignItems: 'center', justifyContent: 'center', background: '#f5f7fa', fontFamily: '-apple-system, PingFang SC, sans-serif' }}>
        <div style={{ textAlign: 'center', color: '#718096' }}>{err}<div style={{ marginTop: 12 }}><a href="/" style={{ color: 'var(--pri)' }}>{t('apiDocs.backHome')}</a></div></div>
      </div>
    )
  }
  if (!spec) {
    return (
      <div style={{ minHeight: '100vh', display: 'flex', alignItems: 'center', justifyContent: 'center', background: '#f5f7fa', fontFamily: '-apple-system, PingFang SC, sans-serif', color: '#6b7280', fontSize: 14 }}>
        {t('apiDocs.loading')}
      </div>
    )
  }

  const base = spec.servers?.[0]?.url || '/openapi/v1'
  const scheme = spec.components?.securitySchemes?.BearerKey
  // 展示顺序：先按 ORDER，规格里多出来的端点追加尾部（契约演进页面零改动）
  const orderedPaths = [...ORDER.filter((p) => spec.paths[p]), ...Object.keys(spec.paths).filter((p) => !ORDER.includes(p))]

  return (
    <div className="px-4 py-8 lg:px-5" style={{ fontFamily: '-apple-system, PingFang SC, sans-serif', background: '#f5f7fa', minHeight: '100vh', color: '#2d3748' }}>
      <div style={{ maxWidth: 860, margin: '0 auto' }}>
        {/* 概览区 */}
        <h1 className="text-xl sm:text-2xl" style={{ marginBottom: 6 }}>{spec.info.title}</h1>
        <div style={{ fontSize: 13, color: '#718096', marginBottom: 16 }}>
          {t('apiDocs.metaLine', { version: spec.info.version, openapi: spec.openapi })} <code style={{ background: '#edf2f7', padding: '1px 6px', borderRadius: 4 }}>{base}</code>
        </div>
        {spec.info.description && (
          <div style={{ background: '#fff', borderRadius: 12, padding: 20, boxShadow: '0 4px 18px rgba(0,0,0,.07)', marginBottom: 20, fontSize: 14, lineHeight: 1.9 }}>
            {spec.info.description.split('\n\n').map((para, i) => <p key={i} style={{ margin: '0 0 8px' }}>{renderInline(para)}</p>)}
            {scheme?.description && <p style={{ margin: '8px 0 0', color: '#4a5568' }}>{renderInline(t('apiDocs.authPrefix') + (scheme.description || ''))}</p>}
          </div>
        )}

        {/* 端点卡片区 */}
        {orderedPaths.map((path) => {
          const ops: Record<string, SpecOp> = spec.paths[path] || {}
          return Object.keys(ops).map((method) => {
            const op: SpecOp = ops[method] ?? ({} as SpecOp)
            const params: SpecParam[] = op.parameters || []
            const bodySchema: SpecMedia['schema'] = op.requestBody?.content?.['application/json']?.schema
            const bodyExample = op.requestBody?.content?.['application/json']?.example
            const respOk = op.responses?.['200']
            const respExample = respOk?.content?.['application/json']?.example
            const others = Object.keys(op.responses || {}).filter((s) => s !== '200')
            return (
              <div key={method + path} id={path} style={{ background: '#fff', borderRadius: 12, padding: 22, boxShadow: '0 4px 18px rgba(0,0,0,.07)', marginBottom: 18 }}>
                {/* 方法 + 路径 + 摘要 */}
                <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
                  <span style={{ background: methodColor(method), color: '#fff', fontSize: 12, fontWeight: 700, padding: '3px 10px', borderRadius: 6 }}>{method.toUpperCase()}</span>
                  <code style={{ fontSize: 14, fontWeight: 600 }}>{base}{path}</code>
                  <b style={{ fontSize: 14, marginLeft: 'auto', color: '#4a5568' }}>{op.summary}</b>
                </div>
                {op.description && <p style={{ fontSize: 13, color: '#718096', margin: '10px 0 0', lineHeight: 1.8 }}>{renderInline(op.description)}</p>}

                {/* 参数表 */}
                {params.length > 0 && (
                  <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 13, marginTop: 14 }}>
                    <thead><tr style={{ textAlign: 'left', color: '#718096', borderBottom: '1px solid #e2e8f0' }}>
                      <th style={{ padding: '6px 8px' }}>{t('apiDocs.colParam')}</th><th style={{ padding: '6px 8px' }}>{t('apiDocs.colIn')}</th><th style={{ padding: '6px 8px' }}>{t('apiDocs.colType')}</th><th style={{ padding: '6px 8px' }}>{t('apiDocs.colRequired')}</th><th style={{ padding: '6px 8px' }}>{t('apiDocs.colDesc')}</th>
                    </tr></thead>
                    <tbody>
                      {params.map((p) => (
                        <tr key={p.name} style={{ borderBottom: '1px dashed #edf2f7' }}>
                          <td style={{ padding: '6px 8px' }}><code>{p.name}</code></td>
                          <td style={{ padding: '6px 8px', color: '#718096' }}>{p.in}</td>
                          <td style={{ padding: '6px 8px', color: '#718096' }}>{p.schema?.type || '-'}</td>
                          <td style={{ padding: '6px 8px' }}>{p.required ? t('apiDocs.yes') : t('apiDocs.no')}</td>
                          <td style={{ padding: '6px 8px', color: '#4a5568' }}>{p.description}{p.schema?.default !== undefined && t('apiDocs.defaultSuffix', { d: String(p.schema.default) })}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                )}

                {/* 请求体字段 */}
                {bodySchema?.properties && (
                  <div style={{ marginTop: 14 }}>
                    <b style={{ fontSize: 13 }}>{t('apiDocs.bodyFields')}</b>
                    <ul style={{ margin: '8px 0', paddingLeft: 18, fontSize: 13, color: '#4a5568', lineHeight: 1.9 }}>
                      {Object.entries(bodySchema.properties).map(([k, v]) => (
                        <li key={k}><code>{k}</code>（{v.type}{(bodySchema.required || []).includes(k) ? t('apiDocs.requiredSuffix') : ''}）：{v.description}</li>
                      ))}
                    </ul>
                  </div>
                )}
                {/* example 为 unknown（OpenAPI 任意 JSON 值）：strict 下 `unknown && jsx` 假分支是 unknown 不可赋 ReactNode，布尔化收口 */}
                {!!bodyExample && (
                  <pre style={{ background: '#1a202c', color: '#e2e8f0', fontSize: 12, padding: 14, borderRadius: 8, overflowX: 'auto', marginTop: 8 }}>{JSON.stringify(bodyExample, null, 2)}</pre>
                )}

                {/* 响应说明 */}
                {respOk?.description && <div style={{ fontSize: 13, marginTop: 12 }}><b>{t('apiDocs.respOk')}</b>　<span style={{ color: '#4a5568' }}>{renderInline(respOk.description)}</span></div>}
                {/* example 为 unknown（OpenAPI 任意 JSON 值）：strict 下 `unknown && jsx` 假分支是 unknown 不可赋 ReactNode，布尔化收口 */}
                {!!respExample && (
                  <pre style={{ background: '#1a202c', color: '#e2e8f0', fontSize: 12, padding: 14, borderRadius: 8, overflowX: 'auto', marginTop: 8 }}>{JSON.stringify(respExample, null, 2)}</pre>
                )}
                {others.length > 0 && (
                  <div style={{ fontSize: 12, color: '#718096', marginTop: 8 }}>
                    {t('apiDocs.otherStatus')}{others.map((s) => `${s} ${op.responses?.[s]?.description || ''}`).join('；')}
                  </div>
                )}

                {/* curl 示例 */}
                <details style={{ marginTop: 12 }}>
                  <summary style={{ cursor: 'pointer', fontSize: 13, color: 'var(--pri)' }}>{t('apiDocs.curlExample')}</summary>
                  <pre style={{ background: '#1a202c', color: '#e2e8f0', fontSize: 12, padding: 14, borderRadius: 8, overflowX: 'auto', marginTop: 8, whiteSpace: 'pre-wrap' }}>
                    {buildCurl(location.origin, base, path, method, op)}
                  </pre>
                </details>
              </div>
            )
          })
        })}

        {/* 底部导航与页脚 */}
        <div style={{ textAlign: 'center', marginTop: 28 }}>
          <a href="/" style={{ color: 'var(--pri)' }}>{t('apiDocs.navHome')}</a>　<a href="/pricing" style={{ color: 'var(--pri)' }}>{t('apiDocs.navPricing')}</a>　<a href="/register" style={{ color: 'var(--pri)' }}>{t('apiDocs.navSignup')}</a>
        </div>
        <footer style={{ textAlign: 'center', padding: 16, color: '#94a3b8', fontSize: 12, borderTop: '1px solid #e5e7eb', marginTop: 28, lineHeight: 2 }}>
          <a href="/user-agreement" style={{ color: 'var(--pri)' }}>{t('login.userAgreement')}</a> · <a href="/privacy-policy" style={{ color: 'var(--pri)' }}>{t('login.privacyPolicy')}</a> · {t('apiDocs.footerBrand', { brand: brand.brandName })}
        </footer>
      </div>
    </div>
  )
}
