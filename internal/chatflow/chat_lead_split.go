// 本文件是 chat_lead.go 两个超长函数的拆分步骤（2026-09-28，纯结构重构，行为零变化）：
// DetectLeadCapture（留资检测）与 MergeCustomerByPhone（OneID 合并）原各 157/240 行，
// 按 2026-09-22 chat.go→chatSessionCtx 阶段方法的先例做"剪切-粘贴"式机械拆分——
// 条件顺序、SQL、错误分支、日志文案逐字保留。
// 关键约束：MergeCustomerByPhone 的各步骤函数只接收主函数 db.DB.Transaction 闭包传入的
// 同一个 tx 句柄——「预收拢 → 迁移 → 改 customer_id」仍在这一个事务内按原顺序执行，
// G1/迁移013（ux_conv_one_active）的顺序与同事务性没有被拆散。
package chatflow

import (
	"ai-scrm/internal/attribution"
	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/mq"
	"ai-scrm/internal/pii"
	"ai-scrm/internal/webhook"
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ============================================================
// DetectLeadCapture 拆分段（留资检测：无合并分支）
// ============================================================

// publishLeadCapturedUserEvent 留资 P3 上行事件发布。
// 单独成段原因：这是 mq 发布侧的旁路动作（失败只打日志不阻断留资），与写库分支无关，混在主函数里会淹没留资判据主线。
func publishLeadCapturedUserEvent(customer *model.Customer, phoneMatch string) {
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
}

// assignSalesWithFewestCustomers 轮询挑当前客户数最少的在册销售（留资主路径，db.DB 句柄）。
// 单独成段原因：这是"已留资→分配顾问"分支的核心判据。OneID 合并路径另有一个走 tx 的同型变体
// （mergeAssignSurvivorSales）——两者错误分支不同（本函数吞查询失败走置零兜底，tx 版必须报错回滚整笔合并），
// 行为零变化前提下刻意不合并成一个，防止"复用"改变错误语义。
func assignSalesWithFewestCustomers(gdb *gorm.DB, tenantID uint) (uint, bool) {
	// 分配给当前客户数最少的顾问（轮询分配）
	// 业务规则：留资成功后自动分配，顾问端立即可见
	// 修复：原来硬编码assigned_user_id=1，改为选当前客户数最少的顾问
	var salesUsers []model.User
	// 修复Bug1（2026-08-22）：角色改用 model.RoleSales 常量。
	// 根因：组织迁移把 sales 改名为 user 后，硬编码"sales"永远查不到 → 永远走兜底分支
	gdb.Where("role = ? AND status = 1 AND tenant_id = ?", model.RoleSales, tenantID).Find(&salesUsers)
	if len(salesUsers) == 0 {
		return 0, false
	}
	minCount := -1
	var bestUserID uint = salesUsers[0].ID
	for _, u := range salesUsers {
		var count int64
		gdb.Model(&model.Customer{}).Where("assigned_user_id = ? AND status = 1", u.ID).Count(&count)
		if minCount < 0 || int(count) < minCount {
			minCount = int(count)
			bestUserID = u.ID
		}
	}
	return bestUserID, true
}

// applyLeadCapturedUpdates 留资无合并分支：客户画像落库 + 内存同步 + 出站副作用。
// 单独成段原因：这是本路径唯一的客户写面（字段级 Updates + 内存镜像 + webhook + 归因回填），
// 五段副作用必须整体在场且顺序不变，聚成一段方便核对"落库列集合没被顺手改"。
func applyLeadCapturedUpdates(customer *model.Customer, phoneMatch string, updates map[string]interface{}) {
	db.DB.Model(customer).Updates(updates)
	// 同步更新内存中的customer对象（修复Bug1：断言改安全形式）
	if v, ok := updates["phone"]; ok {
		customer.Phone, _ = v.(string)
	}
	if v, ok := updates["journey_stage"]; ok {
		customer.JourneyStage, _ = v.(string)
	}
	if v, ok := updates["assigned_user_id"]; ok {
		if uid, uok := v.(uint); uok {
			customer.AssignedUserID = uid
		} else {
			log.Printf("[留资检测-告警] assigned_user_id 类型异常(%T)，保持原值: %v", v, customer.AssignedUserID)
		}
	}
	log.Printf("[留资检测] 客户%d留资成功: phone=%s, stage=%v, assigned=%v",
		customer.ID, pii.MaskPhone(phoneMatch), updates["journey_stage"], updates["assigned_user_id"])
	// D6：出站事件 webhook 扇出（旁路，不阻塞）。载荷只带 customer_id/阶段，不外发手机号明文（商户可凭 OpenAPI Key 取详情）
	webhook.Emit(customer.TenantID, model.WebhookEventLeadCaptured, map[string]interface{}{
		"customer_id": customer.ID,
		"stage":       updates["journey_stage"],
	})
	if v, ok := updates["assigned_user_id"].(uint); ok && v > 0 {
		webhook.Emit(customer.TenantID, model.WebhookEventHumanAssigned, map[string]interface{}{
			"customer_id":      customer.ID,
			"assigned_user_id": v,
		})
		_ = attribution.MarkPendingHuman(customer.TenantID, 0, customer.ID)
	}
	// D9：把留资结果回填到最近一条已归因 AI 回复。
	_ = attribution.MarkLeadCaptured(customer.TenantID, 0, customer.ID)
}

// upsertLeadCapturedFollowUp 留资线索按客户ID合并（有则更新不新建）。
// 单独成段原因：FollowUp 是独立于客户画像的第二写面，且是本仓 C7 盖章红线（db.DB 无请求 ctx）反复命中的位置，
// 单独成段让"显式 TenantID + 租户条件查询"两处历史修复注释与其代码保持同处一地、可整段审计。
func upsertLeadCapturedFollowUp(customer *model.Customer, phoneMatch, customerInput string) {
	// 修复：留资成功后生成线索记录（已留资线索，分配给顾问）
	// 业务规则：已留资线索 → 人工接管 → 顾问端可见
	// 修复问题3：按客户ID合并线索——先查是否已有lead_captured类型的线索，有则更新不新建
	// P2-27 修复：FollowUp content 统一脱敏（手机号+原文内嵌号码），库内不落明文
	var existingFollowUp model.FollowUp
	// P1-5 修复(2026-09-15)：既有记录查询补租户条件——原仅靠 customer_id 全局唯一
	// 兜底，属脆弱不变量（同文件 437 行已有正确示范）。
	result := db.DB.Where("customer_id = ? AND tenant_id = ? AND result = ?", customer.ID, customer.TenantID, "lead_captured").First(&existingFollowUp)
	if result.Error != nil {
		// 没有已有线索，创建新的
		leadFollowUp := model.FollowUp{
			TenantID: customer.TenantID, // P1-5 修复(2026-09-15)：C7 红线再命中——db.DB 无请求 ctx，
			// 盖章回调取到 0 → 留资线索落 tenant_id=0（租户侧 RQ 查询看不见 + 平台视图混入）。
			// 后台/回调路径必须显式传租户，与 billing/privacy/webhook 域同款纪律。
			CustomerID: customer.ID,
			UserID:     customer.AssignedUserID, // 归属顾问
			Type:       "ai_triggered",          // AI触发生成
			Method:     "store",                 // 到店渠道
			Content:    fmt.Sprintf("客户已留资，手机号:%s，触发来源:%s", pii.MaskPhone(phoneMatch), pii.MaskPhoneInText(customerInput)),
			Result:     "lead_captured", // 已留资线索
		}
		db.DB.Create(&leadFollowUp)
		log.Printf("[留资检测-线索生成] 客户%d 已留资线索已生成(FollowUp ID=%d)，分配顾问%d",
			customer.ID, leadFollowUp.ID, customer.AssignedUserID)
	} else {
		// 已有线索，更新内容（按客户ID合并，不新建）
		db.DB.Model(&existingFollowUp).Updates(map[string]interface{}{
			"content": fmt.Sprintf("客户已留资，手机号:%s，触发来源:%s", pii.MaskPhone(phoneMatch), pii.MaskPhoneInText(customerInput)),
			"user_id": customer.AssignedUserID, // 更新归属顾问
		})
		log.Printf("[留资检测-线索合并] 客户%d 已有线索(FollowUp ID=%d)，更新内容，不新建",
			customer.ID, existingFollowUp.ID)
	}
}

// ============================================================
// MergeCustomerByPhone 拆分段（OneID 合并，全部走主事务同一 tx 句柄）
// ============================================================

// lockMergeSurvivor 事务内 FOR UPDATE 读取同手机号老客户（P1-3 行锁范式）。
// 单独成段原因：读与锁必须同处一地才看得清"锁窗口覆盖到写完成"——返回 (nil, nil) 表示
// 锁内复查未命中（含并发已合并），调用方必须按"不合并"空转成功，不得继续任何后续步骤。
func lockMergeSurvivor(tx *gorm.DB, guestCustomer *model.Customer, phone string, tid uint) (*model.Customer, error) {
	var existing model.Customer
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("phone = ? AND id != ? AND status = 1 AND tenant_id = ?", phone, guestCustomer.ID, tid).
		First(&existing).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil // 无匹配老客户（含锁等窗口内已被并发合并）：不合并
		}
		return nil, fmt.Errorf("锁定老客户: %w", err)
	}
	return &existing, nil
}

