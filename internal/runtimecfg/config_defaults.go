// 默认配置定义与平台级键清单（D2a 文件拆分 2026-09-12，自 system_config_service.go 迁出，不改行为）
// 本文件只放"数据表"（默认值/退役键/平台级键），运行时读写逻辑留在 system_config_service.go。
package runtimecfg

import (
	"ai-scrm/internal/model"
)

// retiredConfigKeys 已停用配置键清单（2026-09-09 清理）。
// 这些键曾被默认配置写下，但因实现演进不再被任何代码读取（延迟参数改固定档位、三层分流简化）。
// ensureDefaults 启动时会删除系统层(tenant_id=0)的这些存量键，避免后台配置中心展示死配置误导运营；
// ForceResetDefaults 从 DefaultConfigs 重写时已不再包含它们，天然不会复现。
var retiredConfigKeys = map[string]bool{
	"reply_min_delay": true, "max_reply_delay": true,
	"offline_offset_work_simple": true, "offline_offset_work_medium": true, "offline_offset_work_complex": true,
	"offline_offset_offwork_simple": true, "offline_offset_offwork_medium": true, "offline_offset_offwork_complex": true,
	"l1_simple_delay": true, "l2_simple_delay": true, "l3_simple_delay": true, "l1_complex_delay": true,
	"off_work_multiplier": true, "weekend_multiplier": true,
}

// ============================================================
// 默认配置定义
// 当DB中无数据时，使用这些默认值初始化
// 按四大分类组织，sort_order决定同分类内的显示顺序
// ============================================================

