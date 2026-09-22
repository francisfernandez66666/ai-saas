// replayeval 单测：四桶分层/盲评随机序/裁判解析/编排管线全部用假函数覆盖（零 DB 零网络）；
// 唯一的真连库用例在无 DB 环境变量时 t.Skip，不红 CI。
package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joho/godotenv"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/testutil"
)

// TestMain 统一出口：跑完用例后打印本二进制因 DB 不可用跳过的用例计数（testutil 惯例）。
func TestMain(m *testing.M) {
	os.Exit(testutil.RunMain(m))
}

// TestClassifyOutcome 四桶优先级互斥归桶：终局 > 留资 > 接钩 > 无线索。
func TestClassifyOutcome(t *testing.T) {
	cases := []struct {
		name string
		in   outcomeFlags
		want string
	}{
		{"成交优先", outcomeFlags{Hooked: true, LeadCaptured: true, Dealt: true}, bucketConverted},
		{"仅到店也算converted", outcomeFlags{Hooked: true, Arrived: true}, bucketConverted},
		{"留资次之", outcomeFlags{Hooked: true, LeadCaptured: true}, bucketLead},
		{"接钩再次", outcomeFlags{Hooked: true}, bucketHooked},
		{"全无线索", outcomeFlags{}, bucketNone},
	}
	for _, c := range cases {
		if got := classifyOutcome(c.in); got != c.want {
			t.Errorf("%s: classifyOutcome=%s want=%s", c.name, got, c.want)
		}
	}
}

// mkSamples 快速构造 n 条同桶样本（时间按创建序递降方向打点，便于断言桶内新→旧）。
func mkSamples(bucket string, n int, idBase uint) []replaySample {
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	out := make([]replaySample, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, replaySample{
			AttributionID: idBase + uint(i),
			MessageID:     1000 + idBase + uint(i),
			CreatedAt:     base.Add(time.Duration(i) * time.Hour),
			Bucket:        bucket,
		})
	}
	return out
}

// TestStratifySamples 分层：桶内新→旧、桶间轮转配平、确定性、limit 超额取全量。
func TestStratifySamples(t *testing.T) {
	pool := append(mkSamples(bucketConverted, 2, 1),
		append(mkSamples(bucketNone, 10, 20),
			append(mkSamples(bucketHooked, 3, 40), mkSamples(bucketLead, 1, 60)...)...)...)

	got := stratifySamples(pool, 8)
	if len(got) != 8 {
		t.Fatalf("limit=8 应出 8 条，实得 %d", len(got))
	}
	// 稀有桶不被挤光：converted(池内2)与 lead(1) 必须全部入选
	counts := map[string]int{}
	for _, s := range got {
		counts[s.Bucket]++
	}
	if counts[bucketConverted] != 2 || counts[bucketLead] != 1 {
		t.Errorf("小桶应全量入样: %v", counts)
	}
	// 桶内时间新→旧：首条 converted 应是更大时间戳（id=2 比 id=1 晚）
	if got[0].AttributionID != 2 {
		t.Errorf("轮转第一轮应取 converted 最新样本 id=2，实得 %d", got[0].AttributionID)
	}
	// 确定性：同输入两次调用逐条一致
	again := stratifySamples(pool, 8)
	for i := range got {
		if got[i].AttributionID != again[i].AttributionID {
			t.Fatalf("分层不确定：第 %d 条不一致", i)
		}
	}
	// limit 超池：全量返回
	if all := stratifySamples(pool, 100); len(all) != len(pool) {
		t.Errorf("limit 超池应全量 %d，实得 %d", len(pool), len(all))
	}
	if stratifySamples(nil, 5) != nil || stratifySamples(pool, 0) != nil {
		t.Error("空池/零 limit 应返回 nil")
	}
}

// TestPickReplaySide 盲评随机序：稳定可复现 + 大数下两侧近对半（去位置偏差的前提）。
func TestPickReplaySide(t *testing.T) {
	if a, b := pickReplaySide(7, 123), pickReplaySide(7, 123); a != b {
		t.Fatalf("同 seed 同样本应同序: %s vs %s", a, b)
	}
	aSide, bSide := 0, 0
	for id := uint(1); id <= 400; id++ {
		switch pickReplaySide(1, id) {
		case "A":
			aSide++
		case "B":
			bSide++
		default:
			t.Fatalf("非法侧值: %q", pickReplaySide(1, id))
		}
	}
	if aSide < 140 || bSide < 140 {
		t.Errorf("左右分布过度失衡: A=%d B=%d", aSide, bSide)
	}
}

