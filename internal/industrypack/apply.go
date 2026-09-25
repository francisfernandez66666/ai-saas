// Package industrypack 行业包打包/加密/开包/物化：templates/features 按 pk_{code}_ 前缀写入租户私有层。
package industrypack

// ============================================================
// 包内容物化（绑定/解绑的落地执行）——三级包架构版（2026-08-26）
// 层级语义：
//   行业包/企业包 → 租户级物化（DepartmentID=NULL，全租户可见）
//   部门包       → 部门级物化（行带 DepartmentID，仅该部门继承链召回可见）
// 设计：内容写入租户私有层，复用既有隔离与召回链路
//   - templates/features：id 前缀 pk_{code}_；先删后插（同层级内幂等换版本）
//     召回过滤规则见 strategy.templatesForTenant（预置0全员可见+租户匹配+部门链匹配）
//   - prompts/params/mindset：JSON 存 system_configs 租户覆盖层键（消费端 P2 接通）
//   - flows：导入 flow_definitions（租户私有，code 加 pk_{code}_ 前缀防冲突，见下方 P2 段）
//   - tag_rules：**唯一仍跳过的一项**——它引用插入后才生成的 TargetTagID、跨包映射耦合高，
//     留 P2 标签引擎专项接入（tags 本身已物化，见 materializeTags）
// 继承链应用逻辑（自底向上）：最底层部门包→父部门包→…→顶层部门包
//   →企业租户包→行业包。内容类(模板/卖点)为链上并集；参数类就近覆盖(P2)。
// ============================================================

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// IDPrefix 包物化ID前缀 —— 含租户域（2026-08-26 修复跨租户主键冲突）
// templates.id 是全局主键，多租户物化同一包时必须各自持有不同物理ID：
// pk_{code}_t{tenant}_{origID}；逻辑归属仍由 tenant_id 列表达
func IDPrefix(code string, tenantID uint) string {
	return fmt.Sprintf("pk_%s_t%d_", code, tenantID)
}

