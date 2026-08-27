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
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "缺少 file 字段（.aipack 文件）"})
		return
	}
	// 20<<20 = 20MB 硬上限，防止超大恶意包撑爆磁盘（multipart 体积预校验在落盘前拦截）
	if fh.Size > 20<<20 {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "包体超过 20MB 上限"})
		return
	}
	if !strings.HasSuffix(strings.ToLower(fh.Filename), ".aipack") {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "仅接受 .aipack 文件"})
		return
	}
	keys, err := packKeys()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "服务端密钥未就绪: " + err.Error()})
		return
	}
	src, err := fh.Open()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "文件读取失败"})
		return
	}
	buf := make([]byte, fh.Size)
	if _, err := src.Read(buf); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "文件读取失败"})
		return
	}
	_ = src.Close()

	pc, err := industrypack.Open(buf, keys)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "包校验失败: " + err.Error()})
		return
	}
	if !industrypack.ValidLevel(pc.Manifest.PackLevel) {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "manifest.pack_level 非法"})
		return
	}
	// 树形校验：企业包必须指到已存在的行业包；部门包必须指到已存在的企业包
	if pc.Manifest.ParentCode != "" {
		var parentCnt int64
		db.DB.Model(&model.IndustryPack{}).
			Where("code = ? AND status = ?", pc.Manifest.ParentCode, "active").Count(&parentCnt)
		if parentCnt == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "上级包不存在或未上架: " + pc.Manifest.ParentCode})
			return
		}
	}

	if err := os.MkdirAll(packStoreDir, 0o755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "存储目录创建失败"})
		return
	}
	storeName := fmt.Sprintf("%s_%s.aipack", pc.Manifest.Code, pc.Manifest.Version)
	storePath := filepath.Join(packStoreDir, storeName)
	if err := os.WriteFile(storePath, buf, 0o644); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "落盘失败"})
		return
	}

	var row model.IndustryPack
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
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "入库失败"})
			return
		}
	} else if err := db.DB.Save(&row).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "更新失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": "上传成功（默认下架态）", "data": row})
}

// SuperPackList GET /api/v1/super/packs
func SuperPackList(c *gin.Context) {
	var rows []model.IndustryPack
	q := db.DB.Order("id DESC")
	if lv := c.Query("level"); lv != "" {
		q = q.Where("pack_level = ?", lv)
	}
	q.Find(&rows)
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": rows})
}

// SuperPackStatus PUT /api/v1/super/packs/:id/status
func SuperPackStatus(c *gin.Context) {
	var req struct {
		Status string `json:"status" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || (req.Status != "active" && req.Status != "disabled") {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "status 必须为 active/disabled"})
		return
	}
	res := db.DB.Model(&model.IndustryPack{}).Where("id = ?", c.Param("id")).
		Update("status", req.Status)
	if res.Error != nil || res.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "包不存在"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": "已更新"})
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
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": rows})
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
		c.JSON(http.StatusForbidden, gin.H{"code": 403, "message": "无租户语境"})
		return
	}
	var req struct {
		IndustryPackID   uint  `json:"industry_pack_id" binding:"required"`
		EnterprisePackID *uint `json:"enterprise_pack_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "参数错误：industry_pack_id 必填"})
		return
	}

	ipc, ipack, err := openActivePack(req.IndustryPackID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": err.Error()})
		return
	}
	if ipack.PackLevel != industrypack.LevelIndustry {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "industry_pack_id 必须是行业级包"})
		return
	}

	var epc *industrypack.PackContent
	var epack *model.IndustryPack
	if req.EnterprisePackID != nil && *req.EnterprisePackID > 0 {
		epc, epack, err = openActivePack(*req.EnterprisePackID)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "企业包: " + err.Error()})
			return
		}
		if epack.PackLevel != industrypack.LevelEnterprise {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "enterprise_pack_id 必须是企业级包"})
			return
		}
		if epack.ParentCode != ipack.Code {
			c.JSON(http.StatusBadRequest, gin.H{
				"code":    400,
				"message": fmt.Sprintf("企业包[%s]挂在行业[%s]下，与所选行业[%s]不匹配", epack.Code, epack.ParentCode, ipack.Code),
			})
			return
		}
	}

	if _, err := industrypack.ApplyToTenant(ipc, ti.ID, 0); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "行业包物化失败: " + err.Error()})
		return
	}
	entCode, entVer := "", ""
	var entID *uint
	if epc != nil {
		if _, err := industrypack.ApplyToTenant(epc, ti.ID, 0); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "企业包物化失败: " + err.Error()})
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
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": msg})
}

// TenantPackUnbind POST /api/v1/admin/packs/unbind —— 清除两层物化产物 + 删除绑定与部门绑定
func TenantPackUnbind(c *gin.Context) {
	ti := middleware.GetTenantInfo(c)
	if ti.ID == 0 {
		c.JSON(http.StatusForbidden, gin.H{"code": 403, "message": "无租户语境"})
		return
	}
	var bind model.TenantPackBinding
	if err := db.DB.Where("tenant_id = ?", ti.ID).First(&bind).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "当前未绑定任何行业包"})
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
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": "已解绑并清除全部包内容"})
}