// mergePreactiveConversations G1 预收拢：迁移归属前按 (updated_at, id) 关旧保新。
// 单独成段原因：这段必须排在「迁移会话 → 改 customer_id」**之前**且同事务——013 唯一索引
// ux_conv_one_active 下任何顺序颠倒都会当场撞约束、回滚整笔合并。它和被它保护的迁移段
// （mergeMigrateOwnedRows）由主函数按原顺序先后调用，本函数不自己开事务。
func mergePreactiveConversations(tx *gorm.DB, guestCustomer, existingCustomer *model.Customer, tid uint) error {
	// 1.0 G1 收口(2026-09-16C，迁移013 唯一索引 ux_conv_one_active 配套)：迁移归属前
	// 预收拢双方 active——013 后"同客户多条 active"在 DB 层已不可能，若直接把访客
	// active 会话改挂到老客户名下会当场撞约束、整个合并事务回滚（OneID 合并反而失效）。
	// 规则与 012/9.2 一致：按 (updated_at, id) 定序保留最新一条，另一方的先关账。
	if err := tx.Exec(`UPDATE conversations s SET status = 'closed'
		WHERE s.tenant_id = ? AND s.customer_id = ? AND s.status = 'active'
		  AND EXISTS (SELECT 1 FROM conversations g
			WHERE g.tenant_id = ? AND g.customer_id = ? AND g.status = 'active'
			  AND (g.updated_at, g.id) > (s.updated_at, s.id))`,
		tid, existingCustomer.ID, tid, guestCustomer.ID).Error; err != nil {
		return fmt.Errorf("预收拢(关老客户active): %w", err)
	}
	if err := tx.Exec(`UPDATE conversations g SET status = 'closed'
		WHERE g.tenant_id = ? AND g.customer_id = ? AND g.status = 'active'
		  AND EXISTS (SELECT 1 FROM conversations s
			WHERE s.tenant_id = ? AND s.customer_id = ? AND s.status = 'active'
			  AND (s.updated_at, s.id) >= (g.updated_at, g.id))`,
		tid, guestCustomer.ID, tid, existingCustomer.ID).Error; err != nil {
		return fmt.Errorf("预收拢(关访客active): %w", err)
	}
	return nil
}

