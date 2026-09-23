// Package notify 商业化外呼触达（D3，2026-09-23）：用量预警与到期催缴的租户侧邮件。
//
// 与 tenant_notify.go（到期 7d/3d 提醒）的分工：那张文件管"套餐到期"这一个事件，
// 本文件管"钱不够用了"（用量越档）与"已经没钱了该续费了"（催缴逐档）。
// 三者共用同一套收件人解析与 SMTP 降级口径——收件人只取**该租户启用状态的 tenant_admin**，
// 不发 super_admin（平台身份，按租户重复发会变成轰炸），也不发销售账号（无续费决策权）。
//
// 为什么这些函数返回 bool 而 tenant_notify.go 里的旧函数不返回：
// 调用方（billing 的 sweep）要把"到底发出去了没有"落进 usage_alerts.channels 与指标里，
// 否则 SMTP 未配置时会留下一条"已通知"的假记录，运营看到记录就不再去人工补位，
// 客户实际一封信都没收到——这类"记了但没做"的账在本仓已按静默吞账处理过（M1 教训）。
// 返回 true 的口径是"至少有一条真实通道进入发送流程"，异步失败仍只记日志（旁路不阻塞巡检）。
// （本文件头说明与 package 声明之间留一空行：包文档注释已由 tenant_notify.go 承担，
// 此处再写一份会成重复包注释。）

package notify

import (
	"fmt"
	"log"
	"strings"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/runtimecfg"
)

// TenantAdminEmails 取该租户全部启用状态 tenant_admin 的绑定邮箱（去重、去空串）。
// 从 TenantExpiringEmail 内联查询抽成公共函数（D3 批）：三条外呼链路收件人口径必须一致，
// 复制两份 SQL 迟早一份加了 status=1 过滤、一份忘了——发给已停用账号是投诉源。
func TenantAdminEmails(tenantID uint) []string {
	var emails []string
	if err := db.DB.Model(&model.User{}).
		Where("tenant_id = ? AND role = ? AND status = 1 AND email <> ''",
			tenantID, model.RoleTenantAdmin).
		Pluck("email", &emails).Error; err != nil {
		log.Printf("[商业触达] 查租户%d管理员邮箱失败: %v", tenantID, err)
		return nil
	}
	seen := make(map[string]bool, len(emails))
	out := make([]string, 0, len(emails))
	for _, e := range emails {
		e = strings.TrimSpace(e)
		if e == "" || seen[e] {
			continue
		}
		seen[e] = true
		out = append(out, e)
	}
	return out
}

// SMTPReady 邮件通道是否可用（Host/User 任一为空即视为未配置）。
// 供 sweep 在"根本发不出去"时直接记 skipped，不占用一次 goroutine 与一条失败日志。
func SMTPReady() bool {
	s := NewSMTPSenderFromEnv()
	return s != nil && s.Host != "" && s.User != ""
}

// GroupChannelReady 平台群通知是否至少配了一条通道（企微/钉钉任一）。
// 与 SMTPReady 同用于"sweep 前先判通道在不在"：两条都没配时，
// 预警会全部命中却全部发不出去，此时宁可留 skipped 指标也别写"已通知"的假留痕。
func GroupChannelReady() bool {
	if runtimecfg.DefaultSystemConfigService == nil {
		return false
	}
	return runtimecfg.DefaultSystemConfigService.GetString("wecom_webhook_url", "") != "" ||
		runtimecfg.DefaultSystemConfigService.GetString("dingtalk_webhook_url", "") != ""
}

// sendToTenantAdmins 通用外呼发送：解析收件人 → 逐封异步投 → 返回是否至少发出一封。
// 收件人为空或 SMTP 未配置一律返回 false，并打一行可定位日志（含脱敏收件人数）。
func sendToTenantAdmins(tenantID uint, tenantName, code, subject, body, tag string) bool {
	emails := TenantAdminEmails(tenantID)
	if len(emails) == 0 {
		log.Printf("[商业触达] 租户%s(%s) 无绑定邮箱的管理员，%s邮件未发（仅群/后台留痕）", tenantName, code, tag)
		return false
	}
	s := NewSMTPSenderFromEnv()
	if s.Host == "" || s.User == "" {
		log.Printf("[商业触达] SMTP 未配置，租户%d %s邮件降级日志（%d 个收件人）", tenantID, tag, len(emails))
		return false
	}
	go func() {
		// 邮件是旁路触达：任何 panic 都不能带走小时巡检（与到期提醒同一处置）
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[商业触达] %s 发送 panic 已捕获: %v", tag, r)
			}
		}()
		for _, e := range emails {
			if err := s.SendRaw([]string{e}, subject, body); err != nil {
				log.Printf("[商业触达] %s 发送失败 to=%s: %v", tag, MaskEmailForLog(e), err)
			} else {
				log.Printf("[商业触达] %s 已发 to=%s tenant=%d", tag, MaskEmailForLog(e), tenantID)
			}
		}
	}()
	return true
}

