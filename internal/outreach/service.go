package outreach

import (
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"ai-scrm/internal/contentsafety"
	"ai-scrm/internal/model"
	"ai-scrm/internal/runtimecfg"
)

// isolate 把调用方句柄换成"每条查询各自独立、但仍继承租户条件"的会话句柄。
//
// 为什么必须有：api 层传进来的是 db.RQ(c)——它内部已经执行过 Where("tenant_id = ?")，
// 返回句柄的 clone=0，在它上面继续链式 .Model/.Where 会**就地累加进同一个 Statement**。
// Create 里"先查客户存在性、再查周内频次"两条查询连着写，第二条便带上第一条的
// (id = ? AND tenant_id = ?) 和 Model=Customer，SQL 退化成
//
//	SELECT count(*) FROM "customers" WHERE ... AND customer_id = 3462 AND status='sent'
//
// → 42703 column "customer_id" does not exist，接口 500（smoke §三十二 首跑实锤）。
// Session(&gorm.Session{}) 走 gorm 的 clone=2 分支：每次链式调用先克隆 Statement，
// 租户 where 照常继承，条件之间不再互相污染。
//
// 单测看不见这个坑——它们传的是干净的 db.DB（clone=1，天然每次新建 Statement），
// 只有挂了 RQ 的真实请求链路才炸，故回归护栏见 service_test.go 的派生句柄用例。
func isolate(gdb *gorm.DB) *gorm.DB {
	if gdb == nil {
		return nil
	}
	return gdb.Session(&gorm.Session{})
}

// CheckFunc 内容安全校验钩子（默认挂 C1 词库）。做成变量是为了单测可注入判定，
// 口径同 talkmining.GenerateDraftFunc / strategy.EvalLLMFunc——外部能力一律可替换，
// CI 不依赖词库已加载。
var CheckFunc = contentsafety.Check

// Params 本租户本轮生效的触达参数（租户覆盖 > 系统默认，读取层与批六口径一致）。
type Params struct {
	Enabled     bool   // 总开关：关=不能排期也不能派发
	WeeklyLimit int    // 单客户滚动 7 天内最多几条（0=不限，负数按 0 处理）
	QuietHours  string // 静默时段 "HH:MM-HH:MM"，支持跨午夜；空或非法=不顺延
	WindowHours int    // 微信侧主动发送窗口（小时），默认 48
}

// cfgBoolForTenant / cfgIntForTenant / cfgStringForTenant 三件套：
// 服务未初始化（单测/极早期启动）时退回入参默认值，绝不在 nil 单例上取配置
// （批一 G2 的教训：runtimecfg 读方法曾在 nil 单例上确定性 panic）。
func cfgBoolForTenant(tid uint, key string, def bool) bool {
	svc := runtimecfg.DefaultSystemConfigService
	if svc == nil {
		return def
	}
	return svc.GetBoolForTenant(tid, key, def)
}

func cfgIntForTenant(tid uint, key string, def int) int {
	svc := runtimecfg.DefaultSystemConfigService
	if svc == nil {
		return def
	}
	return svc.GetIntForTenant(tid, key, def)
}

func cfgStringForTenant(tid uint, key, def string) string {
	svc := runtimecfg.DefaultSystemConfigService
	if svc == nil {
		return def
	}
	return svc.GetStringForTenant(tid, key, def)
}

// LoadParams 读取租户生效参数。默认值即出厂口径：关闭、每周 2 条、21:00-09:00 静默、48h 窗口。
// 出厂默认关是本批的放量纪律（与 sales_path/talkmining 四开关同一惯例）。
func LoadParams(tenantID uint) Params {
	return Params{
		Enabled:     cfgBoolForTenant(tenantID, "outreach_enabled", false),
		WeeklyLimit: cfgIntForTenant(tenantID, "outreach_weekly_limit", 2),
		QuietHours:  cfgStringForTenant(tenantID, "outreach_quiet_hours", "21:00-09:00"),
		WindowHours: cfgIntForTenant(tenantID, "outreach_window_hours", 48),
	}
}

// RejectError 排期被拒的结构化错误：Reason 是稳定字面量（model.OutreachReason*），
// api 层按它出 400 + 原因码，前端按它出文案——不做错误字符串匹配，改文案不破坏契约。
type RejectError struct {
	Reason string
	Msg    string
}

