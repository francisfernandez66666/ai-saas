-- 022 down：撤销获客活码两张表与客户中台归因列。
--
-- 回退代价说明（与前几批不同，这一批**会真丢数据**，写在这里是给操作的人看的）：
--   - acquisition_codes / acquisition_scans 是配置与事件留痕，删表只丢"哪个渠道带来多少人"；
--   - customers.acquisition_code 是**已成交客户的历史归因**，删列不可逆——
--     回退前若还想留档，先跑
--       COPY (SELECT id, tenant_id, acquisition_code FROM customers
--             WHERE acquisition_code <> '') TO STDOUT WITH CSV HEADER
--     落一份，再前滚时按同列回填即可。
-- 不动 customers 的其它任何列，不动 conversations/messages/订单与账（本批压根没碰它们）。
BEGIN;

DROP INDEX IF EXISTS idx_customers_acq_code;
ALTER TABLE customers DROP COLUMN IF EXISTS acquisition_code;

DROP INDEX IF EXISTS idx_acq_scan_code_time;
DROP INDEX IF EXISTS idx_acq_scan_dedupe;
DROP TABLE IF EXISTS acquisition_scans;

DROP INDEX IF EXISTS idx_acq_code_tenant;
DROP INDEX IF EXISTS ux_acq_code_global;
DROP TABLE IF EXISTS acquisition_codes;

COMMIT;
