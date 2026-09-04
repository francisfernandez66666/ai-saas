// Package strategy 策略中心引擎（系统"大脑"）
// 串联线上推理7步公式：锚派发打分→softmax归一化→阶段锁/软降级→话术召回→紧迫判定→路由→意向反哺。
// 含 strategy/flow 子目录：flow 管会话生命周期，strategy 管单轮决策。
// 架构红线：业务层不得直连 llm，一律经本包 GenerateReply 桥接（ai→strategy→llm）。
package strategy

import (
	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/service"
	"log"
	"strings"
)

// ============================================================
// 策略中心引擎 - 主入口
// 串联线上推理7步公式，是整个系统的"大脑"
//
// 7步公式回顾：
// Step1：锚派发打分 score_a = w_a · f_a(T, S)
// Step2：softmax归一化 p(a|T,S) = softmax(score_a / τ)，取a_hat
// Step3：软降级修锚（hook_rate低/沉默长→降aggressiveness一级）
// Step4：话术模板召回（按a_hat+标签匹配+相似度）
// Step5：紧迫等级判定（L1/L2/L3）
// Step6：路由决策（AI/J1转人工/养鱼）
// Step7：意向分反哺
// ============================================================

// Engine 策略引擎结构体
// SaaS 化改造：支持多租户下的数据隔离
// TenantID=0 表示查询所有租户数据（启动时默认），>0 则严格隔离
type Engine struct {
	TenantID uint // 当前租户ID
	// 可以在这里缓存模板、卖点等数据，避免每次都查库
	templates []model.Template
	features  []model.Feature
}

// DefaultEngine 默认策略引擎实例
var DefaultEngine *Engine

// InitEngine 初始化策略引擎
// 预加载话术模板和卖点库，提高推理速度
func InitEngine() {
	DefaultEngine = &Engine{}
	DefaultEngine.LoadData()
	log.Println("策略中心引擎初始化完成")
}

// LoadData 加载策略数据（模板+卖点）
// SaaS 化改造：根据 TenantID 过滤查询，实现租户数据隔离
// TenantID=0 时查询所有数据，>0 时只查当前租户
// 修复（M1 2026-08-25）：原实现构建的 query 被丢弃、实际执行 db.DB.Find 裸查——
// 现改为 query.Find 使过滤真正生效。DefaultEngine(TenantID=0) 仍全量加载作为缓存底座，
// 真正的租户隔离闸门在 Infer 召回时的 templatesForTenant/featuresForTenant 内存过滤。
func (e *Engine) LoadData() {
	// 加载所有启用的话术模板
	var templates []model.Template
	query := db.DB.Where("status = ?", 1)
	if e.TenantID > 0 {
		query = query.Scopes(db.TenantFilter(e.TenantID))
	}
	if err := query.Find(&templates).Error; err != nil { // 修复：原为 db.DB.Find（过滤被丢弃）
		log.Printf("[策略引擎] 加载话术模板失败: %v", err)
	}
	e.templates = templates
	log.Printf("已加载 %d 条话术模板 (tenant=%d)", len(templates), e.TenantID)

	// 加载所有启用的卖点
	var features []model.Feature
	query2 := db.DB.Where("status = ?", 1)
	if e.TenantID > 0 {
		query2 = query2.Scopes(db.TenantFilter(e.TenantID))
	}
	if err := query2.Find(&features).Error; err != nil { // 同上修复
		log.Printf("[策略引擎] 加载卖点数据失败: %v", err)
	}
	e.features = features
	log.Printf("已加载 %d 条卖点数据 (tenant=%d)", len(features), e.TenantID)

	// 动态装载车型注册表（modelFromTVector 依赖），仅全量引擎(TenantID=0)刷新一次即可
	if e.TenantID == 0 {
		refreshCarModelRegistry()
	}
}

