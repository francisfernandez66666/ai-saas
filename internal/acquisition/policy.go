// Package acquisition 获客活码领域层：码的配置与启停、公开扫码链路、扫码事件的归因写入，
// 以及"这个码带来了多少人、其中多少开口/留资/到店/成交"的漏斗判定。
//
// 分包位置（D2b 分包红线）：领域代码不进 internal/service，调用方是 api 层与 C 端入口。
// 依赖方向：acquisition → db/model/analytics（阶段集合复用，见 MatchFunnelMetric）；
// **本包不 import internal/llm / chatflow / channel**——它只回答"人从哪来"，
// 不碰"AI 怎么回"，避免又长出一条业务层直连 AI 的红线违规。
package acquisition

import (
	"crypto/rand"
	"math/big"
	"strings"
	"time"

	"ai-scrm/internal/analytics"
)

// ============================================================
// 公开短码
// ============================================================

// codeAlphabet 短码字符集：**去掉了 0/O/1/I/L** 这五个肉眼在海报、朋友圈缩略图、
// 印刷品上极易混淆的字符。活码是给人抄、给人念的，不是给程序读的，
// 混淆字符造成的"客户输错进不来"根本没有报错可查。
const codeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

// CodeLength 短码长度。8 位 × 31 字符集 ≈ 8.5e11 组合，且码只增不删、字符串永不复用，
// 不可枚举；配合公开端点的 IP 限流，遍历猜码在实际速率下不可行。
const CodeLength = 8

// NewCode 生成一个密码学随机的公开短码。
// 为什么不用时间戳/自增派生：那种码会被顺藤摸到别人的租户与流量规模，
// 而活码端点是**公开无鉴权**的，码本身就是唯一的访问凭证。
func NewCode() (string, error) {
	out := make([]byte, CodeLength)
	for i := range out {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(codeAlphabet))))
		if err != nil {
			return "", err
		}
		out[i] = codeAlphabet[n.Int64()]
	}
	return string(out), nil
}

// NormalizeCode 清洗外部传入的码：去空白、转大写（字符集里没有小写，
// 但客户手输/口令传播会给到小写，大小写不敏感比"错了就没归因"划算）。
// 长度不符或含非法字符一律返回空串——调用方据此判"这不是一个合法码"，
// 从而在查库之前就挡掉乱填与遍历（不把每次试探都变成一次 DB 查询）。
func NormalizeCode(raw string) string {
	s := strings.ToUpper(strings.TrimSpace(raw))
	if len(s) != CodeLength {
		return ""
	}
	for _, r := range s {
		if !strings.ContainsRune(codeAlphabet, r) {
			return ""
		}
	}
	return s
}

// ============================================================
// 落地链接
// ============================================================

// LandingPath 扫码后的落地路径（C 端对话页，前端从 query 里读 code）。
const LandingPath = "/client"

// BuildLink 构造活码落地链接。
//
// base 的取值优先级（**在调用方**决定，本函数只拼接，纯函数便于单测）：
//  1. 平台级热配 acquisition_link_base（超管可设，如 https://go.example.com）——
//     印海报时域名必须是确定的，不能取决于"谁在什么机器上点了一下生成"；
//  2. 请求 Host（沿用邀请码链路的既有口径 buildInviteURL，兼容反代 X-Forwarded-Proto）。
//
// 一个诚实的限制写在这里而不是藏起来：C 端租户识别本来就依赖"独立域名/子域"
// （见 middleware.resolveTenant 的取值链），所以**没有独立域名的租户**，
// 用默认域名分发的活码会落到默认租户语境——本包因此在归因处强制校验
// "码所属租户 == 当前请求租户"，不匹配就拒写归因（见 DecideApply），
// 绝不让一个渠道的流量记到另一家头上。
func BuildLink(base, code string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return LandingPath + "?code=" + code
	}
	return base + LandingPath + "?code=" + code
}

// ============================================================
// 扫码去重
// ============================================================

// ScanDedupeWindow 同一访客（同一 visitor_key）对同一码重复打开的合并窗口。
// 为什么必须去重：手机微信里"下拉返回再进来"、"转发给自己再看一次"都是同一件事，
// 不去重则"扫码数"会被同一个人刷成虚高，而渠道预算是按这个数分配的。
const ScanDedupeWindow = 10 * time.Minute

