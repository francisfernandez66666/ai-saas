-- 028（FIX-1 退款链路，2026-09-26）：把"实际发给 PSP 的退款单号"变成库里的持久事实。
--
-- 要修的事实：出款这一步的"发起"与"结果回写"跨事务、跨进程（MarkOrderRefunded 事务 commit
-- 与 executeRefundPayout 之间本就允许崩），而三个 provider（wechat_pay.go:94 / alipay_pay.go:174 /
-- billing_service.go:146）都是 `"RF" + 秒级时间戳 + 订单号` **现场拼号**、拼完即丢。
-- 于是 billing_refund.go:23 那句"幂等依赖 PSP 侧按 out_refund_no 去重"根本不成立：
-- 同一订单补呼两次就是两个号，PSP 侧无从去重；而"这单到底用哪个号出过款"在库里查不到，
-- 主动查单也无从发起（查单的入参就是退款单号）。
--
-- 为什么把约束落到数据库层（口径同 013 会话单活跃 / 027 同编码单上架版本）：
-- "一单一个退款号"是**对账要拿来当依据**的一句话——财务拿号去 PSP 后台核、查单任务拿号去问结果。
-- 只改应用写入点，人工 SQL、旧版本进程、以及上线前的存量行都能继续违反它。
-- 部分唯一索引（WHERE refund_out_no <> ''）而不是全表唯一：绝大多数订单从未退款、
-- 该列为空，全表唯一会让第二笔未退款订单直接建不上。
--
-- 执行顺序有一个坑，必须写在这里（2026-09-26 本机实测抓到）：
-- internal/db/database.go 的口径是 **AutoMigrate 先跑、版本化迁移后跑**，所以 model.BillingOrder
-- 上新增的字段会先被 GORM 建成"可空、无默认值"的列，本文件的
-- `ADD COLUMN IF NOT EXISTS ... NOT NULL DEFAULT ''` 于是整句空转（列已存在，IF NOT EXISTS 直接跳过），
-- 想要的不空约束与默认值一个都没落地——而索引照样建成，看起来"迁移成功了"。
-- 因此这里不只要"建"，还要**把 GORM 建歪的那一列扳回来**：NULL 归一成 ''、补默认值、再上不空约束。
--
-- ⚠ 扳正不是一次性的：GORM 每次启动都会把列形态往 struct 标签上收敛。
-- 只写 `gorm:"size:64"` 时，本文件刚 SET NOT NULL，下一次重启就被扳回可空——
-- 而索引还在、日志照样报成功，于是"DB 层一单一号"退化成"只有非空号才被约束"
-- （NULL 行既不受部分唯一索引管，又因为 NULL = '' 为假而永远取不到补呼）。
-- 2026-09-26 冒烟 §四十三 首跑就是红的（实测读到 is_nullable=YES），修法在
-- model.BillingOrder.RefundOutNo 的标签里同步写 `not null;default:''`——**两边口径必须一起改**。
--
-- 刻意**不回填**历史行：当年真正发出的号里带秒级时间戳，我们从未存过，谁也不知道是哪一串。
-- 把确定性的 "RF{订单号}" 回填进去等于凭空造一条"库里说发过这个号、PSP 侧其实没有"的假账——
-- 这比对账查不到更危险。历史行留空，由 tools/audit_refund_psp_backfill.sh 列出交财务逐笔确认，
-- 确认后要么填真实号、要么走人工出款核销，两条路都留得下证据。
BEGIN;

-- 1) 列（新库由本句建，老库由下面的 ALTER 修正 GORM 建出的可空无默认形态）
ALTER TABLE billing_orders
    ADD COLUMN IF NOT EXISTS refund_out_no VARCHAR(64) NOT NULL DEFAULT '';

COMMENT ON COLUMN billing_orders.refund_out_no IS
    '实际发给 PSP 的退款单号（RF+订单号，一经写入不再改）；空=从未发起出款或修复前的历史行';

-- 1b) 扳正 GORM 先建出的可空列：先归一 NULL，再补默认值，最后上不空约束。
--     顺序不可反：有 NULL 存量时 SET NOT NULL 会直接失败（这是我们要的——不许静默丢约束）。
UPDATE billing_orders SET refund_out_no = '' WHERE refund_out_no IS NULL;
ALTER TABLE billing_orders ALTER COLUMN refund_out_no SET DEFAULT '';
ALTER TABLE billing_orders ALTER COLUMN refund_out_no SET NOT NULL;

-- 1c) 自证：本列必须真的是"非空 + 有默认值"，否则说明上面的扳正没生效（例如换了执行顺序），
--     宁可迁移失败，也不要留一个"看起来加了列、其实约束没上"的账本记录。
DO $$
DECLARE
    bad INTEGER;
BEGIN
    SELECT count(*) INTO bad
    FROM information_schema.columns
    WHERE table_name = 'billing_orders'
      AND column_name = 'refund_out_no'
      AND (is_nullable = 'YES' OR column_default IS NULL);
    IF bad <> 0 THEN
        RAISE EXCEPTION '迁移028：refund_out_no 列未落成 NOT NULL DEFAULT %（AutoMigrate 与迁移的执行顺序或扳正语句失效）', '''';
    END IF;
END
$$;

-- 2) 不变式落库：一个退款单号至多挂在一笔订单上。
--    若因异常数据建不上，这里直接 23505 让迁移失败——这是刻意的：
--    约束建不上必须让人看见，绝不能降级成"能建就建"的软保证。
CREATE UNIQUE INDEX IF NOT EXISTS ux_order_refund_out_no
    ON billing_orders (refund_out_no)
    WHERE refund_out_no <> '';

COMMIT;
