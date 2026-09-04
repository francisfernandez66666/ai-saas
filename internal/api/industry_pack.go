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
//   POST /api/v1/admin/packs/unbind       解绑并清除两层物化产物
//   GET  /api/v1/admin/packs/current      当前绑定三层视图
// 部门侧：
//   POST /api/v1/admin/packs/bind-dept    {department_id, pack_id} 部门包绑定（校验挂靠企业）
//   POST /api/v1/admin/packs/unbind-dept  {department_id}
// ============================================================

import (
	"fmt"
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
	_ = mq.Publish(c.Request.Context(), mq.TopicTenantCfgEvt, tenantID,
		fmt.Sprintf("sys:t%d", tenantID), action,
		map[string]any{"action": action, "scope": "industry_pack"})
}

// SuperPackUpload POST /api/v1/super/packs （multipart: file）
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
		RespErr(c, http.StatusInternalServerError, 500, "服务端密钥未就绪: "+err.Error())
		return
	}
	src, err := fh.Open()
	if err != nil {
		RespErr(c, http.StatusBadRequest, 400, "文件读取失败")
		return
	}
	buf := make([]byte, fh.Size)
	if _, err := src.Read(buf); err != nil {
		RespErr(c, http.StatusBadRequest, 400, "文件读取失败")
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
func SuperPackStatus(c *gin.Context) {
	var req struct {
		Status string `json:"status" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || (req.Status != "active" && req.Status != "disabled") {
		RespErr(c, http.StatusBadRequest, 400, "status 必须为 active/disabled")
		return
	}
	// 超管专属(SuperRequired 守卫)：全局目录按 id 更新是预期，非租户会话被 403 前置拦截
	res := db.DB.Model(&model.IndustryPack{}).Where("id = ?", c.Param("id")).
		Update("status", req.Status)
	if res.Error != nil || res.RowsAffected == 0 {
		RespErr(c, http.StatusNotFound, 404, "包不存在")
		return
	}
	RespOK(c, "已更新", nil)
}

// TenantPackList GET /api/v1/admin/packs?level=&parent_code= 租户侧可选列表（仅 active）
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
	RespOK(c, "", rows)
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

// openActivePackByCode 按 code 取已上架包并开包（继承链回溯用）
func openActivePackByCode(code string) (*industrypack.PackContent, *model.IndustryPack, error) {
	var pack model.IndustryPack
	if err := db.DB.Where("code = ? AND status = ?", code, "active").First(&pack).Error; err != nil {
		return nil, nil, fmt.Errorf("祖先包 %s 不存在或未上架", code)
	}
	pc, _, err := openActivePack(pack.ID)
	if err != nil {
		return nil, nil, err
	}
	return pc, &pack, nil
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
// 幂等：该租户已绑定任意包则跳过；行业 code 无 active 行业包或 general 则跳过（留待默认包兜底）。
// 返回是否成功绑定；失败仅告警不阻断注册。
func BindTenantToIndustryPack(tenantID uint, industry string) bool {
	if tenantID == 0 || industry == "" || industry == "general" {
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
	if err := db.DB.Find(&tenants).Error; err != nil {
		log.Printf("[IndustryPack] 自动应用：列举租户失败 %v", err)
		return
	}
	for _, t := range tenants {
		var cnt int64
		db.DB.Model(&model.TenantPackBinding{}).Where("tenant_id = ?", t.ID).Count(&cnt)
		if cnt > 0 {
			continue
		}
		ipc, ipack, err := openActivePackByCode(indCode)
		if err != nil || ipack.PackLevel != industrypack.LevelIndustry {
			continue
		}
		if _, err := industrypack.ApplyToTenant(ipc, t.ID, 0); err != nil {
			log.Printf("[IndustryPack] 自动应用：租户 %d 行业包物化失败 %v", t.ID, err)
			continue
		}
		entID := (*uint)(nil)
		entCodeS, entVerS := "", ""
		if entCode := os.Getenv("DEFAULT_ENTERPRISE_PACK_CODE"); entCode != "" {
			if epc, epack, e2 := openActivePackByCode(entCode); e2 == nil && epack.PackLevel == industrypack.LevelEnterprise {
				if _, e3 := industrypack.ApplyToTenant(epc, t.ID, 0); e3 == nil {
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
			continue
		}
		log.Printf("[IndustryPack] 自动应用默认行业包 %s 到租户 %d（企业包 %s）", ipack.Code, t.ID, entCodeS)
	}
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
		RespErr(c, http.StatusInternalServerError, 500, "行业包物化失败: "+err.Error())
		return
	}
	entCode, entVer := "", ""
	var entID *uint
	if epc != nil {
		if _, err := industrypack.ApplyToTenant(epc, ti.ID, 0); err != nil {
			RespErr(c, http.StatusInternalServerError, 500, "企业包物化失败: "+err.Error())
			return
		}
		id := epack.ID
		entID = &id
		entCode, entVer = epack.Code, epack.Version
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
		RespErr(c, http.StatusInternalServerError, 500, "祖先包物化失败: "+err.Error())
		return
	}
	res, err := industrypack.ApplyToTenant(pc, ti.ID, req.DepartmentID)
	if err != nil {
		RespErr(c, http.StatusInternalServerError, 500, "物化失败: "+err.Error())
		return
	}
	var b model.DeptPackBinding
	if db.DB.Where("department_id = ?", req.DepartmentID).First(&b).Error == nil {
		if err := db.DB.Model(&b).Updates(map[string]interface{}{
			"pack_id": pack.ID, "pack_code": pack.Code, "applied_version": pack.Version,
		}).Error; err != nil {
			RespErr(c, http.StatusInternalServerError, 500, "绑定更新失败: "+err.Error())
			return
		}
	} else {
		// 修复(2026-08-26)：Create 失败原被静默吞掉导致"假成功"（表缺失时尤甚）
		if err := db.DB.Create(&model.DeptPackBinding{
			TenantID: ti.ID, DepartmentID: req.DepartmentID,
			PackID: pack.ID, PackCode: pack.Code, AppliedVersion: pack.Version,
		}).Error; err != nil {
			RespErr(c, http.StatusInternalServerError, 500, "绑定写入失败: "+err.Error())
			return
		}
	}
	notifyPackChange(c, ti.ID, "upgrade")
	RespOK(c, fmt.Sprintf("部门[%s] 已绑定「%s」v%s：模板 %d / 卖点 %d 生效（仅该部门链可见）",
		dept.Name, pack.Name, pack.Version, res.Templates, res.Features), res)
}

// TenantPackUnbindDept POST /api/v1/admin/packs/unbind-dept {department_id}
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
		RespErr(c, http.StatusInternalServerError, 500, "清除失败: "+err.Error())
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
	res := db.DB.Model(&model.IndustryPack{}).Where("id = ?", c.Param("id")).
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