// templatesForTenant 按租户+部门链过滤话术模板（M1 租户隔离修复 + 三级包架构 2026-08-26）
// 规则：
//   - tenant_id=0 系统预置（行业包物化亦落此层）对所有租户可见；>0 仅归属租户可见
//   - DepartmentID=nil 为租户级内容（行业/企业包）→ 本租户全量可见
//   - DepartmentID 非空为部门专属内容（部门包物化）→ 仅当 deptIDs 含该部门时可见
//     （deptIDs = 顾问所属部门的完整继承链：自身→父→…→根，自底向上查起语义）
//   - 入参 tenantId=0 时 fail-closed 只见预置
//
// templatesForTenant 三集合过滤（租户隔离 + KB继承链可见域）
func templatesForTenant(all []model.Template, tenantID uint, scope *service.RecallScope) []model.Template {
	filtered := make([]model.Template, 0, len(all))
	for i := range all {
		t := &all[i]
		if t.TenantID != 0 && t.TenantID != tenantID {
			continue // 跨租户隔离
		}
		if t.DepartmentID != nil &&
			!scope.OwnDepts[*t.DepartmentID] && !scope.CrossDepts[*t.DepartmentID] {
			continue // 部门专属：不在链内也不满足跨部门回退条件
		}
		filtered = append(filtered, *t)
	}
	return filtered
}

// featuresForTenant 按租户+部门链过滤卖点库（规则同 templatesForTenant）
func featuresForTenant(all []model.Feature, tenantID uint, scope *service.RecallScope) []model.Feature {
	filtered := make([]model.Feature, 0, len(all))
	for i := range all {
		f := &all[i]
		if f.TenantID != 0 && f.TenantID != tenantID {
			continue
		}
		if f.DepartmentID != nil &&
			!scope.OwnDepts[*f.DepartmentID] && !scope.CrossDepts[*f.DepartmentID] {
			continue
		}
		filtered = append(filtered, *f)
	}
	return filtered
}

// ReloadData 重新加载数据（模板/卖点更新后调用）
func (e *Engine) ReloadData() {
	e.LoadData()
}

// Features 获取当前加载的卖点列表
// 供AI Prompt构建器动态注入卖点知识
func (e *Engine) Features() []model.Feature {
	return e.features
}

// Templates 获取当前加载的话术模板列表
func (e *Engine) Templates() []model.Template {
	return e.templates
}