// DefaultConfigs 默认配置列表（导出，供API层强制初始化时引用）
// 注意：每项的 Value 和 DefaultValue 都是 JSON 字符串格式
var DefaultConfigs = []model.SystemConfig{
	// ---- 分类1：reply_speed（回复速度类）----
	// 修复(2026-09-09)：移除已停用的延迟配置键。
	// CalcHumanlikeDelay 已改为固定 5~15s 随机（废除"打字+线下偏移"公式），
	// 原 reply_min_delay/max_reply_delay 与 offline_offset_*（6键）不再被读取，
	// 从默认配置中移除，避免后台展示死配置误导；保留 merge_window/simple_msg_delay/reply_delay_mode 等仍生效键。
	{Category: "reply_speed", Key: "merge_window_seconds", Value: "25", ValueType: "number", Description: "消息合并窗口(秒)", DefaultValue: "25", SortOrder: 10},
	{Category: "reply_speed", Key: "max_merge_messages", Value: "3", ValueType: "number", Description: "最大合并条数", DefaultValue: "3", SortOrder: 11},
	// 修复：以下参数原硬编码在代码中，现改为后台可调，无需发版即可调节
	{Category: "reply_speed", Key: "simple_msg_delay", Value: "8", ValueType: "number", Description: "简单消息延迟(秒)-固定值", DefaultValue: "8", SortOrder: 12},
	{Category: "reply_speed", Key: "store_visit_first_delay", Value: "[10,15]", ValueType: "json", Description: "到店倾向第一段延迟区间(秒)[min,max]", DefaultValue: "[10,15]", SortOrder: 13},
	{Category: "reply_speed", Key: "store_visit_second_delay", Value: "[25,45]", ValueType: "json", Description: "到店倾向第二段延迟区间(秒)[min,max]", DefaultValue: "[25,45]", SortOrder: 14},
	{Category: "reply_speed", Key: "processing_lock_timeout", Value: "600", ValueType: "number", Description: "processing锁超时(秒)-防卡死(C5:原90s会误杀正常在途批次)", DefaultValue: "600", SortOrder: 15},

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
	// D9 包质量低分告警（2026-09-13）：离线评分 0~100，连续劣化触发群告警。
	{Category: "strategy", Key: "evals_pack_alert_enabled", Value: "true", ValueType: "bool", Description: "包质量低分告警开关", DefaultValue: "true", SortOrder: 22},
	{Category: "strategy", Key: "evals_pack_alert_score", Value: "60", ValueType: "number", Description: "包质量低分阈值(0~100，低于此值进入告警候选)", DefaultValue: "60", SortOrder: 23},
	{Category: "strategy", Key: "evals_pack_alert_consecutive", Value: "3", ValueType: "number", Description: "同包/模板连续低分次数阈值", DefaultValue: "3", SortOrder: 24},
	{Category: "strategy", Key: "evals_pack_alert_min_samples", Value: "5", ValueType: "number", Description: "包质量告警最小样本数", DefaultValue: "5", SortOrder: 25},
	// 修复：锚权重从硬编码→后台可调，7组权重对应7种锚类型
	// 后台改权重→热加载→下次推理立即生效，不需要改代码发版
	{Category: "strategy", Key: "anchor_weights", Value: `[{"intent_score_weight":-2.0,"trust_weight":-1.5,"hook_rate_weight":-1.0,"stage_weight":-1.0,"price_sens_weight":0.0,"base_bias":0.5},{"intent_score_weight":0.5,"trust_weight":0.3,"hook_rate_weight":0.2,"stage_weight":0.3,"price_sens_weight":0.0,"base_bias":1.0},{"intent_score_weight":1.0,"trust_weight":0.5,"hook_rate_weight":0.3,"stage_weight":0.8,"price_sens_weight":0.0,"base_bias":0.5},{"intent_score_weight":1.5,"trust_weight":0.8,"hook_rate_weight":0.5,"stage_weight":1.2,"price_sens_weight":0.5,"base_bias":0.3},{"intent_score_weight":1.8,"trust_weight":1.0,"hook_rate_weight":0.6,"stage_weight":1.5,"price_sens_weight":0.8,"base_bias":0.0},{"intent_score_weight":2.0,"trust_weight":1.2,"hook_rate_weight":0.8,"stage_weight":1.8,"price_sens_weight":0.3,"base_bias":-0.2},{"intent_score_weight":2.2,"trust_weight":1.5,"hook_rate_weight":1.0,"stage_weight":2.0,"price_sens_weight":0.5,"base_bias":-0.5}]`, ValueType: "json", Description: "7种锚权重(不抛/同类/拆解/对比/损失/稀缺/代价自担)", DefaultValue: `[{"intent_score_weight":-2.0,"trust_weight":-1.5,"hook_rate_weight":-1.0,"stage_weight":-1.0,"price_sens_weight":0.0,"base_bias":0.5},{"intent_score_weight":0.5,"trust_weight":0.3,"hook_rate_weight":0.2,"stage_weight":0.3,"price_sens_weight":0.0,"base_bias":1.0},{"intent_score_weight":1.0,"trust_weight":0.5,"hook_rate_weight":0.3,"stage_weight":0.8,"price_sens_weight":0.0,"base_bias":0.5},{"intent_score_weight":1.5,"trust_weight":0.8,"hook_rate_weight":0.5,"stage_weight":1.2,"price_sens_weight":0.5,"base_bias":0.3},{"intent_score_weight":1.8,"trust_weight":1.0,"hook_rate_weight":0.6,"stage_weight":1.5,"price_sens_weight":0.8,"base_bias":0.0},{"intent_score_weight":2.0,"trust_weight":1.2,"hook_rate_weight":0.8,"stage_weight":1.8,"price_sens_weight":0.3,"base_bias":-0.2},{"intent_score_weight":2.2,"trust_weight":1.5,"hook_rate_weight":1.0,"stage_weight":2.0,"price_sens_weight":0.5,"base_bias":-0.5}]`, SortOrder: 18},

	// ---- 分类3：mental_stage（心智阶段类）----
	{Category: "mental_stage", Key: "stage_step_enabled", Value: "true", ValueType: "bool", Description: "逐级递进开关", DefaultValue: "true", SortOrder: 1},
	{Category: "mental_stage", Key: "stage_max_increment", Value: "1", ValueType: "number", Description: "每轮最大阶段增量", DefaultValue: "1", SortOrder: 2},
	{Category: "mental_stage", Key: "hook_rate_stage1_threshold", Value: "0.3", ValueType: "number", Description: "HookRate<此值卡在Stage1", DefaultValue: "0.3", SortOrder: 3},
	{Category: "mental_stage", Key: "force_stage0_attempts", Value: "1", ValueType: "number", Description: "Attempts≤此值强制stage=0", DefaultValue: "1", SortOrder: 4},

	// ---- 分类4：ai_chain（AI链路类）----
	{Category: "ai_chain", Key: "model_priority", Value: `["siliconflow_deepseek_v4_flash","siliconflow_glm4_9b","template_fallback"]`, ValueType: "json", Description: "模型降级优先级(可拖拽排序,与 InitRouter 实际链一致;如需恢复智谱在 stage_models 指定 provider=zhipu)", DefaultValue: `["siliconflow_deepseek_v4_flash","siliconflow_glm4_9b","template_fallback"]`, SortOrder: 1},
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
	{Category: "ai_chain", Key: "chat_history_rounds", Value: "3", ValueType: "number", Description: "注入AI的对话历史轮数(0=关闭改用核心摘要)", DefaultValue: "3", SortOrder: 7},
	// 修复问题2：回复延迟模式，instant=秒回无延迟，normal=正常模拟真人延迟
	// 用途：测试/演示场景可切到instant秒回，正式环境用normal
	{Category: "reply_speed", Key: "reply_delay_mode", Value: "normal", ValueType: "string", Description: "回复延迟模式：normal=正常延迟，instant=秒回无延迟", DefaultValue: "normal", SortOrder: 99},

	// M3 分阶段模型覆盖：便宜模型跑意图识别、强模型跑话术生成；空=走全局降级链
	{Category: "ai_chain", Key: "stage_models", Value: "{\"evals\":{\"provider\":\"siliconflow\",\"model\":\"THUDM/GLM-4-9B-0414\"}}", ValueType: "json", Description: "分阶段模型覆盖(reply/intent/strategy/evals各选provider+model,留空走降级链)", DefaultValue: "{\"evals\":{\"provider\":\"siliconflow\",\"model\":\"THUDM/GLM-4-9B-0414\"}}", SortOrder: 8},
	// ---- 分类5：human_takeover（人工接管类）----
	{Category: "human_takeover", Key: "assigned_lead_ai_auto_reply", Value: "true", ValueType: "bool", Description: "已分配线索AI自动回复开关(true=顾问超时未回时AI自动回复)", DefaultValue: "true", SortOrder: 1},
	{Category: "human_takeover", Key: "assigned_lead_ai_timeout", Value: "300", ValueType: "number", Description: "已分配线索顾问超时时间(秒)，超时后AI自动回复", DefaultValue: "300", SortOrder: 2},

	// ---- 分类6：billing（商业化类，2026-08-23 M1/M2/M5）----
	// pay_mode 三态：mock=测试模拟到账（默认，跑通全链路）/ static_qr=静态码+人工确认 / sdk=商户号到位后切换
	{Category: "billing", Key: "pay_mode", Value: "\"mock\"", ValueType: "string", Description: "收款模式(mock模拟到账/static_qr静态码人工确认)", DefaultValue: "\"mock\"", SortOrder: 1},
	{Category: "billing", Key: "static_qr_image", Value: "\"\"", ValueType: "string", Description: "静态收款码(URL或base64，static_qr模式下单返回给租户)", DefaultValue: "\"\"", SortOrder: 2},
	// 灰度开关（借翻译助手决策"默认不强制只留痕"）：false=CheckAIQuota 恒放行只记用量（上线初期防误伤）
	{Category: "billing", Key: "billing_enforced", Value: "false", ValueType: "bool", Description: "计费强制开关(false=超额不停服仅记日志告警)", DefaultValue: "false", SortOrder: 3},
	{Category: "billing", Key: "order_timeout_minutes", Value: "15", ValueType: "int", Description: "待支付订单超时自动关闭(分钟)，超时未付转closed防僵尸单堆积", DefaultValue: "15", SortOrder: 4},
	// ---- M-R 邀请推广（2026-08-25）：全平台统一推广政策，仅超管可调 ----
	{Category: "billing", Key: "trial_token_amount", Value: "300000", ValueType: "int", Description: "注册赠送免费token数（入③免费体验桶）", DefaultValue: "300000", SortOrder: 5},
	{Category: "billing", Key: "trial_token_valid_days", Value: "14", ValueType: "int", Description: "注册赠送免费token有效天数", DefaultValue: "14", SortOrder: 6},
	{Category: "billing", Key: "referral_bonus_tokens", Value: "300000", ValueType: "int", Description: "每邀请1名好友注册成功，邀请人免费token增量", DefaultValue: "300000", SortOrder: 7},
	{Category: "billing", Key: "referral_extend_days", Value: "14", ValueType: "int", Description: "每邀请1名好友注册成功，邀请人免费token有效期延长天数", DefaultValue: "14", SortOrder: 8},
	{Category: "billing", Key: "referral_paid_bonus_tokens", Value: "500000", ValueType: "int", Description: "受邀好友购买paid套餐首笔到账后，邀请人获永久token数(入②余额桶)；单受邀限一次", DefaultValue: "500000", SortOrder: 9},
	// KB继承链改造（2026-08-26）：跨部门回退租户策略——租户级可调（非平台键）
	{Category: "notify", Key: "feedback_collector_url", Value: "", ValueType: "string", Description: "数据飞轮回流collector地址(HTTPS,空=关闭)；调参行为/包操作审计增量每小时上报", DefaultValue: "", SortOrder: 11},
	{Category: "billing", Key: "register_email_daily_limit", Value: "3", ValueType: "int", Description: "防薅v2：同一邮箱每日注册提交上限(生产态主锚；IP限流仅内测兜底)", DefaultValue: "3", SortOrder: 12},
	{Category: "billing", Key: "token_billing_enabled", Value: "false", ValueType: "bool", Description: "Token三桶扣减引擎总闸(false=仅落账不扣费；true=按③免费桶→①订阅额度→②余额扣减)", DefaultValue: "false", SortOrder: 10},
	{Category: "knowledge", Key: "kb_cross_dept_fallback", Value: "true", ValueType: "bool", Description: "跨部门知识回退：开启时兄弟部门的共享部门包内容对本部门可见（精确命中打标采用）", DefaultValue: "true", SortOrder: 1},
	// D3(2026-09-13)：KB 向量检索热开关。关闭时不请求 embedding、不走 pgvector；pgvector 不可用时仍回退旧路径。
	{Category: "knowledge", Key: "kb_vector_search", Value: "true", ValueType: "bool", Description: "KB向量检索开关(pgvector近邻+embedding余弦混合；关闭回退关键词检索)", DefaultValue: "true", SortOrder: 2},
	// 注册试用包额度：新租户注册自动发放 free 包时的 AI 调用次数
	{Category: "billing", Key: "trial_ai_calls", Value: "500", ValueType: "number", Description: "注册试用包AI调用次数(次)", DefaultValue: "500", SortOrder: 4},
	// ---- 支付网关 sdk 配置（2026-08-31 UAT 修复）----
	// 平台级支付网关参数：必须在此预置系统层种子行，admin/config 才能写入系统层(tenant_id=0)，
	// 否则 BatchUpdate 静默0行、webhook/loadGatewayProvider 读系统层永远拿到空值（验签必败）。
	{Category: "billing", Key: "pay_gateway_url", Value: "\"\"", ValueType: "string", Description: "支付网关端点([OI]式HTTP API，sdk模式下单/查单用)", DefaultValue: "\"\"", SortOrder: 13},
	{Category: "billing", Key: "pay_gateway_app_id", Value: "\"\"", ValueType: "string", Description: "支付网关应用ID(商户标识)", DefaultValue: "\"\"", SortOrder: 14},
	{Category: "billing", Key: "pay_gateway_key", Value: "\"\"", ValueType: "string", Description: "支付网关验签密钥(HMAC-SHA256，与网关创建支付/回调验签对称，敏感勿外泄)", DefaultValue: "\"\"", SortOrder: 15},
	{Category: "billing", Key: "pay_gateway_notify_url", Value: "\"\"", ValueType: "string", Description: "支付网关异步通知回调地址(收到到账后回调本系统webhook)", DefaultValue: "\"\"", SortOrder: 16},
	// ---- P0-1 原生支付渠道（2026-09-15，微信支付V3/支付宝当面付）----
	// pay_provider 分发 sdk 模式渠道：默认 gateway 兼容存量部署（已配 pay_gateway_* 零感知）。
	// wechat/alipay 的私钥/密钥为敏感配置，走系统配置或环境变量注入，不入日志不入审计明文。
	{Category: "billing", Key: "pay_provider", Value: "\"gateway\"", ValueType: "string", Description: "sdk模式支付渠道:gateway=通用HMAC网关(默认)|wechat=微信支付V3|alipay=支付宝当面付", DefaultValue: "\"gateway\"", SortOrder: 17},
	{Category: "billing", Key: "pay_wechat_app_id", Value: "\"\"", ValueType: "string", Description: "微信支付公众号/小程序AppID", DefaultValue: "\"\"", SortOrder: 18},
	{Category: "billing", Key: "pay_wechat_mch_id", Value: "\"\"", ValueType: "string", Description: "微信支付商户号", DefaultValue: "\"\"", SortOrder: 19},
	{Category: "billing", Key: "pay_wechat_serial_no", Value: "\"\"", ValueType: "string", Description: "微信商户API证书序列号(Authorization头用)", DefaultValue: "\"\"", SortOrder: 20},
	{Category: "billing", Key: "pay_wechat_private_key", Value: "\"\"", ValueType: "string", Description: "微信商户API私钥PEM全文(敏感勿外泄,PKCS8/PKCS1)", DefaultValue: "\"\"", SortOrder: 21},
	{Category: "billing", Key: "pay_wechat_apiv3_key", Value: "\"\"", ValueType: "string", Description: "微信APIv3密钥(32字节,回调解密用,敏感勿外泄)", DefaultValue: "\"\"", SortOrder: 22},
	{Category: "billing", Key: "pay_wechat_notify_url", Value: "\"\"", ValueType: "string", Description: "微信支付回调地址(须外网可达,如https://域名/api/v1/billing/webhook/wechat)", DefaultValue: "\"\"", SortOrder: 23},
	{Category: "billing", Key: "pay_alipay_app_id", Value: "\"\"", ValueType: "string", Description: "支付宝开放平台应用AppID", DefaultValue: "\"\"", SortOrder: 24},
	{Category: "billing", Key: "pay_alipay_private_key", Value: "\"\"", ValueType: "string", Description: "支付宝应用私钥PEM全文(敏感勿外泄,RSA2)", DefaultValue: "\"\"", SortOrder: 25},
	{Category: "billing", Key: "pay_alipay_public_key", Value: "\"\"", ValueType: "string", Description: "支付宝平台公钥PEM(异步通知验签用,开放平台加签方式页下载)", DefaultValue: "\"\"", SortOrder: 26},
	{Category: "billing", Key: "pay_alipay_notify_url", Value: "\"\"", ValueType: "string", Description: "支付宝异步通知地址(如https://域名/api/v1/billing/webhook/alipay)", DefaultValue: "\"\"", SortOrder: 27},

	// ---- 分类7：notify（触达通道类，批次一顺手做：企微群机器人 + 重置码通道）----
	{Category: "notify", Key: "wecom_webhook_url", Value: "\"\"", ValueType: "string", Description: "企微群机器人webhook(敏感配置勿外泄；留资/人工确认订单推送)", DefaultValue: "\"\"", SortOrder: 1},
	{Category: "notify", Key: "reset_code_channel", Value: "\"log\"", ValueType: "string", Description: "重置码发送通道(log=打日志需校验手机号/smtp=邮件直发)", DefaultValue: "\"log\"", SortOrder: 2},

	// ---- 防薅：Turnstile 人机验证（批次三，2026-08-23 代码就绪）----
	// 挂 C 端免登录接口 /chat/guest · /chat/test；enabled=false 或 secret 为空时完全关闭零开销
	// 前端从 GET /api/v1/turnstile/sitekey 拿站点键渲染组件，验证令牌走 X-Turnstile-Token 头
	{Category: "billing", Key: "billing_markup_multiplier", Value: "1.5", ValueType: "number", Description: "Token成本均摊系数(对外成本口径=真实token×系数)", DefaultValue: "1.5", SortOrder: 5},
	{Category: "billing", Key: "price_micro_per_ktok_zhipu", Value: "15000", ValueType: "number", Description: "智谱单价(微元/千token,成本核算用)", DefaultValue: "15000", SortOrder: 6},
	{Category: "billing", Key: "price_micro_per_ktok_siliconflow", Value: "8000", ValueType: "number", Description: "硅基流动单价(微元/千token,成本核算用)", DefaultValue: "8000", SortOrder: 7},
	{Category: "notify", Key: "email_verify_enabled", Value: "true", ValueType: "bool", Description: "注册邮箱验证开关(注册/换绑邮箱需验证码)", DefaultValue: "true", SortOrder: 7},
	// ---- C1 内容安全闸门（2026-09-12，商用合规前置）----
	{Category: "notify", Key: "contentsafety_enabled", Value: "true", ValueType: "bool", Description: "AI出站回复内容安全闸门开关", DefaultValue: "true", SortOrder: 8},
	{Category: "notify", Key: "contentsafety_mode", Value: "\"shadow\"", ValueType: "string", Description: "内容安全模式:shadow=只告警计数不改写|enforce=MASK替换+BLOCK转人工(首周shadow演练误杀率后切enforce)", DefaultValue: "\"shadow\"", SortOrder: 9},
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

// PlatformLevelKeys 平台级配置键（商业化 M1/M5，2026-08-23）
// 语义：这些参数是平台层开关（收款模式/计费灰度/触达通道），与单个租户无关，
// 必须写系统默认层(tenant_id=0)且仅超管可改——绝不允许落入租户覆盖层，
// 否则读取端(GetString系统层)看不到变更，且任一租户管理员可改全站收款方式
var PlatformLevelKeys = map[string]bool{
	"pay_mode":                         true,
	"static_qr_image":                  true,
	"billing_enforced":                 true,
	"trial_ai_calls":                   true,
	"wecom_webhook_url":                true,
	"reset_code_channel":               true,
	"turnstile_enabled":                true,
	"turnstile_site_key":               true,
	"turnstile_secret_key":             true,
	"register_ip_daily_limit":          true,
	"register_ip_min_interval_sec":     true,
	"registration_review":              true,
	"dingtalk_webhook_url":             true,
	"billing_markup_multiplier":        true,
	"price_micro_per_ktok_zhipu":       true,
	"price_micro_per_ktok_siliconflow": true,
	"stage_models":                     true,
	"order_timeout_minutes":            true, // M4(2026-08-25)：订单超时关闭阈值，平台级统一定价节奏
	"trial_token_amount":               true, // M-R(2026-08-25)：注册赠送token数
	"trial_token_valid_days":           true, // M-R：免费token有效天数
	"referral_bonus_tokens":            true, // M-R：每邀1人注册的token奖励
	"referral_extend_days":             true, // M-R：每邀1人的有效期延长天数
	"referral_paid_bonus_tokens":       true, // M-R：受邀人付费后邀请人得永久token数
	"email_verify_enabled":             true, // 修复(2026-08-25)：文档称平台级热开关但从未入表——写租户层读系统层永远看不到变更
	"token_billing_enabled":            true, // P1.5(2026-08-26)：Token三桶扣减引擎总闸，默认关=灰度兼容现状
	"feedback_collector_url":           true, // P3：数据飞轮回流collector端点(空=关闭)
	"register_email_daily_limit":       true, // 防薅v2(2026-08-26)：账号锚限流阈值
	"pay_gateway_url":                  true, // UAT修复(2026-08-31)：支付网关端点(平台级)——写入系统层供 webhook/网关读取
	"pay_gateway_app_id":               true, // UAT修复(2026-08-31)：支付网关应用ID(平台级)
	"pay_gateway_key":                  true, // UAT修复(2026-08-31)：支付网关验签密钥(平台级)——不入租户覆盖层，否则 webhook 验签永远失败
	"pay_gateway_notify_url":           true, // UAT修复(2026-08-31)：支付网关异步通知地址(平台级)
	"pay_provider":                     true, // P0-1(2026-09-15)：sdk模式渠道分发(平台级)——租户改它等于改全站收款方式
	"pay_wechat_app_id":                true, // P0-1(2026-09-15)：微信支付凭证(平台级,含私钥/apiv3key 敏感项,绝不入租户层)
	"pay_wechat_mch_id":                true,
	"pay_wechat_serial_no":             true,
	"pay_wechat_private_key":           true,
	"pay_wechat_apiv3_key":             true,
	"pay_wechat_notify_url":            true,
	"pay_alipay_app_id":                true, // P0-1(2026-09-15)：支付宝凭证(平台级,含应用私钥/平台公钥敏感项)
	"pay_alipay_private_key":           true,
	"pay_alipay_public_key":            true,
	"pay_alipay_notify_url":            true,
	"contentsafety_enabled":            true, // C1(2026-09-12)：内容安全闸门总开关(平台级，租户不可各自关闭合规)
	"contentsafety_mode":               true, // C1：shadow|enforce 模式(平台级统一灰度节奏)
	"evals_pack_alert_enabled":         true, // D9(2026-09-13)：包质量低分告警开关(平台级统一触达)
	"evals_pack_alert_score":           true, // D9：低分阈值(平台级默认，避免租户关闭质量监控)
	"evals_pack_alert_consecutive":     true, // D9：连续低分次数阈值(平台级)
	"evals_pack_alert_min_samples":     true, // D9：告警最小样本数(平台级)
}
