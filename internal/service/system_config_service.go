package service

import (
	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"encoding/json"
	"log"
	"math/rand"
	"strconv"
	"sync"
)

// ============================================================
// 系统配置服务层 - CRUD + 内存热加载
// 核心设计：
//   1. 服务启动时从DB加载所有配置到内存（sync.RWMutex保护）
//   2. DB为空时用默认值写入DB
//   3. Admin API 更新DB后，自动重新加载内存缓存
//   4. 所有代码读配置走本服务的GetXXX方法，不直接读config.GlobalConfig
// 为什么要有内存缓存？避免每次读配置都查DB，提升性能
// ============================================================

// SystemConfigService 系统配置服务
type SystemConfigService struct {
	mu          sync.RWMutex               // 读写锁，保护内存缓存
	cache       map[string]string          // key→value（仅 tenant_id=0 系统默认，引擎/全局语义）
	tenantCache map[uint]map[string]string // 租户覆盖层：tid→key→value（P2 租户化）
	configs     []model.SystemConfig       // 完整配置列表（保留分类、描述等元信息）
}

// DefaultSystemConfigService 默认系统配置服务实例（全局单例）
var DefaultSystemConfigService *SystemConfigService

// ============================================================
// 默认配置定义
// 当DB中无数据时，使用这些默认值初始化
// 按四大分类组织，sort_order决定同分类内的显示顺序
// ============================================================