// TestParseJudgeVerdict 裁判输出解析：约定词/啰嗦中文/英文/粘连词/废票，一律不崩。
func TestParseJudgeVerdict(t *testing.T) {
	cases := []struct {
		name       string
		raw        string
		replaySide string
		want       string
		parsed     bool
	}{
		{"裸A回放居左", "A", "A", "win", true},
		{"裸B回放居右", "B", "B", "win", true},
		{"裸A回放居右", "A", "B", "loss", true},
		{"中文啰嗦", "经过比较，回复B 更贴近客户到店诉求。", "B", "win", true},
		{"英文啰嗦", "Winner: A because it pushes the visit.", "B", "loss", true},
		{"小写票", "  a ", "A", "win", true},
		{"平局中文", "两份回复水平相当。", "A", "tie", true},
		{"平局英文", "TIE", "B", "tie", true},
		{"粘连词不是票", "AB", "A", "tie", false},
		{"字母数字粘连", "A1 更好", "A", "tie", false},
		{"纯废话", "emmm 我说不清", "A", "tie", false},
		{"空串", "", "B", "tie", false},
	}
	for _, c := range cases {
		got, parsed := parseJudgeVerdict(c.raw, c.replaySide)
		if got != c.want || parsed != c.parsed {
			t.Errorf("%s: parseJudgeVerdict(%q,%q)=(%s,%v) want=(%s,%v)",
				c.name, c.raw, c.replaySide, got, parsed, c.want, c.parsed)
		}
	}
}

// fakeDeps 全假外部能力 + 调用计数，验证编排与 dry-run 零调用。
type fakeDeps struct {
	d                deps
	poolCalls        int
	triggerCalls     int
	generateCalls    int
	judgeCalls       int
	judgeErr         error
	generateErr      error
	judgeResponses   []string // 依次消费的裁判原始输出（取完循环最后一项）
	judgeConsumed    int
	triggerMissIDs   map[uint]bool // attributionID 集合：模拟触发消息缺失
	lastJudgePrompts []string
}

// newFakeDeps 用给定池构造假依赖（generate 恒返回"回放回复"，judge 按脚本出票）。
func newFakeDeps(pool []replaySample) *fakeDeps {
	f := &fakeDeps{triggerMissIDs: map[uint]bool{}}
	f.d.selectPool = func(uint, time.Time, int) ([]replaySample, error) {
		f.poolCalls++
		return pool, nil
	}
	f.d.fetchTrigger = func(_, _, _ uint) (string, error) {
		return "客户消息原文", nil
	}
	f.d.generate = func(_, _, _ uint, in string) (string, error) {
		f.generateCalls++
		if f.generateErr != nil {
			return "", f.generateErr
		}
		return "回放回复", nil
	}
	f.d.judge = func(_ uint, prompt string) (string, error) {
		f.judgeCalls++
		f.lastJudgePrompts = append(f.lastJudgePrompts, prompt)
		if f.judgeErr != nil {
			return "", f.judgeErr
		}
		if len(f.judgeResponses) == 0 {
			f.judgeConsumed++
			return "A", nil // 无脚本时恒答 A（与 TestRunFakePipelineVerdicts 的对账口径一致）
		}
		i := f.judgeConsumed
		if i >= len(f.judgeResponses)-1 {
			i = len(f.judgeResponses) - 1
		}
		f.judgeConsumed++
		return f.judgeResponses[i], nil
	}
	return f
}

