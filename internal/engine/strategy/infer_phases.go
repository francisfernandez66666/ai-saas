// Package strategy 策略中心：Infer 七步公式的阶段方法（2026-09-28 结构重构批）。
//
// 为什么单独成文件：(*Engine).Infer 原为 313 行的 god function，本文件把它的各段
// "剪切-粘贴"为 inferPhase 上的阶段方法，主体只保留调用顺序。判据、阈值、日志文案、
// 导出函数签名零改动；每一步为什么单独拆出去写在各自方法文档注释里。
package strategy

import (
	"ai-scrm/internal/model"
	"ai-scrm/internal/service"
	"log"
)

// inferPhase 一次策略推理的阶段上下文（承载跨段共享的局部变量）。
//
// 一次推理一份，随函数返回即释放，不引入任何包级可变全局，天然无并发共享。
type inferPhase struct {
	e      *Engine
	input  StrategyInput
	output StrategyOutput

	// tVector 是基准 T 向量叠加标签权重（Step0.5）与抗性识别（Step0）之后的"本轮实际用向量"。
	// 注意：Step4 的话术召回刻意仍传 input.TVector（原实现即如此），只有车型/抗性/意向分读它。
	tVector [32]float64

	// 提前声明锚选择链路变量，避免goto跳过变量声明导致编译错误
	//
	// 结构重构批注（2026-09-28）：Infer 里唯一的 goto 已由 if/else 分支取代，原先 9 个
	// 中间量里只有这两个真正跨段（anchorScores: Step1~1.6 → Step2；finalAnchor:
	// Step3 → Step4），其余七个（bestAnchor/confidence/anchorAfterStageLock/
	// isStageDowngraded/isSoftDowngraded/probs/stageCeilingAnchor）回落为 chooseAnchor 的局部变量。
	anchorScores [AnchorCount]float64
	finalAnchor  int
}

// applyTagWeights 执行 Step0.5：把客户标签权重注入 T 向量。
//
// 为什么单独拆出来：这是整条链路里唯一"读库改写 T 向量、且改写结果可能是假的"的一段——
// ApplyTagWeightsToTVector 查库失败要降级、成功但客户无标签时又必须拒绝采信（该函数在无标签
// 时直接 return nil 不写结果，回读会拿到全零假基准把真实画像清零）。它的失败路径与后续
// 纯计算步骤根本不同，留在主函数里那层"非零才采信"守卫最容易被看漏。
func (p *inferPhase) applyTagWeights() {
	// ============================================================
	// Step0.5：打标驱动策略 - 标签权重注入T向量
	// 为什么在最前面？因为后面的锚打分完全依赖T向量
	// 流程：从DB查客户标签 → 查权重映射 → 更新T向量对应维度
	// 这样客户打上"价格敏感"标签，T[1]就会升高，策略就会自动调整
	// ============================================================

	// 构造临时customer对象，只用于承接ApplyTagWeightsToTVector保存结果、以及取标签列表用的CustomerID
	// 修复：基准向量必须显式传入input.TVector（调用方chat.go已保证这是BuildBaseTVector()算出的基准值，
	// 不是可能已叠加过标签权重的持久化值），不能让函数内部自己从这个只有ID的临时customer上现算基准——
	// 这个临时customer没有真实的预算/意向分等结构化字段，现算会得到全零的假基准，冲掉客户真实画像。
	customer := &model.Customer{
		ID: p.input.CustomerID,
	}

	err := service.DefaultTagService.ApplyTagWeightsToTVector(p.input.TenantID, customer, p.input.TVector)
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
			p.tVector = applied
			log.Printf("[策略引擎] Step0.5: 标签权重已注入T向量")
		} else {
			log.Printf("[策略引擎] Step0.5: 客户无标签，保留基准T向量（修复前此路径会清零画像）")
		}
	} else {
		log.Printf("[策略引擎] Step0.5: 标签权重注入失败: %v", err)
	}
}

// detectResistance 执行 Step0：客户未显式设置抗性类型时，从输入文本自动识别。
//
// 为什么单独拆出来：它只条件性地改 T[14] 这一个维度（前置条件是"当前为 0 且输入非空"），
// 无失败路径、无跨段中间量，与 Step0.5 的"整向量可能被替换"是两种不同的改写语义。
func (p *inferPhase) detectResistance() {
	// ============================================================
	// Step0：自动抗性识别（从客户输入文本中识别抗性类型）
	// 如果T向量里抗性类型为0（未设置），则从文本自动识别
	// 为什么放在引擎里？所有调用方都能自动受益，不用每个接口自己做
	// ============================================================
	if p.tVector[14] == 0 && p.input.CustomerInput != "" {
		detected := DetectResistance(p.input.CustomerInput)
		if detected > 0 {
			p.tVector[14] = float64(detected)
			log.Printf("[策略引擎] Step0: 自动识别抗性类型=%d (%s)",
				detected, resistanceName(detected))
		}
	}
}

