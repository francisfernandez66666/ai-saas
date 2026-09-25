// 超管侧行业包换包/重物化端点（G-22a，2026-09-24）
//
// 为什么必须有这一条路：
//  1. 换包入口此前只有租户自己的 /admin/packs/bind（AdminRequired）。平台代管一家
//     「选错行业 / 行业包刚上架 / 演示租户要从汽车切到通用」的租户时，只能把账号
//     交给客户侧去点，超管台没有落点。
//  2. **发新版不等于租户吃到新版**：AppliedVersion 只在绑定时写一次，全仓没有
//     "新版本重物化"通道（grep AppliedVersion 只有写没有读）。于是本批修完 auto/general
//     包内容之后，5 家已绑 auto 的存量租户仍然停在旧版本、且物化产物为 0 行。
//     reapply 就是补这条通道：按 code 取当前可用新版本重新物化并回写版本。
//
// 口径纪律（与 admin 侧 bind 一致，避免两套判据）：
//   - 先物化、后写绑定行：物化失败直接回错，绝不留下"有绑定无物化"的死行
//     （这正是本轮查到的现场：tenant_pack_bindings 里 auto 绑定 5 条、templates 里 0 行）
//   - 只认 active 包；行业包必须 industry 级、企业包必须 enterprise 级且 parent 对得上
//   - **换包要清旧包**：新包物化成功后调 PurgeOtherPacks 把"不在新绑定集合里"的旧包产物
//     （模板/卖点/标签/prompts-params-mindset 覆盖键）全部清掉。不清就是"加包"——
//     召回层只按 tenant_id+status 过滤、不看包绑定，旧行业话术会一直留在 AI 候选池里
//   - 每次动作落 tenant_audit_logs（谁、对哪家、从什么版本换到什么版本）
package api

import (
	"fmt"
	"log"
	"net/http"

	"ai-scrm/internal/db"
	"ai-scrm/internal/industrypack"
	"ai-scrm/internal/middleware"
	"ai-scrm/internal/model"

	"github.com/gin-gonic/gin"
)

// superPackTarget 校验 :id 指向的租户存在，返回租户与操作人
// 超管台按 ID 直改他租户配置，必须先把目标租户钉实——否则包会物化到一个不存在的 tenant_id 上，
// 变成孤儿内容（templates/features 带 tenant_id 但租户表无此行，RLS 与列表都看不见它）。
func superPackTarget(c *gin.Context) (*model.Tenant, uint, bool) {
	tid, ok := PathUintID(c)
	if !ok {
		return nil, 0, false
	}
	var t model.Tenant
	if err := db.DB.First(&t, tid).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "租户不存在")
		return nil, 0, false
	}
	uid, _, _ := middleware.CurrentUser(c)
	return &t, uid, true
}

