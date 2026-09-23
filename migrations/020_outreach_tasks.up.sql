-- 020 主动触达最小闭环(2026-09-23)：触达任务表 + 调度索引。
-- 背景：产品此前只有"客户先来一句、AI/顾问再回"的被动链路，缺"到点主动找客户"的能力
-- （跟进承诺过期、留资未到店、高意向静默客户都需要一次可审计的主动触达）。
-- 本表承载**发送计划**与其合规裁决留痕，派发后复用既有 channel_outbound 队列投递，
-- 不另起一条出站通路（口径同 D5「通道领域代码禁止再进 internal/service」）。
--
-- 与 GORM 的关系：建表真源同时在 internal/model/outreach.go（AutoMigrate 先建列），
-- 本文件用 IF NOT EXISTS 幂等共存——与 migrations/003~007 同一惯例。
-- 这里独有的价值是**调度索引**：AutoMigrate 只会按列建单索引，建不出
-- "只覆盖 pending 行"的部分复合索引，而调度器每轮都按 (tenant_id, scheduled_at) 取待办。
--
-- 为什么是部分索引（WHERE status='pending'）：任务表会长期累积 sent/failed/skipped 终态行做审计，
-- 全量索引随历史线性膨胀，而扫描永远只关心 pending；终态行越多，这个索引反而越小越稳。
BEGIN;

CREATE TABLE IF NOT EXISTS outreach_tasks (
    id            BIGSERIAL PRIMARY KEY,
    tenant_id     BIGINT NOT NULL DEFAULT 0,
    customer_id   BIGINT NOT NULL,
    content       TEXT   NOT NULL,
    scheduled_at  TIMESTAMPTZ NOT NULL,
    status        VARCHAR(20) NOT NULL DEFAULT 'pending',   -- pending|queued|sent|skipped|failed|cancelled
    reason        VARCHAR(40),                              -- skipped 原因码（稳定字面量，前端/smoke 按它判）
    error         VARCHAR(500),                             -- failed 通道错误摘要
    channel_id    BIGINT DEFAULT 0,                         -- 派发时命中的通道
    outbound_id   BIGINT DEFAULT 0,                         -- 关联 channel_outbound.id
    attempts      INT     DEFAULT 0,
    created_by    BIGINT DEFAULT 0,                         -- 排期人（tenant_users.id，0=系统）
    sent_at       TIMESTAMPTZ,
    created_at    TIMESTAMPTZ,
    updated_at    TIMESTAMPTZ
);

-- 调度扫描主索引：一轮只取"某租户到点的 pending 任务"，按时间升序保发送顺序。
CREATE INDEX IF NOT EXISTS idx_outreach_pending_due
    ON outreach_tasks (tenant_id, scheduled_at)
    WHERE status = 'pending';

-- 频控计数索引：判定"该客户近 7 天已发几条"（over_weekly_limit 裁决），
-- 只覆盖 sent 终态行，避免每轮判定顺序扫全表。
CREATE INDEX IF NOT EXISTS idx_outreach_sent_customer
    ON outreach_tasks (tenant_id, customer_id, sent_at)
    WHERE status = 'sent';

COMMIT;
