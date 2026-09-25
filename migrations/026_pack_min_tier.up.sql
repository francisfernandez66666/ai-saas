-- 026（G-22c 包可见档位判据，2026-09-24）：行业包声明"最低可绑档位"。
--
-- 要修的事实：/api/v1/admin/packs 把全部 active 包原样摊给任何租户，
-- /bind 也只查层级与父子关系，**从不看这家租户买的是什么档**。于是个人版租户
-- 在界面上看得见、也绑得上企业级行业包（含跨租户复用的私有话术），
-- 换包一次就把没付费的内容物化成自己的租户级模板 —— 这是商业化漏洞，不只是显示问题。
--
-- 判据放在**平台侧列**而不是包内 manifest：档位是销售口径（同一套汽车行业包
-- 可以先只对企业版开放），不该由包作者签字锁定；且包内容签名链改了就得重新打包签名。
--
-- 默认 '' = 不设门槛：现存 9 个包与全部种子行为零变化，生产上线后由超管按包逐个声明。
-- 取值域 personal/enterprise/custom（与 tenants.tier 同一套词表，见 model.Tenant.Tier），
-- 非法值在写入口（PUT /super/packs/:id/tier）拒绝，读侧遇到未知值按"拒绝绑定"处理
-- —— 档位判据 fail-open 等于没有判据。
BEGIN;

ALTER TABLE industry_packs ADD COLUMN IF NOT EXISTS min_tier VARCHAR(20) NOT NULL DEFAULT '';

-- 超管按档位盘点"哪些包对哪些档开放"时按此列过滤；包目录表行数少（十级），
-- 建索引的收益主要是让上面的查询走 index-only scan 而非顺序扫描。
CREATE INDEX IF NOT EXISTS idx_industry_packs_min_tier ON industry_packs (min_tier);

COMMIT;
