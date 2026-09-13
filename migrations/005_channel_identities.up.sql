-- 005 渠道客户标识映射表（W2 OneID 桥，2026-09-12）
-- 入站：按 (channel_id, external_id) 定位/新建站内客户；出站：据此反查 external_id 回发渠道。
-- 与 GORM AutoMigrate 幂等共存（IF NOT EXISTS）。
BEGIN;

CREATE TABLE IF NOT EXISTS channel_identities (
    id          BIGSERIAL PRIMARY KEY,
    tenant_id   BIGINT NOT NULL,
    channel_id  BIGINT NOT NULL,
    customer_id BIGINT NOT NULL,
    external_id VARCHAR(128) NOT NULL,
    staff_id    VARCHAR(128),
    created_at  TIMESTAMPTZ,
    updated_at  TIMESTAMPTZ
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_chan_identity ON channel_identities (channel_id, external_id);
CREATE INDEX IF NOT EXISTS idx_chan_identity_customer ON channel_identities (customer_id);
CREATE INDEX IF NOT EXISTS idx_chan_identity_tenant ON channel_identities (tenant_id);

COMMIT;
