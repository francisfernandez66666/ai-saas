// PIPL 数据可携带权（FIX-9，2026-09-27 审计批）：C 端本人自助拿自己的数据副本。
//
// 为什么单独写一份而不复用 /admin/export/*.csv：
//  1. **对象不同**：D7 导出是"商家经营分析用的群体名单"，所以手机号列必须掩码（pii.MaskPhone）；
//     可携带副本的对象**就是数据主体本人**，把"我自己填的手机号"掩码后再给我，等于这项权利没给。
//  2. **鉴权方向相反**：D7 走 JWTAuth+AdminRequired+DataScope（越权防线是"别看别人的"）；
//     本端点免登录，自证靠 visitor_key（与 /privacy/deletion-request 同一套身份口径），
//     越权防线是"匿名者只能拿自己那一份"。
//  3. **格式不同**：CSV 适合表格分析，不适合"换一个服务商继续用"（正文里的逗号/换行/公式注入
//     防护会把原文改成带前缀单引号的形态）。PIPL 要的是"通用且结构化的格式"，这里给 JSON。
//
// 红线：本端点**只读**，且投影一律手写字面量键——绝不对 model.Customer/model.Conversation/
// model.Message 整行 json.Marshal。那三个结构里有商家侧不该交给客户的东西
// （customers.remark 是销售对客户的内部评价、customers.intent_score 是商家给这个人打的排序分、
// t_vector 是策略引擎中间产物、
// conversations.state_json 是会话状态机全量、messages.template_id/anchor_type/route_result
// 是话术投放事实、assigned_user_id 是员工身份、visitor_key 是凭证）。
// 用结构体直出＝"以后加字段自动泄露"，所以这里没有结构体，只有一份显式清单。
package api

import (
	"log"
	"net/http"
	"sort"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
)

// portableRowCap 单次请求最多回传的消息条数。
// 为什么不"全量一次给完"：长周期行业 2~3 年的会话可以有几十万条，一次 JSON 给完会
// 把响应憋在网关超时里（半截 JSON 客户拿到也用不了）。所以给定长配额 + 游标续取，
// 并且**截断时必须如实回显 truncated/next_after_id**——静默截断的个人数据副本比不给更糟。
const portableRowCap = 5000

// portableRow 消息流的一行（热表与归档表共用同一结构，archived 标明这条来自冷数据表）。
type portableRow struct {
	ID             uint      `json:"-"` // 仅用于排序/游标与来源标记，不进响应体（响应体手写投影）
	ConversationID uint      `json:"-"`
	SenderType     string    `json:"-"`
	Content        string    `json:"-"`
	MessageType    string    `json:"-"`
	CreatedAt      time.Time `json:"-"`
	Archived       bool      `json:"-"`
}

// fetchPortableRows 从一张消息表取 id>afterID 的最多 n 条（按 id 升序）。
// 排序键选 id 的前提（不是"顺手"，是这条链路的正确性依赖）：
//   - 热表与归档表用**各自的序列**，同 id 在两表并存是可能的（生产搬移保留原主键所以不并存，
//     但没有任何约束保证这一点，测试直插更会造出重号）；
//   - created_at 更不行：归档行与热行可同秒，游标在同一时刻上二义，续取要么重要么漏。
//
// 因此两表合并后**必须显式排序**（见调用处 sort.Slice）：Go 的 append 序是"messages 全部 +
// archive 全部"，不是 id 序；漏掉这一步，游标会在"热表最后一条 id 大于归档某条 id"时漏行。
// 用例 TestPortabilityCursor 盯的就是这个：分页取回的集合必须与种子集合逐条相等且无重复。
func fetchPortableRows(gdb *gorm.DB, table string, customerID, afterID uint, n int) ([]portableRow, error) {
	if n <= 0 {
		return nil, nil
	}
	var rows []portableRow
	err := gdb.Table(table).
		Select("id", "conversation_id", "sender_type", "content", "message_type", "created_at").
		Where("customer_id = ? AND id > ?", customerID, afterID).
		Order("id ASC").Limit(n).
		Scan(&rows).Error
	return rows, err
}

