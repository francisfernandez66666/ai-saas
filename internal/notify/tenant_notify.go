// Package notify 租户侧商业触达：到期/续费提醒直达租户管理员邮箱。
// 背景（2026-09-16 商业缺口批）：ExpireCheck 的 7d/3d 到期提醒此前只进平台企微群，
// 租户自身无任何感知——SaaS 续费漏斗断在"联系不到客户"。本文件补直达触达；
// 调用方（billing.ExpireCheck）已有按天分档审计去重，本函数只管尽力投递、失败不阻塞。
package notify

import (
	"fmt"
	"log"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
)

// TenantExpiringEmail 租户到期提醒邮件：发给该租户全部启用状态的 tenant_admin 绑定邮箱。
// SMTP 未配置（Host/User 为空）时静默降级为日志（与重置码通道同一装配口径）；异步投递不阻塞巡检。
// daysLeft<=0 走"已到期"文案（过期摘除时刻的补缴引导）。
func TenantExpiringEmail(tenantID uint, tenantName, code string, expiredAt time.Time, daysLeft int) {
	var emails []string
	// 只发租户管理员（super_admin 是平台身份，勿按租户重复轰炸）；status=1 启用用户
	if err := db.DB.Model(&model.User{}).
		Where("tenant_id = ? AND role = ? AND status = 1 AND email <> ''",
			tenantID, model.RoleTenantAdmin).
		Pluck("email", &emails).Error; err != nil {
		log.Printf("[到期触达] 查租户%d管理员邮箱失败: %v", tenantID, err)
		return
	}
	if len(emails) == 0 {
		log.Printf("[到期触达] 租户%s(%s) 无绑定邮箱的管理员，跳过邮件（仅群提醒）", tenantName, code)
		return
	}
	s := NewSMTPSenderFromEnv()
	if s.Host == "" || s.User == "" {
		log.Printf("[到期触达] SMTP 未配置，租户%d 到期邮件降级日志（%d 个收件人）", tenantID, len(emails))
		return
	}
	subject := fmt.Sprintf("【LexCross 跨山】贵企业空间将于 %d 天后到期", daysLeft)
	body := fmt.Sprintf(
		"%s（企业码 %s）：\n\n您的 LexCross 工作空间将于 %s 到期，剩余 %d 天。\n到期后新登录与 AI 会话将暂停，数据保留，续费到账后立即恢复。\n\n请登录控制台「账单」页完成续费。\n如已安排续费请忽略本邮件；有问题回复本邮件或联系您的客户成功经理。\n\n—— LexCross 跨山 · AI SCRM",
		tenantName, code, expiredAt.Format("2006-01-02 15:04"), daysLeft)
	if daysLeft <= 0 {
		// 摘除时刻口径：已过期，引导续费恢复
		subject = "【LexCross 跨山】贵企业空间已到期，续费即可恢复"
		body = fmt.Sprintf(
			"%s（企业码 %s）：\n\n您的 LexCross 工作空间已于 %s 到期，新登录与 AI 会话已暂停，数据完整保留。\n续费到账后立即恢复，无需迁移或重新配置。\n\n请登录控制台「账单」页完成续费。\n如已安排续费请忽略本邮件；有问题回复本邮件或联系您的客户成功经理。\n\n—— LexCross 跨山 · AI SCRM",
			tenantName, code, expiredAt.Format("2006-01-02 15:04"))
	}
	go func() {
		// 邮件通道无 panic 恢复先例，触达属旁路，崩了也不能带走巡检
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[到期触达] panic 已捕获: %v", r)
			}
		}()
		for _, e := range emails {
			if err := s.SendRaw([]string{e}, subject, body); err != nil {
				log.Printf("[到期触达] 发送失败 to=%s: %v", MaskEmailForLog(e), err)
			} else {
				log.Printf("[到期触达] 到期提醒邮件已发 to=%s tenant=%d", MaskEmailForLog(e), tenantID)
			}
		}
	}()
}