// Infer 执行一次完整的策略推理
// 输入：客户信息 + 会话状态 + 客户输入
// 输出：完整的策略决策结果
//
// 这是策略引擎的核心函数，串联7步公式
func (e *Engine) Infer(input StrategyInput) StrategyOutput {
	var output StrategyOutput
	// 提前声明锚选择链路变量，避免goto跳过变量声明导致编译错误
	var (
		bestAnchor           int
		confidence           float64
		anchorAfterStageLock int
		isStageDowngraded    bool
		finalAnchor          int
		isSoftDowngraded     bool
		probs                [AnchorCount]float64
		stageCeilingAnchor   int
		anchorScores         [AnchorCount]float64
	)

	// ============================================================
	// Step0.5：打标驱动策略 - 标签权重注入T向量
	// 为什么在最前面？因为后面的锚打分完全依赖T向量
	// 流程：从DB查客户标签 → 查权重映射 → 更新T向量对应维度
	// 这样客户打上"价格敏感"标签，T[1]就会升高，策略就会自动调整
	// ============================================================
	tVector := input.TVector

	// 构造临时customer对象，只用于承接ApplyTagWeightsToTVector保存结果、以及取标签列表用的CustomerID
	// 修复：基准向量必须显式传入input.TVector（调用方chat.go已保证这是BuildBaseTVector()算出的基准值，
	// 不是可能已叠加过标签权重的持久化值），不能让函数内部自己从这个只有ID的临时customer上现算基准——
	// 这个临时customer没有真实的预算/意向分等结构化字段，现算会得到全零的假基准，冲掉客户真实画像。
	customer := &model.Customer{
		ID: input.CustomerID,
	}

	err := service.DefaultTagService.ApplyTagWeightsToTVector(customer, input.TVector)
	if err == nil {
		// 读取应用权重后的T向量
		// 重大修复（2026-08-26）：ApplyTagWeightsToTVector 在客户无标签时直接 return nil、
		// 不写入结果——此处再 GetTVector() 会拿到临时 customer 的全零向量，把真实画像
		// （意向/信任等）清零 → 锚打分退化为纯 bias，未打标客户永远倾向"不抛锚"。
		// 修复：仅当结果向量非零（确实应用了标签权重）时才采用，否则保留基准向量。
		applied := customer.GetTVector()
		nonZero := false
		for _, v := range applied {
			if v != 0 {
				nonZero = true
				break
			}
		}
		if nonZero {
			tVector = applied
			log.Printf("[策略引擎] Step0.5: 标签权重已注入T向量")
		} else {
			log.Printf("[策略引擎] Step0.5: 客户无标签，保留基准T向量（修复前此路径会清零画像）")
		}
	} else {
		log.Printf("[策略引擎] Step0.5: 标签权重注入失败: %v", err)
	}

	// ============================================================
	// Step0：自动抗性识别（从客户输入文本中识别抗性类型）
	// 如果T向量里抗性类型为0（未设置），则从文本自动识别
	// 为什么放在引擎里？所有调用方都能自动受益，不用每个接口自己做
	// ============================================================
	if tVector[14] == 0 && input.CustomerInput != "" {
		detected := DetectResistance(input.CustomerInput)
		if detected > 0 {
			tVector[14] = float64(detected)
			log.Printf("[策略引擎] Step0: 自动识别抗性类型=%d (%s)",
				detected, resistanceName(detected))
		}
	}

	// ============================================================
	// Step0.1：寒暄检测（语义级前置拦截）
	// 不管客户画像多高，纯寒暄强制不抛锚
	// 画像决定"聊什么"，对话阶段决定"能不能推"
	// "你好"→不抛锚（礼貌回应），"你好，请问极石多少钱"→正常走锚
	// ============================================================
	if IsGreeting(input.CustomerInput) {
		log.Printf("[策略引擎] Step0.1: 检测到纯寒暄\"%s\"，强制不抛锚", input.CustomerInput)
		output.FinalAnchor = AnchorNoThrow
		output.OriginalAnchor = AnchorNoThrow
		output.AnchorConfidence = 0.99
		output.SoftDowngrade = false
		output.StageBeforeLock = AnchorNoThrow
		output.StageCeilingAgg = 0
		output.StageDowngraded = false
		// 不抛锚时选"不抛锚-安抚倾听"模板
		output.TemplateID = "tpl_nothrow_001"
		output.TemplateName = "不抛锚-安抚倾听"
		output.ExchangeFlag = false
		output.ExchangeType = ""
		output.AnchorScores = [AnchorCount]float64{}
		output.AnchorProbs = [AnchorCount]float64{}
		// 跳过后续Step1-3，直接进入路由决策
		goto afterAnchorSelection
	}

	// ============================================================
	// Step1：锚派发打分
	// ============================================================
	anchorScores = Step1_CalcAnchorScores(tVector, input.State)

	// ============================================================
	// Step1.5：促单锁（CanPromote=false时压制稀缺/代价自担锚）
	// 业务规则：只有"已到店+已报价"之后才能促单
	// 防止AI在"到店体验"阶段就推"专属优惠""限时优惠"
	// ============================================================
	if !input.CanPromote {
		anchorScores = CalcAnchorScoresPromoteLocked(anchorScores)
		log.Printf("[策略引擎] Step1.5: 促单锁生效，CanPromote=false，稀缺/代价自担锚被压制")
	}

	output.AnchorScores = anchorScores

	// ============================================================
	// Step1.6：线索阶段天花板（业务漏斗硬锁）
	// 与心智阶段锁不同——心智阶段是"对话热度"，线索阶段是"业务漏斗位置"
	// 不管对话多热烈，客户没到店就是不能促单
	// 这是比Step2.5心智阶段锁更严格的硬约束
	// ============================================================
	if input.JourneyStage != "" {
		if ceiling, ok := JourneyStageAggressivenessCeiling[input.JourneyStage]; ok {
			// 特殊处理：arrived+quoted不限制（由CanPromote控制促单）
			if input.JourneyStage == model.JourneyArrived && input.CanPromote {
				// arrived+quoted，不限制aggressiveness
			} else {
				// 强制降级：把所有超过ceiling的锚分数清零
				for a := 0; a < AnchorCount; a++ {
					if AnchorAggressiveness[a] > ceiling {
						anchorScores[a] = -999 // 确保不可能被选中
					}
				}
				log.Printf("[策略引擎] Step1.6: 线索阶段天花板生效，阶段=%s，agg上限=%d", input.JourneyStage, ceiling)
			}
		}
	}

	// ============================================================
	// Step2：softmax归一化 + 选最优锚
	// ============================================================
	probs, bestAnchor, confidence = Step2_SoftmaxAnchor(anchorScores)
	output.AnchorProbs = probs
	output.SelectedAnchor = bestAnchor
	output.AnchorConfidence = confidence
	output.OriginalAnchor = bestAnchor

	// ============================================================
	// Step2.5：心智阶段锁修锚
	// 核心逻辑：锚的aggressiveness不能超过客户当前心智阶段的上限
	// 这是PRD 5级策略链路（认知→懂我→兴趣→促转→沉淀）的强制保障
	//
	// 为什么在Step3之前？
	//   阶段锁是结构性的硬约束（你在认知阶段就不许用对比锚），
	//   而Step3软降级是基于动态信号的微调（接钩率低→降一级）。
	//   先执行硬约束，再执行微调，逻辑更清晰。
	//
	// 典型场景：
	//   新客户说"你好" → stage=0, ceiling=1 → softmax选了对比锚(aggressiveness=3)
	//   → 阶段锁强制降级到同类锚(aggressiveness=1) → 不会"平A开大"
	// ============================================================
	stageCeilingAnchor, isStageDowngraded = Step2_5_StageCeiling(bestAnchor, input.State.CurrentStage)
	output.StageDowngraded = isStageDowngraded
	output.StageBeforeLock = bestAnchor                                   // 阶段锁降级前的锚
	output.StageCeilingAgg = StageAnchorCeiling[input.State.CurrentStage] // 当前阶段允许的上限

	// 阶段锁降级后，用降级结果作为Step3的输入
	anchorAfterStageLock = stageCeilingAnchor

	if isStageDowngraded {
		log.Printf("[策略引擎] Step2.5: 阶段锁降级！原始锚=%d(%s,agg=%d) → 降级到=%d(%s,agg=%d), 阶段=%d(上限=%d)",
			bestAnchor, GetAnchorName(bestAnchor), AnchorAggressiveness[bestAnchor],
			stageCeilingAnchor, GetAnchorName(stageCeilingAnchor), AnchorAggressiveness[stageCeilingAnchor],
			input.State.CurrentStage, output.StageCeilingAgg)
	} else {
		log.Printf("[策略引擎] Step2.5: 阶段锁通过，锚=%d(%s,agg=%d) ≤ 阶段上限=%d, 阶段=%d",
			bestAnchor, GetAnchorName(bestAnchor), AnchorAggressiveness[bestAnchor],
			output.StageCeilingAgg, input.State.CurrentStage)
	}

	// ============================================================
	// Step3：软降级修锚（基于接钩率/沉默时长/情绪等动态信号）
	// 注意：输入是Step2.5降级后的锚，不是softmax的原始锚
	// ============================================================
	finalAnchor, isSoftDowngraded = Step3_SoftDowngrade(anchorAfterStageLock, input.State)
	output.FinalAnchor = finalAnchor
	output.SoftDowngrade = isSoftDowngraded
	// OriginalAnchor始终记录softmax的原始锚（不含任何降级），便于追踪完整的降级链路
	output.OriginalAnchor = bestAnchor

	log.Printf("[策略引擎] Step1-3完整链路: softmax原始锚=%d(%s), 阶段锁降级=%v→%d(%s), 软降级=%v→最终锚=%d(%s), 置信度=%.2f",
		bestAnchor, GetAnchorName(bestAnchor),
		isStageDowngraded, anchorAfterStageLock, GetAnchorName(anchorAfterStageLock),
		isSoftDowngraded, finalAnchor, GetAnchorName(finalAnchor), confidence)

afterAnchorSelection:
	// 寒暄检测和正常锚选择链路在此汇合
	// 寒暄时跳过Step1-3，直接到这里；正常流程走完Step1-3也到这里

	// ============================================================
	// Step4：话术模板召回 + 卖点动态填充
	// 寒暄时已在Step0.1设好模板和话术，跳过Step4和条件交换
	//
	// M1 租户隔离修复（2026-08-25）：召回前按 input.TenantID 内存过滤。
	// 引擎缓存为全量加载（DefaultEngine 含所有租户私有数据），
	// 不过滤则租户A私有话术/卖点会进入租户B客户的AI召回池——跨租户泄露。
	// 规则：预置(tenant_id=0)全员可见；私有仅本租户；TenantID=0 fail-closed 只见预置。
	// ============================================================
	if output.FinalAnchor != AnchorNoThrow || output.TemplateID == "" {
		// 三级包架构+KB继承链（2026-08-26）：解析可见域（链内①/跨部门回退④）
		recallScope := service.ResolveRecallScope(input.TenantID, input.DeptIDs)
		tenantTemplates := templatesForTenant(e.templates, input.TenantID, recallScope)
		log.Printf("[策略引擎] Step4: 模板池过滤 全量=%d → 本租户可见=%d (tenant=%d 链内部门=%d 跨部门候选=%d)",
			len(e.templates), len(tenantTemplates), input.TenantID,
			len(recallScope.OwnDepts), len(recallScope.CrossDepts))
		template, similarity := Step4_RecallTemplate(
			finalAnchor,
			input.CustomerTags,
			input.TVector,
			tenantTemplates,
		)

		if template != nil {
			output.TemplateID = template.ID
			output.TemplateName = template.Name
			// KB继承链：跨部门回退命中的内容打标（🌐跨部门·来自X部门库）
			if template.DepartmentID != nil && recallScope.CrossDepts[*template.DepartmentID] {
				output.TemplateName += " " + recallScope.CrossTags[*template.DepartmentID]
			}

			// 动态填充卖点（M1: 同规则过滤卖点库，防跨租户卖点串入）
			customer := &model.Customer{
				ID:               input.CustomerID,
				InterestProduct: modelFromTVector(tVector),
				Name:             "客户", // 这里简化，实际应从DB获取
			}
			promptText, hookText, _ := FillTemplate(template, customer, featuresForTenant(e.features, input.TenantID, recallScope))
			output.PromptText = promptText
			output.HookText = hookText

			log.Printf("[策略引擎] Step4: 选中模板=%s(%s), 相似度=%.2f", template.ID, template.Name, similarity)
		} else {
			// 兜底话术
			output.PromptText = "感谢您的关注，请问有什么可以帮您的？"
			output.HookText = "您对哪方面比较感兴趣呢？"
			log.Printf("[策略引擎] Step4: 未找到匹配模板，使用兜底话术")
		}

		// 条件交换检查
		resistanceType := resistanceTypeFromTVector(tVector)
		exchangeFlag, exchangeType := CheckExchangeFlag(finalAnchor, resistanceType)

		// 硬锁：CanPromote=false时，强制禁止条件交换
		// 不是靠prompt约束AI，而是代码层面直接拦截，AI根本看不到条件交换指令
		if !input.CanPromote && exchangeFlag {
			log.Printf("[策略引擎] 促单锁拦截条件交换：CanPromote=false，原交换类型=%s，强制关闭", exchangeType)
			exchangeFlag = false
			exchangeType = ""
		}

		output.ExchangeFlag = exchangeFlag
		output.ExchangeType = exchangeType

		if exchangeFlag {
			log.Printf("[策略引擎] 触发条件交换: 抗性=%s, 交换类型=%s", resistanceType, exchangeType)
		}
	}

	// ============================================================
	// Step5：紧迫等级判定
	// ============================================================
	intentScore := tVector[0]
	urgencyLevel := Step5_CalcUrgency(intentScore, input.State.HighIntentRounds)
	output.UrgencyLevel = urgencyLevel

	log.Printf("[策略引擎] Step5: 紧迫等级=%s, 意向分=%.2f, 高意向轮数=%d",
		urgencyLevel, intentScore, input.State.HighIntentRounds)

	// ============================================================
	// Step6：路由决策
	// ============================================================
	routeResult, routeReason := Step6_RouteDecision(tVector, input.State, urgencyLevel, input.CustomerInput)
	output.RouteResult = routeResult
	output.RouteReason = routeReason

	log.Printf("[策略引擎] Step6: 路由=%s, 原因=%s", routeResult, routeReason)

	// ============================================================
	// Step7：意向分反哺（增量）
	// ============================================================
	emotion := DetectEmotion(input.CustomerInput)
	hooked := CheckHooked(input.CustomerInput)
	newIntent := Step7_UpdateIntent(intentScore, input.CustomerInput, hooked, emotion)
	output.IntentDelta = newIntent - intentScore

	log.Printf("[策略引擎] Step7: 意向分变化=%.3f (%.3f → %.3f), 情绪=%s, 是否接钩=%v",
		output.IntentDelta, intentScore, newIntent, emotion, hooked)

	return output
}

