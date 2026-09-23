// D3 触达闭环的查询与人工介入端点（2026-09-23）。
//
// 这一层刻意做薄：所有裁决、投递、状态机推进都在 internal/billing 里，本文件只做三件事——
// 取租户上下文、把领域视图包成信封、把"领域说不成立"与"服务端出错"分成两类回传。
//
// 为什么租户侧（admin）只读、平台侧（super）才有动作按钮：
//   - 用量预警的档位与催缴序列的节奏都是平台级热配（在 PlatformLevelKeys 里），租户改不了，
//     所以给它写接口没有意义，只会多一个"看起来能改其实改了无效"的入口；
//   - 反之"立刻再催一次""线下已付款所以停催"是平台的催收决策，只能超管点，
//     且两次点击都在领域层落审计行（带操作人与 IP），本层不重复写。
//
// 错误回传口径：billing.IsUserFacing 为真的是领域写好的中文判定（这家没有序列、没有到期时间…），
// 原样回显；其余一律走 RespErrInternal——DB 错误里可能带 SQL 片段，P1-3 不给客户端看。
package api

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"ai-scrm/internal/billing"
	"ai-scrm/internal/middleware"
)

// parseLimitQuery 解析 limit 查询参数（非法/缺省返回 0，由领域层按默认值钳）
func parseLimitQuery(c *gin.Context) int {
	n, _ := strconv.Atoi(c.Query("limit"))
	if n < 0 {
		return 0
	}
	return n
}

// AdminUsageAlerts GET /admin/usage/alerts?limit=
// apidump:ts AdminUsageAlertsResp
// 本租户的额度预警视图：当前进度 + 平台生效口径 + 历史留痕。
// 三样东西必须一起给：只给留痕，管理员看不到"现在到哪儿了"；只给进度，就无法解释
// "为什么 80% 没通知我却收到了"（那是上一账期已发过的档，靠 list 自证）。
func AdminUsageAlerts(c *gin.Context) {
	tid := tenantIDOf(c)
	RespOK(c, "ok", gin.H{
		"period_key": billing.UsageAlertPeriod(),
		"config":     billing.CurrentUsageAlertConfig(),
		"progress":   billing.UsageProgress(tid),
		"list":       billing.ListTenantUsageAlerts(tid, parseLimitQuery(c)),
	})
}

// AdminTenantDunning GET /admin/billing/dunning
// apidump:ts AdminDunningResp
// 本租户催缴进度（欠费户后台要看"还能撑到哪天、第几档了"）。无序列时 exists=false，不是错误。
func AdminTenantDunning(c *gin.Context) {
	tid := tenantIDOf(c)
	RespOK(c, "ok", gin.H{
		"dunning": billing.GetTenantDunning(tid),
		"config":  billing.CurrentDunningConfig(),
	})
}

// SuperDunningQueue GET /super/dunning?only_open=&limit=
// apidump:ts SuperDunningQueueResp
// 平台催缴工作队列（跨租户）。默认只看在册未结序列，only_open=false 时连已结的一起回看。
// 附两份平台口径：开关关着时队列必然空，前端据此显示"催缴未启用"而不是"没有欠费的客户"，
// 这两种状态对运营是完全不同的结论。
func SuperDunningQueue(c *gin.Context) {
	onlyOpen := c.Query("only_open") != "false"
	RespOK(c, "ok", gin.H{
		"list":               billing.ListDunningQueue(onlyOpen, parseLimitQuery(c)),
		"config":             billing.CurrentDunningConfig(),
		"usage_alert_config": billing.CurrentUsageAlertConfig(),
	})
}

// SuperDunningNudge POST /super/dunning/:id/nudge
// apidump:ts SuperDunningActionResult
// 人工"立刻再催一次"：按当前档位重发一封，不推进序列（节奏是状态机的，不是按钮的）。
func SuperDunningNudge(c *gin.Context) {
	tid, ok := PathUintID(c)
	if !ok {
		return
	}
	uid, _, _ := middleware.CurrentUser(c)
	if err := billing.NudgeDunningNow(tid, uid, c.ClientIP()); err != nil {
		if billing.IsUserFacing(err) {
			RespErr(c, http.StatusBadRequest, 400, err.Error())
			return
		}
		RespErrInternal(c, err, "人工催缴失败")
		return
	}
	RespOK(c, "催缴已重发", gin.H{"tenant_id": tid})
}

// SuperDunningReset POST /super/dunning/:id/reset
// apidump:ts SuperDunningActionResult
// 人工清序列（线下已收款/谈定缓收）：只停止后续催缴，**不解封**——解封只认可到账那条路。
func SuperDunningReset(c *gin.Context) {
	tid, ok := PathUintID(c)
	if !ok {
		return
	}
	uid, _, _ := middleware.CurrentUser(c)
	if err := billing.ResetDunning(tid, uid, c.ClientIP()); err != nil {
		if billing.IsUserFacing(err) {
			RespErr(c, http.StatusBadRequest, 400, err.Error())
			return
		}
		RespErrInternal(c, err, "催缴序列处理失败")
		return
	}
	RespOK(c, "催缴序列已关闭", gin.H{"tenant_id": tid})
}
