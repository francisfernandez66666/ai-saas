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
	"ai-scrm/internal/schema"
	"ai-scrm/internal/service"

	"github.com/gin-gonic/gin"
	qrcode "github.com/skip2/go-qrcode"
)

// buildInviteURL 按请求构造注册页邀请链接
// scheme 兼容反代场景：优先读取 X-Forwarded-Proto 头，再检查 TLS
func buildInviteURL(c *gin.Context, code string) string {
	scheme := "http"
	if p := c.GetHeader("X-Forwarded-Proto"); p == "https" {
		scheme = "https"
	} else if c.Request.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + c.Request.Host + "/register?ref=" + code
}

// GetReferralInfo 获取邀请推广信息
// GET /api/v1/admin/referral/info
// 返回当前租户的邀请码、邀请链接、邀请统计等信息
func GetReferralInfo(c *gin.Context) {
	ti := middleware.GetTenantInfo(c)
	tenantID := ti.ID
	if tenantID == 0 {
		RespErr(c, http.StatusForbidden, 403, "无租户语境")
		return
	}
	// 查询邀请信息（含邀请码、邀请人数、奖励统计）
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

// GetReferralQRCode 获取邀请链接二维码
// GET /api/v1/admin/referral/qrcode?size=320
// 返回 image/png 格式二维码图片，size 可调 100~1000，默认 320
func GetReferralQRCode(c *gin.Context) {
	ti := middleware.GetTenantInfo(c)
	tenantID := ti.ID
	if tenantID == 0 {
		RespErr(c, http.StatusForbidden, 403, "无租户语境")
		return
	}
	// 确保邀请码存在（自动生成或返回已有）
	code, err := service.EnsureInviteCode(tenantID)
	if err != nil {
		RespErr(c, http.StatusInternalServerError, 500, "邀请码生成失败")
		return
	}
	// 解析二维码尺寸参数
	size := 320
	if s := c.Query("size"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n >= 100 && n <= 1000 {
			size = n
		}
	}
	// 生成二维码图片
	png, err := qrcode.Encode(buildInviteURL(c, code), qrcode.Medium, size)
	if err != nil {
		RespErr(c, http.StatusInternalServerError, 500, "二维码生成失败")
		return
	}
	c.Data(http.StatusOK, "image/png", png)
}

// currentUserID 从 JWT 语境取当前登录用户ID
// 0 表示未登录或系统操作
func currentUserID(c *gin.Context) uint {
	v, _ := c.Get("user_id")
	if uid, ok := v.(uint); ok {
		return uid
	}
	return 0
}

// InviteeRecord 单条邀请记录（前台展示口径）
type InviteeRecord struct {
	TenantID     uint   `json:"tenant_id"`     // 被邀请人账户ID
	CompanyName  string `json:"company_name"`  // 被邀请人公司名
	Email        string `json:"email"`         // 被邀请人绑定邮箱（外键身份锚）
	InvitedOK    bool   `json:"invited_ok"`    // 邀请成功（首绑完成即成功）
	PaidOK       bool   `json:"paid_ok"`       // 好友已支付（存在paid订单）
	PaidRewarded bool   `json:"paid_rewarded"` // 邀请付费奖励已发放
	SignupReward bool   `json:"signup_reward"` // 邀请注册奖励已发放
	RegisteredAt string `json:"registered_at"` // 注册时间
}

// GetReferralRecords 获取邀请好友记录列表
// GET /api/v1/admin/referral/records
// 返回当前租户邀请的所有好友及其奖励发放状态
// P2-29 修复(2026-09-09)：原每行 2 次 Count（N+1）+ 全表返回；改分页 + IN 批量聚合
func GetReferralRecords(c *gin.Context) {
	ti := middleware.GetTenantInfo(c)
	if ti.ID == 0 {
		RespErr(c, http.StatusForbidden, 403, "无租户语境")
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize := schema.NormalizePageSize(atoiDefault(c.DefaultQuery("page_size", "20")))
	if page <= 0 {
		page = 1
	}
	// 查询被邀请的租户列表（invited_by_tenant_id 指向我）
	var total int64
	db.DB.Model(&model.Tenant{}).Where("invited_by_tenant_id = ?", ti.ID).Count(&total)
	var rows []model.Tenant
	db.DB.Where("invited_by_tenant_id = ?", ti.ID).Order("id DESC").
		Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows)

	// 收集受邀租户ID，两次 IN 批量聚合奖励/付费状态（消除每行 2 次 Count 的 N+1）
	if len(rows) == 0 {
		RespOK(c, "", gin.H{"list": []InviteeRecord{}, "total": total, "page": page, "page_size": pageSize})
		return
	}
	ids := make([]uint, 0, len(rows))
	for _, t := range rows {
		ids = append(ids, t.ID)
	}
	signupGranted := map[uint]bool{}
	var signupPairs []struct {
		RefID uint
		Cnt   int64
	}
	db.DB.Model(&model.RewardClaim{}).
		Select("ref_id, count(*) as cnt").
		Where("grant_type='referral_signup' AND tenant_id=? AND ref_id IN ?", ti.ID, ids).
		Group("ref_id").Scan(&signupPairs)
	for _, p := range signupPairs {
		signupGranted[p.RefID] = p.Cnt > 0
	}
	paidOK := map[uint]bool{}
	var paidPairs []struct {
		TenantID uint
		Cnt      int64
	}
	db.DB.Model(&model.BillingOrder{}).
		Select("tenant_id, count(*) as cnt").
		Where("status='paid' AND tenant_id IN ?", ids).
		Group("tenant_id").Scan(&paidPairs)
	for _, p := range paidPairs {
		paidOK[p.TenantID] = p.Cnt > 0
	}

	out := make([]InviteeRecord, 0, len(rows))
	for _, t := range rows {
		out = append(out, InviteeRecord{
			TenantID:     t.ID,
			CompanyName:  t.Name,
			Email:        t.ContactEmail,
			InvitedOK:    true,
			PaidRewarded: t.ReferralPaidRewarded,
			RegisteredAt: t.CreatedAt.Format("2006-01-02 15:04"),
			SignupReward: signupGranted[t.ID],
			PaidOK:       paidOK[t.ID],
		})
	}
	RespOK(c, "", gin.H{"list": out, "total": total, "page": page, "page_size": pageSize})
}