// DefaultConfigs 默认配置列表（导出，供API层强制初始化时引用）
// 注意：每项的 Value 和 DefaultValue 都是 JSON 字符串格式
var DefaultConfigs = []model.SystemConfig{
	// ---- 分类1：reply_speed（回复速度类）----
	{Category: "reply_speed", Key: "reply_min_delay", Value: "15", ValueType: "number", Description: "AI回复最低延迟(秒)-红线", DefaultValue: "15", SortOrder: 1},
	{Category: "reply_speed", Key: "l3_simple_delay", Value: "[3,8]", ValueType: "json", Description: "L3简单问题延迟区间", DefaultValue: "[3,8]", SortOrder: 2},
	{Category: "reply_speed", Key: "l2_simple_delay", Value: "[5,15]", ValueType: "json", Description: "L2简单问题延迟区间", DefaultValue: "[5,15]", SortOrder: 3},
	{Category: "reply_speed", Key: "l1_simple_delay", Value: "[5,15]", ValueType: "json", Description: "L1简单问题延迟区间", DefaultValue: "[5,15]", SortOrder: 4},
	{Category: "reply_speed", Key: "l1_complex_delay", Value: "[60,120]", ValueType: "json", Description: "L1复杂问题延迟区间", DefaultValue: "[60,120]", SortOrder: 5},
	{Category: "reply_speed", Key: "merge_window_seconds", Value: "25", ValueType: "number", Description: "消息合并窗口(秒)", DefaultValue: "25", SortOrder: 6},
	{Category: "reply_speed", Key: "max_merge_messages", Value: "3", ValueType: "number", Description: "最大合并条数", DefaultValue: "3", SortOrder: 7},
	{Category: "reply_speed", Key: "off_work_multiplier", Value: "1.0", ValueType: "number", Description: "下班后延迟系数", DefaultValue: "1.0", SortOrder: 8},
	{Category: "reply_speed", Key: "weekend_multiplier", Value: "1.5", ValueType: "number", Description: "周末延迟系数", DefaultValue: "1.5", SortOrder: 9},
	{Category: "reply_speed", Key: "max_reply_delay", Value: "75", ValueType: "number", Description: "AI回复最大延迟(秒)-硬顶(总回复不超2分钟)", DefaultValue: "75", SortOrder: 10},
	// 修复：以下参数原硬编码在代码中，现改为后台可调，无需发版即可调节
	{Category: "reply_speed", Key: "simple_msg_delay", Value: "8", ValueType: "number", Description: "简单消息延迟(秒)-固定值", DefaultValue: "8", SortOrder: 11},
	{Category: "reply_speed", Key: "store_visit_first_delay", Value: "[10,15]", ValueType: "json", Description: "到店倾向第一段延迟区间(秒)[min,max]", DefaultValue: "[10,15]", SortOrder: 12},
	{Category: "reply_speed", Key: "store_visit_second_delay", Value: "[25,45]", ValueType: "json", Description: "到店倾向第二段延迟区间(秒)[min,max]", DefaultValue: "[25,45]", SortOrder: 13},
	{Category: "reply_speed", Key: "processing_lock_timeout", Value: "90", ValueType: "number", Description: "processing锁超时(秒)-防卡死", DefaultValue: "90", SortOrder: 14},
	{Category: "reply_speed", Key: "offline_offset_work_simple", Value: "30", ValueType: "number", Description: "线下偏移-工作/简单(秒)", DefaultValue: "30", SortOrder: 15},
	{Category: "reply_speed", Key: "offline_offset_work_medium", Value: "60", ValueType: "number", Description: "线下偏移-工作/中等(秒)", DefaultValue: "60", SortOrder: 16},
	{Category: "reply_speed", Key: "offline_offset_work_complex", Value: "90", ValueType: "number", Description: "线下偏移-工作/复杂(秒)", DefaultValue: "90", SortOrder: 17},
	{Category: "reply_speed", Key: "offline_offset_offwork_simple", Value: "60", ValueType: "number", Description: "线下偏移-非工作/简单(秒)", DefaultValue: "60", SortOrder: 18},
	{Category: "reply_speed", Key: "offline_offset_offwork_medium", Value: "120", ValueType: "number", Description: "线下偏移-非工作/中等(秒)", DefaultValue: "120", SortOrder: 19},
	{Category: "reply_speed", Key: "offline_offset_offwork_complex", Value: "180", ValueType: "number", Description: "线下偏移-非工作/复杂(秒)", DefaultValue: "180", SortOrder: 20},

	// ---- 分类2：strategy（策略引擎类）----
	{Category: "strategy", Key: "tau", Value: "0.8", ValueType: "number", Description: "softmax温度参数(越小越锐利)", DefaultValue: "0.8", SortOrder: 1},
	{Category: "strategy", Key: "soft_downgrade_threshold", Value: "0.15", ValueType: "number", Description: "软降级置信度阈值", DefaultValue: "0.15", SortOrder: 2},
	{Category: "strategy", Key: "stage_anchor_ceiling", Value: "[1,2,3,4,6,6]", ValueType: "json", Description: "阶段锁上限(Stage0-5)", DefaultValue: "[1,2,3,4,6,6]", SortOrder: 3},
	{Category: "strategy", Key: "first_round_nothrow_bonus", Value: "5.0", ValueType: "number", Description: "首轮不抛锚加分", DefaultValue: "5.0", SortOrder: 4},
	{Category: "strategy", Key: "first_round_samekind_bonus", Value: "0.0", ValueType: "number", Description: "首轮同类/场景锚加分", DefaultValue: "0.0", SortOrder: 5},
	{Category: "strategy", Key: "first_round_compare_penalty", Value: "-3.0", ValueType: "number", Description: "首轮对比锚及以上减分", DefaultValue: "-3.0", SortOrder: 6},
	{Category: "strategy", Key: "sim_thresh", Value: "0.55", ValueType: "number", Description: "话术模板相似度阈值", DefaultValue: "0.55", SortOrder: 7},
	{Category: "strategy", Key: "theta_hookrate_low", Value: "0.4", ValueType: "number", Description: "低接钩率阈值(触发软降级)", DefaultValue: "0.4", SortOrder: 8},
	{Category: "strategy", Key: "theta_silent", Value: "60", ValueType: "number", Description: "沉默时长阈值(秒,触发软降级)", DefaultValue: "60", SortOrder: 9},
	{Category: "strategy", Key: "theta_trust", Value: "0.3", ValueType: "number", Description: "信任度阈值(低于转人工)", DefaultValue: "0.3", SortOrder: 10},
	{Category: "strategy", Key: "theta_rounds", Value: "3", ValueType: "number", Description: "多轮判断阈值", DefaultValue: "3", SortOrder: 11},
	{Category: "strategy", Key: "theta_hook_rate_crit", Value: "0.2", ValueType: "number", Description: "接钩率危急阈值(低于转人工)", DefaultValue: "0.2", SortOrder: 12},
	{Category: "strategy", Key: "theta_l3_intent", Value: "0.8", ValueType: "number", Description: "L3高意向分阈值", DefaultValue: "0.8", SortOrder: 13},
	{Category: "strategy", Key: "theta_l3_rounds", Value: "2", ValueType: "number", Description: "L3高意向持续轮数", DefaultValue: "2", SortOrder: 14},
	{Category: "strategy", Key: "theta_urgency_l1", Value: "0.5", ValueType: "number", Description: "紧迫等级L1阈值", DefaultValue: "0.5", SortOrder: 15},
	{Category: "strategy", Key: "theta_urgency_l2", Value: "0.7", ValueType: "number", Description: "紧迫等级L2阈值", DefaultValue: "0.7", SortOrder: 16},
	{Category: "strategy", Key: "human_timeout_seconds", Value: "180", ValueType: "number", Description: "人工超时接管(秒)", DefaultValue: "180", SortOrder: 17},
	// 修复：锚权重从硬编码→后台可调，7组权重对应7种锚类型
	// 后台改权重→热加载→下次推理立即生效，不需要改代码发版
	{Category: "strategy", Key: "anchor_weights", Value: `[{"intent_score_weight":-2.0,"trust_weight":-1.5,"hook_rate_weight":-1.0,"stage_weight":-1.0,"price_sens_weight":0.0,"base_bias":0.5},{"intent_score_weight":0.5,"trust_weight":0.3,"hook_rate_weight":0.2,"stage_weight":0.3,"price_sens_weight":0.0,"base_bias":1.0},{"intent_score_weight":1.0,"trust_weight":0.5,"hook_rate_weight":0.3,"stage_weight":0.8,"price_sens_weight":0.0,"base_bias":0.5},{"intent_score_weight":1.5,"trust_weight":0.8,"hook_rate_weight":0.5,"stage_weight":1.2,"price_sens_weight":0.5,"base_bias":0.3},{"intent_score_weight":1.8,"trust_weight":1.0,"hook_rate_weight":0.6,"stage_weight":1.5,"price_sens_weight":0.8,"base_bias":0.0},{"intent_score_weight":2.0,"trust_weight":1.2,"hook_rate_weight":0.8,"stage_weight":1.8,"price_sens_weight":0.3,"base_bias":-0.2},{"intent_score_weight":2.2,"trust_weight":1.5,"hook_rate_weight":1.0,"stage_weight":2.0,"price_sens_weight":0.5,"base_bias":-0.5}]`, ValueType: "json", Description: "7种锚权重(不抛/同类/拆解/对比/损失/稀缺/代价自担)", DefaultValue: `[{"intent_score_weight":-2.0,"trust_weight":-1.5,"hook_rate_weight":-1.0,"stage_weight":-1.0,"price_sens_weight":0.0,"base_bias":0.5},{"intent_score_weight":0.5,"trust_weight":0.3,"hook_rate_weight":0.2,"stage_weight":0.3,"price_sens_weight":0.0,"base_bias":1.0},{"intent_score_weight":1.0,"trust_weight":0.5,"hook_rate_weight":0.3,"stage_weight":0.8,"price_sens_weight":0.0,"base_bias":0.5},{"intent_score_weight":1.5,"trust_weight":0.8,"hook_rate_weight":0.5,"stage_weight":1.2,"price_sens_weight":0.5,"base_bias":0.3},{"intent_score_weight":1.8,"trust_weight":1.0,"hook_rate_weight":0.6,"stage_weight":1.5,"price_sens_weight":0.8,"base_bias":0.0},{"intent_score_weight":2.0,"trust_weight":1.2,"hook_rate_weight":0.8,"stage_weight":1.8,"price_sens_weight":0.3,"base_bias":-0.2},{"intent_score_weight":2.2,"trust_weight":1.5,"hook_rate_weight":1.0,"stage_weight":2.0,"price_sens_weight":0.5,"base_bias":-0.5}]`, SortOrder: 18},

	// ---- 分类3：mental_stage（心智阶段类）----
	{Category: "mental_stage", Key: "stage_step_enabled", Value: "true", ValueType: "bool", Description: "逐级递进开关", DefaultValue: "true", SortOrder: 1},
	{Category: "mental_stage", Key: "stage_max_increment", Value: "1", ValueType: "number", Description: "每轮最大阶段增量", DefaultValue: "1", SortOrder: 2},
	{Category: "mental_stage", Key: "hook_rate_stage1_threshold", Value: "0.3", ValueType: "number", Description: "HookRate<此值卡在Stage1", DefaultValue: "0.3", SortOrder: 3},
	{Category: "mental_stage", Key: "force_stage0_attempts", Value: "1", ValueType: "number", Description: "Attempts≤此值强制stage=0", DefaultValue: "1", SortOrder: 4},

	// ---- 分类4：ai_chain（AI链路类）----
	{Category: "ai_chain", Key: "model_priority", Value: `["siliconflow_deepseek_v4_flash","siliconflow_glm4_9b","zhipu_glm4_flash","template_fallback"]`, ValueType: "json", Description: "模型降级优先级(可拖拽排序)", DefaultValue: `["siliconflow_deepseek_v4_flash","siliconflow_glm4_9b","zhipu_glm4_flash","template_fallback"]`, SortOrder: 1},
	{Category: "ai_chain", Key: "mock_mode", Value: "false", ValueType: "bool", Description: "Mock模式开关(仅返回模板回复)", DefaultValue: "false", SortOrder: 2},
	{Category: "ai_chain", Key: "ai_temperature", Value: "0.7", ValueType: "number", Description: "AI采样温度(0-1,越大越随机)", DefaultValue: "0.7", SortOrder: 3},
	{Category: "ai_chain", Key: "ai_max_tokens", Value: "1024", ValueType: "number", Description: "AI最大输出token数", DefaultValue: "1024", SortOrder: 4},
	// 修复：语气风格后台可调，无需改代码发版
	// 可选值：neutral(冷静专业) / warm(略带热情) / enthusiastic(热情主动)
	// 影响：人设描述、口语词/情绪词列表、倾听模式字数限制
	{Category: "ai_chain", Key: "tone_style", Value: "warm", ValueType: "string", Description: "语气风格(neutral冷静/warm略热情/enthusiastic热情)", DefaultValue: "warm", SortOrder: 5},
	// 修复问题4：引导式反问最大轮数后台可调，达到后关闭反问专注解答+适当介绍ROX品牌
	{Category: "strategy", Key: "guided_dialog_max_rounds", Value: "5", ValueType: "number", Description: "引导式反问最大轮数(达到后关闭反问)", DefaultValue: "5", SortOrder: 19},
	// 修复问题4：重复问题次数阈值后台可调，达到后关闭反问直接走解决陈述
	{Category: "strategy", Key: "repeat_question_max_times", Value: "3", ValueType: "number", Description: "重复问题次数阈值(达到后关闭反问)", DefaultValue: "3", SortOrder: 20},
	// 修复问题6：非车话题重复次数阈值后台可调，达到后切换语气
	{Category: "strategy", Key: "offtopic_repeat_max_times", Value: "3", ValueType: "number", Description: "非车话题重复次数阈值(达到后语气切换)", DefaultValue: "3", SortOrder: 21},
	// 修复问题5：知识库盲点兜底开关，关闭引导式提问改用查一下话术
	{Category: "ai_chain", Key: "knowledge_blindspot_fallback_enabled", Value: "true", ValueType: "bool", Description: "知识库盲点兜底开关(true=盲点时用查一下话术)", DefaultValue: "true", SortOrder: 6},
	// 修复问题7：注入AI的对话历史轮数，0=关闭(改用核心摘要注入)
	{Category: "ai_chain", Key: "chat_history_rounds", Value: "0", ValueType: "number", Description: "注入AI的对话历史轮数(0=关闭改用核心摘要)", DefaultValue: "0", SortOrder: 7},
	// 修复问题2：回复延迟模式，instant=秒回无延迟，normal=正常模拟真人延迟
	// 用途：测试/演示场景可切到instant秒回，正式环境用normal
	{Category: "reply_speed", Key: "reply_delay_mode", Value: "normal", ValueType: "string", Description: "回复延迟模式：normal=正常延迟，instant=秒回无延迟", DefaultValue: "normal", SortOrder: 99},

	// M3 分阶段模型覆盖：便宜模型跑意图识别、强模型跑话术生成；空=走全局降级链
	{Category: "ai_chain", Key: "stage_models", Value: "{}", ValueType: "json", Description: "分阶段模型覆盖(reply/intent/strategy各选provider+model,留空走降级链)", DefaultValue: "{}", SortOrder: 8},
	// ---- 分类5：human_takeover（人工接管类）----
	{Category: "human_takeover", Key: "assigned_lead_ai_auto_reply", Value: "true", ValueType: "bool", Description: "已分配线索AI自动回复开关(true=顾问超时未回时AI自动回复)", DefaultValue: "true", SortOrder: 1},
	{Category: "human_takeover", Key: "assigned_lead_ai_timeout", Value: "300", ValueType: "number", Description: "已分配线索顾问超时时间(秒)，超时后AI自动回复", DefaultValue: "300", SortOrder: 2},

	// ---- 分类6：billing（商业化类，2026-08-23 M1/M2/M5）----
	// pay_mode 三态：mock=测试模拟到账（默认，跑通全链路）/ static_qr=静态码+人工确认 / sdk=商户号到位后切换
	{Category: "billing", Key: "pay_mode", Value: "\"mock\"", ValueType: "string", Description: "收款模式(mock模拟到账/static_qr静态码人工确认)", DefaultValue: "\"mock\"", SortOrder: 1},
	{Category: "billing", Key: "static_qr_image", Value: "\"\"", ValueType: "string", Description: "静态收款码(URL或base64，static_qr模式下单返回给租户)", DefaultValue: "\"\"", SortOrder: 2},
	// 灰度开关（借翻译助手决策"默认不强制只留痕"）：false=CheckAIQuota 恒放行只记用量（上线初期防误伤）
	{Category: "billing", Key: "billing_enforced", Value: "false", ValueType: "bool", Description: "计费强制开关(false=超额不停服仅记日志告警)", DefaultValue: "false", SortOrder: 3},
	// 注册试用包额度：新租户注册自动发放 free 包时的 AI 调用次数
	{Category: "billing", Key: "trial_ai_calls", Value: "500", ValueType: "number", Description: "注册试用包AI调用次数(次)", DefaultValue: "500", SortOrder: 4},

	// ---- 分类7：notify（触达通道类，批次一顺手做：企微群机器人 + 重置码通道）----
	{Category: "notify", Key: "wecom_webhook_url", Value: "\"\"", ValueType: "string", Description: "企微群机器人webhook(敏感配置勿外泄；留资/人工确认订单推送)", DefaultValue: "\"\"", SortOrder: 1},
	{Category: "notify", Key: "reset_code_channel", Value: "\"log\"", ValueType: "string", Description: "重置码发送通道(log=打日志需校验手机号/smtp=邮件直发)", DefaultValue: "\"log\"", SortOrder: 2},

	// ---- 防薅：Turnstile 人机验证（批次三，2026-08-23 代码就绪）----
	// 挂 C 端免登录接口 /chat/guest · /chat/test；enabled=false 或 secret 为空时完全关闭零开销
	// 前端从 GET /api/v1/turnstile/sitekey 拿站点键渲染组件，验证令牌走 X-Turnstile-Token 头
	{Category: "billing", Key: "billing_markup_multiplier", Value: "1.5", ValueType: "number", Description: "Token成本均摊系数(对外成本口径=真实token×系数)", DefaultValue: "1.5", SortOrder: 5},
	{Category: "billing", Key: "price_micro_per_ktok_zhipu", Value: "15000", ValueType: "number", Description: "智谱单价(微元/千token,成本核算用)", DefaultValue: "15000", SortOrder: 6},
	{Category: "billing", Key: "price_micro_per_ktok_siliconflow", Value: "8000", ValueType: "number", Description: "硅基流动单价(微元/千token,成本核算用)", DefaultValue: "8000", SortOrder: 7},
	{Category: "notify", Key: "dingtalk_webhook_url", Value: "\"\"", ValueType: "string", Description: "钉钉群机器人webhook(双通道触达,与企微同时投递)", DefaultValue: "\"\"", SortOrder: 6},
	{Category: "notify", Key: "turnstile_enabled", Value: "false", ValueType: "bool", Description: "Turnstile人机验证开关(挂C端guest/test防刷)", DefaultValue: "false", SortOrder: 3},
	{Category: "notify", Key: "turnstile_site_key", Value: "\"\"", ValueType: "string", Description: "Turnstile站点键(前端渲染用,可公开)", DefaultValue: "\"\"", SortOrder: 4},
	{Category: "notify", Key: "turnstile_secret_key", Value: "\"\"", ValueType: "string", Description: "Turnstile密钥(服务端siteverify用,敏感勿外泄)", DefaultValue: "\"\"", SortOrder: 5},

	// ---- 防薅第二层：注册护栏（借鉴翻译助手三期§3.1，2026-08-24）----
	// signup 即送真实 AI 调用额度=烧钱洞；三键组合：IP限流 + 审核开关（超管 grant-trial 放行）
	{Category: "notify", Key: "register_ip_daily_limit", Value: "3", ValueType: "number", Description: "同IP每日注册租户上限(0=不限)", DefaultValue: "3", SortOrder: 6},
	{Category: "notify", Key: "register_ip_min_interval_sec", Value: "60", ValueType: "number", Description: "同IP两次注册最小间隔秒(0=不限)", DefaultValue: "60", SortOrder: 7},
	{Category: "notify", Key: "registration_review", Value: "false", ValueType: "bool", Description: "注册审核开关(true=新租户待审核不发试用包,超管grant-trial放行)", DefaultValue: "false", SortOrder: 8},
}

