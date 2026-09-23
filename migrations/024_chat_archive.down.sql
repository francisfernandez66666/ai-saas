-- 024 down：撤销会话存档（一张表 + channels 五个存档列）。
--
-- 回退代价（这批**会真丢数据**，写给操作的人看）：
--   chat_archive_records 是企业合规留痕的唯一副本——删表后"客户当时说过什么"
--   只能回企微后台重开存档再等新消息，历史段永久取不回来（企微侧只留 5 天窗口）。
-- 回退前若要留档，先跑：
--   COPY (SELECT id, tenant_id, channel_id, msgid, seq, from_user, chatid,
--                msg_type, content_text, msg_time
--         FROM chat_archive_records) TO STDOUT WITH CSV HEADER
-- 本批没动 messages/conversations/customers 的任何列，回退只碰新表与 channels 新列。
BEGIN;

DROP INDEX IF EXISTS idx_archive_tenant_time;
DROP INDEX IF EXISTS idx_archive_channel_seq;
DROP INDEX IF EXISTS ux_archive_one_row_per_msgid;
DROP TABLE IF EXISTS chat_archive_records;

ALTER TABLE channels DROP COLUMN IF EXISTS archive_seq;
ALTER TABLE channels DROP COLUMN IF EXISTS archive_public_key_ver;
ALTER TABLE channels DROP COLUMN IF EXISTS archive_private_key_cipher;
ALTER TABLE channels DROP COLUMN IF EXISTS archive_secret_cipher;
ALTER TABLE channels DROP COLUMN IF EXISTS archive_enabled;

COMMIT;
