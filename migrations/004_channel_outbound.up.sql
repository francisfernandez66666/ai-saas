-- 004 通道出站队列表（W6，2026-09-12）
-- 把"回复产生"与"通道发送"解耦：AI/人工回复落库后投递本表，taskrunner 批量发送 + 指数退避重试，
-- 超过 5 次进 failed 死信（/admin 通道页可见，可人工重发）。
-- 与 GORM AutoMigrate 幂等共存（IF NOT EXISTS）。
BEGIN;

CREATE TABLE IF NOT EXISTS channel_outbound (
    id              BIGSERIAL PRIMARY KEY,
    tenant_id       BIGINT NOT NULL,
    channel_id      BIGINT NOT NULL,
    customer_id     BIGINT,
    conversation_id BIGINT,
    content         TEXT,
    msg_type        VARCHAR(20) DEFAULT 'text',
    status          VARCHAR(20) DEFAULT 'pending',   -- pending|sent|failed
    retries         INT DEFAULT 0,
    next_retry_at   TIMESTAMPTZ,
    error           VARCHAR(500),
    sent_at         TIMESTAMPTZ,
    created_at      TIMESTAMPTZ,
    updated_at      TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_out_tenant ON channel_outbound (tenant_id);
CREATE INDEX IF NOT EXISTS idx_out_channel ON channel_outbound (channel_id);
CREATE INDEX IF NOT EXISTS idx_out_conversation ON channel_outbound (conversation_id);
CREATE INDEX IF NOT EXISTS idx_out_status ON channel_outbound (status);
-- 扫描器按 (status,next_retry_at) 取到期待发送批次
CREATE INDEX IF NOT EXISTS idx_out_pending_due ON channel_outbound (status, next_retry_at);

COMMIT;
