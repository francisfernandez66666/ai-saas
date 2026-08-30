// 收银台商业化服务：pay_mode 三态分发、订单幂等发放/到账、超时扫描关闭。
package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/mq"
	"ai-scrm/internal/redisclient"

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

// GatewayProvider 真实支付网关适配器（pay_mode=sdk 落点，P2 商业化）
// 对接任意支持「下单接口 + 异步回调」的 PSP（微信/支付宝/聚合码台），
// 通过 HMAC-SHA256 对请求体签名鉴权。商户号/密钥经系统配置（或环境变量）注入，
// 未配置则明确报错，绝不静默走 mock（防测试通道漏进生产）。
//
// 约定（与 webhook 校验对称）：
//
//	下单签名 sign = HMAC_SHA256(key, canonical(params))
//	回调验签 sign = HMAC_SHA256(key, order_no + "|" + trade_status)
//
// 具体字段随 PSP 调整——此处给出可投产的通用骨架，接真实商户号仅需填配置。
type GatewayProvider struct {
	Endpoint  string // 下单接口基址，如 https://api.mch.example.com/unifiedorder
	AppID     string // 商户/应用 ID
	Key       string // 签名密钥
	NotifyURL string // 异步回调地址（PSP 到账后回调 /api/v1/billing/webhook/gateway）
}

// Name 渠道标识
func (g GatewayProvider) Name() string { return "gateway" }

