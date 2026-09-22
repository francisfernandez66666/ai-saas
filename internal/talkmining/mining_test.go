// Package talkmining 纯函数层单测：分组键、按转化率排序、样本不足封堵、PII 脱敏、近似重复归并。
// 全部零 DB——这正是 mining.go 收敛为纯函数的目的（口径可钉死）。
package talkmining

import (
	"fmt"
	"strings"
	"testing"
)

// mk 便捷构造顾问消息记录（默认无锚位上下文，按阶段归并）。
func mk(advisor, customer uint, stage, outcome, content string) Record {
	return Record{AdvisorID: advisor, AnchorType: Unanchored, Stage: stage,
		CustomerID: customer, Content: content, Outcome: outcome}
}

// TestMineGroupKeySeparation 分组键正确性：顾问不同、阶段不同都必须拆簇（不得混学）；
// 同一客户连发多条只计 1 个样本（按客户去重，防把话痨客户的 10 条消息当 10 单证据）。
func TestMineGroupKeySeparation(t *testing.T) {
	recs := []Record{
		mk(1, 10, "lead_captured", OutcomeArrived, "哥，周六店里有个试驾会，顺路过来坐坐？"),
		mk(1, 10, "lead_captured", OutcomeArrived, "哥，周六店里有个试驾会，顺路过来坐坐？加您微信发定位"),
		mk(1, 11, "lead_captured", OutcomeNone, "哥，周六店里有个试驾会，顺路过来坐坐？"),
		mk(2, 12, "arrived", OutcomeDealt, "今天到店感受如何？名额我先帮您留一下"),
	}
	clusters := Mine(recs, Options{})
	if len(clusters) != 2 {
		t.Fatalf("期望按 顾问×阶段 拆成 2 簇，实得 %d: %+v", len(clusters), clusters)
	}
	var a1, a2 *Cluster
	for i := range clusters {
		switch clusters[i].Key.AdvisorID {
		case 1:
			a1 = &clusters[i]
		case 2:
			a2 = &clusters[i]
		}
	}
	if a1 == nil || a2 == nil {
		t.Fatal("分组键丢失")
	}
	if a1.Samples != 2 {
		t.Fatalf("同客户多条消息应去重为 2 样本（客户10+客户11），实得 %d", a1.Samples)
	}
	if a1.MessageCount != 3 {
		t.Fatalf("消息总数应为 3，实得 %d", a1.MessageCount)
	}
	if a1.Conversions != 1 || a1.ConvRate != 0.5 {
		t.Fatalf("客户10到店、客户11未达，转化率应 1/2，实得 %d/%.2f", a1.Conversions, a1.ConvRate)
	}
}

// TestMineSortsByConvRateNotVolume 排序必须按转化率而非样本量：
// 大簇=顾问日常寒暄，若按量排，挖出来的永远是"说得最多的"而不是"卖得动的"。
func TestMineSortsByConvRateNotVolume(t *testing.T) {
	var recs []Record
	// 簇 A：20 个客户，10 个到店 → 转化 50%
	for i := uint(1); i <= 20; i++ {
		outcome := OutcomeNone
		if i <= 10 {
			outcome = OutcomeArrived
		}
		recs = append(recs, mk(1, 100+i, "lead_captured", outcome, fmt.Sprintf("话术A：约试驾的标准句式，客户%d", i)))
	}
	// 簇 B：5 个客户（恰达门槛），4 个到店 → 转化 80%
	for i := uint(1); i <= 5; i++ {
		outcome := OutcomeNone
		if i <= 4 {
			outcome = OutcomeDealt
		}
		recs = append(recs, mk(2, 500+i, "lead_captured", outcome, fmt.Sprintf("话术B：高转的小样本句式，客户%d%s", i, strings.Repeat("字", 8))))
	}
	clusters := Mine(recs, Options{MinSamples: 5, TargetOutcome: OutcomeArrived})
	if len(clusters) != 2 {
		t.Fatalf("期望 2 簇，实得 %d", len(clusters))
	}
	first := clusters[0]
	if first.Key.AdvisorID != 2 || first.ConvRate <= clusters[1].ConvRate {
		t.Fatalf("首位应为小样本高转簇(80%%)，实得 advisor=%d rate=%.2f，次位 rate=%.2f",
			first.Key.AdvisorID, first.ConvRate, clusters[1].ConvRate)
	}
}

// TestMineInsufficientSamples 样本不足组必须显式标记且排在结论位之后——
// 下游 DraftTemplates 依此拒绝出稿，样本不足不得输出结论。
func TestMineInsufficientSamples(t *testing.T) {
	var recs []Record
	for i := uint(1); i <= 3; i++ {
		recs = append(recs, mk(9, 900+i, "arrived", OutcomeDealt, fmt.Sprintf("小样本簇：战败风险话术%d%s", i, strings.Repeat("字", 6))))
	}
	for i := uint(1); i <= 6; i++ {
		recs = append(recs, mk(8, 800+i, "lead_captured", OutcomeArrived, fmt.Sprintf("达标簇：正常推进话术，六个客户都到店了%d", i)))
	}
	clusters := Mine(recs, Options{MinSamples: 5})
	if len(clusters) != 2 {
		t.Fatalf("期望 2 簇，实得 %d", len(clusters))
	}
	if clusters[0].Insufficient {
		t.Fatal("结论位首位不应是不足样本组")
	}
	last := clusters[len(clusters)-1]
	if !last.Insufficient || last.Key.AdvisorID != 9 {
		t.Fatalf("末位应为 advisor=9 的 insufficient_samples 组，实得 %+v", last)
	}
	if last.Samples != 3 {
		t.Fatalf("不足组样本数应为 3，实得 %d", last.Samples)
	}
}

