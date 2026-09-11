-- G-10 基线迁移：当前 AutoMigrate 产出的 schema 快照
-- 用途：新环境部署时作为起点，后续迁移在此基础上递增
-- 执行方式：migrate.go 工具 或 psql -f
-- 创建时间：2026-09-11

BEGIN;

-- ---- 租户 ----
CREATE TABLE IF NOT EXISTS tenants (
    id BIGSERIAL PRIMARY KEY,
    created_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ,
    deleted_at TIMESTAMPTZ,
    name VARCHAR(255),
    code VARCHAR(100) UNIQUE,
    status VARCHAR(20) DEFAULT 'active',
    contact_name VARCHAR(100),
    contact_phone VARCHAR(30),
    contact_email VARCHAR(200),
    domain VARCHAR(200),
    custom_domain VARCHAR(200),
    logo_url TEXT,
    invited_by_tenant_id BIGINT,
    referral_paid_rewarded BOOLEAN DEFAULT FALSE,
    industry VARCHAR(50),
    must_change_password BOOLEAN DEFAULT FALSE,
    token_balance BIGINT DEFAULT 0,
    monthly_token_quota BIGINT DEFAULT 0,
    monthly_token_used BIGINT DEFAULT 0,
    free_token_balance BIGINT DEFAULT 0,
    free_token_expires_at TIMESTAMPTZ,
    usage_reset_at TIMESTAMPTZ,
    plan VARCHAR(50) DEFAULT 'free'
);
CREATE INDEX IF NOT EXISTS idx_tenants_code ON tenants(code) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_tenants_custom_domain ON tenants(custom_domain) WHERE deleted_at IS NULL AND custom_domain != '';
CREATE INDEX IF NOT EXISTS idx_tenants_status ON tenants(status) WHERE deleted_at IS NULL;

-- ---- 租户用户 ----
CREATE TABLE IF NOT EXISTS tenant_users (
    id BIGSERIAL PRIMARY KEY,
    created_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ,
    deleted_at TIMESTAMPTZ,
    tenant_id BIGINT NOT NULL,
    username VARCHAR(100) NOT NULL,
    password_hash VARCHAR(255),
    real_name VARCHAR(100),
    phone VARCHAR(30),
    role VARCHAR(30) DEFAULT 'user',
    department_id BIGINT,
    must_change_password BOOLEAN DEFAULT FALSE,
    avatar_url TEXT,
    UNIQUE(tenant_id, username)
);
CREATE INDEX IF NOT EXISTS idx_tenant_users_tenant ON tenant_users(tenant_id) WHERE deleted_at IS NULL;

