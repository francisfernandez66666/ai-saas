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
  external_user_id?: string // 外部渠道用户ID（OpenAPI 对话端点）
  assignment_reason?: string
  status?: number // 1-正常 0-无效
  created_at?: string
  updated_at?: string
  // 注：Go 侧 TenantID / VisitorKey 标了 json:"-"，**从不上线**（P0-7 防 /customers 整体拖走访客密钥）。
  // 此前这里声明过这两个字段，属于纯虚构口径，已删（2026-09-23 批五 E 契约批）。
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
  // §八-6 C 块(2026-09-18)：待接管标记——/advisor/customers 现未下发该列（Conversation 上有声明），
  // 前端仅当数据真带 true 时渲染徽标；后端补齐列表列后自动生效，勿在前端造值
  pending_handoff?: boolean
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

// 邀请信息（advisor/referral/info 的 referral 子对象）
export interface ReferralInfo {
  invite_code?: string
  invited_count?: number
  paid_count?: number
  free_token_balance?: number
  token_balance?: number
}

// 单条邀请记录（advisor/referral/records）
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
// 包质量 / 通道接入（T7 codegen 锚点类型）
// ============================================================

// PackStatRow 行业包统计行：按租户维度的包使用与效果指标。
export interface PackStatRow {
  tenant_id?: number
  pack_code: string
  pack_version: string
  template_id: string
  sample_count: number
  hook_rate: number
  lead_rate: number
  pending_human_rate: number
  // 终局率（批五 A，2026-09-23）：到店/成交率，由归因行 arrived_at/dealt_at 回填后聚合
  arrive_rate: number
  deal_rate: number
  avg_intent_delta: number
  avg_eval_score?: number
}

/** 判优层行动建议卡片（批五 C·L1）：status=建议下线改稿/领先/观察/样本不足/无对照 */
export interface PackSuggestionRow {
  tenant_id: number
  pack_code: string
  pack_version: string
  template_id: string
  anchor_type: number
  status: 'suggest_review' | 'leading' | 'keep_watching' | 'insufficient_samples' | 'no_peer'
  sample_count: number
  reward_rate: number
  anchor_median_rate: number
  metric: string
  min_samples: number
  confidence: number
  reason: string
}

/** 包质量统计响应：list 为包/模板维度效果行，sample_min 为出数门槛，total_samples 为总样本量 */
export interface PackStatsResp {
  list: PackStatRow[]
  sample_min: number
  total_samples?: number
  // 判优建议卡片（批五 C·L1）：样本不足时仅回显 insufficient_samples，不出判优结论
  suggestions?: PackSuggestionRow[]
}

/** 通道接入视图（F11）：凭据仅回显掩码列，明文只在创建响应中出现一次 */
export interface ChannelView {
  id: number
  type: string
  name: string
  corpid: string
  appid: string
  status: string
  department_id: number
  secret_mask: string
  token_mask: string
  aeskey_mask: string
  config_json: string
  created_at: string
}

/** 通道列表响应信封 */
export interface ChannelListResp {
  list: ChannelView[]
}

/** 创建通道响应：plaintext 为凭据明文一次性回显，callback_url 为微信回调地址 */
export interface CreateChannelResp {
  channel: ChannelView
  plaintext?: { secret?: string; token?: string; encoding_aes_key?: string }
  callback_url?: string
}

/** 通道出站消息视图：status 走 pending/sent/dead 状态机，retries+next_retry_at 驱动指数退避 */
export interface OutboundView {
  id: number
  tenant_id: number
  channel_id: number
  customer_id: number
  conversation_id: number
  content: string
  msg_type: string
  status: string
  retries: number
  next_retry_at?: string | null
  error: string
  sent_at?: string | null
  created_at: string
  updated_at: string
}

/** 出站消息列表响应信封 */
export interface OutboundListResp {
  list: OutboundView[]
}

