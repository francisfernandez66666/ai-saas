// 批五 C·L2 接线单测（2026-09-23 批六补做）：RunAutoPromotionSweep 的闸关零查询、
// 显著胜出者上调一格、冷却窗内第二轮不再叠幅、非 leading 一律不动权重。
// 为什么单独建文件：experiment_decision_test.go 是零 DB 的纯函数测试，
// 本文件要连真库（testutil 连不上自动 skip），混在一起会让"纯函数层"失去无 DB 的可跑性。
package strategy

import (
	"crypto/rand"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/runtimecfg"
	"ai-scrm/internal/testutil"
)

// sweepRndID 进程级随机段（PID + 4 位随机）：同机并发跑多个 go test 进程时也不撞。
var sweepRndID = func() string {
	b := make([]byte, 2)
	if _, err := rand.Read(b); err != nil {
		return strconv.Itoa(os.Getpid())
	}
	return strconv.Itoa(os.Getpid()) + fmt.Sprintf("%x", b)
}()

// sweepID 生成全局唯一的模板 ID。
// 为什么必须唯一：templates 主键是字符串 ID（不分租户），并发跑的多个 go test 进程
// 若用固定 ID 会互相撞 23505——与本包其它"按租户隔离"的用例假设不同。
func sweepID(prefix string) string {
	return fmt.Sprintf("%s_%s_%d", prefix, sweepRndID, time.Now().UnixNano()%1e7)
}

// sweepSeed 建「租户 + 同锚两话术 + 两条 pack_stats 快照」的最小判优场景。
// selfLead/peerLead 为两侧留资率，n 为样本数（两侧同量，判优只比率）。
// 返回 (租户ID, 胜出侧ID, 对照侧ID)——ID 在函数内生成，调用方拿不到固定值也就不会误设常量。
func sweepSeed(t *testing.T, selfLead, peerLead float64, n int64) (uint, string, string) {
	t.Helper()
	selfID, peerID := sweepID("win"), sweepID("peer")
	tid := testutil.CreateTenant(t)
	t.Cleanup(func() { testutil.CleanupTenant(t, tid) })

	for _, id := range []string{selfID, peerID} {
		tpl := model.Template{
			ID: id, TenantID: tid, AnchorType: 2, Name: "晋升测试话术-" + id,
			PromptTemplate: "测试话术", AbGroup: "g1", AbWeight: 10, Status: 1, Priority: 1,
		}
		if err := db.DB.Create(&tpl).Error; err != nil {
			t.Fatalf("建模板失败 %s: %v", id, err)
		}
	}
	for _, s := range []model.PackStatSnapshot{
		{TenantID: tid, PackCode: "sweep", PackVersion: "1.0.0", TemplateID: selfID,
			SampleCount: n, HookRate: 0.4, LeadRate: selfLead, ComputedAt: time.Now()},
		{TenantID: tid, PackCode: "sweep", PackVersion: "1.0.0", TemplateID: peerID,
			SampleCount: n, HookRate: 0.4, LeadRate: peerLead, ComputedAt: time.Now()},
	} {
		if err := db.DB.Create(&s).Error; err != nil {
			t.Fatalf("建快照失败 %s: %v", s.TemplateID, err)
		}
	}
	return tid, selfID, peerID
}

// sweepWeight 读取模板当前权重
func sweepWeight(t *testing.T, tid uint, id string) int {
	t.Helper()
	var tpl model.Template
	if err := db.DB.Where("tenant_id = ? AND id = ?", tid, id).First(&tpl).Error; err != nil {
		t.Fatalf("读模板失败: %v", err)
	}
	return tpl.AbWeight
}

// sweepCfg 装配晋升相关热配置并返回恢复函数
func sweepCfg(t *testing.T, auto string) func() {
	t.Helper()
	return runtimecfg.SetDefaultForTest(runtimecfg.NewStaticService(map[string]string{
		"experiment_auto_promote":           auto,
		"experiment_reward_metric":          "\"lead\"",
		"experiment_min_samples_lead":       "2200",
		"experiment_min_samples_hook":       "400",
		"experiment_confidence":             "0.95",
		"experiment_weight_step":            "10",
		"experiment_weight_max":             "100",
		"experiment_promote_cooldown_hours": "72",
	}, nil))
}

// TestRunAutoPromotionSweepGateOffNoDB 开关关闭时不该有任何写库动作。
// 两条断言分层：本包单跑（无 DB）时 db.DB 为 nil，函数若在闸前不返回就会 panic，
// "跑完且返回 0" 即零查询证明；连库跑（CI）时改成"前后行数一致"的零写证明，
// 用例不会因环境差异被整体 skip（skip 掉的门禁等于没装）。
func TestRunAutoPromotionSweepGateOffNoDB(t *testing.T) {
	restore := sweepCfg(t, "false")
	defer restore()
	if db.DB == nil {
		n, err := RunAutoPromotionSweep(time.Now())
		if n != 0 || err != nil {
			t.Fatalf("开关关闭应返回 (0,nil)，实得 (%d,%v)", n, err)
		}
		return
	}
	testutil.SetupTestDB(t)
	tid, selfID, _ := sweepSeed(t, 0.20, 0.05, 3000)
	before := sweepWeight(t, tid, selfID)
	var auditRows int64
	db.DB.Model(&model.TenantAuditLog{}).Where("action = ?", promoteAuditAction).Count(&auditRows)

	if n, err := RunAutoPromotionSweep(time.Now()); err != nil || n != 0 {
		t.Fatalf("开关关闭应 0 晋升，实得 (%d,%v)", n, err)
	}
	if w := sweepWeight(t, tid, selfID); w != before {
		t.Fatalf("开关关闭不得改权重，%d→%d", before, w)
	}
	var after int64
	db.DB.Model(&model.TenantAuditLog{}).Where("action = ?", promoteAuditAction).Count(&after)
	if after != auditRows {
		t.Fatalf("开关关闭不得写晋升审计，%d→%d", auditRows, after)
	}
}

