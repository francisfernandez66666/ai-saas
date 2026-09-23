// AI 贡献度下钻名单（D4，2026-09-23）。
//
// 看板卡片上的六个客户级数字此前只能看，不能问「这 8 个是谁」。
// 没有下钻，客户对数字的质疑就无处核对；有了下钻但口径不同，质疑反而被坐实。
// 因此本文件与看板共用 contributionCustomers 这份归属表 + analytics 里那一份谓词：
// 名单条数与卡片数字在结构上同源，不是"两边都写对了"，而是"只写了一遍"。
//
// 单位纪律：只有客户级指标可下钻。会话级（活跃/新建会话）、消息级（AI/人工/客户消息）
// 与瞬时值（待接管）的单位不是"客户"，点开一份客户名单就会数字与条数对不上，
// 故这些指标既不在此端点的 metric 白名单里，前端也不给它们加点击。
//
// 权限口径与 /customers 一致：登录态 + 数据范围裁剪（销售只见自己名下、
// 部门管理员见本部门子树、租户/平台管理员见全租户），不含跨租户数据。
package api

import (
	"net/http"
	"sort"
	"strconv"
	"time"

	"ai-scrm/internal/analytics"
	"ai-scrm/internal/db"
	"ai-scrm/internal/model"

	"github.com/gin-gonic/gin"
)

// drillDefaultPageSize 下钻列表默认页大小。
const drillDefaultPageSize = 20

// drillMaxPageSize 下钻列表页大小上限（下钻是核对用，不是导数用；导出走 /admin/export）。
const drillMaxPageSize = 100

// ContributionDrillNote 下钻名单随行下发的口径说明。
//
// 与看板 notes 分开写：看板说的是"这些数字怎么算的"，这里说的是"这个人为什么在名单里"。
var ContributionDrillNote = "名单与看板同一判据：登录者数据范围 ∩ 窗口内有消息往来的会话所涉客户；阶段取 CRM 现状，不是事件时刻归因。"

// AIContributionCustomerRow 下钻名单一行（列与「客户线索」列表对齐，便于同一套行渲染）。
type AIContributionCustomerRow struct {
	ID               uint    `json:"id"`                 // 客户ID
	Name             string  `json:"name"`               // 姓名（访客占位名前端统一显示为"客户"）
	Phone            string  `json:"phone"`              // 手机号（与 /customers 同暴露口径）
	JourneyStage     string  `json:"journey_stage"`      // 当前旅程阶段码（命中归因指标的依据）
	IntentScore      float64 `json:"intent_score"`       // 意向分 0-1
	InterestModel    string  `json:"interest_model"`     // 兴趣产品
	AssignedUserID   uint    `json:"assigned_user_id"`   // 归属顾问ID，0=未分配
	AssignedUserName string  `json:"assigned_user_name"` // 归属顾问姓名
	ServedBy         string  `json:"served_by"`          // 接待归属：ai=AI 独立接待，human=人工参与
}

// AIContributionDrillResp 下钻端点出参。
type AIContributionDrillResp struct {
	Metric     string                                   `json:"metric"`      // 本次下钻的指标码
	Label      string                                   `json:"label"`       // 指标中文名（后端下发，前端不复写）
	PeriodDays int                                      `json:"period_days"` // 统计窗口天数
	Since      string                                   `json:"since"`       // 窗口起（RFC3339）
	Until      string                                   `json:"until"`       // 窗口止（RFC3339）
	Total      int64                                    `json:"total"`       // 命中客户数 = 卡片上那个数字
	Page       int                                      `json:"page"`        // 当前页
	PageSize   int                                      `json:"page_size"`   // 页大小
	Metrics    []analytics.ContributionMetricDefinition `json:"metrics"`     // 可下钻指标清单（前端据此决定哪些数字可点）
	Note       string                                   `json:"note"`        // 随行口径说明
	List       []AIContributionCustomerRow              `json:"list"`        // 本页名单
}

