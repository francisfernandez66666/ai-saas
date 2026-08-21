package chatflow

import (
	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/mq"
	"ai-scrm/internal/service"
	"context"
	"fmt"
	"log"
	"math/rand"
	"regexp"
	"strings"
	"time"
)

// ============================================================
// 留资检测与 OneID 合并（Phase C 自 chat.go 下沉）
// 检测到手机号 → 自动标记为"已留资" + 分配默认顾问
// 业务规则：留资成功后顾问才能在顾问端看到客户信息和聊天记录
// ============================================================

// ============================================================
// 留资检测：从客户消息中提取手机号和姓名
// 检测到手机号 → 自动标记为"已留资" + 分配默认顾问
// 业务规则：留资成功后顾问才能在顾问端看到客户信息和聊天记录
// ============================================================
// DetectLeadCapture 留资检测 + OneID合并
// 返回值：0=未留资, -1=已留资但无需合并, >0=合并后的老客户ID（前端需切换）
func DetectLeadCapture(customerInput string, customer *model.Customer) int {
	// 正则匹配1开头11位手机号
	phoneRegex := regexp.MustCompile(`1[3-9]\d{9}`)
	phoneMatch := phoneRegex.FindString(customerInput)

	if phoneMatch == "" {
		return 0 // 没检测到手机号，不算留资
	}

	// ---- OneID合并：手机号匹配到老客户时，迁移所有数据 ----
	// 业务场景：访客A之前多次打开页面各创建了一个customer_id，某次留资给了手机号
	// 如果手机号匹配到老客户B，把访客A的聊天记录+标签+线索全迁移到B，删掉A
	mergedTargetID := MergeCustomerByPhone(customer, phoneMatch)
	if mergedTargetID > 0 {
		// 合并完成，重新加载老客户数据
		db.DB.First(customer, mergedTargetID)
		log.Printf("[留资检测-OneID] 访客合并到老客户%d，前端需切换customer_id", mergedTargetID)
		return int(mergedTargetID)
	}

	// 检测到手机号 → 更新客户信息（无合并场景）
	updates := map[string]interface{}{}

	// 更新手机号（如果客户记录还没有手机号）
	if customer.Phone == "" {
		updates["phone"] = phoneMatch
	}

	// 推进旅程阶段到"已留资"
	if customer.JourneyStage == "" || customer.JourneyStage == model.JourneyAIConnected || customer.JourneyStage == model.JourneyHumanConnected {
		updates["journey_stage"] = model.JourneyLeadCaptured
	}

	updates["assignment_reason"] = "lead_captured"

	// P3：留资行为上行事件 → CDP + 流程引擎（one_id 暂用客户占位键）
	if err := mq.Publish(context.Background(), mq.TopicUserEvent, customer.TenantID,
		fmt.Sprintf("c:%d", customer.ID), "lead_captured",
		mq.UserEvent{
			EventType:  "behavior",
			EventName:  "lead_captured",
			AnchorType: "phone",
			Attributes: map[string]any{"customer_id": customer.ID, "phone": phoneMatch},
			OccurredAt: time.Now(),
		}); err != nil {
		log.Printf("[MQ] lead_captured 事件发布失败: %v", err)
	}

	// 分配给当前客户数最少的顾问（轮询分配）
	// 业务规则：留资成功后自动分配，顾问端立即可见
	// 修复：原来硬编码assigned_user_id=1，改为选当前客户数最少的顾问
	if customer.AssignedUserID == 0 {
		var salesUsers []model.User
		db.DB.Where("role = ? AND status = 1 AND tenant_id = ?", "sales", customer.TenantID).Find(&salesUsers)
		if len(salesUsers) > 0 {
			minCount := -1
			var bestUserID uint = salesUsers[0].ID
			for _, u := range salesUsers {
				var count int64
				db.DB.Model(&model.Customer{}).Where("assigned_user_id = ? AND status = 1", u.ID).Count(&count)
				if minCount < 0 || int(count) < minCount {
					minCount = int(count)
					bestUserID = u.ID
				}
			}
			updates["assigned_user_id"] = bestUserID
		} else {
			updates["assigned_user_id"] = 2 // 兜底：无销售用户时默认分配2号（张伟）
		}
	}

	if len(updates) > 0 {
		db.DB.Model(customer).Updates(updates)
		// 同步更新内存中的customer对象
		if v, ok := updates["phone"]; ok {
			customer.Phone = v.(string)
		}
		if v, ok := updates["journey_stage"]; ok {
			customer.JourneyStage = v.(string)
		}
		if v, ok := updates["assigned_user_id"]; ok {
			customer.AssignedUserID = v.(uint)
		}
		log.Printf("[留资检测] 客户%d留资成功: phone=%s, stage=%v, assigned=%v",
			customer.ID, phoneMatch, updates["journey_stage"], updates["assigned_user_id"])
	}

	// 修复：留资成功后生成线索记录（已留资线索，分配给顾问）
	// 业务规则：已留资线索 → 人工接管 → 顾问端可见
	// 修复问题3：按客户ID合并线索——先查是否已有lead_captured类型的线索，有则更新不新建
	var existingFollowUp model.FollowUp
	result := db.DB.Where("customer_id = ? AND result = ?", customer.ID, "lead_captured").First(&existingFollowUp)
	if result.Error != nil {
		// 没有已有线索，创建新的
		leadFollowUp := model.FollowUp{
			CustomerID: customer.ID,
			UserID:     customer.AssignedUserID, // 归属顾问
			Type:       "ai_triggered",          // AI触发生成
			Method:     "store",                 // 到店渠道
			Content:    fmt.Sprintf("客户已留资，手机号:%s，触发来源:%s", phoneMatch, customerInput),
			Result:     "lead_captured", // 已留资线索
		}
		db.DB.Create(&leadFollowUp)
		log.Printf("[留资检测-线索生成] 客户%d 已留资线索已生成(FollowUp ID=%d)，分配顾问%d",
			customer.ID, leadFollowUp.ID, customer.AssignedUserID)
	} else {
		// 已有线索，更新内容（按客户ID合并，不新建）
		db.DB.Model(&existingFollowUp).Updates(map[string]interface{}{
			"content": fmt.Sprintf("客户已留资，手机号:%s，触发来源:%s", phoneMatch, customerInput),
			"user_id": customer.AssignedUserID, // 更新归属顾问
		})
		log.Printf("[留资检测-线索合并] 客户%d 已有线索(FollowUp ID=%d)，更新内容，不新建",
			customer.ID, existingFollowUp.ID)
	}

	// 通知顾问（当前简化为日志，后续可接WebSocket/邮件/飞书）
	log.Printf("[通知顾问] 顾问%d 有新的已留资线索：客户%d，手机号%s",
		customer.AssignedUserID, customer.ID, phoneMatch)

	return -1 // 已留资，无需合并
}