// fillGreetingOutput 执行 Step0.1 命中后的落笔：纯寒暄强制不抛锚并预置安抚模板。
//
// 为什么单独拆出来：这是主链路唯一的"短路出口"——它把 output 的锚相关字段全部写成
// 零值/固定值（锚分/概率全零、置信度固定 0.99、模板固定 tpl_nothrow_001），
// 这些"指纹"是冒烟与单测判定寒暄分支的依据，集中一处才不会被后续步骤覆盖掉。
func (p *inferPhase) fillGreetingOutput() {
	log.Printf("[策略引擎] Step0.1: 检测到纯寒暄\"%s\"，强制不抛锚", p.input.CustomerInput)
	p.output.FinalAnchor = AnchorNoThrow
	p.output.OriginalAnchor = AnchorNoThrow
	p.output.AnchorConfidence = 0.99
	p.output.SoftDowngrade = false
	p.output.StageBeforeLock = AnchorNoThrow
	p.output.StageCeilingAgg = 0
	p.output.StageDowngraded = false
	// 不抛锚时选"不抛锚-安抚倾听"模板
	p.output.TemplateID = "tpl_nothrow_001"
	p.output.TemplateName = "不抛锚-安抚倾听"
	p.output.ExchangeFlag = false
	p.output.ExchangeType = ""
	p.output.AnchorScores = [AnchorCount]float64{}
	p.output.AnchorProbs = [AnchorCount]float64{}
	// 跳过后续Step1-3，直接进入路由决策
	// 结构重构批注（2026-09-28）：原先这条注释下面是一句 `goto afterAnchorSelection`；
	// 现在这一跳由 Infer 主体的 if/else 承担（命中寒暄即不进 scoreAnchors/chooseAnchor），
	// 落点与原标签处之后第一条语句（Step4 的入口判定）逐字相同。
}

// scoreAnchors 执行 Step1 ~ Step1.6：锚派发打分，随后叠加两道压制锁。
//
// 为什么单独拆出来：这三段合起来是"分数层的硬约束"（促单锁、线索阶段天花板），
// 它们都只改 anchorScores、不改 output，且必须**在** output.AnchorScores 赋值之前跑完
// ——P2-57 那条"展示层分数与决策层不一致"的回归就发生在这个边界上，边界单独成段最难写错。
func (p *inferPhase) scoreAnchors() {
	// ============================================================
	// Step1：锚派发打分
	// ============================================================
	p.anchorScores = Step1_CalcAnchorScores(p.tVector, p.input.State)

	// ============================================================
	// Step1.5：促单锁（CanPromote=false时压制稀缺/代价自担锚）
	// 业务规则：只有"已到店+已报价"之后才能促单
	// 防止AI在"到店体验"阶段就推"专属优惠""限时优惠"
	// ============================================================
	if !p.input.CanPromote {
		p.anchorScores = CalcAnchorScoresPromoteLocked(p.anchorScores)
		log.Printf("[策略引擎] Step1.5: 促单锁生效，CanPromote=false，稀缺/代价自担锚被压制")
	}

	// ============================================================
	// Step1.6：线索阶段天花板（业务漏斗硬锁）
	// P2-57 修复(2026-09-09)：output.AnchorScores 在此步之后再赋值（原 261 行赋值
	// 在压制前，展示层看到的分数与决策层不一致）。
	// ============================================================
	if p.input.JourneyStage != "" {
		if ceiling, ok := JourneyStageAggressivenessCeiling[p.input.JourneyStage]; ok {
			// 特殊处理：arrived+quoted不限制（由CanPromote控制促单）
			if p.input.JourneyStage == model.JourneyArrived && p.input.CanPromote {
				// arrived+quoted，不限制aggressiveness
			} else {
				// 强制降级：把所有超过ceiling的锚分数清零
				for a := 0; a < AnchorCount; a++ {
					if AnchorAggressiveness[a] > ceiling {
						p.anchorScores[a] = -999 // 确保不可能被选中
					}
				}
				log.Printf("[策略引擎] Step1.6: 线索阶段天花板生效，阶段=%s，agg上限=%d", p.input.JourneyStage, ceiling)
			}
		}
	}

	// P2-57 修复：压制后才赋值展示层——与决策层一致
	p.output.AnchorScores = p.anchorScores
}

