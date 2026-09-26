-- 029 down（FIX-9 发票交付触达，2026-09-27）：删列即回滚——应用侧读的是 model 字段，
-- 列没了 GORM 会报缺列，所以必须先回滚二进制再跑本文件（口径同 028）。
BEGIN;
ALTER TABLE billing_orders DROP COLUMN IF EXISTS invoice_notified_at;
ALTER TABLE billing_orders DROP COLUMN IF EXISTS invoice_notify_result;
COMMIT;
