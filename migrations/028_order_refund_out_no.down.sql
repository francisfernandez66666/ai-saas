-- 028 down：撤掉退款单号列与其唯一约束。
--
-- 回退代价（执行前必须想清楚）：
--   ① 列一删，"这一单当年用哪个号出过款"这个事实就再次消失——已经发出去的退款
--      在 PSP 侧仍然存在（号在人家库里），我们这边却再也没有对账锚点。
--      回退之后主动查单、财务核对、失败重试三条路一起断，只剩人工翻 PSP 后台。
--   ② 因此本 down 只在"整套 FIX-1 未上线、列里全是空串"的前提下才是无损的。
--      执行前先跑一次留档（非空行数必须为 0，否则**不要**回退）：
--        SELECT count(*) FROM billing_orders WHERE refund_out_no <> '';
--        COPY (SELECT id, order_no, refund_out_no, refund_psp_status
--              FROM billing_orders WHERE refund_out_no <> '' ORDER BY id)
--          TO STDOUT WITH CSV HEADER
--   ③ 代码侧不必同时回退：refundOutNo() 读的是模型字段，列缺失时 GORM 写入会报错，
--      出款那一步会因此停在"意图未落库"而不调 PSP（宁可少发不能重发），
--      也就是说误回退的后果是"退款发不出去 + 有告警"，而不是"重复出款"。
BEGIN;

DROP INDEX IF EXISTS ux_order_refund_out_no;

ALTER TABLE billing_orders DROP COLUMN IF EXISTS refund_out_no;

COMMIT;
