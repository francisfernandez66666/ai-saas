// 行业包API：三级树形（行业/企业/部门）行业包的绑定与应用。
package api

// ============================================================
// 行业包 API —— 三级树形架构版（2026-08-26）
//
// 层级：行业(industry) → 企业(enterprise，等同租户) → 部门(department，无限嵌套)
// 绑定与应用逻辑（自底向上查起）：
//   顾问语境 = 其部门继承链上的全部部门包 → 企业租户包 → 行业包（内容并集）
//   C端/无部门语境 = 企业租户包 → 行业包
//
// 平台侧（super_admin）：
//   POST /api/v1/super/packs              multipart 上传 .aipack（manifest 携带 level/parent）
//   GET  /api/v1/super/packs              全量列表
//   PUT  /api/v1/super/packs/:id/status   启停
// 租户侧（tenant_admin）：
//   GET  /api/v1/admin/packs?level=industry|enterprise[&parent_code=]  分层可选列表
//   POST /api/v1/admin/packs/bind         {industry_pack_id, enterprise_pack_id?} 两级组合绑定
//                                         （换行业时同步清掉旧包物化产物，G-22 2026-09-24）
//   POST /api/v1/admin/packs/unbind       解绑并清除两层物化产物
//   GET  /api/v1/admin/packs/current      当前绑定三层视图
// 部门侧：
//   POST /api/v1/admin/packs/bind-dept    {department_id, pack_id} 部门包绑定（校验挂靠企业）
//   POST /api/v1/admin/packs/unbind-dept  {department_id}
// ============================================================

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"ai-scrm/internal/db"
	"ai-scrm/internal/industrypack"
	"ai-scrm/internal/middleware"
	"ai-scrm/internal/model"
	"ai-scrm/internal/mq"

	"github.com/gin-gonic/gin"
)

// packKeys 按环境变量路径装配密钥（缺省 keys/ 目录）
func packKeys() (*industrypack.Keys, error) {
	privPath := os.Getenv("INDUSTRY_PACK_PRIV_KEY")
	pubPath := os.Getenv("INDUSTRY_PACK_PUB_KEY")
	if privPath == "" {
		privPath = filepath.Join("keys", "pack_priv.pem")
	}
	if pubPath == "" {
		pubPath = filepath.Join("keys", "pack_pub.pem")
	}
	return industrypack.LoadKeysFromPaths(privPath, pubPath)
}

// packStoreDir 行业包落盘目录（data/packs），与 DB 中的 file_path 对应，供后续开包读取
const packStoreDir = "data/packs"

// notifyPackChange 绑定/解绑后发布 tenant_cfg_event → 热加载钩子刷新引擎模板池
func notifyPackChange(c *gin.Context, tenantID uint, action string) {
	_ = mq.Publish(middleware.CtxWithTrace(c), mq.TopicTenantCfgEvt, tenantID,
		fmt.Sprintf("sys:t%d", tenantID), action,
		map[string]any{"action": action, "scope": "industry_pack"})
}

// SuperPackUpload POST /api/v1/super/packs （multipart: file）
// SuperPackUpload 上传并安装行业包。
func SuperPackUpload(c *gin.Context) {
	fh, err := c.FormFile("file")
	if err != nil {
		RespErr(c, http.StatusBadRequest, 400, "缺少 file 字段（.aipack 文件）")
		return
	}
	// 20<<20 = 20MB 硬上限，防止超大恶意包撑爆磁盘（multipart 体积预校验在落盘前拦截）
	if fh.Size > 20<<20 {
		RespErr(c, http.StatusBadRequest, 400, "包体超过 20MB 上限")
		return
	}
	if !strings.HasSuffix(strings.ToLower(fh.Filename), ".aipack") {
		RespErr(c, http.StatusBadRequest, 400, "仅接受 .aipack 文件")
		return
	}
	keys, err := packKeys()
	if err != nil {
		RespErrInternal(c, err, "服务端密钥未就绪")
		return
	}
	src, err := fh.Open()
	if err != nil {
		RespErr(c, http.StatusBadRequest, 400, "文件读取失败")
		return
	}
	buf := make([]byte, fh.Size)
	// P1-39 修复(2026-09-09)：单次 Read 不保证填满（大包短读截断→损坏包入库），
	// 必须 io.ReadFull；n!=len(buf) 也算错误。
	if _, err := io.ReadFull(src, buf); err != nil {
		RespErr(c, http.StatusBadRequest, 400, "文件读取失败(短读): "+err.Error())
		return
	}
	_ = src.Close()

	pc, err := industrypack.Open(buf, keys)
	if err != nil {
		RespErr(c, http.StatusBadRequest, 400, "包校验失败: "+err.Error())
		return
	}
	if !industrypack.ValidLevel(pc.Manifest.PackLevel) {
		RespErr(c, http.StatusBadRequest, 400, "manifest.pack_level 非法")
		return
	}
	// 父包校验：IndustryPack 为全局目录表（无 tenant_id），跨租户共享是预期语义
	if pc.Manifest.ParentCode != "" {
		var parentCnt int64
		db.DB.Model(&model.IndustryPack{}).
			Where("code = ? AND status = ?", pc.Manifest.ParentCode, "active").Count(&parentCnt)
		if parentCnt == 0 {
			RespErr(c, http.StatusBadRequest, 400, "上级包不存在或未上架: "+pc.Manifest.ParentCode)
			return
		}
	}

	if err := os.MkdirAll(packStoreDir, 0o755); err != nil {
		RespErr(c, http.StatusInternalServerError, 500, "存储目录创建失败")
		return
	}
	storeName := fmt.Sprintf("%s_%s.aipack", pc.Manifest.Code, pc.Manifest.Version)
	// P1-39 修复(2026-09-09)：落盘名净化——code 含 "../" 可路径穿越写出 data/packs 之外。
	// filepath.Base 只去掉目录部分（净名仍含 "../" 也会被 Base 归一为安全名），再加白名单兜底。
	storeName = filepath.Base(storeName)
	storePath := filepath.Join(packStoreDir, storeName)
	if err := os.WriteFile(storePath, buf, 0o644); err != nil {
		RespErr(c, http.StatusInternalServerError, 500, "落盘失败")
		return
	}

	var row model.IndustryPack
	// 按 code+version 查重：全局目录表（无 tenant_id），跨租户查重是预期
	isNew := db.DB.Where("code = ? AND version = ?", pc.Manifest.Code, pc.Manifest.Version).
		First(&row).Error != nil
	row.Code = pc.Manifest.Code
	row.Name = pc.Manifest.Name
	row.Industry = pc.Manifest.Industry
	row.Version = pc.Manifest.Version
	row.PackLevel = pc.Manifest.PackLevel
	row.ParentCode = pc.Manifest.ParentCode
	row.FileName = fh.Filename
	row.FilePath = storePath
	row.FileSize = int64(len(buf))
	row.ContentSHA256 = pc.Manifest.ContentSHA256
	row.UploadedBy = middleware.GetTenantInfo(c).ID
	if isNew {
		row.Status = "disabled"
		if err := db.DB.Create(&row).Error; err != nil {
			RespErr(c, http.StatusInternalServerError, 500, "入库失败")
			return
		}
	} else if err := db.DB.Save(&row).Error; err != nil {
		RespErr(c, http.StatusInternalServerError, 500, "更新失败")
		return
	}
	RespOK(c, "上传成功（默认下架态）", row)
}

