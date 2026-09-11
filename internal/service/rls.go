// Package service 提供 SCRM 业务服务层实现（计费/消息/配置/脱敏/监控/向量等）。
package service

// ============================================================
// 多租户行级隔离 RLS（P2 RLS，2026-08-29）
//
// 定位：DB 层租户隔离兜底（与业务层 db.RQ/c.PQ fail-closed 双重保险）。
// 策略（休眠式）：默认 app.current_tenant 未设置 → current_setting 返回 NULL →
//   USING 表达式 OR NULL IS NULL 恒真 → 全表可见，与现状完全等价（零行为变更）。
// 激活方式：在事务内 SET LOCAL app.current_tenant='<tid>' 后查询即被 DB 强制按租户收敛
//   （见 SetTenantRLS）。采用 FORCE ROW LEVEL SECURITY 确保即便表 owner 也受策略约束。
// 仅对确含 tenant_id 的业务表启用；平台级表（tenants/system_configs/…）不在列，避免误伤跨租户管理。
// ============================================================

import (
	"fmt"
	"log"

	"ai-scrm/config"
	"ai-scrm/internal/db"
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
	"event_logs",           // 事件日志表（业务事件审计）
	"id_mappings",          // ID 映射表（OneID 身份归一）
	"inbox_events",         // 收件箱事件表
	"flow_state_machines",  // 流程状态机表

	// ---- 模板/功能/行业包表 ----
	"templates",             // 模板表（话术/文案模板）
	"features",              // 功能开关表
	"tenant_pack_bindings",  // 租户行业包绑定表（G-24）
	"billing_orders",        // 计费订单表

	// ---- 用量/审计表 ----
	"usage_records",      // 用量记录表（旧版）
	"usage_ledger",       // 用量台账表（M3 计费底座）
	"tenant_audit_logs",  // 租户审计日志表

	// ---- G-14 补齐：组织/身份/API/流程/标签/行业包相关表 ----
	"departments",             // 部门表（组织架构）
	"customer_identities",     // 客户身份表（多身份归一）
	"api_keys",                // API 密钥表（OpenAPI 鉴权）
	"flow_instances",          // 流程实例表
	"message_event_records",   // 消息事件记录表（MQ 审计）
	"dept_pack_bindings",      // 部门行业包绑定表
	"tags",                    // 标签定义表
	"tag_rules",               // 打标规则表（关键词/抗性/意向匹配）
	"tag_weight_mappings",     // 标签权重映射表（标签→T向量驱动）
	"flow_definitions",        // 流程定义表
	"brands",                  // 品牌表（汽车行业）
	"car_models",              // 车型表（汽车行业）
	"model_specs",             // 车型配置表（汽车行业）
	"competitor_compares",     // 竞品对比表（汽车行业）
}

// EnableRLS 幂等启用租户隔离策略（受 RLS_ENABLED 开关控制）
// 关闭（默认）：不打任何策略，租户隔离完全由应用层 db.T/c.PQ 保证（零行为变更）。
// 开启：对租户业务表创建 FORCE ROW LEVEL SECURITY 策略；业务事务内经
// service.WithTenantRLS(tid, fn) 或 SET LOCAL app.current_tenant 激活后即被 DB 强制收敛。
// 采用休眠式设计：默认 app.current_tenant 未设置时策略恒真，零行为变更
func EnableRLS() {
	if db.DB == nil {
		return
	}
	if !config.GlobalConfig.RLS.Enabled {
		log.Printf("[RLS] 未启用（RLS_ENABLED=false），跳过策略创建；租户隔离由应用层 db.T 保证")
		return
	}
	// P1-5 修复(2026-09-09)：启动时对清单与 information_schema.tables 比对，
	// 表名不匹配即 Fatal（防漂移：新增/改名核心表后 RLS 静默漏保护）
	var exist []string
	db.DB.Raw("SELECT tablename FROM pg_tables WHERE schemaname = current_schema()").Scan(&exist)
	existSet := make(map[string]bool, len(exist))
	for _, t := range exist {
		existSet[t] = true
	}
	missing := make([]string, 0, 4)
	for _, t := range rlsTenantTables {
		if !existSet[t] {
			missing = append(missing, t)
		}
	}
	if len(missing) > 0 {
		log.Fatalf("[RLS] 清单含不存在的表（%v），请修正 rlsTenantTables 与模型迁移保持一致；禁止带病启用 RLS", missing)
	}

	failed := 0
	for _, t := range rlsTenantTables {
		// FORCE：即便表 owner 也受策略约束；因 NULL 旁路，当前不生效，仅作激活准备
		if err := db.DB.Exec(fmt.Sprintf("ALTER TABLE %s FORCE ROW LEVEL SECURITY", t)).Error; err != nil {
			log.Printf("[RLS] 表 %s FORCE 失败: %v", t, err)
			failed++
		}
		db.DB.Exec(fmt.Sprintf("DROP POLICY IF EXISTS tenant_isolation ON %s", t))
		policy := fmt.Sprintf(
			`CREATE POLICY tenant_isolation ON %s FOR ALL USING (
				tenant_id::text = current_setting('app.current_tenant', true)
				OR current_setting('app.current_tenant', true) IS NULL
			)`, t)
		if err := db.DB.Exec(policy).Error; err != nil {
			log.Printf("[RLS] 表 %s 策略创建失败: %v", t, err)
			failed++
		}
	}
	if failed > 0 {
		log.Printf("[RLS] 警告：%d 张表策略创建失败，租户隔离保护不完整，请检查上述日志", failed)
	}
	log.Printf("[RLS] 已对 %d 张租户表启用休眠式行级隔离（SET app.current_tenant 激活）", len(rlsTenantTables))
}

// SetTenantRLS 在事务内激活租户隔离
// 返回已 SET LOCAL 的事务 *gorm.DB，调用方须在该事务内完成查询
// 提交/回滚后设置自动失效（SET LOCAL 作用域为当前事务）
func SetTenantRLS(d *gorm.DB, tenantID uint) *gorm.DB {
	return d.Exec(fmt.Sprintf("SET LOCAL app.current_tenant = '%d'", tenantID))
}

// WithTenantRLS 在事务内激活租户隔离并执行回调（安全激活范式）
// 连接池下无法在普通查询稳定设置会话变量（会跨请求泄漏），故仅支持事务内激活
// 关键跨表读（如超管跨租户审计、计费对账）可改用此 API 获得 DB 级强制隔离兜底
// 示例：
//
//	var rows []model.Customer
//	err := service.WithTenantRLS(tid, func(tx *gorm.DB) error {
//	    return tx.Where("phone = ?", phone).Find(&rows).Error
//	})
func WithTenantRLS(tenantID uint, fn func(tx *gorm.DB) error) error {
	return db.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(fmt.Sprintf("SET LOCAL app.current_tenant = '%d'", tenantID)).Error; err != nil {
			return err
		}
		return fn(tx)
	})
}
