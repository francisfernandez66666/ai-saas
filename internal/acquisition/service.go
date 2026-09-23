// 获客活码的读写层：建码与启停、公开解析、扫码事件、C 端建客时的归因写入、按码取漏斗与客户名单。
//
// 句柄约定（触达批实锤过的红线，这里逐条遵守）：调用方传进来的是 db.RQ(c)/db.PQ(c)，
// 它的 clone=0、条件**就地累加**。本文件每个函数都要跑不止一条查询，
// 所以一律先过 isolate() 换成"继承租户条件但各查询独立"的会话句柄；
// 反向用例见 service_test.go（不隔离时必须真的报错，否则护栏是空转的）。
//
// 写入约定（C7 事务内写租户表红线）：所有写租户表的入口都显式带 tenantID 落列，
// 不依赖请求 context 里的租户自动盖章——后台链路（公开扫码端点）本就没有 gin ctx。
package acquisition

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"ai-scrm/internal/model"

	"gorm.io/gorm"
)

// isolate 见文件头：把派生句柄换成"各查询独立但继承租户条件"的会话句柄。
func isolate(gdb *gorm.DB) *gorm.DB {
	if gdb == nil {
		return nil
	}
	return gdb.Session(&gorm.Session{})
}

// ErrNotFound 码不存在或不在本租户作用域内（对外一律 404，不回显"别家有没有这个码"）
var ErrNotFound = errors.New("acquisition: code not found")

// DefaultWindowDays 漏斗与名单的默认统计窗口
const DefaultWindowDays = 30

// MaxWindowDays 窗口硬上限（一年；再长就该走导出而不是线上聚合）
const MaxWindowDays = 365

// MaxDrillPageSize 下钻名单单页硬顶（口径同 D4：下钻用来"核对这一格是谁"，
// 导数走 /admin/export，不该让一个 query 参数决定响应体大小）。
const MaxDrillPageSize = 100

// ClampDays 把外部传入的 days 钳进 [1, MaxWindowDays]，非法/缺省回默认值。
func ClampDays(days int) int {
	if days <= 0 {
		return DefaultWindowDays
	}
	if days > MaxWindowDays {
		return MaxWindowDays
	}
	return days
}

// CreateInput 建码入参
type CreateInput struct {
	TenantID  uint
	OwnerUser uint
	Name      string
	Channel   string
	Remark    string
}

// ErrReject 入参拒绝（Reason 是稳定原因码，前端与冒烟按它分支，不解析中文）
type ErrReject struct {
	Reason string
	Msg    string
}

// Error 实现 error 接口：拒绝原因带稳定原因码前缀，供上层按码分支而不是猜文案。
func (e *ErrReject) Error() string { return "acquisition: " + e.Msg }

// RejectReason 从 error 里取稳定原因码（非 ErrReject 返回 ""）
func RejectReason(err error) string {
	var re *ErrReject
	if errors.As(err, &re) {
		return re.Reason
	}
	return ""
}

// CreateCode 建一个活码（码字符串由密码学随机生成，撞唯一索引则重试，防极小概率双发）。
//
// 重试上限存在的理由：唯一索引撞车时如果直接报错，用户看到的是"建码失败"却不知道发生了什么；
// 8 位 × 31 字符集撞键概率极低，但"极低"不是"零"，而重试的代价是几微秒。
func CreateCode(gdb *gorm.DB, in CreateInput) (*model.AcquisitionCode, error) {
	if in.TenantID == 0 {
		return nil, &ErrReject{Reason: "tenant_required", Msg: "无租户语境"}
	}
	if r := ValidateCreate(in.Name, in.Channel); r != "" {
		return nil, &ErrReject{Reason: r, Msg: "入参不合法：" + r}
	}
	if remark := strings.TrimSpace(in.Remark); len([]rune(remark)) > 100 {
		return nil, &ErrReject{Reason: "remark_too_long", Msg: "备注过长"}
	}
	g := isolate(gdb)
	var dup int64
	// 同名同渠道大概率是手抖连点两次，不是真要两个一样的码——直接拒，
	// 少一条"三个同名码分不出谁是谁"的运营噪音。
	g.Model(&model.AcquisitionCode{}).Where("tenant_id = ? AND name = ? AND channel = ? AND status = ?",
		in.TenantID, strings.TrimSpace(in.Name), in.Channel, model.AcquisitionStatusActive).Count(&dup)
	if dup > 0 {
		return nil, &ErrReject{Reason: "duplicate_code_name", Msg: "同渠道下已存在同名启用码"}
	}
	for attempt := 0; attempt < 5; attempt++ {
		code, err := NewCode()
		if err != nil {
			return nil, err
		}
		row := model.AcquisitionCode{
			TenantID:    in.TenantID,
			Code:        code,
			Name:        strings.TrimSpace(in.Name),
			Channel:     in.Channel,
			OwnerUserID: in.OwnerUser,
			Status:      model.AcquisitionStatusActive,
			Remark:      strings.TrimSpace(in.Remark),
		}
		// 显式设 TenantID 且走 Session 句柄：D6 盖章门禁要求字面量赋值可见，
		// C7 红线要求后台链路（无请求 ctx）也必须把租户落列。
		if err := isolate(gdb).Create(&row).Error; err != nil {
			if strings.Contains(err.Error(), "23505") || strings.Contains(err.Error(), "duplicate key") {
				continue
			}
			return nil, err
		}
		return &row, nil
	}
	return nil, errors.New("acquisition: 短码生成连续撞唯一索引，请稍后重试")
}

