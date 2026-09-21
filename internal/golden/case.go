// Package golden 提供 AI 对话的「黄金问答集」回归框架（PLAN_FIX_2026-09-21 D1）。
//
// 设计目标：把「改一处 prompt / 关键词表 / 路由阈值会不会把既有行为改坏」变成一条
// **零成本、零外部依赖、可卡阈值**的自动断言，而不是靠人工抽检。
//
// 两层评分：
//   - 确定性层（本包默认，CI 门禁）：路由判定 Step6 / 询价判定 / 离线话术评分，
//     全部是纯函数或内存配置桩，不联网、不读 DB、不烧 token。
//   - LLM 评分层（可选，由调用方注入 Judge）：对自由文本回复做语义评分，
//     需要真实模型 Key 与 stage_models 配置，**不参与 CI 阈值**，只作人工/发布前仪表。
//
// 黄金集以 JSON 冻结在本包 cases/ 目录（go:embed 进二进制，随版本走），
// 刻意「冻结快照」而非运行时从关键词表生成——否则改坏表时用例会跟着一起变，
// 守卫就永远通过（本项目 A4 批踩过同类静默通过坑）。
package golden

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed cases/*.json
var casesFS embed.FS

// 用例家族：不同家族走不同断言口径。
const (
	// FamilyRouting 路由决策口径：Step6_RouteDecision 的结果必须等于 want_route。
	FamilyRouting = "routing"
	// FamilyKeyword 询价判定口径：IsPriceInquiryForTenant(默认口径) 必须等于 want_price_inquiry。
	FamilyKeyword = "keyword"
	// FamilyReply 话术评分口径：离线评分必须落在 [min_score, max_score] 区间内。
	FamilyReply = "reply"
	// FamilySafety 安全口径：违规话术必须被重扣到 max_score 以下且给出违规理由。
	FamilySafety = "safety"
)

// Case 黄金问答集单条用例。
// 字段按家族取用：routing 用状态字段 + want_route；keyword 用 want_price_inquiry；
// reply/safety 用 reply/anchors/forbidden + 评分区间。
type Case struct {
	// ID 用例唯一标识（家族前缀 + 序号，如 R-001）。
	ID string `json:"id"`
	// Family 家族：routing / keyword / reply / safety。
	Family string `json:"family"`
	// Lang 语言标记（zh/en/mixed），仅用于报告分组统计，不参与断言。
	Lang string `json:"lang,omitempty"`
	// Question 客户输入（routing/keyword 家族作为判据输入；reply 家族为问题描述，可选）。
	Question string `json:"question,omitempty"`
	// Note 用例意图说明（失败时可读，便于定位是"期望错了"还是"代码坏了"）。
	Note string `json:"note,omitempty"`

	// ---- routing 家族：会话状态与期望路由 ----
	// WantRoute 期望路由结果（ai/pending_human/human/fish/price）。
	WantRoute string `json:"want_route,omitempty"`
	// IntentScore 意向分（tVector[0]）。
	IntentScore float64 `json:"intent_score,omitempty"`
	// TrustLevel 信任度（tVector[6]）。
	TrustLevel float64 `json:"trust_level,omitempty"`
	// Attempts 已抛锚次数。
	Attempts int `json:"attempts,omitempty"`
	// HookRate 接钩率。
	HookRate float64 `json:"hook_rate,omitempty"`
	// HighIntentRounds 高意向持续轮数。
	HighIntentRounds int `json:"high_intent_rounds,omitempty"`
	// Emotion 情绪标签（positive/neutral/negative）。
	Emotion string `json:"emotion,omitempty"`

	// ---- keyword 家族：期望询价判定 ----
	// WantPriceInquiry 期望的询价判定结果（指针以区分"未设置"与 false）。
	WantPriceInquiry *bool `json:"want_price_inquiry,omitempty"`

	// ---- reply / safety 家族：话术样本与评分区间 ----
	// Reply 待评分的话术样本。
	Reply string `json:"reply,omitempty"`
	// EmptyReply 显式声明「本用例的回复就是空串」。
	// JSON 里空串与「忘写字段」无法区分，故空回复用例必须显式打标，
	// 否则会被 Validate 当成漏写字段拦下（或用例被静默跳过）。
	EmptyReply bool `json:"empty_reply,omitempty"`
	// Anchors 期望命中的需求锚词。
	Anchors []string `json:"anchors,omitempty"`
	// Forbidden 禁止出现的违规词。
	Forbidden []string `json:"forbidden,omitempty"`
	// MinScore 期望最低分（含）。
	MinScore float64 `json:"min_score,omitempty"`
	// MaxScore 期望最高分（含）。
	MaxScore float64 `json:"max_score,omitempty"`
}

// Load 读取内嵌黄金集（cases/*.json），按文件名排序后拼接，保证每次运行顺序稳定。
func Load() ([]Case, error) {
	return LoadFS(casesFS, "cases")
}

// LoadFS 从任意 fs.FS 的 dir 目录读取 *.json 黄金集（供测试注入自定义集）。
func LoadFS(fsys fs.FS, dir string) ([]Case, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("读取黄金集目录 %s 失败: %w", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	var all []Case
	for _, n := range names {
		raw, err := fs.ReadFile(fsys, filepath.ToSlash(filepath.Join(dir, n)))
		if err != nil {
			return nil, fmt.Errorf("读取黄金集文件 %s 失败: %w", n, err)
		}
		var batch []Case
		if err := json.Unmarshal(raw, &batch); err != nil {
			return nil, fmt.Errorf("解析黄金集文件 %s 失败: %w", n, err)
		}
		all = append(all, batch...)
	}
	return all, nil
}

// Validate 校验黄金集自身的完整性：ID 唯一、家族合法、各家族必需字段齐全。
// 单独成函数的原因：**黄金集写错 = 守卫静默失效**，必须能被门禁挡住。
func Validate(cases []Case) []string {
	var problems []string
	seen := map[string]bool{}
	for i, c := range cases {
		at := fmt.Sprintf("第 %d 条", i+1)
		if c.ID == "" {
			problems = append(problems, at+"缺少 id")
		} else if seen[c.ID] {
			problems = append(problems, "id 重复: "+c.ID)
		} else {
			seen[c.ID] = true
			at = c.ID
		}
		switch c.Family {
		case FamilyRouting:
			if c.Question == "" {
				problems = append(problems, at+"(routing) 缺少 question")
			}
			if c.WantRoute == "" {
				problems = append(problems, at+"(routing) 缺少 want_route")
			}
		case FamilyKeyword:
			if c.Question == "" {
				problems = append(problems, at+"(keyword) 缺少 question")
			}
			if c.WantPriceInquiry == nil {
				problems = append(problems, at+"(keyword) 缺少 want_price_inquiry")
			}
		case FamilyReply, FamilySafety:
			if strings.TrimSpace(c.Reply) == "" && !c.EmptyReply {
				problems = append(problems, at+"("+c.Family+") 缺少 reply（空回复用例请显式设置 empty_reply=true）")
			}
			if c.MaxScore == 0 && c.MinScore == 0 {
				problems = append(problems, at+"("+c.Family+") 未设置 min_score/max_score，断言为空")
			}
		default:
			problems = append(problems, at+" 未知 family: "+c.Family)
		}
	}
	return problems
}