// TenantPackCurrent GET /api/v1/admin/packs/current —— 三层视图
func TenantPackCurrent(c *gin.Context) {
	ti := middleware.GetTenantInfo(c)
	if ti.ID == 0 {
		c.JSON(http.StatusForbidden, gin.H{"code": 403, "message": "无租户语境"})
		return
	}
	out := gin.H{"bound": false}
	var bind model.TenantPackBinding
	if db.DB.Where("tenant_id = ?", ti.ID).First(&bind).Error == nil {
		out["bound"] = true
		var ind model.IndustryPack
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
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": out})
}

// TenantPackBindDept POST /api/v1/admin/packs/bind-dept {department_id, pack_id}
// 校验：部门属本租户；pack 为 department 级且 parent==当前绑定的企业包 code
func TenantPackBindDept(c *gin.Context) {
	ti := middleware.GetTenantInfo(c)
	if ti.ID == 0 {
		c.JSON(http.StatusForbidden, gin.H{"code": 403, "message": "无租户语境"})
		return
	}
	var req struct {
		DepartmentID uint `json:"department_id" binding:"required"`
		PackID       uint `json:"pack_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "参数错误：department_id/pack_id 必填"})
		return
	}
	var bind model.TenantPackBinding
	if err := db.DB.Where("tenant_id = ?", ti.ID).First(&bind).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "请先完成行业/企业两级绑定"})
		return
	}
	if bind.EnterprisePackID == nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "未绑定企业包，部门包必须挂在企业之下"})
		return
	}
	var dept model.Department
	if err := db.DB.Where("id = ? AND tenant_id = ?", req.DepartmentID, ti.ID).First(&dept).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "部门不存在或不属于本租户"})
		return
	}
	pc, pack, err := openActivePack(req.PackID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": err.Error()})
		return
	}
	if pack.PackLevel != industrypack.LevelDepartment {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "pack_id 必须是部门级包"})
		return
	}
	if pack.ParentCode != bind.EnterpriseCode {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    400,
			"message": fmt.Sprintf("部门包[%s]挂靠企业[%s]，与本租户企业包[%s]不匹配", pack.Code, pack.ParentCode, bind.EnterpriseCode),
		})
		return
	}
	// 继承链物化：部门包仅写入本部门层；其企业包/行业包祖先内容必须落到租户级
	// （department_id=NULL）才能被本部门语境召回（strategy 查询按 NULL+本部门并集）。
	// 否则仅绑部门包会导致祖先内容完全缺失（P2 修复： advertised 继承但未实现）。
	if err := applyAncestorChain(ti.ID, pack.ParentCode); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "祖先包物化失败: " + err.Error()})
		return
	}
	res, err := industrypack.ApplyToTenant(pc, ti.ID, req.DepartmentID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "物化失败: " + err.Error()})
		return
	}
	var b model.DeptPackBinding
	if db.DB.Where("department_id = ?", req.DepartmentID).First(&b).Error == nil {
		if err := db.DB.Model(&b).Updates(map[string]interface{}{
			"pack_id": pack.ID, "pack_code": pack.Code, "applied_version": pack.Version,
		}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "绑定更新失败: " + err.Error()})
			return
		}
	} else {
		// 修复(2026-08-26)：Create 失败原被静默吞掉导致"假成功"（表缺失时尤甚）
		if err := db.DB.Create(&model.DeptPackBinding{
			TenantID: ti.ID, DepartmentID: req.DepartmentID,
			PackID: pack.ID, PackCode: pack.Code, AppliedVersion: pack.Version,
		}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "绑定写入失败: " + err.Error()})
			return
		}
	}
	notifyPackChange(c, ti.ID, "upgrade")
	c.JSON(http.StatusOK, gin.H{
		"code": 0,
		"message": fmt.Sprintf("部门[%s] 已绑定「%s」v%s：模板 %d / 卖点 %d 生效（仅该部门链可见）",
			dept.Name, pack.Name, pack.Version, res.Templates, res.Features),
		"data": res,
	})
}

// TenantPackUnbindDept POST /api/v1/admin/packs/unbind-dept {department_id}
func TenantPackUnbindDept(c *gin.Context) {
	ti := middleware.GetTenantInfo(c)
	if ti.ID == 0 {
		c.JSON(http.StatusForbidden, gin.H{"code": 403, "message": "无租户语境"})
		return
	}
	var req struct {
		DepartmentID uint `json:"department_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "参数错误：department_id 必填"})
		return
	}
	var b model.DeptPackBinding
	if err := db.DB.Where("tenant_id = ? AND department_id = ?", ti.ID, req.DepartmentID).
		First(&b).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "该部门未绑定部门包"})
		return
	}
	if err := industrypack.UnbindFromTenant(b.PackCode, ti.ID, req.DepartmentID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "清除失败: " + err.Error()})
		return
	}
	db.DB.Delete(&b)
	notifyPackChange(c, ti.ID, "rollback")
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": "部门包已解绑并清除"})
}

// SuperPackShare PUT /api/v1/super/packs/:id/share {share:0|1}
// 跨部门共享开关（KB继承链④层包级 opt-out；仅部门级包有意义，超管专属）
func SuperPackShare(c *gin.Context) {
	var req struct {
		Share *int `json:"share" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || (*req.Share != 0 && *req.Share != 1) {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "share 必须为 0 或 1"})
		return
	}
	res := db.DB.Model(&model.IndustryPack{}).Where("id = ?", c.Param("id")).
		Update("share_cross_dept", *req.Share)
	if res.Error != nil || res.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "包不存在"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": fmt.Sprintf("跨部门共享已置为 %d", *req.Share)})
}
