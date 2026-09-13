-- 002 RLS 覆盖基线回退：解除 FORCE ROW LEVEL SECURITY 并删除 tenant_isolation 策略。
-- 只处理当前仍存在的表，便于与 001/003+ 的回退顺序共存。
BEGIN;

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
        'departments', 'customer_identities', 'api_keys', 'flow_instances',
        'message_event_records', 'dept_pack_bindings',
        'tags', 'tag_rules', 'tag_weight_mappings', 'flow_definitions',
        'brands', 'car_models', 'model_specs', 'competitor_compares'
    ];
BEGIN
    FOREACH tbl IN ARRAY tables LOOP
        IF to_regclass(format('public.%I', tbl)) IS NOT NULL THEN
            EXECUTE format('DROP POLICY IF EXISTS tenant_isolation ON %I', tbl);
            EXECUTE format('ALTER TABLE %I NO FORCE ROW LEVEL SECURITY', tbl);
        END IF;
    END LOOP;
END
$$;

COMMIT;
