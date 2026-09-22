-- 018 批五 A：AI 销售闭环的终局标签回填（PLAN_FIX_2026-09-22 §6-A）。
-- 背景：reply_attributions 原本只有 message 级近端信号（hooked/lead_captured/pending_human），
-- 而"AI 销售"真正要优化的是**终局结果**——客户有没有到店、有没有成交。这两个信号其实早已存在，
-- 只是长在客户级（customers.journey_stage，经 analytics.ArrivedStages/OrderedStages 判定），
-- 与 message 级归因表之间只差一次 join（两边都有 customer_id 索引）。
-- 本迁移把结果标签下沉到归因行，使 pack_stats 能聚合出 arrive_rate/deal_rate，
-- 供 Step4 择臂层（template_bandit）与实验判优层（experiment_decision）当奖励使用。
--
-- 为什么用"回填 + 观察窗"而不是一次性算：销售是周级因果，一条今天发出的 AI 回复
-- 可能在两周后才带来到店。窗口未到的行**不能判为失败**，否则新回复永远比老回复差，
-- 择优会系统性偏向历史样本（这是自动优化最容易踩的坑）。
-- 因此：arrived_at/dealt_at 记录事件时刻，outcome_checked_at 记录最近一次巡检时刻，
-- created_at 距今超过 outcome_window_days（热配，默认 30）的行才判为"窗口已过、结果未发生"。
BEGIN;

ALTER TABLE reply_attributions ADD COLUMN IF NOT EXISTS arrived_at TIMESTAMPTZ;
ALTER TABLE reply_attributions ADD COLUMN IF NOT EXISTS dealt_at TIMESTAMPTZ;
-- 巡检快照：回填当时客户的旅程阶段码（便于复核"为什么这行算到店/没到店"）
ALTER TABLE reply_attributions ADD COLUMN IF NOT EXISTS outcome_stage VARCHAR(30);
ALTER TABLE reply_attributions ADD COLUMN IF NOT EXISTS outcome_checked_at TIMESTAMPTZ;

-- 待回填队列：只捞有模板归因（参与择优）且尚未终判的行，与 010 的 idx_ra_eval_pending 同型
CREATE INDEX IF NOT EXISTS idx_ra_outcome_pending
    ON reply_attributions (tenant_id, customer_id)
    WHERE outcome_checked_at IS NULL AND template_id <> '';

-- pack_stats 增两列终局率（小时任务 SyncPackStats 重算，供择臂与判优读取）
ALTER TABLE pack_stats ADD COLUMN IF NOT EXISTS arrive_rate DOUBLE PRECISION DEFAULT 0;
ALTER TABLE pack_stats ADD COLUMN IF NOT EXISTS deal_rate DOUBLE PRECISION DEFAULT 0;

COMMIT;
