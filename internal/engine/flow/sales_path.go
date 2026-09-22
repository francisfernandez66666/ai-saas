// Package flow 流程引擎：A4 实装（2026-09-23）——销售路径机（销售剧本真相源）。
//
// 审计裁定（PLAN_FIX_2026-09-22_AI_SALES_LOOP §5-A4 / §8-Q3）：本包的节点图此前只是
// 装饰——没有参与每轮回复决策。用户拍板"接真销售剧本"而非删除，本文件即落地：
//   - 节点 = 客户旅程阶段（model.Journey* 词表，与 customers.journey_stage 同源，
//     不另造第二套阶段词表；conversation 的 mental_stage(0-5) 是会话内心智阶段，
//     与本机的"漏斗阶段"是两个既有正交维度，本机只消费后者）；
//   - 边   = 转化条件，条件判定读真实数据（留资走 chatflow.IsLeadCaptured 真相源、
//     人工建联走 assigned_user_id、到店/成交走 journey_stage ∪ reply_attributions
//     .arrived_at/dealt_at〔迁移 018〕、接钩动能走 hooked），绝不写死布尔常量；
//   - 输出 = strategytypes.SalesPathDecision（当前阶段 + 目标阶段 + 转化信号 +
//     next_best_action），由 OrchestrateReply 每轮消费并经 ctx 下传 llm 注入 prompt。
//
// 纪律：只读判定，不改客户阶段（阶段推进是人工/AI 既有链路职权）；任何一步失败
// （开关关、客户缺失、查库出错、阶段未知、超时）一律 fail-open 返回 nil——
// 写法风格对齐 service/rerank.go（未配置/失败/异常全路径回退原样）。
package flow

import (
	"errors"
	"fmt"
	"log"
	"time"

	"ai-scrm/internal/analytics"
	"ai-scrm/internal/chatflow"
	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/runtimecfg"
	"ai-scrm/internal/strategytypes"
)

// 转化信号名（边的"条件"标识，随决策一起输出，供日志/下游观察；非阶段词表）
const (
	SignalLeadCaptured  = "lead_captured"  // 留资信号（阶段/标签真相源）
	SignalHumanAssigned = "human_assigned" // 人工建联信号（客户已归属顾问）
	SignalArrived       = "arrived"        // 到店信号（阶段 ∪ 归因终局列）
	SignalDealt         = "dealt"          // 成交信号（阶段 ∪ 归因终局列）
	SignalDelivered     = "delivered"      // 交付信号（阶段）
	salesPathMaxWalk    = 8                // 路径游走步数上限（节点数有限，防图配置错误死循环）
)

// ErrUnknownSalesStage 未知阶段码：不在 model.JourneyStageOrder 词表内即判失败，
// 调用方（fail-open 层）据此退回现状链路，而不是猜一个阶段继续跑。
var ErrUnknownSalesStage = errors.New("sales path: unknown journey stage")

// PathSignal 一条边绑定的转化信号类型
type PathSignal string

// PathEdge 销售路径机的一条边：From 阶段 --(信号为真即视为已转化)--> To 阶段，
// Action 为该边的下一步动作建议（行业中立措辞，泛行业租户不注入汽车专属词）。
type PathEdge struct {
	From   string     // 起点阶段码（model.Journey*）
	To     string     // 终点阶段码（model.Journey*）
	Signal PathSignal // 边条件绑定的真实信号
	Action string     // 下一步动作（next_best_action）
}

// PathSignals 一轮判定的全部真实信号快照（由 collectPathSignals 查库装配，纯函数消费）
type PathSignals struct {
	LeadCaptured   bool // 已留资（chatflow.IsLeadCaptured：阶段 ∪「已留资」标签）
	HumanAssigned  bool // 已人工建联（customers.assigned_user_id > 0）
	Arrived        bool // 已到店（阶段序 ≥ arrived ∪ reply_attributions.arrived_at 存在）
	Dealt          bool // 已成交（阶段 ∈ OrderedStages ∪ reply_attributions.dealt_at 存在）
	Delivered      bool // 已交付（阶段 = delivered）
	HookedRecently bool // 近 hook_days 天有接钩记录（动能，不作边条件——它不推进阶段，只加大推进力度）
}

