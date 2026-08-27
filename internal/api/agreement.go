// 协议签署：注册即视为同意《用户协议》《隐私政策》落签署记录，超管查询签署列表
package api

import (
	"net/http"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"

	"github.com/gin-gonic/gin"
)

// agreementVersion 协议当前生效版本（产品功能/条款变更时递增）
const agreementVersion = "v2026.08.27"

// RecordAgreementSignatures 注册即同意：对同一用户落《用户协议》《隐私政策》两条签署记录
// 幂等：同用户同类型已签则跳过（避免重复注册/重试产生多条）
func RecordAgreementSignatures(tenantID uint, userID uint) {
	now := time.Now()
	types := []string{"user", "privacy"}
	for _, t := range types {
		var cnt int64
		db.DB.Model(&model.AgreementSignature{}).
			Where("user_id = ? AND agreement_type = ?", userID, t).Count(&cnt)
		if cnt > 0 {
			continue
		}
		db.DB.Create(&model.AgreementSignature{
			TenantID:      tenantID,
			UserID:        userID,
			AgreementType: t,
			Version:       agreementVersion,
			Status:        "signed",
			SignedAt:      now,
		})
	}
}

// SuperAgreementList GET /api/v1/super/agreements
// 平台超管"协议签署"tab 数据：列出已签署用户、协议类型、版本、状态与签署时间
func SuperAgreementList(c *gin.Context) {
	atype := c.Query("type") // user|privacy 可空
	q := db.DB.Model(&model.AgreementSignature{})
	if atype != "" {
		q = q.Where("agreement_type = ?", atype)
	}
	var total int64
	q.Count(&total)
	var rows []model.AgreementSignature
	q.Order("id DESC").Limit(500).Find(&rows)

	type item struct {
		model.AgreementSignature
		Username   string `json:"username"`
		TenantName string `json:"tenant_name"`
	}
	items := make([]item, 0, len(rows))
	for _, r := range rows {
		it := item{AgreementSignature: r}
		db.DB.Model(&model.User{}).Where("id = ?", r.UserID).Pluck("username", &it.Username)
		db.DB.Model(&model.Tenant{}).Where("id = ?", r.TenantID).Pluck("name", &it.TenantName)
		items = append(items, it)
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": gin.H{"list": items, "total": total}})
}