// ShouldCountScan 判断这次扫码是否要新记一行事件。
//
// 规则：带访客键（=同一个人）且距上次不足窗口 → 不重复计；
// 不带访客键（落地页被打开但访客还没领身份）→ 只能照记，
// 因为没有可代表"同一个人"的依据可查——这一格天生带噪，
// 所以渠道之间比较应当看开口率/留资率，而不是只看打开次数（口径随响应下发）。
func ShouldCountScan(visitorKey string, lastSeen time.Time, now time.Time) bool {
	if strings.TrimSpace(visitorKey) == "" {
		return true
	}
	if lastSeen.IsZero() {
		return true
	}
	return now.Sub(lastSeen) >= ScanDedupeWindow
}

// ============================================================
// 归因裁决（纯函数：所有分支都可单测，不碰库）
// ============================================================

// 归因结果码（稳定字面量：响应、日志、冒烟三处按它分支，两侧都不要自造）
const (
	// ApplyOK 归因写入成功
	ApplyOK = "ok"
	// ApplyReasonAbsent 请求没带码（自然流量/老链接），不是错误
	ApplyReasonAbsent = "code_absent"
	// ApplyReasonMalformed 码形态非法（长度/字符不对），未查库即拒
	ApplyReasonMalformed = "code_malformed"
	// ApplyReasonNotFound 码不存在。**与 disabled 刻意共用同一响应形态**，
	// 不给外部试探"哪些码存在但被停了"的探测面
	ApplyReasonNotFound = "code_not_found"
	// ApplyReasonDisabled 码已停用：不再产生新归因
	ApplyReasonDisabled = "code_disabled"
	// ApplyReasonTenantMismatch 码属于别的租户：一律不写（跨租户污染的最后一道闸）
	ApplyReasonTenantMismatch = "code_tenant_mismatch"
	// ApplyReasonAlreadySet 客户身上已有首触码：首触归因不改写
	ApplyReasonAlreadySet = "already_attributed"
	// ApplyReasonStorageError 落库异常（DB 故障/连接问题）。与上面几个码分得很清楚：
	// 那几个是"这次请求本就不该记"，这个是"该记但没记上"——需要人工补，不能混在过去的原因里
	ApplyReasonStorageError = "storage_error"
)

// ApplyDecision 归因裁决结果（Reason 恒定是上面某个码）
type ApplyDecision struct {
	Apply  bool
	Reason string
}

// DecideApply 判断"这次扫码/建客能不能把码写进这个客户"。
//
// 参数拆开来看，每个分支都是一条独立的红线：
//   - tenantID 是**当前请求解析出的租户**，codeTenantID 是**码自己所属的租户**；
//     两者不等即拒——这条是整个公开链路最重要的判断，宁可丢归因也不能串家。
//   - alreadySet：首触归因是"谁把这个人带来的"，一旦成立就不再改（理由见 BuildLink 注释区）。
func DecideApply(rawCode string, codeFound, codeActive bool, codeTenantID, tenantID uint, alreadySet bool) ApplyDecision {
	norm := NormalizeCode(rawCode)
	if norm == "" {
		if strings.TrimSpace(rawCode) == "" {
			return ApplyDecision{Reason: ApplyReasonAbsent}
		}
		return ApplyDecision{Reason: ApplyReasonMalformed}
	}
	if !codeFound {
		return ApplyDecision{Reason: ApplyReasonNotFound}
	}
	if !codeActive {
		return ApplyDecision{Reason: ApplyReasonDisabled}
	}
	if codeTenantID != tenantID || tenantID == 0 {
		return ApplyDecision{Reason: ApplyReasonTenantMismatch}
	}
	if alreadySet {
		return ApplyDecision{Reason: ApplyReasonAlreadySet}
	}
	return ApplyDecision{Apply: true, Reason: ApplyOK}
}

// ============================================================
// 渠道位
// ============================================================

// 渠道位取值与 Customer.Source 同源（见 model/customer.go 的 sourceCode 映射），
// 不另起一套枚举，否则扫码客户的"来源"列与渠道位会各说一遍、迟早对不上。
const (
	ChannelDouyin   = "抖音"
	ChannelXiaohong = "小红书"
	ChannelWechat   = "微信"
	ChannelBaidu    = "百度"
	ChannelStore    = "门店自然"
	ChannelReferral = "老客转介绍"
	ChannelOther    = "其它"
)

