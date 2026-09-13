-- 010 包质量统计快照与离线评分回填（D9，2026-09-13）
BEGIN;

ALTER TABLE reply_attributions ADD COLUMN IF NOT EXISTS eval_score INT DEFAULT -1;
ALTER TABLE reply_attributions ADD COLUMN IF NOT EXISTS eval_checked_at TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS idx_ra_eval_pending ON reply_attributions (id) WHERE template_id <> '' AND eval_score < 0;

CREATE TABLE IF NOT EXISTS pack_stats (
    id                 BIGSERIAL PRIMARY KEY,
    tenant_id          BIGINT NOT NULL,
    pack_code          VARCHAR(32) NOT NULL,
    pack_version       VARCHAR(20) NOT NULL,
    template_id        VARCHAR(50) NOT NULL,
    sample_count       BIGINT DEFAULT 0,
    hook_rate          DOUBLE PRECISION DEFAULT 0,
    lead_rate          DOUBLE PRECISION DEFAULT 0,
    pending_human_rate DOUBLE PRECISION DEFAULT 0,
    avg_intent_delta   DOUBLE PRECISION DEFAULT 0,
    avg_eval_score     DOUBLE PRECISION,
    computed_at        TIMESTAMPTZ,
    UNIQUE (tenant_id, pack_code, pack_version, template_id)
);

CREATE INDEX IF NOT EXISTS idx_pack_stats_tenant_pack ON pack_stats (tenant_id, pack_code, template_id);
CREATE INDEX IF NOT EXISTS idx_pack_stats_pack_version ON pack_stats (pack_code, pack_version);
CREATE INDEX IF NOT EXISTS idx_pack_stats_computed ON pack_stats (computed_at);

COMMIT;