// mergeMigrateOwnedRows 归属迁移：会话/消息/线索/试驾四张子表改挂老客户。
// 单独成段原因：四段是同一个"customer_id 重指"语义的并列语句；会话那条特意用 UpdateColumn
// （不覆写 updated_at）与其余三条不同，单独成段后这处差异集中在一个函数里可读。
func mergeMigrateOwnedRows(tx *gorm.DB, guestCustomer, existingCustomer *model.Customer, tid uint) error {
	// 1. 迁移会话 → 老客户（租户守卫：只迁本租户数据）
	// D2 注(2026-09-16B)：用 UpdateColumn 跳过自动时间戳——归属重指不是业务活动，
	// 覆写 updated_at 会把访客旧会话顶到顾问端"最近更新"列表顶端，且践踏 9.2 收拢的最新判定。
	if err := tx.Model(&model.Conversation{}).
		Where("customer_id = ? AND tenant_id = ?", guestCustomer.ID, tid).
		UpdateColumn("customer_id", existingCustomer.ID).Error; err != nil {
		return fmt.Errorf("迁移会话: %w", err)
	}

	// 2. 迁移消息 → 老客户
	if err := tx.Model(&model.Message{}).
		Where("customer_id = ? AND tenant_id = ?", guestCustomer.ID, tid).
		Update("customer_id", existingCustomer.ID).Error; err != nil {
		return fmt.Errorf("迁移消息: %w", err)
	}

	// 2.5 迁移线索(FollowUp) → 老客户
	if err := tx.Model(&model.FollowUp{}).
		Where("customer_id = ? AND tenant_id = ?", guestCustomer.ID, tid).
		Update("customer_id", existingCustomer.ID).Error; err != nil {
		return fmt.Errorf("迁移线索: %w", err)
	}

	// 3.5 迁移试驾(TestDrive) → 老客户
	if err := tx.Model(&model.TestDrive{}).
		Where("customer_id = ? AND tenant_id = ?", guestCustomer.ID, tid).
		Update("customer_id", existingCustomer.ID).Error; err != nil {
		return fmt.Errorf("迁移试驾: %w", err)
	}
	return nil
}

