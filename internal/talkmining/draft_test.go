// Package talkmining 出稿层单测：钩子未注入安全跳过、样本不足拒稿、同簇幂等不重复出稿、
// LLM 产物入库前二次脱敏。幂等部分真连库（testutil 装配，DB 不可用时本地跳过/CI Fatal）。
package talkmining

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/testutil"
)

// TestMain 挂 testutil 的跳过计数器出口：DB 不可用时显式打出跳过量，防"静默绿"。
func TestMain(m *testing.M) {
	os.Exit(testutil.RunMain(m))
}

// withFakeDraft 临时注入假 LLM 钩子并在测后还原（同 EvalLLMFunc 测试的 old/new 写法）。
func withFakeDraft(t *testing.T, fn func(tenantID uint, prompt string) (string, error)) {
	t.Helper()
	old := GenerateDraftFunc
	GenerateDraftFunc = fn
	t.Cleanup(func() { GenerateDraftFunc = old })
}

// enoughCluster 构造一个达样本门槛的簇（6 个客户 4 个到店，转化率 66.7%）。
func enoughCluster() []Cluster {
	var recs []Record
	for i := uint(1); i <= 6; i++ {
		outcome := OutcomeNone
		if i <= 4 {
			outcome = OutcomeArrived
		}
		recs = append(recs, mk(77, 7000+i, "lead_captured", outcome,
			fmt.Sprintf("哥，周六店里有试驾活动，顺路过来坐坐嘛，我帮您留个名额%02d", i)))
	}
	return Mine(recs, Options{MinSamples: 5})
}

// TestDraftTemplatesNilHookSkips 钩子未注入：整轮安全跳过，返回零账目不报错不 panic
// （离线增强能力缺它零影响，fail-open 口径同 kb_rerank）。
func TestDraftTemplatesNilHookSkips(t *testing.T) {
	old := GenerateDraftFunc
	GenerateDraftFunc = nil
	defer func() { GenerateDraftFunc = old }()
	stat, err := DraftTemplates(1, enoughCluster(), 0)
	if err != nil || stat.Created != 0 {
		t.Fatalf("未注入钩子应零出稿零错误，实得 stat=%+v err=%v", stat, err)
	}
}

// TestDraftTemplatesRejectsPlatformTenant tenantID=0 直接拒——草稿必须落租户归属，
// 否则盖章回调会把它写成平台层模板泄露到所有租户视图（C7 红线）。
func TestDraftTemplatesRejectsPlatformTenant(t *testing.T) {
	withFakeDraft(t, func(uint, string) (string, error) { return "任意草稿", nil })
	if _, err := DraftTemplates(0, enoughCluster(), 0); err == nil {
		t.Fatal("tenantID=0 应被拒绝")
	}
}

// TestBuildDraftPromptUsesMaskedSampleAndStats 提示词组装：带脱敏后的代表文本与
// 样本/转化率统计（LLM 判证据量用），且不含明文手机号。
func TestBuildDraftPromptUsesMaskedSampleAndStats(t *testing.T) {
	recs := []Record{
		mk(7, 701, "arrived", OutcomeArrived, "您留个13812345678，到店报号就行不用等"),
		mk(7, 702, "arrived", OutcomeDealt, "您留个13812345678，到店报号就行不用等哈"),
	}
	clusters := Mine(recs, Options{MinSamples: 2})
	prompt := BuildDraftPrompt(clusters[0])
	if strings.Contains(prompt, "13812345678") {
		t.Fatalf("提示词泄露明文手机号: %s", prompt)
	}
	if !strings.Contains(prompt, "138****5678") || !strings.Contains(prompt, "样本数=2") || !strings.Contains(prompt, "占位符") {
		t.Fatalf("提示词缺统计或脱敏样本: %s", prompt)
	}
}

