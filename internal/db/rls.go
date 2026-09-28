// Package db 承载连接池、迁移、租户查询作用域与行级安全策略。
package db

// ============================================================
// 多租户行级隔离 RLS（P2 RLS，2026-08-29）
//
// 定位：DB 层租户隔离兜底（与业务层 db.RQ/c.PQ fail-closed 双重保险）。
// 策略（休眠式）：默认 app.current_tenant 未设置 → current_setting 返回 NULL →
//   USING 表达式 OR NULL IS NULL 恒真 → 全表可见，与现状完全等价（零行为变更）。
// 激活方式：在事务内 SET LOCAL app.current_tenant='<tid>' 后查询即被 DB 强制按租户收敛
//   （见 SetTenantRLS）。ENABLE ROW LEVEL SECURITY 让策略真正被咨询，FORCE ROW LEVEL SECURITY
//   再确保即便表 owner 也受策略约束——**两者缺一不可**（FIX-A 2026-09-28：旧实现只写 FORCE，
//   而 PG 语义是未 ENABLE 时策略永不被咨询，整套"DB 级兜底"从未真生效）。
// 仅对确含 tenant_id 的业务表启用；平台级表（tenants/system_configs/…）不在列，避免误伤跨租户管理。
// ============================================================

import (
	"database/sql"
	"fmt"
	"log"
	"strings"

	"ai-scrm/config"
	"gorm.io/gorm"
)