// chooseAnchor 执行 Step2 ~ Step3：softmax 选锚 → 心智阶段锁 → 动态信号软降级。
//
// 为什么单独拆出来：这三步是同一个"修锚链"，输入是分数数组、输出是最终锚，
// 中间量（原始锚/概率/降级标记）只在本段被日志读取一次；跨段只交出 finalAnchor。
// stage 传进来的是 Infer 主体已 clamp 过的值（只用于展示 StageCeilingAgg），
// 而阶段锁本身仍收 input.State.CurrentStage 原值——原实现即如此，Step2_5 内部自带边界保护。
func (p *inferPhase) chooseAnchor(stage int) {
	// ============================================================
	// Step2：softmax归一化 + 选最优锚
	// ============================================================
	probs, bestAnchor, confidence := Step2_SoftmaxAnchor(p.anchorScores)
	p.output.AnchorProbs = probs
	p.output.SelectedAnchor = bestAnchor
	p.output.AnchorConfidence = confidence
	p.output.OriginalAnchor = bestAnchor

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
	stageCeilingAnchor, isStageDowngraded := Step2_5_StageCeiling(bestAnchor, p.input.State.CurrentStage)
	p.output.StageDowngraded = isStageDowngraded
	p.output.StageBeforeLock = bestAnchor                // 阶段锁降级前的锚
	p.output.StageCeilingAgg = StageAnchorCeiling[stage] // 当前阶段允许的上限（P2-57：stage 已 clamp）

	// 阶段锁降级后，用降级结果作为Step3的输入
	anchorAfterStageLock := stageCeilingAnchor

	if isStageDowngraded {
		log.Printf("[策略引擎] Step2.5: 阶段锁降级！原始锚=%d(%s,agg=%d) → 降级到=%d(%s,agg=%d), 阶段=%d(上限=%d)",
			bestAnchor, GetAnchorName(bestAnchor), AnchorAggressiveness[bestAnchor],
			stageCeilingAnchor, GetAnchorName(stageCeilingAnchor), AnchorAggressiveness[stageCeilingAnchor],
			p.input.State.CurrentStage, p.output.StageCeilingAgg)
	} else {
		log.Printf("[策略引擎] Step2.5: 阶段锁通过，锚=%d(%s,agg=%d) ≤ 阶段上限=%d, 阶段=%d",
			bestAnchor, GetAnchorName(bestAnchor), AnchorAggressiveness[bestAnchor],
			p.output.StageCeilingAgg, p.input.State.CurrentStage)
	}

	// ============================================================
	// Step3：软降级修锚（基于接钩率/沉默时长/情绪等动态信号）
	// 注意：输入是Step2.5降级后的锚，不是softmax的原始锚
	// ============================================================
	finalAnchor, isSoftDowngraded := Step3_SoftDowngrade(anchorAfterStageLock, p.input.State)
	p.finalAnchor = finalAnchor
	p.output.FinalAnchor = finalAnchor
	p.output.SoftDowngrade = isSoftDowngraded
	// OriginalAnchor始终记录softmax的原始锚（不含任何降级），便于追踪完整的降级链路
	p.output.OriginalAnchor = bestAnchor

	log.Printf("[策略引擎] Step1-3完整链路: softmax原始锚=%d(%s), 阶段锁降级=%v→%d(%s), 软降级=%v→最终锚=%d(%s), 置信度=%.2f",
		bestAnchor, GetAnchorName(bestAnchor),
		isStageDowngraded, anchorAfterStageLock, GetAnchorName(anchorAfterStageLock),
		isSoftDowngraded, p.finalAnchor, GetAnchorName(p.finalAnchor), confidence)
}

// recallTemplateAndExchange 执行 Step4（话术模板召回 + 卖点填充 + 条件交换判定）。
//
// 为什么单独拆出来：外层这段 if 是寒暄短路与正常链路的**共用汇合点**（原 afterAnchorSelection
// 标签之后的第一段），它靠 output 上已有的指纹判断"寒暄是否已把话术设好"，
// 判定与执行分离后，寒暄分支不必再复制一份跳过逻辑。
func (p *inferPhase) recallTemplateAndExchange() {
	// ============================================================
	// Step4：话术模板召回 + 卖点动态填充
	// 寒暄时已在Step0.1设好模板和话术，跳过Step4和条件交换
	//
	// M1 租户隔离修复（2026-08-25）：召回前按 input.TenantID 内存过滤。
	// 引擎缓存为全量加载（DefaultEngine 含所有租户私有数据），
	// 不过滤则租户A私有话术/卖点会进入租户B客户的AI召回池——跨租户泄露。
	// 规则：预置(tenant_id=0)全员可见；私有仅本租户；TenantID=0 fail-closed 只见预置。
	// ============================================================
	if p.output.FinalAnchor != AnchorNoThrow || p.output.TemplateID == "" {
		p.recallTemplate()
		p.resolveConditionExchange()
	}
}

