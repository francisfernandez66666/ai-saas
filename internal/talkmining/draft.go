// Package talkmining —— 本文件为出稿层：高转簇 → LLM 归纳 → templates 表草稿（status=2）+ 输入装载（DB 读）。
//
// 分工理由：mining.go 保持零 DB 可纯测，DB 读（LoadAdvisorRecords）与 DB 写
// （DraftTemplates）都收在这里，测试仅幂等层需要连库。
package talkmining

import (
	"fmt"
	"hash/fnv"
	"log"
	"strings"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/pii"
)

// GenerateDraftFunc 由 main.go 组合根注入：把簇样本交 LLM 归纳成一条可复用话术草稿。
// 签名 (tenantID, prompt) → (text, err)，与 service.EvalLLMFunc 同形态——
// 为什么必须走注入的函数变量而不是直连 internal/llm：
//   - 项目红线"业务层禁止直连 internal/llm"（调用方向 api → strategy → llm）；
//   - talkmining 若 import llm/strategy 会把离线作业包焊进在线依赖图，且 llm→service 成环链路上再叠一环。
//
// 未注入时作业**跳过并打日志，绝不 panic**（离线增强能力，缺它主链零影响，
// 同 EvalLLMFunc/kb_rerank 的 fail-open 惯例）。
var GenerateDraftFunc func(tenantID uint, prompt string) (string, error)

// DraftStat 一轮出稿的结果账目（供日志/接线方观测，不静默吞过程量）。
type DraftStat struct {
	Created             int // 新写入的草稿模板数
	SkippedInsufficient int // 因样本不足被拒的簇数（insufficient_samples 不得出稿）
	SkippedExisting     int // 因签名已存在被跳过的簇数（幂等：同簇不重复出稿）
	SkippedLLM          int // LLM 失败/空返回被跳过的簇数
}

// minedTemplateID 由 (租户, 分组键, 簇签名) 生成**确定性**模板 ID（≤50 字符）。
// 为什么用稳定哈希而不是随机 ID：幂等全靠它——同簇重跑必得同主键，
// templates.id 是字符串主键（天然语义化命名位），既不必加新列也不引唯一索引迁移，
// 撞主键即视为"该簇已出过稿"直接跳过。fnv32a 跨进程/跨版本确定（同 strategy.abHashBucket 口径）。
func minedTemplateID(tenantID uint, c Cluster) string {
	h := fnv.New32a()
	// fnv 的 Write 对内存 buffer 永不返回 error，忽略 errcheck 误报
	_, _ = fmt.Fprintf(h, "%d|%d|%d|%s", tenantID, c.Key.AdvisorID, c.Key.AnchorType, c.Key.Stage)
	keyHash := h.Sum32()
	return fmt.Sprintf("tplmin_%08x_%s", keyHash, c.Signature) // 7+8+1+8 = 24 字符
}

// BuildDraftPrompt 组装给 LLM 的归纳提示词（纯函数，可单测钉住"样本已脱敏/要求无 PII"两点）。
// 关键约束写进提示词：产出通用可复用句式（保留占位符位），不得复述样本中的具体数字/称谓。
func BuildDraftPrompt(c Cluster) string {
	anchorDesc := "锚位未知按阶段归并"
	if c.Key.AnchorType != Unanchored {
		anchorDesc = fmt.Sprintf("%s（类型%d）", model.AnchorTypeName[c.Key.AnchorType], c.Key.AnchorType)
	}
	return "你是销售话术提炼器。下面是金牌顾问在同一场景下转化率最高的一条代表性人工回复。\n" +
		fmt.Sprintf("场景：旅程阶段=%s，%s；统计：样本数=%d，目标(%s)转化率=%.0f%%。\n\n",
			c.Key.Stage, anchorDesc, c.Samples, c.TargetOutcome, c.ConvRate*100) +
		"代表性回复：\n" + c.Representative + "\n\n" +
		"请归纳成一条可复用的话术模板：保留句式结构与推进动作，去掉具体人名/号码/价格等个例信息，" +
		"个例位置改用 {{customer_name}}/{{model}}/{{price}} 类占位符。只输出模板正文，不要解释。"
}