// InitSystemConfigService 初始化系统配置服务
// 启动时调用：1.确保DB有默认数据 2.加载到内存缓存
func InitSystemConfigService() {
	DefaultSystemConfigService = &SystemConfigService{
		cache:       make(map[string]string),
		tenantCache: make(map[uint]map[string]string),
		configs:     make([]model.SystemConfig, 0),
	}

	// 确保DB中有默认数据
	DefaultSystemConfigService.ensureDefaults()

	// 从DB加载到内存
	DefaultSystemConfigService.Reload()

	log.Println("[系统配置] 服务初始化完成，已加载配置项到内存")
}

// ensureDefaults 确保DB中有默认配置数据
// 如果DB为空，用 DefaultConfigs 写入初始数据
// 设计为幂等：已有数据不覆盖，缺失的补上
// 修复：用事务+强制覆盖写入，确保配置一定存在（解决用户反复反馈"后台没有配置项"的问题）
func (s *SystemConfigService) ensureDefaults() {
	inserted := 0
	for _, cfg := range DefaultConfigs {
		// 按 key 查是否已存在
		// 修复（2026-08-23）：必须限定 tenant_id=0（系统层）——只按 key 判断会因
		// 租户覆盖行存在而误跳过，导致系统默认层缺失、全局读值静默回落代码默认
		var existing model.SystemConfig
		result := db.DB.Where("tenant_id = 0 AND \"key\" = ?", cfg.Key).First(&existing)
		if result.Error != nil {
			// 不存在，插入默认值
			if err := db.DB.Create(&cfg).Error; err != nil {
				log.Printf("[系统配置] 插入默认配置失败 key=%s: %v", cfg.Key, err)
			} else {
				inserted++
			}
		}
	}

	// 修复：插入后校验总量，防止DB有脏状态导致0条数据
	var totalCount int64
	db.DB.Model(&model.SystemConfig{}).Count(&totalCount)
	if totalCount == 0 && len(DefaultConfigs) > 0 {
		log.Println("[系统配置] ⚠️ DB中0条配置数据，强制批量插入默认配置")
		// 用事务确保原子性
		tx := db.DB.Begin()
		if err := tx.Create(&DefaultConfigs).Error; err != nil {
			tx.Rollback()
			log.Printf("[系统配置] 批量插入默认配置失败: %v", err)
		} else {
			tx.Commit()
			log.Printf("[系统配置] 已强制插入 %d 条默认配置", len(DefaultConfigs))
		}
	} else if inserted > 0 {
		log.Printf("[系统配置] 已补充插入 %d 条缺失配置", inserted)
	} else {
		log.Printf("[系统配置] DB中已有 %d 条配置，无需补充", totalCount)
	}
}

