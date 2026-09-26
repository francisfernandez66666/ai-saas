// 入队前分流裁决回归（FIX-9 能力缺口批，2026-09-27）
//
// 这一层以前没有单测，只有 web handler 内联的 if——于是"通道补两层分流"这种改动
// 没有任何机器判据能证明 web 与通道给同一句话的是同一个结论。本文件钉四件事：
//  1. 三层各归各位（硬边界 / 分支B / 分支C / 都不命中）；
//  2. **顺序铁律**：既含无关话题词、又含到店词的句子必须走硬边界（判据调换顺序会被这条抓住）；
//  3. 已留资阶段必须被第二层放行（CapturedSkipped 置真、Kind 为空）；
//  4. 手机号必须整串命中：前缀腿（`1[3-9]`）与尾部边界腿（去掉 `\b` 会切出前 11 位）各钉一条。
//
// 反证口径：每条断言都用的是"改坏判据就必红"的取值——例如把顺序换成先判到店，
// 用例 2 立刻拿到 StoreVisit；把 CapturedSkipped 忘在结构体外，用例 3 拿不到放行标记。
package chatflow

import (
	"testing"

	"ai-scrm/internal/model"
	"ai-scrm/internal/runtimecfg"
	"ai-scrm/internal/service"
)

// prerouteTenant 租户 ID 恒取 0：本文件要钉的是"判据本身"，行业词表/话术全部由下面的注入给出，
// 与租户覆盖层无关。取 0 还有个好处——service.TenantUsesAutoTalk(0) 直接短路返回中立口径，
// 不碰 DB，于是这是纯逻辑用例（无库环境照样跑，不会变成"本地绿 CI 红"）。
const prerouteTenant uint = 0

// 注入值与兜底值必须**字面不同**：断言相等即同时证明了"行业键真的被读到"
// （接线断了会拿到兜底文案，等值断言立刻红）。
const (
	prerouteOffTopicReply = "咱们还是聊回正事吧"
	prerouteVisitFirst    = "第一段：给你约个到店体验吧"
	prerouteLeadConfirm   = "已留资确认：说说你的需求吧"
)

// prerouteCfg 注入一份**只含本文件用到的关键词与话术**的行业配置。
// 为什么要注入：行业键缺省会回落到汽车词表，而汽车白名单里有"车/到店/试驾"——
// 用例 2 想构造"同时命中两层的句子"就必须能自己控制两张词表，否则用例含义随默认值漂移。
// 话术给成**单元素数组**：多元素时 GetStoreVisitFirstReply/GetLeadCapturedConfirmReply 走 rand，
// 断言会变成"其中之一"这种弱判据；等值断言要求选出的那条是确定的。
func prerouteCfg(t *testing.T) {
	t.Helper()
	restore := runtimecfg.SetDefaultForTest(runtimecfg.NewStaticService(map[string]string{
		service.IndustryTopicKeywords:       `["车","试驾","价格"]`,
		service.IndustryOffTopicKeywords:    `["天气","股票"]`,
		service.IndustryOffTopicReplies:     `["` + prerouteOffTopicReply + `"]`,
		service.IndustryVisitKeywords:       `["到店","过去看看"]`,
		service.IndustryStoreVisitFirst:     `["` + prerouteVisitFirst + `"]`,
		service.IndustryLeadCapturedConfirm: `["` + prerouteLeadConfirm + `"]`,
	}, nil))
	t.Cleanup(restore)
}

// TestDecidePreRoute_OffTopicBeatsStoreVisit 钉住"顺序铁律"：无关话题（硬边界）必须先于到店意图判定。
// 混合句"天气不错，我想到店看看"两层词都在，判据调换顺序即红——这是 web 与通道共用同一裁决的前提。
func TestDecidePreRoute_OffTopicBeatsStoreVisit(t *testing.T) {
	prerouteCfg(t)

	// 纯无关话题：命中第一层，且不进队列（Kind 非空即调用侧不该入队）
	got := DecidePreRoute(prerouteTenant, "今天天气怎么样", model.JourneyAIConnected)
	if got.Kind != PreRouteOffTopic {
		t.Fatalf("纯无关话题应命中硬边界，得 Kind=%q", got.Kind)
	}
	if got.Reply != prerouteOffTopicReply {
		t.Fatalf("硬边界话术必须取行业键注入值（取到兜底说明没接线），得 %q", got.Reply)
	}

	// 混合句：既含无关话题词（天气）又含到店词（到店）——顺序铁律要求**先判硬边界**。
	// 反证：把 DecidePreRoute 里两段 if 调换顺序，这里会拿到 StoreVisit。
	mixed := DecidePreRoute(prerouteTenant, "天气不错，我想到店看看", model.JourneyAIConnected)
	if mixed.Kind != PreRouteOffTopic {
		t.Fatalf("混合句必须由硬边界先接管（顺序铁律），得 Kind=%q", mixed.Kind)
	}
}

