// 到期催缴状态机 dunning（D3，2026-09-23）：欠费不再只靠销售人肉盯群。
//
// 此前事实链：ExpireCheck 在到期日把租户 active→expired（写侧 402），
// 提醒只有 7d/3d 两档**平台群**消息 + 一封到期邮件，之后系统就沉默了。
// 结果是：到期租户如果没人盯，就那么挂着直到忘记，续费全凭运气；
// 而真正需要"停止服务"这一步时，也没有任何地方记录"我们什么时候说过什么"。
//
// 本文件补的是这条序列：**到期后第 N 天各发一次催缴，宽限期末自动停用，续费自动解除**。
// 档位（dunning_steps）、封禁阈值（dunning_suspend_after_days）、总闸（dunning_enabled）
// 全部走平台级热配置（租户不可自改，判据见 runtimecfg.PlatformLevelKeys 注释）。
//
// 四条设计决定：
//  1. **只处理 status=expired 的租户**：trial/active 的到期由 ExpireCheck 先摘除，
//     本序列不重复判定"到期"这件事，避免两处真相；已 suspended 的不再推档
//     （停用时那封"已停止登录"的信就是序列最后一响，后续是销售的活，超管队列里看得见）。
//  2. **档位只在前进时发信**：planDunning 拿"已越过的最高档"，与库里 stage 比较，
//     不大于 stage 就不发——巡检每小时跑，这条就是"同一档不重复轰炸"的全部机制，
//     不依赖时间窗，也不需要在表里存去重键。
//  3. **封禁归因可区分**：只有 billing_dunning.suspended_at 非空的那次封禁是本序列施加的，
//     续费时才允许自动解除；超管在 super.go 的人工封禁走 super_tenant_status 审计，
//     本序列绝不"替客户决定"把它撤销——那等于欠费户点个按钮就绕过了运营封禁。
//  4. **与续费/摘除共用同一把锁**（expireRenewLockKey）：MarkOrderPaid→GrantPackage 会把
//     status 改回 active 并推后 expired_at；若本序列并发判定为"该封了"，就会出现
//     "刚付完款就被停用"的资金侧事故。共用锁把三者串成一条线，代价只是本轮让路、下小时再来。
package billing

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/metrics"
	"ai-scrm/internal/model"
	"ai-scrm/internal/notify"
	"ai-scrm/internal/redisclient"
	"ai-scrm/internal/runtimecfg"

	"gorm.io/gorm"
)

// 催缴档位默认序列与宽限天数（配置键缺失/非法时兜底，与 config_defaults.go 播种值一致）
var (
	defaultDunningSteps        = []int{0, 3, 7, 14}
	defaultDunningSuspendAfter = 14
)

// dunningAuditSuspendAction 自动封禁审计动作码（与超管人工的 super_tenant_status 严格区分，
// DunningOnPaid 判"这次封禁是不是我们施加的"就靠 suspended_at + 该动作码双证）
const dunningAuditSuspendAction = "dunning_suspend"

// userErr 构造"说给人听"的领域错误（裁决不成立，例如没有序列、没有到期时间）。
// 为什么要专门包一层而不是直接 errors.New：API 层要把这类消息原样回显给超管看，
// 而 DB 错误里可能带 SQL 片段/列名——P1-3 的错误脱敏红线要求两者在源头就区分开，
// 不能让调用方去猜"这条 error 能不能给客户端看"。
func userErr(msg string) error { return dunningUserError{msg: msg} }

// dunningUserError 可回显错误
type dunningUserError struct{ msg string }

// Error 实现 error 接口
func (e dunningUserError) Error() string { return e.msg }

// IsUserFacing 该错误是否可原样回显给调用方（领域里写好的中文判定，不含内部细节）
func IsUserFacing(err error) bool {
	var e dunningUserError
	return errors.As(err, &e)
}

// dunningManualSuspendAction 超管人工封禁审计动作码（super.go:SuperTenantStatus 写入，本包只读它）
const dunningManualSuspendAction = "super_tenant_status"