// IsLeadCaptured 判断客户是否已留资（lead_captured及以上阶段）
// 修复根因：客户已留资后，AI不应再注入到店追问策略，不应再问"留个手机号"
// 已留资 = journey_stage >= lead_captured（与状态机定义一致）
func IsLeadCaptured(customer *model.Customer) bool {
	if customer.JourneyStage == model.JourneyLeadCaptured ||
		customer.JourneyStage == model.JourneyArrived ||
		customer.JourneyStage == model.JourneyOrdered ||
		customer.JourneyStage == model.JourneyDelivered {
		return true
	}
	for _, tag := range customer.GetTags() {
		if tag == "已留资" {
			return true
		}
	}
	return false
}

// ============================================================

// MergeCustomerByPhone 访客留资时OneID合并
// 返回：合并后的目标客户ID，0表示不需要合并
func MergeCustomerByPhone(guestCustomer *model.Customer, phone string) uint {
	// 租户守卫（P2）：OneID 合并只在同一租户内进行，绝不通租
	// 以访客客户所属租户为锚；跨租户同号客户视为不同自然人
	tid := guestCustomer.TenantID

	// 查找同手机号的其他客户（排除自己，限定同租户）
	var existingCustomer model.Customer
	result := db.DB.Where("phone = ? AND id != ? AND status = 1 AND tenant_id = ?", phone, guestCustomer.ID, tid).First(&existingCustomer)
	if result.Error != nil {
		// 没有匹配的老客户，不需要合并
		return 0
	}

	log.Printf("[OneID合并] 检测到同手机号老客户: 访客%d → 老客户%d, 手机号=%s, 租户=%d",
		guestCustomer.ID, existingCustomer.ID, phone, tid)

	// 1. 迁移会话 → 老客户（租户守卫：只迁本租户数据）
	db.DB.Model(&model.Conversation{}).
		Where("customer_id = ? AND tenant_id = ?", guestCustomer.ID, tid).
		Update("customer_id", existingCustomer.ID)

	// 2. 迁移消息 → 老客户
	db.DB.Model(&model.Message{}).
		Where("customer_id = ? AND tenant_id = ?", guestCustomer.ID, tid).
		Update("customer_id", existingCustomer.ID)

	// 2.5 迁移线索(FollowUp) → 老客户
	db.DB.Model(&model.FollowUp{}).
		Where("customer_id = ? AND tenant_id = ?", guestCustomer.ID, tid).
		Update("customer_id", existingCustomer.ID)

	// 3.5 迁移试驾(TestDrive) → 老客户
	db.DB.Model(&model.TestDrive{}).
		Where("customer_id = ? AND tenant_id = ?", guestCustomer.ID, tid).
		Update("customer_id", existingCustomer.ID)

	// 3.6 迁移客户标签关联(customer_tags) → 老客户（去重）
	var guestTagRecords []model.CustomerTag
	db.DB.Where("customer_id = ? AND tenant_id = ?", guestCustomer.ID, tid).Find(&guestTagRecords)
	for _, gtr := range guestTagRecords {
		var count int64
		db.DB.Model(&model.CustomerTag{}).
			Where("customer_id = ? AND tag_id = ? AND tenant_id = ?", existingCustomer.ID, gtr.TagID, tid).
			Count(&count)
		if count == 0 {
			gtr.ID = 0
			gtr.CustomerID = existingCustomer.ID
			gtr.TenantID = tid
			db.DB.Create(&gtr)
		}
	}

	// 4. 合并标签（去重）
	existingTags := existingCustomer.GetTags()
	guestTags := guestCustomer.GetTags()
	mergedTags := existingTags
	tagSet := make(map[string]bool)
	for _, t := range existingTags {
		tagSet[t] = true
	}
	for _, t := range guestTags {
		if !tagSet[t] {
			mergedTags = append(mergedTags, t)
		}
	}
	if len(mergedTags) > 0 {
		existingCustomer.SetTags(mergedTags)
		tVector := existingCustomer.GetTVector()
		existingCustomer.SaveTVector(tVector)
	}

	// 5. 取最高旅程阶段
	guestStageOrder := model.JourneyStageOrder[guestCustomer.JourneyStage]
	existingStageOrder := model.JourneyStageOrder[existingCustomer.JourneyStage]
	if guestStageOrder > existingStageOrder {
		existingCustomer.JourneyStage = guestCustomer.JourneyStage
	}

	// 6. 合并意向分等数值（取更高值）
	if guestCustomer.IntentScore > existingCustomer.IntentScore {
		existingCustomer.IntentScore = guestCustomer.IntentScore
	}
	if guestCustomer.TrustLevel > existingCustomer.TrustLevel {
		existingCustomer.TrustLevel = guestCustomer.TrustLevel
	}

	// 7. 补充老客户缺失信息（访客有的字段老客户没有的）
	if existingCustomer.WechatID == "" && guestCustomer.WechatID != "" {
		existingCustomer.WechatID = guestCustomer.WechatID
	}
	if existingCustomer.Source == "" && guestCustomer.Source != "" {
		existingCustomer.Source = guestCustomer.Source
	}

	// 8. 保存老客户更新
	db.DB.Save(&existingCustomer)

	// 8.5 老客户无顾问时，轮询分配给当前客户最少的销售
	if existingCustomer.AssignedUserID == 0 {
		var salesUsers []model.User
		db.DB.Where("role = ? AND status = 1 AND tenant_id = ?", "sales", existingCustomer.TenantID).Find(&salesUsers)
		if len(salesUsers) > 0 {
			minCount := -1
			var bestUserID uint = salesUsers[0].ID
			for _, u := range salesUsers {
				var count int64
				db.DB.Model(&model.Customer{}).Where("assigned_user_id = ? AND status = 1", u.ID).Count(&count)
				if minCount < 0 || int(count) < minCount {
					minCount = int(count)
					bestUserID = u.ID
				}
			}
			db.DB.Model(&existingCustomer).Update("assigned_user_id", bestUserID)
			existingCustomer.AssignedUserID = bestUserID
			log.Printf("[OneID合并-分配顾问] 老客户%d 无顾问，轮询分配给顾问%d", existingCustomer.ID, bestUserID)
		} else {
			db.DB.Model(&existingCustomer).Update("assigned_user_id", 2)
			existingCustomer.AssignedUserID = 2
			log.Printf("[OneID合并-分配顾问] 老客户%d 无顾问，兜底分配给顾问2", existingCustomer.ID)
		}
	}

	// 9. 标记访客为无效（不物理删除，保留审计）
	db.DB.Model(guestCustomer).Update("status", 0)

	log.Printf("[OneID合并] 合并完成: 访客%d(status=0) → 老客户%d, 标签数=%d, 阶段=%s",
		guestCustomer.ID, existingCustomer.ID, len(mergedTags), existingCustomer.JourneyStage)

	return existingCustomer.ID
}