// Error 实现 error 接口
func (e *RejectError) Error() string { return "outreach: " + e.Msg }

// RejectReason 从错误里取原因码（非 RejectError 返回空串）
func RejectReason(err error) string {
	var re *RejectError
	if errors.As(err, &re) {
		return re.Reason
	}
	return ""
}

// CreateInput 排期入参（由 api 层从请求体绑定后传入；ScheduledAt 零值=立即发送，
// 实际发送仍等下一轮调度扫描）。
type CreateInput struct {
	TenantID    uint
	CustomerID  uint
	Content     string
	ScheduledAt time.Time
	CreatedBy   uint
	Now         time.Time // 便于单测注入时间；零值取 time.Now()
}

// Create 校验并落一条触达任务（状态 pending，等调度裁决）。
//
// 校验顺序按"便宜在前"：开关 → 文案 → 客户归属 → 周内频次 → 静默顺延 → 落库。
// gdb 必须是调用方带的**租户作用域句柄**（api 层传 db.RQ(c)）：本函数内部仍显式
// 按 tenant_id 过滤并落 TenantID，双保险（C7 红线：事务/后台写租户表不得依赖隐式盖章）。
func Create(gdb *gorm.DB, in CreateInput) (*model.OutreachTask, error) {
	gdb = isolate(gdb) // 见 isolate：派生句柄不复位会让多条查询条件互相累加
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	p := LoadParams(in.TenantID)
	if !p.Enabled {
		return nil, &RejectError{Reason: model.OutreachReasonDisabled, Msg: "主动触达未启用，请先在系统配置开启 outreach_enabled"}
	}
	cleaned, reason := ValidateContent(in.Content)
	if reason != "" {
		return nil, &RejectError{Reason: reason, Msg: "触达文案为空、超长或未通过内容安全校验"}
	}
	if in.CustomerID == 0 {
		return nil, &RejectError{Reason: model.OutreachReasonNoChannel, Msg: "缺少 customer_id"}
	}
	var cust model.Customer
	if err := gdb.Where("id = ? AND tenant_id = ?", in.CustomerID, in.TenantID).First(&cust).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, &RejectError{Reason: model.OutreachReasonNoChannel, Msg: "客户不存在或不属于本租户"}
		}
		return nil, err
	}
	if p.WeeklyLimit >= 0 {
		used, err := countSentInWindow(gdb, in.TenantID, in.CustomerID, now.Add(-7*24*time.Hour))
		if err != nil {
			return nil, err
		}
		if p.WeeklyLimit > 0 && used >= int64(p.WeeklyLimit) {
			return nil, &RejectError{
				Reason: model.OutreachReasonOverWeeklyLimit,
				Msg:    fmt.Sprintf("该客户近 7 天已触达 %d 条，达上限 %d", used, p.WeeklyLimit),
			}
		}
	}
	sched := in.ScheduledAt
	if sched.IsZero() {
		sched = now
	}
	task := &model.OutreachTask{
		TenantID:    in.TenantID,
		CustomerID:  in.CustomerID,
		Content:     cleaned,
		ScheduledAt: DeferPastQuiet(sched, p.QuietHours, time.Local),
		Status:      model.OutreachStatusPending,
		CreatedBy:   in.CreatedBy,
	}
	if err := gdb.Create(task).Error; err != nil {
		return nil, err
	}
	return task, nil
}

// countSentInWindow 统计该客户在 cutoff 之后成功发出的条数（频控口径只看 sent，
// queued 不算——queued 未确认送达，把它计入会让一次通道抖动永久吃掉客户额度）。
func countSentInWindow(gdb *gorm.DB, tenantID, customerID uint, cutoff time.Time) (int64, error) {
	var n int64
	err := gdb.Model(&model.OutreachTask{}).
		Where("tenant_id = ? AND customer_id = ? AND status = ? AND sent_at >= ?",
			tenantID, customerID, model.OutreachStatusSent, cutoff).
		Count(&n).Error
	return n, err
}

// Target 派发期解析出的可达目标。
type Target struct {
	ChannelID   uint
	ChannelType string
	ExternalID  string
	LastInbound time.Time // 客户最近一次来句时间（零=从未来过）
}