// ForceResetDefaults 强制重置所有配置为默认值
// 用于后台/config/init接口，删除旧数据后重新写入
// 确保配置数据一定存在，解决"后台什么参数都没有"的问题
func (s *SystemConfigService) ForceResetDefaults() error {
	tx := db.DB.Begin()

	// 1. 删除所有旧配置
	if err := tx.Where("1 = 1").Delete(&model.SystemConfig{}).Error; err != nil {
		tx.Rollback()
		return err
	}

	// 2. 批量插入默认配置
	if err := tx.Create(&DefaultConfigs).Error; err != nil {
		tx.Rollback()
		return err
	}

	tx.Commit()

	// 3. 重新加载内存缓存
	s.Reload()

	log.Printf("[系统配置] 强制重置完成，已写入 %d 条默认配置", len(DefaultConfigs))
	return nil
}

// Reload 从DB重新加载所有配置到内存缓存
// 每次Admin API更新配置后调用，实现热加载
func (s *SystemConfigService) Reload() {
	s.mu.Lock()
	defer s.mu.Unlock()

	var configs []model.SystemConfig
	// 按分类和排序读取所有配置
	if err := db.DB.Order("category ASC, sort_order ASC").Find(&configs).Error; err != nil {
		log.Printf("[系统配置] 加载配置失败: %v", err)
		return
	}

	// 重建内存缓存：系统默认(0)与租户覆盖分层
	newCache := make(map[string]string)
	newTenantCache := make(map[uint]map[string]string)
	for _, cfg := range configs {
		if cfg.TenantID == 0 {
			newCache[cfg.Key] = cfg.Value
		} else {
			if newTenantCache[cfg.TenantID] == nil {
				newTenantCache[cfg.TenantID] = make(map[string]string)
			}
			newTenantCache[cfg.TenantID][cfg.Key] = cfg.Value
		}
	}

	s.cache = newCache
	s.tenantCache = newTenantCache
	s.configs = configs

	log.Printf("[系统配置] 已加载 %d 项配置到内存（含 %d 个租户覆盖层）",
		len(configs), len(newTenantCache))
}

