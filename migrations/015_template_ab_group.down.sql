-- 015 回滚：删除 A/B 实验列（召回回到"最高分唯一定位"旧语义）
DROP INDEX IF EXISTS idx_templates_ab_group;
ALTER TABLE templates DROP COLUMN IF EXISTS ab_group;
ALTER TABLE templates DROP COLUMN IF EXISTS ab_weight;