// dunningAction 一轮对某租户要做的动作（planDunning 的返回值）
type dunningAction struct {
	Notify      bool // 是否发一封催缴（档位前进或施加停用时才发）
	Stage       int  // 动作后的档位序号（1 起）
	DayPast     int  // 到期后第几天
	Suspend     bool // 是否施加停用
	WillSuspend bool // 本轮不封，但宽限期末会封（文案要提前说清）
}

// planDunning 纯函数裁决：给定已发档位、距到期天数、档位序列与封禁阈值，决定本轮动作。
// 无 DB 无时钟，故可表驱动测到每个边界（非单调配置、跳档补发、封后不再推档、阈值=0 只催不封）。
func planDunning(stage, dayPast int, steps []int, suspendAfter int) dunningAction {
	act := dunningAction{Stage: stage, DayPast: dayPast}
	if dayPast < 0 {
		return act // 还没到期，与 ExpireCheck 口径一致（它负责摘除，我们负责摘除之后）
	}
	if suspendAfter > 0 && dayPast >= suspendAfter {
		act.Suspend = true
		act.Notify = true
		act.Stage = len(steps) // 走到末档：序列就此结束（suspended 后不再推）
		return act
	}
	if suspendAfter > 0 {
		act.WillSuspend = true
	}
	// 取"已越过的最高档"：若一次跨过两档（服务停了两天没跑巡检），只补发最高那一档，
	// 已过时的低档不追发——客户收到"你第 3 天该续费了"的邮件时已经第 7 天了，只会觉得骚扰。
	target := 0
	for i, s := range steps {
		if s >= 0 && dayPast >= s {
			target = i + 1
		}
	}
	if target > stage {
		act.Notify = true
		act.Stage = target
	}
	return act
}

// SweepDunning 催缴巡检（main.go 小时 ticker 调用；返回本轮动作数=发信或封禁次数）
func SweepDunning() int { return sweepDunning(nil) }

// SweepDunningForTenants 只推进指定租户（单测/人工补催用）。
// 口子与 SweepUsageAlertsForTenants 同一动机：全表版会真改其它真实租户的 status，
// 测试绝不允许拿到那个权力。
func SweepDunningForTenants(tenantIDs []uint) int { return sweepDunning(tenantIDs) }

func sweepDunning(scope []uint) int {
	if !runtimecfg.SafeCfgBool("dunning_enabled", false) {
		return 0
	}
	// 设计决定 4：与到期摘除/续费扫描串行，拿不到锁本轮让路（定向巡检不抢锁）
	if len(scope) == 0 && redisclient.IsEnabled() {
		h := redisclient.TryLock(expireRenewLockKey, 55*time.Minute)
		if h == nil {
			return 0
		}
		defer h.Unlock()
	}

	steps := normalizeInts(runtimecfg.SafeCfgIntSlice("dunning_steps", defaultDunningSteps), 0, 3650)
	suspendAfter := dunningSuspendAfter()
	now := time.Now()
	actions := 0

	// 1) 先做恢复对账：把"已经不欠费"的序列关掉（续费之外的路径同样要收口——超管手工改回
	//    active、直接推后到期日等），否则超管队列永远挂着一条已结的催缴。
	reconcileRecoveredDunning(now, scope)

	// 2) 只捞已过期的在册租户（设计决定 1）。
	//    跨租户平台巡检：tenants 表本身无 tenant_id 列，按主键行归属，故走 db.DB 显式条件。
	var tenants []model.Tenant
	q := db.DB.Model(&model.Tenant{}).Where("status = 'expired' AND expired_at IS NOT NULL AND expired_at < ?", now)
	if len(scope) > 0 {
		q = q.Where("id IN ?", scope)
	}
	if err := q.Order("expired_at ASC").Find(&tenants).Error; err != nil {
		log.Printf("[催缴] 读过期租户失败: %v", err)
		return actions
	}
	for _, t := range tenants {
		if t.ExpiredAt == nil {
			continue
		}
		if runDunningForTenant(t, *t.ExpiredAt, steps, suspendAfter, now) {
			actions++
		}
	}
	if actions > 0 {
		log.Printf("[催缴] 本轮动作 %d 次（在催 %d 家，档位=%v，停用阈值=%d 天）", actions, len(tenants), steps, suspendAfter)
	}
	return actions
}