// value 把信号快照解析成边条件真值（集中一处，避免边表里嵌闭包难测试）
func (s *PathSignals) value(sig PathSignal) bool {
	switch sig {
	case SignalLeadCaptured:
		return s.LeadCaptured
	case SignalHumanAssigned:
		return s.HumanAssigned
	case SignalArrived:
		return s.Arrived
	case SignalDealt:
		return s.Dealt
	case SignalDelivered:
		return s.Delivered
	default:
		return false // 未知信号名按不满足处理（fail-open，不阻断）
	}
}

// defaultSalesPathEdges 出厂销售剧本（节点=旅程阶段，边=转化条件）。
// 阶段词表完全复用 model.Journey*；human_connected 与 lead_captured 无固定先后
// （与 customer.go 状态机注释第 2 条一致），故两阶段各有一条到对方/到店方向的边。
// 同一节点多条出边时按声明顺序取先满足者；lost/delivered 无出边＝路径终点，不出建议。
var defaultSalesPathEdges = []PathEdge{
	{From: model.JourneyAIConnected, To: model.JourneyLeadCaptured, Signal: SignalLeadCaptured,
		Action: "先解决客户当前的核心顾虑，再自然地邀请客户留下联系方式（电话或微信），为后续跟进建立触点；客户拒绝时不要连续追问。"},
	{From: model.JourneyAIConnected, To: model.JourneyHumanConnected, Signal: SignalHumanAssigned,
		Action: "客户已表现出需要专人服务的信号：整理其关键诉求，推动转由专属顾问一对一建联跟进。"},
	{From: model.JourneyHumanConnected, To: model.JourneyLeadCaptured, Signal: SignalLeadCaptured,
		Action: "补全客户留资信息（电话或微信），确保建联后有可持续跟进的触点。"},
	{From: model.JourneyHumanConnected, To: model.JourneyArrived, Signal: SignalArrived,
		Action: "把客户从线上意向推向线下体验：邀请预约到店面访，用当面体验建立信任。"},
	{From: model.JourneyLeadCaptured, To: model.JourneyArrived, Signal: SignalArrived,
		Action: "把客户从线上意向推向线下体验：邀请预约到店面访，用当面体验建立信任。"},
	{From: model.JourneyArrived, To: model.JourneyOrdered, Signal: SignalDealt,
		Action: "推动成交决策：明确客户的剩余顾虑与决策标准，给出清晰的下单/签约路径。"},
	{From: model.JourneyOrdered, To: model.JourneyDelivered, Signal: SignalDelivered,
		Action: "跟进履约与交付体验，交付完成后顺势铺垫口碑复购。"},
}

// outgoingEdges 取某阶段节点的全部出边（声明顺序即优先级）
func outgoingEdges(stage string, edges []PathEdge) []PathEdge {
	var out []PathEdge
	for _, e := range edges {
		if e.From == stage {
			out = append(out, e)
		}
	}
	return out
}

