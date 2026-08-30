// Package industrypack 行业包打包/加密/开包/物化：templates/features 按 pk_{code}_ 前缀写入租户私有层。
package industrypack

// ============================================================
// 包内容物化（绑定/解绑的落地执行）——三级包架构版（2026-08-26）
//
// 层级语义：
//   行业包/企业包 → 租户级物化（DepartmentID=NULL，全租户可见）
//   部门包       → 部门级物化（行带 DepartmentID，仅该部门继承链召回可见）
//
// 设计：内容写入租户私有层，复用既有隔离与召回链路
//   - templates/features：id 前缀 pk_{code}_；先删后插（同层级内幂等换版本）
//     召回过滤规则见 strategy.templatesForTenant（预置0全员可见+租户匹配+部门链匹配）
//   - prompts/params/mindset：JSON 存 system_configs 租户覆盖层键（消费端 P2 接通）
//   - flows/tags：本批校验合法性并跳过导入（日志明示 P2 接入）
//
// 继承链应用逻辑（自底向上）：最底层部门包→父部门包→…→顶层部门包
//   →企业租户包→行业包。内容类(模板/卖点)为链上并集；参数类就近覆盖(P2)。
// ============================================================

import (
	"encoding/json"
	"fmt"
	"log"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"

	"gorm.io/gorm"
)

// IDPrefix 包物化ID前缀 —— 含租户域（2026-08-26 修复跨租户主键冲突）
// templates.id 是全局主键，多租户物化同一包时必须各自持有不同物理ID：
// pk_{code}_t{tenant}_{origID}；逻辑归属仍由 tenant_id 列表达
func IDPrefix(code string, tenantID uint) string {
	return fmt.Sprintf("pk_%s_t%d_", code, tenantID)
}

// ApplyResult 物化统计
type ApplyResult struct {
	Templates int `json:"templates"` // 注释：模板数
	Features  int `json:"features"` // 注释：功能列表
	Configs   int `json:"configs"` // 注释：配置数
}

