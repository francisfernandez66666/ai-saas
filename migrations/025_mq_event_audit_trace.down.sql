-- 025 down：撤销消息中心台账的消费终态三列与两条索引。
--
-- 回退代价（写给操作的人看）：
--   trace_id / err_msg 里存的是"哪条链路的哪个事件被丢掉、为什么"——删列即永久失去
--   历史死信的可追溯性（dead_letter 行还在，但原因与链路 ID 没了）。
--   回退前若要留档：
--     COPY (SELECT id, tenant_id, event_id, topic, status, trace_id, err_msg, created_at
--           FROM message_event_records WHERE status IN ('dead_letter','failed'))
--       TO STDOUT WITH CSV HEADER
--   回退后代码侧须同时退回旧版 mq 包（旧版不写这三列，新列留着不会报错，只是没人读）。
BEGIN;

DROP INDEX IF EXISTS idx_mq_event_records_dead;
DROP INDEX IF EXISTS idx_mq_event_records_trace;

ALTER TABLE message_event_records DROP COLUMN IF EXISTS err_msg;
ALTER TABLE message_event_records DROP COLUMN IF EXISTS trace_id;
ALTER TABLE message_event_records DROP COLUMN IF EXISTS updated_at;

COMMIT;
