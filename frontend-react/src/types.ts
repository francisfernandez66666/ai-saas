// 前端共享领域类型（P2 TS 类型规范化，2026-08-29）
// 将散落在各页面的内联 type 收敛到此处，统一前后端字段口径，减少 any 漂移。
// 字段命名严格对齐 Go 后端 model 的 json tag，便于前后端一致。

// ============================================================
// 通用响应信封
// ============================================================

// 后端统一返回结构：{ code, message, data }
export interface ApiResp<T> {
  code: number
  message: string
  data: T
}

// 分页信封：list 接口通用包装（data 内含 list/total/page/page_size）
export interface Paginated<T> {
  list: T[]
  total: number
  page: number
  page_size: number
}

// ============================================================
// 鉴权 / 用户
// ============================================================

// 登录响应中的用户信息（对齐 auth.go 返回的 user 子对象）
export interface AuthUser {
  id: number
  username: string
  role: string
  tenant_id: number
  must_change_password: boolean
}

// 登录响应 data 结构（对齐 auth.go Login 返回）
export interface AuthResult {
  token: string
  user: AuthUser
}

// 系统用户（tenant_users 表；不含 password_hash）
export interface User {
  id: number
  username: string
  real_name?: string
  role: string // super_admin/tenant_admin/admin/sales/readonly
  phone?: string
  email?: string
  avatar?: string
  status?: number // 1-正常 0-禁用
  must_change_password?: boolean
  department?: string
  tenant_id?: number | null
  department_id?: number | null
  created_at?: string
  updated_at?: string
}

// 顾问（销售）档案：用户表中 role=sales 的视角的简化实体
export interface Advisor {
  id: number
  username: string
  real_name?: string
  role: string
  avatar?: string
  status?: number
}

// ============================================================
// 租户
// ============================================================

// 租户（tenants 表；指针字段序列化为 null/缺失，用可选联合表示）
export interface Tenant {
  id: number
  name: string
  code: string
  custom_domain?: string | null
  tier?: string // personal/enterprise/custom
  plan_id?: number
  logo_url?: string
  favicon_url?: string
  brand_name?: string
  brand_link?: string
  primary_color?: string
  secondary_color?: string
  custom_css?: string
  custom_js?: string
  contact_name?: string
  contact_phone?: string
  contact_email?: string
  industry?: string
  scale?: string
  trial_start_at?: string | null
  trial_end_at?: string | null
  subscribed_at?: string | null
  expired_at?: string | null
  grace_period_end_at?: string | null
  max_users?: number
  max_customers?: number
  max_departments?: number
  max_ai_calls_monthly?: number
  max_storage_mb?: number
  max_knowledge_brands?: number
  max_knowledge_models?: number
  used_ai_calls?: number
  ai_call_balance?: number
  monthly_token_quota?: number
  monthly_token_used?: number
  token_balance?: number
  free_token_balance?: number
  free_token_expires_at?: string | null
  invite_code?: string
  invited_by_tenant_id?: number | null
  referral_paid_rewarded?: boolean
  used_customers?: number
  used_storage_bytes?: number
  usage_reset_at?: string | null
  status?: string // trial/active/suspended/expired/cancelled
  cancel_at?: string | null
  features_override?: unknown
  white_label_config?: unknown
  created_at?: string
  updated_at?: string
  deleted_at?: string | null
}

// 租户白标配置（名称/Logo/主题色等）——前端注入用
export interface BrandConfig {
  brandName: string
  logoUrl: string
  faviconUrl: string
  primaryColor: string
}

// ============================================================
// 客户
// ============================================================