// SetCodeStatus 启用/停用（**没有删除这条路径**，理由见 model/acquisition.go 注释块）
// 返回受影响行数：0 表示不存在或跨租户（调用方一律 404，不回显差别）。
func SetCodeStatus(gdb *gorm.DB, tenantID, id uint, active bool) (int64, error) {
	status := model.AcquisitionStatusDisabled
	if active {
		status = model.AcquisitionStatusActive
	}
	res := isolate(gdb).Model(&model.AcquisitionCode{}).
		Where("tenant_id = ? AND id = ?", tenantID, id).
		Update("status", status)
	return res.RowsAffected, res.Error
}

// CodeWithStats 一个码 + 它的漏斗数字（Admin 列表与下钻同源）
type CodeWithStats struct {
	Code   model.AcquisitionCode `json:"code"`
	Scans  int64                 `json:"scans"` // 扫码次数（事件级，不可下钻）
	Funnel map[string]int64      `json:"funnel"`
}

// ListWithStats 本租户的码列表（含每个码的漏斗数字）。
//
// 窗口口径**必须说清且不藏着**：这里的时间窗打在"客户创建时间"上（这段时间扫进来的人，
// 今天走到哪一步了），而 D4 贡献度看板的时间窗打在"消息时间"上。
// 两者问的是不同问题（"这批人后来怎么样" vs "这段时间谁在跟我说话"），
// 强行统一反而两边都答不准，故把差异随响应下发（见 FunnelNote）。
func ListWithStats(gdb *gorm.DB, tenantID uint, status string, days int) ([]CodeWithStats, error) {
	days = ClampDays(days)
	g := isolate(gdb)
	var codes []model.AcquisitionCode
	q := g.Where("tenant_id = ?", tenantID)
	if status == "active" || status == "disabled" {
		q = q.Where("status = ?", status)
	}
	if err := q.Order("id DESC").Limit(MaxCodesPerTenant).Find(&codes).Error; err != nil {
		return nil, err
	}
	out := make([]CodeWithStats, 0, len(codes))
	if len(codes) == 0 {
		return out, nil
	}
	ids := make([]uint, 0, len(codes))
	for _, c := range codes {
		ids = append(ids, c.ID)
	}
	scanCounts, err := scanCountsByCode(isolate(gdb), tenantID, ids, days)
	if err != nil {
		return nil, err
	}
	for _, c := range codes {
		cs, err := funnelCustomers(isolate(gdb), tenantID, c.Code, days)
		if err != nil {
			return nil, err
		}
		out = append(out, CodeWithStats{Code: c, Scans: scanCounts[c.ID], Funnel: ClassifyFunnelCounts(cs)})
	}
	return out, nil
}

// MaxCodesPerTenant 一次列表最多带多少个码（码是低频配置，50 个还排不满就说明该清理了）
const MaxCodesPerTenant = 50

