-- 006 个人数据删除请求表（C2 PIPL 删除权，2026-09-12）
-- 与 GORM AutoMigrate 幂等共存（IF NOT EXISTS）。
BEGIN;

CREATE TABLE IF NOT EXISTS deletion_requests (
    id           BIGSERIAL PRIMARY KEY,
    tenant_id    BIGINT NOT NULL,
    scope        VARCHAR(20) NOT NULL,          -- customer|user
    customer_id  BIGINT NOT NULL DEFAULT 0,
    user_id      BIGINT NOT NULL DEFAULT 0,
    status       VARCHAR(20) NOT NULL DEFAULT 'pending',  -- pending|anonymized|failed
    requested_at TIMESTAMPTZ,
    deadline     TIMESTAMPTZ,
    processed_at TIMESTAMPTZ,
    error        VARCHAR(500),
    created_at   TIMESTAMPTZ,
    updated_at   TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_dr_tenant ON deletion_requests (tenant_id);
CREATE INDEX IF NOT EXISTS idx_dr_scope ON deletion_requests (scope);
CREATE INDEX IF NOT EXISTS idx_dr_status ON deletion_requests (status);
-- 日批扫描：到期的 pending
CREATE INDEX IF NOT EXISTS idx_dr_due ON deletion_requests (status, deadline);
CREATE INDEX IF NOT EXISTS idx_dr_customer ON deletion_requests (customer_id);
CREATE INDEX IF NOT EXISTS idx_dr_user ON deletion_requests (user_id);

COMMIT;