// mergeMigrateCustomerTags 标签关联迁移（去重后重建到老客户名下）。
// 单独成段原因：这是事务内唯一的 Create（customer_tags）——C7 红线要求它显式设 TenantID，
// 整段收在一个函数里，"Create 必带租户"这一条只需在一处核对。
func mergeMigrateCustomerTags(tx *gorm.DB, guestCustomer, existingCustomer *model.Customer, tid uint) error {
	// 3.6 迁移客户标签关联(customer_tags) → 老客户（去重）
	var guestTagRecords []model.CustomerTag
	if err := tx.Where("customer_id = ? AND tenant_id = ?", guestCustomer.ID, tid).Find(&guestTagRecords).Error; err != nil {
		return fmt.Errorf("查访客标签: %w", err)
	}
	for _, gtr := range guestTagRecords {
		var count int64
		if err := tx.Model(&model.CustomerTag{}).
			Where("customer_id = ? AND tag_id = ? AND tenant_id = ?", existingCustomer.ID, gtr.TagID, tid).
			Count(&count).Error; err != nil {
			return fmt.Errorf("查标签冲突: %w", err)
		}
		if count == 0 {
			gtr.ID = 0
			gtr.CustomerID = existingCustomer.ID
			gtr.TenantID = tid
			if err := tx.Create(&gtr).Error; err != nil {
				return fmt.Errorf("迁标签落库: %w", err)
			}
		}
	}
	return nil
}

// mergeTagStringsInMemory 合并双方标签字符串集（去重）并重存 T 向量。
// 单独成段原因：纯内存计算不落库（写库在后续 mergeSaveSurvivorProfile 一次完成），
// 拆出来后"事务闭包里哪几条语句真的碰库"一眼可数。
func mergeTagStringsInMemory(existingCustomer, guestCustomer *model.Customer) {
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
}

// mergeCombineProfileFields 5~7：旅程阶段取最高、意向/信任取更高、缺失字段补全（纯内存）。
// 单独成段原因：与 mergeTagStringsInMemory 同理——三条"取大/补缺"合并律不碰库，
// 单独成段后主事务函数里只剩真正的读写语句。
func mergeCombineProfileFields(existingCustomer, guestCustomer *model.Customer) {
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
}

// mergeSaveSurvivorProfile 步骤8：保存老客户（字段级 Updates，列集合冻结）。
// 单独成段原因：这是 P1-3（2026-09-19 批二）把整行 Save 收口成字段级 Updates 的落点——
// 列集合就是护栏语义本身（列集外 phone/name/接管态不得被覆写），单独成段并配注释，
// 防止后人"顺手合并回整行 Save"（2026-09-18 复核批 9 处收口的同族红线）。勿改列集合、勿改回 Save。
func mergeSaveSurvivorProfile(tx *gorm.DB, existingCustomer *model.Customer, tid uint) error {
	// 8. 保存老客户更新
	// P1-3 修复(2026-09-19 审计批二)：tx.Save 整行覆写改字段级 Updates——
	// 只写本次合并真正变更的列（标签/T向量/阶段/意向/信任/微信ID/来源），
	// phone/name/接管态等列即便行锁窗口外被改也不进覆写集合（复核批 9 处收口同族红线，此路径补齐）。
	if err := tx.Model(&model.Customer{}).Where("id = ? AND tenant_id = ?", existingCustomer.ID, tid).Updates(map[string]interface{}{
		"tags":          existingCustomer.Tags,
		"t_vector":      existingCustomer.TVectorJSON, // 列名 t_vector（model/customer.go:45）
		"journey_stage": existingCustomer.JourneyStage,
		"intent_score":  existingCustomer.IntentScore,
		"trust_level":   existingCustomer.TrustLevel,
		"wechat_id":     existingCustomer.WechatID,
		"source":        existingCustomer.Source,
	}).Error; err != nil {
		return fmt.Errorf("保存老客户: %w", err)
	}
	return nil
}

