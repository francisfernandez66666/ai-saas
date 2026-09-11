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
	"errors"
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
	"gorm.io/gorm/clause"
)

// ErrRefundNoRemaining 退款拒绝哨兵：订单权益已全部消耗/过期，无剩余可退
var ErrRefundNoRemaining = errors.New("该订单权益已全部消耗，无剩余可退")

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

// refundClawback 退款需同步回收的权益（关闭「付费→退款→白嫖」口子）
type refundClawback struct {
	tokens int64 // increment：回收②永久余额份额
	expire bool  // paid：摘除订阅（expired_at=现在 + 月配额清零）
}

// MarkOrderRefunded 按剩余比例退款（2026-09-08 商业化审计修复）。
//
// 旧语义：仅置 paid→refunded，不撤销已发权益 —— 存在「先充值增量包、消费掉、
// 再退款白嫖」的口子。新语义（已消费的不能退）：
//
//	increment 增量包：按「该单未消费 token 份额 / 包总 token」比例退钱，并回收对应②桶余额
//	paid      包月包：按「剩余生效天数 / 包总天数」比例退钱，并摘除订阅（立即失效+月配额清零）
//	其余类型（free/0元单）：只置状态，无权益可回收
//
// 幂等/并发：事务内先 FOR UPDATE 锁订单行（重复退款串行、二次进来自检状态），
// 再 FOR UPDATE 锁租户行（与 UsageSink 扣减串行，避免回收与消费竞态）。
// 返回 (订单, 是否本次实际流转)；已退款/已关闭等 → flowed=false（400 语义由调用方映射）。
func MarkOrderRefunded(orderID uint) (*model.BillingOrder, bool, error) {
	now := time.Now()
	var flow bool
	var refundInfo struct {
		refund int64
		tokens int64
	}
	err := db.DB.Transaction(func(tx *gorm.DB) error {
		// 1) 锁订单行：同一订单并发重复退款在此串行化
		var o model.BillingOrder
		// GORM v2 行锁：clause.Locking{Strength:"UPDATE"}（v1 的 gorm:query_option 在 v2 已失效，2026-09-09 审计修复）
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&o, orderID).Error; err != nil {
			return err
		}
		if o.Status != "paid" {
			return nil // 已处理过/非可退状态，非本次流转
		}
		// 2) 锁租户行：与 UsageSink/DeductTokensActual 的行锁扣减互斥
		var t model.Tenant
		if o.TenantID != nil {
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&t, *o.TenantID).Error; err != nil {
				return err
			}
		}
		// 3) 计算按剩余比例退款金额 + 应回收权益（无剩余则拒绝，已消费不退）
		cb, refund, err := computeRefundForOrder(tx, o, t)
		if err != nil {
			return err
		}
		if refund > int64(o.AmountCents) {
			refund = int64(o.AmountCents)
		}
		if refund < 0 {
			refund = 0
		}
		// 4) 条件更新订单（RowsAffected 双保险兜底并发）
		res := tx.Model(&model.BillingOrder{}).
			Where("id = ? AND status = 'paid'", orderID).
			Updates(map[string]any{
				"status":              "refunded",
				"refunded_at":         now,
				"refund_amount_cents": refund,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil
		}
		flow = true
		refundInfo.refund = refund
		// 5) 落地权益回收
		if cb != nil {
			if cb.tokens > 0 {
				v := cb.tokens
				r := tx.Model(&model.Tenant{}).
					Where("id = ?", *o.TenantID).
					Update("token_balance", gorm.Expr("GREATEST(COALESCE(token_balance,0)-?,0)", v))
				if r.Error != nil {
					return r.Error
				}
				refundInfo.tokens = v
			}
			if cb.expire {
				r := tx.Model(&model.Tenant{}).
					Where("id = ?", *o.TenantID).
					Updates(map[string]any{
						"expired_at":          now,
						"monthly_token_quota": 0,
						"monthly_token_used":  0,
					})
				if r.Error != nil {
					return r.Error
				}
			}
			InvalidateShadow(*o.TenantID) // 计费统一：退款回收后影子余额失效
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	if flow {
		IncPaymentFailed() // 退款计为支付失败（成功率分母）
		log.Printf("[Billing] 退款受理 order=%d refund=%d分 tokens回收=%d", orderID, refundInfo.refund, refundInfo.tokens)
	}
	var o model.BillingOrder
	if err := db.DB.First(&o, orderID).Error; err != nil {
		return nil, false, err
	}
	return &o, flow, nil
}

// computeRefundForOrder 计算订单的按比例退款金额与需回收的权益。
// 规则：已消耗/已过期的部分一律不退（ErrRefundNoRemaining）；未消耗部分按比例退并清零回收。
func computeRefundForOrder(tx *gorm.DB, o model.BillingOrder, t model.Tenant) (*refundClawback, int64, error) {
	if o.TenantID == nil {
		return nil, 0, nil // 无租户归属的 legacy 单：仅置状态，无权益可回收
	}
	// 2026-09-09 换包升级防双重：该订单已被另一单作为升级抵扣基数（UpgradeBaseOrderID 指向它），
	// 其剩余价值已在升级时折算进新包金额——若再退款等于"退了旧的钱还白拿新包"，拒绝。
	var usedAsBase int64
	tx.Model(&model.BillingOrder{}).
		Where("upgrade_base_order_id = ? AND status = 'paid'", o.ID).Count(&usedAsBase)
	if usedAsBase > 0 {
		return nil, 0, ErrRefundNoRemaining
	}
	if o.PackageID == 0 {
		return nil, 0, nil // legacy 单（无商业包）：仅置状态
	}
	var pkg model.Package
	if err := tx.First(&pkg, o.PackageID).Error; err != nil {
		return nil, 0, fmt.Errorf("退款计算失败: 订单%d 关联包不存在: %w", o.ID, err)
	}
	switch pkg.PType {
	case model.PackageTypeIncrement:
		// 仅 token 制增量包参与退款；旧版次数制（TokenAmount=0）无权益可回收
		if pkg.TokenAmount <= 0 || o.AmountCents <= 0 {
			return nil, 0, nil
		}
		remaining := orderIncrementRemaining(tx, o, t, pkg.TokenAmount)
		if remaining <= 0 {
			return nil, 0, ErrRefundNoRemaining // 已全部消耗，不能退
		}
		refund := (int64(o.AmountCents)*remaining + pkg.TokenAmount/2) / pkg.TokenAmount // 四舍五入到分
		return &refundClawback{tokens: remaining}, refund, nil
	case model.PackageTypePaid:
		// token 制包月按剩余天退；次数制旧包同规则（DurationDays 兜底）
		if pkg.DurationDays <= 0 || o.AmountCents <= 0 {
			return nil, 0, nil
		}
		if t.ExpiredAt == nil || !t.ExpiredAt.After(time.Now()) {
			return nil, 0, ErrRefundNoRemaining // 订阅已过期/未生效，无剩余
		}
		left := int(time.Until(*t.ExpiredAt).Hours()/24) + 1 // 剩余天数（向上取整到天，当天即退）
		if left <= 0 {
			return nil, 0, ErrRefundNoRemaining
		}
		if left > pkg.DurationDays {
			left = pkg.DurationDays
		}
		refund := int64(o.AmountCents) * int64(left) / int64(pkg.DurationDays)
		return &refundClawback{expire: true}, refund, nil
	default:
		return nil, 0, nil // free 等：金额0/注册礼，无权益可回收
	}
}

// orderIncrementRemaining 计算某笔增量包订单「仍未消耗的 token 份额」。
// token_balance 是「多笔增量包 + 邀请奖励」共享的②桶，无法逐单溯源消费归属，
// 采用公平份额口径：remaining = min(桶可用, 全部未退增量包token) × 本单token / 全部未退增量包token。
// 该口径保证多单退款顺序无关、总量不超购入量，且不触碰受邀奖励的②桶余额。
func orderIncrementRemaining(tx *gorm.DB, o model.BillingOrder, t model.Tenant, orderTokens int64) int64 {
	// 全部未退（status=paid）token 制增量包合计
	var rows []struct {
		TokenAmount int64
	}
	if err := tx.Table("billing_orders").
		Select("COALESCE(packages.token_amount,0) AS token_amount").
		Joins("JOIN packages ON packages.id = billing_orders.package_id").
		Where("billing_orders.tenant_id = ? AND billing_orders.status = 'paid'", *o.TenantID).
		Where("packages.p_type = ? AND packages.token_amount > 0", model.PackageTypeIncrement).
		Scan(&rows).Error; err != nil {
		log.Printf("[Billing] 增量包余额合计查询失败 tenant=%d order=%d: %v", *o.TenantID, o.ID, err)
		return 0
	}
	var purchased int64
	for _, r := range rows {
		purchased += r.TokenAmount
	}
	if purchased <= 0 {
		return 0
	}
	poolAvail := t.TokenBalance
	if poolAvail < 0 {
		poolAvail = 0
	}
	if poolAvail > purchased {
		poolAvail = purchased // 桶里超出购入量的部分 = 邀请奖励等，不参与回收
	}
	return poolAvail * orderTokens / purchased
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
