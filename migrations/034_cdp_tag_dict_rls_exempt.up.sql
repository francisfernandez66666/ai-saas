-- 034（2026-10-16）：把 cdp_tag_definitions 从 RLS 策略面摘掉——标签字典是双层表，
-- 与 system_configs 同型，等值策略会掐断系统层读腿，激活后比不加 RLS 更危险。
--
-- 现场（RLS 本机激活后 uat_advisor G-19 实锤，不是推断）：
--   builtin 原子标签行 tenant_id=0 由 StartIngestConsumer 启动播种、全租户共用（无租户隐私）；
--   ListTagDefinitions 按 tenant_id IN (0,tid) 读，字典本就设计成"系统层+租户自定义"双层。
--   摄入事务 processEvent 内 SetTenantRLS(tx, tid) 之后，ApplyTagTx 按 code 查字典
--   （profile_store.go）被策略 tenant_id = current_setting(...) 过滤，tenant_id=0 行不可见
--   → First 报 not found → 只 WARN 不中断（"[CDP] 未定义标签 beh_visit，跳过"）
--   → 全部内置标签静默丢失：G-19 beh_booked/beh_testdrive/beh_visit/beh_complained
--     四条期望=y 实际=n，而事件落库全 PASS——丢的是标签，不是事件，最难发现的那种。
--   休眠态（未 SET GUC）下策略恒真，所以该缺陷只在 RLS_ENABLED=true 的部署形态现身，
--   是"休眠式设计把问题藏到激活才炸"的又一例；按 system_configs 先例豁免整表
--   （rls.go rlsExemptTables 已同步登记，封闭性由 rls_coverage_test.go 钉住）。
--
-- 与 031 同口径：DDL 在一个事务里，DROP POLICY 与 DISABLE 之间其它会话看不到中间态。
-- 幂等：IF EXISTS + DISABLE 可重复执行（迁移执行器另按 version 去重，双保险）。
BEGIN;

DROP POLICY IF EXISTS tenant_isolation ON cdp_tag_definitions;
-- DISABLE 让该表回到 RLS 之前形态：策略永不被咨询。残留的 FORCE 旗标此时失效——
-- relrowsecurity=false 时 PG 根本不看策略，与迁移 002 之前完全一致；
-- 真要在该表重建策略（连同 FORCE），走本文件的 down，别手工 ALTER。
ALTER TABLE cdp_tag_definitions DISABLE ROW LEVEL SECURITY;

COMMIT;
