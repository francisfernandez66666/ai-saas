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

// 退款出款状态字典（FIX-1，2026-09-26）。
//
// 四态的分工必须说清楚，否则又会退回"账面说退了、钱其实没出"那种自相矛盾：
//
//	（空）        无需出款：mock/manual 渠道，或从未发起过退款请求
//	psp_pending  意图已落库但钱还没确认出去——含"调 PSP 前就崩了""PSP 明确失败"两种，
//	             都按"可安全重试"处理（重试拿的是同一个 refund_out_no，PSP 侧幂等）
//	psp_ok       PSP 已受理（同步返回成功），但**退款是异步到账**，受理不等于到账终态
//	psp_success  主动查单确认已到账（终态，财务可据此关账）
//	psp_failed   主动查单确认失败/超额被拒（终态，必须人工介入：权益已回收、钱没出去）
//
// 旧实现只有 psp_ok，且注释把它写成"网关已出款"——那是假话，也是本批要修的根因之一。
const (
	RefundPspPending = "psp_pending"
	RefundPspOK      = "psp_ok"
	RefundPspSuccess = "psp_success"
	RefundPspFailed  = "psp_failed"
)

// refundOutNo 取这一单的**稳定**退款单号：一单一号，RF+订单号。
//
// 为什么不再带时间戳（FIX-1 根因）：出款这一步"发起"与"结果回写"跨事务跨进程，
// 补呼与查单都只能凭号问 PSP。旧写法 "RF"+秒级时间戳+订单号 让同一订单两次调用得到两个号，
// 于是上面那句"幂等依赖 PSP 侧按 out_refund_no 去重"根本不成立
// ——微信/支付宝各自靠"可退余额"挡住了第二笔（拒后误标 psp_pending + 群告警），
// 通用 HMAC 网关连这层都没有，等于把退款次数交给不透明的第三方去数。
//
// 若将来允许一单多次部分退，追加 "-2" 序号并把序号一起落库（号仍是确定性的），
// 不要退回"用时间戳区分"——那等于把刚建立的幂等锚再拆掉。
func refundOutNo(o *model.BillingOrder) string {
	if o == nil {
		return ""
	}
	if o.RefundOutNo != "" {
		return o.RefundOutNo
	}
	return "RF" + o.OrderNo
}

// refundWriteFault 退款状态落库的**故障注入点**（生产恒 nil，仅测试置值）。
//
// 为什么要有它：这一步的护栏全是"写库失败时必须少发、且必须响"，而真库里
// 不存在"写资金状态必定失败"的开关——不给注入口，这两条护栏就只能停在注释上
// （本仓历史反复证明：只会打日志并返回成功的守卫等于没有守卫）。
// 反证用例 refund_payout_idempotency_test.go 用它验证"注错即不发 PSP + 告警"，
// 不注入时同一条用例必须绿——两头都在，才算锁住。
//
// 关键语义：这是**故障探针**，不是"写库替身"——探针返回 nil 表示"这次不注入，照常真写"。
// 做成替身会咬自己一口：只让"写终态"失败的那条反向用例里，意图落库那一步会因为
// "探针返回了 nil 但没真写"被判成抢写失败，于是 PSP 一次都没发，
// 测试测的不再是它想测的那条腿（2026-09-26 首跑就是这么红的，如实记在这里）。
var refundWriteFault func(orderID uint, columns []string) error

// refundWriteFaultErr 问一次故障探针（未注入或探针放行时返回 nil，调用方照常真写）
func refundWriteFaultErr(orderID uint, columns []string) error {
	if refundWriteFault == nil {
		return nil
	}
	return refundWriteFault(orderID, columns)
}