// ============================================================
// BuildCustomerContextSummary 构建客户核心信息摘要
// 修复问题7：关闭模型记忆后，用核心摘要替代完整对话历史注入
// 摘要内容：客户画像字段 + 最近消息中提取的关键信息
// 不注入完整对话历史（会偏移），只注入核心需求/兴趣/关注点
// ============================================================
func BuildCustomerContextSummary(customer *model.Customer, conversationID uint) string {
	var sb strings.Builder

	// 1. 客户画像字段（已知信息）
	// 硬编码：临时访客名（以"访客_"开头）不注入AI，AI回复中不能以"访客xxxx"称呼客户
	// 不知道真实姓名时，AI一律用"您好"开头
	customerKnownName := customer.Name
	if strings.HasPrefix(customerKnownName, "访客_") {
		customerKnownName = ""
	}
	if customerKnownName != "" {
		sb.WriteString(fmt.Sprintf("· 客户姓名：%s\n", customerKnownName))
	}
	if customer.Phone != "" {
		sb.WriteString(fmt.Sprintf("· 手机号：%s\n", customer.Phone))
	}
	if customer.InterestModel != "" {
		sb.WriteString(fmt.Sprintf("· 兴趣车型：%s\n", customer.InterestModel))
	}
	if customer.CurrentCar != "" {
		sb.WriteString(fmt.Sprintf("· 现在开的车：%s\n", customer.CurrentCar))
	}
	if customer.Budget > 0 {
		sb.WriteString(fmt.Sprintf("· 购车预算：%.0f万\n", customer.Budget))
	}
	if customer.Region != "" || customer.City != "" {
		sb.WriteString(fmt.Sprintf("· 地域：%s %s\n", customer.Region, customer.City))
	}
	if customer.Career != "" {
		sb.WriteString(fmt.Sprintf("· 职业：%s\n", customer.Career))
	}

	// 2. 标签（已打标签代表客户特征）
	tags := customer.GetTags()
	if len(tags) > 0 {
		sb.WriteString(fmt.Sprintf("· 客户标签：%s\n", strings.Join(tags, "、")))
	}

	// 3. 旅程阶段
	sb.WriteString(fmt.Sprintf("· 当前阶段：%s\n", customer.GetJourneyStageName()))
	if customer.JourneySubStage != "" {
		subName := "已试驾"
		if customer.JourneySubStage == model.SubStageQuoted {
			subName = "已报价"
		}
		sb.WriteString(fmt.Sprintf("· 到店子状态：%s\n", subName))
	}

	// 4. 最近3条客户消息（提取需求关键词，不是完整历史）
	// 只取最近3条客户消息中的内容，帮助AI理解当前对话焦点
	if conversationID > 0 {
		var recentMsgs []model.Message
		db.DB.Where("conversation_id = ? AND sender_type = ?", conversationID, "customer").
			Order("id DESC").Limit(3).Find(&recentMsgs)
		if len(recentMsgs) > 0 {
			sb.WriteString("· 最近客户说的话（按时间倒序）：\n")
			for i := len(recentMsgs) - 1; i >= 0; i-- {
				msg := recentMsgs[i]
				content := msg.Content
				if len(content) > 60 {
					content = content[:60] + "..."
				}
				sb.WriteString(fmt.Sprintf("  \"%s\"\n", content))
			}
		}
	}

	result := sb.String()
	if result == "" {
		return "暂无客户核心信息。"
	}
	return result
}

