-- 009 行业包质量归因（D9，2026-09-13）
-- reply_attributions：AI 回复生效包/模板快照与后续事件回填。
-- 与 GORM AutoMigrate 幂等共存（IF NOT EXISTS）。
BEGIN;

CREATE TABLE IF NOT EXISTS reply_attributions (
    id              BIGSERIAL PRIMARY KEY,
    tenant_id       BIGINT NOT NULL,
    message_id      BIGINT NOT NULL UNIQUE,
    conversation_id BIGINT,
    customer_id     BIGINT,
    pack_code       VARCHAR(32),
    pack_version    VARCHAR(20),
    template_id     VARCHAR(50),
    anchor_type     INT DEFAULT 0,
    route_result    VARCHAR(50),
    provider        VARCHAR(30),
    model           VARCHAR(80),
    intent_before   DOUBLE PRECISION DEFAULT 0,
    intent_after    DOUBLE PRECISION DEFAULT 0,
    hooked          BOOLEAN DEFAULT false,
    lead_captured   BOOLEAN DEFAULT false,
    pending_human   BOOLEAN DEFAULT false,
    created_at      TIMESTAMPTZ,
    updated_at      TIMESTAMPTZ
);

-- AutoMigrate 可能先按旧字段名建出 model_name；统一收敛到 model 列。
ALTER TABLE reply_attributions ADD COLUMN IF NOT EXISTS model VARCHAR(80);
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = CURRENT_SCHEMA() AND table_name = 'reply_attributions' AND column_name = 'model_name'
    ) THEN
        EXECUTE 'UPDATE reply_attributions SET model = COALESCE(model, model_name) WHERE model IS NULL';
        EXECUTE 'ALTER TABLE reply_attributions DROP COLUMN model_name';
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_ra_tenant_pack_tpl ON reply_attributions (tenant_id, pack_code, template_id);
CREATE INDEX IF NOT EXISTS idx_ra_pack_version ON reply_attributions (pack_code, pack_version);
CREATE INDEX IF NOT EXISTS idx_ra_customer ON reply_attributions (customer_id);
CREATE INDEX IF NOT EXISTS idx_ra_conversation ON reply_attributions (conversation_id);
CREATE INDEX IF NOT EXISTS idx_ra_created ON reply_attributions (created_at);

COMMIT;
