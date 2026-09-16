-- 013 down：仅摘除唯一索引。存量关账不逆转（同 012 口径：无法区分本次关闭与
-- 人工关闭的历史，重新点亮死会话风险远大于收益）。
DROP INDEX IF EXISTS ux_conv_one_active;
