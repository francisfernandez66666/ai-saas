// Package strategy 策略中心：话术模板召回（Step4，按锚/标签/阶段匹配）。
package strategy

import (
	"ai-scrm/config"
	"ai-scrm/internal/model"
	"ai-scrm/internal/runtimecfg"
	"ai-scrm/pkg/utils"
	"fmt"
	"hash/fnv"
	"strings"
)

// ============================================================
// 策略中心 - 话术模板召回 + 卖点动态填充
// 对应线上推理7步公式的 Step4
// ============================================================
// 流程：
//   1. 根据选定的锚类型a_hat，从话术库中筛选匹配的模板
//   2. 按标签匹配+相似度排序
//   3. 选最优模板后，从卖点库检索匹配的feature
//   4. 动态填充prompt_template中的占位符
// ============================================================

// RecalledTemplate 召回的模板信息
type RecalledTemplate struct {
	Template   *model.Template // 模板对象
	Similarity float64         // 相似度分数
	MatchScore float64         // 综合匹配分
}

// Step4_RecallTemplate 话术模板召回
// 输入：选定的锚类型、客户标签、客户T向量、客户ID（E4 实验分桶用，0=无法分桶退确定性最高分）、
// 租户ID（批五 B 择臂层用：pack_stats 后验按租户读取，0=无租户语境只走规则分）
// 输出：最佳匹配的话术模板
//
// 召回逻辑：
//  1. 先按锚类型过滤
//  2. 再按标签匹配度排序
//  3. 再按适用条件（意向分范围、车型等）过滤
//  4. 择臂层重排（template_bandit_enabled，默认关→纯规则）后取最高
//  5. E4：最高分若落在实验组（ab_group 非空）且组内有并列 variant，
//     按 fnv32(customerID|ab_group) % 权重和 稳定分桶择一（同一客户恒定同组）
func Step4_RecallTemplate(
	anchorType int,
	customerTags []string,
	tVector [32]float64,
	templates []model.Template,
	customerID uint,
	tenantID uint,
) (*model.Template, float64) {

	// 候选模板列表
	var candidates []RecalledTemplate

	intentScore := tVector[0]

	for i := range templates {
		t := &templates[i]

		// 1. 锚类型必须匹配
		if t.AnchorType != anchorType {
			continue
		}

		// 2. 状态必须启用
		if t.Status != 1 {
			continue
		}

		// 3. 意向分范围检查
		if t.MinIntent > 0 && intentScore < t.MinIntent {
			continue
		}
		if t.MaxIntent > 0 && intentScore > t.MaxIntent {
			continue
		}

		// 4. 计算标签匹配度
		tagMatchScore := calcTagMatchScore(t, customerTags)

		// 5. 计算相似度（综合标签匹配 + 优先级）
		similarity := tagMatchScore
		if t.Priority > 0 {
			similarity += float64(t.Priority) * 0.1
		}

		// 相似度阈值过滤
		if similarity < runtimecfg.DefaultSystemConfigService.GetFloat("sim_thresh", config.GlobalConfig.Strategy.SimThresh)*0.5 {
			continue
		}

		candidates = append(candidates, RecalledTemplate{
			Template:   t,
			Similarity: similarity,
			MatchScore: similarity,
		})
	}

	// 如果没有找到匹配的模板，返回nil
	if len(candidates) == 0 {
		// 兜底：找一个同锚类型的任意模板
		for i := range templates {
			if templates[i].AnchorType == anchorType && templates[i].Status == 1 {
				return &templates[i], 0.3
			}
		}
		return nil, 0
	}

	// 按匹配度排序，取最高的——"取最高"之前插入批五 B 择臂层（bandit.go）：
	// template_bandit_enabled=false（默认）时 rankRecallCandidates 返回的规则序与旧
	// 「首个最大分」选择逻辑逐位等价（稳定降序排序的头部即原循环选出的 best），零行为变化
	ranked := rankRecallCandidates(candidates, tenantID)
	best := ranked[0]

	// E4 实验分桶：最高分落在实验组且组内有同分并列 variant 时，按客户稳定哈希择一；
	// 未设实验组（现状绝大多数）走不到这里的多分支——零漂移
	best = pickABVariant(best, candidates, customerID)

	return best.Template, best.Similarity
}

