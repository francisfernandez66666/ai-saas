-- 012 存量治理（D2，2026-09-16B，见 AUDIT_UAT_VERIFY_2026-09-16B）：
-- OneID 合并历史上只迁 customer_id 不收拢会话，且多入口/多通道各开会话，
-- 造成同一客户名下多条 status='active' 会话（复核批次实测 260 会话仅个别关账、
-- 单客户最多 16 条 active）。顾问端计数虚高、上下文分裂。
-- 规则：每 (tenant_id, customer_id) 仅保留 updated_at 最新的一条 active，其余关账。
-- 仅动 status 列，消息/历史完整保留；幂等（二次执行 0 行受影响）。
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

-- 客户表不做自动合并（21 组同号重复客户涉及顾问归属/阶段取并等业务判断，
-- 由 tools/oneid_dup_report.sh 出清单人工/后续批次处置）。
