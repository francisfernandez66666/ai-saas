-- 021 D3 用量预警触达 + dunning 催缴（2026-09-23）：两张表 + 两条支撑索引。
-- 背景：租户「快没钱了」和「已经没钱了」是两个必须主动说出口的时刻，
-- 此前系统只会事后在看板里摆数字，到期也只有 7d/3d 两档平台群提醒——
-- 销售不盯群就没人知道该去催续费，钱就这么漏掉。本批把"该说的话说出去、说过几次"落成数据。
--
-- 与 GORM 的关系：建表真源同时在 internal/model/billing_alert.go（AutoMigrate 先建列），
-- 本文件用 IF NOT EXISTS 幂等共存（口径同 migrations/020）。
-- 这里独有的价值仍是**索引**：AutoMigrate 建不出部分索引，而两张表各有一条
-- 只有部分索引能救活的查询（见下）。
BEGIN;

-- ============================================================
-- 1) usage_alerts：用量预警投递留痕，同时是去重锚
-- 唯一键 (tenant_id, metric, threshold, period_key) 承担全部去重职责：
--  sweeper 每小时跑，靠 INSERT 撞唯一键（23505）判断"这一档这个账期已经说过"，
--  比"先查后插"可靠——多实例同秒扫到同一租户时，查-插之间有竞态窗口，
--  唯一索引保证同一档至多一条，无需分布式锁。
-- ============================================================
CREATE TABLE IF NOT EXISTS usage_alerts (
    id          BIGSERIAL PRIMARY KEY,
    tenant_id   BIGINT NOT NULL DEFAULT 0,
    metric      VARCHAR(30) NOT NULL,          -- monthly_calls|monthly_tokens|token_balance
    threshold   INT     NOT NULL DEFAULT 0,    -- 档位值：百分比口径存 80/95/100；余额口径存 token 下限
    period_key  VARCHAR(20) NOT NULL,          -- 账期锚 'YYYY-MM'（月度重置后同档可再发一次）
    usage_pct   INT     DEFAULT 0,
    remaining   BIGINT  DEFAULT 0,
    channels    VARCHAR(50),                   -- 实际投递通道 email,group（空=通道全不可用，仅日志）
    detail      TEXT,                          -- 触发上下文快照 JSON
    created_at  TIMESTAMPTZ,
    updated_at  TIMESTAMPTZ
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_usage_alert_once
    ON usage_alerts (tenant_id, metric, threshold, period_key);

-- 后台"本账期已发过哪些预警"列表按租户+账期取，缺它则每次展示顺序扫全表。
CREATE INDEX IF NOT EXISTS idx_usage_alert_tenant_period
    ON usage_alerts (tenant_id, period_key);

-- ============================================================
-- 2) billing_dunning：到期催缴状态机（一租户一行，tenant_id 唯一）
-- 唯一键让"新建本轮序列"与"复用旧行"退化成一个 upsert，
-- 并发两实例同时给同一租户起序列时不会长出第二行。
-- ============================================================
CREATE TABLE IF NOT EXISTS billing_dunning (
    id               BIGSERIAL PRIMARY KEY,
    tenant_id        BIGINT NOT NULL DEFAULT 0,
    status           VARCHAR(20) NOT NULL DEFAULT 'running', -- running|resolved|exhausted
    due_at           TIMESTAMPTZ,                            -- 本轮锚定的到期日快照
    stage            INT     NOT NULL DEFAULT 0,             -- 已发档位序号
    next_notify_at   TIMESTAMPTZ,                            -- 下一档预定时刻（sweep 驱动列）
    last_notified_at TIMESTAMPTZ,
    suspended_at     TIMESTAMPTZ,                            -- 本序列自动施加封禁的时刻（null=没封过）
    sent_to          VARCHAR(200),                           -- 最近一次收件人（脱敏）
    detail           TEXT,                                   -- 最近一次档位上下文 JSON
    created_at       TIMESTAMPTZ,
    updated_at       TIMESTAMPTZ
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_billing_dunning_tenant
    ON billing_dunning (tenant_id);

-- 调度扫描主索引：每小时只关心"还没走完的序列里到点的下一档"。
-- 为什么必须是部分索引（WHERE status='running'）：resolved/exhausted 的历史行会永久累积
-- （它们是要留着回答"这家什么时候续的费"的审计），全量索引随历史线性膨胀，
-- 而 running 行数 = 当前真实欠费在催的租户数，量级小两个数量。
CREATE INDEX IF NOT EXISTS idx_billing_dunning_due
    ON billing_dunning (next_notify_at)
    WHERE status = 'running';

COMMIT;