// dunningSuspendAfter 读宽限天数（<=0 表示"只催不封"，与配置描述一致）
func dunningSuspendAfter() int {
	return runtimecfg.SafeCfgInt("dunning_suspend_after_days", defaultDunningSuspendAfter)
}

// DunningConfigView 催缴生效口径。
// 为什么给租户侧也回这份：序列节奏是平台定的（六个键都在 PlatformLevelKeys，租户改不了），
// 后台卡片必须照实说"第 0/3/7/14 天各提醒一次、第 14 天停止登录"，
// 而不是让前端自己写死一套天数——运维改了配置，前端那套就成了第二个真相。
type DunningConfigView struct {
	Enabled          bool  `json:"enabled"`
	Steps            []int `json:"steps"`
	SuspendAfterDays int   `json:"suspend_after_days"`
}

// CurrentDunningConfig 读当前生效的催缴配置（与 sweep 同一套解析口径）
func CurrentDunningConfig() DunningConfigView {
	return DunningConfigView{
		Enabled:          runtimecfg.SafeCfgBool("dunning_enabled", false),
		Steps:            normalizeInts(runtimecfg.SafeCfgIntSlice("dunning_steps", defaultDunningSteps), 0, 3650),
		SuspendAfterDays: dunningSuspendAfter(),
	}
}

// runDunningForTenant 单租户推进一轮，返回是否发生了动作（发信或封禁）
func runDunningForTenant(t model.Tenant, dueAt time.Time, steps []int, suspendAfter int, now time.Time) bool {
	row, created := loadOrCreateDunning(t.ID, dueAt)
	if row.Status != model.DunningStatusRunning {
		return false // 已 resolved/exhausted（到期日变过会被 loadOrCreateDunning 重开）
	}
	if row.SuspendedAt != nil {
		return false // 本序列已经封过，绝不再发第二封
	}
	dayPast := int(now.Sub(dueAt).Hours() / 24)
	act := planDunning(row.Stage, dayPast, steps, suspendAfter)
	if !act.Notify && !act.Suspend {
		return false
	}

	graceEnd := graceEndTime(dueAt, suspendAfter)
	// 宽限期终点落到 tenants.grace_period_end_at：那列此前是死列，本轮起作为
	// 后台/超管看到的"还能撑到哪天"唯一展示值（状态机自身只用派生时间，不回头读它）。
	if graceEnd != nil && (t.GracePeriodEndAt == nil || !t.GracePeriodEndAt.Equal(*graceEnd)) {
		db.DB.Model(&model.Tenant{}).Where("id = ?", t.ID).Update("grace_period_end_at", *graceEnd)
	}

	sent := d3.DunningEmail(notify.DunningEmailInput{
		TenantID: t.ID, Name: t.Name, Code: t.Code,
		Stage: act.Stage, DayPast: dayPast, DueAt: dueAt,
		GraceEnd: graceEnd, WillSuspend: act.WillSuspend && !act.Suspend, Suspended: act.Suspend,
	})
	if sent {
		metrics.IncDunningSent()
	}

	if act.Suspend {
		suspendTenantByDunning(t, row, now)
	}

	updates := map[string]any{
		"stage": act.Stage, "last_notified_at": now, "due_at": dueAt,
	}
	if act.Suspend {
		updates["status"] = model.DunningStatusExhausted
		updates["next_notify_at"] = nil
	} else {
		updates["next_notify_at"] = nextDunningTime(dueAt, steps, act.Stage, now)
	}
	if emails := d3.AdminEmails(t.ID); len(emails) > 0 {
		updates["sent_to"] = maskEmailList(emails)
	}
	detail, _ := json.Marshal(map[string]any{
		"stage": act.Stage, "day_past": dayPast, "notify": act.Notify, "suspend": act.Suspend,
		"delivered": sent, "steps": steps, "suspend_after": suspendAfter, "created_round": created,
	})
	updates["detail"] = string(detail)
	if err := db.DB.Model(&model.BillingDunning{}).Where("id = ? AND tenant_id = ?", row.ID, row.TenantID).Updates(updates).Error; err != nil {
		log.Printf("[催缴] 落状态失败 tenant=%d: %v", t.ID, err)
	}
	return true
}

