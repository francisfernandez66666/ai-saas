// 协议签署：注册即视为同意《用户协议》《隐私政策》落签署记录，超管查询签署列表
package api

import (
	"strconv"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/schema"

	"github.com/gin-gonic/gin"
)

// atoiDefault 字符串转 int，失败/空返回默认值（P2-29 分页口径统一）
func atoiDefault(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

// agreementVersion 协议当前生效版本（产品功能/条款变更时递增）
const agreementVersion = "v2026.08.27"

/*
RecordAgreementSignatures 注册即同意协议

用途：用户注册时自动记录《用户协议》《隐私政策》签署记录。

幂等设计：
  - 同用户同类型已签则跳过，避免重复注册/重试产生多条记录

参数：
  - tenantID: 租户 ID
  - userID: 用户 ID

返回值：无（直接操作数据库）

设计说明：注册即视为同意，无需用户主动签署，符合产品业务逻辑。
*/
func RecordAgreementSignatures(tenantID uint, userID uint) {
	now := time.Now()
	// 协议类型：user（用户协议）、privacy（隐私政策）
	types := []string{"user", "privacy"}
	for _, t := range types {
		// 幂等检查：同用户同类型已签则跳过
		var cnt int64
		db.DB.Model(&model.AgreementSignature{}).
			Where("user_id = ? AND agreement_type = ?", userID, t).Count(&cnt)
		if cnt > 0 {
			continue
		}
		// 创建签署记录
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

/*
SuperAgreementList 协议签署列表查询

GET /api/v1/super/agreements

用途：平台超管查看协议签署情况，支持按协议类型筛选。

参数：
  - type: 协议类型筛选（user/privacy，可选）

返回：
  - list: 签署记录列表（含用户名、租户名）
  - total: 总记录数

设计说明：限制最多返回 500 条，避免大数据量查询性能问题。
*/
func SuperAgreementList(c *gin.Context) {
	atype := c.Query("type") // user|privacy 可空
	// 超管专属(SuperRequired 守卫)跨租户查看协议台账：全平台语义（rls_scope_test 已验证）
	// P2-29 修复(2026-09-09)：分页 + IN 批量关联——原 Limit(500) 全表 + 500 行×2 次 Pluck=千次查询。
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize := schema.NormalizePageSize(atoiDefault(c.DefaultQuery("page_size", "20")))
	if page <= 0 {
		page = 1
	}

	q := db.DB.Model(&model.AgreementSignature{})
	if atype != "" {
		q = q.Where("agreement_type = ?", atype)
	}
	var total int64
	q.Count(&total)
	var rows []model.AgreementSignature
	q.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows)

	// 扩展结构：关联查询用户名和租户名
	type item struct {
		model.AgreementSignature
		Username   string `json:"username"`
		TenantName string `json:"tenant_name"`
	}
	items := make([]item, 0, len(rows))

	// 收集关联键，一次 IN 查回映射（去掉千次 PLuck 的 N+1）
	uidSet, tidSet := map[uint]bool{}, map[uint]bool{}
	for _, r := range rows {
		if r.UserID > 0 {
			uidSet[r.UserID] = true
		}
		if r.TenantID > 0 {
			tidSet[r.TenantID] = true
		}
	}
	uname := map[uint]string{}
	if len(uidSet) > 0 {
		var ids []uint
		for id := range uidSet {
			ids = append(ids, id)
		}
		var pairs []struct {
			ID       uint
			Username string
		}
		db.DB.Model(&model.User{}).Select("id, username").Where("id IN ?", ids).Find(&pairs)
		for _, p := range pairs {
			uname[p.ID] = p.Username
		}
	}
	tname := map[uint]string{}
	if len(tidSet) > 0 {
		var ids []uint
		for id := range tidSet {
			ids = append(ids, id)
		}
		var pairs []struct {
			ID   uint
			Name string
		}
		db.DB.Model(&model.Tenant{}).Select("id, name").Where("id IN ?", ids).Find(&pairs)
		for _, p := range pairs {
			tname[p.ID] = p.Name
		}
	}
	for _, r := range rows {
		it := item{AgreementSignature: r, Username: uname[r.UserID], TenantName: tname[r.TenantID]}
		items = append(items, it)
	}
	RespOK(c, "", gin.H{"list": items, "total": total, "page": page, "page_size": pageSize})
}
