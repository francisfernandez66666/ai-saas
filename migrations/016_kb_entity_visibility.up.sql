-- 016 PLAN_FIX_2026-09-21 B2：知识库主实体可见性列（brands / car_models / competitor_compares）。
-- 背景：014（P2-4）只给 knowledge_fragments 加了 visibility 并把公开搜索收敛为
-- publicOnly，同在 routes_public 免鉴权组挂载的 brands / models / models/:id / compares
-- 四个端点**没有可见性概念**，匿名仍可全量拉取商家产品目录与竞品优劣对比话术
-- （CompetitorCompare.Content）——同类点漏改。本迁移把 014 的口径补齐到主实体。
-- 口径（与 014 完全一致）：visibility ∈ {public, private}，默认 private（fail-closed，
-- 新条目不显式设置即对公开面不可见）；回填 tenant_id=0 的系统预置目录为 public。
-- 边界：model_specs 不单独设列——规格参数随所属车型返回，可见性跟随 car_models。
ALTER TABLE brands
    ADD COLUMN IF NOT EXISTS visibility VARCHAR(10) NOT NULL DEFAULT 'private';

UPDATE brands SET visibility = 'public' WHERE tenant_id = 0;

CREATE INDEX IF NOT EXISTS idx_brand_visibility ON brands (tenant_id, visibility);

ALTER TABLE car_models
    ADD COLUMN IF NOT EXISTS visibility VARCHAR(10) NOT NULL DEFAULT 'private';

UPDATE car_models SET visibility = 'public' WHERE tenant_id = 0;

CREATE INDEX IF NOT EXISTS idx_carmodel_visibility ON car_models (tenant_id, visibility);

ALTER TABLE competitor_compares
    ADD COLUMN IF NOT EXISTS visibility VARCHAR(10) NOT NULL DEFAULT 'private';

UPDATE competitor_compares SET visibility = 'public' WHERE tenant_id = 0;

CREATE INDEX IF NOT EXISTS idx_compare_visibility ON competitor_compares (tenant_id, visibility);
