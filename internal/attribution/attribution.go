// Package attribution 行业包质量归因底座（D9，2026-09-13）。
// 记录 AI 回复生效包/模板快照，并在接钩、留资、转人工等事件上回填；不依赖业务 service，避免新增领域代码进 internal/service。
package attribution

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
)

// Snapshot 回复时生效的行业包快照。
type Snapshot struct {
	PackCode    string
	PackVersion string
}

// 事件回调由组合根（main.go）注入指标/告警等旁路观察者；归因包保持不直接依赖 service，避免领域耦合。
var (
	OnReplyRecorded func(packCode, templateID string)
	OnLeadCaptured  func(packCode string)
)

// SnapshotFromBinding 优先取企业包；无企业包时回退行业包。
func SnapshotFromBinding(bind *model.TenantPackBinding) Snapshot {
	if bind == nil {
		return Snapshot{}
	}
	if bind.EnterprisePackID != nil && *bind.EnterprisePackID > 0 {
		return Snapshot{PackCode: bind.EnterpriseCode, PackVersion: bind.EnterpriseVersion}
	}
	return Snapshot{PackCode: bind.PackCode, PackVersion: bind.AppliedVersion}
}

// RecordReplyInput AI 回复归因写入入参。
type RecordReplyInput struct {
	TenantID       uint
	MessageID      uint
	ConversationID uint
	CustomerID     uint
	TemplateID     string
	AnchorType     int
	RouteResult    string
	Provider       string
	ModelName      string
	IntentBefore   float64
	IntentAfter    float64
}

// RecordReply 幂等写入一条回复归因；message_id 重复时只刷新快照，不重复计数。
func RecordReply(in RecordReplyInput) error {
	if in.MessageID == 0 || in.TenantID == 0 {
		return nil
	}
	snap := SnapshotForTenant(in.TenantID)
	now := time.Now()
	var old model.ReplyAttribution
	if err := db.DB.Where("message_id = ?", in.MessageID).First(&old).Error; err == nil {
		return db.DB.Model(&model.ReplyAttribution{}).Where("id = ?", old.ID).Updates(map[string]interface{}{
			"tenant_id":       in.TenantID,
			"conversation_id": in.ConversationID,
			"customer_id":     in.CustomerID,
			"pack_code":       snap.PackCode,
			"pack_version":    snap.PackVersion,
			"template_id":     in.TemplateID,
			"anchor_type":     in.AnchorType,
			"route_result":    in.RouteResult,
			"provider":        in.Provider,
			"model":           in.ModelName,
			"intent_before":   in.IntentBefore,
			"intent_after":    in.IntentAfter,
			"updated_at":      now,
		}).Error
	}
	row := model.ReplyAttribution{
		TenantID:       in.TenantID,
		MessageID:      in.MessageID,
		ConversationID: in.ConversationID,
		CustomerID:     in.CustomerID,
		PackCode:       snap.PackCode,
		PackVersion:    snap.PackVersion,
		TemplateID:     in.TemplateID,
		AnchorType:     in.AnchorType,
		RouteResult:    in.RouteResult,
		Provider:       in.Provider,
		ModelName:      in.ModelName,
		IntentBefore:   in.IntentBefore,
		IntentAfter:    in.IntentAfter,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := db.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "message_id"}},
		DoNothing: true,
	}).Create(&row).Error; err != nil {
		return err
	}
	if row.ID > 0 && in.TemplateID != "" && OnReplyRecorded != nil {
		OnReplyRecorded(snap.PackCode, in.TemplateID)
	}
	return nil
}

// SnapshotForTenant 读取租户当前生效包快照。
func SnapshotForTenant(tenantID uint) Snapshot {
	if tenantID == 0 {
		return Snapshot{}
	}
	var bind model.TenantPackBinding
	if err := db.DB.Where("tenant_id = ?", tenantID).First(&bind).Error; err != nil {
		return Snapshot{}
	}
	return SnapshotFromBinding(&bind)
}

