-- 011_message_archive：长周期会话冷数据归档表（2026-09-15 价值增强批，opt-in）
-- 背景：messages 表只增不删，长周期/高复购行业 2~3 年后热表会拖慢历史查询与备份。
-- 策略：默认关闭（system_config message_archive_days=0）；开启后每日任务把
--       created_at 早于 N 天的消息"搬进"messages_archive（结构全量对齐 + archived_at 标记）。
-- 合规红线：PIPL 删除权匿名化同时覆盖本表（internal/privacy 已同步），归档不放走个人内容。
CREATE TABLE IF NOT EXISTS messages_archive (
    LIKE messages INCLUDING DEFAULTS INCLUDING COMMENTS,
    archived_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_messages_archive_conv ON messages_archive(conversation_id);
CREATE INDEX IF NOT EXISTS idx_messages_archive_tenant_time ON messages_archive(tenant_id, created_at);
CREATE INDEX IF NOT EXISTS idx_messages_archive_customer ON messages_archive(customer_id);