// scanCountsByCode 每个码在窗口内的扫码事件数（一次分组查询，不做 N+1）
func scanCountsByCode(gdb *gorm.DB, tenantID uint, codeIDs []uint, days int) (map[uint]int64, error) {
	type row struct {
		CodeID uint
		N      int64
	}
	var rows []row
	err := gdb.Model(&model.AcquisitionScan{}).
		Select("code_id, count(*) AS n").
		Where("tenant_id = ? AND code_id IN ? AND created_at >= ?", tenantID, codeIDs, since(days)).
		Group("code_id").Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := map[uint]int64{}
	for _, r := range rows {
		out[r.CodeID] = r.N
	}
	return out, nil
}

func since(days int) time.Time {
	return time.Now().Add(-time.Duration(days) * 24 * time.Hour)
}

// funnelCustomers 取本码在窗口内带来的客户及其漏斗判定输入。
//
// 这是漏斗计数与下钻名单**共用**的唯一取数口：两侧都从这里拿同一份人，
// 所以"卡片 8 个、点进去 8 行"是结构上的必然，不是两边各写对了一次。
// spoke 用 EXISTS 子查询一次取回，不在 Go 里逐客户查消息（那才是真的 N+1）。
func funnelCustomers(gdb *gorm.DB, tenantID uint, code string, days int) ([]FunnelCustomer, error) {
	type row struct {
		ID           uint
		JourneyStage string
		Spoke        bool
	}
	var rows []row
	err := gdb.Table("customers AS c").
		Select("c.id, c.journey_stage, EXISTS (SELECT 1 FROM messages m WHERE m.tenant_id = c.tenant_id AND m.customer_id = c.id AND m.sender_type = 'customer') AS spoke").
		Where("c.tenant_id = ? AND c.acquisition_code = ? AND c.created_at >= ?", tenantID, code, since(days)).
		Order("c.id DESC").Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]FunnelCustomer, 0, len(rows))
	for _, r := range rows {
		out = append(out, FunnelCustomer{CustomerID: r.ID, Spoke: r.Spoke, Stage: r.JourneyStage})
	}
	return out, nil
}

// FunnelCustomerRow 下钻名单的一行（够前端渲染即可，字段与 /customers 对齐）
type FunnelCustomerRow struct {
	ID          uint    `json:"id"`
	Name        string  `json:"name"`
	Phone       string  `json:"phone"`
	Stage       string  `json:"journey_stage"`
	IntentScore float64 `json:"intent_score"`
	Spoke       bool    `json:"spoke"`
	CreatedAt   string  `json:"created_at"`
}

// DrillResult 下钻结果：total 恒为"命中人数"，与漏斗数字同源；list 是当前页。
type DrillResult struct {
	Metric string              `json:"metric"`
	Label  string              `json:"label"`
	Total  int64               `json:"total"`
	List   []FunnelCustomerRow `json:"list"`
	Note   string              `json:"note"`
}

// FunnelNote 口径说明（随响应下发，前端与冒烟都读它，不各自写第二套话）
const FunnelNote = "时间窗打在客户创建时间上：统计的是这段时间里扫该码进来的客户当前的漏斗位置；扫码次数是事件级计数，单位与客户名单不同，故不可下钻。"

