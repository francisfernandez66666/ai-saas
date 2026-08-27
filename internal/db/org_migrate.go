// Package db 数据库连接、自动迁移、租户上下文注入与写入自动盖章（RQ/PQ/T/WithPreset/DataScope）。
package db

import (
	"log"

	"ai-scrm/internal/model"
)

// ============================================================
// 组织架构数据迁移（四级用户体系落地，幂等可重复执行）
//
// 三件事：
//  1. 存量角色映射：sales→user（四级语义），旧 admin→tenant_admin 兜底
//  2. 每租户确保存在默认根部门"销售部"（部门树必须有根）
//  3. 未挂部门的业务用户挂到根部门（readonly/user/dept_admin 均需挂载才有数据范围）
// ============================================================

// MigrateOrgData 组织数据迁移入口（main 启动时调用，幂等）
func MigrateOrgData() {
	renameLegacyAiColumns()
	repairUsageIndex()
	// 0) 修复历史空路径行（早期版本建根未回填 path）
	if res := DB.Exec(`UPDATE departments SET path = '/' || id || '/' WHERE path = '' OR path IS NULL`); res.Error == nil && res.RowsAffected > 0 {
		log.Printf("[org-migrate] 修复 %d 个缺失物化路径的部门", res.RowsAffected)
	}
	// 找默认租户作为兜底目标
	var tid uint
	if err := DB.Model(&model.Tenant{}).Where("status IN ?", []string{"active", "trial"}).
		Order("id ASC").Limit(1).Pluck("id", &tid).Error; err != nil || tid == 0 {
		log.Println("[org-migrate] 无可用租户，跳过组织迁移")
		return
	}

	// 1) 角色映射：四级语义收敛
	mappings := [][2]string{
		{"sales", model.RoleUser},
		{"admin", model.RoleTenantAdmin}, // 旧单机管理员兜底为租户管理员
	}
	for _, m := range mappings {
		res := DB.Exec(`UPDATE tenant_users SET role = ? WHERE role = ? AND role IS DISTINCT FROM 'super_admin'`, m[1], m[0])
		if res.Error != nil {
			log.Printf("[org-migrate] 角色映射 %s→%s 失败: %v", m[0], m[1], res.Error)
			continue
		}
		if res.RowsAffected > 0 {
			log.Printf("[org-migrate] 角色 %s → %s：迁移 %d 人", m[0], m[1], res.RowsAffected)
		}
	}

	// 2) 每个活跃租户确保根部门存在，并返回其ID
	type tenantRow struct {
		ID uint
	}
	var tenants []tenantRow
	if err := DB.Model(&model.Tenant{}).Where("status IN ?", []string{"active", "trial"}).
		Select("id").Find(&tenants).Error; err != nil {
		log.Printf("[org-migrate] 租户列表查询失败: %v", err)
		return
	}
	rootByTenant := map[uint]uint{}
	for _, t := range tenants {
		rootByTenant[t.ID] = ensureRootDepartment(t.ID)
	}

	// 3) 未挂部门的业务用户挂到本租户根部门
	for tidRoot, rootID := range rootByTenant {
		if rootID == 0 {
			continue
		}
		res := DB.Exec(`UPDATE tenant_users SET department_id = ?
			WHERE tenant_id = ? AND department_id IS NULL
			  AND role IN (?, ?, ?)`,
			rootID, tidRoot, model.RoleUser, model.RoleDeptAdmin, model.RoleReadOnly)
		if res.Error != nil {
			log.Printf("[org-migrate] 用户挂载根部门失败 tenant=%d: %v", tidRoot, res.Error)
			continue
		}
		if res.RowsAffected > 0 {
			log.Printf("[org-migrate] 租户%d：%d 个用户挂载到根部门%d", tidRoot, res.RowsAffected, rootID)
		}
	}
}

