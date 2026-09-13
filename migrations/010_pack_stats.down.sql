-- 010 包质量统计快照与离线评分回退（D9）
BEGIN;
DROP TABLE IF EXISTS pack_stats;
DROP INDEX IF EXISTS idx_ra_eval_pending;
ALTER TABLE reply_attributions DROP COLUMN IF EXISTS eval_checked_at;
ALTER TABLE reply_attributions DROP COLUMN IF EXISTS eval_score;
COMMIT;