// rlsTenantTables 启用行级隔离的租户业务表清单（均含 tenant_id 列）
// 仅对确含 tenant_id 的业务表启用；平台级表（tenants/system_configs/…）不在列，避免误伤跨租户管理
//
// 设计原则：
// 1. 休眠式设计：默认 app.current_tenant 未设置时策略恒真，零行为变更
// 2. 激活方式：事务内 SET LOCAL app.current_tenant='<tid>' 后查询即被 DB 强制按租户收敛
// 3. 表清单完整性：启动时与 information_schema 比对，表名不匹配即 Fatal
//
// P1-5 修复(2026-09-09)：修正错误表名（followups→follow_ups、feedback→feedbacks），
// 补齐 usage_ledger / cdp_tag_assignments / cdp_tag_definitions / event_logs / id_mappings /
// inbox_events / flow_state_machines / templates / features——此前 RLS_ENABLED=true 时这些
// 核心表静默无策略保护。
//
// G-14 补全(2026-09-11)：补齐遗漏表（组织/身份/API/流程/标签/行业包相关），
// 确保 RLS_ENABLED=true 时所有含 tenant_id 的表都有策略保护
var rlsTenantTables = []string{
	// ---- 核心业务表 ----
	"customers",             // 客户表（核心业务实体）
	"conversations",         // 会话表（客户对话记录）
	"messages",              // 消息表（对话消息明细）
	"knowledge_fragments",   // 知识片段表（AI知识库）
	"kb_feedback_materials", // 知识库反馈素材表
	"feedbacks",             // 用户反馈表（含满意度评分）
	"follow_ups",            // 跟进记录表
	"test_drives",           // 试驾记录表
	"customer_tags",         // 客户标签关联表
	"agreement_signatures",  // 协议签署表
	"reward_claims",         // 邀请奖励领取表

	// ---- CDP 客户数据平台表 ----
	"cdp_profiles",        // CDP 客户档案表
	"cdp_tag_definitions", // CDP 标签定义表
	"cdp_tag_assignments", // CDP 标签分配表（客户←→标签关联）

	// ---- 事件/消息/流程表 ----
	"event_logs",          // 事件日志表（业务事件审计）
	"id_mappings",         // ID 映射表（OneID 身份归一）
	"inbox_events",        // 收件箱事件表
	"flow_state_machines", // 流程状态机表

	// ---- 模板/功能/行业包表 ----
	"templates",            // 模板表（话术/文案模板）
	"features",             // 功能开关表
	"tenant_pack_bindings", // 租户行业包绑定表（G-24）
	"billing_orders",       // 计费订单表

	// ---- 用量/审计表 ----
	"usage_records",     // 用量记录表（旧版）
	"usage_ledger",      // 用量台账表（M3 计费底座）
	"usage_flush_retry", // 计量扣减挂账表（P0-1，2026-09-20 审计批）
	"tenant_audit_logs", // 租户审计日志表

	// ---- G-14 补齐：组织/身份/API/流程/标签/行业包相关表 ----
	"departments",           // 部门表（组织架构）
	"customer_identities",   // 客户身份表（多身份归一）
	"api_keys",              // API 密钥表（OpenAPI 鉴权）
	"flow_instances",        // 流程实例表
	"message_event_records", // 消息事件记录表（MQ 审计）
	"dept_pack_bindings",    // 部门行业包绑定表
	"tags",                  // 标签定义表
	"tag_rules",             // 打标规则表（关键词/抗性/意向匹配）
	"tag_weight_mappings",   // 标签权重映射表（标签→T向量驱动）
	"flow_definitions",      // 流程定义表
	"brands",                // 品牌表（汽车行业）
	"car_models",            // 车型表（汽车行业）
	"model_specs",           // 车型配置表（汽车行业）
	"competitor_compares",   // 竞品对比表（汽车行业）

	// P1-8 补全(2026-09-15 复核批)：通道/隐私/归因/审计域静默漏保护表——
	// 旧清单只覆盖到 2026-09-11 前的表，其后新增的租户表全部没进（见反向 diff 说明）。
	// 注意：system_configs 按设计不入清单（平台表，tenant_id=0 系统层+租户覆盖双层，
	// 激活 RLS 会切断租户读系统层默认值的休眠旁路，AGENTS"平台级配置"语义依赖）。
	"tenant_users",         // 租户用户表（密码哈希/token_version 所在）
	"channels",             // 通道接入表（AES-GCM 凭据密文）
	"channel_identities",   // 渠道身份映射表（OneID 桥）
	"channel_inbound_msgs", // 通道入站幂等表（消息原文摘要）
	"channel_outbound",     // 通道出站队列/死信表（消息正文）
	"tenant_webhooks",      // 出站 webhook 配置表（含密文 secret）
	"webhook_deliveries",   // webhook 投递记录表（事件载荷快照）
	"deletion_requests",    // PIPL 删除权请求表（主体标识）
	"messages_archive",     // messages 冷数据归档表（迁移011，含原文）
	"reply_attributions",   // D9 回复归因表（意向/会话回溯）
	"pack_stats",           // D9 包效果统计表
	"outreach_tasks",       // 主动触达任务表（触达最小闭环 2026-09-23，含触达正文）
	// D3(2026-09-23)：预警留痕与催缴状态机。二者都含 tenant_id 且承载"我们对这家说过什么、
	// 它欠费到什么程度"——属租户商业隐私，必须进清单（漏进则启动日志刷"清单外表"WARN，
	// 且 RLS_ENABLED 部署形态下这两表将无策略覆盖）。
	"usage_alerts",    // 用量预警投递留痕表（配额水位快照）
	"billing_dunning", // 到期催缴状态机表（欠费进度、封禁时刻）
	// G-14 收口(2026-09-24 欠账批)：获客/商机/存档三批新表漏登记——**逐张都是含 tenant_id 的
	// 租户商业数据**（活码是渠道归因资产、商机与报价是对外承诺与成交价、存档留痕是客户会话原文）。
	// 此前只靠启动日志刷"清单外表"WARN，无人主动看即永久静默；本批同时补 rls_coverage_test.go
	// 把"清单必须覆盖 information_schema 全部含 tenant_id 表（显式豁免除外）"变成 CI 断言，
	// 新表漏登记从"日志里的一行 WARN"转为"测试直接红"。
	"acquisition_codes",    // 获客活码表（迁移022，短码/落地页/归因渠道）
	"acquisition_scans",    // 活码扫码明细表（迁移022，visitor_key 级归因）
	"opportunities",        // 商机主表（迁移023，金额/阶段/流失归因）
	"quotes",               // 报价版本链表（迁移023，对外承诺明细）
	"chat_archive_records", // E8 会话存档留痕表（迁移024，解密后的会话正文）
}

