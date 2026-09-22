// 销售路径机（A4 实装 2026-09-23）单元测试。
// 覆盖四类硬验收：①开关关闭零行为变化（不触库、ctx 不装饰）②边条件真值判定
// （留资/到店/成交各一，纯函数 + DB 双口径）③fail-open 退回现状（采集出错/未知阶段
// 恒返回 nil 不炸链路）④未知阶段不炸（ErrUnknownSalesStage → 上层降级 nil）。
// DB 用例沿用 testutil 哲学：本地无库 SKIP，CI Fatal 不静默绿。
package flow

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/runtimecfg"
	"ai-scrm/internal/strategytypes"
	"ai-scrm/internal/testutil"
)

// TestMain 统一出口：DB 用例跳过计数显式化（防"整包 DB 用例一个没跑"的静默绿）
func TestMain(m *testing.M) {
	os.Exit(testutil.RunMain(m))
}

// ---------------------------------------------------------------------------
// ② 边条件真值判定（纯函数口径：留资/到店/成交各一）
// ---------------------------------------------------------------------------

// TestDecideSalesPathLeadEdge 留资边：起点 ai_connected、信号全假 → 目标=lead_captured、
// 转化信号=lead_captured；留资信号已真但阶段列滞后（stale）时，路径机沿真边前进到
// lead_captured，本轮目标变为到店（arrived）——体现"边判定读真实信号而非阶段字面量"。
func TestDecideSalesPathLeadEdge(t *testing.T) {
	// 信号全假：目标应为留资
	d, err := decideSalesPath(model.JourneyAIConnected, &PathSignals{}, defaultSalesPathEdges)
	if err != nil || d == nil {
		t.Fatalf("纯函数判定不应失败: d=%+v err=%v", d, err)
	}
	if d.TargetStage != model.JourneyLeadCaptured || d.ConversionSignal != SignalLeadCaptured {
		t.Errorf("未留资客户应推进留资，得 %s(%s)", d.TargetStage, d.ConversionSignal)
	}
	// 留资信号为真（阶段滞后）：走过留资边，目标变到店
	sig := &PathSignals{LeadCaptured: true}
	d2, err := decideSalesPath(model.JourneyAIConnected, sig, defaultSalesPathEdges)
	if err != nil || d2 == nil {
		t.Fatalf("留资信号判定失败: %v", err)
	}
	if d2.TargetStage != model.JourneyArrived || d2.ConversionSignal != SignalArrived {
		t.Errorf("留资信号已真应推进到店目标，得 %s(%s)", d2.TargetStage, d2.ConversionSignal)
	}
}

// TestDecideSalesPathArrivedEdge 到店边：lead_captured 阶段，arrived 信号假→目标 arrived；
// arrived 信号真→走过到店边，目标变成交（dealt）。
func TestDecideSalesPathArrivedEdge(t *testing.T) {
	d, err := decideSalesPath(model.JourneyLeadCaptured, &PathSignals{LeadCaptured: true}, defaultSalesPathEdges)
	if err != nil || d == nil {
		t.Fatalf("到店边判定失败: %v", err)
	}
	if d.TargetStage != model.JourneyArrived || d.ConversionSignal != SignalArrived {
		t.Errorf("未到店客户目标应为到店，得 %s(%s)", d.TargetStage, d.ConversionSignal)
	}
	sig := &PathSignals{LeadCaptured: true, Arrived: true}
	d2, err := decideSalesPath(model.JourneyLeadCaptured, sig, defaultSalesPathEdges)
	if err != nil || d2 == nil {
		t.Fatalf("到店信号判定失败: %v", err)
	}
	if d2.TargetStage != model.JourneyOrdered || d2.ConversionSignal != SignalDealt {
		t.Errorf("已到店客户目标应为成交，得 %s(%s)", d2.TargetStage, d2.ConversionSignal)
	}
}

// TestDecideSalesPathDealtEdge 成交边：arrived 阶段，dealt 假→目标 ordered；
// dealt 真→走过成交边到 ordered，目标 delivered。
func TestDecideSalesPathDealtEdge(t *testing.T) {
	d, err := decideSalesPath(model.JourneyArrived, &PathSignals{LeadCaptured: true, Arrived: true}, defaultSalesPathEdges)
	if err != nil || d == nil {
		t.Fatalf("成交边判定失败: %v", err)
	}
	if d.TargetStage != model.JourneyOrdered || d.ConversionSignal != SignalDealt {
		t.Errorf("已到店未成交目标应为下单，得 %s(%s)", d.TargetStage, d.ConversionSignal)
	}
	sig := &PathSignals{LeadCaptured: true, Arrived: true, Dealt: true}
	d2, err := decideSalesPath(model.JourneyArrived, sig, defaultSalesPathEdges)
	if err != nil || d2 == nil {
		t.Fatalf("成交信号判定失败: %v", err)
	}
	if d2.TargetStage != model.JourneyDelivered || d2.ConversionSignal != SignalDelivered {
		t.Errorf("已成交客户目标应为交付，得 %s(%s)", d2.TargetStage, d2.ConversionSignal)
	}
}