// MarkHookedBeforeMessage 标记会话中该客户消息前最近一条 AI 回复已接钩。
func MarkHookedBeforeMessage(tenantID, conversationID, customerMessageID uint) error {
	if tenantID == 0 || conversationID == 0 {
		return nil
	}
	var prev model.Message
	q := db.DB.Where("tenant_id = ? AND conversation_id = ? AND sender_type = ?", tenantID, conversationID, "ai")
	if customerMessageID > 0 {
		q = q.Where("id < ?", customerMessageID)
	}
	if err := q.Order("id DESC").First(&prev).Error; err != nil {
		return nil
	}
	return db.DB.Model(&model.ReplyAttribution{}).
		Where("tenant_id = ? AND message_id = ? AND hooked = false", tenantID, prev.ID).
		Updates(map[string]interface{}{"hooked": true, "updated_at": time.Now()}).Error
}

// MarkLeadCaptured 标记客户/会话最近一条已归因回复带来留资。
func MarkLeadCaptured(tenantID, conversationID, customerID uint) error {
	return markLatest(tenantID, conversationID, customerID, "lead_captured")
}

// MarkPendingHuman 标记客户/会话最近一条已归因回复触发待人工接管。
func MarkPendingHuman(tenantID, conversationID, customerID uint) error {
	return markLatest(tenantID, conversationID, customerID, "pending_human")
}

// markLatest 回填最近一条归因记录的指定指标列。
func markLatest(tenantID, conversationID, customerID uint, column string) error {
	if tenantID == 0 || (conversationID == 0 && customerID == 0) {
		return nil
	}
	q := db.DB.Model(&model.ReplyAttribution{}).Where("tenant_id = ? AND template_id <> '' AND "+column+" = false", tenantID)
	if conversationID > 0 {
		q = q.Where("conversation_id = ?", conversationID)
	}
	if customerID > 0 {
		q = q.Where("customer_id = ?", customerID)
	}
	var row model.ReplyAttribution
	if err := q.Order("id DESC").First(&row).Error; err != nil {
		return nil
	}
	if err := db.DB.Model(&model.ReplyAttribution{}).Where("id = ?", row.ID).
		Updates(map[string]interface{}{column: true, "updated_at": time.Now()}).Error; err != nil {
		return err
	}
	if column == "lead_captured" && OnLeadCaptured != nil {
		OnLeadCaptured(row.PackCode)
	}
	return nil
}

// StatFilter 包/模板效果聚合条件。
type StatFilter struct {
	TenantID    *uint
	PackCode    string
	PackVersion string
	TemplateID  string
	Days        int
}

// Stats 按 tenant×pack_code×pack_version×template_id 聚合样本效果。
func Stats(f StatFilter, gdb ...*gorm.DB) ([]model.PackTemplateStat, error) {
	d := db.DB
	if len(gdb) > 0 && gdb[0] != nil {
		d = gdb[0]
	}
	q := d.Table("reply_attributions AS r").
		Select(`
			r.tenant_id,
			r.pack_code,
			r.pack_version,
			r.template_id,
			COUNT(*) AS sample_count,
			AVG(CASE WHEN r.hooked THEN 1.0 ELSE 0.0 END) AS hook_rate,
			AVG(CASE WHEN r.lead_captured THEN 1.0 ELSE 0.0 END) AS lead_rate,
			AVG(CASE WHEN r.pending_human THEN 1.0 ELSE 0.0 END) AS pending_human_rate,
			AVG(r.intent_after - r.intent_before) AS avg_intent_delta,
			AVG(CASE WHEN r.eval_score >= 0 THEN CAST(r.eval_score AS DOUBLE PRECISION) ELSE NULL END) AS avg_eval_score
		`).
		Where("r.template_id <> ''")
	if f.TenantID != nil {
		q = q.Where("r.tenant_id = ?", *f.TenantID)
	}
	if f.PackCode != "" {
		q = q.Where("r.pack_code = ?", f.PackCode)
	}
	if f.PackVersion != "" {
		q = q.Where("r.pack_version = ?", f.PackVersion)
	}
	if f.TemplateID != "" {
		q = q.Where("r.template_id = ?", f.TemplateID)
	}
	if f.Days > 0 {
		q = q.Where("r.created_at >= ?", time.Now().AddDate(0, 0, -f.Days))
	}
	var rows []model.PackTemplateStat
	err := q.Group("r.tenant_id, r.pack_code, r.pack_version, r.template_id").
		Order("sample_count DESC, r.pack_code ASC, r.template_id ASC").
		Scan(&rows).Error
	return rows, err
}

