-- 015 E4(2026-09-19 增强批)：话术模板 A/B 实验列。
-- 背景：话术优化此前只能"改了全量生效"，无灰度对照手段；D9 包质量闭环已按 template_id
-- 归因接钩/留资，缺的只是"同一触发条件下多 variant 按客户稳定分桶各跑一份"。
-- 口径：ab_group 空串=不参与实验（现状行为零漂移）；ab_weight=组内分流权重（0 视为不参与，
-- 全 0 回退确定性最高分）。Status 语义扩展 2=草稿（引擎 LoadData 只加载 status=1，天然不进召回池）。
ALTER TABLE templates
    ADD COLUMN IF NOT EXISTS ab_group  VARCHAR(50) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS ab_weight INT         NOT NULL DEFAULT 0;

-- 半部分索引：只给"在实验中"的行建索引，预置/普通话术不占空间
CREATE INDEX IF NOT EXISTS idx_templates_ab_group ON templates (ab_group) WHERE ab_group <> '';