// graceEndTime 宽限期截止 = 到期日 + 停用阈值天数；阈值<=0 返回 nil（只催不封）
func graceEndTime(dueAt time.Time, suspendAfter int) *time.Time {
	if suspendAfter <= 0 {
		return nil
	}
	end := dueAt.AddDate(0, 0, suspendAfter)
	return &end
}

// nextDunningTime 下一档预定时刻。
// 返回 nil 有两种含义，都不算异常：① 档位已发完但还没到停用阈值——此后没有"下一封信"可等，
// 停用只取决于天数，靠每小时巡检兜底；② 预定时刻已被本轮跨过（同理，下轮判定自然推进）。
func nextDunningTime(dueAt time.Time, steps []int, stage int, now time.Time) *time.Time {
	if stage <= 0 || stage >= len(steps) {
		return nil
	}
	next := dueAt.AddDate(0, 0, steps[stage])
	if next.Before(now) {
		return nil
	}
	return &next
}

// loadOrCreateDunning 取（或按本轮到期日新建/重开）催缴行；第二返回值表示是否本轮新建。
// 重开条件：库里没有行，或 due_at 与租户当前 expired_at 不一致——含义是"到期日被改过，
// 或续期之后再次到期"。此时旧序列作废、档位归零从头催，避免拿上一轮进度去套新一轮
// （那会在逾期第 1 天就发出第 4 档的"你已经拖了两周"）。
func loadOrCreateDunning(tenantID uint, dueAt time.Time) (model.BillingDunning, bool) {
	var row model.BillingDunning
	err := db.DB.Where("tenant_id = ?", tenantID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		row = model.BillingDunning{TenantID: tenantID, Status: model.DunningStatusRunning, DueAt: &dueAt}
		if cerr := db.DB.Create(&row).Error; cerr != nil { // g12:platform 巡检自建：租户归属由上面 row 字面量显式给出
			// 并发下另一实例刚建行（tenant_id 唯一索引）→ 回读，绝不再写第二条
			if ferr := db.DB.Where("tenant_id = ?", tenantID).First(&row).Error; ferr != nil {
				log.Printf("[催缴] 建序列失败 tenant=%d: %v", tenantID, cerr)
			}
		}
		return row, true
	}
	if err != nil {
		log.Printf("[催缴] 读序列失败 tenant=%d: %v", tenantID, err)
		return model.BillingDunning{TenantID: tenantID, Status: model.DunningStatusRunning, DueAt: &dueAt}, true
	}
	needReopen := row.Status != model.DunningStatusRunning || row.DueAt == nil || !row.DueAt.Equal(dueAt)
	if !needReopen {
		return row, false
	}
	row.Status = model.DunningStatusRunning
	row.Stage = 0
	row.DueAt = &dueAt
	row.NextNotifyAt = nil
	row.SuspendedAt = nil
	if uerr := db.DB.Model(&model.BillingDunning{}).Where("id = ? AND tenant_id = ?", row.ID, row.TenantID).Updates(map[string]any{
		"status": model.DunningStatusRunning, "stage": 0, "due_at": dueAt,
		"next_notify_at": nil, "suspended_at": nil,
	}).Error; uerr != nil {
		log.Printf("[催缴] 重开序列失败 tenant=%d: %v", tenantID, uerr)
	}
	return row, true
}