// ensureRootDepartment 确保租户有根部门"销售部"，返回部门ID（0=失败）
func ensureRootDepartment(tenantID uint) uint {
	var count int64
	DB.Model(&model.Department{}).Where("tenant_id = ?", tenantID).Count(&count)
	if count > 0 {
		// 已有部门树：返回 path 最短的现有根（parent_id IS NULL）
		var root model.Department
		if err := DB.Where("tenant_id = ? AND parent_id IS NULL", tenantID).
			Order("id ASC").First(&root).Error; err == nil {
			return root.ID
		}
		return 0
	}
	// 创建根部门：先插行拿ID，再回填 path="/{id}/"
	root := model.Department{
		TenantID:  tenantID,
		Name:      "销售部",
		Depth:     1,
		SortOrder: 0,
		Status:    1,
	}
	if err := DB.Create(&root).Error; err != nil {
		log.Printf("[org-migrate] 创建根部门失败 tenant=%d: %v", tenantID, err)
		return 0
	}
	path := "/" + uintToStr(root.ID) + "/"
	if err := DB.Model(&root).Update("path", path).Error; err != nil {
		log.Printf("[org-migrate] 回填根路径失败: %v", err)
		return 0
	}
	log.Printf("[org-migrate] 租户%d 创建默认根部门「销售部」 id=%d", tenantID, root.ID)
	return root.ID
}

// uintToStr 轻量转换（避免引入 strconv 到多处）
func uintToStr(v uint) string {
	if v == 0 {
		return "0"
	}
	digits := []byte{}
	for v > 0 {
		digits = append([]byte{byte('0' + v%10)}, digits...)
		v /= 10
	}
	return string(digits)
}

// renameLegacyAiColumns 历史字段拼写修正：aic_talls → ai_calls（幂等）
// 根因：早期 Go 字段 MaxAICTalls/UsedAICTalls 被 GORM 自动派生为错误列名
func renameLegacyAiColumns() {
	pairs := [][3]string{
		{"tenants", "max_aic_talls", "max_ai_calls_monthly"},
		{"tenants", "used_aic_talls", "used_ai_calls"},
	}
	for _, p := range pairs {
		var exists bool
		DB.Raw(`SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_name = ? AND column_name = ?)`, p[0], p[1]).Scan(&exists)
		if !exists {
			continue
		}
		var newExists bool
		DB.Raw(`SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_name = ? AND column_name = ?)`, p[0], p[2]).Scan(&newExists)
		if newExists {
			continue
		}
		if err := DB.Exec(`ALTER TABLE ` + p[0] + ` RENAME COLUMN ` + p[1] + ` TO ` + p[2]).Error; err != nil {
			log.Printf("[org-migrate] 列改名 %s.%s 失败: %v", p[0], p[1], err)
		} else {
			log.Printf("[org-migrate] 已修正历史列名 %s.%s → %s", p[0], p[1], p[2])
		}
	}
}

// repairUsageIndex 修复 usage_records 复合唯一索引（幂等）
// 根因：早期模型只在 date 列建了同名单列索引，ON CONFLICT (tenant_id,date,metric)
// 需要三列联合唯一才能命中
func repairUsageIndex() {
	var cols int
	DB.Raw(`SELECT COUNT(*) FROM pg_attribute a
		JOIN pg_class c ON a.attrelid = c.oid
		JOIN pg_index i ON i.indexrelid = c.oid
		WHERE c.relname = 'idx_tenant_date_metric'`).Scan(&cols)
	if cols == 3 {
		return // 已是正确复合索引
	}
	DB.Exec(`DROP INDEX IF EXISTS idx_tenant_date_metric`)
	if err := DB.Exec(`CREATE UNIQUE INDEX idx_tenant_date_metric
		ON usage_records (tenant_id, date, metric)`).Error; err != nil {
		log.Printf("[org-migrate] 重建用量索引失败: %v", err)
	} else {
		log.Printf("[org-migrate] 已重建 usage_records 三列唯一索引")
	}
}
