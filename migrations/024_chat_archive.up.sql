-- 024 会话存档（E8，2026-09-24）：channels 五个存档列 + chat_archive_records 表 + 三条索引。
--
-- 要解决的业务问题：客户在企微里跟顾问说了什么，商家自己看不见也留不住——员工离职带走对话、
-- 纠纷时拿不出"当时客户确实说过要"。企微的「会话内容存档」把整段会话（含撤回、含我们
-- 没参与的同事内部对话）按 seq 推给企业，这是合规留痕能力，不是收发货链路，所以凭据、
-- 开关、落库全部独立，不复用 messages。
--
-- 与 GORM 的关系（口径同 020/021/022/023）：建表真源同时在 internal/model/chat_archive.go
-- （AutoMigrate 先建列/建表），本文件用 IF NOT EXISTS 幂等共存。
-- 本文件**独有且必须在这里**的是带 WHERE 的部分唯一索引——AutoMigrate 建不出来，
-- 而"同一条存档消息不被重复落两遍"这条不变式只有 DB 层能兜住。
BEGIN;

-- ============================================================
-- 1) channels：存档凭据与游标（密文列，出接口一律不回显）
--
-- 为什么存档 secret 单独一列而不是塞 config_json：config_json 是明文扩展位，
-- 把 secret 写进去等于在日志/接口/备份里裸奔（既有凭据三列 *_cipher 同理）。
--
-- 为什么开关默认 false：与主动触达/催缴同一口径——"忘了关就把客户聊天记录整段抄进
-- 我们库里"是 PIPL 级别的事故，比多打一次群发严重得多。开了才拉、才落库。
-- ============================================================
ALTER TABLE channels ADD COLUMN IF NOT EXISTS archive_enabled BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE channels ADD COLUMN IF NOT EXISTS archive_secret_cipher VARCHAR(512) NOT NULL DEFAULT '';
ALTER TABLE channels ADD COLUMN IF NOT EXISTS archive_private_key_cipher TEXT NOT NULL DEFAULT '';
ALTER TABLE channels ADD COLUMN IF NOT EXISTS archive_public_key_ver INTEGER NOT NULL DEFAULT 0;
-- 游标列写而不是读改写 config_json：与 kf_cursor 同一个理由（并发编辑凭据不丢键、无丢失更新）。
ALTER TABLE channels ADD COLUMN IF NOT EXISTS archive_seq BIGINT NOT NULL DEFAULT 0;

-- ============================================================
-- 2) chat_archive_records：存档消息（解密后的结构化形态 + 解密失败留痕）
--
-- 为什么解不开也要建行（decrypt_error 非空、content_text 为空）：
-- 游标按 seq 单调前移，遇到一条解不开就停住的话，同一 seq 会被反复重拉、
-- 整条同步链路永久卡死。宁可留一条"读不了"的记录，也不能让后面的消息跟着陪葬。
--
-- 为什么没有 conversation_id/customer_id：存档里大量消息对不上站内客户
-- （内部同事对话、群聊、未建档的外部联系人）。硬塞进 messages 只会把 messages 的
-- conversation_id 不变式搞脏，所以两表各留各的账，需要时按 from_user/chatid 关联。
-- ============================================================
CREATE TABLE IF NOT EXISTS chat_archive_records (
    id               BIGSERIAL PRIMARY KEY,
    tenant_id        BIGINT       NOT NULL DEFAULT 0,   -- 后台任务无请求 ctx，必须显式盖章（C7 红线）
    channel_id       BIGINT       NOT NULL,
    msgid            VARCHAR(128) NOT NULL DEFAULT '',  -- 企微消息 ID：幂等锚（空=事件类报文，不参与去重）
    seq              BIGINT       NOT NULL DEFAULT 0,   -- 存档序号：游标推进依据
    public_key_ver   INTEGER      NOT NULL DEFAULT 0,   -- 该条用哪一版公钥加的密
    biz_type         VARCHAR(16)  NOT NULL DEFAULT '',  -- business|system
    action           VARCHAR(16)  NOT NULL DEFAULT '',  -- send|recalled|switch
    from_user        VARCHAR(64)  NOT NULL DEFAULT '',
    sender_name      VARCHAR(64)  NOT NULL DEFAULT '',
    to_list          TEXT         NOT NULL DEFAULT '[]',-- 企微原始 to 列表（JSON 文本，整读整写不建子表）
    chat_type        VARCHAR(16)  NOT NULL DEFAULT '',  -- single|group
    chatid           VARCHAR(64)  NOT NULL DEFAULT '',
    msg_type         VARCHAR(24)  NOT NULL DEFAULT '',  -- text|image|voice|video|file|link|weapp|card...
    content_text     TEXT         NOT NULL DEFAULT '',  -- 文本类正文（非文本类为空）
    media_id         VARCHAR(256) NOT NULL DEFAULT '',  -- 媒体资源 ID（下载须存档许可 + SDK）
    media_status     VARCHAR(16)  NOT NULL DEFAULT '',  -- ''|pending|done|failed
    decrypt_error    VARCHAR(256) NOT NULL DEFAULT '',  -- 解密失败原因（非空即只有信封没有正文）
    msg_time         TIMESTAMPTZ,
    created_at       TIMESTAMPTZ,
    updated_at       TIMESTAMPTZ
);

-- **同一通道的同一条消息只落一行**：企微会重推、我们也会重试同一 seq 段。
-- msgid 为空的（switch 之类事件报文）不进这条约束，否则第二条空 msgid 就撞锚。
CREATE UNIQUE INDEX IF NOT EXISTS ux_archive_one_row_per_msgid
    ON chat_archive_records (channel_id, msgid)
    WHERE msgid <> '';

-- 增量拉取的取数面（按通道顺序读 seq）与合规检索的主路径（某租户某时间段的存档）。
CREATE INDEX IF NOT EXISTS idx_archive_channel_seq ON chat_archive_records (channel_id, seq);
CREATE INDEX IF NOT EXISTS idx_archive_tenant_time ON chat_archive_records (tenant_id, msg_time);

COMMIT;