// ============================================================
// 查询方法 - 从内存缓存读取，无需查DB
// ============================================================

// GetAll 获取所有配置（含元信息）
// 返回完整的配置列表，用于Admin API返回
func (s *SystemConfigService) GetAll() []model.SystemConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// 返回副本，避免外部修改影响缓存
	result := make([]model.SystemConfig, len(s.configs))
	copy(result, s.configs)
	return result
}

// GetByCategory 按分类获取配置列表
// category: reply_speed / strategy / mental_stage / ai_chain
func (s *SystemConfigService) GetByCategory(category string) []model.SystemConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []model.SystemConfig
	for _, cfg := range s.configs {
		if cfg.Category == category {
			result = append(result, cfg)
		}
	}
	return result
}

// GetByKey 按key获取单个配置
// 返回配置对象指针，未找到返回nil
func (s *SystemConfigService) GetByKey(key string) *model.SystemConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for i := range s.configs {
		if s.configs[i].Key == key {
			// 返回副本
			cfg := s.configs[i]
			return &cfg
		}
	}
	return nil
}

// ============================================================
// 便捷取值方法 - 按类型直接返回解析后的值
// 所有代码读配置应走这些方法，不直接读config.GlobalConfig
// ============================================================

// GetFloat 获取float64类型的配置值
// key: 配置键名，如 "tau"
// defaultValue: key不存在时的兜底值
// ============================================================
// 租户级配置（P2）：租户覆盖优先，回退系统默认(0)
// 语义：GetInt 等老方法 = 全局系统默认（引擎/平台层用）
//       GetXxxForTenant = 租户调优后的生效值（请求链路用）
// ============================================================