// decideSalesPath 纯函数路径游走：从当前阶段出发，沿"条件已满足"的边前进到
// 转化信号已真实发生的最远节点（阶段列滞后于信号时以信号为准），停在第一条
// 未满足的边前——该边的目标阶段即本轮要推进的 TargetStage，边上的 Action 即
// next_best_action。返回 (nil, nil) 表示"路径已到终点/独立分支，本轮无推进建议"
// （如 delivered、lost），这不是错误。未知阶段返回 ErrUnknownSalesStage。
func decideSalesPath(stage string, sig *PathSignals, edges []PathEdge) (*strategytypes.SalesPathDecision, error) {
	if sig == nil {
		return nil, errors.New("sales path: nil signals")
	}
	// 空阶段按起点 ai_connected 处理（与 Customer.HasArrived 的同源兜底一致）
	if stage == "" {
		stage = model.JourneyAIConnected
	}
	if _, ok := model.JourneyStageOrder[stage]; !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownSalesStage, stage)
	}

	cur := stage
	visited := map[string]bool{cur: true}
	for step := 0; step < salesPathMaxWalk; step++ {
		var chosen *PathEdge
		for i := range edges {
			e := edges[i]
			if e.From != cur || visited[e.To] {
				continue
			}
			if sig.value(e.Signal) {
				chosen = &edges[i]
				break // 声明顺序即优先级，取先满足者
			}
		}
		if chosen == nil {
			break // 无满足的出边：停在这里
		}
		cur = chosen.To
		visited[cur] = true
	}

	// cur 上找第一条未满足的出边 = 本轮要攻克的转化条件
	edgesFrom := outgoingEdges(cur, edges)
	if len(edgesFrom) == 0 {
		return nil, nil // delivered/lost 等终点节点：无出边即无建议，fail-open 回现状
	}
	var target *PathEdge
	for i := range edgesFrom {
		if !sig.value(edgesFrom[i].Signal) {
			target = &edgesFrom[i]
			break
		}
	}
	if target == nil {
		target = &edgesFrom[0] // 理论上不可达（游走停在第一条未满足边），兜底取首条
	}

	return &strategytypes.SalesPathDecision{
		CurrentStage:     stage,
		CurrentStageName: model.JourneyStageNames[stage],
		TargetStage:      target.To,
		TargetStageName:  model.JourneyStageNames[target.To],
		ConversionSignal: string(target.Signal),
		NextBestAction:   target.Action,
		HookedRecently:   sig.HookedRecently,
	}, nil
}

// buildSalesPathDirective 把决策组装成注入 llm 的 prompt 段（措辞行业中立，
// 不出现车型/试驾等汽车专属词；并显式禁止模型把阶段词表复述给客户）。
func buildSalesPathDirective(d *strategytypes.SalesPathDecision) string {
	if d == nil {
		return ""
	}
	s := fmt.Sprintf("【销售路径】当前销售阶段：%s；本轮推进目标：%s。\n下一步动作：%s",
		d.CurrentStageName, d.TargetStageName, d.NextBestAction)
	if d.HookedRecently {
		s += "\n动能：客户近期对上轮话术有接钩回应，本轮可更主动地推进上述目标。"
	}
	s += "\n约束：只按上述目标组织话术策略；阶段名称是你的内部坐标，禁止对客户提及；禁止编造已发生的转化事实。"
	return s
}

// signalExists 归因表某信号列是否已出现（迁移 018 的 arrived_at/dealt_at 与 hooked）。
// 只读查询、显式带 tenant_id；任何 DB 错误向上抛给 fail-open 层，不做乐观兜底。
// windowDays>0 时限定 created_at 近窗（接钩动能用），=0 看历史全量（终局标签用）。
func signalExists(tenantID, customerID uint, where string, windowDays int) (bool, error) {
	if db.DB == nil {
		return false, errors.New("sales path: db not initialized")
	}
	var ids []uint
	q := db.DB.Model(&model.ReplyAttribution{}).
		Where("tenant_id = ? AND customer_id = ? "+where, tenantID, customerID)
	if windowDays > 0 {
		q = q.Where("created_at >= ?", time.Now().AddDate(0, 0, -windowDays))
	}
	if err := q.Limit(1).Pluck("id", &ids).Error; err != nil {
		return false, err
	}
	return len(ids) > 0, nil
}

