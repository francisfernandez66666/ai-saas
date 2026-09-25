// 档位门槛判据单测（G-22c，2026-09-24）。
//
// 纯函数、不连库：这条判据的错法全是"比较写歪"，跟数据层无关，
// 连库只会让它跑得慢、且在 CI 里多一个可失败点。
// 每组都配反向用例（该放的必须放），因为门禁类实现最容易写成"一律拒"——
// 一律拒在正向上跟正确实现完全同分。
package industrypack

import "testing"

// TestTierRankTable 钉住档位词表与序数：与 model.Tenant.Tier 同一条口径，
// 任何新增档位（如 team）必须同时改这里，否则新档租户在门槛判定上等于"未知档"被拒。
func TestTierRankTable(t *testing.T) {
	cases := map[string]int{"personal": 1, "enterprise": 2, "custom": 3}
	for tier, want := range cases {
		if got := TierRank(tier); got != want {
			t.Errorf("TierRank(%q)=%d 期望 %d", tier, got, want)
		}
		if !ValidTier(tier) {
			t.Errorf("ValidTier(%q)=false，词表内的档位必须有效", tier)
		}
	}
	for _, bad := range []string{"", "Personal", "team", "enterprise ", "gold"} {
		if TierRank(bad) != -1 || ValidTier(bad) {
			t.Errorf("TierRank(%q)=%d 未知档位必须返回 -1 且判非法（大小写/空格都不算匹配）", bad, TierRank(bad))
		}
	}
}

// TestPackAllowedForTier 门槛判据的完整判定矩阵。
func TestPackAllowedForTier(t *testing.T) {
	cases := []struct {
		name       string
		minTier    string
		tenantTier string
		wantOK     bool
		wantReason string
	}{
		// 不设门槛 = 存量 9 个包的现状：任何档位都能绑（含脏档位，见下条注释）
		{"无门槛_personal", "", "personal", true, ""},
		{"无门槛_未知档位仍放行", "", "weird", true, ""},
		// 正向：够档就放
		{"同档personal", "personal", "personal", true, ""},
		{"同档enterprise", "enterprise", "enterprise", true, ""},
		{"高档过门槛", "enterprise", "custom", true, ""},
		{"custom包对custom", "custom", "custom", true, ""},
		// 反向：低一档即拒（门禁本体）
		{"personal租户绑enterprise包", "enterprise", "personal", false, "pack_tier_required"},
		{"enterprise租户绑custom包", "custom", "enterprise", false, "pack_tier_required"},
		{"personal租户绑custom包", "custom", "personal", false, "pack_tier_required"},
		// 脏数据：包上写了不认识的档位名 → 拒（fail-closed，宁可让超管去修那一列）
		{"门槛值非法", "gold", "custom", false, "pack_min_tier_invalid"},
		// 脏数据：租户档位不认识（新档名没登记进词表）→ 拒而不是"当作最低档"
		{"租户档位未知", "personal", "weird", false, "tenant_tier_unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, reason := PackAllowedForTier(tc.minTier, tc.tenantTier)
			if ok != tc.wantOK {
				t.Fatalf("PackAllowedForTier(%q,%q) ok=%v 期望 %v", tc.minTier, tc.tenantTier, ok, tc.wantOK)
			}
			if reason != tc.wantReason {
				t.Errorf("reason=%q 期望 %q（稳定码是前端与冒烟的分支依据）", reason, tc.wantReason)
			}
			// 不变式：放行时不得带原因码，拒绝时必须带（否则界面无法解释"为什么不行"）
			if ok && reason != "" {
				t.Errorf("ok=true 时 reason 必须是空串，实得 %q", reason)
			}
			if !ok && reason == "" {
				t.Error("ok=false 时 reason 不能为空：拒绝必须说清为什么")
			}
		})
	}
}