// TestDraftTemplatesIdempotent 幂等核心断言（真连库）：同一簇跑两轮只出一条草稿，
// 产物必须是 status=2 草稿态 + 正确租户归属；LLM 照抄样本号码时入库前二次掩码。
func TestDraftTemplatesIdempotent(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)
	// templates 不在 testutil 级联清单里，按本包前缀自清（不波及其他测试模板）
	defer db.DB.Exec("DELETE FROM templates WHERE id LIKE 'tplmin_%' AND tenant_id = ?", tid) // g12:platform

	clusters := enoughCluster()
	if len(clusters) != 1 || clusters[0].Insufficient {
		t.Fatalf("测试前提：应恰有 1 个达门槛簇，实得 %+v", clusters)
	}

	// 假 LLM 故意在草稿里照抄明文号码，验证入库前纵深脱敏
	withFakeDraft(t, func(_ uint, prompt string) (string, error) {
		if !strings.Contains(prompt, "试驾活动") {
			return "", fmt.Errorf("提示词未带上样本: %s", prompt)
		}
		return "欢迎到店体验{{model}}，联系电话13812345678，我帮您约时间", nil
	})

	stat, err := DraftTemplates(tid, clusters, 0)
	if err != nil || stat.Created != 1 {
		t.Fatalf("首轮应出稿 1 条，实得 stat=%+v err=%v", stat, err)
	}
	tmplID := minedTemplateID(tid, clusters[0])
	var tpl model.Template
	if err := db.DB.Where("id = ? AND tenant_id = ?", tmplID, tid).First(&tpl).Error; err != nil { // g12:platform
		t.Fatalf("读回草稿: %v", err)
	}
	if tpl.Status != 2 {
		t.Fatalf("草稿必须为 status=2（不进召回池），实得 %d", tpl.Status)
	}
	if tpl.TenantID != tid {
		t.Fatalf("租户归属丢失: %d", tpl.TenantID)
	}
	if strings.Contains(tpl.PromptTemplate, "13812345678") || !strings.Contains(tpl.PromptTemplate, "138****5678") {
		t.Fatalf("LLM 产物未二次脱敏: %s", tpl.PromptTemplate)
	}

	// 第二轮：同簇必须走"已存在"分支，零出稿零新行（钩子被调即计违约，故用计数器兜底）
	calls := 0
	withFakeDraft(t, func(uint, string) (string, error) { calls++; return "第二轮不该被调用", nil })
	stat2, err := DraftTemplates(tid, clusters, 0)
	if err != nil {
		t.Fatalf("二轮报错: %v", err)
	}
	if stat2.Created != 0 || stat2.SkippedExisting != 1 {
		t.Fatalf("二轮应 0 出稿 1 已存在，实得 %+v", stat2)
	}
	if calls != 0 {
		t.Fatalf("已存在簇不应再烧 LLM token，实调 %d 次", calls)
	}
	var total int64
	db.DB.Model(&model.Template{}).Where("id LIKE 'tplmin_%' AND tenant_id = ?", tid).Count(&total) // g12:platform
	if total != 1 {
		t.Fatalf("两轮后草稿行应恰 1 条，实得 %d", total)
	}
}

// TestDraftTemplatesSkipsInsufficient 样本不足簇即便排在入参中也必须拒稿
// （insufficient_samples 不得输出结论 → 更不得变成模板）。
func TestDraftTemplatesSkipsInsufficient(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)
	defer db.DB.Exec("DELETE FROM templates WHERE id LIKE 'tplmin_%' AND tenant_id = ?", tid) // g12:platform

	recs := []Record{
		mk(88, 8801, "arrived", OutcomeDealt, "只有两个客户的窄簇话术，样本远不够下结论"),
		mk(88, 8802, "arrived", OutcomeDealt, "只有两个客户的窄簇话术，样本远不够下结论呀"),
	}
	calls := 0
	withFakeDraft(t, func(uint, string) (string, error) { calls++; return "不该出稿", nil })
	stat, err := DraftTemplates(tid, Mine(recs, Options{MinSamples: 5}), 0)
	if err != nil {
		t.Fatalf("拒稿不应报错: %v", err)
	}
	if stat.Created != 0 || stat.SkippedInsufficient != 1 || calls != 0 {
		t.Fatalf("不足样本簇必须零出稿零烧token: stat=%+v calls=%d", stat, calls)
	}
}