// mergeAssignSurvivorSales 步骤8.5：老客户无顾问时轮询分配（走主事务 tx）。
// 单独成段原因：与留资主路径的 assignSalesWithFewestCustomers 同型但**错误分支不同**——
// 这里查询失败必须回滚整笔合并（旧实现吞错导致"半合并"），故两份实现刻意不共用，
// 合并任何一个错误处理都会改变行为（merge_d2_test.go 的列集外护栏依赖本段：已有顾问时整段不写、
// 需要分配时也只写 assigned_user_id 单列）。
func mergeAssignSurvivorSales(tx *gorm.DB, existingCustomer *model.Customer) error {
	// 8.5 老客户无顾问时，轮询分配给当前客户最少的销售
	if existingCustomer.AssignedUserID == 0 {
		var salesUsers []model.User
		// 修复Bug1（2026-08-22）：角色改用 model.RoleSales 常量（同 DetectLeadCapture 主路径）
		if err := tx.Where("role = ? AND status = 1 AND tenant_id = ?", model.RoleSales, existingCustomer.TenantID).Find(&salesUsers).Error; err != nil {
			return fmt.Errorf("查销售: %w", err)
		}
		if len(salesUsers) > 0 {
			minCount := -1
			var bestUserID uint = salesUsers[0].ID
			for _, u := range salesUsers {
				var count int64
				tx.Model(&model.Customer{}).Where("assigned_user_id = ? AND status = 1", u.ID).Count(&count)
				if minCount < 0 || int(count) < minCount {
					minCount = int(count)
					bestUserID = u.ID
				}
			}
			if err := tx.Model(existingCustomer).Update("assigned_user_id", bestUserID).Error; err != nil {
				return fmt.Errorf("分配顾问: %w", err)
			}
			existingCustomer.AssignedUserID = bestUserID
			log.Printf("[OneID合并-分配顾问] 老客户%d 无顾问，轮询分配给顾问%d", existingCustomer.ID, bestUserID)
		} else {
			// I3修复(2026-08-26)：无可用顾问时不写死跨租户脏值(uint(2))，置 assigned_user_id=0 待人工池认领
			if err := tx.Model(existingCustomer).Update("assigned_user_id", uint(0)).Error; err != nil {
				return fmt.Errorf("顾问置零: %w", err)
			}
			existingCustomer.AssignedUserID = 0
			log.Printf("[OneID合并-分配顾问] 老客户%d 无顾问，置 assigned_user_id=0 待人工池认领", existingCustomer.ID)
		}
	}
	return nil
}

// mergeCollapseSurvivorActives 步骤9.2：合并后收拢老客户名下多余 active 会话。
// 单独成段原因：与开头的 mergePreactiveConversations（迁移前预收拢）是一对——前置收拢保
// 「迁移不撞 013」，本段收拢保「迁移后同客户只剩一条 active」，两段隔着整条迁移链、只能各自成段。
func mergeCollapseSurvivorActives(tx *gorm.DB, existingCustomer *model.Customer, tid uint) error {
	// 9.2 D2 修复(2026-09-16B)：合并后收拢 active 会话——访客会话迁过来后同一客户
	// 可能挂多条 active（多入口/多通道各开一条），顾问端计数虚高、上下文分裂。
	// 保留最近更新的一条，其余关账（closed，历史仍完整可查）。
	if err := tx.Exec(`UPDATE conversations SET status = 'closed'
		WHERE tenant_id = ? AND customer_id = ? AND status = 'active'
		  AND id NOT IN (
			SELECT id FROM conversations
			WHERE tenant_id = ? AND customer_id = ? AND status = 'active'
			ORDER BY updated_at DESC LIMIT 1)`,
		tid, existingCustomer.ID, tid, existingCustomer.ID).Error; err != nil {
		return fmt.Errorf("收拢active会话: %w", err)
	}
	return nil
}
