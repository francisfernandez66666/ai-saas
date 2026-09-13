// 数据导出（D7，2026-09-12）：企业交付项，客户/会话 CSV 流式下载。
// 纪律：读走 db.RQ(c)+db.T+db.DataScope（租户+四级数据范围双闸，admin 组已挂 AdminRequired）；
// PII 遵循现有掩码规则（手机号列掩码、正文内手机/邮箱掩码）——导出用于经营分析而非联系方式倒卖。
package api

import (
	"encoding/csv"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/service"
)

// exportRowCap 单次导出行数上限（防大租户一次性拖爆内存/带宽）；可经 ?limit= 调低，不可调高于硬顶。
const exportRowCap = 50000

func exportLimit(c *gin.Context) int {
	n := exportRowCap
	if v, err := strconv.Atoi(c.Query("limit")); err == nil && v > 0 && v < n {
		n = v
	}
	return n
}

func csvWriter(c *gin.Context, filename string) *csv.Writer {
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", "attachment; filename=\""+filename+"\"")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Status(http.StatusOK)
	// BOM：让 Excel 正确识别 UTF-8，避免中文乱码（企业交付常见诉求）
	_, _ = c.Writer.Write([]byte{0xEF, 0xBB, 0xBF})
	return csv.NewWriter(c.Writer)
}

// AdminExportCustomers GET /admin/export/customers.csv
// 列：id,name,phone(掩码),wechat_id,city,customer_type,interest_model,source,intent_score,journey_stage,tags,assigned_user_id,created_at
func AdminExportCustomers(c *gin.Context) {
	tid := db.EffectiveTenantIDFromGin(c)
	if tid == 0 {
		RespErr(c, http.StatusBadRequest, 400, "缺少租户上下文")
		return
	}
	w := csvWriter(c, "customers.csv")
	_ = w.Write([]string{"id", "name", "phone", "wechat_id", "city", "customer_type",
		"interest_model", "source", "intent_score", "journey_stage", "tags", "assigned_user_id", "created_at"})
	w.Flush()

	limit := exportLimit(c)
	var rows []model.Customer
	q := db.RQ(c).Model(&model.Customer{}).Scopes(db.T(c), db.DataScope(c)).Order("id ASC").Limit(limit)
	if err := q.Find(&rows).Error; err != nil {
		w.Error()
		return
	}
	for i := range rows {
		cu := &rows[i]
		rec := []string{
			strconv.FormatUint(uint64(cu.ID), 10),
			cu.Name,
			service.MaskPhone(cu.Phone), // PII 掩码
			cu.WechatID,
			cu.City,
			cu.CustomerType,
			cu.InterestProduct,
			cu.Source,
			strconv.FormatFloat(cu.IntentScore, 'f', 2, 64),
			cu.JourneyStage,
			cu.Tags,
			strconv.FormatUint(uint64(cu.AssignedUserID), 10),
			cu.CreatedAt.Format(time.RFC3339),
		}
		_ = w.Write(rec)
		w.Flush()
	}
}

// AdminExportConversations GET /admin/export/conversations.csv?customer_id=
// 先复核 customer 在当前数据范围内（防越权拖他人会话），再导出该客户全部消息（正文手机/邮箱掩码）。
func AdminExportConversations(c *gin.Context) {
	tid := db.EffectiveTenantIDFromGin(c)
	if tid == 0 {
		RespErr(c, http.StatusBadRequest, 400, "缺少租户上下文")
		return
	}
	cid, err := strconv.ParseUint(c.Query("customer_id"), 10, 64)
	if err != nil || cid == 0 {
		RespErr(c, http.StatusBadRequest, 400, "customer_id 必填且为正整数")
		return
	}
	// 越权防线：该客户必须落在当前角色的数据范围内（复用同一套 scope 条件）
	var probe model.Customer
	if err := db.RQ(c).Model(&model.Customer{}).Scopes(db.T(c), db.DataScope(c)).
		Where("id = ?", cid).First(&probe).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "客户不存在或无权访问")
		return
	}

	w := csvWriter(c, "conversations_"+strconv.FormatUint(cid, 10)+".csv")
	_ = w.Write([]string{"id", "conversation_id", "sender_type", "content", "message_type", "route_result", "intent_score", "created_at"})
	w.Flush()

	limit := exportLimit(c)
	var msgs []model.Message
	if err := db.RQ(c).Model(&model.Message{}).Where("customer_id = ?", cid).
		Order("id ASC").Limit(limit).Find(&msgs).Error; err != nil {
		return
	}
	for i := range msgs {
		m := &msgs[i]
		rec := []string{
			strconv.FormatUint(uint64(m.ID), 10),
			strconv.FormatUint(uint64(m.ConversationID), 10),
			m.SenderType,
			service.MaskPhoneInText(m.Content), // 正文内手机号掩码
			m.MessageType,
			m.RouteResult,
			strconv.FormatFloat(m.IntentScore, 'f', 2, 64),
			m.CreatedAt.Format(time.RFC3339),
		}
		_ = w.Write(rec)
		w.Flush()
	}
}