// ============================================================
// DetectKnowledgeBlindspot 检测AI回复是否触及知识库盲点
// 修复问题5：模型触及盲点后的不确定信号词检测 + 兜底话术
// 返回：空字符串=未检测到盲点，非空=兜底回复话术
// ============================================================
func DetectKnowledgeBlindspot(aiReply string, userInput string) string {
	// AI回复中的不确定信号词（模型在知识不足时的典型回复模式）
	blindspotSignals := []string{
		"我不太确定", "我不太清楚", "我不确定", "我不清楚",
		"目前没有确切信息", "无法给出具体", "暂时无法",
		"建议您咨询", "建议咨询", "建议联系",
		"具体信息请", "详情请咨询", "建议到店咨询",
		"我这边暂时", "我暂时无法", "我没有相关的",
		"需要进一步了解", "需要确认一下",
	}

	lowerReply := strings.ToLower(aiReply)
	for _, signal := range blindspotSignals {
		if strings.Contains(lowerReply, strings.ToLower(signal)) {
			// 检测到盲点信号，返回兜底话术
			// 模型可按相同句式轻度自由发挥，但核心是：
			// 1. 关闭引导式提问
			// 2. 说"好的，稍等，这个问题我查一下"
			blindspotReplies := []string{
				"好的，稍等，这个问题我查一下",
				"这个我得查查，稍等哈",
				"这个我不太确定，稍等，我帮你查一下",
			}
			randIdx := rand.Intn(len(blindspotReplies))
			return blindspotReplies[randIdx]
		}
	}
	return "" // 未检测到盲点
}