// SuperTenantPackBind POST /api/v1/super/tenants/:id/pack/bind
// apidump:ts SuperPackBindResp
// 入参 {industry_pack_id 必填, enterprise_pack_id 可选}——超管代客户换包并即时物化
func SuperTenantPackBind(c *gin.Context) {
	t, uid, ok := superPackTarget(c)
	if !ok {
		return
	}
	var req struct {
		IndustryPackID   uint  `json:"industry_pack_id" binding:"required"`
		EnterprisePackID *uint `json:"enterprise_pack_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErr(c, http.StatusBadRequest, 400, "参数错误：industry_pack_id 必填")
		return
	}
	ipc, ipack, err := openActivePack(req.IndustryPackID)
	if err != nil {
		RespErr(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	if ipack.PackLevel != industrypack.LevelIndustry {
		RespErr(c, http.StatusBadRequest, 400, "industry_pack_id 必须是行业级包")
		return
	}
	// G-22c(2026-09-24)：超管这一路**故意不拦**档位门槛——销售线下谈好的例外、
	// 给试点客户预先开通高门槛包，都是平台侧要能做到的动作；租户自助入口
	// （TenantPackBind）才是硬拒。但例外必须留下凭据，否则后门把 403 门禁掏空了
	// 还没人知道，故越档时单独落一条 super_pack_tier_override 审计。
	if okTier, reason := industrypack.PackAllowedForTier(ipack.MinTier, t.Tier); !okTier {
		db.DB.Create(&model.TenantAuditLog{
			TenantID: t.ID, UserID: uid, Action: "super_pack_tier_override",
			Resource: fmt.Sprintf("tenant:%d", t.ID),
			Detail: fmt.Sprintf(`{"pack":"%s","pack_min_tier":"%s","tenant_tier":"%s","reason":"%s"}`,
				ipack.Code, ipack.MinTier, t.Tier, reason),
			IP:        c.ClientIP(),
			UserAgent: c.Request.UserAgent(),
		})
		log.Printf("[行业包] 超管越档绑定已记审计：租户%d 档位 %s ← 包 %s 门槛 %s（%s）",
			t.ID, t.Tier, ipack.Code, ipack.MinTier, reason)
	}
	var epc *industrypack.PackContent
	var epack *model.IndustryPack
	if req.EnterprisePackID != nil && *req.EnterprisePackID > 0 {
		epc, epack, err = openActivePack(*req.EnterprisePackID)
		if err != nil {
			RespErr(c, http.StatusBadRequest, 400, "企业包: "+err.Error())
			return
		}
		if epack.PackLevel != industrypack.LevelEnterprise {
			RespErr(c, http.StatusBadRequest, 400, "enterprise_pack_id 必须是企业级包")
			return
		}
		if epack.ParentCode != ipack.Code {
			RespErr(c, http.StatusBadRequest, 400,
				fmt.Sprintf("企业包[%s]挂在行业[%s]下，与所选行业[%s]不匹配", epack.Code, epack.ParentCode, ipack.Code))
			return
		}
	}

	oldCode, oldVer := "", ""
	var old model.TenantPackBinding
	if db.DB.Where("tenant_id = ?", t.ID).First(&old).Error == nil {
		oldCode, oldVer = old.PackCode, old.AppliedVersion
	}

	// 先物化后落绑定（顺序不可换，见文件头口径）
	res, err := industrypack.ApplyToTenant(ipc, t.ID, 0)
	if err != nil {
		RespErrInternal(c, err, "行业包物化失败")
		return
	}
	entCode, entVer := "", ""
	var entID *uint
	if epc != nil {
		eres, e2 := industrypack.ApplyToTenant(epc, t.ID, 0)
		if e2 != nil {
			RespErrInternal(c, e2, "企业包物化失败")
			return
		}
		id := epack.ID
		entID = &id
		entCode, entVer = epack.Code, epack.Version
		res.Templates += eres.Templates
		res.Features += eres.Features
	}
	// G-22(2026-09-24)：换包必须清旧包内容（与 admin 侧 TenantPackBind 同一口径）。
	// 不清就是"加包"不是"换包"——旧行业的话术/卖点/标签还留在租户私有层，
	// 而召回层只按 tenant_id+status 过滤、不看包绑定，AI 会把两套人设混着说。
	// 顺序：新包物化成功之后才清，失败时旧内容原样留着，绝不出现空壳租户。
	keep := []string{ipack.Code}
	if entCode != "" {
		keep = append(keep, entCode)
	}
	purged, err := industrypack.PurgeOtherPacks(t.ID, keep)
	if err != nil {
		RespErrInternal(c, err, "旧包内容清除失败")
		return
	}
	res.PurgedOldPacks = purged

	if old.ID > 0 {
		// 更新自带 tenant_id：绑定行主键虽然已定位到这一行，但"按主键改"意味着一旦上面的
		// 目标租户解析出错，就会把别的租户的绑定静默改掉。租户条件写进 WHERE，越权改不动。
		// 回写失败必须报错而不是吞掉：内容已经物化，绑定行却还指着旧版本，
		// 于是下一轮 reapply 会按旧 code 取包，这家租户从此"看着换了、实际没换"。
		if uerr := db.DB.Model(&old).Where("tenant_id = ?", t.ID).Updates(map[string]interface{}{
			"pack_id": ipack.ID, "pack_code": ipack.Code, "applied_version": ipack.Version,
			"enterprise_pack_id": entID, "enterprise_code": entCode, "enterprise_version": entVer,
		}).Error; uerr != nil {
			RespErrInternal(c, uerr, "绑定行回写失败（内容已物化，请立即重试换包以对齐版本）")
			return
		}
	} else {
		if cerr := db.DB.Create(&model.TenantPackBinding{
			TenantID: t.ID, PackID: ipack.ID, PackCode: ipack.Code,
			AppliedVersion:   ipack.Version,
			EnterprisePackID: entID, EnterpriseCode: entCode, EnterpriseVersion: entVer,
		}).Error; cerr != nil {
			RespErrInternal(c, cerr, "绑定行创建失败（内容已物化，请立即重试换包）")
			return
		}
	}
	auditSuperPack(t.ID, uid, c, "super_pack_bind", oldCode, oldVer, ipack.Code, ipack.Version, entCode, entVer, res)
	notifyPackChange(c, t.ID, "upgrade")
	RespOK(c, fmt.Sprintf("已为租户「%s」换包：%s v%s（模板 %d、卖点 %d）",
		t.Name, ipack.Code, ipack.Version, res.Templates, res.Features), gin.H{
		"tenant_id": t.ID, "pack_code": ipack.Code, "pack_version": ipack.Version,
		"enterprise_code": entCode, "enterprise_version": entVer,
		"templates": res.Templates, "features": res.Features,
		"purged_old_packs": res.PurgedOldPacks,
	})
}

// SuperTenantPackReapply POST /api/v1/super/tenants/:id/pack/reapply
// apidump:ts SuperPackReapplyResp
// 按绑定的 pack code 重新取当前可用版本并重物化——**存量租户吃到新版**的唯一通道。
// 为什么需要它：打包只是把新 .aipack 放进 data/packs 并注册新行，
// 已绑定租户的 templates/features 仍是老内容（先删后插只发生在 ApplyToTenant 里）。
// 幂等：同版本重复调用只是重刷一遍内容，不产生新绑定行。
func SuperTenantPackReapply(c *gin.Context) {
	t, uid, ok := superPackTarget(c)
	if !ok {
		return
	}
	var bind model.TenantPackBinding
	if err := db.DB.Where("tenant_id = ?", t.ID).First(&bind).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "该租户未绑定任何行业包")
		return
	}
	if bind.PackCode == "" {
		RespErr(c, http.StatusBadRequest, 400, "绑定行缺 pack_code，无法按码重物化")
		return
	}
	ipc, ipack, err := openActivePackByCode(bind.PackCode)
	if err != nil {
		RespErr(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	if ipack.PackLevel != industrypack.LevelIndustry {
		RespErr(c, http.StatusBadRequest, 400,
			fmt.Sprintf("包 %s 最新可用版本不是行业级，已停止重物化（避免把租户内容换成错误层级）", bind.PackCode))
		return
	}
	res, err := industrypack.ApplyToTenant(ipc, t.ID, 0)
	if err != nil {
		RespErrInternal(c, err, "行业包重物化失败")
		return
	}
	// 企业包同步刷新：找不到可用版本只跳过不清空——企业包下架不该顺手把租户的历史内容抹掉
	entNote := ""
	entVer := bind.EnterpriseVersion // 审计里写"这一轮之后租户身上的企业层版本"，未刷新则保持原声明值
	if bind.EnterpriseCode != "" {
		if epc, epack, e2 := openActivePackByCode(bind.EnterpriseCode); e2 == nil &&
			epack.PackLevel == industrypack.LevelEnterprise {
			if eres, e3 := industrypack.ApplyToTenant(epc, t.ID, 0); e3 == nil {
				id := epack.ID
				// 同 bind：企业层版本指针回写失败要报错，不能只把新内容悄悄挂上去——
				// 绑定行仍写着旧版，后台显示的"已应用版本"与租户身上真正的话术层就此分叉。
				if uerr := db.DB.Model(&bind).Where("tenant_id = ?", t.ID).Updates(map[string]interface{}{
					"enterprise_pack_id": &id, "enterprise_version": epack.Version,
				}).Error; uerr != nil {
					RespErrInternal(c, uerr, "企业包版本回写失败（内容已物化，请重试重物化）")
					return
				}
				res.Templates += eres.Templates
				res.Features += eres.Features
				entVer = epack.Version
				entNote = fmt.Sprintf(" + %s v%s", epack.Code, epack.Version)
			}
		}
	}
	fromVer := bind.AppliedVersion
	if uerr := db.DB.Model(&bind).Where("tenant_id = ?", t.ID).Updates(map[string]interface{}{
		"pack_id": ipack.ID, "applied_version": ipack.Version,
	}).Error; uerr != nil {
		RespErrInternal(c, uerr, "绑定行版本回写失败（内容已物化，请重试重物化）")
		return
	}
	// 重物化顺手把"身上还留着、但已不在绑定集合里"的包内容清掉（与 bind 同口径）。
	// keep 里刻意带上 bind.EnterpriseCode 原值：即使企业包这轮没找到可用版本（上面只跳过不清空），
	// 绑定行仍然声明着它，此时清掉就等于替租户做一个它没同意的解绑动作。
	keep := []string{ipack.Code}
	if bind.EnterpriseCode != "" {
		keep = append(keep, bind.EnterpriseCode)
	}
	purged, perr := industrypack.PurgeOtherPacks(t.ID, keep)
	if perr != nil {
		RespErrInternal(c, perr, "旧包内容清除失败")
		return
	}
	res.PurgedOldPacks = purged
	auditSuperPack(t.ID, uid, c, "super_pack_reapply", bind.PackCode, fromVer, ipack.Code, ipack.Version,
		bind.EnterpriseCode, entVer, res)
	notifyPackChange(c, t.ID, "upgrade")
	RespOK(c, fmt.Sprintf("已按最新版重新物化：%s v%s → v%s（模板 %d、卖点 %d）",
		ipack.Code, fromVer, ipack.Version, res.Templates, res.Features), gin.H{
		"tenant_id": t.ID, "pack_code": ipack.Code,
		"from_version": fromVer, "to_version": ipack.Version,
		"templates": res.Templates, "features": res.Features, "enterprise": entNote,
		"purged_old_packs": res.PurgedOldPacks,
	})
}

// auditSuperPack 换包/重物化留痕——审计行是"谁把这家租户的话术层换掉了"的唯一凭据
// （包内容是覆盖式写入：先删后插，没有历史行可回溯，所以这里必须把 from/to 版本写全）
func auditSuperPack(tenantID, uid uint, c *gin.Context, action, fromCode, fromVer, toCode, toVer, entCode, entVer string, res *industrypack.ApplyResult) {
	db.DB.Create(&model.TenantAuditLog{
		TenantID: tenantID, UserID: uid, Action: action,
		Resource: fmt.Sprintf("tenant:%d", tenantID),
		// 包内容是覆盖式写入（先删后插，无历史行可回溯），所以 from/to 两级版本都要写全：
		// 只写行业层版本的话，事后无法回答"那次的企业层是哪一个版本"。
		Detail: fmt.Sprintf(`{"from":"%s %s","to":"%s %s","enterprise":"%s %s","templates":%d,"features":%d}`,
			fromCode, fromVer, toCode, toVer, entCode, entVer, res.Templates, res.Features),
		IP:        c.ClientIP(),
		UserAgent: c.Request.UserAgent(),
	})
}
