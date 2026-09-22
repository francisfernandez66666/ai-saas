// Command replayeval 是「AI 销售离线回放评测」独立作业（PLAN_FIX_2026-09-22 §6-E，批五 E）。
//
// 定位：eval_golden.sh 的确定性门禁（80 条冻结集）测的是路由/询价/评分函数回归，
// 它回答不了「新策略有没有让成交率变高」。本命令取历史客户会话上真实发出过 AI 回复的归因行
// （reply_attributions ⨝ messages），用**当前策略**重新生成回复，交 LLM 裁判**盲评**
// （回放 vs 历史真实回复，随机左右序去位置偏差），按**终局转化标签**
// （converted/lead/hooked/none 四桶）分别统计胜率。奖励口径红线：绝不使用
// eval_score/intent_after——那是策略自己的输出，拿它当目标是 reward hacking。
//
// 该作业烧真实 token，**不进每次提交门禁**：发布前或夜间 cron 手动触发。
// 安全默认：-dry-run=true（只选样、零 AI 调用），显式 -dry-run=false 才真跑；
// AI_MOCK_MODE=true 时真跑请求直接拒绝（mock 回复冒充不了真实评测）。
//
// 用法：
//
//	go run ./cmd/replayeval -tenant 7                  # dry-run 选样报告（零消耗）
//	go run ./cmd/replayeval -tenant 7 -dry-run=false   # 真回放+真裁判（烧 token）
//	go run ./cmd/replayeval -tenant 7 -limit 100 -days 14 -out /tmp/replay.md
//
// 退出码：0 正常完成（含 dry-run 与裁判不可用的整批 SKIP——环境未配齐不算策略失败）；
// 2 参数非法（缺租户/租户=0）；1 选样/写报告等运行错误。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"gorm.io/gorm"

	"ai-scrm/config"
	"ai-scrm/internal/ai"
	"ai-scrm/internal/cache"
	"ai-scrm/internal/db"
	"ai-scrm/internal/engine/strategy"
	"ai-scrm/internal/model"
	"ai-scrm/internal/runtimecfg"
)

// limitHardCap 回放条数硬上限：每条样本要烧「生成 1 次 + 裁判 1 次」两笔真实 token，
// 手滑传 10000 就是四位数美元事故，超界一律钳到 200 并显式告警。
const limitHardCap = 200

// poolFactor 候选池相对 limit 的放大倍数：分层采样要先有一坨候选才谈得上按桶配平，
// 6 倍足够让 converted 这类稀有桶也进池，同时把查询量钉死（上限 1200 行）。
const poolFactor = 6

// options 一次运行的全部决策输入（与 deps 分离，便于单测注入）。
type options struct {
	TenantID uint
	Limit    int
	Days     int
	Out      string
	DryRun   bool
	Seed     int64
}

// deps 外部能力槽位（选样/取触发消息/回放生成/裁判调用）。
// main 装配真实实现，单测注入假函数——runReplayEval 因此可全链路无 DB 无网络测试。
type deps struct {
	selectPool   func(tenantID uint, since time.Time, poolLimit int) ([]replaySample, error)
	fetchTrigger func(tenantID, conversationID, beforeMessageID uint) (string, error)
	generate     func(tenantID, customerID, conversationID uint, userInput string) (string, error)
	judge        func(tenantID uint, prompt string) (string, error)
}

