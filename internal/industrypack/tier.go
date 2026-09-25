// 行业包档位门槛判定（G-22c，2026-09-24）。
//
// 判据只有一个来源：本文件的 TierRank / PackAllowedForTier。
// 列表侧（哪些包看得见、标不标"需升级"）与写侧（绑定放行不放行）必须调同一函数——
// 两边各写一遍是这类门禁最常见的腐化路径：界面显示"可绑"而接口回 403，
// 或反过来界面拦住、接口敞开（后者是真漏洞）。
//
// 词表与 model.Tenant.Tier 对齐：personal / enterprise / custom。
// 未知档位（含空串以外的任何拼写错误）一律判"不许绑"：
// 门禁 fail-open 等于没有门禁，宁可让超管去修那条脏数据。
package industrypack

// TierRank 把档位名换成可比较的序数；未知档返回 -1（不参与大小比较，直接拒）。
func TierRank(tier string) int {
	switch tier {
	case "personal":
		return 1
	case "enterprise":
		return 2
	case "custom":
		return 3
	default:
		return -1
	}
}

// ValidTier 判一个档位名是否在词表内（写入口用它挡脏值）。
func ValidTier(tier string) bool { return TierRank(tier) > 0 }

// PackAllowedForTier 判"档位为 tenantTier 的租户能不能绑 minTier 档门槛的包"。
// 返回 ok=false 时 reason 是稳定码（前端与冒烟都按码分支，勿匹配中文文案）。
//
// minTier=” 表示不设门槛（迁移 026 的默认值，也是全部存量包的现状）；
// 租户档位本身未知（脏数据/新档名未登记）同样拒——门槛判定不能建立在猜的基础上。
func PackAllowedForTier(minTier, tenantTier string) (bool, string) {
	if minTier == "" {
		return true, ""
	}
	need := TierRank(minTier)
	if need <= 0 {
		return false, "pack_min_tier_invalid"
	}
	have := TierRank(tenantTier)
	if have <= 0 {
		return false, "tenant_tier_unknown"
	}
	if have < need {
		return false, "pack_tier_required"
	}
	return true, ""
}