// RLSStatusInfo RLS 实际生效形态（P2-2 批三 2026-09-20：readiness 观测位数据源）。
// 背景：策略在 app.current_tenant 未设置时恒真放行（休眠式设计，后台任务依赖），且 PG 对
// SUPERUSER/BYPASSRLS 无条件旁路——"RLS_ENABLED=true"≠"DB 级隔离恒成立"，此前极易误当兜底。
// 该结构把真实形态显式暴露给 /status，纠偏口径见 metrics.ComputeReadiness。
type RLSStatusInfo struct {
	Enabled bool // 是否已随 RLS_ENABLED 建策略（EnableRLS 走完创建流程）
	Bypass  bool // 连接角色为 SUPERUSER/BYPASSRLS：PG 无条件旁路，启用也形同虚设
	Tables  int  // 已挂策略的租户表数
	// EnableVerified 真查 pg_class.relrowsecurity 得到的"确实已 ENABLE"表数（FIX-A 2026-09-28）。
	// 之所以单独设这一位而不是复用 Tables：PG 语义是 relrowsecurity=false 时策略**永不被咨询**，
	// "清单条数/策略挂上数"与"实际生效数"是两个数——旧实现把前者当后者报，实测 14 张表挂了策略
	// 而 relrowsecurity 全为 false，DB 级隔离从未真生效。-1 表示验证查询失败（未能验证，不是 0）。
	EnableVerified int
}

// rlsStatus 进程内状态快照（EnableRLS 只在启动跑一次，读写无并发窗口）
var rlsStatus = RLSStatusInfo{}

// GetRLSStatus 返回 RLS 生效形态快照（未调 EnableRLS / 未启用时为零值 Enabled=false）
func GetRLSStatus() RLSStatusInfo { return rlsStatus }

// RLSPolicyFaceInfo RLS「策略面」快照（FIX-A 2026-09-28 可观测位数据源）。
//
// 为什么在 EnableVerified 之外还要单独看策略面：通电（relrowsecurity=true）只保证
// "PG 会咨询策略"，策略内容本身对不对没人看。本结构的判据是"已 ENABLE **且**挂了
// tenant_isolation 策略"的表里，有几张的 USING 表达式缺了休眠腿——缺了不是"更严"，
// 而是那条连接上休眠态被筛成空集 / 写入撞 42501（空串复位缺陷的现场形态）。
// 与 031 迁移同源：031 按同样的判据决定"哪张表要重建策略"，观测位与迁移判据分叉
// 就是"日志说没事、数据其实没修"的第二个版本，故两侧共用 rlsPolicyHasDormantLegs。
type RLSPolicyFaceInfo struct {
	// Checked 已 ENABLE 且挂了 tenant_isolation 策略的表数（策略面 ∩ 通电面）。
	Checked int
	// DormantLegMissing 其中 USING 表达式缺休眠腿（IS NULL 或空串两条腿任一不齐）的表数。
	DormantLegMissing int
	// MissingTables 缺腿表名（供日志/接口直出，便于逐表定位；已按名字排序）。
	MissingTables []string
	// QueryFailed 真查失败——**必须与"checked=0 且缺腿=0"区分**：前者是"没看成"，后者是"看成了、没事"。
	QueryFailed bool
}

// rlsPolicyHasDormantLegs 判一条策略的 USING 表达式是否两条休眠腿齐全。
// 入参是 pg_get_expr / pg_policies.qual 的 PG 规整文本（不是 Go 常量原文）：
// PG 会把 `current_setting('app.current_tenant', true)` 渲染成
// `current_setting('app.current_tenant'::text, true)`、把 `”` 渲染成 `”::text`，
// 所以判据按规整文本写，比较前折叠空白并转小写，避免"文本形态不同判成缺腿"。
func rlsPolicyHasDormantLegs(qual string) bool {
	norm := strings.ToLower(strings.Join(strings.Fields(qual), " "))
	const cs = "current_setting('app.current_tenant'::text, true)"
	return strings.Contains(norm, cs+" is null") && strings.Contains(norm, cs+" = ''")
}