// main 启动当前命令入口。
func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[replayeval] ")

	tenant := flag.Uint("tenant", 0, "租户 ID（必填，禁止 0/平台层——归因数据按租户隔离，平台层没有可回放的客户会话）")
	limit := flag.Int("limit", 50, fmt.Sprintf("回放条数（默认 50，硬上限 %d 防失控烧钱）", limitHardCap))
	days := flag.Int("days", 30, "取最近 N 天的归因样本（默认 30）")
	out := flag.String("out", "REPLAY_EVAL.md", "报告输出路径（空字符串=只打 stdout 不写文件；产物不入库）")
	// 安全默认必须是 true：漏写参数的一次误调用不该烧钱。真跑要显式 -dry-run=false。
	dryRun := flag.Bool("dry-run", true, "只选样不生成（默认 true，零 token 消耗）；显式 -dry-run=false 才真调 AI")
	seed := flag.Int64("seed", 1, "盲评左右随机序种子：同 seed+同样本必得同序，报告可复现可审计")
	flag.Parse()

	if *tenant == 0 {
		fmt.Fprintln(os.Stderr, "必须指定 -tenant（>0）：平台层(tenant_id=0)无客户会话可回放，禁止跑")
		os.Exit(2)
	}
	if *limit <= 0 {
		*limit = 50
	}
	if *limit > limitHardCap {
		log.Printf("limit=%d 超硬上限，钳制为 %d（防失控烧 token）", *limit, limitHardCap)
		*limit = limitHardCap
	}
	if *days <= 0 {
		*days = 30
	}

	cfg := boot()

	// mock 态闸：AI_MOCK_MODE=true 时生成走模板兜底、裁判走假链路——
	// 拿 mock 文本评出来的"胜率"是纯粹的数字安慰，拒绝并如实报告（退出码 0，非策略失败）。
	if !*dryRun && (cfg.AI.MockMode || runtimecfg.SafeCfgBool("mock_mode", false)) {
		fmt.Println("AI_MOCK_MODE=true（mock 态）：不会真调 AI，回放评测不产出有效结论。去掉 -dry-run=false 或关闭 mock 后重跑。")
		return
	}

	o := options{TenantID: *tenant, Limit: *limit, Days: *days, Out: *out, DryRun: *dryRun, Seed: *seed}
	rep, err := runReplayEval(o, realDeps(), time.Now())
	if err != nil {
		fmt.Fprintf(os.Stderr, "回放评测失败: %v\n", err)
		os.Exit(1)
	}

	for _, l := range rep.summaryLines() {
		fmt.Println(l)
	}
	if o.Out != "" {
		if err := os.WriteFile(o.Out, []byte(rep.markdown()), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "写报告失败 %s: %v\n", o.Out, err)
			os.Exit(1)
		}
		fmt.Printf("\n报告已写入 %s（本地产物，勿 git add）\n", o.Out)
	}
}

// boot 加载环境并初始化本作业需要的最小模块集（对齐 cmd/server/main.go 的依赖顺序，
// 只起读/生成链路用得到的：配置→DB→缓存→热配置→AI 客户端/路由→策略引擎）。
// 不跑 seed/消费者/HTTP——离线作业不改动服务状态。
func boot() *config.Config {
	if _, err := os.Stat(".env"); err == nil {
		_ = godotenv.Load(".env")
	}
	if config.GlobalConfig == nil {
		config.LoadConfig()
	}
	if db.DB == nil {
		if err := db.Init(); err != nil {
			log.Fatalf("初始化数据库失败: %v", err)
		}
	}
	// 标签/知识缓存：strategy.Infer 与 llm 生成链路依赖 DefaultTagCache/DefaultKnowledgeCache，
	// 未初始化会 nil 解引用（同 server 启动顺序 6→6.5）
	cache.InitTagCache()
	cache.InitKnowledgeCache()
	runtimecfg.InitSystemConfigService()
	ai.InitClient()
	ai.InitSiliconFlowClient()
	ai.InitDeepSeekClient()
	ai.InitRouter()
	strategy.InitEngine()
	return config.GlobalConfig
}

// realDeps 装配真实依赖：DB 选样 + strategy 生成 + strategy.GenerateEvals 裁判。
func realDeps() deps {
	return deps{
		selectPool:   selectPoolFromDB,
		fetchTrigger: fetchTriggerFromDB,
		generate:     generateViaStrategy,
		judge:        judgeViaStrategy,
	}
}

