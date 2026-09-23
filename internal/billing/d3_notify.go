// D3 外呼出口的可注入装配（2026-09-23）。
//
// 为什么要这一层间接：D3 的两个 sweep（用量预警 / 催缴）唯一的外部副作用就是"给真人发邮件、
// 往平台群打消息"。而本仓单测与开发库共用同一份 .env/系统配置——SMTP 凭证与企微 webhook
// 都是真值。若 sweep 直接调 notify.*，那么"跑一次 dunning 单测"就等于"给数据库里所有过期租户
// 的管理员群发催缴信 + 在真群里刷一屏【欠费停用】"。这不是假想事故：本地库里就躺着过期的种子/测试租户。
//
// 所以真实外呼统一收在这一个装配点，单测把 d3 换成计数桩（同包内直接替换 + t.Cleanup 还原），
// 生产路径仍然是 notify.* 原样。口径对齐 outreach.SendHook（同一动机：外呼副作用必须可摘除）。
package billing

import (
	"ai-scrm/internal/notify"
)

// d3Sink D3 触达出口集合（函数字段，非接口：只有 6 个动作，接口反而藏住调用点）
type d3Sink struct {
	// AdminEmails 该租户可用管理员邮箱（收件人解析，同时用于判"有没有人可以收"）
	AdminEmails func(tenantID uint) []string
	// SMTPReady 邮件通道是否配置就绪
	SMTPReady func() bool
	// GroupReady 平台群通道是否配置就绪（企微/钉钉任一）
	GroupReady func() bool
	// UsageEmail 发一封用量预警；返回是否至少进入一条真实通道
	UsageEmail func(tenantID uint, name, code, label string, pct int, remaining int64, exhausted bool) bool
	// DunningEmail 发一封催缴（三态文案由入参决定）
	DunningEmail func(in notify.DunningEmailInput) bool
	// Group 往平台群打一条
	Group func(content string)
}

// prodD3Sink 生产装配：全部直通 notify（真实外呼）
func prodD3Sink() d3Sink {
	return d3Sink{
		AdminEmails:  notify.TenantAdminEmails,
		SMTPReady:    notify.SMTPReady,
		GroupReady:   notify.GroupChannelReady,
		UsageEmail:   notify.TenantUsageAlertEmail,
		DunningEmail: notify.TenantDunningEmail,
		Group:        notify.NotifyGroup,
	}
}

// d3 当前生效的外呼装配（包级变量；单测替换后必须还原，见 withD3Sink）
var d3 = prodD3Sink()

// withD3Sink 临时替换外呼装配并返回还原函数（供单测/回归用，生产代码不调）
func withD3Sink(stub d3Sink) func() {
	old := d3
	d3 = stub
	return func() { d3 = old }
}

// d3UsageCall 一次用量预警外呼的入参快照（断言用）
type d3UsageCall struct {
	TenantID  uint
	Label     string
	Pct       int
	Remaining int64
	Exhausted bool
}

// d3CallLog 静默桩的调用台账：单测靠它回答"到底发了几封、发到哪一档、群里有没有响"。
type d3CallLog struct {
	usage   []d3UsageCall
	dunning []notify.DunningEmailInput
	groups  []string
	// admins 桩返回的管理员邮箱列表（nil/空 = 该租户没人可收，用于测"落空通道行"分支）
	admins []string
	// smtpReady/groupReady 桩设定的通道就绪态
	smtpReady  bool
	groupReady bool
}

// newSilentD3Sink 构造静默桩：只记账、不发出任何真实消息；返回值即"该封是否发出去了"，
// 由桩自身的 smtpReady/admins 决定，与生产判定逻辑同构（有 SMTP 且有收件人才算发出）。
func newSilentD3Sink(log *d3CallLog) d3Sink {
	return d3Sink{
		AdminEmails: func(uint) []string { return log.admins },
		SMTPReady:   func() bool { return log.smtpReady },
		GroupReady:  func() bool { return log.groupReady },
		UsageEmail: func(tid uint, _, _, label string, pct int, remaining int64, exhausted bool) bool {
			if !log.smtpReady || len(log.admins) == 0 {
				return false
			}
			log.usage = append(log.usage, d3UsageCall{
				TenantID: tid, Label: label, Pct: pct, Remaining: remaining, Exhausted: exhausted,
			})
			return true
		},
		DunningEmail: func(in notify.DunningEmailInput) bool {
			if !log.smtpReady || len(log.admins) == 0 {
				return false
			}
			log.dunning = append(log.dunning, in)
			return true
		},
		Group: func(content string) {
			if !log.groupReady {
				return
			}
			log.groups = append(log.groups, content)
		},
	}
}