// lookupTenant 解析租户生效值：租户覆盖层 → 系统默认层
func (s *SystemConfigService) lookupTenant(tenantID uint, key string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if tenantID > 0 {
		if tm := s.tenantCache[tenantID]; tm != nil {
			if v, ok := tm[key]; ok {
				return v, true
			}
		}
	}
	v, ok := s.cache[key]
	return v, ok
}

// GetIntForTenant 租户级 int 配置
func (s *SystemConfigService) GetIntForTenant(tenantID uint, key string, defaultValue int) int {
	if v, ok := s.lookupTenant(tenantID, key); ok {
		var n int
		if err := json.Unmarshal([]byte(v), &n); err == nil {
			return n
		}
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return int(f)
		}
	}
	return defaultValue
}

// GetBoolForTenant 租户级 bool 配置
func (s *SystemConfigService) GetBoolForTenant(tenantID uint, key string, defaultValue bool) bool {
	if v, ok := s.lookupTenant(tenantID, key); ok {
		var b bool
		if err := json.Unmarshal([]byte(v), &b); err == nil {
			return b
		}
		if v == "true" {
			return true
		}
		if v == "false" {
			return false
		}
	}
	return defaultValue
}

// GetStringForTenant 租户级 string 配置
func (s *SystemConfigService) GetStringForTenant(tenantID uint, key string, defaultValue string) string {
	if v, ok := s.lookupTenant(tenantID, key); ok {
		var str string
		if err := json.Unmarshal([]byte(v), &str); err == nil {
			return str
		}
		return v
	}
	return defaultValue
}

// GetFloatForTenant 租户级 float 配置
func (s *SystemConfigService) GetFloatForTenant(tenantID uint, key string, defaultValue float64) float64 {
	if v, ok := s.lookupTenant(tenantID, key); ok {
		var f float64
		if err := json.Unmarshal([]byte(v), &f); err == nil {
			return f
		}
	}
	return defaultValue
}

