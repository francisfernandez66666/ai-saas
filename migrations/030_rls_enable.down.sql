-- 030 down（FIX-A，2026-09-28）：**有意不回退，刻意 no-op**——这不是偷懒，是安全口径。
--
-- 为什么不能写成 `ALTER TABLE ... DISABLE ROW LEVEL SECURITY`：
--   ① 本迁移没动策略（tenant_isolation 仍在），DISABLE 之后所有查询将**不咨询任何策略**——
--      这不是"回到 030 之前"，而是把当时"看着有保护、实则从未生效"的状态主动又打开一次；
--      执行 down 的人心里想的通常是"撤销这次迁移"，拿到的却是"关掉租户隔离"，
--      两者后果完全不同，后者不该由一条迁移回退顺带发生。
--   ② 与旧二进制组合尤其危险：旧 EnableRLS 只 FORCE 不 ENABLE，down 之后系统回到
--      "策略在场、relrowsecurity=false、日志照宣称已启用"的静默形态——正是本批要根除的缺陷现场。
--   ③ "撤销 030 的痕迹"无实体可删：幂等 SQL 不落任何标记（无标记列、不建对象），
--      pg_class 上的 relrowsecurity 位就是它全部的作用，删无可删。
-- 真要关掉 DB 级隔离，正确动作是显式的两条之一，且都该由人负责而非迁移代劳：
--   把应用连接角色改为 BYPASSRLS（连 SUPERUSER 旁路都有独立审计语义），或
--   逐表 DROP POLICY tenant_isolation ON ...（连策略一起摘掉，不留"挂着不生效"的假象）。
-- 执行器口径（migrations.go MigrateDownFrom）：本文件仍会被执行并删除 schema_migrations 里的
-- 030 记录——即"账本回退"成立、"防护解除"不成立，这是刻意保留的不对称。
BEGIN;
-- 刻意为空。理由见上方注释；参照 027 down 的先例：回退代价必须先想清楚再动手，
-- 而 030 的"回退代价"没有任何一种写法能低于"什么都不做"。
COMMIT;