// SyncPackStats 小时级物化包/模板效果快照；供 main.go 后台 ticker（pack:quality:sweep）定时调用。
func SyncPackStats(gdb ...*gorm.DB) error {
	d := db.DB
	if len(gdb) > 0 && gdb[0] != nil {
		d = gdb[0]
	}
	rows, err := Stats(StatFilter{}, d)
	if err != nil {
		return err
	}
	now := time.Now()
	for _, r := range rows {
		snap := model.PackStatSnapshot{
			TenantID:       r.TenantID,
			PackCode:       r.PackCode,
			PackVersion:    r.PackVersion,
			TemplateID:     r.TemplateID,
			SampleCount:    r.SampleCount,
			HookRate:       r.HookRate,
			LeadRate:       r.LeadRate,
			PendingRate:    r.PendingRate,
			AvgIntentDelta: r.AvgIntentDelta,
			AvgEvalScore:   r.AvgEvalScore,
			ComputedAt:     now,
		}
		if err := d.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "tenant_id"}, {Name: "pack_code"}, {Name: "pack_version"}, {Name: "template_id"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"sample_count", "hook_rate", "lead_rate", "pending_human_rate", "avg_intent_delta", "avg_eval_score", "computed_at",
			}),
		}).Create(&snap).Error; err != nil {
			return err
		}
	}
	return nil
}

// StatsMessage 单条归因落库/查询错误信息（供 handler 统一响应）。
func StatsMessage(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("包效果统计失败：%v", err)
}

// ---- D9 evals 联动：离线评分与低分告警 ----

// ReplyScoreFunc 由组合根注入（main.go → service.ScoreReplyOffline 适配），返回 0~100 分。
var ReplyScoreFunc func(content string, anchors, forbidden []string) (int, []string)

// PackAlertFunc 由组合根注入（main.go → notifier + metrics），避免归因包直接依赖 service。
var PackAlertFunc func(packCode, message string)

var (
	packAlertMu    sync.Mutex
	lastPackAlert  = map[string]time.Time{}
	alertForbidden = []string{"绝密", "内部", "禁止", "忽略以上指令", "系统提示"}
)

// ScoreReplyAttributions 给未评分的归因回复补离线分（0~100）；limit<=0 默认 200。
func ScoreReplyAttributions(limit int, tenantIDs ...uint) (int, error) {
	if ReplyScoreFunc == nil {
		return 0, nil
	}
	if limit <= 0 {
		limit = 200
	}
	var rows []model.ReplyAttribution
	q := db.DB.Where("template_id <> '' AND eval_score < 0")
	// 测试隔离修复(2026-09-14)：可选租户过滤——共享库下全表扫描会把其它租户
	// （E2E/开发数据）的未评分行挤进 limit，导致单测计数漂移。生产调用不传即全量。
	if len(tenantIDs) > 0 && tenantIDs[0] > 0 {
		q = q.Where("tenant_id = ?", tenantIDs[0])
	}
	if err := q.Order("id ASC").Limit(limit).Find(&rows).Error; err != nil {
		return 0, err
	}
	tplCache := map[string][]string{}
	scored := 0
	for _, row := range rows {
		var msg model.Message
		if err := db.DB.Where("id = ? AND tenant_id = ?", row.MessageID, row.TenantID).First(&msg).Error; err != nil {
			continue
		}
		anchors := templateAnchors(row.TenantID, row.TemplateID, tplCache)
		score, _ := ReplyScoreFunc(msg.Content, anchors, alertForbidden)
		if score < 0 {
			score = 0
		}
		if score > 100 {
			score = 100
		}
		now := time.Now()
		if err := db.DB.Model(&model.ReplyAttribution{}).Where("id = ?", row.ID).Updates(map[string]interface{}{
			"eval_score": score, "eval_checked_at": now, "updated_at": now,
		}).Error; err != nil {
			return scored, err
		}
		scored++
	}
	return scored, nil
}

