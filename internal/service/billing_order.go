// 订单生命周期（D2a 文件拆分 2026-09-12）：下单、到账幂等发放、迟到到账复活、渠道确认。
package service

import (
	"crypto/rand"
	"fmt"
	"log"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func GenerateOrderNo() string {
	b := make([]byte, 4)
	rand.Read(b)
	return fmt.Sprintf("BO%s%s", time.Now().Format("20060102150405"), fmt.Sprintf("%02x", b))
}

// CreateOrderForPackage 按商业包创建订单（M1 下单统一入口）
// 返回订单与应收凭证（static_qr→收款码内容快照；mock→模拟码；sdk→渠道下单）
func CreateOrderForPackage(tenantID uint, pkg *model.Package) (*model.BillingOrder, error) {
	order := &model.BillingOrder{
		OrderNo:             GenerateOrderNo(),
		TenantID:            &tenantID,
		PackageID:           pkg.ID,
		AmountCents:         pkg.PriceCents,
		OriginalAmountCents: pkg.PriceCents,
		Status:              "pending",
	}
	switch pkg.PType {
	case model.PackageTypePaid:
		order.Period = "monthly"
	case model.PackageTypeFree, model.PackageTypeIncrement:
		order.Period = "once" // 增量包买断制
	}

	// 换包升级差额抵扣（2026-09-09）：仅 paid 包参与。
	// 语义（产品决策）：已有生效付费订阅且换订【不同】付费包 → 旧包剩余价值按比例
	// 折算抵扣新包金额（可为0），新包从今天起算生效（ReplaceSub=true → GrantPackageUpgrade）。
	// 同包续订不抵扣（走原顺延语义）；无生效订阅为全新购买。
	if pkg.PType == model.PackageTypePaid {
		if oldOrder, oldPkg, left, ok := ActivePaidSubscription(tenantID); ok && oldOrder.PackageID != pkg.ID {
			// 旧包剩余价值 = 旧实付 × 剩余天数 / 总天数（与 computeRefundForOrder paid 口径一致）
			oldValue := int64(oldOrder.AmountCents) * int64(left) / int64(oldPkg.DurationDays)
			net := int64(pkg.PriceCents) - oldValue
			if net < 0 {
				net = 0
			}
			order.AmountCents = int(net)
			order.OriginalAmountCents = pkg.PriceCents // 原价保留用于展示优惠
			order.ReplaceSub = true
			order.UpgradeOffsetCents = int(oldValue)
			order.UpgradeBaseOrderID = oldOrder.ID
			order.Remark = fmt.Sprintf("换包升级抵扣：旧订单#%d 剩余%d天 抵扣%d分", oldOrder.ID, left, oldValue)
		}
	}

	// pay_mode 三态分发：决定 channel 与支付凭证
	payMode := GetPayMode()
	var qrContent string
	switch payMode {
	case "static_qr":
		order.Channel = "manual"
		qrContent = DefaultSystemConfigService.GetString("static_qr_image", "")
	case "sdk":
		// 真实支付网关：向 PSP 下单，把返回的支付 URL 作为收银台凭证
		prov, err := selectProvider()
		if err != nil {
			return nil, err
		}
		order.Channel = prov.Name()
		qr, err := prov.CreatePayment(order)
		if err != nil {
			return nil, fmt.Errorf("支付网关下单失败: %w", err)
		}
		qrContent = qr
	default: // mock
		order.Channel = "mock"
		qr, _ := (MockProvider{}).CreatePayment(order)
		qrContent = qr
	}
	order.QRContent = qrContent

	// P2-2 RLS热路径接入：订单创建放事务内激活租户隔离（billing_orders 属租户表）
	err := db.DB.Transaction(func(tx *gorm.DB) error {
		if r := SetTenantRLS(tx, tenantID); r.Error != nil {
			return r.Error
		}
		// R11 修复(2026-09-11)：升级抵扣计算（ActivePaidSubscription）是无锁读——并发下两单
		// 都按同一基数算抵扣 = 同一份剩余价值抵扣两次。落库前锁基数订单行复核：
		// 状态必须仍为 paid，且不允许存在另一笔 pending 升级单引用同一基数。
		if order.ReplaceSub && order.UpgradeBaseOrderID > 0 {
			var base model.BillingOrder
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&base, order.UpgradeBaseOrderID).Error; err != nil {
				return fmt.Errorf("原订阅状态已变化，请刷新后重试")
			}
			if base.Status != "paid" {
				return fmt.Errorf("原订阅状态已变化，请刷新后重试")
			}
			var dup int64
			if err := tx.Model(&model.BillingOrder{}).
				Where("upgrade_base_order_id = ? AND status = 'pending'", base.ID).Count(&dup).Error; err != nil {
				return err
			}
			if dup > 0 {
				return fmt.Errorf("已有进行中的升级订单，请先完成支付或取消")
			}
		}
		return tx.Create(order).Error
	})
	if err != nil {
		return nil, err
	}
	log.Printf("[Billing] 订单已创建 order=%s tenant=%d pkg=%s amount=%d分 channel=%s 升级抵扣=%d分",
		order.OrderNo, tenantID, pkg.Code, order.AmountCents, order.Channel, order.UpgradeOffsetCents)
	return order, nil
}

