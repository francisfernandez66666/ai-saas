-- 016 回滚：删除三张知识库主表的可见性列（公开端点随之回到"仅租户过滤"旧语义）
DROP INDEX IF EXISTS idx_brand_visibility;
ALTER TABLE brands DROP COLUMN IF EXISTS visibility;

DROP INDEX IF EXISTS idx_carmodel_visibility;
ALTER TABLE car_models DROP COLUMN IF EXISTS visibility;

DROP INDEX IF EXISTS idx_compare_visibility;
ALTER TABLE competitor_compares DROP COLUMN IF EXISTS visibility;