// ============================================================
// CountSimilarQuestions 检测客户重复提问次数
// 修复问题4b：相似问题超过阈值后，关闭反问引导式语句
// 判断逻辑：客户最近消息和之前消息中关键词重叠度>50%算相似
// ============================================================
func CountSimilarQuestions(customerID uint, currentInput string) int {
	var recentMsgs []model.Message
	db.DB.Where("customer_id = ? AND sender_type = ?", customerID, "customer").
		Order("id DESC").Limit(10).Find(&recentMsgs)

	if len(recentMsgs) <= 1 {
		return 0 // 只有当前消息，没有重复
	}

	// 当前消息的关键词（去停用词后）
	currentWords := ExtractKeywords(currentInput)
	if len(currentWords) == 0 {
		return 0
	}

	repeatCount := 0
	for _, msg := range recentMsgs[1:] { // 跳过最新的（可能是当前输入）
		pastWords := ExtractKeywords(msg.Content)
		if len(pastWords) == 0 {
			continue
		}

		// 计算关键词重叠度
		overlapCount := 0
		for _, w := range currentWords {
			for _, pw := range pastWords {
				if w == pw {
					overlapCount++
					break
				}
			}
		}

		overlapRate := float64(overlapCount) / float64(len(currentWords))
		if overlapRate > 0.5 {
			repeatCount++
		}
	}

	return repeatCount
}

// ============================================================
// ExtractKeywords 提取中文关键词（简单实现：去停用词+分字组词）
// ============================================================
func ExtractKeywords(text string) []string {
	// 简单停用词列表
	stopWords := map[string]bool{
		"的": true, "了": true, "是": true, "在": true, "我": true,
		"你": true, "他": true, "她": true, "吗": true, "吧": true,
		"啊": true, "呢": true, "哦": true, "嗯": true, "哈": true,
		"呀": true, "嘿": true, "哎": true, "来": true, "去": true,
		"过": true, "也": true, "就": true, "还": true, "都": true,
		"但": true, "而": true, "与": true, "或": true, "那": true,
		"这": true, "一个": true, "什么": true, "怎么": true,
		"为什么": true, "哪": true, "哪几个": true, "谁": true,
		"多少": true, "几": true, "想": true, "要": true,
		"能": true, "可以": true, "好": true, "不": true,
		"没": true, "有": true, "对": true, "说": true,
		"看": true, "给": true, "用": true, "把": true,
	}

	runes := []rune(text)
	words := make([]string, 0)

	// 简单2-gram分词（中文最常见的是2字词组）
	for i := 0; i < len(runes)-1; i++ {
		word := string(runes[i]) + string(runes[i+1])
		if !stopWords[word] && !stopWords[string(runes[i])] {
			words = append(words, word)
		}
	}
	// 单字也考虑（如果不是停用词且长度>1字）
	if len(runes) > 2 {
		for _, r := range runes {
			s := string(r)
			if !stopWords[s] && len([]rune(s)) == 1 {
				// 单字非停用词，只在2-gram不足时加入
			}
		}
	}

	return words
}

