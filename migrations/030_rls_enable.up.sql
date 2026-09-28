-- 030（FIX-A，2026-09-28）：给"已挂 tenant_isolation 策略"的表补 ENABLE ROW LEVEL SECURITY——让 DB 级 RLS 真正通电。
--
-- 要修的事实：PG 语义是 **relrowsecurity=false 时策略永不被咨询**；
-- FORCE ROW LEVEL SECURITY 只关"表 owner 旁路"这一格，不含 ENABLE 语义，救不了这件事。
-- 而本仓两条挂策略的路径都只写了 FORCE：
--   ① 002_rls_coverage.up.sql（历史基线，只 FORCE + 建策略）；
--   ② internal/db/rls.go EnableRLS() 旧循环（FORCE + DROP/CREATE POLICY，无 ENABLE）。
-- 2026-09-28 本机实测：14 张表挂着 tenant_isolation 策略、pg_class.relrowsecurity 全为 false——
-- 启动日志年年宣称"已对 N 张租户表启用行级隔离"，DB 级兜底其实从未生效过一次。
--
-- 执行方式：internal/db/migrations.go 的 MigrateUpFrom 按文件名升序整段执行本文件
-- （自带 BEGIN/COMMIT，单连接原子；成功才写 schema_migrations，失败即中断不记录）。
-- 幂等性：对已 ENABLE 的表重复执行 `ALTER TABLE ... ENABLE ROW LEVEL SECURITY` 无害
-- （PG 不报 42710 之类的冲突，重复置位是真 no-op），所以游标循环可以随时重放——
-- 全新库（EnableRLS 尚未跑过、pg_policies 为空）跑本迁移是合法的空转，不报错。
--
-- 为什么按 pg_policies 圈定目标表、而不是照抄 rlsTenantTables 代码清单：
--   ① 本迁移要对"运行时 EnableRLS 已跑过"与"仅 002 基线挂过策略"两种历史形态同时成立，
--      pg_policies 是两侧唯一共同的事实源；代码清单进了新表还没建表时，照抄会让迁移在
--      relation not exists 上炸掉（这恰是 migrations.go 把 MigrateUp 排在 AutoMigrate 之后的原因）。
--   ② 只补 ENABLE，**不动 FORCE、不删/建策略**：002 已挂的策略与 FORCE 保留原样
--      （运行时 EnableRLS 仍会 DROP/CREATE 重建，幂等），两条路径都要求 ENABLE 在场——
--      本迁移补的正是两者共同缺失的那一位。
--   ③ 不"顺手"给全库含 tenant_id 的表 ENABLE：system_configs 等平台/豁免表按设计不挂策略
--      （见 rls.go rlsExemptTables 注释——等值策略会切断它读系统层默认值的回落腿），
--      ENABLE 面必须与策略面逐字一致，多一家都是把无策略的表置成"看着有保护"。
BEGIN;

DO $$
DECLARE
  r RECORD;
  n int := 0;
BEGIN
  -- format('%I') 逐段引用标识符：pg_policies 里的名字虽都是现存关系，
  -- 但拼 SQL 标识符不走裸字符串拼接是通用纪律，不因"来源看起来可信"而豁免。
  FOR r IN
    SELECT DISTINCT schemaname, tablename
    FROM pg_policies
    WHERE policyname = 'tenant_isolation'
  LOOP
    EXECUTE format('ALTER TABLE %I.%I ENABLE ROW LEVEL SECURITY', r.schemaname, r.tablename);
    n := n + 1;
  END LOOP;
  -- n=0 不是错误：策略面为空的库（未启用 RLS 的全新部署）本迁移即合法空转。
  -- 如实 RAISE 计数，让 psql/日志能看到"通电了几张表"，与 smoke §四十七 的实查断言同源。
  RAISE NOTICE '030_rls_enable: 已对 % 张挂有 tenant_isolation 策略的表补 ENABLE ROW LEVEL SECURITY', n;
END $$;

COMMIT;
