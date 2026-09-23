-- 020 down：撤销主动触达任务表。
-- 回退安全前提：本表只存"发送计划与裁决留痕"，删表不会动通道出站队列（channel_outbound）
-- 里已投递的消息记录；已发出的触达在 messages 侧也有独立留痕，故审计链不依赖本表存活。
BEGIN;
DROP INDEX IF EXISTS idx_outreach_sent_customer;
DROP INDEX IF EXISTS idx_outreach_pending_due;
DROP TABLE IF EXISTS outreach_tasks;
COMMIT;