// runReplayEval 评测主流程（纯编排，外部能力全走 deps）。
// now 显式传参保证窗口计算可测。dry-run 在选样+分层后即返回，生成/裁判槽位一次都不碰。
func runReplayEval(o options, d deps, now time.Time) (*evalReport, error) {
	if d.selectPool == nil {
		return nil, errors.New("内部错误：selectPool 未装配")
	}
	poolLimit := o.Limit * poolFactor
	if poolLimit < o.Limit {
		poolLimit = o.Limit
	}
	if poolLimit > limitHardCap*poolFactor {
		poolLimit = limitHardCap * poolFactor
	}
	pool, err := d.selectPool(o.TenantID, now.AddDate(0, 0, -o.Days), poolLimit)
	if err != nil {
		return nil, fmt.Errorf("选样失败: %w", err)
	}
	dist := map[string]int{}
	for _, s := range pool {
		dist[s.Bucket]++
	}
	rep := newEvalReport(o, len(pool), dist)
	selected := stratifySamples(pool, o.Limit)

	if o.DryRun {
		for _, s := range selected {
			rep.recordSelected(s.Bucket)
		}
		rep.Notes = append(rep.Notes, "dry-run：仅完成选样与分层，生成/裁判调用 0 次，零 token 消耗")
		return rep, nil
	}
	if d.fetchTrigger == nil || d.generate == nil || d.judge == nil {
		return nil, errors.New("内部错误：非 dry-run 需要 fetchTrigger/generate/judge 全部装配")
	}

	for _, s := range selected {
		rep.recordSelected(s.Bucket)
		// 触发消息：原 AI 回复之前最近一条客户消息——回放的"题目"必须与历史同款，
		// 找不到（老数据被清理/直发型回复）就跳过该样本，不拿别的消息凑数污染口径
		trigger, err := d.fetchTrigger(o.TenantID, s.ConversationID, s.MessageID)
		if err != nil {
			rep.GenerateFailed++
			continue
		}
		if strings.TrimSpace(trigger) == "" {
			rep.SkippedNoInput++
			continue
		}
		// generate 契约：error=生成链路故障（计入失败）；空串=样本实体已缺失（计入跳过）
		replay, err := d.generate(o.TenantID, s.CustomerID, s.ConversationID, trigger)
		if err != nil {
			rep.GenerateFailed++
			continue
		}
		if strings.TrimSpace(replay) == "" {
			rep.SkippedNoInput++
			continue
		}
		side := pickReplaySide(o.Seed, s.AttributionID)
		raw, err := d.judge(o.TenantID, buildJudgePrompt(trigger, s.HistoryReply, replay, side))
		if err != nil {
			// 裁判失败只计数不炸批；全批零票时按 SKIP 语义退出 0（见 conclusion/notesFor）。
			// 错误详情刻意不进报告——上游错误文本可能携带 URL/参数，防泄露只报计数。
			rep.recordJudgeError(s.Bucket)
			continue
		}
		verdict, parsed := parseJudgeVerdict(raw, side)
		rep.recordVerdict(s.Bucket, verdict, parsed)
	}
	return rep, nil
}

// candidateRow 选样查询的行载体（显式列，避免嵌入模型结构在 Joins 下的列名歧义）。
type candidateRow struct {
	ID             uint
	MessageID      uint
	ConversationID uint
	CustomerID     uint
	CreatedAt      time.Time
	Hooked         bool
	LeadCaptured   bool
	ArrivedAt      *time.Time
	DealtAt        *time.Time
	HistoryReply   string
}

// selectPoolFromDB 从归因表取该租户近 N 天有 AI 回复原文的样本并分桶（只读）。
// join 条件把「归因行↔其记录的 AI 消息」钉死在同租户（历史上盖章缺陷会留 tenant_id=0 脏行，
// 双条件冗余但资金级谨慎不嫌多）；customer_id/conversation_id 为 0 的行不可回放，直接不进池。
func selectPoolFromDB(tenantID uint, since time.Time, poolLimit int) ([]replaySample, error) {
	var rows []candidateRow
	// G-12 口径：db.DB 链式首行即带显式 tenant_id 条件（A 类白名单），不新增白名单外裸用
	err := db.DB.Table("reply_attributions").Where("reply_attributions.tenant_id = ? AND m.tenant_id = ?", tenantID, tenantID).
		Select("reply_attributions.id, reply_attributions.message_id, reply_attributions.conversation_id, "+
			"reply_attributions.customer_id, reply_attributions.created_at, reply_attributions.hooked, "+
			"reply_attributions.lead_captured, reply_attributions.arrived_at, reply_attributions.dealt_at, "+
			"m.content AS history_reply").
		Joins("JOIN messages m ON m.id = reply_attributions.message_id").
		Where("m.sender_type = 'ai' AND m.content <> ''").
		Where("reply_attributions.customer_id > 0 AND reply_attributions.conversation_id > 0").
		Where("reply_attributions.created_at >= ?", since).
		Order("reply_attributions.created_at DESC, reply_attributions.id DESC").
		Limit(poolLimit).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]replaySample, 0, len(rows))
	for _, r := range rows {
		out = append(out, replaySample{
			AttributionID:  r.ID,
			MessageID:      r.MessageID,
			ConversationID: r.ConversationID,
			CustomerID:     r.CustomerID,
			CreatedAt:      r.CreatedAt,
			HistoryReply:   r.HistoryReply,
			Bucket: classifyOutcome(outcomeFlags{
				Hooked:       r.Hooked,
				LeadCaptured: r.LeadCaptured,
				Arrived:      r.ArrivedAt != nil,
				Dealt:        r.DealtAt != nil,
			}),
		})
	}
	return out, nil
}

