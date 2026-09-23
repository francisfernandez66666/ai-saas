-- 021 down：撤销 D3 用量预警与催缴两张表。
-- 回退安全前提：两张表都只存"我们对外说过什么话"的留痕与调度状态，
-- 删表不动 tenants（配额、status、expired_at 全在自己列上）、不动 orders/usage_ledger（钱与账），
-- 也不动 grace_period_end_at——那列本轮被 dunning 写成"宽限期截止"，回退后它会留在最后一次的值上，
-- 属可接受的陈旧值（列在 model.Tenant 里本就存在，不在本迁移的增删范围内，故此处不重置它）。
-- 真正丢的是去重锚：回退后再前滚，同一账期已发过的预警会再发一次，
-- 这是运维知情选择（回退本身就是重放窗口），故不额外做备份。
BEGIN;
DROP INDEX IF EXISTS idx_billing_dunning_due;
DROP INDEX IF EXISTS ux_billing_dunning_tenant;
DROP TABLE IF EXISTS billing_dunning;

DROP INDEX IF EXISTS idx_usage_alert_tenant_period;
DROP INDEX IF EXISTS ux_usage_alert_once;
DROP TABLE IF EXISTS usage_alerts;
COMMIT;