// ChannelCodes 渠道位可选值（**下拉列表与校验共用这一份**：前端渲染什么、后端认什么，永远同集合。
// 校验里另写一遍字面量的话，加一个渠道就会出现"下拉能选、提交报 channel_unknown"）
var ChannelCodes = []string{ChannelDouyin, ChannelXiaohong, ChannelWechat, ChannelBaidu, ChannelStore, ChannelReferral, ChannelOther}

// MaxNameRunes 用途名长度上限（按字素数计，"门店前台立牌-春季车展"远够用）。
const MaxNameRunes = 40

// ValidateCreate 建码入参校验，返回稳定原因码（""=通过）。
// 与触达批同一口径：**拒绝要能被前端和冒烟按码分支**，不要只回一句中文提示。
func ValidateCreate(name, channel string) string {
	n := strings.TrimSpace(name)
	if n == "" {
		return "name_required"
	}
	if len([]rune(n)) > MaxNameRunes {
		return "name_too_long"
	}
	if channel == "" {
		return "channel_required"
	}
	for _, ok := range ChannelCodes {
		if channel == ok {
			return ""
		}
	}
	return "channel_unknown"
}

// ============================================================
// 漏斗判定（与下钻名单同源的唯一谓词，口径同 D4）
// ============================================================

// 漏斗指标码。**只有客户级指标可下钻**：
// scans（扫码次数）单位是"次"，与"哪些人"不同量纲，硬点开就会出现
// "数字 500、名单 3 行"的自相矛盾——这是 D4 立下的单位纪律，本批直接沿用。
const (
	MetricScans   = "scans"   // 扫码次数（事件级，不可下钻）
	MetricNew     = "new"     // 新增客户（客户级）
	MetricSpoke   = "spoke"   // 开过口（客户级）
	MetricLead    = "lead"    // 已留资及之后
	MetricArrived = "arrived" // 已到店及之后
	MetricOrdered = "ordered" // 已成交及之后
)

// FunnelCustomer 一个客户在本码归因下的漏斗判定输入。
// Spoke 来自消息表（该客户在本租户下是否发过至少一条消息），
// Stage 是客户当前旅程阶段——与 D4 一样，看的是 CRM 现状而非事件时刻。
type FunnelCustomer struct {
	CustomerID uint
	Spoke      bool
	Stage      string
}

// MatchFunnelMetric 唯一漏斗谓词。
//
// 阶段集合直接复用 analytics 的 LeadStages/ArrivedStages/OrderedStages：
// "什么算已留资"这个问题全项目只准有一个答案，贡献度看板和活码看板答得不一样，
// 就是给老板摆了两张互相打脸的表。
func MatchFunnelMetric(metric string, c FunnelCustomer) bool {
	switch metric {
	case MetricNew:
		return true // 归因到这个码的客户，本身就是"新增客户"
	case MetricSpoke:
		return c.Spoke
	case MetricLead:
		return analytics.InStageSet(c.Stage, analytics.LeadStages)
	case MetricArrived:
		return analytics.InStageSet(c.Stage, analytics.ArrivedStages)
	case MetricOrdered:
		return analytics.InStageSet(c.Stage, analytics.OrderedStages)
	}
	return false
}

// FunnelMetricCodes 可下钻的客户级指标（顺序即展示顺序；scans 刻意不在其中）
var FunnelMetricCodes = []string{MetricNew, MetricSpoke, MetricLead, MetricArrived, MetricOrdered}

// FunnelMetricLabels 指标码 → 中文口径名（随响应下发，前端不复写第二套文案）
var FunnelMetricLabels = map[string]string{
	MetricScans:   "扫码次数",
	MetricNew:     "新增客户",
	MetricSpoke:   "开过口",
	MetricLead:    "已留资",
	MetricArrived: "已到店",
	MetricOrdered: "已成交",
}

// ClassifyFunnelCounts 按同一谓词累加客户级计数（**必须**调 MatchFunnelMetric，
// 谓词与计数分开维护就是"数字与名单不一致"的标准成因——D4 的同一条教训）。
func ClassifyFunnelCounts(cs []FunnelCustomer) map[string]int64 {
	out := map[string]int64{}
	for _, m := range FunnelMetricCodes {
		out[m] = 0
	}
	for _, c := range cs {
		for _, m := range FunnelMetricCodes {
			if MatchFunnelMetric(m, c) {
				out[m]++
			}
		}
	}
	return out
}