// CreatePayment 向 PSP 下单并返回支付跳转/二维码 URL（真实收款）
func (g GatewayProvider) CreatePayment(order *model.BillingOrder) (string, error) {
	if g.Endpoint == "" || g.AppID == "" || g.Key == "" {
		return "", fmt.Errorf("支付网关未配置（pay_gateway_url/app_id/key 缺失）")
	}
	params := map[string]string{
		"out_trade_no": order.OrderNo,
		"total_fee":    strconv.Itoa(order.AmountCents),
		"app_id":       g.AppID,
		"timestamp":    strconv.FormatInt(time.Now().Unix(), 10),
		"notify_url":   g.NotifyURL,
		"subject":      fmt.Sprintf("AI-SCRM 订单 %s", order.OrderNo),
	}
	params["sign"] = signParams(params, g.Key)
	body, _ := json.Marshal(params)
	req, err := http.NewRequest(http.MethodPost, g.Endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("支付网关请求失败: %w", err)
	}
	defer resp.Body.Close()
	var out struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		PayURL  string `json:"pay_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("支付网关响应解析失败: %w", err)
	}
	if out.Code != 0 {
		return "", fmt.Errorf("支付网关下单失败: %s", out.Message)
	}
	return out.PayURL, nil
}

// signParams 按 key 字典序拼接 sign=HMAC_SHA256(key, k1=v1&k2=v2...)
func signParams(params map[string]string, key string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b bytes.Buffer
	for i, k := range keys {
		if k == "sign" {
			continue
		}
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(params[k])
	}
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write(b.Bytes())
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyGatewaySign 回调验签（与 CreatePayment 签名对称）：sign = HMAC_SHA256(key, order_no|status)
func VerifyGatewaySign(key, orderNo, status, sign string) bool {
	if key == "" || sign == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(orderNo + "|" + status))
	return hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(sign))
}

// loadGatewayProvider 从系统配置/环境变量装配网关（热加载，缺省环境变量兜底）
func loadGatewayProvider() GatewayProvider {
	get := func(sysKey, envKey string) string {
		if DefaultSystemConfigService != nil {
			if v := DefaultSystemConfigService.GetString(sysKey, ""); v != "" {
				return v
			}
		}
		return os.Getenv(envKey)
	}
	return GatewayProvider{
		Endpoint:  get("pay_gateway_url", "PAY_GATEWAY_URL"),
		AppID:     get("pay_gateway_app_id", "PAY_GATEWAY_APP_ID"),
		Key:       get("pay_gateway_key", "PAY_GATEWAY_KEY"),
		NotifyURL: get("pay_gateway_notify_url", "PAY_GATEWAY_NOTIFY_URL"),
	}
}

// selectProvider 按当前 pay_mode 选择渠道适配器
func selectProvider() (PaymentProvider, error) {
	switch GetPayMode() {
	case "sdk":
		g := loadGatewayProvider()
		if g.Endpoint == "" {
			return nil, fmt.Errorf("sdk 支付未配置网关（pay_gateway_url 缺失）")
		}
		return g, nil
	case "static_qr", "mock":
		return MockProvider{}, nil
	default:
		return MockProvider{}, nil
	}
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

// GenerateOrderNo 订单号：秒级时间戳+6字节随机hex（UAT修复 2026-08-26）
// 原"UnixNano%10000"四位尾数在批量下单场景高频碰撞(23505)——改为 crypto/rand
// 8位hex后缀，碰撞概率≈1/2^32且与秒级字段解耦；总长仍≤32满足唯一索引
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
		return tx.Create(order).Error
	})
	if err != nil {
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
	IncPaymentPaid() // P1-2：支付成功计数（成功率分母）
	var o model.BillingOrder
	if err := db.DB.First(&o, orderID).Error; err != nil {
		return nil, false, err
	}
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
	order, flowed, err := MarkOrderPaid(o.ID, channel)
	if err != nil {
		return nil, false, err
	}
	if flowed && order.TenantID != nil {
		if err := GrantOrderEntitlement(nil, order); err != nil {
			// 钱已收权益未发：记录告警，交由超管后台补救（不回滚到账状态）
			log.Printf("[Billing][ERROR] 网关回调订单%s 到账但发放失败: %v", order.OrderNo, err)
		} else {
			PublishPaymentEvent(order)
		}
	}
	return order, flowed, nil
}

// MarkOrderRefunded 幂等退款：paid→refunded 条件更新，置 RefundedAt。
// 返回 (订单, 是否本次流转)。注意：仅置状态，不自动撤销已发放权益——
// 撤销权益（配额回滚）需业务侧单独评估，避免误伤。
func MarkOrderRefunded(orderID uint) (*model.BillingOrder, bool, error) {
	now := time.Now()
	res := db.DB.Model(&model.BillingOrder{}).
		Where("id = ? AND status = 'paid'", orderID).
		Updates(map[string]interface{}{"status": "refunded", "refunded_at": now})
	if res.Error != nil {
		return nil, false, res.Error
	}
	if res.RowsAffected == 0 {
		var o model.BillingOrder
		if err := db.DB.First(&o, orderID).Error; err != nil {
			return nil, false, err
		}
		return &o, false, nil
	}
	IncPaymentFailed() // P1-2：退款计为支付失败（成功率分母）
	var o model.BillingOrder
	if err := db.DB.First(&o, orderID).Error; err != nil {
		return nil, false, err
	}
	return &o, true, nil
}

// RequestInvoice 申请发票：置 InvoiceRequested=true, InvoiceStatus=requested（幂等，仅 pending/paid 可申）
func RequestInvoice(orderID uint) (*model.BillingOrder, error) {
	var o model.BillingOrder
	if err := db.DB.First(&o, orderID).Error; err != nil {
		return nil, fmt.Errorf("订单不存在")
	}
	if o.Status != "paid" {
		return nil, fmt.Errorf("仅已支付订单可申请发票（当前 %s）", o.Status)
	}
	if err := db.DB.Model(&model.BillingOrder{}).Where("id = ?", orderID).
		Updates(map[string]interface{}{"invoice_requested": true, "invoice_status": "requested"}).Error; err != nil {
		return nil, err
	}
	db.DB.First(&o, orderID)
	return &o, nil
}

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
	if DefaultSystemConfigService != nil {
		if v := DefaultSystemConfigService.GetInt("renewal_window_days", 7); v > 0 {
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
		NotifyGroup(fmt.Sprintf("【续费提醒】租户「%s」付费订阅将于 %s 到期，已生成续费订单（%s）",
			t.Name, t.ExpiredAt.Format("2006-01-02"), pkg.Name))
		n++
	}
	if n > 0 {
		log.Printf("[Billing] 续费扫描生成 %d 笔续费订单", n)
	}
	return n
}

// ReconcileBilling 对账（P2）：发现「已支付但订阅权益未真正生效」的异常单并幂等重发一次。
// 判定：订单 paid 且租户仍为 active/trial，但 expired_at 为 NULL（说明 GrantPackage 未落地，属发放失败）。
// 仅此一种清晰信号才重发，避免对已正常到期的订阅误续费；重发以审计动作去重防循环。
// 返回本次补救发放数。
func ReconcileBilling() int {
	var orders []model.BillingOrder
	if err := db.DB.Where("status = 'paid' AND package_id > 0").Find(&orders).Error; err != nil {
		log.Printf("[Billing] 对账列举订单失败: %v", err)
		return 0
	}
	n := 0
	for _, o := range orders {
		if o.TenantID == nil {
			continue
		}
		var t model.Tenant
		if err := db.DB.Where("id = ?", *o.TenantID).First(&t).Error; err != nil {
			continue
		}
		// 仅 active/trial 但 expired_at 为空 → 发放遗漏
		if (t.Status == "active" || t.Status == "trial") && (t.ExpiredAt == nil) {
			guard := fmt.Sprintf("billing_reconcile_%d", o.ID)
			var cnt int64
			db.DB.Model(&model.TenantAuditLog{}).Where("tenant_id = ? AND action = ?", *o.TenantID, guard).Count(&cnt)
			if cnt > 0 {
				continue
			}
			var pkg model.Package
			if err := db.DB.Where("id = ?", o.PackageID).First(&pkg).Error; err != nil {
				continue
			}
			if err := GrantPackage(nil, *o.TenantID, &pkg); err != nil {
				log.Printf("[Billing] 对账重发失败 tenant=%d order=%d: %v", *o.TenantID, o.ID, err)
				continue
			}
			db.DB.Create(&model.TenantAuditLog{
				TenantID: *o.TenantID, Action: guard, Resource: fmt.Sprintf("order:%d", o.ID),
				Detail: `{"reconcile":"regrant_entitlement"}`,
			})
			log.Printf("[Billing] 对账补救发放 tenant=%d order=%d pkg=%s", *o.TenantID, o.ID, pkg.Code)
			n++
		}
	}
	if n > 0 {
		log.Printf("[Billing] 对账完成，补救发放 %d 笔", n)
	}
	return n
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
	if err := GrantPackage(tx, *order.TenantID, &pkg); err != nil {
		return err
	}
	// M-R 邀请推广（2026-08-25）：受邀人首笔 paid 包月套餐到账 →
	// 邀请人获永久 token（幂等闸门 ReferralPaidRewarded，单受邀限一次；increment/free 不触发）
	if pkg.PType == model.PackageTypePaid {
		RewardPaidReferral(tx, *order.TenantID)
	}
	return nil
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

// SweepExpiredOrders 订单超时自动关闭（M4，2026-08-25）
// 背景：SAAS_PLAN §6.3 规划"订单超时关闭"一直未实现——pending 僵尸单无限堆积，
// static_qr 弃付单永远混在待办视野外。阈值 order_timeout_minutes（平台级键，默认15分钟）。
// 幂等：条件 UPDATE pending→closed，重复扫描零副作用；不发权益不发事件（closed≠paid）。
// 返回本次关闭的订单数。
func SweepExpiredOrders() int64 {
	minutes := 15
	if DefaultSystemConfigService != nil {
		if v := DefaultSystemConfigService.GetInt("order_timeout_minutes", 15); v > 0 {
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
		IncPaymentFailed() // P1-2：超时关闭计为支付失败（成功率分母）
		log.Printf("[Billing] 订单超时关闭 %d 笔(超过%d分钟未付)", res.RowsAffected, minutes)
	}
	return res.RowsAffected
}
