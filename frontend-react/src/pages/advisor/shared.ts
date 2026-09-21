// 顾问工作台共享常量 / 领域小类型 / 展示辅助（C2 拆分 2026-09-21）
// 背景：原 pages/Advisor.tsx 单文件 51,979 字节（统计+列表+详情+6 个弹窗全塞在一个组件里），
// 按「首页 / 跟进 / 我的 / 详情 / 卡片 / 弹窗」拆成子组件后，这些常量与纯函数被多方共用，
// 抽到此处避免子组件之间互相 import 形成环。
// 约束：本文件只放**纯常量与纯函数**（无副作用、无 import 组件），便于子组件独立单测。
import type { Conversation } from '../../types'

// API 顾问工作台接口前缀
export const API = '/api/v1/advisor'

// STAGE_LABELS 客户旅程阶段码 → 中文名（列表/详情展示用）
export const STAGE_LABELS: Record<string, string> = { ai_connected: 'AI建联', human_connected: '人工建联', lead_captured: '已留资', arrived: '已到店', ordered: '已下单', delivered: '已交车', lost: '已战败' }

// STAGE_COLORS 阶段码 → Tailwind 徽标样式（状态标签配色）
export const STAGE_COLORS: Record<string, string> = { ai_connected: 'bg-gray-100 text-gray-600', human_connected: 'bg-blue-100 text-blue-600', lead_captured: 'bg-cyan-100 text-cyan-600', arrived: 'bg-green-100 text-green-600', ordered: 'bg-orange-100 text-orange-600', delivered: 'bg-red-100 text-red-600', lost: 'bg-gray-200 text-gray-600' }

// TABS 客户列表顶部筛选页签（值 + 中文文案）
export const TABS = [{ k: 'all', t: '全部' }, { k: 'pending', t: '待跟进' }, { k: 'following', t: '跟进中' }, { k: 'arrived', t: '已到店' }, { k: 'test_drive', t: '已试驾' }]

// H 客户姓名的展示处理：匿名访客统一显示为"客户"（避免泄露原始标识）
export const H = (n?: string) => (!n || n.startsWith('访客_')) ? '客户' : n

// HI 客户头像占位字符：访客取"客"，普通客户取姓名首字符
export const HI = (n?: string) => (!n || n.startsWith('访客_')) ? '客' : (n?.[0] || '?')

// Stat 首页统计卡片的数据结构（数值 + 文案 + 颜色）
export type Stat = { value: number; label: string; color: string }

// Quota 「我的」页套餐与三桶余额（GET /billing/my-package）
export type Quota = {
  used_ai_calls?: number
  max_ai_calls?: number
  ai_call_balance?: number
  expired_at?: string
}

// Followup 跟进提醒条目（GET /advisor/followups）
export type Followup = {
  customer_id: number
  customer_name?: string
  content?: string
  next_follow_at?: string
  method?: string
}

// TestDrive 试驾单（/advisor/test-drive 读写；字段对齐后端 test_drive model 的 json tag）
export type TestDrive = {
  id: number
  customer_id?: number
  model_name?: string
  contact_name?: string
  contact_phone?: string
  location?: string
  note?: string
  status?: string
  scheduled_at?: string
}

// TD_STATUS_LABELS 试驾单状态码 → 中文（未知/缺失按已取消呈现，与原内联三元语义一致）
export const TD_STATUS_LABELS: Record<string, string> = { pending: '待试驾', completed: '已完成', cancelled: '已取消' }

// ChannelContext 企微侧边栏上下文（GET /channel/wecom/context）
export type ChannelContext = {
  customer_id?: number
  journey_stage?: string
  interest_model?: string
  external_userid?: string
  staff_id?: string
  tags?: string
}

// Recommend 策略推荐卡片（GET /advisor/strategy/recommend）
export type Recommend = {
  intent_score?: number
  urgency_level?: string
  recommends?: { anchor_name?: string; template_name?: string; prompt_template?: string }[]
}

// FU_METHODS 新建跟进的方式选项（电话/微信/到店/邮件）
export const FU_METHODS = [{ k: 'phone', t: '电话' }, { k: 'wechat', t: '微信' }, { k: 'store', t: '到店' }, { k: 'email', t: '邮件' }]

// NAV_TABS 底部导航页签（首页/跟进/我的）
export const NAV_TABS = [{ k: 'home', t: '首页' }, { k: 'followup', t: '跟进' }, { k: 'me', t: '我的' }]

// URGENCY_LABELS 紧迫度码 → 中文（策略推荐卡片右上角展示）
export const URGENCY_LABELS: Record<string, string> = { high: '紧迫', medium: '中等', low: '平稳' }

// SENDER_LABELS 消息发送者类型 → 中文（历史会话时间线前缀）
export const SENDER_LABELS: Record<string, string> = { human: '顾问', ai: 'AI', customer: '客户' }

// fmtShort 统一的时间短格式（月/日 时:分），列表与时间线共用
export const fmtShort = (s?: string | null) => (s ? new Date(s).toLocaleString('zh-CN', { month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' }) : '')

// splitTags 渠道上下文的 tags 是逗号分隔字符串（中英文逗号都可能），切成去空数组
export const splitTags = (s?: string) => String(s || '').split(/[,，]/).map((x: string) => x.trim()).filter(Boolean)

// ConvBrief 详情里历史会话行的最小渲染集（Conversation 的子集，避免子组件依赖完整类型）
export type ConvBrief = Pick<Conversation, 'id' | 'channel' | 'status' | 'last_message_at'>