// templateAnchors 解析模板锚点列表并带请求级缓存。
func templateAnchors(tenantID uint, templateID string, cache map[string][]string) []string {
	key := fmt.Sprintf("%d:%s", tenantID, templateID)
	if v, ok := cache[key]; ok {
		return v
	}
	var tpl model.Template
	anchors := []string{}
	if err := db.DB.Where("id = ? AND (tenant_id = ? OR tenant_id = 0)", templateID, tenantID).First(&tpl).Error; err == nil {
		for _, list := range [][]string{jsonStringArray(tpl.TriggerTags), jsonStringArray(tpl.HookFields), jsonStringArray(tpl.ApplicableModels)} {
			anchors = append(anchors, list...)
		}
	}
	cache[key] = anchors
	return anchors
}

// jsonStringArray 将 JSON 字符串数组解析为 Go 切片。
func jsonStringArray(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var arr []string
	if err := json.Unmarshal([]byte(s), &arr); err == nil {
		return arr
	}
	return []string{s}
}

// AlertFilter 低分告警聚合条件。
type AlertFilter struct {
	Days        int
	MinSamples  int
	Threshold   int
	Consecutive int
	CoolDown    time.Duration
}

// CheckPackQualityAlerts 检查同包/模板最近连续样本是否低分劣化，并按冷却窗口触发告警。
func CheckPackQualityAlerts(f AlertFilter) ([]string, error) {
	if f.Days <= 0 {
		f.Days = 3
	}
	if f.MinSamples <= 0 {
		f.MinSamples = 5
	}
	if f.Threshold <= 0 {
		f.Threshold = 60
	}
	if f.Consecutive <= 0 {
		f.Consecutive = 3
	}
	if f.CoolDown <= 0 {
		f.CoolDown = 24 * time.Hour
	}
	since := time.Now().AddDate(0, 0, -f.Days)
	type group struct {
		TenantID   uint
		PackCode   string
		TemplateID string
	}
	var groups []group
	if err := db.DB.Model(&model.ReplyAttribution{}).
		Select("tenant_id, pack_code, template_id").
		Where("template_id <> '' AND eval_score >= 0 AND created_at >= ?", since).
		Group("tenant_id, pack_code, template_id").
		Having("COUNT(*) >= ?", f.MinSamples).
		Scan(&groups).Error; err != nil {
		return nil, err
	}
	var alerts []string
	for _, g := range groups {
		if PackAlertFunc == nil && g.PackCode == "" {
			continue
		}
		var scores []int
		if err := db.DB.Model(&model.ReplyAttribution{}).
			Select("eval_score").
			Where("tenant_id = ? AND pack_code = ? AND template_id = ? AND eval_score >= 0", g.TenantID, g.PackCode, g.TemplateID).
			Order("id DESC").Limit(f.Consecutive).
			Scan(&scores).Error; err != nil {
			return alerts, err
		}
		if len(scores) < f.Consecutive || avgInt(scores) >= float64(f.Threshold) {
			continue
		}
		key := fmt.Sprintf("%d:%s:%s", g.TenantID, g.PackCode, g.TemplateID)
		packAlertMu.Lock()
		if t, ok := lastPackAlert[key]; ok && time.Since(t) < f.CoolDown {
			packAlertMu.Unlock()
			continue
		}
		lastPackAlert[key] = time.Now()
		packAlertMu.Unlock()
		msg := fmt.Sprintf("包 %s 模板 %s 回复质量劣化：最近 %d 次均分 %.1f，低于阈值 %d", g.PackCode, g.TemplateID, f.Consecutive, avgFloat(scores), f.Threshold)
		alerts = append(alerts, msg)
		if PackAlertFunc != nil {
			PackAlertFunc(g.PackCode, msg)
		}
	}
	sort.Strings(alerts)
	return alerts, nil
}

// avgInt 计算整数切片的平均值。
func avgInt(xs []int) float64 {
	if len(xs) == 0 {
		return 0
	}
	var s int64
	for _, x := range xs {
		s += int64(x)
	}
	return float64(s) / float64(len(xs))
}

// avgFloat 复用整数平均值逻辑，保持调用点语义清晰。
func avgFloat(xs []int) float64 { return avgInt(xs) }