// recallTemplate 执行 Step4 的召回与填充：取一致性快照 → 过滤可见域 → 选模板 → 填卖点。
//
// 为什么单独拆出来：这一段是整条链路里唯一的"缓存快照 + 可见域解析"消费点，
// e.snapshot() 与 service.ResolveRecallScope() 每次推理各只能调一次（快照调两次会读到
// 热重载中间的两个版本，模板池与卖点池就可能不同源），故把两者都关在本段内部传递。
func (p *inferPhase) recallTemplate() {
	// 三级包架构+KB继承链（2026-08-26）：解析可见域（链内①/跨部门回退④）
	snap := p.e.snapshot() // G4：本次推理取一致性快照，避免与热重载并发换切片撕裂
	recallScope := service.ResolveRecallScope(p.input.TenantID, p.input.DeptIDs)
	tenantTemplates := templatesForTenant(snap.templates, p.input.TenantID, recallScope)
	log.Printf("[策略引擎] Step4: 模板池过滤 全量=%d → 本租户可见=%d (tenant=%d 链内部门=%d 跨部门候选=%d)",
		len(snap.templates), len(tenantTemplates), p.input.TenantID,
		len(recallScope.OwnDepts), len(recallScope.CrossDepts))
	template, similarity := Step4_RecallTemplate(
		p.finalAnchor,
		p.input.CustomerTags,
		p.input.TVector,
		tenantTemplates,
		p.input.CustomerID, // E4：实验分桶按客户稳定哈希，0=退确定性最高分
		p.input.TenantID,   // 批五 B：择臂层按租户读 pack_stats 后验（开关默认关=纯规则）
	)

	if template != nil {
		p.output.TemplateID = template.ID
		p.output.TemplateName = template.Name
		// KB继承链：跨部门回退命中的内容打标（🌐跨部门·来自X部门库）
		if template.DepartmentID != nil && recallScope.CrossDepts[*template.DepartmentID] {
			p.output.TemplateName += " " + recallScope.CrossTags[*template.DepartmentID]
		}

		// 动态填充卖点（M1: 同规则过滤卖点库，防跨租户卖点串入）
		customer := &model.Customer{
			ID:              p.input.CustomerID,
			InterestProduct: modelFromTVector(p.tVector),
			Name:            "客户", // 这里简化，实际应从DB获取
		}
		promptText, hookText, _ := FillTemplate(template, customer, featuresForTenant(snap.features, p.input.TenantID, recallScope))
		p.output.PromptText = promptText
		p.output.HookText = hookText

		log.Printf("[策略引擎] Step4: 选中模板=%s(%s), 相似度=%.2f", template.ID, template.Name, similarity)
	} else {
		// 兜底话术
		p.output.PromptText = "感谢您的关注，请问有什么可以帮您的？"
		p.output.HookText = "您对哪方面比较感兴趣呢？"
		log.Printf("[策略引擎] Step4: 未找到匹配模板，使用兜底话术")
	}
}

// resolveConditionExchange 执行 Step4 尾部的条件交换判定与促单硬锁。
//
// 为什么单独拆出来：它是"代码层拦截"而不是 prompt 约束——CanPromote=false 时把
// CheckExchangeFlag 已经算出来的交换类型强行清空。这段的判据来自上一段的输出，
// 与召回本身没有数据依赖（不读快照也不查可见域），拆开后两条防线各自可核。
func (p *inferPhase) resolveConditionExchange() {
	// 条件交换检查
	resistanceType := resistanceTypeFromTVector(p.tVector)
	exchangeFlag, exchangeType := CheckExchangeFlag(p.finalAnchor, resistanceType)

	// 硬锁：CanPromote=false时，强制禁止条件交换
	// 不是靠prompt约束AI，而是代码层面直接拦截，AI根本看不到条件交换指令
	if !p.input.CanPromote && exchangeFlag {
		log.Printf("[策略引擎] 促单锁拦截条件交换：CanPromote=false，原交换类型=%s，强制关闭", exchangeType)
		exchangeFlag = false
		exchangeType = ""
	}

	p.output.ExchangeFlag = exchangeFlag
	p.output.ExchangeType = exchangeType

	if exchangeFlag {
		log.Printf("[策略引擎] 触发条件交换: 抗性=%s, 交换类型=%s", resistanceType, exchangeType)
	}
}