// TestDecideSalesPathTerminalAndBranchNodes 终点/独立分支：delivered 与 lost 无出边，
// 返回 (nil, nil)＝"本轮无推进建议"（不是错误）；全信号真的 stale 客户游走至终点同理。
func TestDecideSalesPathTerminalAndBranchNodes(t *testing.T) {
	for _, stage := range []string{model.JourneyDelivered, model.JourneyLost} {
		d, err := decideSalesPath(stage, &PathSignals{}, defaultSalesPathEdges)
		if err != nil || d != nil {
			t.Errorf("%s 应返回 (nil,nil)，得 d=%+v err=%v", stage, d, err)
		}
	}
	all := &PathSignals{LeadCaptured: true, HumanAssigned: true, Arrived: true, Dealt: true, Delivered: true}
	d, err := decideSalesPath(model.JourneyAIConnected, all, defaultSalesPathEdges)
	if err != nil || d != nil {
		t.Errorf("全信号真应游走至终点后返回 (nil,nil)，得 d=%+v err=%v", d, err)
	}
}

// ---------------------------------------------------------------------------
// ④ 未知阶段不炸
// ---------------------------------------------------------------------------

// TestDecideSalesPathUnknownStage 未知阶段码：显式 ErrUnknownSalesStage（词表外拒绝放行，
// 与 Customer.HasArrived 对未知阶段的 fail-closed 口径一致）；空串按起点处理。
func TestDecideSalesPathUnknownStage(t *testing.T) {
	_, err := decideSalesPath("not_a_stage", &PathSignals{}, defaultSalesPathEdges)
	if !errors.Is(err, ErrUnknownSalesStage) {
		t.Errorf("未知阶段应报 ErrUnknownSalesStage，得 %v", err)
	}
	d, err := decideSalesPath("", &PathSignals{}, defaultSalesPathEdges)
	if err != nil || d == nil || d.CurrentStage != model.JourneyAIConnected {
		t.Errorf("空阶段应兜底为 ai_connected，得 d=%+v err=%v", d, err)
	}
	// 内核层（EvaluateSalesPath 去开关后的部分）：未知阶段客户 → fail-open nil，不 panic
	d2 := evaluateSalesPathEval(&model.Customer{ID: 1, TenantID: 1, JourneyStage: "weird"}, 30)
	if d2 != nil {
		t.Errorf("未知阶段经内核评估应降级 nil，得 %+v", d2)
	}
}

// ---------------------------------------------------------------------------
// ① 开关关闭零行为变化
// ---------------------------------------------------------------------------

// TestEvaluateSalesPathSwitchOffZeroChange 默认关闭：nil 配置服务下 GetBoolForTenant
// 回退默认 false，EvaluateSalesPath 在任何查库之前短路返回 nil——db.DB 未初始化
// （测试环境）若误触库会直接 panic，返回 nil 即证明"关闭时零查询零装饰"。
func TestEvaluateSalesPathSwitchOffZeroChange(t *testing.T) {
	// 保存并强制 nil-service（本包其余用例不依赖该全局）
	saved := runtimecfg.DefaultSystemConfigService
	runtimecfg.DefaultSystemConfigService = nil
	defer func() { runtimecfg.DefaultSystemConfigService = saved }()

	if got := EvaluateSalesPath(&model.Customer{ID: 1, TenantID: 7, JourneyStage: model.JourneyAIConnected}); got != nil {
		t.Errorf("开关默认关应恒返回 nil，得 %+v", got)
	}
	if got := fetchSalesPath(7, &model.Customer{ID: 1, TenantID: 7}); got != nil {
		t.Errorf("fetchSalesPath 关闭时应恒 nil（不起协程不查库），得 %+v", got)
	}
}

// TestSalesPathCtxRoundTrip ctx 载体：nil 决策 WithSalesPath 原样返回（关闭路径下
// llm 读取端恒 nil 的机制保证）；非 nil 决策可往返；空 ctx 读取为 nil。
func TestSalesPathCtxRoundTrip(t *testing.T) {
	base := context.Background()
	if got := strategytypes.WithSalesPath(base, nil); got != base {
		t.Errorf("nil 决策不应包装 ctx")
	}
	if got := strategytypes.SalesPathFromContext(base); got != nil {
		t.Errorf("未挂载时读取应为 nil")
	}
	d := &strategytypes.SalesPathDecision{CurrentStage: model.JourneyAIConnected, TargetStage: model.JourneyLeadCaptured}
	ctx := strategytypes.WithSalesPath(base, d)
	if back := strategytypes.SalesPathFromContext(ctx); back != d {
		t.Errorf("ctx 往返不一致: %+v", back)
	}
}