// ApplyToTenant 将包内容物化到指定层级（事务）
// tenantID 必须 >0（系统层禁止）；deptID=0 租户级（行业/企业包），>0 部门级（部门包）
func ApplyToTenant(pc *PackContent, tenantID uint, deptID uint) (*ApplyResult, error) {
	if tenantID == 0 {
		return nil, fmt.Errorf("系统层(0)禁止绑定行业包——包内容只进租户私有层")
	}
	code := pc.Manifest.Code
	prefix := IDPrefix(code, tenantID)
	res := &ApplyResult{}

	err := db.DB.Transaction(func(tx *gorm.DB) error {
		scripts, err := pc.ParseScripts()
		if err != nil {
			return err
		}
		kb, err := pc.ParseProductKB()
		if err != nil {
			return err
		}

		// ---- templates：同层级先删后插（版本切换干净替换）----
		delT := tx.Where("tenant_id = ? AND id LIKE ?", tenantID, prefix+"%")
		if deptID > 0 {
			delT = delT.Where("department_id = ?", deptID)
		} else {
			delT = delT.Where("department_id IS NULL")
		}
		if err := delT.Delete(&model.Template{}).Error; err != nil {
			return err
		}
		var deptPtr *uint
		if deptID > 0 {
			d := deptID
			deptPtr = &d
		}
		for i := range scripts {
			s := &scripts[i]
			status := s.Status
			if status == 0 {
				status = 1 // 缺省启用
			}
			row := model.Template{
				ID: prefix + s.ID, TenantID: tenantID,
				AnchorType: s.AnchorType, SubType: s.SubType,
				Name: s.Name, Category: s.Category,
				MinIntent: s.MinIntent, MaxIntent: s.MaxIntent,
				PromptTemplate: s.PromptTemplate, HookTemplate: s.HookTemplate,
				Priority: s.Priority, Status: status,
				DepartmentID: deptPtr,
			}
			if len(s.TriggerTags) > 0 {
				row.TriggerTags = marshalJSON(s.TriggerTags)
			}
			if len(s.RequiredTags) > 0 {
				row.RequiredTags = marshalJSON(s.RequiredTags)
			}
			if len(s.ApplicableModels) > 0 {
				row.ApplicableModels = marshalJSON(s.ApplicableModels)
			}
			if len(s.HookFields) > 0 {
				row.HookFields = marshalJSON(s.HookFields)
			}
			if len(s.RequiredFeatures) > 0 {
				// 引用同样加前缀，指向包内物化后的卖点ID
				pf := make([]string, len(s.RequiredFeatures))
				for i, v := range s.RequiredFeatures {
					pf[i] = prefix + v
				}
				row.RequiredFeatures = marshalJSON(pf)
			}
			if err := tx.Create(&row).Error; err != nil {
				return fmt.Errorf("模板 %s 写入失败: %w", row.ID, err)
			}
			res.Templates++
		}

		// ---- features：同规则 ----
		delF := tx.Where("tenant_id = ? AND id LIKE ?", tenantID, prefix+"%")
		if deptID > 0 {
			delF = delF.Where("department_id = ?", deptID)
		} else {
			delF = delF.Where("department_id IS NULL")
		}
		if err := delF.Delete(&model.Feature{}).Error; err != nil {
			return err
		}
		for i := range kb.Features {
			f := &kb.Features[i]
			status := f.Status
			if status == 0 {
				status = 1
			}
			row := model.Feature{
				ID: prefix + f.ID, TenantID: tenantID,
				FeatureName: f.FeatureName, Category: f.Category,
				DescTemplate: f.DescTemplate, ShortDesc: f.ShortDesc,
				Priority: f.Priority, Status: status,
				DepartmentID: deptPtr,
			}
			if len(f.Params) > 0 {
				row.Params = marshalJSON(f.Params)
			}
			if len(f.ApplicableTags) > 0 {
				row.ApplicableTags = marshalJSON(f.ApplicableTags)
			}
			if len(f.ApplicableModels) > 0 {
				row.ApplicableModels = marshalJSON(f.ApplicableModels)
			}
			if err := tx.Create(&row).Error; err != nil {
				return fmt.Errorf("卖点 %s 写入失败: %w", row.ID, err)
			}
			res.Features++
		}

		// ---- prompts/params/mindset → system_configs 租户覆盖层存证（仅租户级包写配置键；
		//      部门包的参数类就近覆盖待 P2 部门语境读取端一并实现）----
		if deptID == 0 {
			for _, item := range []struct{ file, key string }{
				{FilePrompts, "pack_prompts_" + code},
				{FileParams, "pack_params_" + code},
				{FileMindset, "pack_mindset_" + code},
			} {
				raw, ok := pc.RawFile(item.file)
				if !ok || len(raw) == 0 {
					continue
				}
				if !json.Valid(raw) {
					return fmt.Errorf("%s 不是合法 JSON", item.file)
				}
				var cnt int64
				tx.Model(&model.SystemConfig{}).
					Where("tenant_id = ? AND \"key\" = ?", tenantID, item.key).Count(&cnt)
				if cnt > 0 {
					if err := tx.Model(&model.SystemConfig{}).
						Where("tenant_id = ? AND \"key\" = ?", tenantID, item.key).
						Update("value", string(raw)).Error; err != nil {
						return err
					}
				} else if err := tx.Create(&model.SystemConfig{
					TenantID: tenantID, Category: "industry_pack",
					Key: item.key, Value: string(raw), ValueType: "json",
					Description: "行业包[" + pc.Manifest.Name + "] " + item.file,
				}).Error; err != nil {
					return err
				}
				res.Configs++
			}
		}

		// ---- P2（2026-08-26）：flows.json 导入 flow_definitions（租户私有，code 加前缀防冲突）----
		if raw, ok := pc.RawFile(FileFlows); ok && len(raw) > 2 && deptID == 0 {
			var flows []struct {
				Code        string          `json:"code"`
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Nodes       json.RawMessage `json:"nodes"`
				Edges       json.RawMessage `json:"edges"`
				StartNodeID string          `json:"start_node_id"`
			}
			if err := json.Unmarshal(raw, &flows); err != nil {
				return fmt.Errorf("flows.json 解析失败: %w", err)
			}
			for _, fl := range flows {
				if fl.Code == "" || fl.Name == "" {
					continue
				}
				code := prefix + fl.Code
				nodesJSON := string(fl.Nodes)
				if nodesJSON == "" {
					nodesJSON = "[]"
				}
				edgesJSON := string(fl.Edges)
				if edgesJSON == "" {
					edgesJSON = "[]"
				}
				var existing model.FlowDefinition
				if err := tx.Where("code = ?", code).First(&existing).Error; err == nil {
					if err := tx.Model(&existing).Updates(map[string]interface{}{
						"name": fl.Name, "description": fl.Description,
						"nodes": nodesJSON, "edges": edgesJSON,
						"start_node_id": fl.StartNodeID,
					}).Error; err != nil {
						return err
					}
					continue
				}
				row := model.FlowDefinition{
					TenantID: tenantID, Name: fl.Name, Code: code,
					Description: fl.Description, NodesJSON: nodesJSON,
					EdgesJSON: edgesJSON, StartNodeID: fl.StartNodeID, Status: 1,
				}
				if err := tx.Create(&row).Error; err != nil {
					return fmt.Errorf("流程 %s 写入失败: %w", code, err)
				}
			}
		}
		// tags：仍为校验跳过（tag/tag_rules 模型耦合高，随标签引擎专项）
		if raw, ok := pc.RawFile(FileTags); ok && len(raw) > 0 && !json.Valid(raw) {
			return fmt.Errorf("tags.json 不是合法 JSON")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	scope := "租户级"
	if deptID > 0 {
		scope = fmt.Sprintf("部门%d", deptID)
	}
	log.Printf("[IndustryPack] 已物化到租户%d [%s] pack=%s v%s 模板=%d 卖点=%d 配置=%d",
		tenantID, scope, code, pc.Manifest.Version, res.Templates, res.Features, res.Configs)
	return res, nil
}

// UnbindFromTenant 解绑清除：删除该包在指定层级的全部物化产物
// deptID=0 清租户级行+配置键；deptID>0 仅清该部门前缀行（配置键不动）
func UnbindFromTenant(code string, tenantID uint, deptID uint) error {
	prefix := IDPrefix(code, tenantID)
	return db.DB.Transaction(func(tx *gorm.DB) error {
		delT := tx.Where("tenant_id = ? AND id LIKE ?", tenantID, prefix+"%")
		delF := tx.Where("tenant_id = ? AND id LIKE ?", tenantID, prefix+"%")
		if deptID > 0 {
			delT = delT.Where("department_id = ?", deptID)
			delF = delF.Where("department_id = ?", deptID)
		} else {
			delT = delT.Where("department_id IS NULL")
			delF = delF.Where("department_id IS NULL")
		}
		if err := delT.Delete(&model.Template{}).Error; err != nil {
			return err
		}
		if err := delF.Delete(&model.Feature{}).Error; err != nil {
			return err
		}
		if deptID == 0 {
			if err := tx.Where("tenant_id = ? AND \"key\" IN ?", tenantID,
				[]string{"pack_prompts_" + code, "pack_params_" + code, "pack_mindset_" + code}).
				Delete(&model.SystemConfig{}).Error; err != nil {
				return err
			}
		}
		log.Printf("[IndustryPack] 已解除绑定并清除租户%d 的包=%s 物化数据(dept=%d)", tenantID, code, deptID)
		return nil
	})
}

// marshalJSON 小工具
func marshalJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
