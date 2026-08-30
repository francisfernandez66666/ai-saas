// 双层 KB 与行业包 Prompt 消费、租户资料二元组融合检索（pgvector 前朴素方案）。
package service

// ============================================================
// 双层KB与行业包 Prompt 消费（P2，2026-08-26）
//
// GetBoundPackPrompts：读取租户绑定包的 prompts.json 定制指令
//   （apply 阶段存证于 system_configs 键 pack_prompts_{code}）
// SearchTenantKnowledge：租户自有资料融合检索（二元组打分，pgvector 前的朴素方案）
// ============================================================

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
)

// boundPackCodes 取租户绑定包的行业+企业 code（去重）
func boundPackCodes(tenantID uint) []string {
	var bind model.TenantPackBinding
	if err := db.DB.Where("tenant_id = ?", tenantID).First(&bind).Error; err != nil {
		return nil
	}
	codes := []string{bind.PackCode}
	if bind.EnterpriseCode != "" && bind.EnterpriseCode != bind.PackCode {
		codes = append(codes, bind.EnterpriseCode)
	}
	return codes
}

// GetBoundPackPrompts 收集租户绑定包（行业+企业）的定制系统指令
func GetBoundPackPrompts(tenantID uint) []string {
	if tenantID == 0 {
		return nil
	}
	codes := boundPackCodes(tenantID)
	var out []string
	for _, c := range codes {
		var cfg model.SystemConfig
		if err := db.DB.Where("tenant_id = ? AND \"key\" = ?", tenantID, "pack_prompts_"+c).
			First(&cfg).Error; err != nil {
			continue
		}
		var pj struct {
			SystemInstruction string `json:"system_instruction"`
			Persona           string `json:"persona"`
		}
		if json.Unmarshal([]byte(cfg.Value), &pj) != nil {
			continue
		}
		if s := strings.TrimSpace(pj.Persona); s != "" {
			out = append(out, s)
		}
		if s := strings.TrimSpace(pj.SystemInstruction); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// GetBoundPackParams 收集租户绑定包（行业+企业）的 params.json 参数约束
// 支持 {"params": {...}} 或裸对象；扁平化为可读 "key: value" 行
func GetBoundPackParams(tenantID uint) []string {
	if tenantID == 0 {
		return nil
	}
	var out []string
	for _, c := range boundPackCodes(tenantID) {
		var cfg model.SystemConfig
		if err := db.DB.Where("tenant_id = ? AND \"key\" = ?", tenantID, "pack_params_"+c).
			First(&cfg).Error; err != nil {
			continue
		}
		out = append(out, flattenJSONLines(cfg.Value, "")...)
	}
	return out
}

// GetBoundPackMindset 收集租户绑定包（行业+企业）的 mindset.json 心态/策略指引
// 支持 {"mindset": ["...","..."]} / {"guidance": "..."} / 字符串数组
func GetBoundPackMindset(tenantID uint) []string {
	if tenantID == 0 {
		return nil
	}
	var out []string
	for _, c := range boundPackCodes(tenantID) {
		var cfg model.SystemConfig
		if err := db.DB.Where("tenant_id = ? AND \"key\" = ?", tenantID, "pack_mindset_"+c).
			First(&cfg).Error; err != nil {
			continue
		}
		var raw any
		if json.Unmarshal([]byte(cfg.Value), &raw) != nil {
			continue
		}
		switch v := raw.(type) {
		case []any:
			for _, it := range v {
				if s := strings.TrimSpace(fmt.Sprint(it)); s != "" {
					out = append(out, s)
				}
			}
		case map[string]any:
			if arr, ok := v["mindset"].([]any); ok {
				for _, it := range arr {
					if s := strings.TrimSpace(fmt.Sprint(it)); s != "" {
						out = append(out, s)
					}
				}
			}
			if g, ok := v["guidance"].(string); ok && strings.TrimSpace(g) != "" {
				out = append(out, strings.TrimSpace(g))
			}
		}
	}
	return out
}

// flattenJSONLines 将 JSON 值扁平化为 "k: v" 行（嵌套以 parent.k 表达）
func flattenJSONLines(raw, prefix string) []string {
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		// 非 JSON 则原样返回
		if s := strings.TrimSpace(raw); s != "" {
			return []string{s}
		}
		return nil
	}
	var out []string
	var walk func(val any, p string)
	walk = func(val any, p string) {
		switch t := val.(type) {
		case map[string]any:
			for k, sub := range t {
				key := k
				if p != "" {
					key = p + "." + k
				}
				walk(sub, key)
			}
		case []any:
			for i, sub := range t {
				walk(sub, fmt.Sprintf("%s[%d]", p, i))
			}
		default:
			out = append(out, fmt.Sprintf("%s: %v", p, t))
		}
	}
	if m, ok := v.(map[string]any); ok {
		if inner, ok := m["params"].(map[string]any); ok {
			walk(inner, "")
			return out
		}
	}
	walk(v, prefix)
	return out
}

// isHanOrWordR 判断是否为汉字/字母/数字（用于关键词切分，过滤标点空格）
func isHanOrWordR(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// mergeFragmentsByID 合并两个片段集并按 ID 去重（近邻结果优先保留）
func mergeFragmentsByID(a, b []model.KnowledgeFragment) []model.KnowledgeFragment {
	seen := map[uint]bool{}
	out := make([]model.KnowledgeFragment, 0, len(a)+len(b))
	for _, f := range a {
		if !seen[f.ID] {
			seen[f.ID] = true
			out = append(out, f)
		}
	}
	for _, f := range b {
		if !seen[f.ID] {
			seen[f.ID] = true
			out = append(out, f)
		}
	}
	return out
}

// extractKeywords 提取关键词token（≥2字的连续汉字/字母数字段）
func extractKeywords(input string) []string {
	input = strings.ToLower(input)
	var tokens []string
	cur := []rune{}
	for _, r := range input {
		if isHanOrWordR(r) {
			cur = append(cur, r)
		} else if len(cur) > 0 {
			if len(cur) >= 2 {
				tokens = append(tokens, string(cur))
			}
			cur = cur[:0]
		}
	}
	if len(cur) >= 2 {
		tokens = append(tokens, string(cur))
	}
	return tokens
}

// bigramSet 生成字符串的二元组集合（相邻两字片段），用于关键词相似度打分
func bigramSet(s string) map[string]bool {
	r := []rune(strings.ToLower(s))
	out := map[string]bool{}
	for i := 0; i+1 < len(r); i++ {
		out[string(r[i:i+2])] = true
	}
	return out
}

// SearchTenantKnowledge 租户自有资料检索（企业知识类片段，二元组交集打分）
// 命中阈值 ≥3；返回 top-N（默认3）。行业包内容经模板/卖点通道注入，此处只查租户层。
func SearchTenantKnowledge(tenantID uint, userInput string, limit int) []model.KnowledgeFragment {
	if tenantID == 0 || strings.TrimSpace(userInput) == "" {
		return nil
	}
	query := map[string]bool{}
	for _, t := range extractKeywords(userInput) {
		for bg := range bigramSet(t) {
			query[bg] = true
		}
	}
	if len(query) == 0 {
		return nil
	}
	// 查询向量（仅一次）：未启用向量客户端则为空 → 跳过语义分量
	var queryEmb []float32
	if DefaultEmbeddingClient != nil {
		queryEmb = DefaultEmbeddingClient.Embed(userInput)
	}

	var rows []model.KnowledgeFragment
	if pgvectorEnabled && len(queryEmb) > 0 {
		// P1-补 pgvector：SQL 向量近邻召回候选集（租户隔离 + 已向量化），再走混合打分
		qvec := toVectorLiteral(queryEmb)
		db.DB.Raw(`SELECT * FROM knowledge_fragments WHERE tenant_id = ? AND status = 1 AND category = ? AND embedding IS NOT NULL ORDER BY embedding <=> ?::vector LIMIT ?`,
			tenantID, "企业知识", qvec, limit*8+50).Scan(&rows)
		// 补充历史未向量化片段（embedding IS NULL），避免 ANN 漏检；与近邻结果按 ID 去重
		var legacy []model.KnowledgeFragment
		db.DB.Where("tenant_id = ? AND status = 1 AND category = ? AND embedding IS NULL", tenantID, "企业知识").
			Order("id DESC").Limit(300).Find(&legacy)
		rows = mergeFragmentsByID(rows, legacy)
	}
	if len(rows) == 0 {
		// 回退：全量载入走原关键词+内存余弦路径
		db.DB.Where("tenant_id = ? AND status = 1 AND category = ?", tenantID, "企业知识").
			Order("id DESC").Limit(300).Find(&rows)
	}

	// 评分（2026-08-26 v2）：按关键词逐词计分，单词贡献封顶2（部分命中也给分）
	// 总分 = 内容分 + 标题分×3；阈值 ≥2（此前整句二元组并集+阈值3 会漏掉短词强命中）
	// P0-4 向量检索：启用向量客户端时，叠加查询向量与片段向量的余弦相似度（语义召回）
	type scored struct {
		frag  model.KnowledgeFragment
		score float64
	}
	var hits []scored
	tokens := extractKeywords(userInput)
	tokenSets := make([]map[string]bool, len(tokens))
	for i, t := range tokens {
		tokenSets[i] = bigramSet(t)
	}
	for _, f := range rows {
		fb := bigramSet(f.Content)
		tb := bigramSet(f.Title)
		contentScore, titleScore := 0, 0
		for _, ts := range tokenSets {
			cb, tbh := 0, 0
			for bg := range ts {
				if fb[bg] {
					cb++
				}
				if tb[bg] {
					tbh++
				}
			}
			if cb > 2 {
				cb = 2
			}
			if tbh > 2 {
				tbh = 2
			}
			contentScore += cb
			titleScore += tbh
		}
		total := float64(contentScore + titleScore*3)
		// 语义分量：余弦相似度(0~1)映射为加权分（与关键词分同量级混合）
		if queryEmb != nil {
			if fEmb := embeddingFromJSON(f.EmbeddingJSON); len(fEmb) > 0 {
				sim := cosineSimilarity(queryEmb, fEmb)
				total += float64(sim) * 6.0 // 语义命中权重（近似一次强关键词匹配）
			}
		}
		// 阈值：纯关键词阈值≥2；含向量分量时相似度>0.3 即视为命中
		if total >= 2 || (queryEmb != nil && embeddingFromJSON(f.EmbeddingJSON) != nil && cosineSimilarity(queryEmb, embeddingFromJSON(f.EmbeddingJSON)) > 0.3) {
			hits = append(hits, scored{f, total})
		}
	}
	for i := 1; i < len(hits); i++ {
		for j := i; j > 0 && hits[j].score > hits[j-1].score; j-- {
			hits[j], hits[j-1] = hits[j-1], hits[j]
		}
	}
	if limit <= 0 {
		limit = 3
	}
	out := make([]model.KnowledgeFragment, 0, limit)
	for i := 0; i < len(hits) && i < limit; i++ {
		out = append(out, hits[i].frag)
	}
	return out
}