// TestBuildSalesPathDirective prompt 段组装：含阶段名与动作；接钩动能仅在真时出现。
func TestBuildSalesPathDirective(t *testing.T) {
	d := &strategytypes.SalesPathDecision{
		CurrentStageName: "AI建联", TargetStageName: "已留资",
		NextBestAction: "邀请客户留下联系方式",
	}
	s := buildSalesPathDirective(d)
	if s == "" || !strings.Contains(s, "AI建联") || !strings.Contains(s, "邀请客户留下联系方式") {
		t.Errorf("注入段缺关键字: %q", s)
	}
	if strings.Contains(s, "动能") {
		t.Errorf("无接钩记录不应出现动能句: %q", s)
	}
	d.HookedRecently = true
	if !strings.Contains(buildSalesPathDirective(d), "动能") {
		t.Errorf("有接钩记录应出现动能句")
	}
	if buildSalesPathDirective(nil) != "" {
		t.Errorf("nil 决策应返回空串")
	}
}

// ---------------------------------------------------------------------------
// ③ fail-open（DB 口径）+ 边条件真值（真实数据口径）
// ---------------------------------------------------------------------------

// TestSalesPathSignalsFromDB 真库断言"边判定读真实数据"：
// 留资/到店/成交信号分别由 customers 列与 reply_attributions（迁移 018）驱动，
// 且未知阶段/无 DB 场景恒降级 nil。本地无库 SKIP、CI Fatal（testutil 语义）。
func TestSalesPathSignalsFromDB(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)

	// 新客户：ai_connected、无归因行 → 目标=留资（信号采集自真实空表，全假）
	cust := &model.Customer{TenantID: tid, Name: "路径机测试客", JourneyStage: model.JourneyAIConnected}
	if err := db.DB.Create(cust).Error; err != nil {
		t.Fatalf("建测试客户失败: %v", err)
	}
	d := evaluateSalesPathEval(cust, 30)
	if d == nil || d.TargetStage != model.JourneyLeadCaptured {
		t.Fatalf("无信号新客户应目标留资，得 %+v", d)
	}
	if d.HookedRecently {
		t.Errorf("无归因行不应标记接钩动能")
	}

	// 接钩动能：近窗 hooked=true 归因行 → HookedRecently 真（真实数据驱动）
	// message_id 全局唯一索引：取 2e9 起步的纳秒偏移，避开 dev 库真实消息 ID 区间
	baseMsgID := uint(2000000000 + time.Now().UnixNano()%500000000)
	attr := &model.ReplyAttribution{TenantID: tid, MessageID: baseMsgID, CustomerID: cust.ID, Hooked: true}
	if err := db.DB.Create(attr).Error; err != nil {
		t.Fatalf("建归因行失败: %v", err)
	}
	d2 := evaluateSalesPathEval(cust, 30)
	if d2 == nil || !d2.HookedRecently {
		t.Fatalf("hooked 归因行应点亮动能信号，得 %+v", d2)
	}

	// 到店信号（阶段侧真值）：journey_stage=lead_captured + arrived_at 回填 → 目标=成交
	cust2 := &model.Customer{TenantID: tid, Name: "路径机到店客", JourneyStage: model.JourneyLeadCaptured}
	if err := db.DB.Create(cust2).Error; err != nil {
		t.Fatalf("建测试客户2失败: %v", err)
	}
	now := time.Now()
	attr2 := &model.ReplyAttribution{TenantID: tid, MessageID: baseMsgID + 1, CustomerID: cust2.ID, ArrivedAt: &now}
	if err := db.DB.Create(attr2).Error; err != nil {
		t.Fatalf("建到店归因行失败: %v", err)
	}
	d3 := evaluateSalesPathEval(cust2, 30)
	if d3 == nil || d3.TargetStage != model.JourneyOrdered || d3.ConversionSignal != SignalDealt {
		t.Fatalf("arrived_at 真信号应把目标推到成交，得 %+v", d3)
	}

	// 成交信号（归因列驱动）：补 dealt_at → 走过成交边，目标=交付
	attr2Copy := *attr2
	if err := db.DB.Model(&model.ReplyAttribution{}).Where("id = ?", attr2Copy.ID).Update("dealt_at", now).Error; err != nil {
		t.Fatalf("回填 dealt_at 失败: %v", err)
	}
	d4 := evaluateSalesPathEval(cust2, 30)
	if d4 == nil || d4.TargetStage != model.JourneyDelivered {
		t.Fatalf("dealt_at 真信号应把目标推到交付，得 %+v", d4)
	}

	// fail-open：db 正常但采集入参无效（客户零 ID）→ nil 不炸
	if got := evaluateSalesPathEval(&model.Customer{ID: 0, TenantID: tid}, 30); got != nil {
		t.Errorf("无效客户应 fail-open nil，得 %+v", got)
	}
}
