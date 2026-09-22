// Package strategytypes 销售路径机（A4 实装，2026-09-23）的中立输出类型与 context 载体。
//
// 为什么放本包：路径机实体在 internal/engine/flow（节点=客户旅程阶段、边=转化条件），
// 但其结构化输出要经 OrchestrateReply → strategy.GenerateReply → llm 的既有调用链下钻，
// 而 llm/strategy 都不允许反向 import engine/flow（依赖方向红线）。本包是仓库既有的
// "策略中立类型层"（断 ai→strategy 环），flow 写、llm 读，两侧都只依赖本包，不成环。
package strategytypes

import "context"

// salesPathCtxKey 销售路径决策的 context 键（私有类型防碰撞）
type salesPathCtxKey struct{}

// SalesPathDecision 销售路径机每轮输出的结构化结果。
// 语义：只读判定——描述"客户当前在销售路径的哪个节点、下一个可推进的转化节点是哪里、
// 本轮为到达它该做的下一步动作"，**不携带任何改客户阶段的指令**（阶段推进仍归人工/AI 既有链路）。
type SalesPathDecision struct {
	CurrentStage     string // 当前销售阶段码（model.Journey* 词表，与 customers.journey_stage 同源）
	CurrentStageName string // 当前阶段中文名（JourneyStageNames）
	TargetStage      string // 下一个待推进的阶段节点
	TargetStageName  string // 目标阶段中文名
	ConversionSignal string // 该边（转化条件）绑定的真实信号名：lead_captured/human_assigned/arrived/dealt/delivered
	NextBestAction   string // 到达目标阶段的下一步动作（每轮建议，供 prompt 消费）
	HookedRecently   bool   // 近窗内该客户有接钩记录（reply_attributions.hooked=true）——推进动能信号
	PromptDirective  string // 组装好的注入段（flow 侧生成，llm 侧只做拼接，开关关闭时整条链路为 nil）
}

// WithSalesPath 把路径机决策挂到 ctx 上（下游 strategy→llm 透传，不改任何函数签名）。
// decision 为 nil 时原样返回 ctx——这是"默认零行为变化"的机制保证：
// 开关关闭/任何失败路径下 ctx 不带任何值，llm 读取处恒为 nil，输出逐字节等价。
func WithSalesPath(ctx context.Context, decision *SalesPathDecision) context.Context {
	if ctx == nil || decision == nil {
		return ctx
	}
	return context.WithValue(ctx, salesPathCtxKey{}, decision)
}

// SalesPathFromContext 读取 ctx 上挂载的销售路径决策；未挂载返回 nil（消费方按现状走）。
func SalesPathFromContext(ctx context.Context) *SalesPathDecision {
	if ctx == nil {
		return nil
	}
	d, _ := ctx.Value(salesPathCtxKey{}).(*SalesPathDecision)
	return d
}