// RLSPolicyFace 实查 pg_policy + pg_class，返回策略面快照（不受 RLS_ENABLED 控制：
// 策略是库内持久状态，002 基线与 030/031 迁移都能留下它，与本次进程是否启用无关）。
func RLSPolicyFace() RLSPolicyFaceInfo {
	out := RLSPolicyFaceInfo{}
	if DB == nil {
		out.QueryFailed = true
		return out
	}
	var rows []struct {
		Table string
		Qual  string
	}
	// 只看"通电且挂了策略"的表：未 ENABLE 的表策略永不生效，缺腿只是休眠式设计里的一行文本，
	// 把它报成故障会与本缺陷的真实形态（策略在场且生效但休眠腿不全）混淆。
	err := DB.Raw(`SELECT c.relname::text AS table, pg_get_expr(p.polqual, p.polrelid) AS qual
		FROM pg_policy p
		JOIN pg_class c ON c.oid = p.polrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = current_schema() AND p.polname = 'tenant_isolation' AND c.relrowsecurity
		ORDER BY c.relname`).Scan(&rows).Error
	if err != nil {
		out.QueryFailed = true
		return out
	}
	out.Checked = len(rows)
	for _, r := range rows {
		if !rlsPolicyHasDormantLegs(r.Qual) {
			out.DormantLegMissing++
			out.MissingTables = append(out.MissingTables, r.Table)
		}
	}
	return out
}

// rlsExemptTables 设计内**不**进 RLS 清单的含 tenant_id 表，key=表名，value=不入库的理由。
//
// 这张表是"豁免的白名单"，因此它本身必须是**封闭集**：新增一条即等于给一张租户表关掉
// DB 级收敛，必须在此写明理由并由 rls_coverage_test.go 的封闭性断言放行（测试里同时
// 钉死"豁免集必须恰为这些键"，避免有人图省事往里塞一张真表蒙过门禁）。
//
// system_configs 为什么必须豁免：它是"系统层默认值(tenant_id=0) + 租户覆盖行(tenant_id=N)"
// 双层同表设计，读路径靠 `tenant_id IN (0, N)` 取并集回落。RLS 策略是单值等值
// （tenant_id = current_setting(...)），一旦激活，租户运行时读系统层默认值这条腿会被 DB
// 直接掐断——表现为配置读不回默认、走零值，比不加 RLS 更危险。
var rlsExemptTables = map[string]string{
	"system_configs": "平台双层配置表：系统层 tenant_id=0 与租户覆盖同表，RLS 等值策略会切断回落默认值那条读腿",
}

// rlsCoverageCheck 对账"库内全部含 tenant_id 的基表"与"RLS 清单 + 豁免集"，是清单完整性的
// 唯一判据源（启动 WARN 与 CI 断言共用，两侧口径不可能分叉）。
//
// 返回三个互斥问题集：
//   - unlisted：库里有、清单没登记、也不在豁免集 → 该表激活 RLS 后不受 DB 收敛（真漏保）
//   - phantom：清单登记了但库里没有（且不含 tenant_id）→ 表名拼错/改名未同步，启用时 Fatal
//   - unusedExempt：豁免集里的键在库中并非含 tenant_id 的基表 → 死豁免，通常是表已删而豁免留档，
//     留着它等于给未来同名的"新表"预先开了后门
//
// 参数为切片而非直接读包变量，是为了让反证用例能在**清单副本**上跑（不碰真数据）。
func rlsCoverageCheck(allTables, listed []string, exempt map[string]string) (unlisted, phantom, unusedExempt []string) {
	listedSet := make(map[string]bool, len(listed))
	for _, t := range listed {
		listedSet[t] = true
	}
	dbSet := make(map[string]bool, len(allTables))
	for _, t := range allTables {
		dbSet[t] = true
	}
	for _, t := range allTables {
		if listedSet[t] {
			continue
		}
		// 豁免必须带**非空理由**才算豁免：只认键存在的话，`"deals": ""` 这种空壳条目
		// 就成了绕过对账的后门——加豁免的正当动机都能写成一句话，写不出理由即不该豁免。
		if reason, exempted := exempt[t]; exempted && strings.TrimSpace(reason) != "" {
			continue
		}
		unlisted = append(unlisted, t)
	}
	for _, t := range listed {
		if !dbSet[t] {
			phantom = append(phantom, t)
		}
	}
	for t := range exempt {
		if !dbSet[t] {
			unusedExempt = append(unusedExempt, t)
		}
	}
	return
}

