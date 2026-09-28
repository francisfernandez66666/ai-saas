-- 031（FIX-A 追加，2026-09-28）：把所有 tenant_isolation 策略的表达式重建成「GUC 未设置**或为空串**即放行」的三腿写法。
--
-- 为什么 030 通电之后必须紧跟这一条：030 只是把 relrowsecurity 置成 true，让 002 与 EnableRLS
-- 挂上去的**两腿表达式**第一次真的被 PG 咨询。而那两腿（`IS NULL`）判休眠态判错了一件事——
-- 本机实跑（psql 逐条量出来的，不是推断）：
--
--   ①新会话（从未 SET 过该 GUC）      current_setting('app.current_tenant', true) → NULL   IS NULL = true
--   ②跑过一次 SET LOCAL 的那条连接、事务提交之后 → **''（空串）**                        IS NULL = **false**
--
-- PG 对自定义 GUC 的"复位"是置成空串，不是变回未定义；而连接池不会关掉那条连接。于是 030 之后
-- 的真实形态是：**任何曾经激活过 RLS 的连接，此后 current_setting 恒为 ''**——两腿写法在这条连接上
-- 把休眠态判成了激活态，普通查询被筛成空集、写入直接 42501。本轮 `go test ./internal/billing/`
-- 九条用例红就是这个形态（`new row violates row-level security policy for table "billing_orders"`），
-- 单跑一条绿、整包跑红，差别只在同一台池子里有没有先跑过带 SetTenantRLS 的事务。
--
-- 判据写成「NULL 或 空串」两条都算未激活：空串不可能是合法租户号，把它当"没激活"没有歧义；
-- 而激活态仍由第一条等值腿严格收敛（SET 了 '11' 就只能看见 11 的行，031 不放松这一点，
-- rls_coverage_test.go 对三种取值逐一求值）。
--
-- 表达式文本与 internal/db/rls.go 的 rlsPolicyUsing **必须逐字一致**：两处各写一份就是
-- "测的是 A、线上建的是 B"，所以内部/db 的用例把 pg_policies 里读回的 qual 与该常量做归一化比对，
-- 任一侧改动而另一侧没跟，CI 直接红。
--
-- 原子性与瞬间空窗：整段在一个事务里（PG 的 DDL 事务性成立）。DROP/CREATE 之间其它会话
-- 看不见"无策略"的中间态——它们会被表锁挡住，直到本事务提交才看到新策略，
-- 所以不会出现"半秒内全站读空集"的现场（这点很重要：策略面是读路径的谓词，不是可以渐进替换的东西）。
--
-- 为什么按 pg_policies 圈表、而不是照抄代码清单：与 030 同一口径——pg_policies 是
-- "运行时 EnableRLS"与"仅 002 基线"两种历史形态唯一共同的事实源；且本次重建的对象就是策略本身，
-- 给没有策略的表建策略属于越界（那是 rlsTenantTables 与 RLS_ENABLED 的职责）。
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
    -- 先判现状再动手（与 ensureTenantUniqueIndexes 同一口径）：表达式里已经有"空串腿"的表直接跳过，
    -- 避免每次部署都无谓地 DROP/CREATE 一次策略，也让"本迁移重复执行"与"执行过但没生效"
    -- 在日志里可区分（n 只数真正重建过的张数）。
    IF EXISTS (
      SELECT 1 FROM pg_policy p JOIN pg_class c ON c.oid = p.polrelid
                 JOIN pg_namespace nsp ON nsp.oid = c.relnamespace
      WHERE nsp.nspname = r.schemaname AND c.relname = r.tablename
        AND p.polname = 'tenant_isolation'
        AND replace(replace(pg_get_expr(p.polqual, p.polrelid), E'\n', ' '), E'\t', ' ')
            LIKE '%=''''%'
    ) THEN
      CONTINUE;
    END IF;

    EXECUTE format('DROP POLICY IF EXISTS tenant_isolation ON %I.%I', r.schemaname, r.tablename);
    -- 表达式与 rls.go 的 rlsPolicyUsing 逐字一致；FOR ALL 也与 EnableRLS 保持一致。
    EXECUTE format(
      $pol$CREATE POLICY tenant_isolation ON %I.%I FOR ALL USING (
        tenant_id::text = current_setting('app.current_tenant', true)
        OR current_setting('app.current_tenant', true) IS NULL
        OR current_setting('app.current_tenant', true) = ''
      )$pol$, r.schemaname, r.tablename);
    n := n + 1;
  END LOOP;
  -- n=0 是合法结果：策略面为空（全新库且 RLS 从未启用），或全部已是三腿写法（重复执行本迁移）。
  RAISE NOTICE '031_rls_policy_guc_empty: 已按三腿写法重建 tenant_isolation 策略 % 张（其余为现状已合格或无策略）', n;
END $$;

COMMIT;