// ============================================================
// 辅助函数
// ============================================================

// carModelRegistry 车型注册表（按 Sort/ID 升序，1 起始索引对应 code=1）
// 由 LoadData 从 car_models 表动态装载，替代原先硬编码的"越野SUV-X*"映射，
// 使不同行业包（车型体系不同）不再被写死字符串绑死。
var carModelRegistry []string

// refreshCarModelRegistry 从 car_models 装载车型注册表（code=i 对应 registry[i-1]）
// 泛行业化（P2.2）：同步注入 model 包的兴趣产品编码器，行业包注册表即编码字典
func refreshCarModelRegistry() {
	var models []model.CarModel
	if err := db.DB.Where("status = ?", 1).Order("sort ASC, id ASC").Find(&models).Error; err != nil {
		log.Printf("[策略引擎] 装载车型注册表失败: %v", err)
		return
	}
	regs := make([]string, 0, len(models))
	for i := range models {
		regs = append(regs, models[i].Name)
	}
	carModelRegistry = regs
	// 注入编码器：注册表内产品 code=i+1，未命中归 99（其他产品）
	model.RegisterModelCodeResolver(func(product string) float64 {
		for i, name := range regs {
			if name == product {
				return float64(i + 1)
			}
		}
		return 99
	})
	log.Printf("[策略引擎] 车型注册表已装载 %d 个车型", len(regs))
}