// rlsTenantTablesDuplicate 返回清单内重复登记的表名（空=无重复）。
// 集合式对账看不出重复，但重复项会让"清单条数"这个观测值虚高，也会让启用循环白跑一遍，
// 属清单卫生问题，交给测试钉住。
func rlsTenantTablesDuplicate(listed []string) []string {
	seen := make(map[string]int, len(listed))
	for _, t := range listed {
		seen[t]++
	}
	var dups []string
	for t, n := range seen {
		if n > 1 {
			dups = append(dups, t)
		}
	}
	return dups
}

// rlsTenantIDTables 查出库内所有"含 tenant_id 列的基表"，是清单对账的库侧数据源。
// 独立成函数是为了让启动 WARN 与 CI 断言读**同一条 SQL**（SQL 分叉会让两侧口径不一致，
// 那比没有断言更糟）。排除视图（table_type='BASE TABLE'）：视图上的 tenant_id 不承载写入。
func rlsTenantIDTables(gdb *gorm.DB) ([]string, error) {
	var tables []string
	err := gdb.Raw(`SELECT DISTINCT c.table_name FROM information_schema.columns c
		JOIN information_schema.tables t ON t.table_name = c.table_name AND t.table_schema = c.table_schema
		WHERE c.column_name = 'tenant_id' AND c.table_schema = current_schema()
		  AND t.table_type = 'BASE TABLE'
		ORDER BY c.table_name`).Scan(&tables).Error
	return tables, err
}

// reconcileRLSChecklist 把 RLS 清单与库内真实含 tenant_id 的基表对账，返回三个问题集
// （语义见 rlsCoverageCheck）。**不受 RLS_ENABLED 控制**：清单漂移是代码与库结构的事实问题，
// 与本次是否真的建策略无关——旧实现只在 RLS_ENABLED=true 的分支里跑反向 diff，
// 默认关的部署连一行 WARN 都不出，等于把护栏挂在了一个常关的开关后面。
// fatalOnPhantom 只在真要启用策略时传 true：清单里多一张不存在的表，在无 RLS 形态下
// 只是卫生问题，不足以让服务起不来；启用形态下策略会建不出来（或建在错表上），必须拒启。
func reconcileRLSChecklist(fatalOnPhantom bool) (unlisted, phantom, deadExempt []string) {
	if DB == nil {
		return nil, nil, nil
	}
	dbTables, err := rlsTenantIDTables(DB)
	if err != nil {
		if fatalOnPhantom {
			log.Fatalf("[RLS] 清单对账查询失败，禁止带病启用 RLS: %v", err)
		}
		log.Printf("[RLS][WARN] 清单对账查询失败（DB 不可用？本次跳过对账）: %v", err)
		return nil, nil, nil
	}
	unlisted, phantom, deadExempt = rlsCoverageCheck(dbTables, rlsTenantTables, rlsExemptTables)
	if len(phantom) > 0 {
		if fatalOnPhantom {
			log.Fatalf("[RLS] 清单含不存在的表（%v），请修正 rlsTenantTables 与模型迁移保持一致；禁止带病启用 RLS", phantom)
		}
		log.Printf("[RLS][WARN] 清单含不存在的表（%v）：与模型迁移不一致，启用 RLS 时将拒绝启动", phantom)
	}
	if len(deadExempt) > 0 {
		log.Printf("[RLS][WARN] 豁免集里的这些键在库中不是含 tenant_id 的基表（%v）：属死豁免，表已删就该同步删掉——留着等于给未来同名新表预先开了后门", deadExempt)
	}
	if len(unlisted) > 0 {
		log.Printf("[RLS][WARN] 下列含 tenant_id 的表未在 rlsTenantTables 清单（RLS 激活后不受 DB 收敛，需确认属\"平台表/设计内豁免\"还是漏网）：%v", unlisted)
	}
	return
}

