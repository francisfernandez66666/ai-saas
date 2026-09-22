-- 018 down：撤销终局标签列（数据可丢——归因表原始信号 hooked/lead_captured 仍在，
-- 重跑小时任务即可再次回填，故回退安全）。
BEGIN;
DROP INDEX IF EXISTS idx_ra_outcome_pending;
ALTER TABLE reply_attributions DROP COLUMN IF EXISTS arrived_at;
ALTER TABLE reply_attributions DROP COLUMN IF EXISTS dealt_at;
ALTER TABLE reply_attributions DROP COLUMN IF EXISTS outcome_stage;
ALTER TABLE reply_attributions DROP COLUMN IF EXISTS outcome_checked_at;
ALTER TABLE pack_stats DROP COLUMN IF EXISTS arrive_rate;
ALTER TABLE pack_stats DROP COLUMN IF EXISTS deal_rate;
COMMIT;
