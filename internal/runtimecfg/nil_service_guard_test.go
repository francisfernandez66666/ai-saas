// 本文件测试系统配置单例未初始化（nil receiver）时全部读方法的安全兜底。
// 来源：2026-09-22 审计 G2/G3——全仓 47 处无判空直调 DefaultSystemConfigService，
// 其中 billing_order.go 的 static_qr 分支因 GetPayMode 改走"未就绪=安全侧"首次踩中该路径，
// 单测确定性 panic。修法统一收在方法内（一处守卫消灭全部地雷），本文件为守卫的回归锁。
package runtimecfg

import (
	"testing"

	"ai-scrm/internal/model"
)

// nilSvc 构造未初始化的服务指针——等价于线上 InitSystemConfigService() 尚未调用的窗口期
func nilSvc() *SystemConfigService { return nil }

// TestNilServiceReadersReturnDefaults 断言 nil 单例读任意键一律返回调用方默认值且不 panic，
// 覆盖 string/int/bool/float/slice/json 六族 + 租户级四族。
func TestNilServiceReadersReturnDefaults(t *testing.T) {
	s := nilSvc()

	if got := s.GetString("pay_mode", "static_qr"); got != "static_qr" {
		t.Errorf("GetString 应回退默认值，得 %q", got)
	}
	if got := s.GetInt("merge_window_seconds", 25); got != 25 {
		t.Errorf("GetInt 应回退默认值，得 %d", got)
	}
	if got := s.GetBool("kb_rerank", false); got != false {
		t.Errorf("GetBool 应回退默认值，得 %v", got)
	}
	if got := s.GetFloat("theta_rounds", 3.5); got != 3.5 {
		t.Errorf("GetFloat 应回退默认值，得 %v", got)
	}
	if got := s.GetIntSlice("l3_simple_delay", []int{500, 1500}); len(got) != 2 || got[0] != 500 {
		t.Errorf("GetIntSlice 应回退默认值，得 %v", got)
	}
	if got := s.GetStringSlice("model_priority", []string{"glm"}); len(got) != 1 {
		t.Errorf("GetStringSlice 应回退默认值，得 %v", got)
	}
	var weights map[string]float64
	if s.GetJSON("anchor_weights", &weights) {
		t.Error("GetJSON 在单例未初始化时必须返回 false（视为读不到键），不得写入 target")
	}
	if weights != nil {
		t.Errorf("GetJSON 失败时不得改动 target，得 %v", weights)
	}
}

// TestNilServiceTenantReadersReturnDefaults 租户级四族同样兜底：
// 请求链路（GetXxxForTenant）在冷路径/单测里最常裸调，panic 面最大。
func TestNilServiceTenantReadersReturnDefaults(t *testing.T) {
	s := nilSvc()
	const tid = uint(7)

	if got := s.GetStringForTenant(tid, "human_keywords", "转人工"); got != "转人工" {
		t.Errorf("GetStringForTenant 应回退默认值，得 %q", got)
	}
	if got := s.GetIntForTenant(tid, "merge_window_seconds", 25); got != 25 {
		t.Errorf("GetIntForTenant 应回退默认值，得 %d", got)
	}
	if got := s.GetBoolForTenant(tid, "token_billing_enabled", true); got != true {
		t.Errorf("GetBoolForTenant 应回退默认值，得 %v", got)
	}
	if got := s.GetFloatForTenant(tid, "tau", 0.3); got != 0.3 {
		t.Errorf("GetFloatForTenant 应回退默认值，得 %v", got)
	}
	// GetInterval 走 GetJSON 出口，未初始化时落"默认区间"分支，值域必须落在 [min,max]
	for i := 0; i < 50; i++ {
		if got := s.GetInterval("arrive_offline_offset", 2000, 5000); got < 2000 || got > 5000 {
			t.Fatalf("GetInterval 未初始化时应落在默认区间，第 %d 次得 %d", i, got)
		}
	}
}

// TestNilServiceListersAndReloadAreSafe 列表读取返回空、Reload 空转不 panic。
// Reload 是所有写方法的收尾动作，守卫在此即让整条写链对 nil 安全。
func TestNilServiceListersAndReloadAreSafe(t *testing.T) {
	s := nilSvc()

	if got := s.GetAll(); got != nil {
		t.Errorf("GetAll 未初始化应返回 nil，得 %d 项", len(got))
	}
	if got := s.GetByCategory("strategy"); got != nil {
		t.Errorf("GetByCategory 未初始化应返回 nil，得 %d 项", len(got))
	}
	if got := s.GetByKey("pay_mode"); got != (*model.SystemConfig)(nil) {
		t.Errorf("GetByKey 未初始化应返回 nil，得 %+v", got)
	}
	s.Reload() // 不得 panic
}