// 客户（customers 表）
export interface Customer {
  id: number
  name?: string
  phone?: string
  wechat_id?: string
  gender?: number // 0-未知 1-男 2-女
  age?: number
  region?: string
  city?: string
  career?: string
  customer_type?: string // potential/owner
  interest_model?: string
  current_car?: string
  car_age?: number
  source?: string
  budget?: number
  decision_cycle?: number
  store_visited?: number // 0-未到店 1-已到店 2-多次到店
  trust_level?: number
  intent_score?: number
  price_sensitivity?: number
  brand_awareness?: number
  resistance_type?: string // none/price/spec/service/brand
  journey_stage?: string // ai_connected/human_connected/lead_captured/arrived/ordered/delivered/lost
  journey_sub_stage?: string // 空/test_driven/quoted
  tags?: string // JSON 数组字符串
  t_vector_json?: string
  remark?: string
  assigned_user_id?: number
  tenant_id?: number
  visitor_key?: string
  external_user_id?: string
  assignment_reason?: string
  status?: number // 1-正常 0-无效
  created_at?: string
  updated_at?: string
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
// 注意：detail.tags 的线上格式为 { tag_name }[]（后端 customer detail 返回），
// 与 tags 列表接口的 Tag 实体不同，此处按实际 wire 格式标注。
export interface Detail {
  customer: Customer
  tags?: { tag_name: string }[]
  followups?: any[]
  conversations?: Conversation[]
}

// ============================================================
// 会话 / 消息
// ============================================================

// 会话（conversations 表）
export interface Conversation {
  id: number
  tenant_id?: number
  customer_id: number
  assigned_user_id?: number
  flow_instance_id?: number
  status?: string // active/closed/transferred
  channel?: string // web/wechat/phone
  session_id?: string
  mode?: string // ai/human/fish
  attempts?: number
  hook_count?: number
  last_tid?: string
  last_anchor_type?: number
  emotion?: string // positive/neutral/negative
  high_intent_rounds?: number
  current_stage?: number
  silent_duration?: number
  last_message_at?: string | null
  last_human_reply_at?: string | null
  is_human_locked?: boolean
  is_ai_reply_enabled?: boolean
  guided_disabled?: boolean
  guided_remaining_rounds?: number
  pending_handoff?: boolean
  handoff_notified_at?: string | null
  state_json?: string
  created_at?: string
  updated_at?: string
}

// 一条聊天消息（messages 表，前后端统一口径）
export interface Msg {
  id?: number | string
  conversation_id?: number
  sender_type: string
  content: string
  created_at?: string
}

// 消息（messages 表）——完整字段版，供聊天记录/历史接口使用
export interface ChatMessage {
  id: number
  tenant_id?: number
  conversation_id: number
  customer_id?: number
  sender_type: string // customer/ai/human/system
  sender_id?: number
  content: string
  message_type?: string // text/image/card
  anchor_type?: number
  template_id?: string
  route_result?: string
  intent_score?: number
  hooked?: boolean | null
  emotion?: string
  metadata?: string
  created_at?: string
  updated_at?: string
}

// 聊天发送响应（advisor/chat/send 返回 conversation_id 等）
export interface ChatSendResult {
  conversation_id?: number
  message?: ChatMessage
}

// ============================================================
// 标签
// ============================================================

// 标签（tags 表）
export interface Tag {
  id: number
  tenant_id?: number
  name: string
  code?: string
  category?: string
  weight?: number
  description?: string
  status?: number
  created_at?: string
  updated_at?: string
}

// ============================================================
// 流程引擎
// ============================================================

// 流程节点（flow_definitions.nodes 内单节点）
export interface FlowNode {
  id: string
  type: string // start/ai/strategy/human/condition/tag_update/wait/end
  name: string
  config?: Record<string, unknown>
  next_nodes?: string[]
}

// 流程连线（flow_definitions.edges 内单连线）
export interface FlowEdge {
  from: string
  to: string
  condition?: string
}

// 流程定义（flow_definitions 表）
export interface FlowDefinition {
  id: number
  tenant_id?: number
  name: string
  code?: string
  description?: string
  nodes_json?: string
  edges_json?: string
  start_node_id?: string
  is_default?: boolean
  status?: number
  version?: string
  created_at?: string
  updated_at?: string
}

// ============================================================
// 商业化：订单 / 套餐 / 包
// ============================================================

// 订单（billing_orders 表）
export interface BillingOrder {
  id: number
  order_no?: string
  tenant_id?: number | null
  plan_id?: number
  package_id?: number
  amount_cents?: number
  original_amount_cents?: number
  period?: string // monthly/yearly/once
  pay_channel?: string // wechat/alipay/manual（legacy）
  channel?: string // mock/manual/wechat/alipay
  status?: string // pending/paid/refunding/refunded/closed/expired
  paid_at?: string | null
  refunded_at?: string | null
  expire_at?: string | null
  payment_data?: string
  manual_confirm?: boolean
  invoice_requested?: boolean
  invoice_status?: string
  qr_content?: string
  remark?: string
  created_at?: string
  updated_at?: string
}

// 支付下单响应 data（billing/subscribe 等返回 {order, pay_mode}）
export interface PaymentIntent {
  order: BillingOrder
  pay_mode?: string
}

// 商业包（packages 表）
export interface Package {
  id: number
  code: string
  name: string
  p_type: string // free/paid/increment
  ai_calls?: number
  token_amount?: number
  price_cents?: number
  duration_days?: number
  description?: string
  enabled?: boolean
  sort_order?: number
  created_at?: string
  updated_at?: string
}

// 订阅套餐（subscription_plans 表）
export interface SubscriptionPlan {
  id: number
  name: string
  code: string
  tier?: string // personal/enterprise/custom
  description?: string
  price_monthly_cents?: number
  price_yearly_cents?: number
  trial_days?: number
  max_users?: number
  max_departments?: number
  max_customers?: number
  max_ai_calls_monthly?: number
  max_storage_mb?: number
  max_knowledge_brands?: number
  max_knowledge_models?: number
  features?: string
  highlights?: string
  is_active?: boolean
  sort_order?: number
  created_at?: string
  updated_at?: string
}

// 顾问/租户当前套餐概况（billing/my-package 返回）
export interface MyPackage {
  plan?: SubscriptionPlan
  pkg?: Package
  ai_calls_used?: number
  ai_calls_total?: number
  token_balance?: number
  expired_at?: string | null
}

// ============================================================
// 知识库
// ============================================================

// 知识库片段（knowledge_fragments 表）
export interface KbMaterial {
  id: number
  tenant_id?: number
  category?: string // 品牌/车型/技术/服务/活动/企业知识
  title: string
  content?: string
  tags?: string // JSON 数组字符串
  applicable_models?: string // JSON 数组字符串
  status?: number // 1启用 0禁用
  sort?: number
  embedding_json?: string
  created_at?: string
  updated_at?: string
}

// ============================================================
// 用量看板
// ============================================================

// 单日用量行（admin/usage/summary by_day）
export interface UsageDayRow {
  date: string
  calls: number
  tokens: number
  cost_micro: number
  cost_yuan?: number
}

// 阶段分布行（admin/usage/summary by_stage）
export interface UsageStageRow {
  stage: string
  calls?: number
  tokens?: number
  count?: number
}

// 租户用量汇总（admin/usage/summary）
export interface UsageSummary {
  days: number
  total_calls: number
  total_tokens: number
  total_cost_yuan: number
  by_day: UsageDayRow[]
  by_stage: UsageStageRow[]
}

// 单模型成本行（super/usage/cost models）
export interface UsageCostRow {
  provider: string
  model: string
  calls: number
  tokens: number
  cost_yuan: number
  cost_share_pct: number
}

// 平台级用量成本汇总（super/usage/cost）
export interface UsageCostSummary {
  days: number
  total_calls: number
  total_tokens: number
  total_cost_yuan: number
  models: UsageCostRow[]
}

// ============================================================
// 邀请推广
// ============================================================

// 邀请信息（admin/referral/info 的 referral 子对象）
export interface ReferralInfo {
  invite_code?: string
  invited_count?: number
  paid_count?: number
  free_token_balance?: number
  token_balance?: number
}

// 单条邀请记录（admin/referral/records）
export interface ReferralRecord {
  tenant_id: number
  company_name: string
  email: string
  invited_ok?: boolean
  paid_ok?: boolean
  paid_rewarded?: boolean
  signup_reward?: boolean
  registered_at?: string
}

// ============================================================
// 表格行数据类型（P1-2：消除 TDesign 单元格回调中的 any 漂移）
// ============================================================

// 对齐 tdesign-react 的 TableRowData（其底层即 Record<string, any>）。
// 行数据来自后端 JSON，字段动态，统一用此别名替代散落的 `any`，
// 既收敛类型口径，又保留对任意字段的访问能力。
export type TableRowData = Record<string, any>

// TDesign PrimaryTableCol.cell 回调入参（取常用字段）。
// 用此别名替代 (p: any)，消除显式 any 标注。
export interface CellProps {
  row: TableRowData
  rowIndex: number
  col: { colKey: string; title?: string }
  colIndex: number
}