// rlsPolicyUsing 租户隔离策略的 USING 表达式——**休眠式设计的核心，也是本文件唯一的行为契约**。
// 三条腿的分工：第一条是激活态（SET 过 GUC 就只放行本租户）；后两条是休眠态（未激活即全表放行），
// 后台任务、迁移、超管跨租户视图全走休眠腿。删掉休眠腿 RLS 看着"更严"，实际会让所有未显式设 GUC
// 的查询静默返回空集、写入报 42501（全站级故障）。因此它不只写在注释里——rls_coverage_test.go 把这段
// 表达式当 SQL 直接求值，并配"抽掉休眠腿必须翻红"的反证。
//
// 为什么光有 IS NULL 一条休眠腿不够（FIX-A 落地当天实跑量出来的，2026-09-28）：
//
//	①新会话        current_setting('app.current_tenant',true) → NULL，IS NULL = true（放行）
//	②跑过一次 SET LOCAL 的那条连接、事务提交之后 → **''（空串）**，IS NULL = **false**
//
// 即 PG 的"复位"对自定义 GUC 是把值置成空串、不是把它变回未定义。连接池不会关掉这些连接，
// 于是**任何曾经激活过 RLS 的连接，此后的 current_setting 恒为 ”**——只看 IS NULL 的休眠腿在这条
// 连接上永久失效：普通查询被筛成空集、写入撞 42501（本轮补 ENABLE 之后 internal/billing 的九条
// 用例正是这样红的：策略从没被咨询过，所以这个形态在 002 之后一直存在、只是没人看得见）。
// 所以休眠态判据必须写成「NULL 或 空串」两者。空串不可能是合法租户号，把它当"未激活"没有歧义。
const rlsPolicyUsing = `tenant_id::text = current_setting('app.current_tenant', true)
				OR current_setting('app.current_tenant', true) IS NULL
				OR current_setting('app.current_tenant', true) = ''`

// rlsPolicySQL 按表名生成 CREATE POLICY 语句。
// 生产路径与测试断言共用这一模板，避免"测的是表达式 A、线上建的是表达式 B"。
func rlsPolicySQL(table string) string {
	return fmt.Sprintf(`CREATE POLICY tenant_isolation ON %s FOR ALL USING (
				%s
			)`, table, rlsPolicyUsing)
}

