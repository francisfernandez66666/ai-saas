-- 027 down：撤销"同编码只允许一个上架版本"的约束。
--
-- 回退代价（必须先想清楚再执行）：
--   ① 约束没了，但**被本迁移下架的那些行不会自动回到 active**——
--      SQL 无法知道"当年是谁在上架"，回填信息不在库里。回退后同一 code 可能一个上架版本都没有，
--      于是 openActivePackByCode 取不到包：新租户注册落包跳过、继承链回溯断在中间一层、
--      换包报"包 X 不存在或未上架"。这不是"回到改造前"，而是"回到比改造前更空的状态"。
--   ② 真要恢复到回退前那份多版本并存，只能照下面查询先留档、再按 id 手工置回：
--        COPY (SELECT id, code, version, status FROM industry_packs ORDER BY code, id)
--          TO STDOUT WITH CSV HEADER
--      执行本 down **之前**跑一次，留档即恢复依据。
--   ③ 代码侧不必同时回退：写侧 ActivatePackExclusive 的"兄弟让位"在没有索引时只是多一次
--      UPDATE，语义仍正确；启动期 ReconcileActiveVersions 也仍会收拢多版本。
--      也就是说 down 之后系统行为几乎不变——这正是当初把不变式同时写进代码和索引的好处：
--      约束是兜底，不是唯一执行者。
BEGIN;

DROP INDEX IF EXISTS ux_pack_one_active_per_code;

COMMIT;