// ResolveTarget 解析"这个客户现在能从哪条通道收到消息"。
//
// 取该客户最近一条 channel_identities（客户可能同时有企微与公众号身份，最近活跃者优先），
// 再核对该通道属于本租户且为启用态。通道层出站时也会拒停用通道（sendOne 判 Fatal 直接进死信），
// 这里在**入队前**先拦，是为了让任务留下可解释的 reason，而不是只在该客户的出站死信里多一行。
func ResolveTarget(gdb *gorm.DB, tenantID, customerID uint) (*Target, error) {
	gdb = isolate(gdb) // 三次查询（身份/通道/最近来句）必须各自独立，见 isolate
	var ident model.ChannelIdentity
	err := gdb.Where("tenant_id = ? AND customer_id = ?", tenantID, customerID).
		Order("updated_at DESC, id DESC").First(&ident).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil // 没有任何渠道身份：不可达，非错误
	}
	if err != nil {
		return nil, err
	}
	var ch model.Channel
	if err := gdb.Where("id = ? AND tenant_id = ?", ident.ChannelID, tenantID).First(&ch).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if ch.Status != model.ChannelStatusActive {
		return &Target{ChannelID: ch.ID, ChannelType: ch.Type, ExternalID: ident.ExternalID, LastInbound: time.Time{}},
			fmt.Errorf("%w: channel=%d status=%s", ErrChannelInactive, ch.ID, ch.Status)
	}
	last, err := lastInboundAt(gdb, tenantID, customerID)
	if err != nil {
		return nil, err
	}
	return &Target{ChannelID: ch.ID, ChannelType: ch.Type, ExternalID: ident.ExternalID, LastInbound: last}, nil
}

// ErrChannelInactive 命中通道已停用/未验证（调用方用 errors.Is 判别，不匹配字符串）。
var ErrChannelInactive = errors.New("通道非启用态")

// lastInboundAt 客户最近一次来句时间（sender_type='customer'）。
// 为什么查 messages 而不是 channel_inbound_msgs：后者是入站幂等台账（无 customer_id），
// 而 messages 有租户与发送方列且已建 customer_id 索引，口径也与"客户说过话"完全一致。
func lastInboundAt(gdb *gorm.DB, tenantID, customerID uint) (time.Time, error) {
	var rows []time.Time
	err := gdb.Model(&model.Message{}).
		Where("tenant_id = ? AND customer_id = ? AND sender_type = ?", tenantID, customerID, "customer").
		Order("created_at DESC").Limit(1).Pluck("created_at", &rows).Error
	if err != nil {
		return time.Time{}, err
	}
	if len(rows) == 0 {
		return time.Time{}, nil
	}
	return rows[0], nil
}

// List 按状态分页取任务（status 空=全部）。返回 (行, 总数)：总数用于前端分页与队列徽标。
func List(gdb *gorm.DB, tenantID uint, status string, limit, offset int) ([]model.OutreachTask, int64, error) {
	// Count 与 Find 共用同一个 q：不 isolate 的话 Count 之后条件继续累加，
	// 列表页第二次查询就会带上 Count 的 Model 痕迹（同 Create 的 42703 家族）。
	gdb = isolate(gdb)
	q := gdb.Model(&model.OutreachTask{}).Where("tenant_id = ?", tenantID)
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var rows []model.OutreachTask
	err := q.Order("scheduled_at DESC, id DESC").Limit(limit).Offset(offset).Find(&rows).Error
	return rows, total, err
}

// Cancel 人工撤回：只有 pending 可撤。
// queued 之后消息已经进通道出站队列（可能已投出），"撤回"会给用户一个做不到的承诺——
// 因此宁可让运营看到 queued 不可撤，也不做假撤销。返回影响行数（0=状态不符）。
func Cancel(gdb *gorm.DB, tenantID, id uint) (int64, error) {
	res := isolate(gdb).Model(&model.OutreachTask{}).
		Where("id = ? AND tenant_id = ? AND status = ?", id, tenantID, model.OutreachStatusPending).
		Updates(map[string]any{
			"status":     model.OutreachStatusCancelled,
			"updated_at": time.Now(),
		})
	return res.RowsAffected, res.Error
}