// EnableRLS 幂等启用租户隔离策略（受 RLS_ENABLED 开关控制）
// 关闭（默认）：不打任何策略，租户隔离完全由应用层 db.T/c.PQ 保证（零行为变更）。
// 开启：对租户业务表逐表 ENABLE + FORCE ROW LEVEL SECURITY 并重建 tenant_isolation 策略
// （FIX-A 2026-09-28 补 ENABLE：只有 FORCE 没有 ENABLE 时 PG 根本不会咨询策略）；业务事务内经
// db.WithTenantRLS(tid, fn) 或 SET LOCAL app.current_tenant 激活后即被 DB 强制收敛。
// 采用休眠式设计：默认 app.current_tenant 未设置时策略恒真，零行为变更。
// 结尾日志报的是 pg_class.relrowsecurity 的**实查数**，不是清单条数（见 RLSStatusInfo.EnableVerified）。
func EnableRLS() {
	if DB == nil {
		return
	}
	// P1-5 修复(2026-09-09) / G-14 收口(2026-09-24)：清单与 information_schema 双向对账，
	// 判据源收进 rlsCoverageCheck（启动 WARN 与 CI 断言共用同一谓词与同一条 SQL）。
	// 未启用 RLS 时也照常刷 WARN——CI 侧的硬断言在 rls_coverage_test.go，不依赖本函数被调用。
	if !config.GlobalConfig.RLS.Enabled {
		reconcileRLSChecklist(false)
		log.Printf("[RLS] 未启用（RLS_ENABLED=false），跳过策略创建；租户隔离由应用层 db.T 保证")
		return
	}
	reconcileRLSChecklist(true)

	// P1-8 修复(2026-09-15)①：SUPERUSER/BYPASSRLS 检测——PG 对超级用户与 BYPASSRLS 角色
	// **无条件旁路 RLS**，FORCE ROW LEVEL SECURITY 也管不住。默认 docker-compose 的
	// POSTGRES_USER 即 SUPERUSER——旧实现在这种部署形态下 RLS_ENABLED=true 给出的
	// "DB 级第二道闸"是虚设的（GAP/AGENTS 把它当已成立兜底，需纠偏）。
	var isSuper, bypass bool
	if err := DB.Raw("SELECT COALESCE(rolsuper,false) FROM pg_roles WHERE rolname = current_user").Scan(&isSuper).Error; err == nil {
		DB.Raw("SELECT COALESCE(rolbypassrls,false) FROM pg_roles WHERE rolname = current_user").Scan(&bypass)
	}
	rlsStatus.Bypass = isSuper || bypass // P2-2 批三：观测位数据源（/status rls_effective）
	if isSuper || bypass {
		log.Printf("[RLS][WARN] 当前连接角色是 SUPERUSER/BYPASSRLS——PostgreSQL 无条件旁路行级安全，FORCE ROW LEVEL SECURITY 不生效！")
		log.Printf("[RLS][WARN] RLS_ENABLED=true 的\"DB 级兜底\"在本部署形态下形同虚设。生产须为应用建 NOSUPERUSER NOBYPASSRLS 角色（见 DEPLOY_CHECKLIST）")
	}

	// P1-8 修复(2026-09-15)②的反向点名已并入上面的 reconcileRLSChecklist。

	failed := 0
	for _, t := range rlsTenantTables {
		// FIX-A(2026-09-28) 根因补位：PG 语义下 relrowsecurity=false（未 ENABLE）时，
		// 表上挂的策略**永不被咨询**——FORCE ROW LEVEL SECURITY 只关"表 owner 旁路"这一格，
		// 不含 ENABLE 语义，救不了这件事。旧循环只有 FORCE + DROP/CREATE POLICY，于是启动日志
		// 宣称"已对 N 张租户表启用"而 DB 级隔离从未真生效（2026-09-28 本机实测：14 张表挂了
		// tenant_isolation 策略、pg_class.relrowsecurity 全为 false）。ENABLE 必须排在 FORCE 前：
		// 顺序反了也不报错，但"先 FORCE 后 ENABLE"在中间窗口里 owner 仍可旁路，口径按依赖序写。
		// 失败处理与 FORCE 同口径：计数 + WARN，让末尾的实查数字与 failed 计数互相印证。
		if err := DB.Exec(fmt.Sprintf("ALTER TABLE %s ENABLE ROW LEVEL SECURITY", t)).Error; err != nil {
			log.Printf("[RLS] 表 %s ENABLE 失败: %v", t, err)
			failed++
		}
		// FORCE：即便表 owner 也受策略约束；因 NULL 旁路，当前不生效，仅作激活准备
		if err := DB.Exec(fmt.Sprintf("ALTER TABLE %s FORCE ROW LEVEL SECURITY", t)).Error; err != nil {
			log.Printf("[RLS] 表 %s FORCE 失败: %v", t, err)
			failed++
		}
		DB.Exec(fmt.Sprintf("DROP POLICY IF EXISTS tenant_isolation ON %s", t))
		if err := DB.Exec(rlsPolicySQL(t)).Error; err != nil {
			log.Printf("[RLS] 表 %s 策略创建失败: %v", t, err)
			failed++
		}
	}
	if failed > 0 {
		log.Printf("[RLS] 警告：%d 张表策略创建失败，租户隔离保护不完整，请检查上述日志", failed)
	}
	rlsStatus.Enabled = true
	rlsStatus.Tables = len(rlsTenantTables)
	// FIX-A(2026-09-28)："启用"的宣告从"循环没报错"改为真查 pg_class——这两件事在旧实现里
	// 被混为一谈，而正是这种混淆让缺陷静默存活了三年（002 起只写 FORCE）。查询失败时如实打
	// "未能验证"，绝不再凭循环无报错宣称启用（一个只会打日志的守卫等于没有守卫）。
	var verified int
	if err := DB.Raw(`SELECT count(*) FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND c.relrowsecurity AND c.relname::text = ANY($1)`,
		rlsTenantTables).Scan(&verified).Error; err != nil {
		rlsStatus.EnableVerified = -1 // -1＝未能验证，与"真查为 0"严格区分
		log.Printf("[RLS][WARN] relrowsecurity 实查失败，未能验证启用结果（下面的循环计数不是验证值）: %v", err)
		log.Printf("[RLS] 休眠式行级隔离已对 %d 张租户表执行 ENABLE/FORCE/建策略（**未能验证**，请检查 pg_class 查询权限）", len(rlsTenantTables))
		return
	}
	rlsStatus.EnableVerified = verified
	if verified < len(rlsTenantTables) {
		log.Printf("[RLS][WARN] 实查 relrowsecurity=true 的表只有 %d/%d：差额表要么建表失败要么 ALTER 被拒，逐表检查上方日志", verified, len(rlsTenantTables))
	}
	log.Printf("[RLS] 已对 %d/%d 张租户表启用休眠式行级隔离（实查 pg_class.relrowsecurity=true 数/清单数；SET app.current_tenant 激活；未设置 GUC 恒真放行属设计内，非全时强隔离）", verified, len(rlsTenantTables))
}