// SuperPackList GET /api/v1/super/packs
// SuperPackList 返回平台行业包列表。
func SuperPackList(c *gin.Context) {
	var rows []model.IndustryPack
	q := db.DB.Order("id DESC")
	if lv := c.Query("level"); lv != "" {
		q = q.Where("pack_level = ?", lv)
	}
	q.Find(&rows)
	RespOK(c, "", rows)
}

// SuperPackStatus PUT /api/v1/super/packs/:id/status
// SuperPackStatus 查询行业包发布状态。
func SuperPackStatus(c *gin.Context) {
	var req struct {
		Status string `json:"status" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || (req.Status != "active" && req.Status != "disabled") {
		RespErr(c, http.StatusBadRequest, 400, "status 必须为 active/disabled")
		return
	}
	// 超管专属(SuperRequired 守卫)：全局目录按 id 更新是预期，非租户会话被 403 前置拦截
	// 健壮性收口(2026-09-05)：uint 主键入口校验，非法不再触 DB
	pid, ok := PathUintID(c)
	if !ok {
		return
	}
	res := db.DB.Model(&model.IndustryPack{}).Where("id = ?", pid).
		Update("status", req.Status)
	if res.Error != nil || res.RowsAffected == 0 {
		RespErr(c, http.StatusNotFound, 404, "包不存在")
		return
	}
	RespOK(c, "已更新", nil)
}

// SuperPackTier PUT /api/v1/super/packs/:id/tier  {"min_tier":"enterprise"}
// apidump:ts SuperPackTierResp
//
// G-22c(2026-09-24)：档位门槛由平台侧声明，不写进包内 manifest——
// 同一个包对哪一档开放是销售口径（可以先只对企业版开），不该由包作者签字锁死；
// 且写进包内容就要重新打包签名，改一次门槛发一次版。
// min_tier 传空串=清除门槛（恢复全档位可见），与列默认值同一语义。
func SuperPackTier(c *gin.Context) {
	pid, ok := PathUintID(c)
	if !ok {
		return
	}
	var req struct {
		MinTier *string `json:"min_tier"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.MinTier == nil {
		RespErr(c, http.StatusBadRequest, 400, `参数错误：min_tier 必填（可传空串表示不设门槛）`)
		return
	}
	mt := *req.MinTier
	if mt != "" && !industrypack.ValidTier(mt) {
		RespErr(c, http.StatusBadRequest, 400, "min_tier 只能是 personal/enterprise/custom 或空串")
		return
	}
	res := db.DB.Model(&model.IndustryPack{}).Where("id = ?", pid).Update("min_tier", mt)
	if res.Error != nil || res.RowsAffected == 0 {
		RespErr(c, http.StatusNotFound, 404, "包不存在")
		return
	}
	var row model.IndustryPack
	db.DB.First(&row, pid)
	// 档位门槛是**销售口径的变更**（谁把这个包对个人版放开了），必须留痕；
	// 这是平台级动作、不归属任何租户，故 tenant_id=0 显式写明（g12:platform）。
	db.DB.Create(&model.TenantAuditLog{
		TenantID: 0, // g12:platform 平台目录动作，非租户会话
		UserID:   middleware.GetTenantInfo(c).ID,
		Action:   "super_pack_tier",
		Resource: fmt.Sprintf("industry_pack:%d", row.ID),
		Detail:   fmt.Sprintf(`{"pack":"%s %s","min_tier":"%s"}`, row.Code, row.Version, mt),
		IP:       c.ClientIP(),
	})
	RespOK(c, "已更新", gin.H{"id": row.ID, "code": row.Code, "min_tier": row.MinTier})
}

// tenantPackView 租户侧包列表的一行：包目录字段原样摊平（前端按 code/name 读，
// 不改形状）+ 两个档位判定结果字段。
type tenantPackView struct {
	model.IndustryPack
	// TierLocked 当前租户档位不够，界面上应置灰并提示升级（写侧一定会 403，这里只是提前如实说）
	TierLocked bool `json:"tier_locked"`
	// TierReason 稳定原因码（"" 表示可绑）：pack_tier_required / pack_min_tier_invalid / tenant_tier_unknown
	// 前端与冒烟都按码分支，不去匹配中文文案。
	TierReason string `json:"tier_reason"`
}

// TenantPackList GET /api/v1/admin/packs?level=&parent_code= 租户侧可选列表（仅 active）
// apidump:ts AdminPackListView
//
// G-22c(2026-09-24)：以前这里把全部 active 包原样摊给任何租户，绑定侧也从不看档位，
// 个人版租户因此在界面上看得见、点得动企业级行业包，一次换包就把没付费的内容物化成
// 自己的租户级模板。现在列表**标注**而不**隐藏**：让租户知道企业版还有这套内容是一句
// 升级引导，悄悄从下拉里消失只会让人以为产品没这个能力。真正的强制在写侧
// （TenantPackBind 调同一个 PackAllowedForTier 回 403），两侧共用判据故不会出现
// "界面说可绑、接口拒"的错位。
func TenantPackList(c *gin.Context) {
	q := db.DB.Where("status = ?", "active").Order("id ASC")
	if lv := c.Query("level"); lv != "" {
		q = q.Where("pack_level = ?", lv)
	}
	if p := c.Query("parent_code"); p != "" {
		q = q.Where("parent_code = ?", p)
	}
	var rows []model.IndustryPack
	q.Find(&rows)
	tenantTier := middleware.GetTenantInfo(c).Tier
	out := make([]tenantPackView, 0, len(rows))
	for _, r := range rows {
		ok, reason := industrypack.PackAllowedForTier(r.MinTier, tenantTier)
		out = append(out, tenantPackView{IndustryPack: r, TierLocked: !ok, TierReason: reason})
	}
	RespOK(c, "", out)
}

// openActivePack 读+开包公共实现
func openActivePack(packID uint) (*industrypack.PackContent, *model.IndustryPack, error) {
	var pack model.IndustryPack
	if err := db.DB.First(&pack, packID).Error; err != nil {
		return nil, nil, fmt.Errorf("行业包不存在")
	}
	if pack.Status != "active" {
		return nil, nil, fmt.Errorf("该行业包未上架")
	}
	keys, err := packKeys()
	if err != nil {
		return nil, nil, fmt.Errorf("服务端密钥未就绪")
	}
	raw, err := os.ReadFile(pack.FilePath)
	if err != nil {
		return nil, nil, fmt.Errorf("包文件缺失，请联系平台方")
	}
	pc, err := industrypack.Open(raw, keys)
	if err != nil {
		return nil, nil, fmt.Errorf("包完整性校验失败: %w", err)
	}
	return pc, &pack, nil
}

// openActivePackByCode 按 code 取已上架包并开包（继承链回溯、注册即时落包、默认包应用共用）
// 版本选择口径（G-22，2026-09-24）：同一 code 下可能并存多条 active 行——
// 历史版本（auto 1.0.0 / 1.1.0 / 1.2.0）、当年误标层级的行、以及 FilePath 为空的种子/测试行。
// 旧实现用 `First()`（按主键升序）取**最旧**那条：auto 恒命中 1.0.0，而它当年被标成 enterprise，
// 于是两个调用点的 `PackLevel != industry` 检查一律拒绝——**默认行业包与注册即时落包
// 自始至终没有为任何租户物化过内容**（现场：5 条 auto 绑定、物化产物 0 行）。
// 现按 id 倒序（后注册=新版本优先）逐条尝试开包，第一条能开成功的才算数：
// 历史空文件行与损坏行不再有能力挡住新版本，版本升级也不再依赖人工下架旧行。
func openActivePackByCode(code string) (*industrypack.PackContent, *model.IndustryPack, error) {
	var packs []model.IndustryPack
	if err := db.DB.Model(&model.IndustryPack{}).Where("code = ? AND status = ?", code, "active").
		Order("id DESC").Find(&packs).Error; err != nil || len(packs) == 0 {
		return nil, nil, fmt.Errorf("包 %s 不存在或未上架", code)
	}
	var lastErr error
	for i := range packs {
		pc, pack, err := openActivePack(packs[i].ID)
		if err == nil {
			return pc, pack, nil
		}
		lastErr = err
	}
	return nil, nil, fmt.Errorf("包 %s 无可用版本（最后错误: %v）", code, lastErr)
}

// packTierBlocked G-22c 档位门槛预检：只读包目录那一行，**不开包**。
//
// 为什么不顺手放在 openActivePack 之后：开包要签名密钥 + 包文件在盘，
// 环境没备齐时它先返回"服务端密钥未就绪/包文件缺失"，档位这条商业门禁就变成
// "看运气生效"——冒烟在 CI 里根本打不到它。预检前置后，403 与密钥无关，永远可测。
//
// 包行不存在时判"不拦"：让后续 openActivePack 去回它那句统一的"包不存在"，
// 此处若回 403 会把"包不存在"错报成"你没权限"，排查方向直接被带偏。
func packTierBlocked(packID uint, tenantTier string) (bool, string) {
	var p model.IndustryPack
	if err := db.DB.Select("min_tier").First(&p, packID).Error; err != nil {
		return false, ""
	}
	ok, reason := industrypack.PackAllowedForTier(p.MinTier, tenantTier)
	if !ok {
		log.Printf("[行业包] 档位门槛拒绝：pack=%d 需要 %s，租户档位 %s，原因 %s",
			packID, p.MinTier, tenantTier, reason)
	}
	return !ok, reason
}

// respPackTierDenied 403 + 稳定原因码（形态对齐 dealWriteErr：文案可改、码不可改）。
// 403 而非 402：与"个人版子部门超配额 403"同一套权益口径，前端已有 403 分流。
func respPackTierDenied(c *gin.Context, reason string) {
	c.JSON(http.StatusForbidden, gin.H{
		"code": 403, "message": "该行业包对当前套餐档位未开放", "error_code": "pack_tier_denied", "reason": reason,
	})
}

// applyAncestorChain 沿 ParentCode 向上物化全部祖先包到租户级（department_id=NULL），
// 保证部门语境下内容继承可见。祖先缺失/开包失败仅告警跳过（不阻断部门包绑定），
// 但企业级祖先必须成功——否则部门包失去依托。
func applyAncestorChain(tenantID uint, startParentCode string) error {
	code := startParentCode
	visited := map[string]bool{}
	for code != "" && !visited[code] {
		visited[code] = true
		pc, pack, err := openActivePackByCode(code)
		if err != nil {
			log.Printf("[IndustryPack] 继承链回溯 %s 失败: %v（跳过）", code, err)
			break
		}
		if _, err := industrypack.ApplyToTenant(pc, tenantID, 0); err != nil {
			return fmt.Errorf("祖先包 %s 物化失败: %w", code, err)
		}
		code = pack.ParentCode
	}
	return nil
}

// BindTenantToIndustryPack 按行业 code 绑定行业包到租户（注册即时落包，泛行业化 P4）
// 幂等：该租户已绑定任意包则跳过；行业 code 无 active 行业包则跳过（留待默认包兜底）。
// 返回是否成功绑定；失败仅告警不阻断注册。
// G-22b（2026-09-24）：去掉对 "general" 的硬编码早退。此前 general 只是"租户身份标签"、
// 无包内容，所以直接 return false；现在 general 已有中立兜底内容包（六件套齐全），
// 早退会让选"通用行业"注册的租户**永远拿不到任何人设与话术**，只能靠汽车基包兜底。
// 未上架 general 包的环境行为不变：openActivePackByCode 取不到 active 行业包，仍走原跳过分支。
func BindTenantToIndustryPack(tenantID uint, industry string) bool {
	if tenantID == 0 || industry == "" {
		return false
	}
	var cnt int64
	db.DB.Model(&model.TenantPackBinding{}).Where("tenant_id = ?", tenantID).Count(&cnt)
	if cnt > 0 {
		return false
	}
	ipc, ipack, err := openActivePackByCode(industry)
	if err != nil || ipack.PackLevel != industrypack.LevelIndustry {
		log.Printf("[行业包] 租户%d 行业[%s]无 active 行业包，跳过注册落包: %v", tenantID, industry, err)
		return false
	}
	// G-22c(2026-09-24)：注册即时落包是**无人看见**的写入路径（没有界面、没有 403 回执），
	// 一旦给低档位租户落了高门槛包，等于绕过付费墙白送内容，故与 /bind 同判据。
	// 跳过而非报错：注册主链路不能因为"这个行业的包要企业版"而失败，落不成包的新租户
	// 走中立兜底包或后续手动换包。
	var t model.Tenant
	if err := db.DB.First(&t, tenantID).Error; err != nil {
		log.Printf("[行业包] 租户%d 档位读取失败，跳过注册落包: %v", tenantID, err)
		return false
	}
	if ok, reason := industrypack.PackAllowedForTier(ipack.MinTier, t.Tier); !ok {
		log.Printf("[行业包] 租户%d(档位 %s) 不符合行业包 %s 的门槛 %s，注册落包跳过（原因 %s）",
			tenantID, t.Tier, ipack.Code, ipack.MinTier, reason)
		return false
	}
	if _, err := industrypack.ApplyToTenant(ipc, tenantID, 0); err != nil {
		log.Printf("[行业包] 租户%d 行业包物化失败 %s: %v", tenantID, industry, err)
		return false
	}
	if err := db.DB.Create(&model.TenantPackBinding{
		TenantID: tenantID, PackID: ipack.ID, PackCode: ipack.Code,
		AppliedVersion: ipack.Version,
	}).Error; err != nil {
		log.Printf("[行业包] 租户%d 行业包绑定写入失败 %s: %v", tenantID, industry, err)
		return false
	}
	log.Printf("[行业包] 注册即时落包: 租户%d ← 行业包 %s v%s", tenantID, ipack.Code, ipack.Version)
	return true
}

// AutoApplyDefaultIndustryPack 启动期自动应用默认行业包（auto_rox 落地）
// 对所有尚未绑定任何包的租户，绑定 DEFAULT_INDUSTRY_PACK_CODE（默认 "auto"）行业包，
// 可选叠加 DEFAULT_ENTERPRISE_PACK_CODE 企业包。幂等：已绑定租户跳过。
// 前提：对应 .aipack 已由超管上传并上架（data/packs 落盘）；未上传则静默跳过。
func AutoApplyDefaultIndustryPack() {
	indCode := os.Getenv("DEFAULT_INDUSTRY_PACK_CODE")
	if indCode == "" {
		indCode = "auto"
	}
	var tenants []model.Tenant
	// P2-73 修复：过滤注销(cancelled)/软删除租户——原 Find 全量包含已注销租户，
	// 启动物化会为它们浪费物化资源并留脏绑定。
	if err := db.DB.Where("status <> ?", "cancelled").Find(&tenants).Error; err != nil {
		log.Printf("[IndustryPack] 自动应用：列举租户失败 %v", err)
		return
	}
	for _, t := range tenants {
		var cnt int64
		db.DB.Model(&model.TenantPackBinding{}).Where("tenant_id = ?", t.ID).Count(&cnt)
		if cnt > 0 {
			continue
		}
		applyPackPairToTenant(t, indCode, os.Getenv("DEFAULT_ENTERPRISE_PACK_CODE"))
	}
}

// applyPackPairToTenant 给一个租户落"行业包 (+ 可选企业包)"这一对，并把绑定行写全。
//
// 抽成函数是 G-24(2026-09-25) 的要求：启动期默认落包和演示租户落包必须是**同一条**
// "先物化、再写 binding" 的链。只写 binding 不物化会造出"有绑定、无内容"的行，
// 而 AutoApplyDefaultIndustryPack 按"已绑过"跳过它——租户就永久卡在空包上（seed 注释同此）。
// 返回是否真的落了包（档位不符/包没上架/物化失败都回 false，调用方各自决定要不要重试）。
//
// G-22c 档位门禁保留在这里而不是调用方：两个入口都走同一判据，才不会"界面可绑、启动期绕过"。
func applyPackPairToTenant(t model.Tenant, indCode, entCode string) bool {
	if indCode == "" {
		return false
	}
	ipc, ipack, err := openActivePackByCode(indCode)
	if err != nil || ipack.PackLevel != industrypack.LevelIndustry {
		return false
	}
	// G-22c(2026-09-24)：默认落包同样过档位门槛——它是启动期批量写的，
	// 漏判等于每次重启都给全部低档位租户补发一份不该有的内容。
	if ok, reason := industrypack.PackAllowedForTier(ipack.MinTier, t.Tier); !ok {
		log.Printf("[IndustryPack] 自动应用：跳过租户 %d（档位 %s，包门槛 %s，原因 %s）",
			t.ID, t.Tier, ipack.MinTier, reason)
		return false
	}
	if _, err := industrypack.ApplyToTenant(ipc, t.ID, 0); err != nil {
		log.Printf("[IndustryPack] 自动应用：租户 %d 行业包物化失败 %v", t.ID, err)
		return false
	}
	entID := (*uint)(nil)
	entCodeS, entVerS := "", ""
	if entCode != "" {
		if epc, epack, e2 := openActivePackByCode(entCode); e2 == nil && epack.PackLevel == industrypack.LevelEnterprise {
			// 企业包同样过档位门槛（G-22c 同一条理由）：漏判就是"每次重启给低档位租户白送品牌层内容"。
			// 只跳过企业层，行业基包照常落——低档位租户该看到的是通用兜底，而不是什么都没有。
			if ok, reason := industrypack.PackAllowedForTier(epack.MinTier, t.Tier); !ok {
				log.Printf("[IndustryPack] 租户 %d(档位 %s) 不符合企业包 %s 的门槛 %s，本层跳过（原因 %s）",
					t.ID, t.Tier, epack.Code, epack.MinTier, reason)
			} else if _, e3 := industrypack.ApplyToTenant(epc, t.ID, 0); e3 == nil {
				id := epack.ID
				entID = &id
				entCodeS, entVerS = epack.Code, epack.Version
			}
		}
	}
	if err := db.DB.Create(&model.TenantPackBinding{
		TenantID: t.ID, PackID: ipack.ID, PackCode: ipack.Code,
		AppliedVersion:   ipack.Version,
		EnterprisePackID: entID, EnterpriseCode: entCodeS, EnterpriseVersion: entVerS,
	}).Error; err != nil {
		log.Printf("[IndustryPack] 自动应用：租户 %d 绑定写入失败 %v", t.ID, err)
		return false
	}
	log.Printf("[IndustryPack] 自动应用默认行业包 %s 到租户 %d（企业包 %s）", ipack.Code, t.ID, entCodeS)
	return true
}

// demoPackApplier 是 applyPackPairToTenant 的测试注入点。
// 单测要钉的是"该不该动手"这道闸（哪个租户、什么条件下才写绑定），而真落包需要
// .aipack 文件 + 签名密钥在场（CI 上未必有），不该把环境依赖引进守卫用例。
var demoPackApplier = applyPackPairToTenant

// BindSeedDemoTenantPack G-24(2026-09-25)：演示数据与行业包对齐。
//
// seed 写进库的品牌/车型/规格/竞品/知识是 auto_rox（极石）那一套，而启动期默认落包
// 给的是 `auto` 基包（DEFAULT_INDUSTRY_PACK_CODE 默认值）——全新库上演示租户会出现
// "货架上是极石的车，AI 说的是通用汽车话术"。这里按种子集合声明的包族给**那一个**
// 演示租户补上正确的包（行业包 + 企业包），真租户一律不碰：批量替租户换包等于绕过付费墙。
//
// 只在"该租户还没有任何绑定，或那条绑定是空壳"时动手，跑在 AutoApplyDefaultIndustryPack 之前；
// 之后运维在后台手动换包的回写不会被本轮重启覆盖（有内容即跳过）。
//
// 空壳＝binding 行在、但该包在该租户私有层一条内容都没落下。这不是假想形态：seed 的旧写法
// 就是"只写 binding 不物化"，本机演示租户（code=default）直到今天还卡在 pk_auto_t1_ 零模板的
// 状态上，于是 AutoApplyDefaultIndustryPack 按"已绑过"永远跳过它——租户被一行假记录锁死。
// 对**只有这一个**演示租户做修复是安全的：包族由种子集合自己声明，档位门槛照过，
// 且不删任何内容（空壳本来就没内容），只把那一行假 binding 换成真落过的。
func BindSeedDemoTenantPack(tenantCode, indCode, entCode string) {
	if tenantCode == "" || indCode == "" {
		return
	}
	var t model.Tenant
	// 启动期按 code 取演示租户：此处没有请求 ctx，tenants 又属平台表，故显式 Model 声明表名
	// （G-12 棘轮按语句里的模型名放行平台级查询，不靠注释豁免）
	if err := db.DB.Model(&model.Tenant{}).Where("code = ?", tenantCode).First(&t).Error; err != nil {
		log.Printf("[IndustryPack] 演示租户 %q 不存在，跳过演示包绑定", tenantCode)
		return
	}
	var cur model.TenantPackBinding
	err := db.DB.Where("tenant_id = ?", t.ID).First(&cur).Error
	if err == nil {
		if !packAppliedContentExists(t.ID, cur.PackCode) {
			log.Printf("[IndustryPack] 演示租户 %q 的绑定 %s v%s 是空壳（私有层零内容），删壳重落 %s/%s",
				t.ID, cur.PackCode, cur.AppliedVersion, indCode, entCode)
			if e := db.DB.Where("tenant_id = ?", t.ID).Delete(&model.TenantPackBinding{}).Error; e != nil {
				log.Printf("[IndustryPack] 演示租户 %q 空壳绑定删除失败，本轮不动手: %v", t.ID, e)
				return
			}
			demoPackApplier(t, indCode, entCode)
			return
		}
		// 有内容即不越权改写（人工换包优先）。但"存量库"的企业层必然补不上——G-24 之前上线的库
		// 演示租户早被默认落包写上了 auto 基包，现场只表现为"演示话术不贴车型"，
		// 没人会想到是启动顺序的历史账。这里把差的那一层点名说清，让运维知道该做哪一个动作
		// （本函数不替他做：企业层空也可能是他主动不要品牌层）。
		if entCode != "" && cur.EnterpriseCode == "" {
			log.Printf("[IndustryPack] 演示租户 %q 已绑基包 %s 但缺企业包 %s（G-24 前的存量绑定，本轮不改写）——需要在超管后台为其重落企业包",
				tenantCode, cur.PackCode, entCode)
		}
		return
	}
	demoPackApplier(t, indCode, entCode)
}

// packAppliedContentExists 判断某个包在该租户私有层是否真落下了内容。
//
// 判据取"任一类内容行存在"：模板/卖点按主键前缀、标签/流程按 code 前缀（pk_<code>_t<tid>_），
// 再加配置存证 pack_prompts_/pack_params_/pack_mindset_<code> 兜底。列名必须逐表给对——
// tags/flow_definitions 的主键是自增 uint，拿 `id LIKE 'pk_...'` 去问 PG 会直接 42883 报错，
// 而本函数对查询失败是保守回 true 的，于是"判据写错"会表现为"所有租户都已有内容、空壳永远修不掉"
// （单测 TestPackAppliedContentExists 第一次跑就是这么抓出来的）。
// 前缀里的 `_` 在 LIKE 里是单字符通配，但两个包 code 在前缀第 8 位就已错开（auto 是 't'、
// auto_rox 是 'r'），互不命中。
//
// 查询本身出错一律回 true 并打日志（保守不动存量）：这道判据只用来决定"要不要修一个已知的坏状态"，
// 判错一次不该换来一次内容重写。
func packAppliedContentExists(tenantID uint, packCode string) bool {
	if packCode == "" {
		return false
	}
	like := industrypack.IDPrefix(packCode, tenantID) + "%"
	// 表名由租户私有层的 id/code 前缀规则决定（pk_<包>_t<租户>_），前缀已在函数入口校验，
	// 拼进 LIKE 的参数只可能是这两段受控字符串，不含用户输入。
	for _, probe := range []struct {
		m      any // 内容表模型（字段名刻意不叫 model，避免与包名 model 在同一段里读混）
		column string
	}{
		{&model.Template{}, "id"},         // 话术模板：字符串主键就是包前缀
		{&model.Feature{}, "id"},          // 卖点：同上
		{&model.Tag{}, "code"},            // 标签：主键是自增 ID，包归属在 code 列
		{&model.FlowDefinition{}, "code"}, // 流程定义：同上
	} {
		var n int64
		if err := db.DB.Model(probe.m).Where("tenant_id = ? AND "+probe.column+" LIKE ?", tenantID, like).Count(&n).Error; err != nil {
			log.Printf("[IndustryPack] 空壳判据查询 %T.%s 失败，保守按「已有内容」处理: %v", probe.m, probe.column, err)
			return true
		}
		if n > 0 {
			return true
		}
	}
	var n int64
	if err := db.DB.Model(&model.SystemConfig{}).
		Where("tenant_id = ? AND \"key\" IN (?)", tenantID,
			[]string{"pack_prompts_" + packCode, "pack_params_" + packCode, "pack_mindset_" + packCode}).
		Count(&n).Error; err != nil {
		log.Printf("[IndustryPack] 空壳判据查询配置存证失败，保守按「已有内容」处理: %v", err)
		return true
	}
	return n > 0
}

// TenantPackBind POST /api/v1/admin/packs/bind
// {industry_pack_id 必填, enterprise_pack_id 可选}——两级组合绑定并物化
func TenantPackBind(c *gin.Context) {
	ti := middleware.GetTenantInfo(c)
	if ti.ID == 0 {
		RespErr(c, http.StatusForbidden, 403, "无租户语境")
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

	// 档位门槛判据必须在开包**之前**：openActivePack 要读签名密钥与包文件，
	// 环境没备齐时它先报错，档位这条商业门禁就成了"看运气生效"。
	// 判据与列表侧共用 PackAllowedForTier，两侧同函数才不会"界面可绑、接口 403"。
	if blocked, reason := packTierBlocked(req.IndustryPackID, ti.Tier); blocked {
		respPackTierDenied(c, reason)
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

	var epc *industrypack.PackContent
	var epack *model.IndustryPack
	if req.EnterprisePackID != nil && *req.EnterprisePackID > 0 {
		if blocked, reason := packTierBlocked(*req.EnterprisePackID, ti.Tier); blocked {
			respPackTierDenied(c, reason)
			return
		}
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
			RespErr(c, http.StatusBadRequest, 400, fmt.Sprintf("企业包[%s]挂在行业[%s]下，与所选行业[%s]不匹配", epack.Code, epack.ParentCode, ipack.Code))
			return
		}
	}

	if _, err := industrypack.ApplyToTenant(ipc, ti.ID, 0); err != nil {
		RespErrInternal(c, err, "行业包物化失败")
		return
	}
	entCode, entVer := "", ""
	var entID *uint
	if epc != nil {
		if _, err := industrypack.ApplyToTenant(epc, ti.ID, 0); err != nil {
			RespErrInternal(c, err, "企业包物化失败")
			return
		}
		id := epack.ID
		entID = &id
		entCode, entVer = epack.Code, epack.Version
	}
	// G-22(2026-09-24)：换包必须清掉旧包内容，顺序放在新包物化成功之后——
	// 失败时旧内容原样留着，不会出现"旧的删了、新的没进来"的空壳租户。
	// 清完再写绑定行，保证库里内容与被绑的包始终一致。
	keep := []string{ipack.Code}
	if entCode != "" {
		keep = append(keep, entCode)
	}
	if _, err := industrypack.PurgeOtherPacks(ti.ID, keep); err != nil {
		RespErrInternal(c, err, "旧包内容清除失败")
		return
	}

	var bind model.TenantPackBinding
	if err := db.DB.Where("tenant_id = ?", ti.ID).First(&bind).Error; err == nil {
		db.DB.Model(&bind).Updates(map[string]interface{}{
			"pack_id": ipack.ID, "pack_code": ipack.Code, "applied_version": ipack.Version,
			"enterprise_pack_id": entID, "enterprise_code": entCode, "enterprise_version": entVer,
		})
	} else {
		db.DB.Create(&model.TenantPackBinding{
			TenantID: ti.ID, PackID: ipack.ID, PackCode: ipack.Code,
			AppliedVersion:   ipack.Version,
			EnterprisePackID: entID, EnterpriseCode: entCode, EnterpriseVersion: entVer,
		})
	}
	notifyPackChange(c, ti.ID, "upgrade")

	msg := fmt.Sprintf("已绑定行业「%s」v%s", ipack.Name, ipack.Version)
	if epc != nil {
		msg += fmt.Sprintf(" + 企业包「%s」v%s", epack.Name, epack.Version)
	}
	RespOK(c, msg, nil)
}

// TenantPackUnbind POST /api/v1/admin/packs/unbind —— 清除两层物化产物 + 删除绑定与部门绑定
func TenantPackUnbind(c *gin.Context) {
	ti := middleware.GetTenantInfo(c)
	if ti.ID == 0 {
		RespErr(c, http.StatusForbidden, 403, "无租户语境")
		return
	}
	var bind model.TenantPackBinding
	if err := db.DB.Where("tenant_id = ?", ti.ID).First(&bind).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "当前未绑定任何行业包")
		return
	}
	_ = industrypack.UnbindFromTenant(bind.PackCode, ti.ID, 0)
	if bind.EnterpriseCode != "" {
		_ = industrypack.UnbindFromTenant(bind.EnterpriseCode, ti.ID, 0)
	}
	// 部门绑定随租户解绑一并清除
	db.DB.Where("tenant_id = ?", ti.ID).Delete(&model.DeptPackBinding{})
	db.DB.Delete(&bind)
	notifyPackChange(c, ti.ID, "rollback")
	RespOK(c, "已解绑并清除全部包内容", nil)
}

