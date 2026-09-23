-- 022 获客活码（2026-09-23 获客批）：两张表 + 客户首触归因列 + 三条索引。
--
-- 要解决的业务问题：销售在抖音/小红书/门店海报/朋友圈各放一个入口，钱花出去了，
-- 但系统里只有一句"来源=外部体验"（/chat/guest 的写死默认值）。结果是
-- **没有任何人能回答"哪个渠道值得继续投钱"**——活码就是把这句话回答出来的最小闭环：
-- 一个码 = 一个渠道位，扫码进来的人被打上这个码，之后的开口/留资/到店/成交都记在它名下。
--
-- 与 GORM 的关系（口径同 020/021）：建表真源同时在 internal/model/acquisition.go
-- （AutoMigrate 先建列），本文件用 IF NOT EXISTS 幂等共存。
-- 本文件**独有且必须在这里**的东西是部分索引与"非空才建索引"的归因列：
-- AutoMigrate 建不出带 WHERE 的索引，而这里三条查询缺了部分索引就得顺序扫全表。
BEGIN;

-- ============================================================
-- 1) acquisition_codes：活码本体（一个码一行，只启停不删除）
--
-- 为什么**不提供删除**：码一旦印上海报/投出去就收不回来，删掉行会让历史归因变成
-- 指向空气的外键，"这个渠道带来多少成交"永久查不出来；更糟的是码字符串被回收后
-- 再建一个同名码，两拨不相干的客户会被合并统计——错账比缺账难查得多。
-- 所以这里只支持 active/disabled 两态，字符串全库唯一且永不复用。
-- ============================================================
CREATE TABLE IF NOT EXISTS acquisition_codes (
    id             BIGSERIAL PRIMARY KEY,
    tenant_id      BIGINT      NOT NULL DEFAULT 0,
    code           VARCHAR(16) NOT NULL,              -- 公开短码（crypto/rand，不可枚举）
    name           VARCHAR(100) NOT NULL DEFAULT '',  -- 用途名，如"门店前台立牌"
    channel        VARCHAR(30)  NOT NULL DEFAULT '',  -- 渠道位：抖音/小红书/微信/百度/门店/其它
    owner_user_id  BIGINT       NOT NULL DEFAULT 0,   -- 建码人（**只是统计维度，不参与客户分配**）
    status         VARCHAR(20)  NOT NULL DEFAULT 'active', -- active|disabled
    remark         VARCHAR(255) NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ,
    updated_at     TIMESTAMPTZ
);

-- 码字符串全库唯一（不是租户内唯一）：公开解析链路只拿得到码本身，
-- 拿不到租户，必须单键定位到唯一的行——这也是"跨租户码"能被识别出来的前提。
CREATE UNIQUE INDEX IF NOT EXISTS ux_acq_code_global ON acquisition_codes (code);

-- 后台列表按租户取、且默认只看启用的：没有这条索引，租户列表页每次顺序扫全表。
CREATE INDEX IF NOT EXISTS idx_acq_code_tenant ON acquisition_codes (tenant_id, status);

-- ============================================================
-- 2) acquisition_scans：扫码事件（漏斗最上面那一层）
--
-- 为什么单独一张事件表、而不是只往 customers 上打一列就完事：
-- "扫了但一句话没说"的人恰恰是渠道质量最诚实的信号——只记到客户表，
-- 就永远看不出"这个码带来 500 次扫码、只有 3 个人开口"这种该停投的渠道。
--
-- customer_id=0 / visitor_key='' 的行表示"扫了但还没领身份"（落地页被打开）。
-- ============================================================
CREATE TABLE IF NOT EXISTS acquisition_scans (
    id          BIGSERIAL PRIMARY KEY,
    tenant_id   BIGINT      NOT NULL DEFAULT 0,
    code_id     BIGINT      NOT NULL,
    customer_id BIGINT      NOT NULL DEFAULT 0,  -- 0=只扫码未建客
    visitor_key VARCHAR(64) NOT NULL DEFAULT '', -- 已领身份的访客串同一人（去重锚），''=纯打开
    created_at  TIMESTAMPTZ
);

-- 同一访客短期内重复扫码只算一次（详见 acquisition.ScanDedupeWindow）：
-- 这条查询永远是"取该 (码,访客) 的最近一条"，所以做成**按时间倒序的部分索引**，
-- 且只索引带访客键的行——纯打开的匿名行没有去重依据，把它们塞进索引只会让索引变大。
CREATE INDEX IF NOT EXISTS idx_acq_scan_dedupe
    ON acquisition_scans (code_id, visitor_key, created_at DESC)
    WHERE visitor_key <> '';

-- 渠道漏斗统计按 (租户, 码) 时间段取数。
CREATE INDEX IF NOT EXISTS idx_acq_scan_code_time
    ON acquisition_scans (tenant_id, code_id, created_at);

-- ============================================================
-- 3) customers.acquisition_code：客户首次扫码归因（粘性的首触归因）
--
-- 为什么存字符串而不是 code_id：归因要能回答"这个成交客户当初是从哪张码进来的"，
-- 而客户表是各条业务链路（导出/名单/贡献度）都要读的宽表，带字符串少一次 join，
-- 且码停用后历史归因照样可读（停用不改字符串，见上）。
-- 为什么"首触"而不是"最近触达"：客户先扫门店立牌、三天后又扫销售个人码，
-- 若按最近触达归因，所有渠道钱都会流向最后那个触点，等于把门店的钱记到个人头上。
-- 写入侧因此只在**空值时**写一次（见 acquisition.ApplyToGuest）。
-- ============================================================
ALTER TABLE customers ADD COLUMN IF NOT EXISTS acquisition_code VARCHAR(16) NOT NULL DEFAULT '';

-- 按码取客户名单（下钻与导出）：绝大多数客户这列是空的（自然流量/老客转介绍），
-- 全量索引等于给一列空值建索引，故只索引非空行。
CREATE INDEX IF NOT EXISTS idx_customers_acq_code
    ON customers (tenant_id, acquisition_code)
    WHERE acquisition_code <> '';

COMMIT;
