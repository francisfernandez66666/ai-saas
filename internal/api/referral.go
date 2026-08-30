// 邀请推广API：邀请码/链接/二维码与邀请记录查询。
package api

// ============================================================
// 邀请推广 API（M-R，2026-08-25）
//
// GET /api/v1/admin/referral/info    邀请码/链接参数/统计/我的桶现状
// GET /api/v1/admin/referral/qrcode  后端渲染邀请链接二维码 PNG（前端 <img>/blob 直接用）
//
// 权限：AdminRequired（租户管理员及以上）——推广主体是租户管理员。
// 二维码由后端出（用户明确要求），内容 = 注册页链接 ?ref=<邀请码>，
// 受邀人完成注册即首绑邀请关系（InvitedByTenantID 仅此一次）。
// ============================================================

import (
	"net/http"
	"strconv"

	"ai-scrm/internal/db"
	"ai-scrm/internal/middleware"
	"ai-scrm/internal/model"
	"ai-scrm/internal/service"

	"github.com/gin-gonic/gin"
	qrcode "github.com/skip2/go-qrcode"
)

// buildInviteURL 按请求构造注册页邀请链接（scheme 兼容反代 X-Forwarded-Proto）
func buildInviteURL(c *gin.Context, code string) string {
	scheme := "http"
	if p := c.GetHeader("X-Forwarded-Proto"); p == "https" {
		scheme = "https"
	} else if c.Request.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + c.Request.Host + "/register?ref=" + code
}

// GetReferralInfo GET /api/v1/admin/referral/info
func GetReferralInfo(c *gin.Context) {
	ti := middleware.GetTenantInfo(c)
	tenantID := ti.ID
	if tenantID == 0 {
		RespErr(c, http.StatusForbidden, 403, "无租户语境")
		return
	}
	info, err := service.GetReferralInfo(tenantID)
	if err != nil {
		RespErr(c, http.StatusInternalServerError, 500, "查询失败")
		return
	}
	RespOK(c, "", gin.H{
		"invite_url": buildInviteURL(c, info.InviteCode),
		"referral":   info,
	})
}

// GetReferralQRCode GET /api/v1/admin/referral/qrcode?size=320
// 返回 image/png；size 可调 100~1000，默认 320
func GetReferralQRCode(c *gin.Context) {
	ti := middleware.GetTenantInfo(c)
	tenantID := ti.ID
	if tenantID == 0 {
		RespErr(c, http.StatusForbidden, 403, "无租户语境")
		return
	}
	code, err := service.EnsureInviteCode(tenantID)
	if err != nil {
		RespErr(c, http.StatusInternalServerError, 500, "邀请码生成失败")
		return
	}
	size := 320
	if s := c.Query("size"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n >= 100 && n <= 1000 {
			size = n
		}
	}
	png, err := qrcode.Encode(buildInviteURL(c, code), qrcode.Medium, size)
	if err != nil {
		RespErr(c, http.StatusInternalServerError, 500, "二维码生成失败")
		return
	}
	c.Data(http.StatusOK, "image/png", png)
}

// currentUserID 从 JWT 语境取当前登录用户ID（0=未登录/系统）
func currentUserID(c *gin.Context) uint {
	v, _ := c.Get("user_id")
	if uid, ok := v.(uint); ok {
		return uid
	}
	return 0
}

// InviteeRecord 单条邀请记录（前台展示口径）
type InviteeRecord struct {
	TenantID     uint   `json:"tenant_id"` // 被邀请人账户ID
	CompanyName  string `json:"company_name"`
	Email        string `json:"email"`         // 被邀请人绑定邮箱（外键身份锚）
	InvitedOK    bool   `json:"invited_ok"`    // 邀请成功（首绑完成即成功）
	PaidOK       bool   `json:"paid_ok"`       // 好友已支付（存在paid订单）
	PaidRewarded bool   `json:"paid_rewarded"` // 邀请付费奖励已发放
	SignupReward bool   `json:"signup_reward"` // 邀请注册奖励已发放
	RegisteredAt string `json:"registered_at"`
}

// GetReferralRecords GET /api/v1/admin/referral/records —— 邀请好友前台记录列表
func GetReferralRecords(c *gin.Context) {
	ti := middleware.GetTenantInfo(c)
	if ti.ID == 0 {
		RespErr(c, http.StatusForbidden, 403, "无租户语境")
		return
	}
	var rows []model.Tenant
	db.DB.Where("invited_by_tenant_id = ?", ti.ID).Order("id DESC").Find(&rows)

	out := make([]InviteeRecord, 0, len(rows))
	for _, t := range rows {
		rec := InviteeRecord{
			TenantID:     t.ID,
			CompanyName:  t.Name,
			Email:        t.ContactEmail,
			InvitedOK:    true,
			PaidRewarded: t.ReferralPaidRewarded,
			RegisteredAt: t.CreatedAt.Format("2006-01-02 15:04"),
		}
		// 邀请注册奖励是否发放：referral_signup 台账(邀请人=我,受邀=该租户)
		var signupCnt, paidCnt int64
		db.DB.Model(&model.RewardClaim{}).
			Where("grant_type='referral_signup' AND tenant_id=? AND ref_id=?", ti.ID, t.ID).Count(&signupCnt)
		rec.SignupReward = signupCnt > 0
		// 好友支付状态：名下是否存在 paid 订单
		db.DB.Model(&model.BillingOrder{}).
			Where("tenant_id=? AND status='paid'", t.ID).Count(&paidCnt)
		rec.PaidOK = paidCnt > 0
		out = append(out, rec)
	}
	RespOK(c, "", gin.H{"list": out})
}