// ============================================================
// CountOffTopicRepeats 检测非车话题重复次数
// 修复问题6：非车话题重复3次后改语气
// 判断逻辑：客户最近多条消息都被IsOffTopic判定为非车话题
// ============================================================
func CountOffTopicRepeats(customerID uint) int {
	var recentMsgs []model.Message
	db.DB.Where("customer_id = ? AND sender_type = ?", customerID, "customer").
		Order("id DESC").Limit(10).Find(&recentMsgs)

	offtopicCount := 0
	for _, msg := range recentMsgs {
		if service.IsOffTopic(msg.Content) {
			offtopicCount++
		}
	}

	return offtopicCount
}

// CountTotalOffTopic 统计客户全部非车话题消息总数
func CountTotalOffTopic(customerID uint) int {
	var count int64
	db.DB.Model(&model.Message{}).
		Where("customer_id = ? AND sender_type = ?", customerID, "customer").
		Count(&count)
	var allMsgs []model.Message
	db.DB.Where("customer_id = ? AND sender_type = ?", customerID, "customer").
		Order("id ASC").Find(&allMsgs)
	offtopicCount := 0
	for _, msg := range allMsgs {
		if service.IsOffTopic(msg.Content) {
			offtopicCount++
		}
	}
	return offtopicCount
}

// CountConsecutiveOnTopic 统计客户最近连续车相关消息轮数
func CountConsecutiveOnTopic(customerID uint) int {
	var recentMsgs []model.Message
	db.DB.Where("customer_id = ? AND sender_type = ?", customerID, "customer").
		Order("id DESC").Limit(10).Find(&recentMsgs)
	count := 0
	for _, msg := range recentMsgs {
		if !service.IsOffTopic(msg.Content) {
			count++
		} else {
			break
		}
	}
	return count
}

// StripGuidedQuestions 硬拦截AI回复中的反问句
// 基础版：中文疑问语气词 + 问号
func StripGuidedQuestions(reply string) string {
	markers := []string{"吗", "呢", "吧", "？", "?"}
	cutIdx := -1
	for _, m := range markers {
		idx := strings.Index(reply, m)
		if idx >= 0 && (cutIdx < 0 || idx < cutIdx) {
			cutIdx = idx
		}
	}
	if cutIdx >= 0 {
		before := strings.TrimSpace(reply[:cutIdx])
		if before != "" {
			return before
		}
	}
	return reply
}

// StripAllQuestions 全面剥离AI回复中的所有疑问句（引导关闭后使用）
// 硬编码：覆盖更多中文反问模式，确保AI在引导关闭后不再反问客户
// 包括隐含反问：以"您"开头 + 询问性动词（有、是、需要、觉得、打算、考虑等）
func StripAllQuestions(reply string) string {
	// 1. 先走基础版剥离（语气词+问号）
	stripped := StripGuidedQuestions(reply)
	if stripped != reply {
		return stripped
	}
	// 2. 检查显式反问模式词
	questionPatterns := []string{
		"什么", "怎么", "哪儿", "哪些", "哪",
		"有没有", "是不是", "要不要", "能不能", "会不会",
		"是否", "可否", "能否", "有何",
		"特殊要求", "都差不多", "特殊需求",
	}
	for _, p := range questionPatterns {
		idx := strings.Index(reply, p)
		if idx >= 0 {
			before := strings.TrimSpace(reply[:idx])
			if before != "" {
				return before
			}
		}
	}

	trimmed := strings.TrimSpace(reply)

	// 3. 检查隐含反问句：以"您"+"[平时/平时/都/有/觉得/考虑/打算/需要/对]"开头
	// 这些模式几乎总是反问客户，不是陈述
	implicitQuestionPrefixes := []string{
		"您平时", "您都", "您有", "您觉得", "您考虑", "您打算", "您需要", "您对",
		"你平时", "你都", "你有", "你觉得", "你考虑", "你打算", "你需要", "你对",
	}
	for _, prefix := range implicitQuestionPrefixes {
		if strings.HasPrefix(trimmed, prefix) {
			// 尝试在第一个句号处切分
			sentEnd := strings.IndexAny(trimmed, "。")
			if sentEnd > 0 {
				before := strings.TrimSpace(trimmed[:sentEnd])
				if before != "" && !HasAnyPrefix(before, implicitQuestionPrefixes) {
					return before
				}
			}
			return ""
		}
	}

	// 4. 检查句末隐含求确认句："吧" "啊？" "嗯？"
	if strings.HasSuffix(trimmed, "吧") {
		before := strings.TrimSpace(trimmed[:len(trimmed)-3])
		if before != "" {
			return before
		}
	}
	return reply
}

// HasAnyPrefix 检查字符串是否以列表中的任一前缀开头
func HasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}
