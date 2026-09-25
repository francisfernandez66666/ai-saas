// 主动触达管理端（触达最小闭环 · 批次3，2026-09-23）：队列列表 + 排期 + 撤回。
//
// 只走 admin 闸（AdminRequired 在路由组上），所有读写一律用 db.RQ(c) 取库并按行内 tenant_id
// 收敛——触达任务是租户私有数据，超管不带 X-Tenant-ID 时也拿不到别人的队列（List/Cancel 的
// tenant_id 条件即兜底）。
//
// 出参不回显 external_id/channel 凭据等通道细节：前端只需要"发给了哪个客户、什么时候发、
// 为什么没发出去"，可达性诊断信息由 reason 承载（稳定原因码，文案在前端）。
package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"ai-scrm/internal/db"
	"ai-scrm/internal/errcodes"
	"ai-scrm/internal/middleware"
	"ai-scrm/internal/model"
	"ai-scrm/internal/outreach"
)

// outreachTaskView 任务出参视图（含客户名，前端列表不再逐行二次查询）
func outreachTaskView(t *model.OutreachTask, customerName string) gin.H {
	return gin.H{
		"id":            t.ID,
		"customer_id":   t.CustomerID,
		"customer_name": customerName,
		"content":       t.Content,
		"scheduled_at":  t.ScheduledAt,
		"status":        t.Status,
		"reason":        t.Reason,
		"error":         t.Error,
		"channel_id":    t.ChannelID,
		"outbound_id":   t.OutboundID,
		"attempts":      t.Attempts,
		"created_by":    t.CreatedBy,
		"sent_at":       t.SentAt,
		"created_at":    t.CreatedAt,
	}
}

// customerNamesByID 批量取本页任务涉及的客户名（一次 IN 查询，避免逐行 N+1）
func customerNamesByID(c *gin.Context, rows []model.OutreachTask) map[uint]string {
	ids := make([]uint, 0, len(rows))
	seen := make(map[uint]bool, len(rows))
	for i := range rows {
		if rows[i].CustomerID > 0 && !seen[rows[i].CustomerID] {
			seen[rows[i].CustomerID] = true
			ids = append(ids, rows[i].CustomerID)
		}
	}
	out := make(map[uint]string, len(ids))
	if len(ids) == 0 {
		return out
	}
	type pair struct {
		ID   uint
		Name string
	}
	var pairs []pair
	// 名称列走 RQ+T 双闸：跨租户 ID 猜不出来也不该被读出
	if err := db.RQ(c).Scopes(db.T(c)).Model(&model.Customer{}).
		Select("id", "name").Where("id IN ?", ids).Scan(&pairs).Error; err != nil {
		return out
	}
	for _, p := range pairs {
		out[p.ID] = p.Name
	}
	return out
}

// ListOutreachTasks GET /admin/outreach/tasks?status=&limit=&offset=
// apidump:ts OutreachListResp
// 触达队列列表：按计划时间倒序分页，附本租户生效参数（前端据此显示"开关未开"提示）。
func ListOutreachTasks(c *gin.Context) {
	tid := db.EffectiveTenantIDFromGin(c)
	status := c.Query("status")
	limit, _ := strconv.Atoi(c.Query("limit"))
	offset, _ := strconv.Atoi(c.Query("offset"))
	rows, total, err := outreach.List(db.RQ(c), tid, status, limit, offset)
	if err != nil {
		RespErrInternal(c, err, "触达队列读取失败")
		return
	}
	names := customerNamesByID(c, rows)
	list := make([]gin.H, 0, len(rows))
	for i := range rows {
		list = append(list, outreachTaskView(&rows[i], names[rows[i].CustomerID]))
	}
	p := outreach.LoadParams(tid)
	RespOK(c, "ok", gin.H{
		"list":  list,
		"total": total,
		"config": gin.H{
			"enabled":      p.Enabled,
			"weekly_limit": p.WeeklyLimit,
			"quiet_hours":  p.QuietHours,
			"window_hours": p.WindowHours,
		},
	})
}

// CreateOutreachTask POST /admin/outreach/tasks {customer_id,content,scheduled_at}
// apidump:ts OutreachTaskRow
// 排期一条触达：合规裁决（开关/文案/客户归属/周内频次/静默顺延）全在 outreach.Create 里，
// 这里只做入参形态校验与拒绝原因回传（reason 是稳定码，前端按它出文案）。
func CreateOutreachTask(c *gin.Context) {
	tid := db.EffectiveTenantIDFromGin(c)
	uid, _, _ := middleware.CurrentUser(c)
	var req struct {
		CustomerID  uint   `json:"customer_id" binding:"required"`
		Content     string `json:"content" binding:"required"`
		ScheduledAt string `json:"scheduled_at"` // 空=立即排期（落在静默段会自动顺延）
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErrBind(c, err)
		return
	}
	sched := time.Time{}
	if s := req.ScheduledAt; s != "" {
		parsed, err := time.Parse(time.RFC3339, s)
		if err != nil {
			RespErr(c, http.StatusBadRequest, 400, "scheduled_at 需为 RFC3339 时间")
			return
		}
		sched = parsed.Local()
	}
	task, err := outreach.Create(db.RQ(c), outreach.CreateInput{
		TenantID: tid, CustomerID: req.CustomerID, Content: req.Content,
		ScheduledAt: sched, CreatedBy: uid,
	})
	if err != nil {
		if reason := outreach.RejectReason(err); reason != "" {
			// 400 + 稳定原因码：这是"这次请求不合规"而非服务端故障。
			// 不走 RespErr 是为了多带一个 reason 键——msg 是人读文案，reason 是给前端分支和
			// smoke 断言用的字面量（两者都可能改文案，但码不能改）。
			c.JSON(http.StatusBadRequest, gin.H{
				"code": 400, "message": err.Error(),
				"error_code": errcodes.OutreachRejected, "reason": reason,
			})
			return
		}
		RespErrInternal(c, err, "触达排期失败")
		return
	}
	names := customerNamesByID(c, []model.OutreachTask{*task})
	RespOK(c, "ok", outreachTaskView(task, names[task.CustomerID]))
}

// CancelOutreachTask POST /admin/outreach/tasks/:id/cancel
// apidump:ts OutreachCancelResp
// 撤回一条尚未派发的任务（仅 pending 可撤）；已 queued 的归通道层，撤回不再有意义。
func CancelOutreachTask(c *gin.Context) {
	tid := db.EffectiveTenantIDFromGin(c)
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		RespErr(c, http.StatusBadRequest, 400, "ID 非法")
		return
	}
	affected, cerr := outreach.Cancel(db.RQ(c), tid, uint(id))
	if cerr != nil {
		RespErrInternal(c, cerr, "撤回失败")
		return
	}
	if affected == 0 {
		// 不存在 / 跨租户 / 已过 pending 态，三者对调用方同等不可撤回——不回显具体哪一种，
		// 免得给探测者一个"该 ID 属于本租户"的信号
		RespErr(c, http.StatusNotFound, 404, "任务不存在或已不可撤回")
		return
	}
	RespOK(c, "ok", gin.H{"id": id})
}
