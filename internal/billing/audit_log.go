// 审计留痕写面单点（FIX-16，2026-09-29 审计批）
package billing

import (
	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"log"
)

// ============================================================
// 为什么留痕写入要收成一个带检错的单点
//
// 改前形态：billing 里五处 `db.DB.Create(&model.TenantAuditLog{...})` 全是裸语句——
// GORM 返回的 *gorm.DB 被原地丢弃，写失败零痕迹。审计行不是日志装饰：
// 催缴封禁归因靠 `dunning_suspend` 动作码 + `billing_dunning.suspended_at` 双证
// 才敢在续费时自动解封（封错人=把欠费户放回货架），超管复位/补发靠留痕自证
// "谁在什么时候决定的"。留痕静默丢 = 系统里发生了一堆**证明不了发生过**的动作，
// 事后对账只能凭时间猜。
//
// 口径与资金路径不同处：审计是旁路，写失败**不反转主流程**（动作已经真实发生，
// 回滚反而把"发生了但没留痕"变成"没发生"这种更错的账面）。所以这里只点名不抛出——
// 但必须点名：ERROR 日志把 action/resource/tenant 三要素说全，人工可补录。
// ============================================================

// createAuditLog 写一条租户级审计留痕（带检错；失败只点名不阻断旁路主流程）。
func createAuditLog(entry model.TenantAuditLog) {
	if err := db.DB.Create(&entry).Error; err != nil {
		log.Printf("[审计][ERROR] 留痕落库失败 action=%s resource=%s tenant=%d: %v（主动作已生效，需人工补录）",
			entry.Action, entry.Resource, entry.TenantID, err)
	}
}