// SetTenantRLS 在事务内激活租户隔离
// 返回已 SET LOCAL 的事务 *gorm.DB，调用方须在该事务内完成查询
// 提交/回滚后设置自动失效（SET LOCAL 作用域为当前事务）
//
// fail-closed（FIX-A 2026-09-28）：传进来的必须是**事务句柄**。非事务句柄上跑 `SET LOCAL`
// 时 PG 只给一句 "SET LOCAL can only be used in transaction blocks" 的 WARNING 就照常返回
// 命令成功——调用方以为自己在这一句之后就被 DB 强制按租户收敛了，实际一条策略都没生效；
// 更糟的是它还把那台连接上的自定义 GUC 置成了空串（见 rlsPolicyUsing 的实跑记录），
// 于是"没激活"被伪装成"激活过了"。补 ENABLE 之前策略永不被咨询，这个误用毫无后果，
// 所以它一直活着；现在必须当场报错，让调用方的 `if r.Error != nil { return r.Error }` 接得住。
func SetTenantRLS(d *gorm.DB, tenantID uint) *gorm.DB {
	if d == nil || d.Statement == nil {
		return d
	}
	// GORM 的事务句柄其底层连接池就是 *sql.Tx；根句柄是 *sql.DB（或注入的自定义池）。
	// 只认 *sql.Tx，是为了让"看起来传了 tx、其实传了 db.DB"这种写法直接红。
	if _, isTx := d.Statement.ConnPool.(*sql.Tx); !isTx {
		// 错误只挂在返回的会话副本上：gorm 的 AddError 会写进句柄自己的 Error 字段，
		// 直接对根句柄 AddError 会让它之后的每一次查询都带着这个错（误伤面比原缺陷更大）。
		// AddError 返回的是聚合后的 error，不是 *gorm.DB，所以这里分两步写。
		rejected := d.Session(&gorm.Session{})
		rejected.AddError(fmt.Errorf("SetTenantRLS 必须在事务句柄上调用（SET LOCAL 在事务块外是 no-op，RLS 不会激活）：收到的是非事务连接"))
		return rejected
	}
	return d.Exec(fmt.Sprintf("SET LOCAL app.current_tenant = '%d'", tenantID))
}

// WithTenantRLS 在事务内激活租户隔离并执行回调（安全激活范式）
// 连接池下无法在普通查询稳定设置会话变量（会跨请求泄漏），故仅支持事务内激活
// 关键跨表读（如超管跨租户审计、计费对账）可改用此 API 获得 DB 级强制隔离兜底
// 示例：
//
//	var rows []model.Customer
//	err := db.WithTenantRLS(tid, func(tx *gorm.DB) error {
//	    return tx.Where("phone = ?", phone).Find(&rows).Error
//	})
//
// WithTenantRLS 在事务内激活租户行级安全并执行回调。
func WithTenantRLS(tenantID uint, fn func(tx *gorm.DB) error) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(fmt.Sprintf("SET LOCAL app.current_tenant = '%d'", tenantID)).Error; err != nil {
			return err
		}
		return fn(tx)
	})
}
