-- 026 down：撤销行业包的档位门槛列与索引。
--
-- 回退代价：min_tier 上存的是"这个包只对哪一档以上开放"的销售约定，删列即全部回到
-- "任何人可绑任何包"（G-22c 修复前的状态）。回退前建议先留档：
--   COPY (SELECT code, version, pack_level, min_tier FROM industry_packs WHERE min_tier <> '')
--     TO STDOUT WITH CSV HEADER
-- 代码侧须同时退回不读该列的版本，否则旧包目录查询会因缺列报 42703。
BEGIN;

DROP INDEX IF EXISTS idx_industry_packs_min_tier;
ALTER TABLE industry_packs DROP COLUMN IF EXISTS min_tier;

COMMIT;
