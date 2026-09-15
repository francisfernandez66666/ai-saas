// 退款与发票（D2a 文件拆分 2026-09-12）：按比例退款回收、增量份额计算、发票申请。
package billing

import "ai-scrm/internal/metrics"

import "ai-scrm/internal/notify"

import (
	"fmt"
	"log"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/webhook"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// executeRefundPayout 对真实渠道订单发起 PSP 出款并落 refund_psp_status（P1-2，2026-09-15）。
// 从 MarkOrderRefunded 抽出，供对账器在"账面 refunded 但出款意图未落库"的崩溃窗口补呼。
// 幂等依赖 PSP 侧按 out_refund_no=订单号 去重（微信 V3/支付宝同退款单号重复请求返回原单）。
func executeRefundPayout(o *model.BillingOrder, refundCents int64) {
	if o == nil || refundCents <= 0 {
		return
	}
	// P0-1 渠道分发修复(2026-09-15)：原实现固定 loadGatewayProvider 出款——
	// wechat/alipay 渠道订单退款会被打到通用网关端点（协议不对，必败误标 psp_pending）。
	// 现按订单 channel 装配对应适配器出款。
	if o.Channel == "" || o.Channel == "mock" || o.Channel == "manual" {
		return // 模拟/人工渠道无资金动作
	}
	status := "psp_ok"
	prov, perr := providerForChannel(o.Channel)
	if perr != nil {
		status = "psp_pending"
		log.Printf("[Billing][ERROR] 订单%d(%s) 退款出款适配器装配失败: %v（账面已回收，需人工出款核销）",
			o.ID, o.OrderNo, perr)
		notify.NotifyGroup(fmt.Sprintf("【退款出款失败】订单 %s 应退 %d 分，出款通道未就绪(%v)，权益已回收但资金未出，请财务人工处理",
			o.OrderNo, refundCents, perr))
	} else if rerr := prov.Refund(o, int(refundCents)); rerr != nil {
		status = "psp_pending"
		log.Printf("[Billing][ERROR] 订单%d(%s) PSP 出款失败: %v（账面已回收，需人工出款核销）",
			o.ID, o.OrderNo, rerr)
		notify.NotifyGroup(fmt.Sprintf("【退款出款失败】订单 %s 应退 %d 分，PSP 出款异常(%v)，权益已回收但资金未出，请财务人工处理",
			o.OrderNo, refundCents, rerr))
	}
	db.DB.Model(&model.BillingOrder{}).Where("id = ?", o.ID).Update("refund_psp_status", status)
}

// refundClawback 退款需同步回收的权益（关闭「付费→退款→白嫖」口子）
type refundClawback struct {
	tokens    int64      // increment：回收②永久余额份额
	expire    bool       // paid：摘除订阅（expired_at=现在 + 月配额清零）
	shrinkDay int        // paid 非最新订阅单：expired_at 仅回退该单剩余天数（R12 多单不误伤）
	expireTo  *time.Time // C1 修复(2026-09-14)：退最新单但更早订阅单窗口未耗尽 → expired_at 回落到其最晚窗口终点而非 now
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
				// C1 修复(2026-09-14)：退最新单时若更早订阅窗口未耗尽，到期日回落到该窗口终点
				// 且保留月配额（订阅仍有效）；确无在途订阅才即刻摘除清零。
				exp := now
				updates := map[string]any{"expired_at": exp}
				if cb.expireTo != nil && cb.expireTo.After(now) {
					updates["expired_at"] = *cb.expireTo
				} else {
					updates["monthly_token_quota"] = 0
					updates["monthly_token_used"] = 0
				}
				r := tx.Model(&model.Tenant{}).
					Where("id = ?", *o.TenantID).
					Updates(updates)
				if r.Error != nil {
					return r.Error
				}
			}
			// R12：非最新订阅单只把到期日回退本单剩余天数（不误伤后续订阅）
			if cb.shrinkDay > 0 && t.ExpiredAt != nil {
				newExp := t.ExpiredAt.AddDate(0, 0, -cb.shrinkDay)
				if newExp.Before(now) {
					newExp = now
				}
				if r := tx.Model(&model.Tenant{}).Where("id = ?", *o.TenantID).
					Update("expired_at", newExp); r.Error != nil {
					return r.Error
				}
			}
			// R7 修复(2026-09-11)：退款回收邀请人"付费推荐奖励"——此前受邀人首笔包月退款后，
			// 邀请人白留 50 万永久 token（可"付费→退款"洗奖励）。仅当该受邀租户已无其它
			// paid 包月单（触发单消失）时回收，并重置幂等闸门（再付费可再得，语义一致）。
			if cb.expire || cb.shrinkDay > 0 {
				ClawbackPaidReferralReward(tx, t)
			}
			InvalidateShadow(*o.TenantID) // 计费统一：退款回收后影子余额失效
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	if flow {
		metrics.IncPaymentFailed() // 退款计为支付失败（成功率分母）
		log.Printf("[Billing] 退款受理 order=%d refund=%d分 tokens回收=%d", orderID, refundInfo.refund, refundInfo.tokens)
		// D6：出站事件 webhook 扇出（order.refunded），旁路不阻塞
		var rtid uint
		db.DB.Model(&model.BillingOrder{}).Where("id = ?", orderID).Select("tenant_id").Scan(&rtid)
		if rtid > 0 {
			webhook.Emit(rtid, model.WebhookEventOrderRefunded, map[string]interface{}{
				"order_id":            orderID,
				"refund_amount_cents": refundInfo.refund,
				"status":              "refunded",
			})
		}
		// R8 修复(2026-09-11)：真实渠道订单联动 PSP 出款——此前退款只回收权益，
		// 账面 refunded 与客户实际收到退款完全脱钩。出款失败不吞：订单标 psp_pending
		// + 群告警，账面与资金状态显式分离，财务可据此人工出款核销。
		// P1-2 改造(2026-09-15)：出款动作抽成 executeRefundPayout——对账器需要在
		// "事务提交后、出款落库前崩溃"的窗口里补呼（refunded 且 psp_status 为空）。
		var o2 model.BillingOrder
		if db.DB.First(&o2, orderID).Error == nil && refundInfo.refund > 0 {
			executeRefundPayout(&o2, refundInfo.refund)
		}
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
	// P1-4 修复(2026-09-15)：口径从 status='paid' 扩到 IN ('paid','pending')——
	// 升级单 U 尚在 pending（用户未扫码/回调未到）时退掉基数单 X，随后 U 迟到到账被
	// Reopen 复活按"已消失的抵扣净值"发货：同一份剩余价值退钱+抵款两次兑现。
	// pending 是 15 分钟自然态，拒退让财务稍后再操作，比双花可控。
	var usedAsBase int64
	tx.Model(&model.BillingOrder{}).
		Where("upgrade_base_order_id = ? AND status IN ('paid','pending')", o.ID).Count(&usedAsBase)
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
		// R12 修复(2026-09-11)：退款窗口改用"本订单自身生效窗口"（paid_at → +DurationDays），
		// 不再读租户级 expired_at——多笔包月叠加时 expired_at 被续到最后一单之后，
		// 退旧单会按"全部剩余天"超退（旧单第10天退，租户还剩50天 → 竟退 50/30 天封顶全额）。
		// 多单场景退旧单只回收旧单自己的剩余窗口，租户 expired_at 相应回退（shrinkDay），
		// 仅当本单是最新一笔付费订阅时才整体摘除（expire）。
		start := o.CreatedAt
		if o.PaidAt != nil {
			start = *o.PaidAt // 到账时间即窗口起点（存量单 paid_at 为空时回落 created_at）
		}
		end := start.AddDate(0, 0, pkg.DurationDays)
		nowT := time.Now()
		if !end.After(nowT) {
			return nil, 0, ErrRefundNoRemaining // 本单窗口已耗尽，已消费不退
		}
		left := int(end.Sub(nowT).Hours()/24) + 1 // 本单剩余天数（当天即退向上取整）
		if left > pkg.DurationDays {
			left = pkg.DurationDays
		}
		refund := int64(o.AmountCents) * int64(left) / int64(pkg.DurationDays)
		// 是否存在比本单更晚生效且仍 paid 的包月单：有 → 只回到期日；无 → 整体摘除
		var later int64
		tx.Model(&model.BillingOrder{}).
			Joins("JOIN packages ON packages.id = billing_orders.package_id").
			Where("billing_orders.tenant_id = ? AND billing_orders.status = 'paid' AND billing_orders.id <> ?", *o.TenantID, o.ID).
			Where("packages.p_type = ? AND COALESCE(billing_orders.paid_at, billing_orders.created_at) > ?",
				model.PackageTypePaid, start).
			Count(&later)
		if later > 0 {
			return &refundClawback{shrinkDay: left}, refund, nil
		}
		// C1 修复(2026-09-14)：旧逻辑"无更晚单 → 整体摘除 expired_at=now"，叠加订阅下
		// 会把**更早订单未消耗的窗口**一并清零（3/1 买 A 至 3/31 + 3/15 买 B 至 4/14，
		// 3/20 退 B → A 剩余 11 天蒸发）。改为回落到其余仍付费订阅单的最晚窗口终点。
		var other struct {
			MaxEnd *time.Time
		}
		tx.Table("billing_orders o2").
			Select("MAX(COALESCE(o2.paid_at, o2.created_at) + (COALESCE(p2.duration_days, 0) * INTERVAL '1 day')) AS max_end").
			Joins("JOIN packages p2 ON p2.id = o2.package_id").
			Where("o2.tenant_id = ? AND o2.id <> ? AND o2.status = 'paid' AND p2.p_type = ?",
				*o.TenantID, o.ID, model.PackageTypePaid).
			Scan(&other)
		if other.MaxEnd != nil && other.MaxEnd.After(nowT) {
			expireTo := *other.MaxEnd
			return &refundClawback{expire: true, expireTo: &expireTo}, refund, nil
		}
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
func RequestInvoice(orderID uint, args ...string) (*model.BillingOrder, error) {
	var o model.BillingOrder
	if err := db.DB.First(&o, orderID).Error; err != nil {
		return nil, fmt.Errorf("订单不存在")
	}
	if o.Status != "paid" {
		return nil, fmt.Errorf("仅已支付订单可申请发票（当前 %s）", o.Status)
	}
	// §W 发票极限(2026-09-14)：抬头/税号/邮箱随申请落库（旧实现前端硬编码"AI-SCRM服务费"、
	// 无税号字段）——资质到位前支持"人工开票 + 超管回录发票号"，requested→issued→voided 状态机。
	upd := map[string]interface{}{"invoice_requested": true, "invoice_status": "requested"}
	if len(args) > 0 && args[0] != "" {
		upd["invoice_title"] = args[0]
	}
	if len(args) > 1 && args[1] != "" {
		upd["invoice_tax_no"] = args[1]
	}
	if len(args) > 2 && args[2] != "" {
		upd["invoice_email"] = args[2]
	}
	// 已开票(reissued 前)允许重提更新抬头；issued 之后拒绝改
	if o.InvoiceStatus == "issued" {
		return nil, fmt.Errorf("发票已开具，如需修改请联系平台作废")
	}
	if err := db.DB.Model(&model.BillingOrder{}).Where("id = ?", orderID).
		Updates(upd).Error; err != nil {
		return nil, err
	}
	db.DB.First(&o, orderID)
	return &o, nil
}

// IssueInvoice §W：超管人工开票后回录发票号，置 issued（幂等：仅 requested 可转 issued）。
func IssueInvoice(orderID uint, invoiceNo string) (*model.BillingOrder, error) {
	var o model.BillingOrder
	if err := db.DB.First(&o, orderID).Error; err != nil {
		return nil, fmt.Errorf("订单不存在")
	}
	if o.InvoiceStatus != "requested" {
		return nil, fmt.Errorf("仅已申请待开具的发票可回录（当前 %s）", o.InvoiceStatus)
	}
	if invoiceNo == "" {
		return nil, fmt.Errorf("发票号必填")
	}
	if err := db.DB.Model(&model.BillingOrder{}).Where("id = ?", orderID).
		Updates(map[string]interface{}{"invoice_status": "issued", "invoice_no": invoiceNo}).Error; err != nil {
		return nil, err
	}
	db.DB.First(&o, orderID)
	return &o, nil
}

// VoidInvoice §W：作废发票（issued→voided），允许租户重新申请。
func VoidInvoice(orderID uint) (*model.BillingOrder, error) {
	var o model.BillingOrder
	if err := db.DB.First(&o, orderID).Error; err != nil {
		return nil, fmt.Errorf("订单不存在")
	}
	if o.InvoiceStatus == "" {
		return nil, fmt.Errorf("该订单未申请发票")
	}
	if err := db.DB.Model(&model.BillingOrder{}).Where("id = ?", orderID).
		Updates(map[string]interface{}{"invoice_status": "voided", "invoice_requested": false}).Error; err != nil {
		return nil, err
	}
	db.DB.First(&o, orderID)
	return &o, nil
}