// suspendTenantByDunning 宽限期满自动停用：只处理"当前仍是 expired"的租户，
// 条件 UPDATE 让续费到账并发的窗口自己关掉（RowsAffected=0 即说明状态已被改走，放弃本次封禁）。
// 缓存说明：TenantResolver 本地缓存 5s TTL，本处不显式广播失效——小时级动作多等 5s 无风险，
// 而引 middleware 包会造出 billing→middleware 的反向依赖（middleware 反过来被 api 依赖）。
func suspendTenantByDunning(t model.Tenant, row model.BillingDunning, now time.Time) {
	res := db.DB.Model(&model.Tenant{}).Where("id = ? AND status = 'expired'", t.ID).
		Update("status", "suspended")
	if res.Error != nil {
		log.Printf("[催缴] 停用失败 tenant=%d: %v", t.ID, res.Error)
		return
	}
	if res.RowsAffected == 0 {
		return // 状态已被并发改走（例如刚好续费到账），不抢
	}
	db.DB.Model(&model.BillingDunning{}).Where("id = ? AND tenant_id = ?", row.ID, row.TenantID).Update("suspended_at", now)
	db.DB.Create(&model.TenantAuditLog{
		TenantID: t.ID, UserID: 0, Action: dunningAuditSuspendAction,
		Resource: fmt.Sprintf("tenant:%d", t.ID),
		Detail:   fmt.Sprintf(`{"reason":"dunning_grace_expired","stage":%d,"at":"%s"}`, row.Stage, now.Format(time.RFC3339)),
	})
	metrics.IncDunningSuspended()
	d3.Group(fmt.Sprintf("【欠费停用】租户「%s」(%s) 到期已过宽限期，已停止登录（数据保留）。续费到账后自动恢复",
		t.Name, t.Code))
	log.Printf("[催缴] 租户%d「%s」宽限期满自动停用（数据保留，续费自动恢复）", t.ID, t.Name)
}

// DunningOnPaid 到账解除：订单真实转 paid 后调用（MarkOrderPaid / ReopenClosedOrderPaid 两个落点）。
// 做两件事：① 催缴序列置 resolved（停止后续所有档位）；② 若停用是**本序列**施加的，解除停用。
// 判据用 suspended_at 非空 + "之后没有出现过超管人工封禁审计"双证——
// 只凭 suspended_at 会误放开"我们封了之后超管又另有心意封了一次"的租户。
//
// 为什么只加这一处就能覆盖"增量包也救回来"的场景：租户被封后只能靠后台/超管代付或
// 已有 API Key 下单（登录已被 403 拦住），只要任何订单到账就会走到本函数——
// 而 GrantPackage 的 paid 分支虽然也会把 status 写成 active，增量包分支不碰 status，
// 所以**解除封禁这件事只能在这里做**，别处都做不了。
func DunningOnPaid(tenantID uint, orderNo string) {
	if tenantID == 0 {
		return
	}
	var row model.BillingDunning
	if err := db.DB.Where("tenant_id = ? AND status <> ?", tenantID, model.DunningStatusResolved).
		First(&row).Error; err != nil {
		return // 没有催缴序列（从未欠费）——最常见路径，静默返回
	}
	now := time.Now()
	detail, _ := json.Marshal(map[string]any{
		"resolved_reason": "paid", "order_no": orderNo, "resolved_at": now.Format(time.RFC3339),
	})
	updates := map[string]any{
		"status": model.DunningStatusResolved, "next_notify_at": nil, "detail": string(detail),
	}

	var t model.Tenant
	if err := db.DB.First(&t, tenantID).Error; err == nil && row.SuspendedAt != nil && t.Status == "suspended" {
		if manualSuspendAfter(tenantID, *row.SuspendedAt) {
			log.Printf("[催缴] 租户%d 存在停用之后的人工封禁，续费不自动解除（交超管判定）", tenantID)
		} else if err := db.DB.Model(&model.Tenant{}).Where("id = ? AND status = 'suspended'", tenantID).
			Update("status", "active").Error; err == nil {
			// 只把状态放回可用：expired_at/配额由 GrantPackage 负责，本函数绝不越权改钱与额度
			updates["suspended_at"] = nil
			d3.Group(fmt.Sprintf("【欠费恢复】租户「%s」(%s) 续费到账，已解除催缴停用", t.Name, t.Code))
			log.Printf("[催缴] 租户%d 续费到账，催缴停用已解除", tenantID)
		}
	}
	if err := db.DB.Model(&model.BillingDunning{}).Where("id = ? AND tenant_id = ?", row.ID, row.TenantID).Updates(updates).Error; err != nil {
		log.Printf("[催缴] 解除序列失败 tenant=%d: %v", tenantID, err)
	}
	// 宽限期终点随序列一起归零：留着会让后台对一家已不欠费的租户显示"宽限期还剩 X 天"
	db.DB.Model(&model.Tenant{}).Where("id = ?", tenantID).Update("grace_period_end_at", nil)
}

