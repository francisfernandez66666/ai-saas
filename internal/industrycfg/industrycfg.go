// 行业语义与行业包读取层（FIX-14，2026-09-29 端到端审计批）。
//
// 为什么单独成包：`go list -deps ./internal/ai` 此前把 `ai-scrm/internal/service`
// 整个拉进 AI 底座包的依赖闭包——prompt_builder 只用了 service 里 7 个行业语义/
// 行业包读取函数，却顺着 service → mq/notify/metrics/cache/pii 的链把整棵业务编排树
// 都拖进了 AI 层的依赖面（TECH_DOC 六层分层里 ai 属于底座层，不该反向认识业务层）。
// 这 7 个函数的真实依赖只有 db/model/runtimecfg 三个底座包，把它们抽到本叶子包，
// ai 与 service 各自单向 import 这里，判据仍是单点，依赖方向却回正。
//
// 目标不变式（阶段零门禁 tools/check_ai_decoupling.sh 机器锁定）：
// `go list -deps ./internal/ai` 的结果中不得出现 `ai-scrm/internal/service`。
//
// 行业语义配置键（industry.*，system_configs 内 category="industry"）
// 本包只声明读取层自身需要的四个键；其余 industry.* 键仍在
// internal/service/industry_semantics.go，经该文件的常量别名与本包同源。
package industrycfg