// PrivacyMyData POST /privacy/my-data {customer_id, visitor_key?, after_id?, limit?}
// apidump:ts PrivacyMyDataResp
// 返回本人数据副本：身份与画像（对本人不掩码）+ 会话清单 + 消息流（含冷归档）。
// 鉴权口径与删除权受理完全一致：匿名必须 visitor_key 与目标客户一致；登录态必须该客户
// 落在本人数据范围内（删除权那边 B8 已把"登录即放行且不看归属"封掉，这里同一判据）。
func PrivacyMyData(c *gin.Context) {
	var req struct {
		CustomerID uint   `json:"customer_id"`
		VisitorKey string `json:"visitor_key"`
		AfterID    uint   `json:"after_id"`
		Limit      int    `json:"limit"`
	}
	_ = c.ShouldBindJSON(&req)

	tid := db.EffectiveTenantIDFromGin(c)
	if tid == 0 {
		RespErr(c, http.StatusBadRequest, 400, "缺少租户上下文")
		return
	}
	if req.CustomerID == 0 {
		RespErr(c, http.StatusBadRequest, 400, "customer_id 必填")
		return
	}

	// ① 身份闸：先认人再取数（顺序反了会把几十万行读进内存才被拒）。
	//    跨租户/不存在的客户统一 404 不回显差异，与删除权一致。
	var cust model.Customer
	if err := db.RQ(c).Where("id = ?", req.CustomerID).First(&cust).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "客户不存在")
		return
	}
	_, logged := c.Get("user_id")
	if !logged {
		if req.VisitorKey == "" || !hashEqual(cust.VisitorKey, req.VisitorKey) {
			RespErr(c, http.StatusForbidden, 403, "visitor_key 校验失败")
			return
		}
	} else if !customerInDataScope(c, cust.AssignedUserID) {
		RespErr(c, http.StatusForbidden, 403, "该客户不在你的数据范围内，无权导出本人副本")
		return
	}

	// ② 本次配额：只允许调低。?limit=99999999 能绕过定长配额就不是"客户自助"该有的口子。
	limit := portableRowCap
	if req.Limit > 0 && req.Limit < limit {
		limit = req.Limit
	}

	// ③ 消息流：热表 + 冷归档表**都要读**。只读 messages 的话，message_archive_days 一开，
	//    客户的副本就少一截历史且没有任何地方报错——这是"静默不完整"，比不给更糟。
	//
	//    句柄纪律（AGENTS.md 红线）：`db.RQ(c)` 是 clone=0 句柄，同一句柄连着跑第二条查询会把
	//    第一条的 Where/Model 一起带进去（outreach 批的 42703 恒 500 就是这么来的）。
	//    所以循环里**每跑一条查询就重新取一次 db.RQ(c)**，绝不把句柄提到循环外复用。
	//
	//    每表各取 limit+1 条（不是 limit 条）：多要那一条**不返回**，只用来判断"这张表后面还有没有"。
	//    若只取 limit 条，则"热表还有第 limit+1 条"这种情况在合并后看不出来——归档表这一轮
	//    可能一条都没有，merged 长度恰好等于 limit，于是 truncated=false，游标就此停住，
	//    客户拿到的是一份"看起来给完了、其实少了尾巴"的副本。这属于静默不完整，比不给更糟。
	var merged []portableRow
	for _, src := range []struct {
		table    string
		archived bool
	}{
		{table: "messages", archived: false},
		{table: "messages_archive", archived: true},
	} {
		rows, err := fetchPortableRows(db.RQ(c), src.table, req.CustomerID, req.AfterID, limit+1)
		if err != nil {
			// 归档表缺失/查询故障都**不返回半成品**：一份"看起来完整、其实少了一半"的个人数据副本
			// 会被客户当成全量交给另一个服务商，缺陷就转嫁给了当事人。
			log.Printf("[PIPL可携带] 租户%d 客户%d 读取 %s 失败: %v", tid, req.CustomerID, src.table, err)
			RespErr(c, http.StatusInternalServerError, 500, "副本读取失败，请稍后重试")
			return
		}
		for i := range rows {
			rows[i].Archived = src.archived
		}
		merged = append(merged, rows...)
	}
	// 先排序、后截断——顺序反过来就是丢数据：append 序是"热表全部 + 归档全部"，
	// 而归档行是**更早**的消息，生产搬移保留原主键所以它们的 id 普遍**小于**热表行。
	// 按 append 序砍尾巴，砍掉的正是这批最小的 id，而 next_after_id 又取自砍完之后的最大值，
	// 于是游标直接跳过那批小 id 且永不回头（用例 TestPortabilityCursor 首跑即抓到：
	// 种子三条 8360(归档)/8361/8362，limit=2 时第一页回 [8361,8362]、第二页空，8360 永久丢失）。
	sort.Slice(merged, func(i, j int) bool { return merged[i].ID < merged[j].ID })
	truncated := len(merged) > limit
	if truncated {
		merged = merged[:limit]
	}

	msgList := make([]gin.H, 0, len(merged))
	var nextID uint
	for i := range merged {
		r := &merged[i]
		nextID = r.ID
		msgList = append(msgList, gin.H{
			"id":              r.ID,
			"conversation_id": r.ConversationID,
			"sender_type":     r.SenderType,
			"content":         r.Content, // 本人副本不掩码：掩码会得到一份"我自己说的话被改掉"的假副本
			"message_type":    r.MessageType,
			"archived":        r.Archived,
			"created_at":      r.CreatedAt,
		})
	}

	var convs []model.Conversation
	if err := db.RQ(c).Where("customer_id = ?", req.CustomerID).Order("id ASC").Limit(portableRowCap).
		Find(&convs).Error; err != nil {
		RespErr(c, http.StatusInternalServerError, 500, "副本读取失败，请稍后重试")
		return
	}
	convList := make([]gin.H, 0, len(convs))
	for i := range convs {
		cv := &convs[i]
		convList = append(convList, gin.H{
			"id":              cv.ID,
			"status":          cv.Status,
			"channel":         cv.Channel,
			"created_at":      cv.CreatedAt,
			"updated_at":      cv.UpdatedAt,
			"last_message_at": cv.LastMessageAt,
		})
	}

	// 留痕但不落敏感值：visitor_key 是凭证，绝不进日志（本行只有租户/客户/条数/游标）。
	log.Printf("[PIPL可携带] 租户%d 客户%d 导出本人副本 %d 条消息（游标起点=%d 截断=%v 登录态=%v）",
		tid, req.CustomerID, len(msgList), req.AfterID, truncated, logged)

	RespOK(c, "ok", gin.H{
		"generated_at": time.Now().Format(time.RFC3339),
		// 显式清单：新增客户列不会自动出现在副本里，必须在这里点名——
		// 反方向也一样（商家内部字段不会因为"模型加了列"就泄露给客户）。
		"customer": gin.H{
			"id":               cust.ID,
			"name":             cust.Name,
			"phone":            cust.Phone, // 明文：数据主体本人的号码
			"wechat_id":        cust.WechatID,
			"gender":           cust.Gender,
			"age":              cust.Age,
			"city":             cust.City,
			"region":           cust.Region,
			"career":           cust.Career,
			"customer_type":    cust.CustomerType,
			"interest_product": cust.InterestProduct,
			"source":           cust.Source,
			"acquisition_code": cust.AcquisitionCode,
			"external_user_id": cust.ExternalUserID,
			"journey_stage":    cust.JourneyStage,
			"tags":             cust.Tags,
			// intent_score **不在清单里**（冒烟 §四十六 首跑抓到的，本批实测）：
			// 划法不是"敏感就不给"，而是"是谁产生的"——tags/journey_stage 是客户自己参与出来的
			// 归类与流程位置（他自己说过要什么、走到哪一步），可携带副本该带上；
			// 而意向分是**商家侧给这个人打的排序分**，与 remark 同族（销售判断），
			// 交出去既没有迁移价值，又等于把内部评分口径向当事人公开。
			// 同一族里还有 t_vector（策略中间产物）、visitor_key（凭证）、assigned_user_id（员工身份）。
			"created_at": cust.CreatedAt,
			"updated_at": cust.UpdatedAt,
		},
		"conversations": convList,
		"messages":      msgList,
		"page": gin.H{
			"row_cap":       portableRowCap,
			"limit":         limit,
			"after_id":      req.AfterID,
			"next_after_id": nextID,
			"truncated":     truncated,
		},
	})
}