// remainingPaidDays 计算付费订阅剩余生效天数（向上取整到天，当天即退1天；不超包总时长）
func remainingPaidDays(expiredAt *time.Time, durationDays int) int {
	if expiredAt == nil || durationDays <= 0 {
		return 0
	}
	if !expiredAt.After(time.Now()) {
		return 0 // 已过期：无剩余价值
	}
	left := int(time.Until(*expiredAt).Hours()/24) + 1 // 向上取整到天
	if left > durationDays {
		left = durationDays
	}
	return left
}

// ActivePaidSubscription 查询租户当前生效的付费订阅（最近一笔已付 paid 包订单 + 租户未过期）
// 返回 (旧订单, 旧包, 剩余天数, 是否存在生效订阅)；无生效订阅返回 ok=false。
// 剩余天数以租户真实到期日（tenants.expired_at）为准——叠加续订场景 PaidAt+duration 不可靠。
func ActivePaidSubscription(tenantID uint) (*model.BillingOrder, *model.Package, int, bool) {
	var oldOrder model.BillingOrder
	if err := db.DB.Where("tenant_id = ? AND status = 'paid' AND package_id > 0", tenantID).
		Order("paid_at DESC").First(&oldOrder).Error; err != nil {
		return nil, nil, 0, false
	}
	var oldPkg model.Package
	if err := db.DB.First(&oldPkg, oldOrder.PackageID).Error; err != nil {
		return nil, nil, 0, false
	}
	if oldPkg.PType != model.PackageTypePaid || oldPkg.DurationDays <= 0 {
		return nil, nil, 0, false
	}
	var t model.Tenant
	if err := db.DB.Select("expired_at").First(&t, tenantID).Error; err != nil {
		return nil, nil, 0, false
	}
	left := remainingPaidDays(t.ExpiredAt, oldPkg.DurationDays)
	if left <= 0 {
		return nil, nil, 0, false // 已过期，无剩余价值可抵扣
	}
	return &oldOrder, &oldPkg, left, true
}

// MarkOrderPaid 幂等标记订单到账：pending→paid 条件更新
// 返回 (订单, 是否本次实际流转)。false=已被处理过（重复 confirm/mock-pay），调用方不得二次发放
func MarkOrderPaid(orderID uint, channel string) (*model.BillingOrder, bool, error) {
	now := time.Now()
	res := db.DB.Model(&model.BillingOrder{}).
		Where("id = ? AND status = 'pending'", orderID).
		Updates(map[string]interface{}{
			"status":       "paid",
			"paid_at":      now,
			"payment_data": fmt.Sprintf(`{"channel":"%s","confirmed_at":"%s"}`, channel, now.Format(time.RFC3339)),
		})
	if res.Error != nil {
		return nil, false, res.Error
	}
	if res.RowsAffected == 0 {
		// 已是 paid/closed 等：查出返回供审计展示，但不视为本次流转
		var o model.BillingOrder
		if err := db.DB.First(&o, orderID).Error; err != nil {
			return nil, false, err
		}
		return &o, false, nil
	}
	IncPaymentPaid() // P1-2：支付成功计数（成功率分母）
	var o model.BillingOrder
	if err := db.DB.First(&o, orderID).Error; err != nil {
		return nil, false, err
	}
	return &o, true, nil
}