// TestDecidePreRoute_StoreVisitBranches 到店意图的两分支（B=句中带手机号当场留资、C=未留资先接住意向）
// 与手机号正则边界（前缀腿 `1[3-9]`、尾部 `\b` 腿）逐条钉住，并断言三句话术来自行业键而非兜底文案。
func TestDecidePreRoute_StoreVisitBranches(t *testing.T) {
	prerouteCfg(t)

	// 分支C：到店意图、句中无手机号 → 第一段接住意向
	noPhone := DecidePreRoute(prerouteTenant, "我周末想到店看看", model.JourneyAIConnected)
	if noPhone.Kind != PreRouteStoreVisit {
		t.Fatalf("到店意图且未留资应走分支C，得 Kind=%q", noPhone.Kind)
	}
	if noPhone.Reply != prerouteVisitFirst {
		t.Fatalf("分支C话术必须取行业键注入值（取到兜底说明没接线），得 %q", noPhone.Reply)
	}
	if noPhone.Phone != "" {
		t.Fatalf("分支C不该带手机号，得 %q", noPhone.Phone)
	}

	// 分支B：到店意图 + 句中带手机号 → 当场留资确认，且手机号是**整串**
	withPhone := DecidePreRoute(prerouteTenant, "我想到店看看，电话13800138000", model.JourneyAIConnected)
	if withPhone.Kind != PreRouteLeadConfirm {
		t.Fatalf("到店意图+手机号应走分支B，得 Kind=%q", withPhone.Kind)
	}
	if withPhone.Phone != "13800138000" {
		t.Fatalf("手机号应整串命中，得 %q", withPhone.Phone)
	}
	// 这条同时钉住 FIX-9 的文案单点：三句汽车口径已从 chat_main 搬进行业键，
	// 三条链（web 正式/web 免登录/通道）拿到的必须是**同一个**函数出的那一句。
	if withPhone.Reply != prerouteLeadConfirm {
		t.Fatalf("分支B确认话术必须取行业键注入值（三句硬编码已搬来，取错说明没接线），得 %q", withPhone.Reply)
	}

	// 手机号边界·前缀腿：`1[3-9]` 收紧为 `1\d` 时 "1234567890" 会被当成号（放行→分支B 必红）
	if got := DecidePreRoute(prerouteTenant, "我想到店看看，电话1234567890", model.JourneyAIConnected); got.Kind != PreRouteStoreVisit {
		t.Fatalf("10 位非法号不该判成留资分支，得 Kind=%q Phone=%q", got.Kind, got.Phone)
	}
	// 手机号边界·尾腿：去掉 `\b` 后 "13800138000123"（14 位连号）会被切出前 11 位当成手机号，
	// 客户收到的就不是"接住意向"而是"当场留资确认"——这条专门钉住那个尾部断言。
	long := DecidePreRoute(prerouteTenant, "我想到店看看，电话13800138000123", model.JourneyAIConnected)
	if long.Kind != PreRouteStoreVisit || long.Phone != "" {
		t.Fatalf("14 位连号不该被切成 11 位手机号，得 Kind=%q Phone=%q", long.Kind, long.Phone)
	}
}

// TestDecidePreRoute_CapturedStageSkipsFastPath 已越过留资期的客户（留资/到店/成交/交付）必须被第二层放行，
// 且放行要留下 CapturedSkipped 标记、不得顺手带话术或手机号；四个终局阶段与三个非终局阶段正反各钉一遍。
func TestDecidePreRoute_CapturedStageSkipsFastPath(t *testing.T) {
	prerouteCfg(t)

	// 已留资客户再喊到店 + 带手机号：两层都不该命中，但要留下"被阶段闸放行"的标记
	got := DecidePreRoute(prerouteTenant, "我想到店看看，电话13800138000", model.JourneyLeadCaptured)
	if got.Kind != PreRouteNone {
		t.Fatalf("已留资客户必须跳过到店快速通道，得 Kind=%q", got.Kind)
	}
	if !got.CapturedSkipped {
		t.Fatal("放行必须置 CapturedSkipped（调用侧那行排障日志依赖它；漏设=静默改语义）")
	}
	if got.Hit() {
		t.Fatal("放行即未命中，Hit() 必须为 false，否则调用侧会把这句话当快速通道直答")
	}
	// 放行时不得顺手带话术/手机号：带了调用侧会误投（旧实现是 goto 到正常流程，零输出）
	if got.Reply != "" || got.Phone != "" {
		t.Fatalf("放行时不得带话术/手机号，得 reply=%q phone=%q", got.Reply, got.Phone)
	}

	// 四个终局阶段一律放行；AI 接洽/人工建联/空阶段不放行（反证：白名单少写一个阶段就会红在这里）
	for _, st := range []string{model.JourneyLeadCaptured, model.JourneyArrived, model.JourneyOrdered, model.JourneyDelivered} {
		if !CapturedStage(st) {
			t.Errorf("阶段 %s 应判为已越过留资期", st)
		}
		if d := DecidePreRoute(prerouteTenant, "我想到店看看", st); d.Kind != PreRouteNone || !d.CapturedSkipped {
			t.Errorf("阶段 %s 下到店快速通道必须放行，得 Kind=%q skipped=%v", st, d.Kind, d.CapturedSkipped)
		}
	}
	for _, st := range []string{model.JourneyAIConnected, model.JourneyHumanConnected, ""} {
		if CapturedStage(st) {
			t.Errorf("阶段 %q 不该判为已留资", st)
		}
	}
}

// TestDecidePreRoute_NoHitFallsThroughToQueue 两层都不命中的普通询价句必须原样交回简单消息判定 +
// 合并队列（Kind=PreRouteNone 且无放行标记），否则等于把正常对话抢进罐头话术。
func TestDecidePreRoute_NoHitFallsThroughToQueue(t *testing.T) {
	prerouteCfg(t)

	// 既无关话题也不算到店：两层都不命中，交回简单消息判定 + 合并队列
	got := DecidePreRoute(prerouteTenant, "你们首付大概多少", model.JourneyAIConnected)
	if got.Hit() || got.CapturedSkipped {
		t.Fatalf("普通询价句不该被前两层截走，得 Kind=%q skipped=%v", got.Kind, got.CapturedSkipped)
	}
	if got.Reply != "" || got.Phone != "" {
		t.Fatalf("未命中时不得带话术/手机号（带了调用侧会误投），得 reply=%q phone=%q", got.Reply, got.Phone)
	}
}
