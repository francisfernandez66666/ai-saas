-- 007 出站事件 webhook（D6，2026-09-12）
-- tenant_webhooks：订阅配置；webhook_deliveries：投递队列（含重试/死信审计）。
-- 与 GORM AutoMigrate 幂等共存（IF NOT EXISTS）。
BEGIN;

CREATE TABLE IF NOT EXISTS tenant_webhooks (
    id          BIGSERIAL PRIMARY KEY,
    tenant_id   BIGINT NOT NULL,
    name        VARCHAR(60),
    url         VARCHAR(300) NOT NULL,
    secret      VARCHAR(200),
    events      VARCHAR(300),
    active      BOOLEAN DEFAULT true,
    fail_count  INT DEFAULT 0,
    disabled_at TIMESTAMPTZ,
    created_at  TIMESTAMPTZ,
    updated_at  TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_wh_tenant ON tenant_webhooks (tenant_id);
CREATE INDEX IF NOT EXISTS idx_wh_active ON tenant_webhooks (active);

CREATE TABLE IF NOT EXISTS webhook_deliveries (
    id            BIGSERIAL PRIMARY KEY,
    tenant_id     BIGINT NOT NULL,
    webhook_id    BIGINT NOT NULL,
    event         VARCHAR(40) NOT NULL,
    payload       TEXT,
    status        VARCHAR(20) DEFAULT 'pending',   -- pending|delivered|dead
    attempts      INT DEFAULT 0,
    next_retry_at TIMESTAMPTZ,
    last_error    VARCHAR(300),
    delivered_at  TIMESTAMPTZ,
    created_at    TIMESTAMPTZ,
    updated_at    TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_wd_tenant ON webhook_deliveries (tenant_id);
CREATE INDEX IF NOT EXISTS idx_wd_webhook ON webhook_deliveries (webhook_id);
CREATE INDEX IF NOT EXISTS idx_wd_pending_due ON webhook_deliveries (status, next_retry_at);

COMMIT;