// ReopenClosedOrderPaid R5 修复(2026-09-11)：迟到到账保护。
// 订单被超时巡检 closed 后，真实资金才到达（PSP 回调延迟/银行转账慢）——此前该回调
// 静默丢弃（扣钱不发货、对账也不覆盖 closed 单）。现允许"真钱信号"通道
// （webhook 验签后 / 超管人工确认）把 closed 单重新拉回 paid 并补发权益；
// mock 渠道绝不允许（自助接口无真实资金，reopen 会成白嫖入口）。
// 条件 UPDATE 保证并发下只有一个调用方拿到流转权。
func ReopenClosedOrderPaid(orderID uint, channel string) (*model.BillingOrder, bool, error) {
	if channel == "" || channel == "mock" {
		return nil, false, nil
	}
	now := time.Now()
	res := db.DB.Model(&model.BillingOrder{}).
		Where("id = ? AND status = 'closed'", orderID).
		Updates(map[string]interface{}{
			"status":       "paid",
			"paid_at":      now,
			"payment_data": fmt.Sprintf(`{"channel":"%s","late_payment_reopen":true,"confirmed_at":"%s"}`, channel, now.Format(time.RFC3339)),
		})
	if res.Error != nil {
		return nil, false, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, false, nil
	}
	var o model.BillingOrder
	if err := db.DB.First(&o, orderID).Error; err != nil {
		return nil, false, err
	}
	IncPaymentPaid()
	log.Printf("[Billing][WARN] 订单%s 超时关闭后迟到到账，已自动恢复 paid 并补发权益 channel=%s", o.OrderNo, channel)
	NotifyGroup(fmt.Sprintf("【迟到到账】订单 %s（%d分）超时关闭后收到 %s 渠道到账，已自动恢复发放，请财务复核", o.OrderNo, o.AmountCents, channel))
	return &o, true, nil
}

// ConfirmOrderByChannel 支付网关异步回调统一落点（webhook 复用）：
// 按 order_no 定位 → 幂等到账 → 发放权益 → 发布 payment 事件。
// 返回 (订单, 是否本次实际流转)。flowed=false 视为重复回调/已处理。
func ConfirmOrderByChannel(orderNo, channel string) (*model.BillingOrder, bool, error) {
	var o model.BillingOrder
	if err := db.DB.Where("order_no = ?", orderNo).First(&o).Error; err != nil {
		return nil, false, fmt.Errorf("订单不存在: %s", orderNo)
	}
	// L3 修复(2026-09-11)：webhook 路径参数 :channel 与订单实际渠道互验——
	// 此前任何验签通过的回调打到 /webhook/任意渠道 都能给别渠道订单发货（渠道混淆）。
	// manual 作为人工确认别名放行。
	if channel != "" && channel != "manual" && o.Channel != "" && o.Channel != channel {
		return nil, false, fmt.Errorf("回调渠道(%s)与订单渠道(%s)不一致", channel, o.Channel)
	}
	order, flowed, err := MarkOrderPaid(o.ID, channel)
	if err != nil {
		return nil, false, err
	}
	if !flowed {
		// R5：真实到账回调遇到超时关单 → 复活补发（mock 渠道在 Reopen 内部已拒）
		reopened, ok, rerr := ReopenClosedOrderPaid(o.ID, channel)
		if rerr != nil {
			return nil, false, rerr
		}
		if ok {
			order, flowed = reopened, true
		}
	}
	if flowed && order.TenantID != nil {
		if err := GrantOrderEntitlement(nil, order); err != nil {
			// 钱已收权益未发：记录告警，交由对账器按台账补发（不回滚到账状态）
			log.Printf("[Billing][ERROR] 网关回调订单%s 到账但发放失败: %v", order.OrderNo, err)
		} else {
			PublishPaymentEvent(order)
		}
	}
	return order, flowed, nil
}

// refundClawback 退款需同步回收的权益（关闭「付费→退款→白嫖」口子）