// TestRunAutoPromotionSweepPromotesLeadingWithCooldown leading（显著胜出）者第一次被顶一格，
// 第二次因冷却窗（72h）不叠幅——防"小时任务 × 步长 10"几天内冲到 100 独占分流。
func TestRunAutoPromotionSweepPromotesLeadingWithCooldown(t *testing.T) {
	testutil.SetupTestDB(t)
	tid, selfID, peerID := sweepSeed(t, 0.20, 0.05, 3000)

	restore := sweepCfg(t, "true")
	defer restore()

	n, err := RunAutoPromotionSweep(time.Now())
	if err != nil {
		t.Fatalf("首轮 sweep 报错: %v", err)
	}
	if n != 1 {
		t.Fatalf("应晋升 1 条，实得 %d", n)
	}
	if w := sweepWeight(t, tid, selfID); w != 20 {
		t.Fatalf("胜出模板权重应 10→20，实得 %d", w)
	}
	if w := sweepWeight(t, tid, peerID); w != 10 {
		t.Fatalf("对照（落后）模板不该被自动调权，实得 %d", w)
	}
	// 审计留痕：人工一键回滚与事后追责都靠这条，缺它等于"系统偷偷改了话术权重"
	var audits int64
	if err := db.DB.Model(&model.TenantAuditLog{}).
		Where("tenant_id = ? AND action = ? AND resource = ?", tid, promoteAuditAction, "template:"+selfID).
		Count(&audits).Error; err != nil {
		t.Fatalf("查审计失败: %v", err)
	}
	if audits != 1 {
		t.Fatalf("晋升审计应 1 条，实得 %d", audits)
	}

	// 第二轮：同一时刻再跑，冷却窗未满 → 权重不再叠
	if n2, err := RunAutoPromotionSweep(time.Now()); err != nil || n2 != 0 {
		t.Fatalf("冷却期内第二轮应 0 晋升，实得 (%d,%v)", n2, err)
	}
	if w := sweepWeight(t, tid, selfID); w != 20 {
		t.Fatalf("冷却期内权重不应变化，实得 %d", w)
	}
	// 冷却窗已过（模拟 3 天后）：再顶一格 → 30
	if n3, err := RunAutoPromotionSweep(time.Now().Add(73 * time.Hour)); err != nil || n3 != 1 {
		t.Fatalf("冷却过后应再晋升 1 条，实得 (%d,%v)", n3, err)
	}
	if w := sweepWeight(t, tid, selfID); w != 30 {
		t.Fatalf("冷却过后权重应 20→30，实得 %d", w)
	}
}

// TestRunAutoPromotionSweepKeepsWatchingUntouched 样本达标但差异不过置信 → keep_watching，
// 一条都不许动（自动调权只认"显著"，不认"看起来好一点"）。
func TestRunAutoPromotionSweepKeepsWatchingUntouched(t *testing.T) {
	testutil.SetupTestDB(t)
	tid, selfID, peerID := sweepSeed(t, 0.10, 0.095, 2500)
	restore := sweepCfg(t, "true")
	defer restore()

	if n, err := RunAutoPromotionSweep(time.Now()); err != nil || n != 0 {
		t.Fatalf("差异不显著应 0 晋升，实得 (%d,%v)", n, err)
	}
	for _, id := range []string{selfID, peerID} {
		if w := sweepWeight(t, tid, id); w != 10 {
			t.Fatalf("模板%s 权重不该被改动，实得 %d", id, w)
		}
	}
}

// TestRunAutoPromotionSweepSkipsMissingTemplate pack_stats 有快照但模板已删除/停用 → 跳过，
// 既不计入晋升数也不报错（孤儿快照是历史常态，不能卡死小时任务）。
func TestRunAutoPromotionSweepSkipsMissingTemplate(t *testing.T) {
	testutil.SetupTestDB(t)
	tid, selfID, peerID := sweepSeed(t, 0.20, 0.05, 3000)
	if err := db.DB.Model(&model.Template{}).Where("tenant_id = ? AND id = ?", tid, selfID).
		Update("status", 0).Error; err != nil {
		t.Fatalf("停用模板失败: %v", err)
	}
	restore := sweepCfg(t, "true")
	defer restore()

	if n, err := RunAutoPromotionSweep(time.Now()); err != nil || n != 0 {
		t.Fatalf("停用模板后应 0 晋升，实得 (%d,%v)", n, err)
	}
	if w := sweepWeight(t, tid, peerID); w != 10 {
		t.Fatalf("同锚伙伴不该因对照被停用而独自晋升，实得 %d", w)
	}
}
