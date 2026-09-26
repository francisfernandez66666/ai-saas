-- 029（FIX-9 发票交付触达，2026-09-27）：把"发票开好之后有没有真的通知到客户"变成库里的持久事实。
--
-- 要修的事实：超管在 InvoiceTab 点「开具」、填了发票号，链路到此为止——
-- IssueInvoice（billing_refund.go:705）只把 invoice_status 置 issued、invoice_no 回录，
-- 全仓没有任何一处给 invoice_email 发过信（grep InvoiceIssued 在本迁移之前零命中）。
-- 客户在申请发票时是**主动填了邮箱**的（RequestInvoice 收 args[2] 落 invoice_email），
-- 也就是说客户留了收货地址而我们从没寄过东西：他只能自己反复回到收银台刷新，
-- 或者提工单问"我的发票到底开了没有"。开票是财务动作，动作完成了却不说，等于做了一半。
--
-- 为什么要落列而不是"发完就算"：本仓对"记了但没做"的账已有过教训（M1 静默吞账、
-- backup.sh 只打 WARN 就 exit 0）。一封外发邮件有三种失败方式——收件人邮箱是空的、
-- SMTP 压根没配（此时 notify 走 log 通道，**返回的是成功**）、 SMTP 配了但连接被拒。
-- 前两种在应用日志里都长得像"发过了"。只有把结果码写进这一行，超管台的发票列表
-- 才能把"已开具但客户没收到"单独挑出来人工补发，而不是等到客户投诉才发现。
-- 结果码是 enum 语义的字符串（smtp_sent / log_only / no_recipient / send_failed / send_timeout），
-- 刻意**不用 bool**：log_only 绝不能报成"已送达"，那正是"只会打日志并返回成功的守卫等于没有守卫"。
--
-- invoice_notified_at 与结果码分开存：时间是"最后一次尝试"的时刻，用于判断
-- "这封是今天补发的还是开票当时发的"；重发会同时刷新两列，所以两列都只描述最后一次动作。
--
-- ⚠ 列形态的口径与 028 完全一致，原因也一致：db.Init 是 **AutoMigrate 先跑、版本化迁移后跑**，
-- 新增字段会先被 GORM 建成"可空、无默认值"，本文件的 ADD COLUMN ... NOT NULL DEFAULT '' 整句空转
-- （列已存在，IF NOT EXISTS 直接跳过），于是约束看着加上了其实一条没落地。
-- 所以这里同样要**扳正**：NULL 归一成 ''、补默认值、再上不空约束，最后自证。
-- 而 GORM 每次启动都会把列形态往 struct 标签收敛——model.BillingOrder 上这两列的标签
-- 必须同步带 `not null;default:''`，**两边口径必须一起改**（028 实测抓到的那次翻车不再重复）。
BEGIN;

-- 1) 结果码列（新库由本句建，老库由下面的 ALTER 修正 GORM 建出的可空无默认形态）
ALTER TABLE billing_orders
    ADD COLUMN IF NOT EXISTS invoice_notify_result VARCHAR(24) NOT NULL DEFAULT '';

COMMENT ON COLUMN billing_orders.invoice_notify_result IS
    '开票后触达客户的最后一次外呼结果码：smtp_sent/log_only/no_recipient/send_failed/send_timeout；空=尚未尝试';

-- 1b) 通知时刻列（可空是刻意的：空 = 从未尝试，与 '' 结果码同义，不需要默认值）
ALTER TABLE billing_orders
    ADD COLUMN IF NOT EXISTS invoice_notified_at TIMESTAMPTZ;

COMMENT ON COLUMN billing_orders.invoice_notified_at IS
    '最后一次开票触达尝试的时刻（含失败）；空=从未尝试';

-- 1c) 扳正 GORM 先建出的可空列：先归一 NULL，再补默认值，最后上不空约束。
--     顺序不可反：有 NULL 存量时 SET NOT NULL 会直接失败（这是我们要的——不许静默丢约束）。
UPDATE billing_orders SET invoice_notify_result = '' WHERE invoice_notify_result IS NULL;
ALTER TABLE billing_orders ALTER COLUMN invoice_notify_result SET DEFAULT '';
ALTER TABLE billing_orders ALTER COLUMN invoice_notify_result SET NOT NULL;

-- 1d) 自证：本列必须真的是"非空 + 有默认值"，否则说明上面的扳正没生效（例如换了执行顺序），
--     宁可迁移失败，也不要留一个"看起来加了列、其实约束没上"的账本记录。
DO $$
DECLARE
    bad INTEGER;
BEGIN
    SELECT count(*) INTO bad
    FROM information_schema.columns
    WHERE table_name = 'billing_orders'
      AND column_name = 'invoice_notify_result'
      AND (is_nullable = 'YES' OR column_default IS NULL);
    IF bad <> 0 THEN
        RAISE EXCEPTION '迁移029：invoice_notify_result 列未落成 NOT NULL DEFAULT %（AutoMigrate 与迁移的执行顺序或扳正语句失效）', '''';
    END IF;
END
$$;

COMMIT;