// TenantUsageAlertEmail 用量预警邮件：告诉租户管理员"贵司本月 AI 用量已到 X%，还剩 Y"。
// metricLabel 是人话口径（如"本月 AI 调用次数配额"），remaining 由调用方按指标口径换算好传入。
// 文案铁律（对齐 AGENTS 去 AI 味约定）：短句、无敬语堆叠、不出现内部术语（桶/档位/阈值）。
func TenantUsageAlertEmail(tenantID uint, tenantName, code, metricLabel string, pct int, remaining int64, exhausted bool) bool {
	subject := fmt.Sprintf("【LexCross 跨山】贵企业空间%s已用 %d%%", metricLabel, pct)
	body := fmt.Sprintf(
		"%s（企业码 %s）：\n\n%s已用 %d%%，剩余 %s。\n用量耗尽后 AI 会话会自动降级为规则话术，历史数据与客户不受影响。\n\n如需扩大额度，请登录控制台「账单」页购买增量包或升级套餐。\n若已有扩容计划请忽略本邮件。\n\n—— LexCross 跨山 · AI SCRM",
		tenantName, code, metricLabel, pct, formatRemaining(metricLabel, remaining))
	if exhausted {
		// 100% 档单独一版文案：此刻"剩余 0"再说"快用完了"已经晚了，直接给下一步动作
		subject = fmt.Sprintf("【LexCross 跨山】贵企业空间%s已耗尽", metricLabel)
		body = fmt.Sprintf(
			"%s（企业码 %s）：\n\n%s已耗尽。\n当前 AI 会话会降级为规则话术回复，客户侧不会中断，但智能应答已停用。\n\n续费或购买增量包后立即恢复，无需重新配置。\n请登录控制台「账单」页处理。\n\n—— LexCross 跨山 · AI SCRM",
			tenantName, code, metricLabel)
	}
	return sendToTenantAdmins(tenantID, tenantName, code, subject, body, "用量预警")
}

// DunningEmailInput 催缴邮件入参（用结构体而不是五个 bool 位置参数：
// 这封信的三态文案极易传错位，调用点必须靠字段名自证）。
type DunningEmailInput struct {
	TenantID    uint
	Name        string     // 租户名
	Code        string     // 企业码
	Stage       int        // 第几档（从 1 计）
	DayPast     int        // 到期后第几天（0=当天）
	DueAt       time.Time  // 到期时刻
	GraceEnd    *time.Time // 宽限期截止（nil=不封禁，只催）
	WillSuspend bool       // 尚未封禁，但宽限期末会停登录
	Suspended   bool       // 已因欠费停登录（本序列施加）
}

// TenantDunningEmail 到期催缴邮件：第 Stage 档，距到期 DayPast 天。
//
// 三态文案的口径必须与真实行为一致（发错一次就是恐吓邮件）：
//
//	expired（到期）    = 写侧 402（新建会话/接管等动作停），登录与读仍放行 —— ExpireCheck 既有语义；
//	suspended（宽限期末）= TenantResolver 全拦 403，连登录都进不去。
//
// 所以"停止登录"一句只出现在 Suspended/WillSuspend 两支，前面的档位只说"新建与 AI 应答已停"。
// 把封禁规则提前说清楚，比封完之后让客户去猜"为什么登不进去"省一次客服工单，也是欠费争议的免责留痕。
func TenantDunningEmail(in DunningEmailInput) bool {
	subject := fmt.Sprintf("【LexCross 跨山】贵企业空间已到期%d天，续费即可恢复", in.DayPast)
	if in.DayPast <= 0 {
		subject = "【LexCross 跨山】贵企业空间今日到期，续费即可继续"
	}
	if in.Suspended {
		subject = "【LexCross 跨山】贵企业空间已停止登录，续费后立即恢复"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s（企业码 %s）：\n\n", in.Name, in.Code)
	switch {
	case in.Suspended:
		fmt.Fprintf(&b, "您的 LexCross 工作空间已于 %s 到期，因超过宽限期未续费，现已停止登录（第 %d 次通知）。\n",
			in.DueAt.Format("2006-01-02 15:04"), in.Stage)
		b.WriteString("客户资料、历史会话与配置全部保留，续费到账后原样恢复，无需迁移或重新配置。\n")
	case in.DayPast <= 0:
		fmt.Fprintf(&b, "您的 LexCross 工作空间已于 %s 到期，新建会话与 AI 应答已停，历史数据完整保留。\n",
			in.DueAt.Format("2006-01-02 15:04"))
	default:
		fmt.Fprintf(&b, "您的 LexCross 工作空间已于 %s 到期，至今已过期 %d 天（第 %d 次提醒）。\n",
			in.DueAt.Format("2006-01-02 15:04"), in.DayPast, in.Stage)
	}
	if in.GraceEnd != nil && !in.GraceEnd.IsZero() && !in.Suspended {
		fmt.Fprintf(&b, "宽限期至 %s。", in.GraceEnd.Format("2006-01-02"))
		if in.WillSuspend {
			b.WriteString("届时若仍未续费，整个空间会停止登录，客户资料与历史会话仍然保留。\n")
		} else {
			b.WriteString("在此之前您随时可以登录控制台完成续费。\n")
		}
	}
	b.WriteString("\n续费到账后立即恢复，无需迁移或重新配置。\n请登录控制台「账单」页完成续费。\n如已安排付款或有其它情况，回复本邮件即可联系到您的客户成功经理。\n\n—— LexCross 跨山 · AI SCRM")
	return sendToTenantAdmins(in.TenantID, in.Name, in.Code, subject, b.String(), "到期催缴")
}

// formatRemaining 把剩余量按指标口径转成人话（次数 vs token 数）。
// token 一律万位缩写：管理员看"还剩 128 万"比"还剩 1284300"快一个数量级的判断。
func formatRemaining(metricLabel string, remaining int64) string {
	if remaining < 0 {
		remaining = 0
	}
	if strings.Contains(metricLabel, "token") || strings.Contains(metricLabel, "Token") {
		if remaining >= 10000 {
			return fmt.Sprintf("%.0f 万 tokens", float64(remaining)/10000)
		}
		return fmt.Sprintf("%d tokens", remaining)
	}
	return fmt.Sprintf("%d 次", remaining)
}