// pickABVariant 在最高分候选中做 A/B 分桶（E4，2026-09-19 增强批）
// 规则：
//   - best 不参与实验（AbGroup 空）或组内无同分 variant → 原样返回（零漂移）
//   - variant 权重和 <= 0 → 确定性返回 best（视为未配置分流）
//   - 否则 bucket = fnv32("customerID|ab_group") % 权重和，按候选顺序累计权重落桶
//     同一客户在同一实验组内永远命中同一 variant（跨进程稳定，不依赖 map 序/随机数）
func pickABVariant(best RecalledTemplate, candidates []RecalledTemplate, customerID uint) RecalledTemplate {
	group := best.Template.AbGroup
	if group == "" {
		return best
	}

	var variants []RecalledTemplate
	var totalWeight int
	for _, c := range candidates {
		if c.Template.AbGroup == group && c.MatchScore == best.MatchScore {
			variants = append(variants, c)
			if c.Template.AbWeight > 0 {
				totalWeight += c.Template.AbWeight
			}
		}
	}
	if len(variants) < 2 || totalWeight <= 0 {
		return best
	}

	bucket := int(abHashBucket(customerID, group) % uint32(totalWeight))
	cum := 0
	for _, v := range variants {
		if v.Template.AbWeight <= 0 {
			continue // 权重 0 = 只作对照备份，不吃分流流量
		}
		cum += v.Template.AbWeight
		if bucket < cum {
			return v
		}
	}
	return best // 理论不可达（bucket < totalWeight），兜底
}

// abHashBucket 稳定分桶哈希：fnv32a 跨进程/跨版本确定，勿换成带 seed 的哈希
func abHashBucket(customerID uint, group string) uint32 {
	h := fnv.New32a()
	// fnv 的 Write 对内存 buffer 永不返回 error，忽略 errcheck 误报
	_, _ = fmt.Fprintf(h, "%d|%s", customerID, group)
	return h.Sum32()
}

// calcTagMatchScore 计算标签匹配分数
// 规则：
//   - 必须标签（required_tags）必须全部满足，否则得0分
//   - 触发标签（trigger_tags）满足越多分越高
//   - 最终分数 = 匹配的触发标签数 / 总触发标签数
func calcTagMatchScore(t *model.Template, customerTags []string) float64 {
	requiredTags := t.GetRequiredTags()
	triggerTags := t.GetTriggerTags()

	// 检查必须标签
	for _, reqTag := range requiredTags {
		if !utils.ContainsString(customerTags, reqTag) {
			return 0 // 必须标签不满足，直接0分
		}
	}

	// 如果没有触发标签，给一个基础分
	if len(triggerTags) == 0 {
		return 0.6 // 基础匹配分
	}

	// 计算触发标签匹配率
	matchCount := 0
	for _, trigTag := range triggerTags {
		if utils.ContainsString(customerTags, trigTag) {
			matchCount++
		}
	}

	return float64(matchCount) / float64(len(triggerTags))
}

// ============================================================
// 卖点动态填充
// ============================================================

// FillTemplate 填充话术模板的占位符
// 流程：
//  1. 从模板的required_features中获取需要的卖点
//  2. 根据T向量和客户标签从卖点库检索匹配的feature
//  3. 将feature的内容填充到prompt_template的{{}}占位符中
//  4. 同时填充客户相关的通用占位符（姓名、车型等）
func FillTemplate(
	template *model.Template,
	customer *model.Customer,
	features []model.Feature,
) (promptText string, hookText string, matchedFeatures []model.Feature) {

	// 1. 获取匹配的卖点
	matchedFeatures = matchFeatures(template, customer, features)

	// 2. 填充抛话术
	promptText = template.PromptTemplate
	promptText = fillPlaceholders(promptText, customer, matchedFeatures)

	// 3. 填充钩话术
	hookText = template.HookTemplate
	hookText = fillPlaceholders(hookText, customer, matchedFeatures)

	return promptText, hookText, matchedFeatures
}