// BatchUpdateForTenant 租户级批量更新：upsert 到 (tenant_id, key)
// 与全局 BatchUpdate 隔离——租户改参数绝不污染系统默认
func (s *SystemConfigService) BatchUpdateForTenant(tenantID uint, items []ConfigUpdateItem) error {
	if tenantID == 0 {
		return s.BatchUpdate(items)
	}
	for _, item := range items {
		if !json.Valid([]byte(item.Value)) {
			log.Printf("[系统配置] 跳过非法JSON值: tenant=%d key=%s", tenantID, item.Key)
			continue
		}
		var existing model.SystemConfig
		err := db.DB.Where("tenant_id = ? AND \"key\" = ?", tenantID, item.Key).First(&existing).Error
		if err != nil {
			// 租户首次覆盖：以系统默认行做模板克隆
			var def model.SystemConfig
			db.DB.Where("tenant_id = 0 AND \"key\" = ?", item.Key).First(&def)
			row := model.SystemConfig{
				TenantID:     tenantID,
				Category:     def.Category,
				Key:          item.Key,
				Value:        item.Value,
				ValueType:    def.ValueType,
				Description:  def.Description,
				DefaultValue: def.DefaultValue,
				SortOrder:    def.SortOrder,
			}
			if row.ValueType == "" {
				row.ValueType = "string"
			}
			if err := db.DB.Create(&row).Error; err != nil {
				log.Printf("[系统配置] 租户覆盖写入失败: tenant=%d key=%s err=%v", tenantID, item.Key, err)
				return err
			}
		} else {
			if err := db.DB.Model(&existing).Update("value", item.Value).Error; err != nil {
				return err
			}
		}
		log.Printf("[系统配置] 租户覆盖已更新: tenant=%d key=%s", tenantID, item.Key)
	}
	s.Reload()
	return nil
}

// GetFloat 获取 float 配置值（key 不存在回退默认值）
func (s *SystemConfigService) GetFloat(key string, defaultValue float64) float64 {
	s.mu.RLock()
	val, exists := s.cache[key]
	s.mu.RUnlock()

	if !exists {
		return defaultValue
	}

	var f float64
	if err := json.Unmarshal([]byte(val), &f); err == nil {
		return f
	}
	return defaultValue
}

// GetInt 获取int类型的配置值
// key: 配置键名，如 "theta_rounds"
// defaultValue: key不存在时的兜底值
func (s *SystemConfigService) GetInt(key string, defaultValue int) int {
	s.mu.RLock()
	val, exists := s.cache[key]
	s.mu.RUnlock()

	if !exists {
		return defaultValue
	}

	// JSON数字默认解析为float64，先试float再转int
	var f float64
	if err := json.Unmarshal([]byte(val), &f); err == nil {
		return int(f)
	}
	return defaultValue
}

// GetBool 获取bool类型的配置值
// key: 配置键名，如 "mock_mode"
// defaultValue: key不存在时的兜底值
func (s *SystemConfigService) GetBool(key string, defaultValue bool) bool {
	s.mu.RLock()
	val, exists := s.cache[key]
	s.mu.RUnlock()

	if !exists {
		return defaultValue
	}

	var b bool
	if err := json.Unmarshal([]byte(val), &b); err == nil {
		return b
	}
	return defaultValue
}

// GetString 获取string类型的配置值
// key: 配置键名
// defaultValue: key不存在时的兜底值
func (s *SystemConfigService) GetString(key string, defaultValue string) string {
	s.mu.RLock()
	val, exists := s.cache[key]
	s.mu.RUnlock()

	if !exists {
		return defaultValue
	}

	var s2 string
	if err := json.Unmarshal([]byte(val), &s2); err == nil {
		return s2
	}
	return val // 不是JSON字符串，直接返回原始值
}

// GetIntSlice 获取int数组类型的配置值
// key: 配置键名，如 "l3_simple_delay"
// defaultValue: key不存在时的兜底值
func (s *SystemConfigService) GetIntSlice(key string, defaultValue []int) []int {
	s.mu.RLock()
	val, exists := s.cache[key]
	s.mu.RUnlock()

	if !exists {
		return defaultValue
	}

	// 先尝试直接解析为[]int
	var intSlice []int
	if err := json.Unmarshal([]byte(val), &intSlice); err == nil {
		return intSlice
	}

	// 再尝试[]float64转[]int（JSON数字默认为float64）
	var floatSlice []float64
	if err := json.Unmarshal([]byte(val), &floatSlice); err == nil {
		result := make([]int, len(floatSlice))
		for i, v := range floatSlice {
			result[i] = int(v)
		}
		return result
	}

	return defaultValue
}

// GetStringSlice 获取string数组类型的配置值
// key: 配置键名，如 "model_priority"
// defaultValue: key不存在时的兜底值
func (s *SystemConfigService) GetStringSlice(key string, defaultValue []string) []string {
	s.mu.RLock()
	val, exists := s.cache[key]
	s.mu.RUnlock()

	if !exists {
		return defaultValue
	}

	var slice []string
	if err := json.Unmarshal([]byte(val), &slice); err == nil {
		return slice
	}
	return defaultValue
}

