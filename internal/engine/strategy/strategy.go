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
	"sync/atomic"
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

// cachedData 策略引擎缓存快照（G4 修复，2026-09-14）：
// 模板/卖点以"整体换指针"的不可变快照形式发布，读侧一次 Load 拿到一致性视图，
// 杜绝旧实现"直接换切片头字段"与在途 Infer 读造成的数据竞态（torn read）。
type cachedData struct {
	templates []model.Template
	features  []model.Feature
}

// Engine 策略引擎结构体
// SaaS 化改造：支持多租户下的数据隔离
// TenantID=0 表示查询所有租户数据（启动时默认），>0 则严格隔离
type Engine struct {
	TenantID uint // 当前租户ID
	// data 缓存模板、卖点等数据（atomic.Value 承载不可变 *cachedData 快照），避免每次查库
	data atomic.Value
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
	log.Printf("已加载 %d 条卖点数据 (tenant=%d)", len(features), e.TenantID)

	// G4：整体发布新快照（读侧要么全旧要么全新，不会读到换到一半的切片头）
	e.data.Store(&cachedData{templates: templates, features: features})

	// 动态装载车型注册表（modelFromTVector 依赖），仅全量引擎(TenantID=0)刷新一次即可
	if e.TenantID == 0 {
		refreshCarModelRegistry()
	}
}

// snapshot 取当前缓存快照（从未加载返回空快照，调用方按空集降级）。
func (e *Engine) snapshot() *cachedData {
	if v := e.data.Load(); v != nil {
		return v.(*cachedData)
	}
	return &cachedData{}
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
	return e.snapshot().features
}

// Templates 获取当前加载的话术模板列表
func (e *Engine) Templates() []model.Template {
	return e.snapshot().templates
}

// Infer 执行一次完整的策略推理
// 输入：客户信息 + 会话状态 + 客户输入
// 输出：完整的策略决策结果
//
// 这是策略引擎的核心函数，串联7步公式
//
// 结构重构（2026-09-28，纯"剪切-粘贴"，行为零变化）：函数体按原有注释分段抽到
// inferPhase 的阶段方法上（infer_phases.go），本体只保留调用顺序与 Step5~7。
// 唯一形态变化：Step0.1 寒暄短路原先用 `goto afterAnchorSelection` 跳过 Step1~3，
// 现由下面的 if/else 承担，两条分支在同一处汇合后语句序列与原标签处之后逐字相同
// （goto 与标签之间只有 Step1~3，且标签后不存在被跳过的变量声明，故两者等价）。
func (e *Engine) Infer(input StrategyInput) StrategyOutput {
	// 一次推理一份阶段上下文：跨段共享的 T 向量与 output 累加器都收在这里，
	// 不用包级可变全局，也不新增任何并发面。
	p := &inferPhase{e: e, input: input, tVector: input.TVector}

	p.applyTagWeights() // Step0.5：标签权重注入T向量（后续锚打分完全依赖T向量）

	p.detectResistance() // Step0：T向量未设抗性类型时从客户输入文本自动识别

	// ============================================================
	// Step0.1：寒暄检测（语义级前置拦截）
	// 不管客户画像多高，纯寒暄强制不抛锚
	// 画像决定"聊什么"，对话阶段决定"能不能推"
	// "你好"→不抛锚（礼貌回应），"你好，请问极石多少钱"→正常走锚
	// ============================================================
	// P2-57 修复：CurrentStage 越界防御——当前心智阶段 0-5 共六段，StageAnchorCeiling 为
	// [6]int 数组，非法值（<0 或 ≥6）直接 panic。提前 clamp，后续所有引用 stage 统一用 clamped 值。
	// 声明位置须在所有 goto 标签之前，避免 Go "jumps over declaration" 编译错误。
	// ============================================================
	// 结构重构批注（2026-09-28）：本函数唯一的 goto 已改为 if/else，上面那句"声明位置须在所有
	// goto 标签之前"的约束至此不再成立，保留原文以留存 P2-57 的修复语境。
	stage := input.State.CurrentStage
	if stage < 0 {
		stage = 0
	} else if stage >= len(StageAnchorCeiling) {
		stage = len(StageAnchorCeiling) - 1
	}

	// ============================================================
	if IsGreeting(input.CustomerInput) {
		p.fillGreetingOutput() // Step0.1 命中：强制不抛锚，不进 Step1~3
	} else {
		p.scoreAnchors() // Step1 打分 + Step1.5 促单锁 + Step1.6 线索阶段天花板
		p.chooseAnchor(stage)
	}

	// 寒暄检测和正常锚选择链路在此汇合
	// 寒暄时跳过Step1-3，直接到这里；正常流程走完Step1-3也到这里

	p.recallTemplateAndExchange() // Step4：话术模板召回 + 卖点填充 + 条件交换

	// ============================================================
	// Step5：紧迫等级判定
	// ============================================================
	intentScore := p.tVector[0]
	urgencyLevel := Step5_CalcUrgency(intentScore, input.State.HighIntentRounds)
	p.output.UrgencyLevel = urgencyLevel

	log.Printf("[策略引擎] Step5: 紧迫等级=%s, 意向分=%.2f, 高意向轮数=%d",
		urgencyLevel, intentScore, input.State.HighIntentRounds)

	// ============================================================
	// Step6：路由决策
	// ============================================================
	routeResult, routeReason := Step6_RouteDecision(p.tVector, input.State, urgencyLevel, input.CustomerInput, input.TenantID)
	p.output.RouteResult = routeResult
	p.output.RouteReason = routeReason

	log.Printf("[策略引擎] Step6: 路由=%s, 原因=%s", routeResult, routeReason)

	// ============================================================
	// Step7：意向分反哺（增量）
	// ============================================================
	emotion := DetectEmotion(input.CustomerInput)
	hooked := CheckHooked(input.CustomerInput)
	newIntent := Step7_UpdateIntent(intentScore, input.CustomerInput, hooked, emotion)
	p.output.IntentDelta = newIntent - intentScore

	log.Printf("[策略引擎] Step7: 意向分变化=%.3f (%.3f → %.3f), 情绪=%s, 是否接钩=%v",
		p.output.IntentDelta, intentScore, newIntent, emotion, hooked)

	// Step5~7 留在主体：三步各自只有一行判定 + 一行日志，且 urgencyLevel / intentScore
	// 是相邻步之间的唯一传递量——再往外抽只会把这个主线切成三段。
	return p.output
}

// ============================================================
// 辅助函数
// ============================================================

// carModelRegistry 车型注册表（按 Sort/ID 升序，1 起始索引对应 code=1）
// 由 LoadData 从 car_models 表动态装载，替代原先硬编码的"越野SUV-X*"映射，
// 使不同行业包（车型体系不同）不再被写死字符串绑死。
// P2-5 修复(2026-09-15)：裸切片定时刷新整体重赋值 vs 请求 goroutine 并发读 = data race
// （同文件 templates/features 已用 atomic.Pointer，此处漏网）。改为原子指针交换整包快照。
var carModelRegistry atomic.Pointer[[]string]

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
	carModelRegistry.Store(&regs)
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
	registry := carModelRegistry.Load()
	if registry == nil {
		return ""
	}
	if modelCode > 0 && modelCode-1 < len(*registry) && (*registry)[modelCode-1] != "" {
		return (*registry)[modelCode-1]
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
		"贵不少", "贵很多", "偏贵", "划算", "便宜",
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