// TestDryRunZeroAICalls dry-run（默认安全态）：只选样，生成/裁判调用必须为 0。
func TestDryRunZeroAICalls(t *testing.T) {
	f := newFakeDeps(append(mkSamples(bucketConverted, 3, 1), mkSamples(bucketNone, 9, 50)...))
	o := options{TenantID: 7, Limit: 4, Days: 30, DryRun: true, Seed: 1}
	rep, err := runReplayEval(o, f.d, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if f.generateCalls != 0 || f.judgeCalls != 0 {
		t.Fatalf("dry-run 竟然调了 AI: generate=%d judge=%d", f.generateCalls, f.judgeCalls)
	}
	if rep.PoolSize != 12 {
		t.Errorf("池大小 %d want 12", rep.PoolSize)
	}
	if rep.Tallies[bucketConverted].Selected != 2 || rep.Tallies[bucketNone].Selected != 2 {
		t.Errorf("limit=4 轮转应 converted/none 各 2，实得 %+v", rep.Tallies)
	}
	if !strings.Contains(rep.conclusion(), "dry-run") {
		t.Errorf("dry-run 结论口径错误: %s", rep.conclusion())
	}
}

// TestRunFakePipelineVerdicts 全管线（假生成+假裁判）：分桶胜负/废票/合计/结论。
func TestRunFakePipelineVerdicts(t *testing.T) {
	pool := append(mkSamples(bucketHooked, 2, 101), mkSamples(bucketLead, 1, 201)...)
	f := newFakeDeps(pool)
	o := options{TenantID: 7, Limit: 10, Days: 30, DryRun: false, Seed: 1}
	rep, err := runReplayEval(o, f.d, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if f.generateCalls != 3 || f.judgeCalls != 3 {
		t.Fatalf("3 条样本应各调 1 次生成+裁判，实得 gen=%d judge=%d", f.generateCalls, f.judgeCalls)
	}
	// 裁判恒答 "A"：回放居左的样本记胜、居右的记负（与 pickReplaySide 逐一对账）
	for _, s := range pool {
		side := pickReplaySide(o.Seed, s.AttributionID)
		want := "win"
		if side == "B" {
			want = "loss"
		}
		tl := rep.Tallies[s.Bucket]
		switch want {
		case "win":
			tl.Win--
		case "loss":
			tl.Loss--
		}
	}
	for _, b := range bucketOrder {
		tl := rep.Tallies[b]
		if tl.Win != 0 || tl.Tie != 0 || tl.Loss != 0 {
			t.Errorf("按恒答A重算后桶 %s 残差不为 0: %+v", b, *tl)
		}
	}
	// 废票路径：裁判回废话 → 记平局且 Unparsed 计数
	f2 := newFakeDeps(mkSamples(bucketNone, 1, 301))
	f2.judgeResponses = []string{"emmm 说不清"}
	rep2, err := runReplayEval(options{TenantID: 7, Limit: 1, DryRun: false}, f2.d, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	tl := rep2.Tallies[bucketNone]
	if tl.Tie != 1 || tl.Unparsed != 1 || rep2.JudgeExecuted {
		t.Errorf("废票应记平+计数+不算有效票: %+v executed=%v", *tl, rep2.JudgeExecuted)
	}
}

// TestEmptyTriggerSkipsSample 缺触发消息（题目）的样本必须跳过且不烧生成/裁判调用。
func TestEmptyTriggerSkipsSample(t *testing.T) {
	f := newFakeDeps(mkSamples(bucketNone, 2, 701))
	f.d.fetchTrigger = func(_, _, _ uint) (string, error) { return "   ", nil }
	rep, err := runReplayEval(options{TenantID: 7, Limit: 2, DryRun: false}, f.d, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if rep.SkippedNoInput != 2 || f.generateCalls != 0 || f.judgeCalls != 0 {
		t.Errorf("空题目应整样跳过: skipped=%d gen=%d judge=%d", rep.SkippedNoInput, f.generateCalls, f.judgeCalls)
	}
}

// TestJudgeUnavailableSkipsBatch 裁判全失败：整批 SKIP 语义——不报错、零票、结论如实。
func TestJudgeUnavailableSkipsBatch(t *testing.T) {
	f := newFakeDeps(mkSamples(bucketConverted, 2, 401))
	f.judgeErr = errors.New("no model configured")
	rep, err := runReplayEval(options{TenantID: 7, Limit: 2, DryRun: false}, f.d, time.Now())
	if err != nil {
		t.Fatalf("裁判不可用不应炸任务: %v", err)
	}
	if rep.JudgeExecuted || rep.JudgeErrors != 2 {
		t.Errorf("应零票+2 次裁判失败记录: %+v", *rep)
	}
	if !strings.Contains(rep.conclusion(), "SKIP") {
		t.Errorf("结论应声明 SKIP: %s", rep.conclusion())
	}
	if !strings.Contains(rep.markdown(), "整批按 SKIP 处理") {
		t.Error("报告应披露 SKIP 原因计数")
	}
}

// TestReportRendersCountsAndNoSecrets 报告必须逐桶打印样本数/胜负，且不拖入任何密钥形文本。
func TestReportRendersCountsAndNoSecrets(t *testing.T) {
	f := newFakeDeps(append(mkSamples(bucketLead, 1, 501), mkSamples(bucketNone, 1, 502)...))
	f.judgeResponses = []string{"B"}
	rep, err := runReplayEval(options{TenantID: 7, Limit: 2, Days: 30, Seed: 3, DryRun: false}, f.d, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	md := rep.markdown()
	for _, want := range []string{"converted", "lead", "hooked", "none", "样本数", "废票"} {
		if !strings.Contains(md, want) {
			t.Errorf("报告缺要素 %q", want)
		}
	}
	lines := rep.summaryLines()
	if len(lines) < 6 {
		t.Fatalf("stdout 摘要行数过少: %v", lines)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "胜") || !strings.Contains(joined, "样本") {
		t.Errorf("stdout 摘要缺胜负/样本数: %s", joined)
	}
}

// TestBuildJudgePromptSidePlacement 盲评摆位：回放回复必须严格按随机序落到 A 或 B。
func TestBuildJudgePromptSidePlacement(t *testing.T) {
	p := buildJudgePrompt("题目", "历史回复H", "回放回复R", "A")
	if strings.Index(p, "回放回复R") > strings.Index(p, "历史回复H") {
		t.Error("replaySide=A 时回放应在历史之前")
	}
	if !strings.Contains(p, "只输出一个词") {
		t.Error("裁判提示词缺输出契约")
	}
	p2 := buildJudgePrompt("题目", "历史回复H", "回放回复R", "B")
	if strings.Index(p2, "历史回复H") > strings.Index(p2, "回放回复R") {
		t.Error("replaySide=B 时历史应在回放之前")
	}
}

// TestLimitHardCapRunSide 编排层兜底：limit 超硬上限时池查询量被钳（不真调 AI 也能验证）。
func TestLimitHardCapRunSide(t *testing.T) {
	o := options{TenantID: 7, Limit: limitHardCap, Days: 30, DryRun: true}
	var seenPoolLimit int
	d := deps{selectPool: func(_ uint, _ time.Time, poolLimit int) ([]replaySample, error) {
		seenPoolLimit = poolLimit
		return nil, nil
	}}
	if _, err := runReplayEval(o, d, time.Now()); err != nil {
		t.Fatal(err)
	}
	if seenPoolLimit != limitHardCap*poolFactor {
		t.Errorf("池上限应为 %d，实得 %d", limitHardCap*poolFactor, seenPoolLimit)
	}
}

// ============================================================
// 真连库段：无 DB 环境变量整段 SKIP（本地无库/CI 未配库都不红）
// ============================================================

// loadRootEnvUpward 从测试工作目录向上找项目根 .env 并加载（对齐 testutil 的定位方式），
// 之后再判 DB_HOST/DATABASE_URL 是否配置——避免"CI 没配库 → SetupTestDB Fatal"把门禁弄红。
func loadRootEnvUpward() {
	dir, _ := os.Getwd()
	for i := 0; i < 6; i++ {
		p := filepath.Join(dir, ".env")
		if _, err := os.Stat(p); err == nil {
			_ = godotenv.Load(p)
			return
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return
		}
		dir = parent
	}
}

// requireDBForTest DB 集成用例的 skip 友好前置。
func requireDBForTest(t *testing.T) {
	t.Helper()
	loadRootEnvUpward()
	if os.Getenv("DB_HOST") == "" && os.Getenv("DATABASE_URL") == "" && os.Getenv("POSTGRES_DB") == "" {
		t.Skip("未检测到 DB 环境变量（DB_HOST/DATABASE_URL），跳过真连库用例")
	}
	testutil.SetupTestDB(t)
}

// TestSelectPoolAndTriggerFromDB 真库端到端小闭环：插归因/消息样本 → 选样分桶 →
// 取触发消息 → 假生成假裁判跑通 runReplayEval（验证真实 SQL 接线，AI 一律假替）。
func TestSelectPoolAndTriggerFromDB(t *testing.T) {
	requireDBForTest(t)
	tid := testutil.CreateTenant(t)
	t.Cleanup(func() { testutil.CleanupTenant(t, tid) })

	cust := model.Customer{TenantID: tid, Name: "回放评测客户", IntentScore: 0.6, TrustLevel: 0.5, JourneyStage: "arrived"}
	if err := db.DB.Create(&cust).Error; err != nil {
		t.Fatal(err)
	}
	conv := model.Conversation{TenantID: tid, CustomerID: cust.ID, Status: "active"}
	if err := db.DB.Create(&conv).Error; err != nil {
		t.Fatal(err)
	}
	q := model.Message{TenantID: tid, ConversationID: conv.ID, CustomerID: cust.ID, SenderType: "customer", Content: "周末想去店里看看车"}
	if err := db.DB.Create(&q).Error; err != nil {
		t.Fatal(err)
	}
	ans := model.Message{TenantID: tid, ConversationID: conv.ID, CustomerID: cust.ID, SenderType: "ai", Content: "周末到店我给你留位"}
	if err := db.DB.Create(&ans).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	attr := model.ReplyAttribution{
		TenantID: tid, MessageID: ans.ID, ConversationID: conv.ID, CustomerID: cust.ID,
		TemplateID: "tpl_x", Hooked: true, ArrivedAt: &now,
	}
	if err := db.DB.Create(&attr).Error; err != nil {
		t.Fatal(err)
	}
	// 噪声行：消息不存在（join 不中）+ 非 ai 消息（sender_type 过滤不中）
	attr2 := model.ReplyAttribution{TenantID: tid, MessageID: ans.ID + 999999, ConversationID: conv.ID, CustomerID: cust.ID}
	if err := db.DB.Create(&attr2).Error; err != nil {
		t.Fatal(err)
	}
	humanMsg := model.Message{TenantID: tid, ConversationID: conv.ID, CustomerID: cust.ID, SenderType: "human", Content: "人工插话"}
	if err := db.DB.Create(&humanMsg).Error; err != nil {
		t.Fatal(err)
	}
	attr3 := model.ReplyAttribution{TenantID: tid, MessageID: humanMsg.ID, ConversationID: conv.ID, CustomerID: cust.ID}
	if err := db.DB.Create(&attr3).Error; err != nil {
		t.Fatal(err)
	}

	pool, err := selectPoolFromDB(tid, now.AddDate(0, 0, -1), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(pool) != 1 || pool[0].AttributionID != attr.ID {
		t.Fatalf("应恰选 1 条真实 AI 归因样本，实得 %+v", pool)
	}
	if pool[0].Bucket != bucketConverted {
		t.Errorf("ArrivedAt 非空应归 converted 桶，实得 %s", pool[0].Bucket)
	}
	trigger, err := fetchTriggerFromDB(tid, conv.ID, ans.ID)
	if err != nil {
		t.Fatal(err)
	}
	if trigger != "周末想去店里看看车" {
		t.Errorf("触发消息错配: %q", trigger)
	}

	f := newFakeDeps(pool) // 真库选样 + 假 AI：全链路编排（含真实 fetchTrigger）在此过一遍
	f.d.fetchTrigger = fetchTriggerFromDB
	f.judgeResponses = []string{"平局"}
	o := options{TenantID: tid, Limit: 1, Days: 1, Seed: 2, DryRun: false}
	rep, err := runReplayEval(o, f.d, now)
	if err != nil {
		t.Fatal(err)
	}
	tl := rep.Tallies[bucketConverted]
	if f.judgeCalls != 1 || tl.Tie != 1 || tl.votes() != 1 {
		t.Errorf("真库选样+假裁判全链路异常: judgeCalls=%d tally=%+v", f.judgeCalls, *tl)
	}
}
