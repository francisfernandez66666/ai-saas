// 权益发放与对账扫描（D2a 文件拆分 2026-09-12）：续费、对账、order_entitlement 台账、超时关闭、支付事件。
package billing

import "ai-scrm/internal/metrics"

import "ai-scrm/internal/notify"

import (
	"ai-scrm/internal/runtimecfg"
	"context"
	"fmt"
	"log"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/mq"
	"ai-scrm/internal/redisclient"
	"ai-scrm/internal/webhook"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SweepSubscriptionRenewals 订阅生命周期（P2）：为即将到期的付费订阅生成续费订单。
// 不自动扣款——复用既有下单/支付流程，由租户完成支付后续费。幂等：近 30 天已有同包订单则跳过。
// 返回本次生成的续费订单数。由 main 定时 ticker 调用（如每 6 小时）。
// P2-5 竞态修复：与 ExpireCheck 共用 expireRenewLockKey 串行化，避免「刚过期又生成续费单」。
func SweepSubscriptionRenewals() int {
	// P2-5：与过期摘除串行化（多实例 TryLock 选主；未启用 Redis 各实例直跑但同进程内仍互斥）
	if redisclient.IsEnabled() {
		if h := redisclient.TryLock(expireRenewLockKey, 55*time.Minute); h == nil {
			return 0 // 过期摘除正在跑，本论跳过，下轮再扫
		} else {
			defer h.Unlock()
		}
	}
	days := 7
	if runtimecfg.DefaultSystemConfigService != nil {
		if v := runtimecfg.DefaultSystemConfigService.GetInt("renewal_window_days", 7); v > 0 {
			days = v
		}
	}
	cutoff := time.Now().AddDate(0, 0, days)
	var tenants []model.Tenant
	if err := db.DB.Where("status = 'active' AND expired_at IS NOT NULL AND expired_at > NOW() AND expired_at <= ?", cutoff).
		Find(&tenants).Error; err != nil {
		log.Printf("[Billing] 续费扫描列举租户失败: %v", err)
		return 0
	}
	n := 0
	for _, t := range tenants {
		// 找该租户最近一笔已付付费包订单，作为续费模板
		var lastOrder model.BillingOrder
		if err := db.DB.Where("tenant_id = ? AND status = 'paid' AND package_id > 0", t.ID).
			Order("paid_at DESC").First(&lastOrder).Error; err != nil {
			continue
		}
		var pkg model.Package
		if err := db.DB.Where("id = ? AND enabled = ? AND ptype = ?", lastOrder.PackageID, true, model.PackageTypePaid).
			First(&pkg).Error; err != nil {
			continue
		}
		// 去重：近 30 天已有该包续费/购买订单则跳过
		var dup int64
		db.DB.Model(&model.BillingOrder{}).
			Where("tenant_id = ? AND package_id = ? AND created_at >= ?", t.ID, pkg.ID, time.Now().AddDate(0, 0, -30)).
			Count(&dup)
		if dup > 0 {
			continue
		}
		if _, err := CreateOrderForPackage(t.ID, &pkg); err != nil {
			log.Printf("[Billing] 续费订单生成失败 tenant=%d pkg=%s: %v", t.ID, pkg.Code, err)
			continue
		}
		notify.NotifyGroup(fmt.Sprintf("【续费提醒】租户「%s」付费订阅将于 %s 到期，已生成续费订单（%s）",
			t.Name, t.ExpiredAt.Format("2006-01-02"), pkg.Name))
		n++
	}
	if n > 0 {
		log.Printf("[Billing] 续费扫描生成 %d 笔续费订单", n)
	}
	return n
}

// ReconcileBilling 对账（P2）：发现「已支付但发放未落地」的异常单并按台账幂等补发。
// R2 修复(2026-09-11)：旧判定"paid 且租户 expired_at IS NULL"对 increment/free 订单
// 恒为假阳性（增量包根本不改 expired_at），每轮把已正常发放的增量单再发一遍——
// 每笔增量单被白送一份 300 万 token（审计单轮漏一次，guard 审计去重只是掩盖而非修复）。
// 新判定唯一锚：paid 且**缺 order_entitlement 发放台账行**（GrantOrderEntitlement 已改为
// 台账先行单事务，缺行=发放确实未落地）。created_at 留 10 分钟窗口避让在途发放。
// 返回本次补救发放数。
func ReconcileBilling() int {
	var orders []model.BillingOrder
	if err := db.DB.Where("status = 'paid' AND package_id > 0 AND tenant_id IS NOT NULL").
		Where("created_at < NOW() - INTERVAL '10 minutes'").
		Where("id NOT IN (SELECT ref_id FROM reward_claims WHERE grant_type = ? AND ref_id IS NOT NULL)",
			model.RewardOrderEntitlement).
		Limit(200).Find(&orders).Error; err != nil {
		log.Printf("[Billing] 对账列举订单失败: %v", err)
		return 0
	}
	n := 0
	for _, o := range orders {
		order := o
		if err := GrantOrderEntitlement(nil, &order); err != nil {
			log.Printf("[Billing] 对账重发失败 tenant=%d order=%d: %v", *order.TenantID, order.ID, err)
			continue
		}
		db.DB.Create(&model.TenantAuditLog{
			TenantID: *order.TenantID, Action: fmt.Sprintf("billing_reconcile_%d", order.ID),
			Resource: fmt.Sprintf("order:%d", order.ID),
			Detail:   `{"reconcile":"regrant_entitlement"}`,
		})
		log.Printf("[Billing] 对账补救发放 tenant=%d order=%d pkg=%d", *order.TenantID, order.ID, order.PackageID)
		n++
	}
	if n > 0 {
		log.Printf("[Billing] 对账完成，补救发放 %d 笔", n)
	}
	return n
}

// GrantOrderEntitlement 按订单关联的商业包发放权益（M2 发放落点）
// R10 修复(2026-09-11)：此前"改单(paid)"与"发放权益"跨事务、无发放锚点——
// 到账后进程崩溃 = 钱收了权益永久丢失（注释宣称的 order_entitlement 自愈锚从未有代码写入）。
// 现在：发放全程收进单事务，事务内先写 order_entitlement 台账（ref_id=order_id 唯一索引
// ux_reward_order_grant），撞库=已发过直接幂等短路；发放失败随事务整体回滚、可安全重试。
// 对账器（ReconcileBilling）以"paid 但缺台账行"为唯一补发信号。
func GrantOrderEntitlement(tx *gorm.DB, order *model.BillingOrder) error {
	if order.PackageID == 0 || order.TenantID == nil {
		return fmt.Errorf("订单%d 无商业包/租户归属，跳过发放", order.ID)
	}
	run := func(tx *gorm.DB) error {
		oid := order.ID
		res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.RewardClaim{
			GrantType: model.RewardOrderEntitlement, TenantID: *order.TenantID,
			RefID: &oid, Note: "order:" + order.OrderNo,
		})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			log.Printf("[Billing] 订单%s 发放台账已存在，幂等跳过（不二次发放）", order.OrderNo)
			return nil
		}
		var pkg model.Package
		if err := tx.First(&pkg, order.PackageID).Error; err != nil {
			return fmt.Errorf("订单%d 关联包不存在: %w", order.ID, err)
		}
		// 2026-09-09 换包升级：差额抵扣订单（ReplaceSub=true）走"从今天起算"的发放，
		// 替换旧订阅而非顺延；普通订单走默认顺延语义
		if order.ReplaceSub {
			if err := GrantPackageUpgrade(tx, *order.TenantID, &pkg); err != nil {
				return err
			}
		} else if err := GrantPackage(tx, *order.TenantID, &pkg); err != nil {
			return err
		}
		// M-R 邀请推广（2026-08-25）：受邀人首笔 paid 包月套餐到账 →
		// 邀请人获永久 token（幂等闸门 ReferralPaidRewarded，单受邀限一次；increment/free 不触发）
		if pkg.PType == model.PackageTypePaid {
			RewardPaidReferral(tx, *order.TenantID)
		}
		return nil
	}
	if tx != nil {
		return run(tx)
	}
	return db.DB.Transaction(run)
}

