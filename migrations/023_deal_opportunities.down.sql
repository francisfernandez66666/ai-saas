-- 023 down：撤销商机与报价两张表。
--
-- 回退代价（这批**会真丢数据**，写给操作的人看）：
--   - opportunities 是销售管道的过程账：删表后"哪个阶段卡了多久、赢单金额多少"永久查不出来；
--   - quotes 是**给客户看过的钱**：删表等于抹掉报价证据，商务纠纷时无从核对当时发的是哪一版。
-- 回退前若要留档，先跑：
--   COPY (SELECT id, tenant_id, customer_id, stage, amount_cents, source_code, created_at
--         FROM opportunities) TO STDOUT WITH CSV HEADER
--   COPY (SELECT id, tenant_id, opportunity_id, version, status, total_cents, created_at
--         FROM quotes) TO STDOUT WITH CSV HEADER
-- 本批没动 customers/conversations/messages/订单与账的任何列，回退只碰这两张新表。
BEGIN;

DROP INDEX IF EXISTS idx_quote_due;
DROP INDEX IF EXISTS ux_quote_one_open_per_deal;
DROP INDEX IF EXISTS idx_quote_deal;
DROP TABLE IF EXISTS quotes;

DROP INDEX IF EXISTS idx_deal_customer;
DROP INDEX IF EXISTS idx_deal_tenant_stage;
DROP INDEX IF EXISTS ux_deal_one_open_per_customer;
DROP TABLE IF EXISTS opportunities;

COMMIT;
