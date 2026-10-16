-- 034 down（2026-10-16）：恢复 cdp_tag_definitions 上的 tenant_isolation 策略。
--
-- 警告：单独执行本文件而不回退 rls.go 的豁免登记，等于把 G-19 缺陷装回库上——
-- RLS_ENABLED=true 时摄入事务重新查不到 builtin 字典（tenant_id=0 行被等值策略过滤），
-- ApplyTagTx 只 WARN 不中断，全部内置标签静默丢失。
-- 回退本迁移的唯一正当理由：产品决定字典也要 DB 级按租户收敛——那得先改读路径
-- （例如字典查询退出激活事务，或把该表策略改成 (0, current) 双列等值），
-- 并同步把表移回 rlsTenantTables、改 rls_coverage_test.go 的封闭性断言。
--
-- 表达式与 rls.go 的 rlsPolicyUsing 逐字一致（三腿写法，与 031 同款）：
-- rls_enable_test.go 的 TestRLSMigrationPolicyTextMatchesGoTemplate 以 PG 渲染后的
-- 文本做等值比对，任一侧漂移即 CI 红。
BEGIN;

ALTER TABLE cdp_tag_definitions ENABLE ROW LEVEL SECURITY;
ALTER TABLE cdp_tag_definitions FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON cdp_tag_definitions;
CREATE POLICY tenant_isolation ON cdp_tag_definitions FOR ALL USING (
        tenant_id::text = current_setting('app.current_tenant', true)
        OR current_setting('app.current_tenant', true) IS NULL
        OR current_setting('app.current_tenant', true) = ''
);

COMMIT;