// ============================================================
// 会话存档（E8，2026-09-24）：企微「会话内容存档」的配置、同步与留痕查询
// 口径三条：密钥材料永不出接口（只给公钥与指纹）；列表只给摘要，全文走详情；
// SDK 未接入是「待办引导」而不是失败弹窗，故以稳定原因码表达。
// ============================================================

/** 单通道存档状态：enabled/key_configured 是「配了没」，fetcher_ready 是「取数接了没」，
 *  三个 total 让管理员一眼看出「一条没有」是还没开、没接 SDK、还是全都解不开 */
export interface ArchiveStatus {
  enabled: boolean
  key_configured: boolean
  secret_configured: boolean
  public_key_ver: number
  /** 当前私钥对应公钥的 SHA-1 指纹（冒号分隔），与企微后台显示的那把比对用 */
  fingerprint: string
  /** 拉取游标：只单调前移，回退等于把历史重抄一遍 */
  cursor_seq: number
  /** false=官方 C SDK 未编入（正常，等接入），不是链路故障 */
  fetcher_ready: boolean
  last_msg_at?: string | null
  stored_total: number
  /** decrypt_error 非空行数：钥匙与后台公钥版本不符时会在这里体现 */
  failed_total: number
  /** 加解密公钥版本与当前配置不符的行数（可读，但该改 public_key_ver） */
  ver_mismatch_total: number
  /** 取数能力缺口的稳定原因码（""=已接入），值同 archive_sdk_not_built */
  sdk_reason: string
}

/** GET / PUT /admin/channels/:id/archive 出参 */
export interface ArchiveStatusResp {
  status: ArchiveStatus
}

/** POST /admin/channels/:id/archive/key 出参：**只有公钥**，private_key_echo 恒 false */
export interface ArchiveKeyResp {
  public_key_pem: string
  fingerprint: string
  public_key_ver: number
  private_key_echo: boolean
}

/** 一轮同步的计数（fetched/stored/dup_skipped/stale_skipped/decrypt_failed 互不重叠） */
export interface ArchiveIngestResult {
  fetched: number
  stored: number
  dup_skipped: number
  stale_skipped: number
  decrypt_failed: number
  max_seq: number
}

/** POST /admin/channels/:id/archive/sync 出参：ok=false 时 reason 给稳定码
 *  （archive_disabled / archive_sdk_not_built / archive_key_missing / archive_secret_rekey_required） */
export interface ArchiveSyncResp {
  ok: boolean
  reason: string
  result: ArchiveIngestResult
}

/** 存档名单一行：**不含 content_text**，正文降为 120 字摘要（全文走详情接口，读一次留一条审计） */
export interface ArchiveRecordRow {
  id: number
  channel_id: number
  msgid: string
  seq: number
  public_key_ver: number
  biz_type: string
  action: string
  from_user: string
  sender_name: string
  chat_type: string
  chatid: string
  msg_type: string
  media_id: string
  media_status: string
  /** 非空即这条只有信封没有正文（解不开也留痕，否则游标卡死） */
  decrypt_error: string
  msg_time?: string | null
  text_preview: string
  has_full_text: boolean
}

/** GET /admin/channels/:id/archive/records 出参：page_size 回显实际生效值（硬顶 100） */
export interface ArchiveRecordListResp {
  list: ArchiveRecordRow[]
  total: number
  page: number
  page_size: number
  page_size_cap: number
  note: string
}