// TenantPackCurrent GET /api/v1/admin/packs/current —— 三层视图
func TenantPackCurrent(c *gin.Context) {
	ti := middleware.GetTenantInfo(c)
	if ti.ID == 0 {
		RespErr(c, http.StatusForbidden, 403, "无租户语境")
		return
	}
	out := gin.H{"bound": false}
	var bind model.TenantPackBinding
	if db.DB.Where("tenant_id = ?", ti.ID).First(&bind).Error == nil {
		out["bound"] = true
		var ind model.IndustryPack
		// PackID 来自上方按 tenant_id 查出的绑定行，读全局目录表对应包（跨租户查空在 rls_scope_test 已验证）
		db.DB.Select("id,code,name,industry,version,pack_level").First(&ind, bind.PackID)
		out["industry"] = ind
		if bind.EnterprisePackID != nil {
			var ent model.IndustryPack
			db.DB.Select("id,code,name,industry,version,pack_level").First(&ent, *bind.EnterprisePackID)
			out["enterprise"] = ent
		}
		var depts []model.DeptPackBinding
		db.DB.Where("tenant_id = ?", ti.ID).Find(&depts)
		out["departments"] = depts
	}
	RespOK(c, "", out)
}

// TenantPackBindDept POST /api/v1/admin/packs/bind-dept {department_id, pack_id}
// 校验：部门属本租户；pack 为 department 级且 parent==当前绑定的企业包 code
func TenantPackBindDept(c *gin.Context) {
	ti := middleware.GetTenantInfo(c)
	if ti.ID == 0 {
		RespErr(c, http.StatusForbidden, 403, "无租户语境")
		return
	}
	var req struct {
		DepartmentID uint `json:"department_id" binding:"required"`
		PackID       uint `json:"pack_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErr(c, http.StatusBadRequest, 400, "参数错误：department_id/pack_id 必填")
		return
	}
	var bind model.TenantPackBinding
	if err := db.DB.Where("tenant_id = ?", ti.ID).First(&bind).Error; err != nil {
		RespErr(c, http.StatusBadRequest, 400, "请先完成行业/企业两级绑定")
		return
	}
	if bind.EnterprisePackID == nil {
		RespErr(c, http.StatusBadRequest, 400, "未绑定企业包，部门包必须挂在企业之下")
		return
	}
	var dept model.Department
	if err := db.DB.Where("id = ? AND tenant_id = ?", req.DepartmentID, ti.ID).First(&dept).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "部门不存在或不属于本租户")
		return
	}
	// G-22c：部门包同样有 min_tier（企业版租户绑定制级部门包是同一类越档）。
	// 判据与行业/企业两级同函数，且必须在开包前——见 packTierBlocked 注释。
	if blocked, reason := packTierBlocked(req.PackID, ti.Tier); blocked {
		respPackTierDenied(c, reason)
		return
	}
	pc, pack, err := openActivePack(req.PackID)
	if err != nil {
		RespErr(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	if pack.PackLevel != industrypack.LevelDepartment {
		RespErr(c, http.StatusBadRequest, 400, "pack_id 必须是部门级包")
		return
	}
	if pack.ParentCode != bind.EnterpriseCode {
		RespErr(c, http.StatusBadRequest, 400, fmt.Sprintf("部门包[%s]挂靠企业[%s]，与本租户企业包[%s]不匹配", pack.Code, pack.ParentCode, bind.EnterpriseCode))
		return
	}
	// 继承链物化：部门包仅写入本部门层；其企业包/行业包祖先内容必须落到租户级
	// （department_id=NULL）才能被本部门语境召回（strategy 查询按 NULL+本部门并集）。
	// 否则仅绑部门包会导致祖先内容完全缺失（P2 修复： advertised 继承但未实现）。
	if err := applyAncestorChain(ti.ID, pack.ParentCode); err != nil {
		RespErrInternal(c, err, "祖先包物化失败")
		return
	}
	res, err := industrypack.ApplyToTenant(pc, ti.ID, req.DepartmentID)
	if err != nil {
		RespErrInternal(c, err, "物化失败")
		return
	}
	var b model.DeptPackBinding
	if db.DB.Where("department_id = ?", req.DepartmentID).First(&b).Error == nil {
		if err := db.DB.Model(&b).Updates(map[string]interface{}{
			"pack_id": pack.ID, "pack_code": pack.Code, "applied_version": pack.Version,
		}).Error; err != nil {
			RespErrInternal(c, err, "绑定更新失败")
			return
		}
	} else {
		// 修复(2026-08-26)：Create 失败原被静默吞掉导致"假成功"（表缺失时尤甚）
		if err := db.DB.Create(&model.DeptPackBinding{
			TenantID: ti.ID, DepartmentID: req.DepartmentID,
			PackID: pack.ID, PackCode: pack.Code, AppliedVersion: pack.Version,
		}).Error; err != nil {
			RespErrInternal(c, err, "绑定写入失败")
			return
		}
	}
	notifyPackChange(c, ti.ID, "upgrade")
	RespOK(c, fmt.Sprintf("部门[%s] 已绑定「%s」v%s：模板 %d / 卖点 %d 生效（仅该部门链可见）",
		dept.Name, pack.Name, pack.Version, res.Templates, res.Features), res)
}

// TenantPackUnbindDept POST /api/v1/admin/packs/unbind-dept {department_id}
// TenantPackUnbindDept 解除租户部门与行业包绑定。
func TenantPackUnbindDept(c *gin.Context) {
	ti := middleware.GetTenantInfo(c)
	if ti.ID == 0 {
		RespErr(c, http.StatusForbidden, 403, "无租户语境")
		return
	}
	var req struct {
		DepartmentID uint `json:"department_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErr(c, http.StatusBadRequest, 400, "参数错误：department_id 必填")
		return
	}
	var b model.DeptPackBinding
	if err := db.DB.Where("tenant_id = ? AND department_id = ?", ti.ID, req.DepartmentID).
		First(&b).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "该部门未绑定部门包")
		return
	}
	if err := industrypack.UnbindFromTenant(b.PackCode, ti.ID, req.DepartmentID); err != nil {
		RespErrInternal(c, err, "清除失败")
		return
	}
	db.DB.Delete(&b)
	notifyPackChange(c, ti.ID, "rollback")
	RespOK(c, "部门包已解绑并清除", nil)
}