-- ---- 部门 ----
CREATE TABLE IF NOT EXISTS departments (
    id BIGSERIAL PRIMARY KEY,
    created_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ,
    deleted_at TIMESTAMPTZ,
    tenant_id BIGINT NOT NULL,
    name VARCHAR(100),
    parent_id BIGINT DEFAULT 0,
    path VARCHAR(255),
    sort_order INT DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_departments_tenant ON departments(tenant_id) WHERE deleted_at IS NULL;

-- ---- 客户 ----
CREATE TABLE IF NOT EXISTS customers (
    id BIGSERIAL PRIMARY KEY,
    created_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ,
    deleted_at TIMESTAMPTZ,
    tenant_id BIGINT NOT NULL,
    name VARCHAR(100),
    phone VARCHAR(30),
    wechat VARCHAR(100),
    source VARCHAR(50),
    status VARCHAR(20) DEFAULT 'active',
    assigned_user_id BIGINT,
    visitor_key VARCHAR(100),
    tags JSONB,
    intent_score FLOAT DEFAULT 0,
    stage VARCHAR(50),
    channel VARCHAR(50),
    device VARCHAR(20),
    cdp_id VARCHAR(100),
    last_active_at TIMESTAMPTZ,
    metadata JSONB
);
CREATE INDEX IF NOT EXISTS idx_customers_tenant ON customers(tenant_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_customers_assigned ON customers(assigned_user_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_customers_visitor_key ON customers(visitor_key) WHERE deleted_at IS NULL AND visitor_key != '';
CREATE INDEX IF NOT EXISTS idx_customers_cdp_id ON customers(tenant_id, cdp_id) WHERE deleted_at IS NULL AND cdp_id != '';

-- ---- 会话 ----
CREATE TABLE IF NOT EXISTS conversations (
    id BIGSERIAL PRIMARY KEY,
    created_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ,
    deleted_at TIMESTAMPTZ,
    tenant_id BIGINT NOT NULL,
    customer_id BIGINT NOT NULL,
    status VARCHAR(20) DEFAULT 'active',
    assigned_user_id BIGINT,
    channel VARCHAR(50),
    session_state JSONB,
    ai_reply_enabled BOOLEAN DEFAULT TRUE,
    last_message_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_conversations_tenant_customer ON conversations(tenant_id, customer_id) WHERE deleted_at IS NULL;

-- ---- 消息 ----
CREATE TABLE IF NOT EXISTS messages (
    id BIGSERIAL PRIMARY KEY,
    created_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ,
    deleted_at TIMESTAMPTZ,
    tenant_id BIGINT NOT NULL,
    conversation_id BIGINT NOT NULL,
    customer_id BIGINT,
    sender_type VARCHAR(20),
    sender_id BIGINT,
    content TEXT,
    message_type VARCHAR(20) DEFAULT 'text',
    metadata JSONB
);
CREATE INDEX IF NOT EXISTS idx_messages_conversation ON messages(conversation_id) WHERE deleted_at IS NULL;

-- ---- 系统配置 ----
CREATE TABLE IF NOT EXISTS system_configs (
    id BIGSERIAL PRIMARY KEY,
    created_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ,
    tenant_id BIGINT DEFAULT 0,
    category VARCHAR(50),
    key VARCHAR(100),
    value TEXT,
    description VARCHAR(500),
    UNIQUE(tenant_id, category, key)
);

-- ---- 策略/话术 ----
CREATE TABLE IF NOT EXISTS strategies (
    id BIGSERIAL PRIMARY KEY,
    created_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ,
    deleted_at TIMESTAMPTZ,
    tenant_id BIGINT NOT NULL,
    name VARCHAR(100),
    anchor_type INT,
    template TEXT,
    is_active BOOLEAN DEFAULT TRUE
);

-- ---- 知识库 ----
CREATE TABLE IF NOT EXISTS knowledge_bases (
    id BIGSERIAL PRIMARY KEY,
    created_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ,
    deleted_at TIMESTAMPTZ,
    tenant_id BIGINT NOT NULL,
    title VARCHAR(200),
    content TEXT,
    category VARCHAR(50),
    is_active BOOLEAN DEFAULT TRUE,
    embedding VECTOR(1024)
);

-- ---- 行业包 ----
CREATE TABLE IF NOT EXISTS industry_packs (
    id BIGSERIAL PRIMARY KEY,
    created_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ,
    deleted_at TIMESTAMPTZ,
    code VARCHAR(100),
    version VARCHAR(20),
    name VARCHAR(200),
    industry VARCHAR(50),
    pack_level VARCHAR(20),
    status VARCHAR(20) DEFAULT 'active',
    pack_data JSONB,
    UNIQUE(code, version)
);

CREATE TABLE IF NOT EXISTS tenant_pack_bindings (
    id BIGSERIAL PRIMARY KEY,
    created_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ,
    tenant_id BIGINT NOT NULL,
    pack_id BIGINT NOT NULL,
    bound_at TIMESTAMPTZ DEFAULT NOW()
);

-- ---- 订单/计费 ----
CREATE TABLE IF NOT EXISTS orders (
    id BIGSERIAL PRIMARY KEY,
    created_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ,
    deleted_at TIMESTAMPTZ,
    tenant_id BIGINT NOT NULL,
    package_id BIGINT,
    amount_micro BIGINT,
    status VARCHAR(20) DEFAULT 'pending',
    pay_mode VARCHAR(20),
    channel VARCHAR(50),
    refund_amount_micro BIGINT DEFAULT 0,
    refunded_at TIMESTAMPTZ,
    metadata JSONB
);

CREATE TABLE IF NOT EXISTS packages (
    id BIGSERIAL PRIMARY KEY,
    created_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ,
    deleted_at TIMESTAMPTZ,
    name VARCHAR(100),
    price_micro BIGINT,
    token_quota BIGINT,
    duration_days INT,
    is_active BOOLEAN DEFAULT TRUE,
    level VARCHAR(20)
);

-- ---- 用量 ----
CREATE TABLE IF NOT EXISTS usage_records (
    id BIGSERIAL PRIMARY KEY,
    created_at TIMESTAMPTZ,
    tenant_id BIGINT NOT NULL,
    date VARCHAR(10),
    metric VARCHAR(50),
    value BIGINT DEFAULT 0,
    UNIQUE(tenant_id, date, metric)
);

CREATE TABLE IF NOT EXISTS usage_ledger (
    id BIGSERIAL PRIMARY KEY,
    created_at TIMESTAMPTZ,
    tenant_id BIGINT NOT NULL,
    request_id VARCHAR(100),
    provider VARCHAR(50),
    model VARCHAR(100),
    prompt_tokens INT,
    completion_tokens INT,
    total_tokens INT,
    cost_micro BIGINT
);

-- ---- 审计日志 ----
CREATE TABLE IF NOT EXISTS tenant_audit_logs (
    id BIGSERIAL PRIMARY KEY,
    created_at TIMESTAMPTZ,
    tenant_id BIGINT NOT NULL,
    user_id BIGINT,
    action VARCHAR(100),
    detail JSONB,
    ip VARCHAR(50)
);

-- ---- API Key ----
CREATE TABLE IF NOT EXISTS api_keys (
    id BIGSERIAL PRIMARY KEY,
    created_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ,
    tenant_id BIGINT NOT NULL,
    key_hash VARCHAR(255),
    key_prefix VARCHAR(20),
    name VARCHAR(100),
    is_active BOOLEAN DEFAULT TRUE,
    api_key_perms JSONB,
    api_calls BIGINT DEFAULT 0,
    last_used_at TIMESTAMPTZ
);

-- ---- CDP ----
CREATE TABLE IF NOT EXISTS cdp_profiles (
    id BIGSERIAL PRIMARY KEY,
    created_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ,
    deleted_at TIMESTAMPTZ,
    tenant_id BIGINT NOT NULL,
    cdp_id VARCHAR(100),
    traits JSONB,
    events JSONB,
    UNIQUE(tenant_id, cdp_id)
);

CREATE TABLE IF NOT EXISTS cdp_tag_definitions (
    id BIGSERIAL PRIMARY KEY,
    created_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ,
    tenant_id BIGINT NOT NULL,
    code VARCHAR(100),
    name VARCHAR(200),
    category VARCHAR(50),
    UNIQUE(tenant_id, code)
);

CREATE TABLE IF NOT EXISTS cdp_tag_assignments (
    id BIGSERIAL PRIMARY KEY,
    created_at TIMESTAMPTZ,
    tenant_id BIGINT NOT NULL,
    profile_id BIGINT,
    tag_id BIGINT,
    assigned_at TIMESTAMPTZ DEFAULT NOW(),
    expired_at TIMESTAMPTZ
);

-- ---- 邮箱验证 ----
CREATE TABLE IF NOT EXISTS email_verifies (
    id BIGSERIAL PRIMARY KEY,
    created_at TIMESTAMPTZ,
    tenant_id BIGINT DEFAULT 0,
    email VARCHAR(200),
    code VARCHAR(10),
    purpose VARCHAR(50),
    expires_at TIMESTAMPTZ,
    used BOOLEAN DEFAULT FALSE
);

-- ---- 邀请 ----
CREATE TABLE IF NOT EXISTS invite_rewards (
    id BIGSERIAL PRIMARY KEY,
    created_at TIMESTAMPTZ,
    tenant_id BIGINT NOT NULL,
    inviter_tenant_id BIGINT,
    invitee_tenant_id BIGINT,
    reward_tokens BIGINT DEFAULT 0,
    status VARCHAR(20) DEFAULT 'pending'
);

COMMIT;
