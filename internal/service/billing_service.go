package service

import (
	"context"
	"fmt"
	"log"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/mq"

	"gorm.io/gorm"
)

// ============================================================
// 收银台服务（商业化第一批 M1，2026-08-23）
//
// pay_mode 三态（源自翻译助手生产验证设计）：
//   mock        测试模拟到账（默认，跑通全链路；生产靠接口403双保险）
//   static_qr   静态收款码 + 租户「我已付费」→ critical 审计 + 催告超管 → 人工确认发放
//   sdk         商户号到位后切 wechat/alipay 适配器（本期只留适配器接口位）
//
// 幂等铁律：MarkOrderPaid 用条件 UPDATE（pending→paid），
// RowsAffected=0 视为已处理过，绝不二次发放权益
// ============================================================

// PaymentProvider 支付渠道适配器接口（M1 预留接口位）
// 商户号到位后实现 WechatProvider/AlipayProvider 即可切换，收银台代码零改动
type PaymentProvider interface {
	Name() string                                                      // wechat/alipay
	CreatePayment(order *model.BillingOrder) (qrURL string, err error) // 下单返回支付凭证
}

// MockProvider 模拟渠道：无真实收款，配合 mock-pay 接口跑通全链路
type MockProvider struct{}

// Name 渠道标识
func (MockProvider) Name() string { return "mock" }

// CreatePayment 生成模拟支付凭证（无真实收款，配合 mock-pay 跑通全链路）
func (MockProvider) CreatePayment(order *model.BillingOrder) (string, error) {
	return fmt.Sprintf("mock://pay/%s?amount=%d", order.OrderNo, order.AmountCents), nil
}

// GetPayMode 读当前收款模式（系统配置热加载，默认 mock）
func GetPayMode() string {
	if DefaultSystemConfigService == nil {
		return "mock"
	}
	mode := DefaultSystemConfigService.GetString("pay_mode", "mock")
	if mode != "mock" && mode != "static_qr" && mode != "sdk" {
		return "mock" // 脏配置兜底
	}
	return mode
}

// GenerateOrderNo 订单号：时间戳+随机段，32位内唯一索引兜底
func GenerateOrderNo() string {
	return fmt.Sprintf("BO%s%04d", time.Now().Format("20060102150405"), time.Now().UnixNano()%10000)
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

	// pay_mode 三态分发：决定 channel 与支付凭证
	payMode := GetPayMode()
	var qrContent string
	switch payMode {
	case "static_qr":
		order.Channel = "manual"
		qrContent = DefaultSystemConfigService.GetString("static_qr_image", "")
	case "sdk":
		// 本期未接商户号：明确报错而非静默走 mock（防止测试通道漏进生产）
		return nil, fmt.Errorf("sdk 支付渠道尚未开通，请使用静态码收款或联系平台")
	default: // mock
		order.Channel = "mock"
		qr, _ := (MockProvider{}).CreatePayment(order)
		qrContent = qr
	}
	order.QRContent = qrContent

	if err := db.DB.Create(order).Error; err != nil {
		return nil, err
	}
	log.Printf("[Billing] 订单已创建 order=%s tenant=%d pkg=%s amount=%d分 channel=%s",
		order.OrderNo, tenantID, pkg.Code, order.AmountCents, order.Channel)
	return order, nil
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
	var o model.BillingOrder
	if err := db.DB.First(&o, orderID).Error; err != nil {
		return nil, false, err
	}
	return &o, true, nil
}

// GrantOrderEntitlement 按订单关联的商业包发放权益（M2 发放落点）
func GrantOrderEntitlement(tx *gorm.DB, order *model.BillingOrder) error {
	if order.PackageID == 0 || order.TenantID == nil {
		return fmt.Errorf("订单%d 无商业包/租户归属，跳过发放", order.ID)
	}
	var pkg model.Package
	if err := db.DB.First(&pkg, order.PackageID).Error; err != nil {
		return fmt.Errorf("订单%d 关联包不存在: %w", order.ID, err)
	}
	return GrantPackage(tx, *order.TenantID, &pkg)
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
}
