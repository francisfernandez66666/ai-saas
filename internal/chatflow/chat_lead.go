// Package chatflow 聊天流模块：延迟取消/留资检测与 OneID 合并/会话状态维护/业务驱动消费
package chatflow

import "ai-scrm/internal/notify"

import "ai-scrm/internal/pii"

import (
	"ai-scrm/internal/cdp"
	"ai-scrm/internal/db"
	"ai-scrm/internal/logx"
	"ai-scrm/internal/model"
	"ai-scrm/internal/mq"
	"ai-scrm/internal/service"
	"context"
	"fmt"
	"log"
	"math/rand"
	"regexp"
	"strings"

	"gorm.io/gorm"
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

// PhoneRegex 手机号匹配正则（P1-27 修复：抽包级变量，三处留资路径共用）
// I1修复(2026-08-26)：加单词边界\b，避免长数字串子串误命中（如订单号/身份证片段误触发真分配顾问+企微推送）
var PhoneRegex = regexp.MustCompile(`\b1[3-9]\d{9}\b`)

// RoundsHintLeadCaptured 留资成功后引导式反问轮数（两入口统一语义）
// DetectLeadCapture 内部写库后，调用方应刷新内存 customer 以同步 journey_stage（P1-23）
// DetectLeadCapture 留资检测 + OneID合并
// 返回值：0=未留资, -1=已留资但无需合并, >0=合并后的老客户ID（前端需切换）
// 2026-09-28 拆分说明：原 157 行函数按 2026-09-22 chatSessionCtx 先例拆为 chat_lead_split.go 的
// 步骤函数（publishLeadCapturedUserEvent / assignSalesWithFewestCustomers / applyLeadCapturedUpdates /
// upsertLeadCapturedFollowUp），本函数只剩编排——判据、条件顺序、SQL、错误分支、文案逐字未动。
// 留资两分支口径不变：已留资（本函数落 phone/阶段/顾问）→ 人工接管+分配顾问；未留资 → AI 接管不给顾问；
// 人工锁定态裁决仍单点在 HumanTakeoverDecide，本函数不旁路。
func DetectLeadCapture(customerInput string, customer *model.Customer) int {
	phoneMatch := PhoneRegex.FindString(customerInput)

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
	publishLeadCapturedUserEvent(customer, phoneMatch)

	// 分配给当前客户数最少的顾问（轮询分配）
	// 业务规则：留资成功后自动分配，顾问端立即可见
	if customer.AssignedUserID == 0 {
		if bestUserID, found := assignSalesWithFewestCustomers(db.DB, customer.TenantID); found {
			updates["assigned_user_id"] = bestUserID
		} else {
			// I3修复(2026-08-26)：无可用顾问时不写死跨租户脏值(uint(2))，
			// 改为 assigned_user_id=0，由后台人工池认领
			updates["assigned_user_id"] = uint(0)
		}
	}

	if len(updates) > 0 {
		// FIX-2（2026-09-29 审计批）：画像落库失败 = 本次留资没有发生。
		// 旧写法吞掉错误继续往下——线索生成、顾问通知日志、企微群推"新留资"、
		// 流程回流全部建立在一行没写进去的更新上，商户收到假事件而真线索蒸发。
		// 现在提前返回 0（未留资）：客户下一句再带同号会重新走检测（同字段重写幂等），
		// 失败本身已有 ERROR 日志 + ai_scrm_lead_capture_write_fail_total 计数可见。
		if err := applyLeadCapturedUpdates(customer, phoneMatch, updates); err != nil {
			return 0
		}
	}

	// 修复：留资成功后生成线索记录（已留资线索，分配给顾问）
	upsertLeadCapturedFollowUp(customer, phoneMatch, customerInput)

	// 通知顾问（当前简化为日志，后续可接WebSocket/邮件/飞书）
	log.Printf("[通知顾问] 顾问%d 有新的已留资线索：客户%d，手机号%s",
		customer.AssignedUserID, customer.ID, logx.Mask(phoneMatch))

	// 商业化批次一顺手做（2026-08-23）：留资成功 → 企微群机器人推送
	// SCRM 最高价值触达：销售群实时收到"新留资线索"通知（手机号脱敏）
	notify.NotifyLeadCaptured(customer.Name, maskPhone(phoneMatch), customer.InterestProduct)

	// Phase C（2026-08-22）：业务结果回流 → 推进流程主干（编排层消费 flow_result）
	// 注意：不直接 import flow 包（会形成 chatflow→flow→strategy→llm→chatflow 环），
	// 本地查在途实例 + mq 发布，消费者在 flow 包
	publishFlowResult(customer.TenantID, customer.ID, "lead_captured",
		map[string]any{"customer_id": customer.ID, "phone_masked": maskPhone(phoneMatch)})

	return -1 // 已留资，无需合并
}

// publishFlowResult 业务结果回流发布（chatflow 本地版，避免循环依赖）
func publishFlowResult(tenantID uint, customerID uint, nodeResult string, detail map[string]any) {
	var inst model.FlowInstance
	err := db.DB.Where("tenant_id = ? AND customer_id = ? AND status = ?",
		tenantID, customerID, "running").Order("id DESC").First(&inst).Error
	if err != nil {
		return // 无在途实例：结果无需驱动流程
	}
	oneID := cdp.ResolveOneID(tenantID, customerID)
	if err := mq.Publish(context.Background(), mq.TopicFlowResult, tenantID, oneID,
		nodeResult, mq.FlowResultEvent{
			InstanceID: inst.ID,
			NodeID:     inst.CurrentNodeID,
			Result:     nodeResult,
			Detail:     detail,
		}); err != nil {
		log.Printf("[回流] flow_result 发布失败: %v", err)
	}
}

// maskPhone 手机号脱敏（回流事件 detail 不带明文手机号）
func maskPhone(p string) string {
	if len(p) != 11 {
		return p
	}
	return p[:3] + "****" + p[7:]
}

// IsLeadCaptured 判断客户是否已留资（lead_captured及以上阶段）
// 修复根因：客户已留资后，AI不应再注入到店追问策略，不应再问"留个手机号"
// 已留资 = journey_stage >= lead_captured（与状态机定义一致）
func IsLeadCaptured(customer *model.Customer) bool {
	if customer == nil {
		return false
	}
	// FIX-9(2026-09-27)：阶段腿改问 CapturedStage——同一个四阶段白名单此前在这里是第五份手抄。
	if CapturedStage(customer.JourneyStage) {
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
// 2026-09-28 拆分说明：原 240 行函数按 chatSessionCtx 先例拆为 chat_lead_split.go 的 mergeXxx 步骤函数，
// 本函数只剩编排。所有步骤函数只接收下面 db.DB.Transaction 闭包的同一个 tx 句柄——
// 「锁定读 → G1 预收拢 → 迁移归属 → 字段级保存 → 收拢 active」仍在一个事务内按原顺序执行，
// 预收拢与迁移的同事务性（013 ux_conv_one_active 护栏）没有被拆散。
func MergeCustomerByPhone(guestCustomer *model.Customer, phone string) uint {
	// 租户守卫（P2）：OneID 合并只在同一租户内进行，绝不通租
	// 以访客客户所属租户为锚；跨租户同号客户视为不同自然人
	tid := guestCustomer.TenantID

	// D2 修复(2026-09-16B，AUDIT_UAT_VERIFY_2026-09-16B)：1~9 步收进单事务——旧实现
	// 9 条独立语句无事务，中途任一条失败即"半合并"脑裂（消息挂老客户、访客仍有效、
	// identity 指错人），且全部走 db.DB 吞 error 无从感知。失败整体回滚并返回 0
	// （调用方按"未合并"继续走访客留资流程，数据自洽，宁可不合并不可半合并）。
	// C7 红线合规：事务内唯一的 Create（customer_tags）已显式设 TenantID，update 全部带 tenant_id 条件。
	// P1-3 修复(2026-09-19 审计批二)：老客户读取由事务外 db.DB 裸读（读→存窗口 stale 覆写
	// 并发接管态/顾问分配，且同手机号并发双留资各合并一次产生双胞胎）挪进事务内
	// FOR UPDATE（对照 billing_refund.go 行锁范式）——读与写之间行锁持有，串行化。
	var survivorID uint
	var existingCustomer model.Customer
	txErr := db.DB.Transaction(func(tx *gorm.DB) error {
		survivor, err := lockMergeSurvivor(tx, guestCustomer, phone, tid)
		if err != nil {
			return err
		}
		if survivor == nil {
			return nil // 无匹配老客户（含锁等窗口内已被并发合并）：不合并，空转成功
		}
		existingCustomer = *survivor
		survivorID = existingCustomer.ID // 提前登记；失败回滚时整体作废
		log.Printf("[OneID合并] 检测到同手机号老客户: 访客%d → 老客户%d, 手机号=%s, 租户=%d",
			guestCustomer.ID, existingCustomer.ID, logx.Mask(phone), tid)

		// G1 预收拢（必须排在迁移之前，见 mergePreactiveConversations 注释）
		if err := mergePreactiveConversations(tx, guestCustomer, &existingCustomer, tid); err != nil {
			return err
		}

		// 1~3.5 迁移会话/消息/线索/试驾 → 老客户
		if err := mergeMigrateOwnedRows(tx, guestCustomer, &existingCustomer, tid); err != nil {
			return err
		}

		// 3.6 迁移客户标签关联（去重，事务内唯一 Create）
		if err := mergeMigrateCustomerTags(tx, guestCustomer, &existingCustomer, tid); err != nil {
			return err
		}

		// 4~7 内存合并：标签集/T向量、最高阶段、取高数值、补缺字段（写库统一在第8步）
		mergeTagStringsInMemory(&existingCustomer, guestCustomer)
		mergeCombineProfileFields(&existingCustomer, guestCustomer)

		// 8. 保存老客户更新（字段级 Updates，列集合冻结——勿合并回整行 Save）
		if err := mergeSaveSurvivorProfile(tx, &existingCustomer, tid); err != nil {
			return err
		}

		// 8.5 老客户无顾问时轮询分配
		if err := mergeAssignSurvivorSales(tx, &existingCustomer); err != nil {
			return err
		}

		// 9. 标记访客为无效（不物理删除，保留审计）
		if err := tx.Model(guestCustomer).Update("status", 0).Error; err != nil {
			return fmt.Errorf("访客置无效: %w", err)
		}

		// 9.2 合并后收拢 active 会话（与开头预收拢成对，见 mergeCollapseSurvivorActives 注释）
		if err := mergeCollapseSurvivorActives(tx, &existingCustomer, tid); err != nil {
			return err
		}

		return nil
	})
	if txErr != nil {
		log.Printf("[OneID合并-告警] 合并事务失败已整体回滚(访客%d→老客户%d,手机号=%s): %v",
			guestCustomer.ID, existingCustomer.ID, logx.Mask(phone), txErr)
		return 0
	}
	if survivorID == 0 {
		return 0 // 锁内复查未命中：不合并，后续 CDP/identity 旁路一步都不能跑
	}

	// 9.5 CDP 锚点重指向（Phase B，2026-08-22）：
	// 访客的 phone 锚点若已映射到访客 OneID(c:{guestID})，重指向 canonical 老客户 OneID
	// 保证 ResolveOneID 查 phone 锚点必得 canonical，画像/事件/状态表分片键统一
	cdp.RepointAnchor(tid, "phone", phone,
		fmt.Sprintf("c:%d", guestCustomer.ID),
		fmt.Sprintf("c:%d", survivorID))

	// 9.6 L3：身份标识落库（增量补 identity，不重写现有合并逻辑）
	// 把合并双方(访客+老客户)的手机号/微信号等身份锚点写入 customer_identities，
	// 统一指向最终保留的老客户(identity.customer_id=survivorID)，
	// 并清理被合并访客遗留的 identity 行。D2 后置于主事务成功之后（自身幂等 upsert+独立事务）。
	if err := persistMergedIdentities(tid, &existingCustomer, guestCustomer); err != nil {
		log.Printf("[OneID合并-告警] 身份标识落库失败(不影响主流程): %v", err)
	}

	log.Printf("[OneID合并] 合并完成: 访客%d(status=0) → 老客户%d, 标签数=%d, 阶段=%s",
		guestCustomer.ID, survivorID, len(existingCustomer.GetTags()), existingCustomer.JourneyStage)

	return survivorID
}

// persistMergedIdentities L3：OneID 合并时把双方身份锚点统一落 customer_identities
// 规则：
//   - 取双方(老客户+访客)的非空身份(phone/wechat)，逐条 upsert 到 customer_identities，
//     customer_id 统一指向 survivor（最终保留的自然人），冲突(同租户同类型同值)说明已归属
//     survivor，忽略即可；
//   - 删除被合并访客(guest)遗留的全部 identity 行（避免脏锚点指向无效客户）。
//
// 用 db.DB.Transaction 包裹（参照 I2 事务化改法），任一步失败整体回滚，不污染身份表。
func persistMergedIdentities(tid uint, survivor, guest *model.Customer) error {
	// 收集双方身份锚点（类型→值），去空
	identities := map[string]string{}
	add := func(typ, val string) {
		if val == "" {
			return
		}
		identities[typ] = val
	}
	add(model.IdentityTypePhone, survivor.Phone)
	add(model.IdentityTypeWechat, survivor.WechatID)
	add(model.IdentityTypePhone, guest.Phone)
	add(model.IdentityTypeWechat, guest.WechatID)

	return db.DB.Transaction(func(tx *gorm.DB) error {
		for typ, val := range identities {
			// upsert：冲突(uniq_tenant_identity)说明该锚点已归属 survivor，忽略
			var cnt int64
			if err := tx.Model(&model.CustomerIdentity{}).
				Where("tenant_id = ? AND identity_type = ? AND identity_value = ?", tid, typ, val).
				Count(&cnt).Error; err != nil {
				return err
			}
			if cnt > 0 {
				continue
			}
			rec := model.CustomerIdentity{
				TenantID:      tid,
				CustomerID:    survivor.ID,
				IdentityType:  typ,
				IdentityValue: val,
				Verified:      typ == model.IdentityTypePhone, // 留资手机号视为已验证
			}
			if err := tx.Create(&rec).Error; err != nil {
				return err
			}
		}
		// 清理被合并访客遗留的 identity 行
		if err := tx.Where("tenant_id = ? AND customer_id = ?", tid, guest.ID).
			Delete(&model.CustomerIdentity{}).Error; err != nil {
			return err
		}
		return nil
	})
}

// ============================================================
// BuildCustomerContextSummary 构建客户核心信息摘要
// 修复问题7：关闭模型记忆后，用核心摘要替代完整对话历史注入
// 摘要内容：客户画像字段 + 最近消息中提取的关键信息
// 不注入完整对话历史（会偏移），只注入核心需求/兴趣/关注点
// ============================================================
// BuildCustomerContextSummary 组装客户上下文摘要，供 AI 生成回复与顾问查看。
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
		sb.WriteString(fmt.Sprintf("· 手机号：%s\n", pii.MaskPhone(customer.Phone)))
	}
	if customer.InterestProduct != "" {
		sb.WriteString(fmt.Sprintf("· 兴趣产品：%s\n", customer.InterestProduct))
	}
	if customer.CurrentProduct != "" {
		sb.WriteString(fmt.Sprintf("· 当前在用产品：%s\n", customer.CurrentProduct))
	}
	if customer.Budget > 0 {
		sb.WriteString(fmt.Sprintf("· 预算：%.0f万\n", customer.Budget))
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

	// 5. 历史交互语义焦点（增强：让 AI 知道对话走到哪了，避免重复引导）
	// 取最近 6 条（客户+AI 混合）消息，提炼意图信号与最近一次 AI 回复
	if conversationID > 0 {
		var recentAll []model.Message
		db.DB.Where("conversation_id = ?", conversationID).
			Order("id DESC").Limit(6).Find(&recentAll)
		if len(recentAll) > 0 {
			intentFlags := map[string]bool{}
			for i := len(recentAll) - 1; i >= 0; i-- {
				t := strings.ToLower(recentAll[i].Content)
				switch {
				case strings.Contains(t, "试驾") || strings.Contains(t, "试乘"):
					intentFlags["已提及试驾"] = true
				case strings.Contains(t, "置换") || strings.Contains(t, "旧车") || strings.Contains(t, "二手车"):
					intentFlags["已提及置换"] = true
				case strings.Contains(t, "金融") || strings.Contains(t, "贷款") || strings.Contains(t, "分期") || strings.Contains(t, "首付"):
					intentFlags["已提及金融"] = true
				case strings.Contains(t, "优惠") || strings.Contains(t, "折扣") || strings.Contains(t, "便宜"):
					intentFlags["已提及议价"] = true
				}
			}
			if len(intentFlags) > 0 {
				keys := make([]string, 0, len(intentFlags))
				for k := range intentFlags {
					keys = append(keys, k)
				}
				sb.WriteString("· 历史交互焦点：" + strings.Join(keys, "、") + "\n")
			}
			// 最近一次 AI 回复摘要（避免 AI 重复说刚说过的话）
			for _, m := range recentAll {
				if m.SenderType == "ai" || m.SenderType == "bot" || m.SenderType == "advisor" {
					last := m.Content
					if len(last) > 50 {
						last = last[:50] + "..."
					}
					sb.WriteString(fmt.Sprintf("· AI 最近一次回复：%s\n", last))
					break
				}
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
// DetectKnowledgeBlindspot 识别 AI 回复中的知识盲区提示。
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
// CountSimilarQuestions 统计客户相似问题出现次数，用于重复咨询判断。
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
// ExtractKeywords 从文本中提取关键词。
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
	// P3-12 修复：单字分支原为空体 if（注释"2-gram 不足时加入"但无实现），删除死代码。
	return words
}

// ============================================================
// CountOffTopicRepeats 检测非车话题重复次数
// 修复问题6：非车话题重复3次后改语气
// 判断逻辑：客户最近多条消息都被IsOffTopic判定为非车话题
// ============================================================
// CountOffTopicRepeats 统计客户重复离题次数。
func CountOffTopicRepeats(customerID uint) int {
	var recentMsgs []model.Message
	db.DB.Where("customer_id = ? AND sender_type = ?", customerID, "customer").
		Order("id DESC").Limit(10).Find(&recentMsgs)

	offtopicCount := 0
	for _, msg := range recentMsgs {
		if service.IsOffTopicForTenant(msg.TenantID, msg.Content) {
			offtopicCount++
		}
	}

	return offtopicCount
}

// CountTotalOffTopic 统计客户全部非车话题消息总数
// P2-61 修复(2026-09-09)：原全历史载入进内存跑关键词（O(N) 内存+计算）；
// 业务只在"胡搅蛮缠"判定用（阈值>10 且连续在话题<3），收敛到最近 500 条足够，
// 超长历史客户不再每轮回复拖全表进内存。
func CountTotalOffTopic(customerID uint) int {
	var allMsgs []model.Message
	db.DB.Where("customer_id = ? AND sender_type = ?", customerID, "customer").
		Order("id DESC").Limit(500).Find(&allMsgs)
	offtopicCount := 0
	for _, msg := range allMsgs {
		if service.IsOffTopicForTenant(msg.TenantID, msg.Content) {
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
		if !service.IsOffTopicForTenant(msg.TenantID, msg.Content) {
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
