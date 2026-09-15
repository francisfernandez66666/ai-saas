// Admin 后台共享类型与菜单常量（F1：从巨型 Admin.tsx 拆出，避免页面级重复定义）

export type Cfg = {
  key: string
  category: string
  value: string
  value_type: 'number' | 'bool' | 'string' | 'json'
  description?: string
  default_value?: string
}

export type MenuItemDef = { k: string; label: string; link?: string }

export type MenuGroupDef = { title: string; items: MenuItemDef[] }

export const MENU_GROUPS: MenuGroupDef[] = [
  {
    title: '店端运营',
    items: [
      { k: 'dashboard', label: '工作台' },
      { k: 'customers', label: '客户线索' },
      { k: 'cdp', label: 'CDP画像' },
      { k: 'knowledge', label: '知识库' },
      { k: 'tenant_kb', label: '租户资料' },
      { k: 'advisor', label: '顾问工作台', link: '/advisor' },
      { k: 'billing', label: '收银台', link: '/billing' },
    ],
  },
  {
    title: '系统管理',
    items: [
      { k: 'reply_speed', label: '回复速度' },
      { k: 'strategy', label: '策略配置' },
      { k: 'strategy_templates', label: '策略模板' },
      { k: 'strategy_test', label: '策略试运行' },
      { k: 'mental_stage', label: '心智阶段' },
      { k: 'ai_chain', label: 'AI链路' },
      { k: 'commercial', label: '商业化' },
      { k: 'notify', label: '触达通知' },
      { k: 'tags', label: '标签体系' },
      { k: 'flow_engine', label: '流程引擎' },
      { k: 'industry_packs', label: '行业包' },
      { k: 'org', label: '组织架构', link: '/org' },
      { k: 'channels', label: '通道接入' },
      { k: 'openapi', label: '开放平台' },
      { k: 'webhooks', label: 'Webhook' },
      { k: 'usage', label: '用量' },
      { k: 'referral', label: '邀请推广' },
      { k: 'branding', label: '品牌定制' },
      { k: 'privacy', label: '隐私删除请求' },
      { k: 'audit', label: '审计日志' }
    ],
  },
]

export const CONFIG_CATS = ['reply_speed', 'strategy', 'mental_stage', 'ai_chain', 'billing', 'notify']
