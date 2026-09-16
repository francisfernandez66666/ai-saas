-- 013 G1 收口(2026-09-16C，AUDIT_GAP_REALITY_2026-09-16)：活跃会话唯一性 DB 兜底。
-- 012 只清了存量、应用层锁只覆盖 web 主入口；guest/testhandler/顾问首发/通道/OpenAPI
-- 多实例并发仍可再造重复 active（OneID 事故根因）。本迁移把不变式交给数据库：
-- 每 (tenant_id, customer_id) 至多一条 status='active' 会话（conversations 无软删列，直接部分索引）。
-- 建索引前先幂等收拢一遍（与 012 同规则：保留 updated_at 最新一条，其余关账）——
-- 若 012 之后又有新重复，此处不清就建不上索引；重复组存在时以 (updated_at, id) 定序，判定确定。
UPDATE conversations c
SET status = 'closed'
WHERE c.status = 'active'
  AND EXISTS (
    SELECT 1 FROM conversations k
    WHERE k.tenant_id = c.tenant_id
      AND k.customer_id = c.customer_id
      AND k.status = 'active'
      AND (k.updated_at, k.id) > (c.updated_at, c.id)
  );

CREATE UNIQUE INDEX IF NOT EXISTS ux_conv_one_active
    ON conversations (tenant_id, customer_id)
    WHERE status = 'active';