// ApplyResult 物化统计
type ApplyResult struct {
	Templates int `json:"templates"` // 模板数
	Features  int `json:"features"`  // 功能列表
	Configs   int `json:"configs"`   // 配置数
	Tags      int `json:"tags"`      // 标签数
	// PurgedOldPacks 本次动作清掉的"已不在绑定集合里"的旧包个数（G-22 换包语义，
	// 由 PurgeOtherPacks 统计后由调用方回填——ApplyToTenant 自身不做跨包清除）
	PurgedOldPacks int `json:"purged_old_packs"`
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
			// G-21(2026-09-24)：空话术模板不进库。
			// 现场：auto 基包 scripts.json 把字段写成 "template"/"trigger_keywords"，
			// 而 ScriptTemplate 认的是 prompt_template/hook_template/trigger_tags——
			// encoding/json 对未知字段静默忽略，ParseScripts 一路"成功"，落库的三条模板
			// 话术全为空。空模板能被策略召回（无 trigger_tags 时还给 0.6 基础分），
			// 于是召回层挑中一条什么都没有的模板，AI 拿不到话术参考，现场毫无线索。
			// 口径：跳过并 WARN（宁缺毋空），空模板一条也不写。
			if strings.TrimSpace(s.PromptTemplate) == "" && strings.TrimSpace(s.HookTemplate) == "" {
				log.Printf("[行业包] scripts.json 模板 %s(%s) 的 prompt_template 与 hook_template 全为空，已跳过——检查字段名是否写成 template/content", code, s.ID)
				continue
			}
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
			// G-21：同上——卖点的正文描述为空则跳过。卖点会被 FillTemplate 注入话术，
			// 空描述卖点等于给模板填了一段空白，且让包内容统计虚高（"配了 5 个卖点"其实一个都没有）。
			if strings.TrimSpace(f.DescTemplate) == "" && strings.TrimSpace(f.ShortDesc) == "" {
				log.Printf("[行业包] product_kb.json 卖点 %s(%s) 的 desc_template 与 short_desc 全为空，已跳过", code, f.ID)
				continue
			}
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
				if jsonSemanticallyEmpty(raw) {
					// G-21(2026-09-24)：空壳（`[]`/`{}`/全空值对象）**不写覆盖键**。
					// 旧判据只挡 len(raw)==0，而打包工具给未填充的文件统一补 `[]\n` 壳，
					// 于是壳被当内容写进 pack_prompts_/pack_params_/pack_mindset_{code}。
					// 危害不是脏数据而是**静默失去继承**：industry_semantics 读键只看"非空即用"，
					// 空壳让租户的人设/参数/约束读成空串、又不回落基包与系统层——
					// 配一个未填充的子包等于把行业话术层整段关掉，现场还毫无线索。
					if cnt > 0 {
						// 自愈：历史物化留下的空覆盖键要删掉，否则这个租户永远被自己的空壳挡住。
						// 删前逐字复核库里的值也确实是空壳——非空内容永不在此路径被删。
						var cur model.SystemConfig
						if err := tx.Where("tenant_id = ? AND \"key\" = ?", tenantID, item.key).
							First(&cur).Error; err == nil && jsonSemanticallyEmpty([]byte(cur.Value)) {
							if err := tx.Delete(&cur).Error; err != nil {
								return err
							}
							log.Printf("[行业包] %s 是空壳、库内值也是空壳，已清除覆盖键 %s（回落基包/系统层）", item.file, item.key)
						}
					}
					continue
				}
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
		// G-21：flows 同样按"语义有内容"判，替掉旧的 len(raw) > 2 字节魔术数
		if raw, ok := pc.RawFile(FileFlows); ok && deptID == 0 && !jsonSemanticallyEmpty(raw) {
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
		// ---- tags.json：物化行业包预置标签（租户私有层，code 加包前缀防冲突 + 幂等换版本）----
		// tag_rules 因引用 TargetTagID（插入后生成），跨包映射耦合高，留待标签引擎专项（P2）
		if n, err := materializeTags(pc, tx, tenantID); err != nil {
			return err
		} else {
			res.Tags += n
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
	log.Printf("[IndustryPack] 已物化到租户%d [%s] pack=%s v%s 模板=%d 卖点=%d 配置=%d 标签=%d",
		tenantID, scope, code, pc.Manifest.Version, res.Templates, res.Features, res.Configs, res.Tags)
	return res, nil
}

// materializeTags 将包内 tags.json 物化到租户私有标签层（P0-3）
// 幂等：同包前缀(code LIKE pk_{code}_t{tenant}_%)先删后插，重绑即换版本。
// 标签 code 加包前缀避免与租户自建/其他包重名冲突；name 维持原语义（租户级唯一，
// 重绑时已删旧行故不冲突）。空 tags.json 直接跳过（向后兼容不报错）。
func materializeTags(pc *PackContent, tx *gorm.DB, tenantID uint) (int, error) {
	raw, ok := pc.RawFile(FileTags)
	if !ok || len(raw) == 0 {
		return 0, nil
	}
	if !json.Valid(raw) {
		return 0, fmt.Errorf("tags.json 不是合法 JSON")
	}
	var items []struct {
		Code        string  `json:"code"`
		Name        string  `json:"name"`
		Category    string  `json:"category"`
		Weight      float64 `json:"weight"`
		Description string  `json:"description"`
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		return 0, fmt.Errorf("tags.json 解析失败: %w", err)
	}
	if len(items) == 0 {
		return 0, nil
	}
	prefix := IDPrefix(pc.Manifest.Code, tenantID)
	// 幂等清除：仅删本包前缀标签，不动租户其他标签
	if err := tx.Where("tenant_id = ? AND code LIKE ?", tenantID, prefix+"%").
		Delete(&model.Tag{}).Error; err != nil {
		return 0, fmt.Errorf("清除旧包标签失败: %w", err)
	}
	count := 0
	for _, it := range items {
		if it.Code == "" || it.Name == "" {
			continue
		}
		w := it.Weight
		if w == 0 {
			w = 1.0
		}
		row := model.Tag{
			TenantID:    tenantID,
			Name:        it.Name,
			Code:        prefix + it.Code,
			Category:    it.Category,
			Weight:      w,
			Description: it.Description,
			Status:      1,
		}
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
		if result.Error != nil {
			log.Printf("[IndustryPack] 标签 %q 物化失败: %v", it.Name, result.Error)
			continue
		}
		if result.RowsAffected == 0 {
			log.Printf("[IndustryPack] 标签 %q 重名跳过(租户%d)", it.Name, tenantID)
			continue
		}
		count++
	}
	if count > 0 {
		log.Printf("[IndustryPack] 物化标签 %d 个到租户%d (pack=%s)", count, tenantID, pc.Manifest.Code)
	}
	return count, nil
}

// PurgeOtherPacks 换包时清掉"上一个包"留在租户级的物化产物（G-22，2026-09-24）。
//
// 为什么必须有：ApplyToTenant 的先删后插只删 **本包前缀** `pk_{code}_t{tenant}_` 的行，
// 换成另一个 code 的包时旧包前缀一行都不动；而召回层（strategy.templatesForTenant）
// 只按 tenant_id + status 过滤、**不看包绑定**。两者叠加的结果是"换包"其实变成了"加包"——
// 旧行业的话术、卖点、标签还留在 AI 的候选池里，现场表现为"切成通用行业后 AI 还在约试驾"。
// 解绑口 UnbindFromTenant 一直存在，但绑定路径上没有任何一处调用它，这就是缺陷本身。
//
// 口径：
//   - keepCodes 传本次绑定后的最终集合（行业包 + 可选企业包）。**同 code 重物化/升版本
//     不算换包**，故必须留在 keep 里，否则 reapply 会先把刚写进去的内容删掉。
//   - 待清 code 从**已落库的行 ID 反解**，不信任绑定行的历史记录：绑定行丢失或被人工
//     改脏时，磁盘上的旧内容同样得被换掉——只有"库里现在有什么"是事实。
//   - 只清租户级（department_id IS NULL）：部门包是另一条继承链，不由租户换包动作支配。
//   - 另扫一遍 pack_prompts_/pack_params_/pack_mindset_ 三个覆盖键：只写过配置、
//     一条模板都没出的包（内容为空壳）也要留下清理路径，否则它的键会永久遮蔽系统层。
func PurgeOtherPacks(tenantID uint, keepCodes []string) (int, error) {
	if tenantID == 0 {
		return 0, fmt.Errorf("系统层(0)禁止清除包内容")
	}
	keep := map[string]bool{}
	for _, c := range keepCodes {
		if c != "" {
			keep[c] = true
		}
	}
	found := map[string]bool{}
	mark := func(raw, marker string) {
		code := packCodeFromPrefixedID(raw, tenantID, marker)
		if code != "" && !keep[code] {
			found[code] = true
		}
	}
	// 模板与卖点行：ID 形如 pk_{code}_t{tenant}_{orig}
	var ids []string
	if err := db.DB.Model(&model.Template{}).Where("tenant_id = ? AND department_id IS NULL AND id LIKE ?",
		tenantID, "pk_%").Pluck("id", &ids).Error; err != nil {
		return 0, err
	}
	for _, id := range ids {
		mark(id, "pk_")
	}
	var fids []string
	if err := db.DB.Model(&model.Feature{}).Where("tenant_id = ? AND department_id IS NULL AND id LIKE ?",
		tenantID, "pk_%").Pluck("id", &fids).Error; err != nil {
		return 0, err
	}
	for _, id := range fids {
		mark(id, "pk_")
	}
	// 包写进 system_configs 的租户覆盖键：键名形如 pack_prompts_{code}
	var keys []string
	if err := db.DB.Model(&model.SystemConfig{}).Where("tenant_id = ? AND \"key\" LIKE ?",
		tenantID, "pack_%").Pluck("key", &keys).Error; err != nil {
		return 0, err
	}
	for _, k := range keys {
		for _, p := range []string{"pack_prompts_", "pack_params_", "pack_mindset_"} {
			if strings.HasPrefix(k, p) {
				code := strings.TrimPrefix(k, p)
				if code != "" && !keep[code] {
					found[code] = true
				}
				break
			}
		}
	}
	n := 0
	for code := range found {
		if err := UnbindFromTenant(code, tenantID, 0); err != nil {
			return n, fmt.Errorf("清除旧包 %s 物化产物失败: %w", code, err)
		}
		n++
	}
	if n > 0 {
		log.Printf("[行业包] 租户%d 换包：已清除 %d 个不在新绑定集合内的旧包物化产物", tenantID, n)
	}
	return n, nil
}

// packCodeFromPrefixedID 从物化行 ID 反解包 code。
// 前缀格式是 pk_{code}_t{tenant}_{orig}（见 IDPrefix），故 code = 去掉 `pk_` 后、
// 第一个 `_t{tenant}_` 之前的部分。刻意取**第一个**而不是最后一个：code 里允许带下划线
// （auto_rox、auto_rox_sales），而 `_t{tenant}_` 只会在 code 结束处出现一次；
// 反解失败（不是包前缀行/租户号不匹配）返回空串，调用方按"不认识"跳过。
func packCodeFromPrefixedID(id string, tenantID uint, marker string) string {
	if !strings.HasPrefix(id, marker) {
		return ""
	}
	rest := strings.TrimPrefix(id, marker)
	sep := fmt.Sprintf("_t%d_", tenantID)
	i := strings.Index(rest, sep)
	if i <= 0 {
		return ""
	}
	return rest[:i]
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
		// 清除本包前缀物化标签（code LIKE pk_{code}_t{tenant}_%）
		tagDel := tx.Where("tenant_id = ? AND code LIKE ?", tenantID, prefix+"%")
		if deptID > 0 {
			// 标签无部门列，部门级解绑仅清租户级包标签（与物化一致，租户-wide）
		}
		if err := tagDel.Delete(&model.Tag{}).Error; err != nil {
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

// jsonSemanticallyEmpty 判断一段 JSON 是否"没有承载任何内容"。
// 口径刻意保守：**只有全空才算空**——空数组、空对象、null、空串，以及
// "每个叶子值都是空"的对象/数组（如 {"keywords":[],"persona":""}）。
// 数字与布尔一律算有内容（0/false 可能是真实配置值，宁可写覆盖键也不误吞）；
// 这样最坏情况是多留一个键，而不是把包里的真东西判没了。
func jsonSemanticallyEmpty(raw []byte) bool {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return false // 非法 JSON 不在此判（上层另有 json.Valid/解析报错路径）
	}
	return valueEmpty(v)
}

// valueEmpty 递归判定 JSON 值是否为空
func valueEmpty(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(t) == ""
	case []any:
		for _, e := range t {
			if !valueEmpty(e) {
				return false
			}
		}
		return true
	case map[string]any:
		for _, e := range t {
			if !valueEmpty(e) {
				return false
			}
		}
		return true
	default:
		return false // 数字/布尔：视为有内容
	}
}
