-- G-14 RLS 覆盖面补全迁移
-- 为所有含 tenant_id 的业务表启用休眠式行级隔离
-- 执行方式：migrate.go 工具 或 psql -f（需 RLS_ENABLED=true 时由 EnableRLS() 动态创建，本文件为参考基线）
-- 创建时间：2026-09-11

BEGIN;

-- ---- 核心业务表（已有 RLS 策略，此处为基线记录） ----
DO $$
DECLARE
    tbl TEXT;
    tables TEXT[] := ARRAY[
        'customers', 'conversations', 'messages', 'knowledge_fragments', 'kb_feedback_materials',
        'feedbacks', 'follow_ups', 'test_drives', 'customer_tags', 'agreement_signatures',
        'reward_claims', 'cdp_profiles', 'cdp_tag_definitions', 'cdp_tag_assignments',
        'event_logs', 'id_mappings', 'inbox_events', 'flow_state_machines',
        'templates', 'features', 'tenant_pack_bindings', 'billing_orders',
        'usage_records', 'usage_ledger', 'tenant_audit_logs',
        -- G-14 新增
        'departments', 'customer_identities', 'api_keys', 'flow_instances',
        'message_event_records', 'dept_pack_bindings',
        'tags', 'tag_rules', 'tag_weight_mappings', 'flow_definitions',
        'brands', 'car_models', 'model_specs', 'competitor_compares'
    ];
BEGIN
    FOREACH tbl IN ARRAY tables LOOP
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', tbl);
        EXECUTE format('DROP POLICY IF EXISTS tenant_isolation ON %I', tbl);
        EXECUTE format(
            'CREATE POLICY tenant_isolation ON %I FOR ALL USING (
                tenant_id::text = current_setting(''app.current_tenant'', true)
                OR current_setting(''app.current_tenant'', true) IS NULL
            )', tbl);
    END LOOP;
END
$$;

COMMIT;
