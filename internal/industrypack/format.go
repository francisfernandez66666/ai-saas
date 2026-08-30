// Package industrypack 行业包打包/加密/开包/物化：templates/features 按 pk_{code}_ 前缀写入租户私有层。
package industrypack

// ============================================================
// 行业包格式定义（P1 行业包地基，2026-08-25）
//
// 八件套目录结构（打包源目录）：
//   manifest.json   由 CLI 自动生成（含 content_sha256，平台私钥签名）
//   scripts.json    话术模板库 → 物化到 templates 表（租户私有层）
//   product_kb.json 产品知识库：features[] → features 表；其余(faqs等)P2 消费
//   prompts.json    行业级系统指令 → 存配置键（prompt_builder P2 接通）
//   flows.json      流程模板 → P2 接入 flow_definitions
//   tags.json       标签字典 → P2 接入 tags/tag_rules
//   params.json     策略超参数 → 存配置键（引擎分片 P2 消费）
//   mindset.json    心智周期/培育参数 → 存配置键（P2 消费）
//
// .aipack 容器布局（防破解=IP保护：混合加密+签名）：
//   magic "AIP1" | u32 sigLen | RSA-SHA256(manifestJSON) | u32 manLen | manifestJSON
//   | u32 blobLen | RSA-OAEP(pub, AES-256key) | nonce(12B) | AES-GCM(tar.gz)
// ============================================================

import (
	"time"
)

// Magic 容器魔数+格式版本
const Magic = "AIP1"

// FormatVersion 当前包格式版本
const FormatVersion = 1

// 文件名常量（八件套）
const (
	FileManifest  = "manifest.json"
	FileScripts   = "scripts.json"
	FileProductKB = "product_kb.json"
	FilePrompts   = "prompts.json"
	FileFlows     = "flows.json"
	FileTags      = "tags.json"
	FileParams    = "params.json"
	FileMindset   = "mindset.json"
)

// Manifest 包元数据（明文存放，整体被平台私钥签名）
type Manifest struct {
	FormatVersion int       `json:"format_version"` // = FormatVersion
	Code          string    `json:"code"`           // 包唯一码：auto / auto_rox ...
	Name          string    `json:"name"`           // 显示名
	Industry      string    `json:"industry"`       // 注释：行业
	Version       string    `json:"version"`        // semver
	Publisher     string    `json:"publisher"`      // 注释：发布者
	CreatedAt     time.Time `json:"created_at"`     // 注释：创建时间
	ContentSHA256 string    `json:"content_sha256"` // hex(sha256(tar.gz))——解密后校验完整性
	// ---- 三级树形结构（2026-08-26 定稿）：行业 → 企业(=租户) → 部门 ----
	// industry   行业包：parent_code 为空
	// enterprise 企业包：parent_code = 所属行业包 code（企业等同租户，一企一主包）
	// department 部门包：parent_code = 所属企业包 code（P2 物化接入，先通挂树）
	PackLevel  string `json:"pack_level"`            // industry / enterprise / department
	ParentCode string `json:"parent_code,omitempty"` // 上级包 code（行业包为空）
}

// 包层级枚举
const (
	LevelIndustry   = "industry"
	LevelEnterprise = "enterprise"
	LevelDepartment = "department"
)

// ValidLevel 校验层级合法值
func ValidLevel(l string) bool {
	return l == LevelIndustry || l == LevelEnterprise || l == LevelDepartment
}

// PackContent 开包后的完整内容（内存态）
type PackContent struct {
	Manifest Manifest          // 注释：包元数据
	Files    map[string][]byte // 相对文件名 → 内容
}

// ScriptTemplate scripts.json 元素 → model.Template 映射源
type ScriptTemplate struct {
	ID               string   `json:"id"`                // 注释：主键ID
	AnchorType       int      `json:"anchor_type"`       // 注释：锚类型
	SubType          string   `json:"sub_type"`          // 注释：子类型
	Name             string   `json:"name"`              // 注释：名称
	Category         string   `json:"category"`          // 注释：分类
	TriggerTags      []string `json:"trigger_tags"`      // 注释：触发标签
	RequiredTags     []string `json:"required_tags"`     // 注释：必含标签
	MinIntent        float64  `json:"min_intent"`        // 注释：最低意向分
	MaxIntent        float64  `json:"max_intent"`        // 注释：最高意向分
	ApplicableModels []string `json:"applicable_models"` // 注释：适用车型(json)
	PromptTemplate   string   `json:"prompt_template"`   // 注释：抛话术模板
	HookTemplate     string   `json:"hook_template"`     // 注释：钩话术模板
	HookFields       []string `json:"hook_fields"`       // 注释：钩采集字段
	RequiredFeatures []string `json:"required_features"` // 注释：所需卖点
	Priority         int      `json:"priority"`          // 注释：优先级
	Status           int      `json:"status"`            // 注释：状态
}

// KbFeature product_kb.json 内 features[] 元素 → model.Feature 映射源
type KbFeature struct {
	ID               string            `json:"id"`                // 注释：主键ID
	FeatureName      string            `json:"feature_name"`      // 注释：卖点名称
	Category         string            `json:"category"`          // 注释：分类
	DescTemplate     string            `json:"desc_template"`     // 注释：描述模板
	ShortDesc        string            `json:"short_desc"`        // 注释：简短描述
	Params           map[string]string `json:"params"`            // 注释：参数(json)
	ApplicableTags   []string          `json:"applicable_tags"`   // 注释：适用标签(json)
	ApplicableModels []string          `json:"applicable_models"` // 注释：适用车型(json)
	Priority         int               `json:"priority"`          // 注释：优先级
	Status           int               `json:"status"`            // 注释：状态
}

// ProductKB product_kb.json 顶层结构（本批消费 features；其余 P2）
type ProductKB struct {
	Features []KbFeature      `json:"features"` // 注释：功能列表
	Faqs     []map[string]any `json:"faqs"`     // P2: knowledge_fragments
	Products []map[string]any `json:"products"` // P2
	Notes    string           `json:"notes"`    // 注释：备注
}

// ---- 确定性输出工具（同内容同哈希） ----

// timeNow 打包时间戳：截断到秒并取 UTC，保证跨时区/跨次打包同内容同哈希
func timeNow() time.Time { return time.Now().UTC().Truncate(time.Second) }

// timeZero tar 条目固定零时间，避免打包环境本地时间污染导致哈希漂移
func timeZero() time.Time { return time.Unix(0, 0).UTC() }

// sortStrings 插入排序文件名，保证 tar.gz 内容确定性（同输入同输出）
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