// writeRefundPspStatus 回写出款状态并**检查写入结果**（FIX-1：资金状态写失败不许静默）。
//
// 为什么单独抽出来：旧实现 executeRefundPayout 末尾那句 Update 是全链路唯一的资金状态写，
// 既不查 Error 也不看 RowsAffected。写失败时的后果不是"少一行状态"，而是
// 对账器每轮都把它当"出款意图未落库"再呼一次 PSP、每轮给财务群发一封"退款出款失败"，
// 订单永远停在待补呼态。RowsAffected==0 单独判错：那意味着这行订单不在了
// （被清理/ID 错），此时 PSP 侧的钱与账面对不上，比写失败更该响。
//
// 条件带 tenant_id（G-12，2026-09-27）：这是**资金状态写**，"按主键改一行"在这里不够——
// 上层任何一处把别家订单 ID 传进来（对账器扫的正是跨租户队列），旧写法就会真的改走别人家的单，
// 而 RowsAffected 仍是 1、没有任何东西会响。带上租户谓词后这类错配落到"0 行 = 判错"这条腿上。
// 谓词用 `IS NOT DISTINCT FROM` 而不是 `=`：billing_orders.tenant_id 是可空列（历史行确实为 NULL），
// 写成 `=` 会让那些行**永远匹配不上**，等于把老订单的退款状态回写全部打成"0 行判错"。
func writeRefundPspStatus(orderID uint, tenantID *uint, status string) error {
	if err := refundWriteFaultErr(orderID, []string{"refund_psp_status"}); err != nil {
		return err
	}
	res := db.DB.Model(&model.BillingOrder{}).
		Where("id = ? AND tenant_id IS NOT DISTINCT FROM ?", orderID, tenantID).
		Update("refund_psp_status", status)
	if res.Error != nil {
		return fmt.Errorf("退款出款状态回写失败: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("退款出款状态回写影响 0 行（订单 %d 不存在或已被清理）", orderID)
	}
	return nil
}

// writeRefundIntent 落库出款意图（单号 + psp_pending），返回是否本次写入成功。
//
// 条件写 `WHERE id=? AND refund_out_no 为空 AND tenant_id IS NOT DISTINCT FROM ?` 而不是无条件 Update：
// （注：这里刻意不写 SQL 字面量那对单引号——gofmt 会把 doc 注释里的两个连续单引号
// 规范成一个右双引号，改完再格式化就会咬自己一口，2026-09-27 实测）
// 并发/双实例同时补呼时
// 只有一方 RowsAffected=1，另一方拿到 0 后**回读对方写下的那个号**继续用（同号＝PSP 侧幂等），
// 而不是各写各的号——那正是旧实现"一人一个新号"的重复出款根因。
// 租户谓词与 writeRefundPspStatus 同一口径（G-12，含 `IS NOT DISTINCT FROM` 对 NULL 历史行的兼容）：
// 补呼队列是跨租户扫出来的，只按主键写等于把"归属核对"交给调用方不出错——资金状态写不接受这种前提。
// RowsAffected==0 在这里**不是错误**（语义是"别人已经写了"），所以与写失败必须分开返回。
func writeRefundIntent(orderID uint, tenantID *uint, outNo string) (written bool, err error) {
	if ferr := refundWriteFaultErr(orderID, []string{"refund_out_no", "refund_psp_status"}); ferr != nil {
		return false, ferr
	}
	res := db.DB.Model(&model.BillingOrder{}).
		Where("id = ? AND refund_out_no = '' AND tenant_id IS NOT DISTINCT FROM ?", orderID, tenantID).
		Updates(map[string]interface{}{"refund_out_no": outNo, "refund_psp_status": RefundPspPending})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// executeRefundPayout 对真实渠道订单发起 PSP 出款并落 refund_psp_status（P1-2，2026-09-15；FIX-1 收口 2026-09-26）。
// 从 MarkOrderRefunded 抽出，供对账器在"账面 refunded 但出款意图未落库"的崩溃窗口补呼。
//
// 三步顺序是这一步的全部要点（缺一不可）：
//  1. **意图先落库**（写 refund_out_no + psp_pending）：写不成就不调 PSP。
//     宁可少发一笔（下一轮对账补呼，补呼拿同一个号仍然安全），也不能"发了但库里不知道发过"
//     ——后者会让人在 PSP 后台看不见第二笔时选择再发一次，那是真的重复出款。
//  2. 调 PSP：按订单渠道装配适配器（P0-1 修复：曾固定走通用网关，协议不对必败误标）。
//  3. **回写结果并检查错误**：受理→psp_ok，失败→psp_pending（同一号可重试）。
//
// 幂等到这里才真正成立：三处 provider 都读 order.RefundOutNo（同一订单恒同一号），
// 微信 V3/支付宝对同退款单号的重复请求返回原单，而不是当成第二笔。
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
	// ① 意图先落库。读现状这一步不能省：补呼（对账器 / 人工重发）进来时号已经写过了，
	//    必须复用**那个号**，绝不能另拼一个新号（换号＝PSP 认不出这是同一次请求）。
	var cur model.BillingOrder
	if err := db.DB.Select("id", "refund_out_no", "refund_psp_status").First(&cur, o.ID).Error; err != nil {
		log.Printf("[Billing][ERROR] 订单%d(%s) 出款前读状态失败: %v（本次不发 PSP，等下一轮对账）", o.ID, o.OrderNo, err)
		notify.NotifyGroup(fmt.Sprintf("【退款出款未发起】订单 %s 应退 %d 分，出款前读取订单失败(%v)，本次未向支付平台发起请求，下一轮对账自动重试",
			o.OrderNo, refundCents, err))
		return
	}
	outNo := cur.RefundOutNo
	if outNo == "" {
		outNo = "RF" + o.OrderNo
		written, ierr := writeRefundIntent(o.ID, o.TenantID, outNo)
		if ierr != nil {
			log.Printf("[Billing][ERROR] 订单%d(%s) 出款意图落库失败: %v（本次不发 PSP）", o.ID, o.OrderNo, ierr)
			notify.NotifyGroup(fmt.Sprintf("【退款出款未发起】订单 %s 应退 %d 分，退款单号落库失败(%v)，本次未向支付平台发起请求，下一轮对账自动重试",
				o.OrderNo, refundCents, ierr))
			return
		}
		if !written {
			// 并发补呼/双实例抢先写了号：读回**它那个号**继续用（同号＝PSP 侧幂等），绝不另拼新号
			var again model.BillingOrder
			if err := db.DB.Select("refund_out_no").First(&again, o.ID).Error; err != nil || again.RefundOutNo == "" {
				log.Printf("[Billing][ERROR] 订单%d(%s) 抢写后回读退款单号失败: %v", o.ID, o.OrderNo, err)
				notify.NotifyGroup(fmt.Sprintf("【退款出款未发起】订单 %s 退款单号回读失败，本次未发起，请人工核对", o.OrderNo))
				return
			}
			outNo = again.RefundOutNo
		}
	}
	o.RefundOutNo = outNo // 三处适配器统一读这一个号（不再各自拼时间戳）

	// ② 调 PSP ③ 回写结果（回写必查错）
	status := RefundPspOK
	prov, perr := providerForChannel(o.Channel)
	if perr != nil {
		status = RefundPspPending
		log.Printf("[Billing][ERROR] 订单%d(%s) 退款出款适配器装配失败: %v（账面已回收，需人工出款核销）",
			o.ID, o.OrderNo, perr)
		notify.NotifyGroup(fmt.Sprintf("【退款出款失败】订单 %s 应退 %d 分，出款通道未就绪(%v)，权益已回收但资金未出，请财务人工处理",
			o.OrderNo, refundCents, perr))
	} else if rerr := prov.Refund(o, int(refundCents)); rerr != nil {
		status = RefundPspPending
		log.Printf("[Billing][ERROR] 订单%d(%s) PSP 出款失败: %v（账面已回收，需人工出款核销）",
			o.ID, o.OrderNo, rerr)
		notify.NotifyGroup(fmt.Sprintf("【退款出款失败】订单 %s 应退 %d 分，PSP 出款异常(%v)，权益已回收但资金未出，请财务人工处理",
			o.OrderNo, refundCents, rerr))
	}
	if werr := writeRefundPspStatus(o.ID, o.TenantID, status); werr != nil {
		// 钱已经动了、状态却没写上——这是最贵的一种失败：下一轮对账会拿同一个号补呼
		// （PSP 侧因同号而幂等），但必须当场报警，不能只留在日志里。
		log.Printf("[Billing][ERROR] 订单%d(%s) 出款结果落库失败: %v", o.ID, o.OrderNo, werr)
		notify.NotifyGroup(fmt.Sprintf("【退款状态未落库】订单 %s 已按 %s 处理但状态写失败(%v)；退款单号 %s，请财务按号核对支付平台后人工核销",
			o.OrderNo, status, werr, outNo))
	}
}

// RefundResultQuerier 可选能力：向 PSP **主动查**一笔退款的终态（FIX-1 第四步）。
//
// 为什么用"可选接口 + 类型断言"而不是塞进 PaymentProvider：
// 三家协议不对称——微信 V3 有独立查单端点、支付宝有 fastpay.refund.query、
// 通用 HMAC 网关的契约里根本没有查单。强行统一会让某家只能"永远返回成功"来交差，
// 那种假实现比不做更坏（它会把 psp_ok 一路翻成 psp_success）。
// 未实现该接口的渠道由调用侧记为"不可回查"，状态停在 psp_ok 并进观测位。
type RefundResultQuerier interface {
	// QueryRefundStatus 返回归一化结果：success / failed / processing（其余值按 processing 处理）
	QueryRefundStatus(order *model.BillingOrder, outRefundNo string) (string, error)
}

// reconcileRefundQueryBatch 单轮回查上限（配确定性排序，口径同 reconcilePayoutBatch）。
const reconcileRefundQueryBatch = 100

// refundResultQueryQuery 回查取单的唯一构造点：已受理、有号、且不在刚发起的窗口内。
//
// 为什么只捞 psp_ok：psp_pending 是"没发出去/发失败了"，该走补呼（reconcilePayoutMissingQuery
// 那条腿管空态）而不是查单；psp_success/psp_failed 已终态，反复查没有意义。
// 10 分钟避让窗与出款侧同口径，防把刚受理、PSP 侧还没落地的单查成"失败"。
func refundResultQueryQuery(gdb *gorm.DB) *gorm.DB {
	return gdb.Where("status = 'refunded' AND refund_psp_status = ?", RefundPspOK).
		Where("refund_out_no <> ''").
		Where("updated_at < NOW() - INTERVAL '10 minutes'").
		Order("id ASC").
		Limit(reconcileRefundQueryBatch)
}

// ReconcileRefundResults 主动查单收敛退款终态（FIX-1 第四步）：
// 把 psp_ok（"已受理"）推向 psp_success / psp_failed，让账面不再长期骗财务。
// 挂在既有 billing:reconcile 小时 ticker 上（cmd/server/main.go），不新起定时器。
func ReconcileRefundResults() int {
	return reconcileRefundResultsWith(providerForChannel)
}

// reconcileRefundResultsWith 查单收敛的实现体，渠道装配器由调用方注入
// （生产传 providerForChannel；测试传真假 PSP——真实商户号不可能在 CI 里查单，
// 不给这个注入口，"查单失败不改状态"这条资金判据就只能靠人肉读代码相信）。
//
// 返回本轮改写为终态的单数。三条纪律：
//   - 查单失败（网络/协议错）**不改状态**：一次查不到不代表钱没出去，误判成 failed
//     会把一笔真退款从人工核销队列里摘掉；
//   - processing 原样留着，下一轮再查（微信侧退款可能几小时才终态）；
//   - 判成 failed 必须群告警：权益已经回收了，钱又被 PSP 退回，客户两头落空。
func reconcileRefundResultsWith(resolve func(string) (PaymentProvider, error)) int {
	var orders []model.BillingOrder
	if err := refundResultQueryQuery(db.DB).Find(&orders).Error; err != nil {
		log.Printf("[Billing] 退款回查取单失败: %v", err)
		return 0
	}
	changed := 0
	for _, oo := range orders {
		order := oo
		if reconcileOneRefundResult(&order, resolve) {
			changed++
		}
	}
	// 观测位回填：本轮没查、或查了仍未终态的单都算"受理未证实"积压（口径见 metrics 侧注释）。
	// 计数失败只跳过本轮水位，不影响收敛本身——观测面不得把资金任务带崩。
	var unverified int64
	// g12:platform：这一条是**跨租户水位**（"全平台还有几笔受理未证实"），不是某一家的明细，
	// 按租户过滤会得到"当前这一家 0 笔"的假绿，而告警恰恰要看全局积压。
	// 写成单行是刻意的：链式换行会让 G-12 分类器按行判豁免标记时抓不到候选行。
	if err := db.DB.Model(&model.BillingOrder{}).Where("status = 'refunded' AND refund_psp_status = ?", RefundPspOK).Count(&unverified).Error; err == nil { // g12:platform 跨租户观测聚合
		metrics.SetRefundOutcomeUnverified(unverified)
	}
	if changed > 0 {
		log.Printf("[Billing] 退款回查完成，收敛终态 %d 笔", changed)
	}
	return changed
}

// reconcileOneRefundResult 收敛**一笔**退款，返回是否改写成了终态。
//
// 为什么单独成函数（而不是留在上面的循环里）：真实商户号不可能在 CI 里被查单，
// 循环体又不依赖任何注入点——不拆开，"查单失败/处理中一律不改状态"这四条资金判据
// 就只能靠人肉读代码相信。拆开之后单测可以按五态逐格打（refund_payout_idempotency_test.go），
// 生产循环与测试走的是同一段代码。
func reconcileOneRefundResult(order *model.BillingOrder, resolve func(string) (PaymentProvider, error)) bool {
	prov, err := resolve(order.Channel)
	if err != nil {
		return false // 通道未就绪：连问都没处问，保持 psp_ok 等人工
	}
	q, ok := prov.(RefundResultQuerier)
	if !ok {
		return false // 该渠道无查单协议（通用 HMAC 网关）：刻意不猜，停在 psp_ok
	}
	st, qerr := q.QueryRefundStatus(order, order.RefundOutNo)
	if qerr != nil {
		log.Printf("[Billing] 订单%d(%s) 退款查单失败(号 %s): %v（状态保持 %s，下轮再查）",
			order.ID, order.OrderNo, order.RefundOutNo, qerr, RefundPspOK)
		return false
	}
	var next string
	switch st {
	case "success":
		next = RefundPspSuccess
	case "failed":
		next = RefundPspFailed
	default:
		return false // processing / 未知：不动，等下一轮
	}
	if err := writeRefundPspStatus(order.ID, order.TenantID, next); err != nil {
		log.Printf("[Billing][ERROR] 订单%d(%s) 退款终态回写失败: %v", order.ID, order.OrderNo, err)
		notify.NotifyGroup(fmt.Sprintf("【退款终态未落库】订单 %s 支付平台侧已是 %s，本地写状态失败(%v)；退款单号 %s，请人工核销",
			order.OrderNo, next, err, order.RefundOutNo))
		return false
	}
	log.Printf("[Billing] 订单%d(%s) 退款终态收敛 %s→%s（号 %s）", order.ID, order.OrderNo, RefundPspOK, next, order.RefundOutNo)
	if next == RefundPspFailed {
		notify.NotifyGroup(fmt.Sprintf("【退款确认失败】订单 %s 支付平台侧已判退款失败，退款单号 %s；该单权益已回收、资金未出，请财务立即人工处理",
			order.OrderNo, order.RefundOutNo))
	}
	return true
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
//	paid      包月包：按「未消耗积分 / 本单应发放积分」比例退钱（2026-09-24 口径变更，
//	          旧为剩余天数比例——见 paidRefundCents），并摘除订阅（立即失效+月配额清零）
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
		// token 制包月按未消耗积分比例退；次数制旧包（无月度 token 配额）回退剩余天数比例
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
		refund := paidRefundCents(t, pkg, int64(o.AmountCents), start, nowT)
		if refund <= 0 {
			// 2026-09-24 用户拍板口径「不退已消耗积分」：①桶当期额度已用满即视为这份钱
			// 花完了，一律拒退（旧的天数比例口径在这里仍会退钱——见 paidRefundCents 注释）。
			return nil, 0, ErrRefundNoRemaining
		}
		// 到期日回退量仍按"本单未交付完的剩余天数"（服务窗口的物理长度），与退款金额解耦：
		// 钱按积分消耗算，日期按天回退。
		left := int(end.Sub(nowT).Hours()/24) + 1 // 本单剩余天数（当天即退向上取整）
		if left > pkg.DurationDays {
			left = pkg.DurationDays
		}
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

// paidRefundCents 包月订单按「未消耗积分比例」计算应退金额（分）。
//
// 口径（2026-09-24 用户拍板：按积分余额比例退款，不退已消耗积分）：
//
//	应退 = 实付 × 未消耗积分 / 本单应发放积分
//	未消耗 = 尚未过完的期数 × 月度额度 − 当期已消耗（used 是租户级共享计数，
//	        多单并存时钳到本包额度：宁可少退不超退，见函数内注释）
//
// 为什么不用旧的天数比例：天数只反映"服务期过了多久"，不反映"额度用了多少"。
// 第 2 天就把整月 300 万 token 跑完的租户，按天数比例仍能退 28/30≈93% 的钱，
// 而那笔额度是真实发生的算力成本——退款后等于白拿一个月用量。按额度比例退，
// 用满即一分不退（拒退走 ErrRefundNoRemaining），与增量包"已消费不可退"同口径。
//
// 期数：包月按月度额度分期发放（DurationDays=30 → 1 期，90 → 3 期）。
// 已整月过完的期次额度随月重置作废，计为全额消耗——所以历史期不参与退，
// 只有"当期剩余 + 未到期数"折算成可退份额。
// legacy 次数制包（token_amount=0，无额度维度）回退剩余天数比例，保持旧行为。
func paidRefundCents(t model.Tenant, pkg model.Package, amount int64, start, now time.Time) int64 {
	if amount <= 0 {
		return 0
	}
	if pkg.TokenAmount <= 0 {
		// legacy 次数制：没有积分维度可退，只能按剩余天数
		elapsed := int(now.Sub(start).Hours() / 24)
		left := pkg.DurationDays - elapsed
		if left < 0 {
			left = 0
		}
		if left > pkg.DurationDays {
			left = pkg.DurationDays
		}
		return amount * int64(left) / int64(pkg.DurationDays)
	}
	periods := (pkg.DurationDays + 29) / 30 // 本单覆盖的月度额度期数（向上取整，30→1 期）
	if periods < 1 {
		periods = 1
	}
	elapsedDays := int(now.Sub(start).Hours() / 24)
	if elapsedDays < 0 {
		elapsedDays = 0
	}
	elapsedPeriods := elapsedDays / 30 // 已整月过完的期数（额度已作废，视为全额消耗）
	if elapsedPeriods > periods {
		elapsedPeriods = periods
	}
	notYet := periods - elapsedPeriods // 含当期在内的"还活着"的期数
	if notYet <= 0 {
		return 0
	}
	// 当期已消耗：①桶 monthly_token_used 是租户级共享计数。多笔包月并存时它可能大于
	// 本包额度，此处钳到本包额度——宁可判"这份已用满"少退钱，也不能反过来把别人的用量
	// 算成本单未消耗而超退。真实用量按各单额度分摊是后续项，本口径不会漏钱只会偏保守。
	used := t.MonthlyTokenUsed
	if used < 0 {
		used = 0 // 脏数据/并发清零窗口：欠账不为负，按零消耗处理
	}
	if used > pkg.TokenAmount {
		used = pkg.TokenAmount
	}
	remaining := int64(notYet)*pkg.TokenAmount - used
	if remaining <= 0 {
		return 0
	}
	total := int64(periods) * pkg.TokenAmount
	return (amount*remaining + total/2) / total
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
//
// FIX-9（2026-09-27）：开具成功即同步触达客户，并把如实的结果码写回订单。
// 触达点放在这里而不是 HTTP handler 里，是为了让"开了票但没告诉客户"这条路**在结构上不存在**——
// handler 里加一句，下一个调用方（未来的批量开票、财务导入）就会漏掉它。
// 重复发送由状态机本身挡住：只有 requested 能进这里，第二次调用直接报错，
// 所以"重发"必须走 ResendInvoiceNotice 这条显式路径（它只认 issued，且会留新时刻与新结果码）。
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
	// 触达失败**不回滚开具**：发票已经在税务侧开出来了，状态必须如实是 issued；
	// 此时唯一的正确动作是把"客户没收到"记下来并交给超管台重发，而不是把票退回待开具让人再开一张。
	NotifyInvoiceIssued(&o)
	return &o, nil
}

// ResendInvoiceNotice 重发开票交付邮件（FIX-9）：给"已开具但客户没收到"那批单一条出路。
//
// 为什么要有这条：InvoiceIssuedEmail 的结果码里有四档（no_recipient / log_only /
// send_failed / send_timeout）意味着客户侧什么都没发生，而状态机此刻已经锁死在 issued——
// 没有重发入口，运营就只能让客户重新申请、超管作废再开，为了补一封邮件去改两张票据记录，
// 没人会真这么做，结果就是这些单永远停在"账面已交付"。
// 只认 issued：未开具的单重发等于凭空承诺一张还不存在的发票。
func ResendInvoiceNotice(orderID uint) (*model.BillingOrder, string, error) {
	var o model.BillingOrder
	if err := db.DB.First(&o, orderID).Error; err != nil {
		return nil, "", fmt.Errorf("订单不存在")
	}
	if o.InvoiceStatus != "issued" {
		return nil, "", fmt.Errorf("仅已开具的发票可重发交付通知（当前 %s）", o.InvoiceStatus)
	}
	if o.InvoiceNo == "" {
		return nil, "", fmt.Errorf("该订单尚无发票号，无法交付")
	}
	code := NotifyInvoiceIssued(&o)
	return &o, code, nil
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
