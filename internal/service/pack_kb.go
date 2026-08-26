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
	"strings"
	"unicode"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
)

// GetBoundPackPrompts 收集租户绑定包（行业+企业）的定制系统指令
func GetBoundPackPrompts(tenantID uint) []string {
	if tenantID == 0 {
		return nil
	}
	var bind model.TenantPackBinding
	if err := db.DB.Where("tenant_id = ?", tenantID).First(&bind).Error; err != nil {
		return nil
	}
	codes := []string{bind.PackCode}
	if bind.EnterpriseCode != "" && bind.EnterpriseCode != bind.PackCode {
		codes = append(codes, bind.EnterpriseCode)
	}
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

func isHanOrWordR(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.IsLetter(r) || unicode.IsDigit(r)
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
	var rows []model.KnowledgeFragment
	db.DB.Where("tenant_id = ? AND status = 1 AND category = ?", tenantID, "企业知识").
		Order("id DESC").Limit(300).Find(&rows)

	// 评分（2026-08-26 v2）：按关键词逐词计分，单词贡献封顶2（部分命中也给分）
	// 总分 = 内容分 + 标题分×3；阈值 ≥2（此前整句二元组并集+阈值3 会漏掉短词强命中）
	type scored struct {
		frag  model.KnowledgeFragment
		score int
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
		total := contentScore + titleScore*3
		if total >= 2 {
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