/*
GetAIContributionCustomers 返回 AI 贡献度某个客户级指标背后的客户名单。

查询参数：
  - metric：必填，ai_served|human_served|ai_lead|assisted_lead|ai_arrived|ai_ordered
  - days：统计窗口，默认 30，取值 (0,365]（与看板同参同界）
  - page / page_size：分页，page_size 上限 100

返回：AIContributionDrillResp（total 与看板同一判据算出）

权限：登录态任意角色，按数据范围裁剪；指标不在白名单返回 400。
*/
// GetAIContributionCustomers 下钻名单。
// apidump:ts AIContributionDrillResp
// AI 贡献度指标背后的客户名单（与看板同源同口径）。
func GetAIContributionCustomers(c *gin.Context) {
	metric := c.Query("metric")
	if _, ok := analytics.ContributionMetricLabels[metric]; !ok {
		RespErr(c, http.StatusBadRequest, 400, "metric 不支持下钻，可选值见响应 metrics 字段")
		return
	}

	days := contributionDefaultDays
	if v := c.Query("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= contributionMaxDays {
			days = n
		}
	}
	page := 1
	if v, err := strconv.Atoi(c.Query("page")); err == nil && v > 0 {
		page = v
	}
	size := drillDefaultPageSize
	if v, err := strconv.Atoi(c.Query("page_size")); err == nil && v > 0 {
		size = v
	}
	if size > drillMaxPageSize {
		size = drillMaxPageSize
	}

	// until 在入口取一次：total 与看板数字、窗口起止必须同一时刻基准
	until := time.Now()
	since := until.AddDate(0, 0, -days)

	matched := analytics.FilterContributionCustomers(contributionCustomers(c, since), metric)
	// 排序基准与客户ID（新增/编号大的在前）：按 map 遍历顺序分页会翻页重复或漏人
	sort.Slice(matched, func(i, j int) bool { return matched[i].CustomerID > matched[j].CustomerID })

	resp := AIContributionDrillResp{
		Metric:     metric,
		Label:      analytics.ContributionMetricLabels[metric],
		PeriodDays: days,
		Since:      since.Format(time.RFC3339),
		Until:      until.Format(time.RFC3339),
		Total:      int64(len(matched)),
		Page:       page,
		PageSize:   size,
		Metrics:    analytics.ContributionDrillMetrics,
		Note:       ContributionDrillNote,
		List:       []AIContributionCustomerRow{},
	}
	if len(matched) == 0 {
		RespOK(c, "success", resp)
		return
	}

	start := (page - 1) * size
	if start >= len(matched) {
		RespOK(c, "success", resp) // 越界页只回空列表，total 仍如实（前端据此收回页码）
		return
	}
	end := start + size
	if end > len(matched) {
		end = len(matched)
	}
	pageRows := matched[start:end]

	ids := make([]uint, 0, len(pageRows))
	for _, m := range pageRows {
		ids = append(ids, m.CustomerID)
	}
	var customers []model.Customer
	db.RQ(c).Scopes(db.DataScope(c)).Model(&model.Customer{}).
		Select("id, name, phone, journey_stage, intent_score, interest_model, assigned_user_id").
		Where("id IN ?", ids).Find(&customers)
	custOf := make(map[uint]model.Customer, len(customers))
	for _, cu := range customers {
		custOf[cu.ID] = cu
	}

	// 顾问姓名一次取回（按页内 distinct 归属，避免逐行查）
	uidSet := make(map[uint]bool, len(customers))
	for _, cu := range customers {
		if cu.AssignedUserID > 0 {
			uidSet[cu.AssignedUserID] = true
		}
	}
	userNameOf := make(map[uint]string, len(uidSet))
	if len(uidSet) > 0 {
		uids := make([]uint, 0, len(uidSet))
		for id := range uidSet {
			uids = append(uids, id)
		}
		// 顾问走 RQ（tenant 作用域表），与 advisor 列表取归属人姓名的口径一致，勿用 PQ 旁路租户
		var users []model.User
		db.RQ(c).Model(&model.User{}).Select("id, real_name, username").Where("id IN ?", uids).Find(&users)
		for _, u := range users {
			name := u.RealName
			if name == "" {
				name = u.Username
			}
			userNameOf[u.ID] = name
		}
	}

	for _, m := range pageRows {
		cu, ok := custOf[m.CustomerID]
		if !ok {
			// 客户行缺失（注销/删除/不在本人数范围内）：不编造一行，也不因此改动 total——
			// total 是"命中数字"，list 是"此刻还能看到的明细"，两者本就可不等，前端如实展示。
			continue
		}
		served := "ai"
		if m.HasHuman {
			served = "human"
		}
		resp.List = append(resp.List, AIContributionCustomerRow{
			ID:               cu.ID,
			Name:             cu.Name,
			Phone:            cu.Phone,
			JourneyStage:     cu.JourneyStage,
			IntentScore:      cu.IntentScore,
			InterestModel:    cu.InterestProduct,
			AssignedUserID:   cu.AssignedUserID,
			AssignedUserName: userNameOf[cu.AssignedUserID],
			ServedBy:         served,
		})
	}
	RespOK(c, "success", resp)
}