// collectPathSignals 查库装配一轮判定的真实信号快照（A4 纪律：边判定必须读真实数据）。
// 阶段侧口径复用 analytics 的阶段集合谓词（与 AI 贡献度/终局回填同一真相源），
// 归因侧口径复用 reply_attributions.arrived_at/dealt_at/hooked（迁移 018）。
func collectPathSignals(customer *model.Customer, hookDays int) (*PathSignals, error) {
	if customer == nil || customer.ID == 0 {
		return nil, errors.New("sales path: invalid customer")
	}
	sig := &PathSignals{
		// 留资真相源：与 chatflow 主链完全同一函数（阶段 ∪「已留资」标签），不自造口径
		LeadCaptured:  chatflow.IsLeadCaptured(customer),
		HumanAssigned: customer.AssignedUserID > 0,
		Arrived:       analytics.InStageSet(customer.JourneyStage, analytics.ArrivedStages),
		Dealt:         analytics.InStageSet(customer.JourneyStage, analytics.OrderedStages),
		Delivered:     customer.JourneyStage == model.JourneyDelivered,
	}
	// 归因终局列并入信号：阶段列由人工/AI 既有链路推进，可能滞后于事实信号
	//（如小时任务已回填 arrived_at 但顾问还没改 journey_stage），路径机以"任一为真"为准。
	arrived, err := signalExists(customer.TenantID, customer.ID, "AND arrived_at IS NOT NULL", 0)
	if err != nil {
		return nil, fmt.Errorf("sales path: query arrived signal: %w", err)
	}
	sig.Arrived = sig.Arrived || arrived
	dealt, err := signalExists(customer.TenantID, customer.ID, "AND dealt_at IS NOT NULL", 0)
	if err != nil {
		return nil, fmt.Errorf("sales path: query dealt signal: %w", err)
	}
	sig.Dealt = sig.Dealt || dealt
	hooked, err := signalExists(customer.TenantID, customer.ID, "AND hooked", hookDays)
	if err != nil {
		return nil, fmt.Errorf("sales path: query hook signal: %w", err)
	}
	sig.HookedRecently = hooked
	return sig, nil
}

// EvaluateSalesPath 每轮回复链路的路径机评估入口（OrchestrateReply 消费）。
// 全路径 fail-open：热开关 sales_path_enabled 默认关（关闭时先于任何查库短路，
// 返回 nil ⇒ ctx 不被装饰 ⇒ 下游输出逐字节等价现状）；租户/客户无效、DB 出错、
// 阶段未知、路径终点无建议，一律返回 nil 并留日志，绝不阻断回复链路。
func EvaluateSalesPath(customer *model.Customer) *strategytypes.SalesPathDecision {
	if customer == nil || customer.TenantID == 0 || customer.ID == 0 {
		return nil
	}
	// 热开关（默认 false，租户可覆盖）：nil-service 时 GetBoolForTenant 自带守卫回退默认
	enabled := runtimecfg.DefaultSystemConfigService.GetBoolForTenant(
		customer.TenantID, "sales_path_enabled", false)
	if !enabled {
		return nil
	}
	hookDays := runtimecfg.DefaultSystemConfigService.GetIntForTenant(
		customer.TenantID, "sales_path_hook_days", 30)
	return evaluateSalesPathEval(customer, hookDays)
}

// evaluateSalesPathEval 开关之后的评估内核（独立成函数便于单测直调，不依赖热开关装配）。
// 失败语义与外层一致：出错/未知阶段/终点无建议 → nil（fail-open 回现状），只留日志。
func evaluateSalesPathEval(customer *model.Customer, hookDays int) *strategytypes.SalesPathDecision {
	sig, err := collectPathSignals(customer, hookDays)
	if err != nil {
		log.Printf("[销售路径] 信号采集失败(fail-open 回现状) tenant=%d customer=%d: %v",
			customer.TenantID, customer.ID, err)
		return nil
	}
	decision, err := decideSalesPath(customer.JourneyStage, sig, defaultSalesPathEdges)
	if err != nil {
		log.Printf("[销售路径] 判定失败(fail-open 回现状) tenant=%d customer=%d stage=%q: %v",
			customer.TenantID, customer.ID, customer.JourneyStage, err)
		return nil
	}
	if decision == nil {
		return nil // 路径终点（已交付/已战败）：无推进建议，现状输出
	}
	decision.PromptDirective = buildSalesPathDirective(decision)
	return decision
}