// DraftTemplates 把排序靠前的簇喂给 LLM 归纳为模板草稿并写入 templates（status=2 草稿态）。
//
// 三条硬约束及理由：
//  1. Insufficient 簇一律拒出稿——样本不足组只是统计残影，出稿等于让噪声占用审核人力；
//  2. 只写 status=2（E4 草稿态，引擎 LoadData 只加载 status=1，草稿天然不进召回池）——
//     自动启用不可想象：这是从真人身上学来的打法，进生产话术池前人必须看一眼；
//  3. 幂等按 minedTemplateID（分组键+归一化代表文本的稳定哈希）：先查后插 + 撞主键视为已存在。
//
// maxDrafts<=0 表示不限量。GenerateDraftFunc 未注入时整轮跳过（打日志、返回零账目、不报错不 panic）。
func DraftTemplates(tenantID uint, clusters []Cluster, maxDrafts int) (DraftStat, error) {
	var stat DraftStat
	if tenantID == 0 {
		return stat, fmt.Errorf("talkmining: 草稿必须落租户归属（tenantID=0 会被盖章回调视为平台层，拒绝）")
	}
	if GenerateDraftFunc == nil {
		log.Printf("[talkmining] GenerateDraftFunc 未注入，跳过草稿归纳（%d 个候选簇不出稿）", len(clusters))
		return stat, nil
	}
	for _, c := range clusters {
		if c.Insufficient {
			stat.SkippedInsufficient++
			continue
		}
		if maxDrafts > 0 && stat.Created >= maxDrafts {
			break
		}
		tmplID := minedTemplateID(tenantID, c)
		var cnt int64
		// 后台作业无请求 ctx（盖章回调拿不到租户），故裸 db.DB + 显式 tenant_id 条件 + 显式设 TenantID（C7 红线口径）
		if err := db.DB.Model(&model.Template{}).Where("id = ? AND tenant_id = ?", tmplID, tenantID).Count(&cnt).Error; err != nil { // g12:platform
			return stat, fmt.Errorf("talkmining: 查重模板 %s: %w", tmplID, err)
		}
		if cnt > 0 {
			stat.SkippedExisting++
			continue
		}
		out, err := GenerateDraftFunc(tenantID, BuildDraftPrompt(c))
		if err != nil {
			log.Printf("[talkmining] LLM 归纳失败 advisor=%d anchor=%d stage=%s: %v", c.Key.AdvisorID, c.Key.AnchorType, c.Key.Stage, err)
			stat.SkippedLLM++
			continue
		}
		out = strings.TrimSpace(out)
		if out == "" {
			stat.SkippedLLM++
			continue
		}
		// 纵深防御：提示词已要求去 PII，但 LLM 可能照抄样本号码——产物入库前再过一遍掩码
		out = pii.MaskPhoneInText(out)
		anchor := c.Key.AnchorType
		if anchor == Unanchored {
			anchor = 0 // 模板表锚类型合法域 0-6；Unanchored 簇的语境已体现在 Category/Name 的阶段维度里
		}
		tpl := model.Template{
			ID:             tmplID,
			TenantID:       tenantID,
			AnchorType:     anchor,
			SubType:        "talk_mining",
			Name:           fmt.Sprintf("话术挖掘·顾问%d·%s·样本%d·转化%.0f%%", c.Key.AdvisorID, c.Key.Stage, c.Samples, c.ConvRate*100),
			Category:       "talk_mining",
			PromptTemplate: out,
			Priority:       0,
			Status:         2, // 草稿态：人工经既有 enable 路由启用后才进召回池
			Version:        "mined-v1",
		}
		if err := db.DB.Create(&tpl).Error; err != nil { // g12:platform
			// 撞主键（并发重跑/多实例同 tick）视为该簇已出稿，静默跳过维持幂等
			if strings.Contains(err.Error(), "23505") || strings.Contains(err.Error(), "duplicate key") {
				stat.SkippedExisting++
				continue
			}
			return stat, fmt.Errorf("talkmining: 写入草稿模板 %s: %w", tmplID, err)
		}
		stat.Created++
	}
	return stat, nil
}

// LoadAdvisorRecords 从 messages/conversations/customers 读窗口内的顾问人工回复，组装挖掘输入。
//
// 口径说明（两处诚实近似，勿当精确归因用）：
//   - sender_type='human' 且 sender_id>0 才算顾问消息（客户/AI/system 一律不进候选池，隐私红线）；
//   - 终局结果用客户**当前** journey_stage 近似（批五 A 的 arrive/deal 时间窗回填尚未接线；
//     窗口内未推进一步的客户会被低估转化——低估只是少出稿，不会错出稿，安全侧）。
//
// limit<=0 时不限量（调用方按租户规模自钳）。
func LoadAdvisorRecords(tenantID uint, since, until time.Time, limit int) ([]Record, error) {
	type row struct {
		AdvisorID  uint   `gorm:"column:advisor_id"`
		AnchorType int    `gorm:"column:anchor_type"`
		CustomerID uint   `gorm:"column:customer_id"`
		Content    string `gorm:"column:content"`
		Stage      string `gorm:"column:journey_stage"`
	}
	var rows []row
	q := db.DB.Table("messages AS m"). // g12:platform
						Select("m.sender_id AS advisor_id, m.anchor_type, m.customer_id, m.content, c.journey_stage").
						Joins("JOIN customers c ON c.id = m.customer_id AND c.tenant_id = m.tenant_id").
						Where("m.tenant_id = ? AND m.sender_type = 'human' AND m.sender_id > 0 AND m.content <> '' AND m.created_at >= ? AND m.created_at < ?", tenantID, since, until)
	if limit > 0 {
		q = q.Limit(limit)
	}
	if err := q.Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("talkmining: 读取顾问消息失败: %w", err)
	}
	records := make([]Record, 0, len(rows))
	for _, r := range rows {
		anchor := r.AnchorType
		if anchor == 0 {
			// 人工消息的 anchor_type 列几乎恒为默认 0（该列是 AI 链路写的），0 又与合法锚"不抛"撞值——
			// 无法区分即退化为"锚位未知"，按阶段归并（Unanchored），宁可粗分组不要假分组。
			anchor = Unanchored
		}
		records = append(records, Record{
			AdvisorID:  r.AdvisorID,
			AnchorType: anchor,
			Stage:      r.Stage,
			CustomerID: r.CustomerID,
			Content:    r.Content,
			Outcome:    OutcomeFromStage(r.Stage),
		})
	}
	return records, nil
}
