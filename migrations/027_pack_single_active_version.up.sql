-- 027（残项5，2026-09-25）：一个行业包编码同时只允许一个上架版本。
--
-- 要修的事实：industry_packs 的 code 只有普通索引（model.IndustryPack 上 `index`），
-- 从来没有唯一约束，而把行置为 active 的三个写入点都没做过"同 code 兄弟让位"。
-- 其中主因是启动期 AutoRegisterLocalPacks：它扫 data/packs/*.aipack 时**对每个文件都
-- 强制 status=active**，于是同一包的三个历史版本文件同时上架。
-- 本机现场（2026-09-25 实查）：21 条 active 行、8 个编码各自多版本并存
--   auto 1.0.0/1.1.0/1.2.0、auto_rox 1.0.0/1.1.0/1.1.1、b2b/crossborder/ecom/edu/realty/wedding 各两版。
--
-- 为什么必须落到数据库层（口径同 013 会话单活跃）：
-- "同一编码只有一个上架版本"是一句**读侧要拿来当依据**的话——
-- 租户侧包列表按它决定"汽车"在下拉里出现几行，继承链按它决定给新租户物化哪一版内容，
-- 包质量归因按 pack_code+version 分样本。只改应用写入点，人工 SQL、旧版本进程、
-- 以及本次改造上线前的存量数据都能继续违反它；写侧代码与 DB 约束必须同时在。
--
-- 择新口径（与 internal/industrypack/version.go 的 NewerPackID **逐字同一套规则**）：
--   ① 版本号按点分数字段比数值序（1.10.0 > 1.9.0，字符串比较会判反，见 version.go 注释）；
--   ② 不可解析的版本号沉底（脏数据不许抢到上架位）；
--   ③ 完全同版本时 id 大者优先（后注册覆盖先注册，种子行 created_at 可能为空故不用时间）。
-- 这里给版本号算一个**可文本比较的定长键**：逐段 lpad 到 12 位再用 "." 连接。
-- 为什么不用 `string_to_array(version,'.')::int[]` 直接比（数组是逐元素比序的，看着正合适）：
-- **PostgreSQL 根本没有数组的 < / > 比较运算符**，那样写整条迁移在 42883 上直接失败。
-- 定长零填充把"逐段数值序"翻译成等宽文本的字典序：1.10.0 的键大于 1.9.0，
-- 而"1.2"是"1.2.0"的键的前缀，短者判小——与 Go 侧 CompareVersions 的口径逐字对齐。
-- 正则守卫放在 CASE 里：非数字版本（脏数据 "v1beta"）回落空串=沉底②，
-- 否则一条脏版本号会让整条 UPDATE 在 22P02（非法类型输入）上炸掉、迁移永久卡住。
BEGIN;

-- 1) 存量收拢：把每个 code 里"不是最高版本"的 active 行置为 disabled。
--    幂等（重复执行零副作用），且 disabled 行**不删**——包文件、历史绑定行（pack_id=2 的
--    auto 1.1.0 上还挂着 2 家租户的 applied_version）、包质量归因样本都按 id 引用它，
--    删行等于把历史版本链抹成悬空外键。下架才是正确动作：内容还在盘上，随时可回滚上架。
UPDATE industry_packs p
SET status = 'disabled'
WHERE p.status = 'active'
  AND EXISTS (
    SELECT 1
    FROM industry_packs k
    WHERE k.code = p.code
      AND k.status = 'active'
      AND k.id <> p.id
      AND (
        CASE WHEN k.version ~ '^[0-9]+(\.[0-9]+)*$'
             THEN (SELECT string_agg(lpad(x, 12, '0'), '.')
                   FROM unnest(string_to_array(k.version, '.')) AS x)
             ELSE '' END,
        k.id
      ) > (
        CASE WHEN p.version ~ '^[0-9]+(\.[0-9]+)*$'
             THEN (SELECT string_agg(lpad(x, 12, '0'), '.')
                   FROM unnest(string_to_array(p.version, '.')) AS x)
             ELSE '' END,
        p.id
      )
  );

-- 2) 不变式落库：同 code 至多一条 active。
--    部分索引（WHERE status='active'）而不是 (code,status) 全表唯一——
--    disabled 的多个历史版本必须能共存（回滚上一版就是把它重新上架），
--    被约束的只有"现役"这一格。AutoMigrate 建不出带 WHERE 的索引，只能写在这里。
--    上面的收拢若没清干净，这里会直接 23505（唯一键冲突）报错、迁移失败——这是刻意的：
--    约束建不上必须让人看见，绝不能降级成"能建就建"的软保证。
CREATE UNIQUE INDEX IF NOT EXISTS ux_pack_one_active_per_code
    ON industry_packs (code)
    WHERE status = 'active';

COMMIT;