// modelFromTVector 从T向量获取车型
// 泛行业化（P2.2）：仅依赖动态注册表（car_models），移除硬编码兜底映射
func modelFromTVector(tVector [32]float64) string {
	modelCode := int(tVector[3])
	if modelCode == 0 {
		return ""
	}
	if modelCode == 99 {
		return "其他车型"
	}
	if modelCode > 0 && modelCode-1 < len(carModelRegistry) && carModelRegistry[modelCode-1] != "" {
		return carModelRegistry[modelCode-1]
	}
	return ""
}

// resistanceTypeFromTVector 从T向量获取抗性类型
func resistanceTypeFromTVector(tVector [32]float64) string {
	code := int(tVector[14])
	resistanceMap := map[int]string{
		0: "none",
		1: "price",
		2: "spec",
		3: "service",
		4: "brand",
	}
	if name, ok := resistanceMap[code]; ok {
		return name
	}
	return "none"
}

// ============================================================
// 抗性识别相关
// ============================================================

// DetectResistance 从客户消息文本中识别抗性类型
// 简单关键词匹配，后续可升级为AI分类
// 返回：0=无 1=价格 2=规格 3=服务 4=品牌
func DetectResistance(text string) int {
	if text == "" {
		return 0
	}
	text = strings.ToLower(text)

	// 价格抗性关键词（最多）
	// 修复（2026-08-30）：原列表混入正则式字面量"比.*贵"，调用方用 strings.Contains 匹配，
	// 该字面量几乎不可能出现在用户原文里导致永远不命中。改为纯关键词，并补"比X贵"类口语。
	priceKeywords := []string{
		"贵", "太贵", "价格高", "便宜点", "优惠", "降价", "贵了",
		"不值", "性价比", "贵了点", "超出预算", "预算不够",
		"多少钱", "价格", "能便宜", "再少", "再降",
		"比", "贵不少", "贵很多", "偏贵", "划算",
		"price", "expensive", "too much", "too expensive", "costly",
	}
	for _, kw := range priceKeywords {
		if strings.Contains(text, kw) {
			return 1
		}
	}

	// 规格抗性关键词
	specKeywords := []string{
		"动力不够", "配置低", "配置不行", "马力", "续航",
		"空间小", "太小", "动力弱", "参数", "配置差",
		"加速", "油耗高", "费油",
	}
	for _, kw := range specKeywords {
		if strings.Contains(text, kw) {
			return 2
		}
	}

	// 服务抗性关键词
	serviceKeywords := []string{
		"售后", "服务差", "保养", "维修", "取送车",
		"服务不好", "质保", "保修",
	}
	for _, kw := range serviceKeywords {
		if strings.Contains(text, kw) {
			return 3
		}
	}

	// 品牌抗性关键词
	brandKeywords := []string{
		"品牌", "没听过", "杂牌", "不如", "品牌力",
		"知名度", "牌子", "小众",
	}
	for _, kw := range brandKeywords {
		if strings.Contains(text, kw) {
			return 4
		}
	}

	return 0
}

// resistanceName 抗性类型名称
func resistanceName(code int) string {
	names := map[int]string{
		0: "无",
		1: "价格抗性",
		2: "规格抗性",
		3: "服务抗性",
		4: "品牌抗性",
	}
	if name, ok := names[code]; ok {
		return name
	}
	return "未知"
}