// PublishPaymentEvent 确认到账后发布 payment 子事件
// 对齐 SAAS_PLAN §2.5 预留：未来流程引擎可挂「付费成功→开通欢迎流程」；
// one_id 为空由 mq 兜底为 sys:t{tid}（租户级事件）
func PublishPaymentEvent(order *model.BillingOrder) {
	if order.TenantID == nil {
		return
	}
	err := mq.Publish(context.Background(), mq.TopicUserEvent, *order.TenantID, "", "payment",
		mq.PaymentStatusEvent{
			OrderNo:     order.OrderNo,
			Status:      "paid",
			AmountCents: order.AmountCents,
			Channel:     order.Channel,
			PaidAt:      time.Now(),
		})
	if err != nil {
		log.Printf("[MQ] payment 事件发布失败 order=%s: %v", order.OrderNo, err)
	}
	// D6：出站事件 webhook 扇出（payment.paid），旁路不阻塞主流程
	webhook.Emit(*order.TenantID, model.WebhookEventPaymentPaid, map[string]interface{}{
		"order_no":     order.OrderNo,
		"amount_cents": order.AmountCents,
		"channel":      order.Channel,
		"status":       "paid",
	})
}

// SweepExpiredOrders 订单超时自动关闭（M4，2026-08-25）
// 背景：SAAS_PLAN §6.3 规划"订单超时关闭"一直未实现——pending 僵尸单无限堆积，
// static_qr 弃付单永远混在待办视野外。阈值 order_timeout_minutes（平台级键，默认15分钟）。
// 幂等：条件 UPDATE pending→closed，重复扫描零副作用；不发权益不发事件（closed≠paid）。
// 返回本次关闭的订单数。
func SweepExpiredOrders() int64 {
	minutes := 15
	if runtimecfg.DefaultSystemConfigService != nil {
		if v := runtimecfg.DefaultSystemConfigService.GetInt("order_timeout_minutes", 15); v > 0 {
			minutes = v
		}
	}
	cutoff := time.Now().Add(-time.Duration(minutes) * time.Minute)
	res := db.DB.Model(&model.BillingOrder{}).
		Where("status = ? AND created_at < ?", "pending", cutoff).
		Update("status", "closed")
	if res.Error != nil {
		log.Printf("[Billing] 订单超时扫描失败: %v", res.Error)
		return 0
	}
	if res.RowsAffected > 0 {
		metrics.IncPaymentFailed() // P1-2：超时关闭计为支付失败（成功率分母）
		log.Printf("[Billing] 订单超时关闭 %d 笔(超过%d分钟未付)", res.RowsAffected, minutes)
	}
	return res.RowsAffected
}
