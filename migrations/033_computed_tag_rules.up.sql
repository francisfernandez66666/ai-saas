-- P2-4 计算标签规则表（配置驱动，业务自助配派生标签规则，无需改代码）
-- 建表真源在此；internal/db/database.go 的 AutoMigrate 幂等共存（仅增量建表，不冲突）。
-- 红线：目标标签若属 attitude 维，摄入层 computed_tag.go 会跳过（态度严禁由规则派生）。

CREATE TABLE IF NOT EXISTS cdp_computed_tag_rules (
    id             BIGSERIAL PRIMARY KEY,
    tenant_id      BIGINT      NOT NULL DEFAULT 0,
    name           VARCHAR(64) NOT NULL,
    target_tag_code VARCHAR(32) NOT NULL,
    condition      JSON        NOT NULL,
    priority       INT         NOT NULL DEFAULT 0,
    status         INT         NOT NULL DEFAULT 1,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 租户级唯一（同名规则不重复）；目标标签查询索引
CREATE UNIQUE INDEX IF NOT EXISTS idx_cdp_rule_tenant_name
    ON cdp_computed_tag_rules (tenant_id, name);
CREATE INDEX IF NOT EXISTS idx_cdp_rule_target
    ON cdp_computed_tag_rules (target_tag_code);
CREATE INDEX IF NOT EXISTS idx_cdp_rule_tenant_status
    ON cdp_computed_tag_rules (tenant_id, status);