// GetJSON 获取任意JSON类型的配置值
// 通用反序列化方法，将配置值解析到target指针指向的结构中
// 用法：var weights []AnchorWeight; svc.GetJSON("anchor_weights", &weights)
// 修复：锚权重等复杂配置从硬编码→后台可调，需要通用JSON读取能力
func (s *SystemConfigService) GetJSON(key string, target interface{}) bool {
	s.mu.RLock()
	val, exists := s.cache[key]
	s.mu.RUnlock()

	if !exists {
		return false
	}

	if err := json.Unmarshal([]byte(val), target); err != nil {
		log.Printf("[系统配置] JSON解析失败: key=%s, error=%v", key, err)
		return false
	}
	return true
}

// ============================================================
// 更新方法 - 写DB + 热加载内存
// ============================================================

// BatchUpdate 批量更新配置
// items: [{key, value}, ...] 只更新Value字段，不改变其他元信息
// 更新后自动重载内存缓存
func (s *SystemConfigService) BatchUpdate(items []ConfigUpdateItem) error {
	if len(items) == 0 {
		return nil
	}

	// 逐项更新DB
	for _, item := range items {
		if item.Key == "" {
			continue
		}

		// 校验值是否为合法JSON（所有值都以JSON格式存储）
		if !json.Valid([]byte(item.Value)) {
			log.Printf("[系统配置] 跳过非法JSON值: key=%s, value=%s", item.Key, item.Value)
			continue
		}

		// 修复（2026-08-23）：限定系统默认层(tenant_id=0)——本方法语义是"写系统层"，
		// 不带租户过滤会误改所有租户覆盖行
		result := db.DB.Model(&model.SystemConfig{}).
			Where("tenant_id = 0 AND \"key\" = ?", item.Key).
			Update("value", item.Value)
		if result.Error != nil {
			log.Printf("[系统配置] 更新失败: key=%s, error=%v", item.Key, result.Error)
			return result.Error
		}

		log.Printf("[系统配置] 已更新: key=%s, value=%s", item.Key, item.Value)
	}

	// 更新后重新加载内存缓存
	s.Reload()

	return nil
}

// ResetAll 恢复所有配置为默认值
// 用DefaultConfigs中的DefaultValue覆盖当前Value
// 修复（2026-08-23）：限定系统默认层(tenant_id=0)，租户覆盖层不动
func (s *SystemConfigService) ResetAll() error {
	for _, cfg := range DefaultConfigs {
		result := db.DB.Model(&model.SystemConfig{}).
			Where("tenant_id = 0 AND \"key\" = ?", cfg.Key).
			Update("value", cfg.DefaultValue)
		if result.Error != nil {
			log.Printf("[系统配置] 重置失败: key=%s, error=%v", cfg.Key, result.Error)
			return result.Error
		}
	}

	// 重新加载内存缓存
	s.Reload()

	log.Println("[系统配置] 所有配置已恢复默认值")
	return nil
}

// ConfigUpdateItem 配置更新项
// Admin API批量更新时的请求体结构
type ConfigUpdateItem struct {
	Key   string `json:"key" binding:"required"`   // 配置键名
	Value string `json:"value" binding:"required"` // 新值（JSON字符串）
}

// PlatformLevelKeys 平台级配置键（商业化 M1/M5，2026-08-23）
// 语义：这些参数是平台层开关（收款模式/计费灰度/触达通道），与单个租户无关，
// 必须写系统默认层(tenant_id=0)且仅超管可改——绝不允许落入租户覆盖层，
// 否则读取端(GetString系统层)看不到变更，且任一租户管理员可改全站收款方式
var PlatformLevelKeys = map[string]bool{
	"pay_mode":                        true,
	"static_qr_image":                 true,
	"billing_enforced":                true,
	"trial_ai_calls":                  true,
	"wecom_webhook_url":               true,
	"reset_code_channel":              true,
	"turnstile_enabled":               true,
	"turnstile_site_key":              true,
	"turnstile_secret_key":            true,
	"register_ip_daily_limit":         true,
	"register_ip_min_interval_sec":    true,
	"registration_review":             true,
	"dingtalk_webhook_url":            true,
	"billing_markup_multiplier":       true,
	"price_micro_per_ktok_zhipu":      true,
	"price_micro_per_ktok_siliconflow": true,
	"stage_models":                    true,
}

// GetInterval 解析[min,max]格式的配置项，返回区间内的随机整数值
// 修复：到店倾向延迟等参数改为后台可调区间，统一用此方法解析
// fallback: 解析失败时返回defaultValue
func (s *SystemConfigService) GetInterval(key string, defaultMin, defaultMax int) int {
	var interval [2]int
	if s.GetJSON(key, &interval) && interval[0] >= 0 && interval[1] > interval[0] {
		return interval[0] + rand.Intn(interval[1]-interval[0]+1)
	}
	// fallback：用默认区间
	return defaultMin + rand.Intn(defaultMax-defaultMin+1)
}