// fetchTriggerFromDB 取原 AI 回复之前最近一条客户消息内容（回放的输入题目）。
// 找不到返回空串不报错——调用方按「缺题目」跳过该样本。
func fetchTriggerFromDB(tenantID, conversationID, beforeMessageID uint) (string, error) {
	// G-12 口径：首行带 tenant_id 条件（A 类白名单），只读 Pluck 不碰写路径
	var contents []string
	err := db.DB.Model(&model.Message{}).Where("tenant_id = ? AND conversation_id = ? AND sender_type = 'customer' AND id < ?", tenantID, conversationID, beforeMessageID).
		Order("id DESC").Limit(1).
		Pluck("content", &contents).Error
	if err != nil {
		return "", err
	}
	if len(contents) == 0 {
		return "", nil
	}
	return contents[0], nil
}

// generateViaStrategy 用当前策略重新生成回复。
// 接线证据（红线口径）：strategy.DefaultEngine.Infer（7 步推理，含批五 B 择臂层）→
// strategy.GenerateReply → llm.GenerateAIReply → ai.Router，即与线上完全同一条生成链路，
// 业务/作业层绝不直连 internal/llm。ctx 用 context.Background()——E3 要求生成链吃
// "只带 trace、不带请求取消"的 ctx；离线作业没有请求可取消，Background 即最简合规形态。
// DeptIDs 传 nil=纯租户语境（与 C 端客户对话一致，不归顾问部门链）。
func generateViaStrategy(tenantID, customerID, conversationID uint, userInput string) (string, error) {
	if strategy.DefaultEngine == nil {
		return "", errors.New("策略引擎未初始化")
	}
	var customer model.Customer
	if err := db.DB.Where("tenant_id = ? AND id = ?", tenantID, customerID).First(&customer).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", nil // 客户已删除：按「缺题目」同路跳过，不算生成失败
		}
		return "", err
	}
	var conv model.Conversation
	if err := db.DB.Where("tenant_id = ? AND id = ?", tenantID, conversationID).First(&conv).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", nil
		}
		return "", err
	}
	out := strategy.DefaultEngine.Infer(strategy.StrategyInput{
		TVector:        customer.BuildBaseTVector(), // 与 chat_main 同口径：基准向量，不吃叠加过的持久化值
		State:          conv.GetState(),
		CustomerInput:  userInput,
		CustomerTags:   customer.GetTags(),
		CustomerID:     customer.ID,
		ConversationID: conv.ID,
		CanPromote:     customer.CanPromote(),
		JourneyStage:   customer.JourneyStage,
		TenantID:       tenantID,
	})
	reply := strategy.GenerateReply(context.Background(), &customer, conv.ID, userInput, &out, nil)
	return strings.TrimSpace(reply), nil
}

// judgeViaStrategy 裁判调用：strategy.GenerateEvals → llm.GenerateEvalsText，
// 走 stage_models 的 evals 阶段模型（与数据飞轮素材评分同一注入点，零新增 AI 出口）。
// ai.Router 为 nil 或链路全失败 → 返回 error，上层记裁判失败；全批零票时整批 SKIP 退出 0。
func judgeViaStrategy(tenantID uint, prompt string) (string, error) {
	if ai.Router == nil {
		return "", errors.New("AI 路由未初始化，裁判模型不可用")
	}
	messages := []ai.ChatMessage{{Role: "user", Content: prompt}}
	reply, err := strategy.GenerateEvals(tenantID, messages, 0.1)
	if err != nil {
		return "", err
	}
	return reply, nil
}

// buildJudgePrompt 组装盲评提示词：回放回复按 replaySide 放到 A 或 B，
// 历史真实回复放另一侧——裁判不知道哪份是回放，消除"新旧"先验；
// 左右随机序由 pickReplaySide 完成，消除模型的位置偏好。
func buildJudgePrompt(trigger, historyReply, replayReply, replaySide string) string {
	sideA, sideB := historyReply, replayReply
	if replaySide == "A" {
		sideA, sideB = replayReply, historyReply
	}
	var b strings.Builder
	b.WriteString("你是 AI 销售话术评审。下面是同一条客户消息对应的两份候选回复（回复A、回复B）。\n")
	b.WriteString("请判断哪一份更可能推动客户走向到店/成交（终局转化），其次看口语自然度与需求针对性。\n")
	b.WriteString("只输出一个词：A、B 或 平局。不要输出任何其它内容。\n\n")
	fmt.Fprintf(&b, "客户消息：%s\n\n回复A：%s\n\n回复B：%s\n",
		trigger, strings.TrimSpace(sideA), strings.TrimSpace(sideB))
	return b.String()
}