// manualSuspendAfter 该租户在 at 之后是否存在超管人工封禁审计
func manualSuspendAfter(tenantID uint, at time.Time) bool {
	var n int64
	db.DB.Model(&model.TenantAuditLog{}).
		Where("tenant_id = ? AND action = ? AND created_at > ? AND detail LIKE ?",
			tenantID, dunningManualSuspendAction, at, `%"suspended"%`).
		Count(&n)
	return n > 0
}

// reconcileRecoveredDunning 把"不再处于欠费在催态"的序列关掉（running 与 exhausted 都管）。
//
// 为什么 exhausted 也要进来：封禁施加后序列停在 exhausted，此后超管手工把租户改回 active
// （线下结清、赠送期等）就没人收口了——超管队列里会永远挂着一家已经不欠费的租户。
//
// 若停用是本序列施加、且其后没有人工封禁审计，顺手把状态交还给 expired
// （不是 active：它确实过期了，只是不该由催缴序列继续封着它——恢复登录只认到账）。
// 反之若租户已不在 suspended 态（人工恢复过），suspended_at 必须一起清零：留着就成了假归因，
// 下一次 DunningOnPaid 会以为"这家的封禁是我们施加的"而去解除一个并不存在的封禁。
func reconcileRecoveredDunning(now time.Time, scope []uint) {
	var rows []model.BillingDunning
	q := db.DB.Where("status <> ?", model.DunningStatusResolved) // g12:platform 跨租户对账（billing_dunning 一户一行，平台序列表）
	if len(scope) > 0 {
		q = q.Where("tenant_id IN ?", scope)
	}
	if err := q.Find(&rows).Error; err != nil {
		log.Printf("[催缴] 对账读取失败: %v", err)
		return
	}
	for _, row := range rows {
		var t model.Tenant
		if err := db.DB.First(&t, row.TenantID).Error; err != nil {
			continue
		}
		// 仍算欠费的两种形态：① 过期未缴（本序列的正常工作对象）；
		// ② 因本序列停用而 suspended——这是序列的终态，绝不能下一轮就把封禁撤销掉
		stillOwed := (t.Status == "expired" && t.ExpiredAt != nil && t.ExpiredAt.Before(now)) ||
			(t.Status == "suspended" && row.SuspendedAt != nil)
		if stillOwed {
			continue
		}
		if row.SuspendedAt != nil {
			if t.Status == "suspended" && !manualSuspendAfter(t.ID, *row.SuspendedAt) {
				db.DB.Model(&model.Tenant{}).Where("id = ? AND status = 'suspended'", t.ID).Update("status", "expired")
				log.Printf("[催缴] 租户%d 序列对账收口，催缴停用已回退为 expired（可登录、写侧仍拦）", t.ID)
			}
			db.DB.Model(&model.BillingDunning{}).Where("id = ? AND tenant_id = ?", row.ID, row.TenantID).Update("suspended_at", nil)
		}
		if err := db.DB.Model(&model.BillingDunning{}).Where("id = ? AND tenant_id = ?", row.ID, row.TenantID).
			Updates(map[string]any{"status": model.DunningStatusResolved, "next_notify_at": nil}).Error; err != nil {
			log.Printf("[催缴] 对账收口失败 tenant=%d: %v", row.TenantID, err)
		}
	}
}

// DunningRowView 超管催缴队列出参（字段名即契约）
type DunningRowView struct {
	TenantID       uint       `json:"tenant_id"`
	TenantName     string     `json:"tenant_name"`
	Code           string     `json:"code"`
	TenantStatus   string     `json:"tenant_status"`
	Status         string     `json:"status"`
	Stage          int        `json:"stage"`
	DayPast        int        `json:"day_past"`
	DueAt          *time.Time `json:"due_at"`
	NextNotifyAt   *time.Time `json:"next_notify_at"`
	LastNotifiedAt *time.Time `json:"last_notified_at"`
	Suspended      bool       `json:"suspended"`
	GraceEnd       *time.Time `json:"grace_end"`
	SentTo         string     `json:"sent_to"`
}