// matchFeatures 匹配卖点
// 从卖点库中选出与模板需求和客户标签匹配的卖点
func matchFeatures(template *model.Template, customer *model.Customer, features []model.Feature) []model.Feature {
	requiredFeatIDs := template.GetRequiredFeatures()
	customerTags := customer.GetTags()
	customerModel := customer.InterestProduct

	var matched []model.Feature

	for i := range features {
		f := &features[i]

		// 如果模板指定了需要的卖点ID，则只取这些
		if len(requiredFeatIDs) > 0 {
			found := false
			for _, id := range requiredFeatIDs {
				if f.ID == id {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}

		// 检查状态
		if f.Status != 1 {
			continue
		}

		// 检查车型匹配
		if len(f.GetApplicableModels()) > 0 {
			modelMatch := false
			for _, m := range f.GetApplicableModels() {
				if m == customerModel {
					modelMatch = true
					break
				}
			}
			if !modelMatch {
				continue
			}
		}

		// 检查标签匹配（卖点的适用标签，客户有任意一个就算匹配）
		if len(f.GetApplicableTags()) > 0 {
			tagMatch := false
			for _, tag := range f.GetApplicableTags() {
				if utils.ContainsString(customerTags, tag) {
					tagMatch = true
					break
				}
			}
			// 如果卖点有标签约束但客户都不满足，跳过
			if !tagMatch {
				// 但如果是模板明确要求的卖点，还是保留
				if len(requiredFeatIDs) > 0 {
					// 保留
				} else {
					continue
				}
			}
		}

		matched = append(matched, *f)
	}

	return matched
}

// fillPlaceholders 填充占位符
// 支持的占位符：
//   - {{customer_name}} 客户姓名
//   - {{model}} 兴趣车型
//   - {{feature_name}} 卖点名称
//   - {{feature_desc}} 卖点描述
//   - {{price}} 价格（预算
//   - {{budget}} 预算
func fillPlaceholders(text string, customer *model.Customer, features []model.Feature) string {
	result := text

	// 通用占位符
	replaceMap := map[string]string{
		"{{customer_name}}": customer.Name,
		"{{model}}":         customer.InterestProduct,
		"{{budget}}":        fmt.Sprintf("%.0f万", customer.Budget),
		"{{region}}":        customer.Region,
	}

	for placeholder, value := range replaceMap {
		result = strings.ReplaceAll(result, placeholder, value)
	}

	// 卖点相关占位符
	if len(features) > 0 {
		// 第一个卖点
		f0 := features[0]
		f0Desc := f0.RenderDescription()

		result = strings.ReplaceAll(result, "{{feature_name}}", f0.FeatureName)
		result = strings.ReplaceAll(result, "{{feature_desc}}", f0Desc)

		// 如果有多个卖点，也支持 {{feature1_name}}, {{feature1_desc}} 等
		for i, f := range features {
			keyName := fmt.Sprintf("{{feature%d_name}}", i+1)
			keyDesc := fmt.Sprintf("{{feature%d_desc}}", i+1)
			result = strings.ReplaceAll(result, keyName, f.FeatureName)
			result = strings.ReplaceAll(result, keyDesc, f.RenderDescription())
		}
	}

	// 处理剩余的未填充占位符（清空）
	for {
		startIdx := strings.Index(result, "{{")
		if startIdx == -1 {
			break
		}
		endIdx := strings.Index(result[startIdx:], "}}")
		if endIdx == -1 {
			break
		}
		placeholder := result[startIdx : startIdx+endIdx+2]
		result = strings.ReplaceAll(result, placeholder, "")
	}

	return result
}

// ============================================================
// 条件交换机制
// 当 a_hat=对比锚/损失锚 且 抗性类型!=无 时，exchange_flag=true
// 三换：换规格(年轻人)/换付款(预算紧)/换服务(首购/老板)
// ============================================================

// CheckExchangeFlag 检查是否触发条件交换
// 条件交换：当客户有抗性时，AI不直接让步，而是提出一个交换条件
// 例如：客户说"太贵了" → "如果您今天能定，我帮您申请个金融优惠"（交换：价格 ↔ 今天定车）
// 触发条件：锚的aggressiveness >= 拆解锚（2）且存在抗性
func CheckExchangeFlag(anchorType int, resistanceType string) (bool, string) {
	// 抗性类型为"无"则不触发
	if resistanceType == "none" || resistanceType == "" {
		return false, ""
	}

	// 只有aggressiveness >= 拆解锚的才触发条件交换
	// 不抛/同类/场景锚太温和，不需要交换
	if AnchorAggressiveness[anchorType] < AnchorAggressiveness[AnchorDisassemble] {
		return false, ""
	}

	// 根据抗性类型决定交换方式
	exchangeType := ""
	switch resistanceType {
	case "price":
		// 价格抗性 -> 换付款方式/金融方案
		exchangeType = "payment"
	case "spec":
		// 规格抗性 -> 换规格/配置
		exchangeType = "spec"
	case "service":
		// 服务抗性 -> 换服务/售后
		exchangeType = "service"
	case "brand":
		// 品牌抗性 -> 换服务/价值体验
		exchangeType = "service"
	default:
		exchangeType = "spec"
	}

	return true, exchangeType
}