// SuperPackShare PUT /api/v1/super/packs/:id/share {share:0|1}
// 跨部门共享开关（KB继承链④层包级 opt-out；仅部门级包有意义，超管专属）
func SuperPackShare(c *gin.Context) {
	var req struct {
		Share *int `json:"share" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || (*req.Share != 0 && *req.Share != 1) {
		RespErr(c, http.StatusBadRequest, 400, "share 必须为 0 或 1")
		return
	}
	// 超管专属(SuperRequired 守卫)：全局目录按 id 更新是预期，跨租户由守卫放行
	// 健壮性收口(2026-09-05)：uint 主键入口校验，非法不再触 DB
	pid, ok := PathUintID(c)
	if !ok {
		return
	}
	res := db.DB.Model(&model.IndustryPack{}).Where("id = ?", pid).
		Update("share_cross_dept", *req.Share)
	if res.Error != nil || res.RowsAffected == 0 {
		RespErr(c, http.StatusNotFound, 404, "包不存在")
		return
	}
	RespOK(c, fmt.Sprintf("跨部门共享已置为 %d", *req.Share), nil)
}

// AutoRegisterLocalPacks 启动期自动上架 data/packs 目录下的预置行业包（泛行业化 P4）
// 目标：data/packs/*.aipack 随代码分发，注册即入库 active——resolveIndustry 依赖 industry_packs
// 的 code 命中，否则新行业（realty/b2b/...）注册时全部回落 general。
// 幂等：按 code+version 查重，已存在则只补 status=active 不回写内容。
// 依赖：keys 目录存在（打包-分发共用同一对密钥）；解包失败仅告警跳过，不影响启动。
func AutoRegisterLocalPacks() {
	entries, err := os.ReadDir(packStoreDir)
	if err != nil {
		if os.IsNotExist(err) {
			log.Println("[行业包] data/packs 目录不存在，跳过自动上架")
			return
		}
		log.Printf("[行业包] 扫描 data/packs 失败: %v", err)
		return
	}
	keys, err := packKeys()
	if err != nil {
		log.Printf("[行业包] 密钥未就绪，跳过自动上架: %v", err)
		return
	}
	registered := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".aipack") {
			continue
		}
		path := filepath.Join(packStoreDir, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			log.Printf("[行业包] 读取 %s 失败: %v（跳过）", e.Name(), err)
			continue
		}
		pc, err := industrypack.Open(raw, keys)
		if err != nil {
			log.Printf("[行业包] 解包 %s 失败: %v（跳过）", e.Name(), err)
			continue
		}
		m := pc.Manifest
		var row model.IndustryPack
		isNew := db.DB.Where("code = ? AND version = ?", m.Code, m.Version).First(&row).Error != nil
		row.Code = m.Code
		row.Name = m.Name
		row.Industry = m.Industry
		row.Version = m.Version
		row.PackLevel = m.PackLevel
		row.ParentCode = m.ParentCode
		row.FileName = e.Name()
		row.FilePath = path
		row.FileSize = int64(len(raw))
		row.ContentSHA256 = m.ContentSHA256
		row.Status = "active"
		row.UploadedBy = 0 // 存储种子，平台级
		if isNew {
			if err := db.DB.Create(&row).Error; err != nil {
				log.Printf("[行业包] 注册 %s v%s 失败: %v", m.Code, m.Version, err)
				continue
			}
		} else if err := db.DB.Model(&row).Update("status", "active").Error; err != nil {
			log.Printf("[行业包] 激活 %s v%s 失败: %v", m.Code, m.Version, err)
			continue
		}
		registered++
		log.Printf("[行业包] 自动上架: code=%s name=%s v%s level=%s", m.Code, m.Name, m.Version, m.PackLevel)
	}
	log.Printf("[行业包] 自动上架完成，共注册 %d 个包", registered)
}