// ListDunningQueue 催缴队列（跨租户，平台侧运营视图）。
// onlyOpen=true 只看未结序列；limit 钳 [1,200]。
// 每家再读一次 tenant 行拿展示字段——队列规模就是"当前在催的租户数"，
// 这个量级（几十户）不值得为它写一条 join，宁可直白。
func ListDunningQueue(onlyOpen bool, limit int) []DunningRowView {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := db.DB.Model(&model.BillingDunning{}) // g12:platform 超管催缴队列：平台侧跨租户视图
	if onlyOpen {
		q = q.Where("status <> ?", model.DunningStatusResolved)
	}
	var rows []model.BillingDunning
	q.Order("stage DESC, updated_at DESC").Limit(limit).Find(&rows)

	suspendAfter := dunningSuspendAfter()
	out := make([]DunningRowView, 0, len(rows))
	for _, r := range rows {
		v := DunningRowView{
			TenantID: r.TenantID, Status: r.Status, Stage: r.Stage, DueAt: r.DueAt,
			NextNotifyAt: r.NextNotifyAt, LastNotifiedAt: r.LastNotifiedAt,
			Suspended: r.SuspendedAt != nil, SentTo: r.SentTo,
		}
		var t model.Tenant
		if db.DB.First(&t, r.TenantID).Error == nil {
			v.TenantName, v.Code, v.TenantStatus = t.Name, t.Code, t.Status
			if t.ExpiredAt != nil {
				v.DayPast = int(time.Since(*t.ExpiredAt).Hours() / 24)
				v.GraceEnd = graceEndTime(*t.ExpiredAt, suspendAfter)
			}
		}
		out = append(out, v)
	}
	return out
}

// TenantDunningView 租户侧看得见的自己的催缴进度（Admin「账单」卡片用）
type TenantDunningView struct {
	Exists      bool       `json:"exists"`
	Status      string     `json:"status"`
	Stage       int        `json:"stage"`
	TotalStages int        `json:"total_stages"`
	DayPast     int        `json:"day_past"`
	DueAt       *time.Time `json:"due_at"`
	GraceEnd    *time.Time `json:"grace_end"`
	Suspended   bool       `json:"suspended"`
}

// GetTenantDunning 读某租户催缴进度（无序列返回 Exists=false，不是错误）
func GetTenantDunning(tenantID uint) TenantDunningView {
	var row model.BillingDunning
	if err := db.DB.Where("tenant_id = ?", tenantID).First(&row).Error; err != nil {
		return TenantDunningView{}
	}
	v := TenantDunningView{Exists: true, Status: row.Status, Stage: row.Stage, DueAt: row.DueAt,
		Suspended:   row.SuspendedAt != nil,
		TotalStages: len(normalizeInts(runtimecfg.SafeCfgIntSlice("dunning_steps", defaultDunningSteps), 0, 3650))}
	var t model.Tenant
	if db.DB.First(&t, tenantID).Error == nil && t.ExpiredAt != nil {
		v.DayPast = int(time.Since(*t.ExpiredAt).Hours() / 24)
		v.GraceEnd = graceEndTime(*t.ExpiredAt, dunningSuspendAfter())
	}
	return v
}

