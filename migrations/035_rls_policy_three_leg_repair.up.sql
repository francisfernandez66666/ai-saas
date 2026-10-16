-- 035（2026-10-16）：把全部 tenant_isolation 策略重建成三腿写法——修复"旧二进制复活 FIX-A 缺陷"的库污染。
--
-- 事故链（2026-10-16 实锤，非推断）：
--   tools/smoke_redis.sh 旧写法 `[ -x bin/ai-scrm-redis ] || go build` 复用了 2026-09-27 01:59
--   的二进制——早于 FIX-A（2026-09-28 给 rlsPolicyUsing 补空串休眠腿）。该段 start_instance
--   不覆盖 RLS_ENABLED，二进制读项目 .env（本机当日已翻 true）→ 旧 EnableRLS 拿**两腿模板**
--   把 60 张租户表策略逐个 DROP POLICY + CREATE POLICY 重建。冒烟先跑（三腿全对）后
--   smoke_redis 跑（两腿污染），同一轮 test_all 的 go test 层随后红三条：
--     · TestRLSMigrationPolicyTextMatchesGoTemplate：Go 模板（三腿）≠ 真表 qual（两腿）；
--     · TestRLSPolicyFaceObservation：DormantLegMissing=58；
--     · TestCrossTenantSameTagNameAllowed：连接池里跑过 SET LOCAL 的连接提交后 GUC 复位成 ''
--       （不是 NULL），两腿策略把"未激活"判成"激活且租户号为空"→ INSERT 撞 42501。
--   脚本侧已改为每次强制重编译（smoke_redis.sh 0) 前置有事故注释）；本迁移负责把**已被污染
--   的库**修回三腿。
--
-- 为什么必须是一条新迁移、而不是"重放 031"：迁移执行器按 schema_migrations 去重，031 已记账
-- 的库永远不重跑；而"旧二进制 + RLS_ENABLED=true"这条污染路径与代码版本无关（拿任意旧二进制
-- 连一下库就能复现），修复必须是可执行的新迁移，不是一句"重新应用 031"。
--
-- 与 031 的分工：031 是"两腿升三腿"的首次落地；本迁移是同一判据的**可重放修复版**。
-- 031 的跳过条件实际无效——`LIKE '%=''''%'` 想匹配 `=''`，而 PG 把三腿渲染成 `= ''::text`
-- （等号后带空格），永不命中，故 031 每次都全量重建。本迁移按渲染后的真实形态判据
-- （qual 含 `''::text` 即已合格，与 smoke.sh §四十七 同源），合格的表跳过、只重建缺腿的，
-- 让"重复执行"与"修了几张"在 RAISE NOTICE 里可区分。
--
-- 表达式与 rls.go 的 rlsPolicyUsing 逐字一致（三腿写法）；rls_enable_test.go 的
-- TestRLSMigrationPolicyTextMatchesGoTemplate 以 PG 渲染后的文本做等值比对，任一侧漂移即 CI 红。
--
-- down 刻意 no-op（与 030/031 同口径）：回退本迁移 = 把两腿写法装回去 = 重新引入
-- "连接池 GUC 复位成 '' 后读空集、写 42501"的静默故障。执行器口径（migrations.go
-- MigrateDownFrom）：本文件仍会被执行并删除 schema_migrations 里的 035 记录——
-- 即"账本回退"成立、"防护解除"不成立，这是刻意保留的不对称。
BEGIN;

DO $$
DECLARE
  r RECORD;
  n int := 0;
BEGIN
  FOR r IN
    SELECT DISTINCT schemaname, tablename
    FROM pg_policies
    WHERE policyname = 'tenant_isolation'
  LOOP
    -- 判据按 PG 渲染后的真实文本：三腿写法渲染成 `... = ''::text`（::text 是 PG 补的类型转换）。
    -- 用 chr(39)||chr(39)||'::text' 拼引号，语义与 shell/SQL 转义解耦（与 smoke.sh 同写法）。
    IF EXISTS (
      SELECT 1 FROM pg_policy p JOIN pg_class c ON c.oid = p.polrelid
                 JOIN pg_namespace nsp ON nsp.oid = c.relnamespace
      WHERE nsp.nspname = r.schemaname AND c.relname = r.tablename
        AND p.polname = 'tenant_isolation'
        AND replace(replace(pg_get_expr(p.polqual, p.polrelid), E'\n', ' '), E'\t', ' ')
            LIKE '%'||chr(39)||chr(39)||'::text%'
    ) THEN
      CONTINUE;
    END IF;

    EXECUTE format('DROP POLICY IF EXISTS tenant_isolation ON %I.%I', r.schemaname, r.tablename);
    EXECUTE format(
      $pol$CREATE POLICY tenant_isolation ON %I.%I FOR ALL USING (
        tenant_id::text = current_setting('app.current_tenant', true)
        OR current_setting('app.current_tenant', true) IS NULL
        OR current_setting('app.current_tenant', true) = ''
      )$pol$, r.schemaname, r.tablename);
    n := n + 1;
  END LOOP;
  -- n=0 是合法结果：策略面为空（未启用 RLS 的全新库），或全部已是三腿（重复执行本迁移）。
  RAISE NOTICE '035_rls_policy_three_leg_repair: 已按三腿写法重建 tenant_isolation 策略 % 张（其余为现状已合格或无策略）', n;
END $$;

COMMIT;