// DrillCustomers 按指标取本码命中的人（metric 不在客户级白名单内返回 ErrBadMetric）。
//
// 分页语义沿用 D4：total 不随分页变；越界页只回空列表但 total 如实；
// 客户行缺失时**不编造行也不改 total**（"命中数"与"此刻还能看到的明细"本就可不等）。
func DrillCustomers(gdb *gorm.DB, tenantID, codeID uint, metric string, days, page, pageSize int) (*DrillResult, error) {
	if !IsDrillableMetric(metric) {
		return nil, ErrBadMetric
	}
	days = ClampDays(days)
	g := isolate(gdb)
	var c model.AcquisitionCode
	if err := g.Where("tenant_id = ? AND id = ?", tenantID, codeID).First(&c).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	cs, err := funnelCustomers(g, tenantID, c.Code, days)
	if err != nil {
		return nil, err
	}
	hits := make([]FunnelCustomer, 0, len(cs))
	for _, x := range cs {
		if MatchFunnelMetric(metric, x) {
			hits = append(hits, x)
		}
	}
	res := &DrillResult{Metric: metric, Label: FunnelMetricLabels[metric], Total: int64(len(hits)), List: []FunnelCustomerRow{}, Note: FunnelNote}
	if len(hits) == 0 {
		return res, nil
	}
	// 命中集已按 id 倒序（funnelCustomers 里 Order id DESC），分页只需切窗口
	start := (page - 1) * pageSize
	if start >= len(hits) {
		return res, nil // 越界页：list 空、total 如实
	}
	end := start + pageSize
	if end > len(hits) {
		end = len(hits)
	}
	ids := make([]uint, 0, end-start)
	for _, h := range hits[start:end] {
		ids = append(ids, h.CustomerID)
	}
	var rows []model.Customer
	if err := g.Model(&model.Customer{}).
		Where("tenant_id = ? AND id IN ?", tenantID, ids).
		Order("id DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	byID := map[uint]FunnelCustomer{}
	for _, h := range hits {
		byID[h.CustomerID] = h
	}
	for _, r := range rows {
		h := byID[r.ID]
		res.List = append(res.List, FunnelCustomerRow{
			ID: r.ID, Name: r.Name, Phone: r.Phone, Stage: r.JourneyStage,
			IntentScore: r.IntentScore, Spoke: h.Spoke,
			CreatedAt: r.CreatedAt.Format("2006-01-02 15:04:05"),
		})
	}
	return res, nil
}

// ErrBadMetric 指标不可下钻（单位不是"客户"的一律拒，不默认回某一份名单）
var ErrBadMetric = errors.New("acquisition: metric not drillable")

// IsDrillableMetric 该指标是否可下钻（scans 刻意不在内，见 FunnelMetricCodes 注释）
func IsDrillableMetric(metric string) bool {
	for _, m := range FunnelMetricCodes {
		if m == metric {
			return true
		}
	}
	return false
}

// ============================================================
// 公开侧：解析与扫码事件
// ============================================================

// Resolve 按码查启用中的码（**全局单键查询，不带租户条件**——公开链路拿不到租户）。
// 返回 ErrNotFound 表示"码不存在或形态非法"，调用方据此回 404；
// 停用中的码同样按不存在处理（对外形态一致，不给试探面），历史统计在管理端仍可读。
func Resolve(gdb *gorm.DB, rawCode string) (*model.AcquisitionCode, error) {
	code := NormalizeCode(rawCode)
	if code == "" {
		return nil, ErrNotFound
	}
	var c model.AcquisitionCode
	// 这里必须是平台句柄（调用方传 db.DB）：租户条件在此刻还没建立。
	if err := isolate(gdb).Where("code = ?", code).First(&c).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if c.Status != model.AcquisitionStatusActive {
		return nil, ErrNotFound
	}
	return &c, nil
}

// ScanInput 扫码事件入参
type ScanInput struct {
	TenantID   uint
	CodeID     uint
	Code       string
	CustomerID uint
	VisitorKey string
	// Now 注入点：单测要能把"上一次扫码"摆在窗口内/窗口外两侧（不注入就只能睡 10 分钟）
	Now time.Time
}

// RecordScan 记一次扫码事件（同码同访客窗口内去重）。
//
// 返回 counted=false 表示落在去重窗口里、没新写行——这不是错误：
// 落地页重复打开是常态，把它当错误回 4xx 会让前端在用户脸上弹失败提示。
func RecordScan(gdb *gorm.DB, in ScanInput) (counted bool, err error) {
	if in.TenantID == 0 || in.CodeID == 0 {
		return false, errors.New("acquisition: 扫码事件缺少租户或码")
	}
	g := isolate(gdb)
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	vk := strings.TrimSpace(in.VisitorKey)
	if vk != "" {
		var last time.Time
		// 只取"该码该访客"的最近一条：走 migrations/022 的 (code_id, visitor_key, created_at DESC)
		// 部分索引，一次定位即判，不把历史事件全捞进内存。
		// 目的参数必须是切片——gorm 的 Pluck 对非标量切片目标会直接报
		// "pluck dest must be a slice"，单值接收看着更顺眼但跑不起来（首跑实踩）。
		var seen []time.Time
		if err := g.Model(&model.AcquisitionScan{}).
			Where("tenant_id = ? AND code_id = ? AND visitor_key = ?", in.TenantID, in.CodeID, vk).
			Order("created_at DESC").Limit(1).
			Pluck("created_at", &seen).Error; err != nil {
			return false, err
		}
		if len(seen) > 0 {
			last = seen[0]
		}
		if !ShouldCountScan(vk, last, now) {
			return false, nil
		}
	}
	row := model.AcquisitionScan{
		TenantID:   in.TenantID,
		CodeID:     in.CodeID,
		CustomerID: in.CustomerID,
		VisitorKey: vk,
		CreatedAt:  now,
	}
	if err := g.Create(&row).Error; err != nil {
		return false, err
	}
	return true, nil
}

// ============================================================
// C 端建客时的归因写入
// ============================================================

// AttributionResult 归因结果（Applied=false 时 Reason 说明为什么没写上，绝不静默）
type AttributionResult struct {
	Applied bool
	Reason  string
	Channel string // 命中后应写进 Customer.Source 的渠道位（未命中为空）
	Code    string // 规范化后的码
	Scans   int64  // 本次是否新记了扫码事件（1/0）
}

// ApplyToGuest 给刚建好（或即将建好）的匿名访客打码。
//
// 三条硬约束，缺一条这个功能就会变成事故源：
//  1. **码所属租户必须等于当前请求租户**，不等一律不写（DecideApply 里那条判断）。
//     没有独立域名的租户用默认域名分发活码时，这条把"记到别人家"变成"没记上"——
//     丢归因可查、串家不可查。
//  2. **首触改写禁止**：客户身上已有码就不再改（见 model.Customer.AcquisitionCode 注释）。
//  3. **归因绝不能打断聊天**：本函数任何失败都只回 Reason，不返回 error 让调用方放弃建客；
//     客户进不来比归因没记上严重得多。DB 故障除外（那本来也没法继续）。
func ApplyToGuest(gdb *gorm.DB, tenantID, customerID uint, rawCode string, visitorKey string) (AttributionResult, error) {
	code := NormalizeCode(rawCode)
	if code == "" {
		r := DecideApply(rawCode, false, false, 0, tenantID, false)
		return AttributionResult{Reason: r.Reason}, nil
	}
	var c model.AcquisitionCode
	found := false
	if err := isolate(gdb).Where("code = ?", code).First(&c).Error; err == nil {
		found = true
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return AttributionResult{}, err
	}
	already := false
	if found && customerID > 0 {
		// 只看这一列，不整行拉客户（归因是热路径上的旁路，别顺手多读一遍宽表）
		var cur []string
		isolate(gdb).Model(&model.Customer{}).Where("id = ?", customerID).Pluck("acquisition_code", &cur)
		already = len(cur) > 0 && strings.TrimSpace(cur[0]) != ""
	}
	dec := DecideApply(rawCode, found, c.Status == model.AcquisitionStatusActive, c.TenantID, tenantID, already)
	res := AttributionResult{Reason: dec.Reason, Code: code}
	if !dec.Apply {
		return res, nil
	}
	// 只写归因列，**不整行 Save**（复核批起反复踩的 stale 覆写坑：客户可能在这之间被接管/分配）
	upd := isolate(gdb).Model(&model.Customer{}).
		Where("id = ? AND tenant_id = ? AND acquisition_code = ''", customerID, tenantID).
		Updates(map[string]any{
			"acquisition_code": code,
			// 渠道位同步进"来源"列：既有客户列表、贡献度、T 向量第 7 维都读它，
			// 不写就会出现"名单里这个人属于活码、来源列却还写着外部体验"的自相矛盾。
			"source":     c.Channel,
			"updated_at": time.Now(),
		})
	if upd.Error != nil {
		return res, upd.Error
	}
	if upd.RowsAffected == 0 {
		// 并发双码：两个请求同时给同一个人打码，后到的这一个条件不成立。
		// 首触语义下这是正常结果，报 already_attributed 而不是编一个"成功"。
		res.Reason = ApplyReasonAlreadySet
		return res, nil
	}
	res.Applied = true
	res.Channel = c.Channel
	counted, err := RecordScan(isolate(gdb), ScanInput{TenantID: tenantID, CodeID: c.ID, Code: code, CustomerID: customerID, VisitorKey: visitorKey})
	if err != nil {
		// 归因已写成，扫码事件写失败不再回滚：漏斗少一格可补，客户身份丢了补不回来。
		res.Reason = fmt.Sprintf("%s;scan_log_failed", ApplyOK)
		return res, nil
	}
	if counted {
		res.Scans = 1
	}
	return res, nil
}