import (
	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/runtimecfg"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// 行业语义配置键（industry.*，system_configs 内 category="industry"）
const (
	IndustrySalesperson      = "industry.salesperson"        // 销售顾问人设（Prompt 人设兜底）
	IndustryDomainConstraint = "industry.domain_constraint"  // 领域约束句子（Prompt 内"只聊X"指令）
	IndustryPriceReplyLead   = "industry.price_reply_lead"   // 询价回复：已留资（体验后报价，不含"约试驾"）
	IndustryPriceReplyNoLead = "industry.price_reply_nolead" // 询价回复：未留资（引导到店后报价）
)

// industryKeywordList 解析行业关键词列表（JSON 数组）
// 优先级：租户覆盖 → 系统默认(tenant_id=0) → fallback（代码内置）
// service 侧同名私有函数是本函数的别名变量（判据单点，勿复制实现）
func IndustryKeywordList(tenantID uint, key string, fallback []string) []string {
	if runtimecfg.DefaultSystemConfigService == nil {
		return fallback
	}
	list := IndustryListFrom(runtimecfg.DefaultSystemConfigService.GetStringForTenant(tenantID, key, ""))
	if len(list) > 0 {
		return list
	}
	return fallback
}

// IndustryListFrom 反序列化 JSON 数组字符串；非法/空数组返回 nil
func IndustryListFrom(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var list []string
	if err := json.Unmarshal([]byte(raw), &list); err != nil || len(list) == 0 {
		return nil
	}
	return list
}

// IndustryPriceRepliesForTenant 租户级询价回复话术
// P1-29 修复(2026-09-09)：询价硬拦截话术迁入行业键（JSON 数组）。
// lead=true 为已留资分支（体验后报价，严禁"约试驾"——P1-30），lead=false 为未留资引导分支。
// UATFOLLOWUP F2 修复(2026-09-15)：兜底按「有无行业包绑定」分流——
// 有绑定（车企等既有租户）→ 保持汽车口径不变（兼容既有行为与 UAT 断言）；
// 无绑定（general/新行业未上架包）→ 行业中立口径，不再让通用行业租户
// 收到"约试驾/车价"等汽车销售话术（UAT 字节级实测复现：general 租户询价
// 硬拦截返回试驾话术）。行业键已配置时两口径均被覆盖，优先级不变。
// 批四 P2 修正(DEFECT_VERIFY_2026-09-20)：分流谓词收紧为「汽车族绑定」（TenantUsesAutoTalk），
// 非 auto 包（edu/wedding/realty/…）绑定租户同走中立口径。
func IndustryPriceRepliesForTenant(tenantID uint, lead bool) []string {
	key := IndustryPriceReplyNoLead
	if lead {
		key = IndustryPriceReplyLead
	}
	if list := IndustryKeywordList(tenantID, key, nil); len(list) > 0 {
		return list
	}
	return PriceReplyFallback(tenantID, lead)
}

// PriceReplyFallback 询价回复兜底口径分流（F2）：
// 绑定汽车族包 → 汽车版（兼容既有车企租户）；无绑定/非 auto 包 → 行业中立版（批四 P2）。
// 导出仅供 service 侧回归测试（tenant_auto_talk_test.go）经别名调用，不是新的对外 API。
func PriceReplyFallback(tenantID uint, lead bool) []string {
	if TenantUsesAutoTalk(tenantID) {
		if lead {
			return defaultPriceRepliesLead
		}
		return defaultPriceRepliesNoLead
	}
	if lead {
		return neutralPriceRepliesLead
	}
	return neutralPriceRepliesNoLead
}

// neutralPriceRepliesLead 中立口径·已留资询价回复（不提试驾/车，行业无关）
var neutralPriceRepliesLead = []string{
	"价格得看具体方案和你的需求来定，你体验之后就清楚了",
	"费用跟方案组合有关，确认好需求我按你的情况出个详细报价",
	"具体价格看你选什么方案，你定好了我按需求给你报价",
}

// neutralPriceRepliesNoLead 中立口径·未留资询价回复（引导进一步沟通，不提行业专属动作）
var neutralPriceRepliesNoLead = []string{
	"价格得看你的需求来定，要不我先了解下你的情况，再给你做个详细报价",
	"费用要看具体方案，你说说主要想解决什么，我按需求给你报个准数",
	"价格跟方案配置有关，我先帮你捋一下需求，然后给你个合适的报价",
}

// defaultPriceRepliesLead 已留资询价回复（体验后报价，不约试驾，与 prompt 硬规则一致）
var defaultPriceRepliesLead = []string{
	"价格得看具体配置和您的需求来定，您体验后就知道了",
	"车价跟配置和选装方案有关，确认好后我按您的需求出个详细报价",
	"具体价格看您选什么配置，您定好了我按需求给您报价",
}

// defaultPriceRepliesNoLead 未留资询价回复（引导到店试驾后报价）
var defaultPriceRepliesNoLead = []string{
	"要不帮您约个试驾，体验过后我再根据您的配置需求做个报价，怎么样呀",
	"价格得看配置来定，要不先帮您约个试驾，您试完车我按您的需求做个详细报价，行不",
	"车价跟具体配置有关，要不我帮您安排个试驾，体验好了我按您的需求出个报价，您看咋样",
}

// IndustrySalespersonForTenant 租户级销售顾问人设（行业包可配置；空=空串由调用方回退内置）
func IndustrySalespersonForTenant(tenantID uint) string {
	if runtimecfg.DefaultSystemConfigService == nil {
		return ""
	}
	return runtimecfg.DefaultSystemConfigService.GetStringForTenant(tenantID, IndustrySalesperson, "")
}

// IndustryDomainConstraintForTenant 租户级领域约束句子（Prompt 内"只聊X"指令；空=空串由调用方回退内置）
func IndustryDomainConstraintForTenant(tenantID uint) string {
	if runtimecfg.DefaultSystemConfigService == nil {
		return ""
	}
	return runtimecfg.DefaultSystemConfigService.GetStringForTenant(tenantID, IndustryDomainConstraint, "")
}

// TenantUsesAutoTalk 租户兜底话术是否走汽车领域口径（批四 P2，DEFECT_VERIFY_2026-09-20）。
// 旧口径「绑定任意行业包即汽车话术」对非 auto 包错位：包加载器（pack_kb）只写
// pack_prompts/params/mindset 键、从不写 industry.*，于是绑了 edu/wedding/realty 包的租户
// 人设是"越野SUV销售"、跑题回"聊车吧"、询价回"约试驾"。
// 新口径按绑定包在 industry_packs.industry 字段的族别分流：仅汽车族（auto/auto_rox/
// auto_rox_sales 等 industry=auto 的行业包及其企业/部门子包）保留汽车兜底；
// 非 auto 包与无绑定同走行业中立口径。db 未初始化（单测环境）按中立兜底；结果 30s 进程缓存。
func TenantUsesAutoTalk(tenantID uint) bool {
	if tenantID == 0 || db.DB == nil {
		return false
	}
	if v, ok := autoTalkCache.Load(tenantID); ok {
		if e, good := v.(autoTalkCacheEntry); good && time.Now().Before(e.expireAt) {
			return e.auto
		}
	}
	auto := false
	if codes := BoundPackCodes(tenantID); len(codes) > 0 {
		var n int64
		// 任一绑定包（行业或企业码）属汽车族即算汽车租户；查询失败按中立（保守向，宁中性不错位）
		if err := db.DB.Model(&model.IndustryPack{}).
			Where("code IN ? AND industry = ?", codes, "auto").Count(&n).Error; err == nil {
			auto = n > 0
		}
	}
	autoTalkCache.Store(tenantID, autoTalkCacheEntry{auto: auto, expireAt: time.Now().Add(30 * time.Second)})
	return auto
}

// autoTalkCache 汽车族判定短TTL进程缓存（与 BoundPackCodes 同 30s 口径，检索/建 prompt 高频调用）
type autoTalkCacheEntry struct {
	auto     bool
	expireAt time.Time
}

var autoTalkCache sync.Map // tenantID(uint) → autoTalkCacheEntry

// BoundPackCodes 取租户绑定包的行业+企业 code（去重）
// 一个租户可绑定行业包和企业包，返回去重后的code列表
// P2-51 修复：加短TTL进程缓存——检索/提 prompt 每次会话都查绑定表（每片段、每 prompt 各一次）
func BoundPackCodes(tenantID uint) []string {
	if tenantID == 0 {
		return nil
	}
	// F3 修复(2026-09-15)：db 未初始化（纯逻辑单测环境）直接返回无绑定，
	// 防止 nil 指针 panic——与 TenantUsesAutoTalk 的 nil 防护口径一致
	if db.DB == nil {
		return nil
	}
	if v, ok := packBindCache.Load(tenantID); ok {
		e := v.(packBindCacheEntry)
		if time.Now().Before(e.expireAt) {
			return e.codes
		}
		packBindCache.Delete(tenantID)
	}
	var bind model.TenantPackBinding
	if err := db.DB.Where("tenant_id = ?", tenantID).First(&bind).Error; err != nil {
		packBindCache.Store(tenantID, packBindCacheEntry{codes: nil, expireAt: time.Now().Add(packBindCacheTTL)})
		return nil
	}
	codes := []string{bind.PackCode}
	if bind.EnterpriseCode != "" && bind.EnterpriseCode != bind.PackCode {
		codes = append(codes, bind.EnterpriseCode)
	}
	packBindCache.Store(tenantID, packBindCacheEntry{codes: codes, expireAt: time.Now().Add(packBindCacheTTL)})
	return codes
}

// packBindCache 租户绑定包 code 短TTL缓存（P2-51）
const packBindCacheTTL = 30 * time.Second

type packBindCacheEntry struct {
	codes    []string
	expireAt time.Time
}

var packBindCache sync.Map // tenantID(uint) → packBindCacheEntry

// GetBoundPackPrompts 收集租户绑定包（行业+企业）的定制系统指令
// 从 system_configs 表读取 pack_prompts_{code} 键值，解析 persona 和 system_instruction
// 返回拼接后的指令列表，用于构建LLM系统提示
func GetBoundPackPrompts(tenantID uint) []string {
	if tenantID == 0 {
		return nil
	}
	codes := BoundPackCodes(tenantID)
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
// 用于注入LLM提示的参数约束信息
func GetBoundPackParams(tenantID uint) []string {
	if tenantID == 0 {
		return nil
	}
	var out []string
	for _, c := range BoundPackCodes(tenantID) {
		var cfg model.SystemConfig
		if err := db.DB.Where("tenant_id = ? AND \"key\" = ?", tenantID, "pack_params_"+c).
			First(&cfg).Error; err != nil {
			continue
		}
		out = append(out, FlattenJSONLines(cfg.Value, "")...)
	}
	return out
}

// GetBoundPackMindset 收集租户绑定包（行业+企业）的 mindset.json 心态/策略指引
// 支持 {"mindset": ["...","..."]} / {"guidance": "..."} / 字符串数组
// 用于注入LLM提示的心态和策略指引
func GetBoundPackMindset(tenantID uint) []string {
	if tenantID == 0 {
		return nil
	}
	var out []string
	for _, c := range BoundPackCodes(tenantID) {
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

// FlattenJSONLines 将 JSON 值扁平化为 "k: v" 行（嵌套以 parent.k 表达）
// 递归解析JSON，将嵌套结构展平为可读的键值对
//
// 2026-09-25 残项收口：**map 分支的键必须先排序再递归**。这里的排序不是美观问题——
// 扁平化的产物会被 prompt_builder 逐行拼进【行业包参数约束】段，Go 的 map 迭代序
// 每进程每轮随机，同一份 params.json 于是每次输出的行序都在漂：
// ① 系统提示词前缀不稳定，模型侧/prompt cache 白烧；
// ② 同一租户同一份包参数在两次请求里长得不一样，排查"回复为什么变了"时无从比对。
// 键值配对本身没错（键名随递归下传），错的是**行序**——所以修法就是让序确定。
// 对照 internal/industrypack/format.go 的 sortStrings：打包侧早就按这个口径做确定性输出，
// 只是读侧的 flatten 路径漏了。
func FlattenJSONLines(raw, prefix string) []string {
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
			keys := make([]string, 0, len(t))
			for k := range t {
				keys = append(keys, k)
			}
			sort.Strings(keys) // 确定性行序：同输入必同输出（见函数头注释）
			for _, k := range keys {
				key := k
				if p != "" {
					key = p + "." + k
				}
				walk(t[k], key)
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
