-- 023 商机与报价（2026-09-23 商机批）：两张表 + 两条部分唯一索引 + 两条查询索引。
--
-- 要解决的业务问题：客户身上只有一个写着"已成交"的阶段字符串，没有金额、没有报价单、
-- 也没有"这张单子在报价环节卡了 20 天"。销售管理者因此永远只看得到**结果**，
-- 看不到**过程**——而业绩恰恰是在过程里丢掉的。商机管道 + 报价版本序列就是把过程变成数据。
--
-- 与 GORM 的关系（口径同 020/021/022）：建表真源同时在 internal/model/deal.go
-- （AutoMigrate 先建列），本文件用 IF NOT EXISTS 幂等共存。
-- 本文件**独有且必须在这里**的东西是带 WHERE 的部分索引：AutoMigrate 建不出来，
-- 而"一个客户同时只能有一张在途商机"这条不变式只有 DB 层能兜住（见下）。
BEGIN;

-- ============================================================
-- 1) opportunities：商机（销售管道的一行，终局后即为历史）
--
-- 为什么不存 owner_user_id：单子的归属就是客户的归属（assigned_user_id）。
-- 快照一份到商机上，客户改派后就会出现"单子还挂在离职顾问名下、新顾问看不见"的孤儿单——
-- 这是销售管道最不能忍的错账。读侧一律按客户当前归属裁剪（见 internal/deal.VisibleDeals）。
--
-- 为什么金额是分（amount_cents）：与 billing_orders.amount_cents 同量纲。
-- 项目里所有"钱"都是分，只有 token 成本用微元；同一套表混两种口径迟早有人把 1 万当 1 百万统计。
--
-- stage_entered_at 是"在这一步停了几天"的唯一依据：用 updated_at 判停滞会被任何一次
-- 备注编辑冲掉，等于停滞永远为零——看板上一片绿，卡住的单子恰好是最该看见的那几张。
-- ============================================================
CREATE TABLE IF NOT EXISTS opportunities (
    id            BIGSERIAL PRIMARY KEY,
    tenant_id     BIGINT       NOT NULL DEFAULT 0,
    customer_id   BIGINT       NOT NULL,               -- 挂在哪个客户身上（可见性由它的归属决定）
    title         VARCHAR(120) NOT NULL DEFAULT '',    -- 单子名称，如"XT5 两台大客户单"
    stage         VARCHAR(24)  NOT NULL DEFAULT 'qualified', -- lead|qualified|quoted|negotiating|won|lost
    stage_entered_at TIMESTAMPTZ,                      -- 进入当前阶段的时刻（停滞天数依据）
    amount_cents  BIGINT       NOT NULL DEFAULT 0,     -- 预计金额（分），0=还没问到价
    source        VARCHAR(24)  NOT NULL DEFAULT 'manual', -- manual|acquisition|ai
    source_code   VARCHAR(16)  NOT NULL DEFAULT '',    -- 活码归因快照（码停用后照样读，口径同 022）
    expected_close_at TIMESTAMPTZ,                     -- 预期成交日，可空（不硬塞默认值）
    won_at        TIMESTAMPTZ,
    lost_at       TIMESTAMPTZ,
    lost_reason   VARCHAR(24)  NOT NULL DEFAULT '',    -- price|competitor|no_budget|no_decision|timeout|other
    created_at    TIMESTAMPTZ,
    updated_at    TIMESTAMPTZ
);

-- **一个客户同时至多一张在途商机**（口径同 G1 会话单活跃 / 迁移 013）。
-- 只靠代码判重，多实例并发建单就会撞出两张，而"在途商机数"与"有在途单的客户数"
-- 是漏斗上的两个格子——它们不相等时没人能解释为什么。
-- 索引**只覆盖非终局行**：所以单子作废/流失后可以重开一张新的，历史那张照原样留着当事实。
CREATE UNIQUE INDEX IF NOT EXISTS ux_deal_one_open_per_customer
    ON opportunities (tenant_id, customer_id)
    WHERE stage NOT IN ('won', 'lost');

-- 管道视图与停滞榜按 (租户, 阶段) 取、按 stage_entered_at 排序：
-- 没这条索引，看板每次刷新顺序扫全表，而它是后台默认页。
CREATE INDEX IF NOT EXISTS idx_deal_tenant_stage
    ON opportunities (tenant_id, stage, stage_entered_at);

-- 客户详情/OneID 合并后按客户回查单子。
CREATE INDEX IF NOT EXISTS idx_deal_customer
    ON opportunities (customer_id);

-- ============================================================
-- 2) quotes：报价单（同商机内版本递增，改版不覆盖旧版）
--
-- 为什么单独一张表：一条商机的阶段只有一个，但可以出过 N 版报价。
-- 合表就只能"覆盖"，而**客户手里那张是几月几日哪一版**是商务纠纷时要回答的问题——
-- 覆盖掉旧版等于把证据抹了。作废/被取代都是历史事实，行永不删除（口径同活码只启停）。
--
-- lines 存 JSON 文本而不是明细表：报价明细是**整张单据的正文**，永远整读整写，
-- 没有人会跨单据聚合"某个项目的总销量"（要做的话是报表域另一回事）。
-- 为一列从不下钻的东西建表，只会多一次 join 和一处能写坏的外键。
-- ============================================================
CREATE TABLE IF NOT EXISTS quotes (
    id             BIGSERIAL PRIMARY KEY,
    tenant_id      BIGINT      NOT NULL DEFAULT 0,
    opportunity_id BIGINT      NOT NULL,
    version        INTEGER     NOT NULL DEFAULT 1,     -- 第几版（1 起，改版 +1）
    lines          TEXT        NOT NULL DEFAULT '[]',  -- 明细 JSON（小计/合计后端回填，前端传来的合计不作数）
    total_cents    BIGINT      NOT NULL DEFAULT 0,     -- 合计（分）
    status         VARCHAR(16) NOT NULL DEFAULT 'draft', -- draft|sent|accepted|declined|superseded|void|expired
    valid_until    TIMESTAMPTZ,                        -- 有效期，NULL=不设到期
    created_by     BIGINT      NOT NULL DEFAULT 0,     -- 开单人（审计维度，不参与可见性）
    sent_at        TIMESTAMPTZ,
    decided_at     TIMESTAMPTZ,                        -- 客户接受/拒绝的时刻
    note           VARCHAR(255) NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ,
    updated_at     TIMESTAMPTZ
);

-- **一张商机同时至多一张在途报价**（draft/sent）。两张"待发出"的草稿会让人分不清
-- 到底哪张发给过客户；已接受/拒绝/作废/被取代都是终局行，不参与这条约束，历史照留。
CREATE UNIQUE INDEX IF NOT EXISTS ux_quote_one_open_per_deal
    ON quotes (opportunity_id)
    WHERE status IN ('draft', 'sent');

-- 按商机取版本序列（详情页要显示"一共出过几版"）。
CREATE INDEX IF NOT EXISTS idx_quote_deal ON quotes (opportunity_id);

-- 过期 sweep 的取数面：只扫"已发出且还没决定"的行。
-- 全量索引会给七态里五个终局态白建索引，而这正是行数占绝大多数的部分。
CREATE INDEX IF NOT EXISTS idx_quote_due
    ON quotes (tenant_id, valid_until)
    WHERE status = 'sent';

COMMIT;