// NudgeDunningNow 超管人工"立刻再催一次"：不推进档位（那是序列的节奏），
// 只按当前态重发一封并刷新留痕，用于队列里"这家销售说要手动跟一下"。
// operatorID/ip 由 API 层带入并落在审计行上：人工催缴是"以平台名义对客户开口"的动作，
// 出了纠纷（客户说被骚扰）必须能回答是谁按的按钮，故不接受匿名调用（operatorID=0 直接拒）。
// 返回 error 时调用方（API）按 400 处理——没有收件人或通道没配都算动作未成立。
func NudgeDunningNow(tenantID, operatorID uint, ip string) error {
	if operatorID == 0 {
		return userErr("缺少操作人身份")
	}
	var row model.BillingDunning
	if err := db.DB.Where("tenant_id = ?", tenantID).First(&row).Error; err != nil {
		return userErr("该租户没有催缴序列")
	}
	var t model.Tenant
	if err := db.DB.First(&t, tenantID).Error; err != nil {
		return userErr("租户不存在")
	}
	if t.ExpiredAt == nil {
		return userErr("该租户没有到期时间，无需催缴")
	}
	now := time.Now()
	dayPast := int(now.Sub(*t.ExpiredAt).Hours() / 24)
	suspendAfter := dunningSuspendAfter()
	stage := row.Stage
	if stage < 1 {
		stage = 1 // 人工推一档时序列还没发过第一档：按第 1 档口径说，不编造进度
	}
	ok := d3.DunningEmail(notify.DunningEmailInput{
		TenantID: t.ID, Name: t.Name, Code: t.Code, Stage: stage, DayPast: dayPast,
		DueAt: *t.ExpiredAt, GraceEnd: graceEndTime(*t.ExpiredAt, suspendAfter),
		WillSuspend: suspendAfter > 0 && dayPast < suspendAfter,
		Suspended:   t.Status == "suspended",
	})
	if !ok {
		return userErr("无可用收件人或邮件通道未配置")
	}
	metrics.IncDunningSent()
	if err := db.DB.Model(&model.BillingDunning{}).Where("id = ? AND tenant_id = ?", row.ID, row.TenantID).
		Updates(map[string]any{"last_notified_at": now, "sent_to": maskEmailList(d3.AdminEmails(tenantID))}).Error; err != nil {
		return err
	}
	db.DB.Create(&model.TenantAuditLog{
		TenantID: tenantID, UserID: operatorID, Action: "dunning_manual_nudge",
		Resource: fmt.Sprintf("tenant:%d", tenantID),
		Detail:   fmt.Sprintf(`{"day_past":%d,"stage":%d}`, dayPast, stage), IP: ip,
	})
	return nil
}

// ResetDunning 超管人工清序列（线下已付款、或已谈定缓收的情况）：只停催缴，**不动 status**。
// 为什么不顺手解封：解封是有资金含义的动作，必须走"到账"那条唯一路径（DunningOnPaid），
// 否则后台一个按钮就能把欠费户放回货架，与 M1 的资金红线同构。
// 留痕写在解除之后：审计行是"谁在什么时候决定不再催这家"的唯一凭据，序列行本身会被下一轮重开覆盖。
func ResetDunning(tenantID, operatorID uint, ip string) error {
	if operatorID == 0 {
		return userErr("缺少操作人身份")
	}
	var row model.BillingDunning
	if err := db.DB.Where("tenant_id = ? AND status <> ?", tenantID, model.DunningStatusResolved).
		First(&row).Error; err != nil {
		return userErr("该租户没有待处理的催缴序列")
	}
	now := time.Now()
	detail, _ := json.Marshal(map[string]any{"resolved_reason": "manual_reset", "resolved_at": now.Format(time.RFC3339)})
	if err := db.DB.Model(&model.BillingDunning{}).Where("id = ? AND tenant_id = ?", row.ID, row.TenantID).
		Updates(map[string]any{"status": model.DunningStatusResolved, "next_notify_at": nil, "detail": string(detail)}).Error; err != nil {
		return err
	}
	db.DB.Create(&model.TenantAuditLog{
		TenantID: tenantID, UserID: operatorID, Action: "dunning_manual_reset",
		Resource: fmt.Sprintf("tenant:%d", tenantID),
		Detail:   fmt.Sprintf(`{"stage":%d,"suspended":%t}`, row.Stage, row.SuspendedAt != nil), IP: ip,
	})
	return nil
}

// maskEmailList 收件人留痕一律脱敏：表里存明文邮箱等于把 PII 复制进又一张**超管全局可见**的表，
// 而排障只需要知道"发到过哪几个地址"。最多留 3 个（列长 200）。
func maskEmailList(emails []string) string {
	out := make([]string, 0, len(emails))
	for _, e := range emails {
		out = append(out, notify.MaskEmailForLog(e))
		if len(out) == 3 {
			break
		}
	}
	return strings.Join(out, ",")
}