// TestMineMasksPIIInRepresentative 隐私红线：代表原文入库/出稿前必须过手机号掩码。
func TestMineMasksPIIInRepresentative(t *testing.T) {
	var recs []Record
	for i := uint(1); i <= 6; i++ {
		recs = append(recs, mk(3, 300+i, "lead_captured", OutcomeArrived,
			"您的号码13812345678我记下了，到店报这个号就行"+string(rune('A'+i))))
	}
	clusters := Mine(recs, Options{MinSamples: 5})
	if len(clusters) != 1 {
		t.Fatalf("期望 1 簇，实得 %d", len(clusters))
	}
	rep := clusters[0].Representative
	if strings.Contains(rep, "13812345678") {
		t.Fatalf("产物泄露明文手机号: %s", rep)
	}
	if !strings.Contains(rep, "138****5678") {
		t.Fatalf("产物未含掩码号，实得: %s", rep)
	}
}

// TestMineMergesNearDuplicates 近似重复归并：同一句带标点/空白/表情/全角变体应聚为 1 个变体，
// 代表取消息数最多的变体首见原文。
func TestMineMergesNearDuplicates(t *testing.T) {
	var recs []Record
	texts := []string{
		"周六有试驾会，过来坐坐？",
		"周六有试驾会 过来坐坐",
		"周六有试驾会！！过来坐坐😊",
	}
	for i, txt := range texts {
		recs = append(recs, mk(4, uint(400+i), "lead_captured", OutcomeArrived, txt))
	}
	// 另一个真正不同的句式（只 1 条，不该抢代表位）
	recs = append(recs, mk(4, 410, "lead_captured", OutcomeNone, "另一句完全不同的话术内容"))
	clusters := Mine(recs, Options{MinSamples: 2})
	if len(clusters) != 1 {
		t.Fatalf("期望 1 簇，实得 %d", len(clusters))
	}
	c := clusters[0]
	if c.VariantCount != 2 {
		t.Fatalf("近似重复应归并为 2 个变体（3+1），实得 %d", c.VariantCount)
	}
	if c.MessageCount != 4 {
		t.Fatalf("消息总数应为 4，实得 %d", c.MessageCount)
	}
	if !strings.HasPrefix(c.Representative, "周六有试驾会") {
		t.Fatalf("代表应为多数变体的首见原文，实得: %s", c.Representative)
	}
}

// TestMineSignatureStable 签名幂等性：同输入两跑签名必等（Draft 层判重依赖它）。
func TestMineSignatureStable(t *testing.T) {
	recs := []Record{
		mk(5, 501, "arrived", OutcomeArrived, "到店后推个限时权益，成交顺一点"),
		mk(5, 502, "arrived", OutcomeDealt, "到店后推个限时权益，成交顺一点"),
	}
	c1 := Mine(recs, Options{MinSamples: 2})
	c2 := Mine(recs, Options{MinSamples: 2})
	if len(c1) != 1 || len(c2) != 1 {
		t.Fatalf("期望各 1 簇，实得 %d/%d", len(c1), len(c2))
	}
	if c1[0].Signature != c2[0].Signature || len(c1[0].Signature) != 8 {
		t.Fatalf("签名应稳定且为 8 位十六进制: %q vs %q", c1[0].Signature, c2[0].Signature)
	}
}

// TestMineDropsNoiseAndOrphans 归一化过短的寒暄、无顾问/无客户归属的孤儿消息不得进簇。
func TestMineDropsNoiseAndOrphans(t *testing.T) {
	recs := []Record{
		mk(6, 601, "lead_captured", OutcomeArrived, "好的"),         // 归一化后 2 字 < 默认下限 4
		mk(0, 602, "lead_captured", OutcomeArrived, "没有顾问归属的消息体"), // advisor=0 无法归因
		mk(6, 0, "lead_captured", OutcomeArrived, "没有客户归属的消息体"),   // customer=0 虚增样本
		mk(6, 603, "lead_captured", OutcomeArrived, "这是一条合格的推进话术示例"),
	}
	clusters := Mine(recs, Options{})
	// 三条垃圾（寒暄过短/advisor=0/customer=0）必须都被丢弃，只剩 1 条有效消息成簇：
	// 样本 1、被标记不足（默认门槛 5），绝不能把孤儿消息计成样本。
	if len(clusters) != 1 {
		t.Fatalf("期望仅 1 簇（垃圾记录应被丢弃），实得 %d: %+v", len(clusters), clusters)
	}
	if clusters[0].Samples != 1 || !clusters[0].Insufficient {
		t.Fatalf("有效簇应 1 样本且标记不足: %+v", clusters[0])
	}
}

// TestOutcomeFromStageMapping 阶段→终局档位映射：ordered/delivered 算成交，arrived 算到店，
// lost 显式归 none（战败绝不能进转化分子）。
func TestOutcomeFromStageMapping(t *testing.T) {
	cases := map[string]string{
		"ai_connected":  OutcomeNone,
		"lead_captured": OutcomeNone,
		"arrived":       OutcomeArrived,
		"ordered":       OutcomeDealt,
		"delivered":     OutcomeDealt,
		"lost":          OutcomeNone,
		"unknown_xxx":   OutcomeNone,
	}
	for stage, want := range cases {
		if got := OutcomeFromStage(stage); got != want {
			t.Fatalf("OutcomeFromStage(%s)=%s，期望 %s", stage, got, want)
		}
	}
}
