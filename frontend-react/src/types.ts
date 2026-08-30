// 前端共享领域类型（P2 TS 类型规范化，2026-08-29）
// 将散落在各页面的内联 type 收敛到此处，统一前后端字段口径，减少 any 漂移。

// 一条聊天消息（前后端统一口径）
export interface Msg {
  id?: number | string
  conversation_id?: number
  sender_type: string
  content: string
  created_at?: string
}

// 客户列表项（顾问/管理端列表展示用）
export interface Cust {
  id: number
  name?: string
  phone?: string
  interest_model?: string
  journey_stage?: string
  assigned_user_name?: string
  conv_mode?: string
  last_message?: string
  last_message_at?: string
  updated_at?: string
}

// 客户详情（资料+标签+跟进+会话）
export interface Detail {
  customer: any
  tags?: { tag_name: string }[]
  followups?: any[]
  conversations?: any[]
}

// 租户白标配置（名称/Logo/主题色等）
export interface BrandConfig {
  brandName: string
  logoUrl: string
  faviconUrl: string
  primaryColor: string
}
