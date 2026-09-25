-- 025（G-15① 观测面真实性，2026-09-24）：消息中心台账补"消费终态 + trace + 失败原因"。
--
-- 要修的事实：message_event_records 以前只记发布阶段（sent/failed），消费侧无论成功
-- 还是重试耗尽被丢弃，台账一个字都不改。结果就是"sent"永远等于"已妥投"——
-- 而进程内总线（MQ_TYPE=log，**默认形态**）与 Kafka 消费循环在重试耗尽后都是
-- 只打一行 log 就放弃，事件从此不存在。观测面对着一份永远全绿的假账。
--
-- 三条改动，全部向后兼容（旧行 status 仍是 sent，读侧只多不减）：
--   1. trace_id：把 Header 里的链路 ID 原样落库，发布/消费两段同 trace，
--      "gin → 队列 → MQ → 消费者"断链处可按 trace 追（G-15④ 的落库侧）。
--   2. err_msg：失败/死信原因（截断到 512，日志与库都要说清为什么丢）。
--   3. 部分索引 WHERE status='dead_letter'：死信是**需要人来处理**的少量行，
--      运维面按状态取名单时必须走索引，不能对台账全表扫描。
--
-- 为什么 updated_at 也在本次加：终态是 UPDATE 同一行（event_id 唯一），没有 updated_at
-- 就分不出"发布即失败"和"发了三小时才消费掉"。

BEGIN;

ALTER TABLE message_event_records ADD COLUMN IF NOT EXISTS trace_id VARCHAR(64) NOT NULL DEFAULT '';
ALTER TABLE message_event_records ADD COLUMN IF NOT EXISTS err_msg  VARCHAR(512) NOT NULL DEFAULT '';
ALTER TABLE message_event_records ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT now();

CREATE INDEX IF NOT EXISTS idx_mq_event_records_trace ON message_event_records (trace_id) WHERE trace_id <> '';
CREATE INDEX IF NOT EXISTS idx_mq_event_records_dead ON message_event_records (created_at) WHERE status = 'dead_letter';

COMMIT;
