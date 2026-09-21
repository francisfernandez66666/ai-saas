// 平台超管后台共享常量 / 领域类型 / 通用容器（C2 拆分 2026-09-21）
// 背景：原 pages/SuperAdmin.tsx 单文件 40,276 字节（14 个菜单视图挤在一个组件里），
// 拆成 per-view Tab 组件后，常量、行类型与通用容器被多方共用，抽到此处避免子组件互引成环。
import { Input } from 'tdesign-react'

// FB_TYPES 反馈类型码 → 中文名（反馈列表列渲染）
export const FB_TYPES: Record<string, string> = { ai_reply: 'AI话术', feature: '功能建议', rating: '满意度', other: '其他', client_error: '前端异常' }
// TYPE_NAMES 商业包类型码 → 中文名（包管理列表列渲染）
export const TYPE_NAMES: Record<string, string> = { free: '试用', paid: '包月', increment: '增量买断' }
// AG_TYPES 协议类型码 → 中文名（协议签署列表列渲染）
export const AG_TYPES: Record<string, string> = { user: '用户协议', privacy: '隐私政策' }

// P1-10(2026-09-20)：平台管理菜单单一数据源——桌面 Aside Menu 与窄屏顶栏下拉共用，防两处漂移
export const SUPER_MENUS: { k: string; label: string }[] = [
  { k: 'tenants', label: '租户管理' },
  { k: 'packages', label: 'AI 商业包' },
  { k: 'industry_packs', label: '行业包管理' }, // 区别于 AI 商业包 packages，此处是行业包 .aipack 目录
  { k: 'pack_quality', label: '包质量' },
  { k: 'cost', label: '模型成本核算' },
  { k: 'feedbacks', label: '用户反馈' },
  { k: 'materials', label: '素材审核' }, // P1-9：KB 素材池人工评审（通过/拒绝/AI评分）
  { k: 'pending', label: '待确认收款' },
  { k: 'invoices', label: '发票受理' }, // requested→issued/voided 人工闭环
  { k: 'refunds', label: '退款受理' }, // B7 退款平台审批位（执行/驳回）
  { k: 'audit', label: '审计日志' },
  { k: 'agreements', label: '协议签署' },
  { k: 'branding', label: '品牌定制（白标）' },
  { k: 'monitor', label: '平台健康监控' },
]

// 租户摘要行（超管租户列表）
export type Tenant = { id: number; name: string; code: string; plan_name?: string; used_customers: number; max_customers?: number; status: string; created_at: string; max_users?: number; used_users?: number; plan_id?: number }
// 换套餐下拉数据源（GET /super/plans，商业缺口批 2026-09-16）
export type PlanOpt = { id: number; name: string; tier?: string; max_users: number; max_customers: number; price_monthly_cents: number }
// AI 商业包模型（后台包管理）
export type Pkg = { id: number; code: string; name: string; p_type: string; ai_calls: number; price_cents: number; duration_days?: number; enabled: boolean }
// 模型成本核算汇总（近 N 天）
export type Cost = { days: number; total_calls: number; total_tokens: number; total_cost_yuan: number; models: { provider: string; model: string; calls: number; tokens: number; cost_yuan: number; cost_share_pct: number }[] }
// 用户反馈条目（顾问端提交）
export type Fb = { id: number; tenant_id: number; tenant_name?: string; username?: string; target_type: string; content: string; context?: string; status: string; created_at: string }
// 待确认收款订单（已扫码付款）
export type Pending = { id: number; order_no: string; tenant_id: number; tenant_name?: string; package_name?: string; amount_cents: number; created_at: string }
// 审计日志记录行
export type Audit = { created_at: string; tenant_id: number; action: string; username?: string; resource?: string; detail?: string; ip?: string }
// 协议签署记录（用户/隐私）
export type Ag = { id: number; username?: string; tenant_id: number; tenant_name?: string; agreement_type: string; version: string; status: string; signed_at: string }
// D9 包质量跨租户聚合行（key 为前端合成的 React rowKey）
export type PackQualityRow = { key: string; tenant_id: number; pack_code: string; pack_version: string; template_id: string; sample_count: number; hook_rate: number; lead_rate: number; pending_human_rate: number; avg_intent_delta: number; avg_eval_score?: number | null }
// 白标表单（custom_css/custom_js 为 A5 补齐的编辑位）
export type BdForm = { custom_domain: string; brand_name: string; brand_link: string; logo_url: string; favicon_url: string; primary_color: string; secondary_color: string; custom_css: string; custom_js: string }

// esc 把空值安全转成字符串，避免表格渲染出 undefined
export const esc = (s?: string) => (s == null ? '' : String(s))

// Section 后台通用分区容器：标题 + 可选说明 + 内容
export function Section({ title, desc, children }: { title: React.ReactNode; desc?: string; children: React.ReactNode }) {
  return (
    <div style={{ marginBottom: 30 }}>
      <h2 style={{ fontSize: 18, marginBottom: 4, marginTop: 10 }}>{title}</h2>
      {desc && <p style={{ color: '#718096', fontSize: 13, marginBottom: 12 }}>{desc}</p>}
      {children}
    </div>
  )
}

// Field 白标配置的单行输入字段：标签 + 输入框
export function Field({ label, v, set, ph }: { label: string; v: string; set: (x: string) => void; ph?: string }) {
  return <div><label style={{ display: 'block', fontSize: 13, color: '#475569', marginBottom: 4 }}>{label}</label><Input value={v} onChange={(x) => set(x)} placeholder={ph} style={{ width: '100%' }} /></div>
}

// SELECT_STYLE 审计日志等筛选控件（下拉/输入框）统一样式
export const SELECT_STYLE: React.CSSProperties = { padding: 8, border: '1px solid #e2e8f0', borderRadius: 6 }