/** GET /admin/channel-archive/records/:id 出参：详情含正文全文与 to_list 原始 JSON */
export interface ArchiveRecordDetailResp {
  record: ArchiveRecordRow & { content_text: string; to_list: string }
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

// ============================================================
// 契约覆盖补充类型（P2-8c：让 api.d.ts 的 ApiRoutes 映射对高频端点生效）
// ============================================================

// 组织架构-部门树节点（org/departments/tree 返回，递归结构）
export interface DeptNode {
  id: number
  name: string
  user_count?: number
  children?: DeptNode[]
}

// 系统配置项（admin/config 列表项；对齐 admin/shared.ts 的 Cfg 结构）
export interface AdminConfigItem {
  key: string
  value: string
  value_type?: string
  category?: string
  description?: string
}

// AI 接待策略模板（strategy/templates 列表项）
// 批五 E 契约批（2026-09-23）改写：原声明 `id: number` 与后端 model.Template 的
// **字符串主键**（`ID string gorm:"primaryKey;size:50"`）对不上，且缺 anchor_type /
// prompt_template / ab_group / ab_weight 等实际下发字段——按 model.Template 的 json tag 全量重写。
export interface TalkTemplate {
  id: string
  tenant_id?: number
  anchor_type: number // 0-6 锚类型
  sub_type?: string // 对比锚子类型 spec/service
  name: string
  category?: string
  trigger_tags?: string // JSON 数组字符串
  required_tags?: string
  min_intent?: number
  max_intent?: number
  applicable_models?: string
  prompt_template: string // 抛话术
  hook_template?: string // 钩话术
  hook_fields?: string
  required_features?: string
  ab_group?: string // E4 实验组名，空=不参与实验
  ab_weight?: number // 组内分流权重 0-100
  department_id?: number | null // null=租户级；非空=部门专属
  priority?: number
  usage_count?: number
  status?: number // 1-启用 0-禁用 2-草稿（E4）
  version?: string
  created_at?: string
  updated_at?: string
}

// 卖点（features 表，对齐 model.Feature json tag）
export interface SellingFeature {
  id: string
  tenant_id?: number
  feature_name: string
  category?: string
  desc_template: string
  short_desc?: string
  params?: string // JSON 对象字符串
  applicable_tags?: string
  applicable_models?: string
  department_id?: number | null
  priority?: number
  status?: number
  created_at?: string
  updated_at?: string
}

// 锚类型统计项（schema.AnchorStats，strategy/stats/anchors 列表元素）
export interface AnchorStat {
  anchor_type: number
  anchor_name: string
  usage_count: number
  hook_rate: number // 该锚消息占全部 AI 消息的比例（近似值）
  conversion_rate: number // 当前实现未填，恒 0，保留字段与后端结构一致
}

// 工作台概览（schema.StatsOverview，stats/overview）
export interface StatsOverview {
  total_customers: number
  new_customers_today: number
  active_conversations: number
  conversion_rate: number
  avg_intent_score: number
  human_transfer_rate: number
}

// AI 贡献度（schema.AIContribution，stats/ai-contribution，D2）。
// Go 侧字段无 omitempty，故线上恒为全字段必现——这里按必填声明，
// 页面若拿到缺字段即编译期暴露，而不是运行时 undefined 静默渲染。
export interface AIContribution {
  period_days: number
  since: string
  until: string
  new_conversations: number
  active_conversations: number
  ai_served_customers: number
  human_served_customers: number
  ai_serve_share: number
  handoff_rate: number
  ai_leads: number
  assisted_leads: number
  ai_lead_rate: number
  assisted_lead_rate: number
  ai_arrived: number
  ai_ordered: number
  ai_arrive_rate: number
  ai_order_rate: number
  ai_messages: number
  human_messages: number
  customer_messages: number
  ai_message_share: number
  pending_handoff_now: number
  notes: string[]
}

// 可下钻指标定义（schema.ContributionMetricDefinition，随下钻响应 metrics 字段回带）。
export interface ContributionMetricOption {
  metric: string
  label: string
}

// 下钻名单一行（schema.AIContributionCustomerRow，stats/ai-contribution/customers，D4）。
export interface AIContributionCustomerRow {
  id: number
  name: string
  phone: string
  journey_stage: string
  intent_score: number
  interest_model: string
  assigned_user_id: number
  assigned_user_name: string
  served_by: string // ai=AI 独立接待 / human=人工参与
}

// AI 贡献度下钻响应（schema.AIContributionDrillResp，D4）。
// total 与看板卡片上的数字同一判据算出，list 是"此刻还能看到的明细"，两者可不等
// （客户被注销或落在本人数据范围外时少一行），前端如实分列展示，不做补齐。
export interface AIContributionDrillResp {
  metric: string
  label: string
  period_days: number
  since: string
  until: string
  total: number
  page: number
  page_size: number
  metrics: ContributionMetricOption[]
  note: string
  list: AIContributionCustomerRow[]
}

// 看板 → 客户列表的下钻预设（D4）。
// 刻意只带 metric 与 days 两个字段：归属/阶段判定全在后端那一份谓词里，
// 前端若再传"补充条件"，就变成前后端各持一套判据——名单与卡片数字必然对不上。
export interface ContributionDrill {
  metric: string
  days: number
}

// 流程实例（model.FlowInstance，flows/instances 与 start/advance 响应）
export interface FlowInstance {
  id: number
  tenant_id?: number
  flow_def_id: number
  conversation_id?: number
  customer_id?: number
  current_node_id?: string
  status?: string // running/completed/suspended
  state_json?: string
  started_at?: string
  ended_at?: string | null
  created_at?: string
  updated_at?: string
}

// 试驾单（model.TestDrive，advisor/test-drives）
export interface TestDriveRow {
  id: number
  tenant_id?: number
  customer_id: number
  advisor_id: number
  status?: string // pending/completed/cancelled
  scheduled_at?: string
  model_name?: string
  contact_name?: string
  contact_phone?: string
  location?: string
  note?: string
  result?: string
  created_at?: string
  updated_at?: string
}

// 跟进记录（model.FollowUp，advisor/followups 与创建响应）
export interface FollowUpRow {
  id: number
  tenant_id?: number
  customer_id: number
  conversation_id?: number
  user_id: number
  type?: string // manual/ai_triggered
  method?: string // phone/wechat/store/email
  content?: string
  result?: string
  next_follow_at?: string | null
  intent_change?: number
  created_at?: string
  updated_at?: string
}

// 客户标签关联（model.CustomerTag，advisor/customer/:id/tags 与 customers/:id/tags）
export interface CustomerTagRow {
  id: number
  tenant_id?: number
  customer_id: number
  tag_id: number
  tag_name?: string
  source?: string // manual/auto/ai
  weight?: number
  expire_at?: string
  created_at?: string
}

// 顾问统计卡（advisor/stats 内联 statItem：后端匿名结构体）
// color 为必填：Go 侧六项 statItem 全部显式设色（blue/green/purple/orange/red），无一留空。
export interface AdvisorStatItem {
  label: string
  value: number
  color: string
}

// 顾问下拉项（advisor/list 内联 advisorItem）
export interface AdvisorRef {
  id: number
  real_name?: string
  username?: string
}

// 顾问切换 AI 回复的结果（advisor/chat/toggle-ai-reply：后端 gin.H 三键，非整行会话）
export interface AiReplyToggle {
  is_ai_reply_enabled: boolean
  mode: string // ai/human/fish
  is_human_locked: boolean
}

// 打标接口回传的标签名列表（customers/:id/tags）。
// 单独命名而非直接写 string[]：gen_api_types 用标识符正则从 data_ts 抽导入名，
// 小写 `string` 会被当成待导入类型、生成物随即编译失败。
export type TagNames = string[]

// 顾问客户列表行（advisor/customers：model.Customer 展开 + 后端附加列）
export interface AdvisorCustomerRow extends Customer {
  last_message?: string // 超 50 字后端已截断加省略号
  conv_mode?: string // ai/human
  lead_status?: string // journey_stage 中文名
  lead_sub_status?: string // 已试驾/已报价/空
  assigned_user_name?: string
  last_message_at?: string
}

// OutreachTaskRow 主动触达任务行（GET/POST /admin/outreach/tasks 出参，字段与
// internal/api/outreach_admin.go 的 outreachTaskView 一一对应）。
// status/reason 的取值是后端稳定字面量（internal/model/outreach.go），前端只按它出文案。
export interface OutreachTaskRow {
  id: number
  customer_id: number
  customer_name: string // 后端已按本页客户批量补齐，前端不再逐行二次查询
  content: string
  scheduled_at: string
  status: string // pending/queued/sent/skipped/failed/cancelled
  reason: string // 稳定原因码（被拦下/失败时非空）
  error: string
  channel_id: number
  outbound_id: number
  attempts: number
  created_by: number
  sent_at: string | null
  created_at: string
}

// OutreachConfig 本租户生效的触达参数（租户覆盖 > 系统默认，随列表一起回吐，
// 前端据此显示"开关未开/静默时段"提示，不再自己猜默认值）。
export interface OutreachConfig {
  enabled: boolean
  weekly_limit: number
  quiet_hours: string
  window_hours: number
}

// OutreachListResp 触达队列分页响应（list/total/config 三段）。
export interface OutreachListResp {
  list: OutreachTaskRow[]
  total: number
  config: OutreachConfig
}

// OutreachCancelResp 撤回成功的回显（仅 pending 可撤，撤不动一律 404 不回显原因）。
export interface OutreachCancelResp {
  id: number
}

// ============================================================
// D3 用量预警 + 到期催缴（2026-09-23）
// 字段与 internal/billing/{usage_alert,dunning}.go 的视图结构体一一对应；
// metric/status 是后端稳定字面量，前端只按它出文案，不自造第二套枚举。
// ============================================================

// UsageAlertConfig 额度预警的平台生效口径（档位与水位都是平台级，租户只读）
export interface UsageAlertConfig {
  enabled: boolean
  thresholds: number[] // 已用百分比分档，升序（如 [80,95,100]）
  token_balance_below: number // 预充值余额低于该绝对值才提示（此档无分母）
  group_ready: boolean // 平台群通道是否已配（配了才有人真在盯）
  email_ready: boolean // SMTP 是否已配
}

// UsageProgressRow 单项指标当前进度；unlimited=true 表示这项没有分母，不画进度条
export interface UsageProgressRow {
  metric: string // monthly_calls/monthly_tokens/token_balance
  label: string
  used: number
  max: number // 0=不限额
  pct: number // 已用百分比（上限钳 100）
  remaining: number
  unlimited: boolean
  warn_below: number // 绝对水位预警线（仅余额档非 0）
}

// UsageAlertRow 一条额度预警留痕（="某账期某档已经通知过你"）
export interface UsageAlertRow {
  metric: string
  threshold: number
  period_key: string // 'YYYY-MM' 账期锚
  usage_pct: number
  remaining: number
  channels: string // email/group 逗号串；空串=没有可用收件人
  created_at: string
}

// AdminUsageAlertsResp GET /admin/usage/alerts 出参（进度 + 口径 + 留痕三段）
export interface AdminUsageAlertsResp {
  period_key: string
  config: UsageAlertConfig
  progress: UsageProgressRow[]
  list: UsageAlertRow[]
}

// DunningConfig 催缴生效口径（第几天各催一次、第几天停用）
export interface DunningConfig {
  enabled: boolean
  steps: number[] // 到期后第 N 天各发一次催缴
  suspend_after_days: number // 0=只催不封
}

// TenantDunningView 本租户催缴进度（exists=false 表示从未欠费，不是错误）
export interface TenantDunningView {
  exists: boolean
  status: string // running/resolved/exhausted
  stage: number // 已发到的档位序号
  total_stages: number
  day_past: number // 到期后第几天
  due_at: string | null
  grace_end: string | null
  suspended: boolean
}

// AdminDunningResp GET /admin/billing/dunning 出参
export interface AdminDunningResp {
  dunning: TenantDunningView
  config: DunningConfig
}

// DunningRow 超管催缴队列一行（一家租户当前这一轮走到哪）
export interface DunningRow {
  tenant_id: number
  tenant_name: string
  code: string
  tenant_status: string // active/trial/expired/suspended/...
  status: string // running/resolved/exhausted
  stage: number
  day_past: number
  due_at: string | null
  next_notify_at: string | null
  last_notified_at: string | null
  suspended: boolean
  grace_end: string | null
  sent_to: string // 脱敏收件人（a***@b.com，最多 3 个）
}

// SuperDunningQueueResp GET /super/dunning 出参（队列 + 两份平台口径）
export interface SuperDunningQueueResp {
  list: DunningRow[]
  config: DunningConfig
  usage_alert_config: UsageAlertConfig
}

// SuperDunningActionResult 人工催缴动作回显（档位是否前进由状态机决定，接口不回推进度）
export interface SuperDunningActionResult {
  tenant_id: number
}

// ============================================================
// 获客活码（2026-09-23 获客批）
// 字段与 internal/api/acquisition_admin.go / acquisition_public.go 的出参一一对应；
// channel/status/metric 都是后端稳定字面量（internal/acquisition/policy.go），
// 前端只按它出文案与开关，不自造第二套枚举——渠道加一个值就要"下拉能选、提交报错"了。
// ============================================================

/** 漏斗五格数字（键为后端指标码；后端保证键齐，缺格即契约破坏） */
export interface AcqFunnel {
  new: number
  spoke: number
  lead: number
  arrived: number
  ordered: number
}

/** 活码列表一行：配置 + 落地链接 + 窗口内数字（link 由后端三级基址算出，前端不拼） */
export interface AcqCodeRow {
  id: number
  code: string
  name: string
  channel: string
  status: string // active/disabled
  remark: string
  owner_user_id: number
  created_at: string
  link: string
  scans: number // 扫码次数（事件级，刻意不可下钻）
  funnel: AcqFunnel
}

/** 展示口径（随列表下发：渠道枚举、可下钻指标、指标中文名、基址是否配好） */
export interface AcqConfig {
  channels: string[]
  metrics: string[]
  labels: Record<string, string>
  link_base_configured: boolean
}

/** GET /admin/acquisition/codes 出参 */
export interface AcqCodeListResp {
  list: AcqCodeRow[]
  total: number
  days: number
  config: AcqConfig
}

/** POST /admin/acquisition/codes/:id/status 回显 */
export interface AcqStatusResp {
  id: number
  status: string
}

/** 下钻名单一行（字段与 /customers 对齐，phone 已由后端按读权限处理） */
export interface AcqCustomerRow {
  id: number
  name: string
  phone: string
  journey_stage: string
  intent_score: number
  spoke: boolean
  created_at: string
}

/** GET /admin/acquisition/codes/:id/customers 出参（total 恒等于卡片数字） */
export interface AcqDrillResp {
  metric: string
  label: string
  total: number
  list: AcqCustomerRow[]
  note: string
  page: number
  page_size: number
}

/** GET /api/v1/acquisition/:code 出参（落地页自检，只回渲染所需最小集合） */
export interface AcqResolveResp {
  code: string
  channel: string
  landing_path: string
}

/** POST /api/v1/acquisition/:code/scan 出参（counted=false 是去重命中，不是错误） */
export interface AcqScanResp {
  counted: boolean
}

// ============================================================
// 商机与报价（商机批，2026-09-23 · 批次2）
// 字段与 internal/api/deal_admin.go / deal_advisor.go 的出参一一对应。
// 阶段码 / 过滤码 / 流失原因 / 报价状态都是后端稳定字面量（internal/deal/policy.go +
// internal/model/deal.go），**中文口径名一律随 config 下发**：前端写第二套枚举，
// 加一个阶段就会出现"看板格子叫得出名字、下钻标题对不上"。
// 金额一律**分**（int64）：报价是钱，前端展示才除 100，任何中间步骤都不做浮点往返。
// ============================================================

/** 看板一个阶段格子（drill_filter 直接拿去下钻，前端不自己拼字符串） */
export interface DealStageCell {
  stage: string
  stage_name: string
  count: number
  amount_cents: number
  drill_filter: string
}

/** 看板与报价展示的口径集合（枚举 + 中文名 + 入参上限） */
export interface DealConfig {
  stages: string[]
  stage_names: Record<string, string>
  filters: string[]
  filter_labels: Record<string, string>
  lost_reasons: Record<string, string>
  quote_statuses: Record<string, string>
  sources: string[]
  max_amount_cents: number
  max_quote_lines: number
  default_stuck_days: number
}

/** GET /admin/deals/board 出参：六格 + 在途/停滞/赢单三位 + 赢单率（分母只算终局单） */
export interface DealBoardResp {
  days: number
  stuck_days: number
  stages: DealStageCell[]
  open_count: number
  open_amount_cents: number
  stuck_count: number
  stuck_amount_cents: number
  won_count: number
  won_amount_cents: number
  lost_count: number
  total_count: number
  win_rate_pct: number
  /** true=单量超出后端一次扫描上限，数字比真实值小（如实标注，不静默少算） */
  truncated: boolean
  note: string
  config: DealConfig
}

/** 报价单摘要（列表内嵌；明细行只在详情给） */
export interface DealQuoteSummary {
  id: number
  version: number
  status: string
  status_name: string
  total_cents: number
  valid_until: string
  sent_at: string
  decided_at: string
  created_at: string
}

/** 商机一行（列表与详情同形，归属/客户名由后端 join 到位） */
export interface DealRow {
  id: number
  customer_id: number
  customer_name: string
  customer_phone: string
  customer_journey_stage: string
  owner_user_id: number
  owner_name: string
  title: string
  stage: string
  stage_name: string
  stage_entered_at: string
  stalled_days: number
  amount_cents: number
  source: string
  source_code: string
  expected_close_at: string
  won_at: string
  lost_at: string
  lost_reason: string
  lost_reason_name: string
  quote_count: number
  /** 当前那张活着的报价（草稿或已发出）；无则 null */
  open_quote: DealQuoteSummary | null
  created_at: string
}

/** GET /admin/deals?filter= 出参（total 恒等于看板格子数字，两侧同源） */
export interface DealDrillResp {
  filter: string
  label: string
  days: number
  stuck_days: number
  total: number
  list: DealRow[]
  truncated: boolean
  note: string
  page: number
  page_size: number
}

/** 报价单详情（明细行合计由服务端算，前端传来的合计不作数） */
export interface DealQuoteDetail extends DealQuoteSummary {
  opportunity_id: number
  lines: { name: string; qty: number; unit_cents: number; total_cents: number }[]
  note: string
  created_by: number
  updated_at: string
}

/** GET /admin/deals/:id 与全部写操作的回读形态（写完立刻回读真实落库结果） */
export interface DealDetailResp {
  deal: DealRow
  quotes: DealQuoteSummary[]
  config: DealConfig
}

/** 报价动作（发出/接受/拒绝/作废/建版）统一回一张单的最新形态 */
export interface DealQuoteResp {
  quote: DealQuoteDetail
}

/** GET /admin/deals/:id/quotes 出参（version 倒序，含历史与作废） */
export interface DealQuoteListResp {
  list: DealQuoteSummary[]
  total: number
}

/** GET /advisor/customer/:id/deals 出参（含终局单：顾问问的是"跟过几单"） */
export interface AdvisorDealListResp {
  customer_id: number
  list: DealRow[]
  total: number
  config: DealConfig
}

// ============================================================
// E1 企微侧边栏应用级签名（2026-09-24）
// 字段与 internal/channel.AgentCfgResult 的 json tag 一一对应。
// 全部是字符串：agentid 走字符串下发，JS 侧数字会丢精度，而签名必须与它逐字配套。
// ============================================================

/** GET /channel/wecom/agentconfig 出参，直接摊进 wx.agentConfig({...}) */
export interface WecomAgentConfigResp {
  corpid: string
  agentid: string
  timestamp: string
  noncestr: string
  signature: string
}
