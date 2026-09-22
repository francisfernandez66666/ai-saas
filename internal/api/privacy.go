// PIPL 删除权 API（C2，2026-09-12）
// C 端入口 POST /privacy/deletion-request（免登录，visitor_key 自证 / 登录态可撤回本人账号）；
// Admin 入口 /admin/privacy/deletion-requests（列表 + 手动立即执行 + 审计）。
package api

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"ai-scrm/internal/db"
	"ai-scrm/internal/middleware"
	"ai-scrm/internal/model"
	"ai-scrm/internal/privacy"
)

// PrivacyDeletionRequest POST /privacy/deletion-request
// body: {scope:"customer"|"user", customer_id?, visitor_key?}
// - customer：匿名/登录均可，匿名必须带与目标一致的 visitor_key（C 端自证）；
// - user：仅登录态可撤回本人账号 PII（user_id 取 JWT，不收外部传参，防越权）。
func PrivacyDeletionRequest(c *gin.Context) {
	var req struct {
		Scope      string `json:"scope"`
		CustomerID uint   `json:"customer_id"`
		VisitorKey string `json:"visitor_key"`
	}
	_ = c.ShouldBindJSON(&req)
	tid := db.EffectiveTenantIDFromGin(c)
	if tid == 0 {
		RespErr(c, http.StatusBadRequest, 400, "缺少租户上下文")
		return
	}
	scope := req.Scope
	if scope == "" {
		scope = model.DeletionScopeCustomer
	}

	switch scope {
	case model.DeletionScopeUser:
		uidV, ok := c.Get("user_id")
		uid, _ := uidV.(uint)
		if !ok || uid == 0 {
			RespErr(c, http.StatusUnauthorized, 401, "撤回账号需登录")
			return
		}
		row, err := privacy.Enqueue(tid, model.DeletionScopeUser, 0, uid, 0)
		if err != nil && err != privacy.ErrAlreadyPending {
			RespErr(c, http.StatusBadRequest, 400, "受理失败："+err.Error())
			return
		}
		RespOK(c, "删除请求已受理，到期将匿名化", gin.H{"id": row.ID, "deadline": row.Deadline, "duplicated": err == privacy.ErrAlreadyPending})
		return

	case model.DeletionScopeCustomer:
		if req.CustomerID == 0 {
			RespErr(c, http.StatusBadRequest, 400, "customer_id 必填")
			return
		}
		// 身份防线：匿名必须 visitor_key 与目标客户一致。
		// B8 修复(2026-09-14)：登录态旧实现"即放行且不看归属"——任何销售可对租户内**任意客户**
		// 发起 PIPL 删除（误删/恶意面）。现收敛为：仍须该客户落在本人数据范围内
		// （tenant_admin/super 恒真，sales 仅本人名下），与读接口 DataScope 同口径。
		var cust model.Customer
		_, logged := c.Get("user_id")
		if !logged {
			if err := db.RQ(c).Where("id = ?", req.CustomerID).First(&cust).Error; err != nil {
				RespErr(c, http.StatusNotFound, 404, "客户不存在")
				return
			}
			if req.VisitorKey == "" || !hashEqual(cust.VisitorKey, req.VisitorKey) {
				RespErr(c, http.StatusForbidden, 403, "visitor_key 校验失败")
				return
			}
		} else {
			if err := db.RQ(c).Where("id = ?", req.CustomerID).First(&cust).Error; err != nil {
				RespErr(c, http.StatusNotFound, 404, "客户不存在")
				return
			}
			if !customerInDataScope(c, cust.AssignedUserID) {
				RespErr(c, http.StatusForbidden, 403, "该客户不在你的数据范围内，无权发起删除")
				return
			}
		}
		row, err := privacy.Enqueue(tid, model.DeletionScopeCustomer, req.CustomerID, 0, 0)
		if err != nil && err != privacy.ErrAlreadyPending {
			RespErr(c, http.StatusBadRequest, 400, "受理失败："+err.Error())
			return
		}
		RespOK(c, "删除请求已受理，到期将匿名化", gin.H{"id": row.ID, "deadline": row.Deadline, "duplicated": err == privacy.ErrAlreadyPending})
		return
	}
	RespErr(c, http.StatusBadRequest, 400, "scope 非法")
}

// AdminListDeletionRequests GET /admin/privacy/deletion-requests?status=&page=&page_size=
// AdminListDeletionRequests 列出租户 PIPL 删除请求。
func AdminListDeletionRequests(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 200 {
		size = 20
	}
	q := db.RQ(c).Model(&model.DeletionRequest{}).Scopes(db.T(c))
	if s := c.Query("status"); s != "" {
		q = q.Where("status = ?", s)
	}
	var total int64
	q.Count(&total)
	var rows []model.DeletionRequest
	q.Order("id DESC").Offset((page - 1) * size).Limit(size).Find(&rows)
	RespOK(c, "ok", gin.H{"list": rows, "total": total, "page": page, "page_size": size})
}

// AdminExecuteDeletionRequest POST /admin/privacy/deletion-requests/:id/execute
// 手动立即执行（不等 15 天到期）：满足"用户催删/监管要求"场景。幂等：已匿名化的再点无副作用。
func AdminExecuteDeletionRequest(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		RespErr(c, http.StatusBadRequest, 400, "id 非法")
		return
	}
	// 越权防线：只能执行本租户的请求
	var row model.DeletionRequest
	if err := db.RQ(c).Scopes(db.T(c)).First(&row, id).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "删除请求不存在")
		return
	}
	if err := privacy.ExecuteDeletion(uint(id)); err != nil {
		RespErrInternal(c, err, "执行失败")
		return
	}
	// 审计留痕
	uid, _, _ := middleware.CurrentUser(c)
	db.DB.Create(&model.TenantAuditLog{
		TenantID: row.TenantID, UserID: uid, Action: "privacy_deletion_execute",
		Resource: "deletion_request:" + strconv.FormatUint(id, 10), Detail: "scope=" + row.Scope,
		IP: c.ClientIP(), UserAgent: c.Request.UserAgent(),
	})
	RespOK(c, "已匿名化", gin.H{"id": id})
}
