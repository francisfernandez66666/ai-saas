// Package privacy — PIPL 删除权落地（C2，2026-09-12）
// 与商家侧"注销后数据保留"（法务资产）分轨：客户/用户主动申请删除 → 本包受理入队 → 到期日批匿名化。
// 匿名化口径：**只断可识别 PII 与自由文本，保留行数与数值统计**（intent_score/hooked/…）——
// 经营分析/模型反哺不失真，同时满足"内容列匿名"的 PIPL 删除权要求。幂等：重复执行/重复入队无副作用。
package privacy

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"gorm.io/gorm"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
)

// DefaultGrace 删除请求受理到执行的等待期（PIPL "15 个工作日内" 的落地：受理即 +15d）。
const DefaultGrace = 15 * 24 * time.Hour

// anonPrefix 匿名化后内容列前缀（幂等标志：已带前缀的内容列不再二次哈希）
const anonPrefix = "anon:"

// ErrAlreadyPending 同主体已有 pending 请求，重复受理幂等返回原请求（由调用方决定 200/409 语义）
var ErrAlreadyPending = errors.New("已有待处理的删除请求")

// Enqueue 受理一次删除请求。幂等：同 (tenant,scope,customer_id|user_id) 已有 pending → 返回原行 + ErrAlreadyPending。
// grace 传 <=0 走 DefaultGrace；测试可传小值触发到期。
func Enqueue(tenantID uint, scope string, customerID, userID uint, grace time.Duration) (*model.DeletionRequest, error) {
	if tenantID == 0 {
		return nil, errors.New("租户上下文缺失")
	}
	switch scope {
	case model.DeletionScopeCustomer:
		if customerID == 0 {
			return nil, errors.New("customer_id 必填")
		}
		// 越权防线：客户必须属于该租户
		var cu model.Customer
		if err := db.DB.Where("id = ? AND tenant_id = ?", customerID, tenantID).First(&cu).Error; err != nil {
			return nil, fmt.Errorf("客户不存在或不属于本租户: %w", err)
		}
	case model.DeletionScopeUser:
		if userID == 0 {
			return nil, errors.New("user_id 必填")
		}
		var u model.User
		if err := db.DB.Where("id = ? AND tenant_id = ?", userID, tenantID).First(&u).Error; err != nil {
			return nil, fmt.Errorf("用户不存在或不属于本租户: %w", err)
		}
	default:
		return nil, errors.New("scope 非法（应为 customer|user）")
	}
	if grace <= 0 {
		grace = DefaultGrace
	}
	// 幂等：先查同主体 pending 行
	q := db.DB.Where("tenant_id = ? AND scope = ? AND status = ?", tenantID, scope, model.DeletionStatusPending)
	if scope == model.DeletionScopeCustomer {
		q = q.Where("customer_id = ?", customerID)
	} else {
		q = q.Where("user_id = ?", userID)
	}
	var exist model.DeletionRequest
	if err := q.First(&exist).Error; err == nil {
		return &exist, ErrAlreadyPending
	}
	now := time.Now()
	row := &model.DeletionRequest{
		TenantID:    tenantID,
		Scope:       scope,
		CustomerID:  customerID,
		UserID:      userID,
		Status:      model.DeletionStatusPending,
		RequestedAt: now,
		Deadline:    now.Add(grace),
	}
	if err := db.DB.Create(row).Error; err != nil {
		return nil, fmt.Errorf("写入删除请求失败: %w", err)
	}
	return row, nil
}

// ExecuteDeletion 立即执行一条请求的匿名化，幂等：已 anonymized 的行原样返回。
func ExecuteDeletion(reqID uint) error {
	var req model.DeletionRequest
	if err := db.DB.First(&req, reqID).Error; err != nil {
		return err
	}
	if req.Status == model.DeletionStatusAnonymized {
		return nil // 幂等
	}
	return db.DB.Transaction(func(tx *gorm.DB) error {
		var err error
		switch req.Scope {
		case model.DeletionScopeCustomer:
			err = anonymizeCustomer(tx, req.TenantID, req.CustomerID)
		case model.DeletionScopeUser:
			err = anonymizeUser(tx, req.TenantID, req.UserID)
		default:
			err = fmt.Errorf("scope 非法: %s", req.Scope)
		}
		if err != nil {
			tx.Model(&model.DeletionRequest{}).Where("id = ?", req.ID).Updates(map[string]interface{}{
				"status": model.DeletionStatusFailed,
				"error":  truncate(err.Error(), 480),
			})
			return err
		}
		now := time.Now()
		return tx.Model(&model.DeletionRequest{}).Where("id = ?", req.ID).Updates(map[string]interface{}{
			"status":       model.DeletionStatusAnonymized,
			"processed_at": now,
			"error":        "",
		}).Error
	})
}

// ProcessExpired 日批：把到期(status=pending & deadline<=now)的请求逐条匿名化。返回处理数。
// 单实例串行由调用侧锁保证（多实例走 redisclient.TryLock，参考 main.go 通道 worker）。
func ProcessExpired() int {
	var due []model.DeletionRequest
	if err := db.DB.Where("status = ? AND deadline <= ?", model.DeletionStatusPending, time.Now()).
		Order("id ASC").Limit(500).Find(&due).Error; err != nil {
		log.Printf("[privacy] 扫描到期删除请求失败: %v", err)
		return 0
	}
	n := 0
	for _, r := range due {
		if err := ExecuteDeletion(r.ID); err != nil {
			log.Printf("[privacy] 执行删除请求 id=%d 失败: %v", r.ID, err)
			continue
		}
		n++
	}
	if n > 0 {
		log.Printf("[privacy] 日批匿名化 %d 条删除请求", n)
	}
	return n
}

// ---------- 内部匿名化实现 ----------

// anonymizeCustomer 客户主体：断 messages.content（哈希化，保行数与数值列）+ customers PII 列 + cdp profile_data。
// 数值统计列（intent_score/hooked/…）不动，保证经营分析与模型反哺不失真。
func anonymizeCustomer(tx *gorm.DB, tenantID, customerID uint) error {
	// 1. 消息：content 哈希化（已 anon: 前缀跳过——幂等）
	var msgs []model.Message
	if err := tx.Where("tenant_id = ? AND customer_id = ?", tenantID, customerID).
		Select("id, content").Find(&msgs).Error; err != nil {
		return err
	}
	for _, m := range msgs {
		if m.Content == "" || strings.HasPrefix(m.Content, anonPrefix) {
			continue
		}
		sum := sha256.Sum256([]byte(m.Content))
		newContent := anonPrefix + hex.EncodeToString(sum[:])
		if err := tx.Model(&model.Message{}).Where("id = ?", m.ID).Update("content", newContent).Error; err != nil {
			return err
		}
	}
	// 2. 客户：清 PII 列，保留统计列/阶段
	if err := tx.Model(&model.Customer{}).Where("id = ? AND tenant_id = ?", customerID, tenantID).
		Updates(map[string]interface{}{
			"name":             "",
			"phone":            "",
			"wechat_id":        "",
			"visitor_key":      "",
			"external_user_id": "",
			"remark":           anonPrefix, // 备注是自由文本 → 直接置空串前缀（长度短，二次执行幂等）
		}).Error; err != nil {
		return err
	}
	// 3. CDP 画像数据：整体清空 profile_data（数值 event_count 在事件表，不在这里）
	if err := tx.Model(&model.CdpProfile{}).Where("tenant_id = ? AND customer_id = ?", tenantID, customerID).
		Updates(map[string]interface{}{
			"profile_data": "{}",
			"profile_name": "",
		}).Error; err != nil {
		return err
	}
	return nil
}

// anonymizeUser 平台用户：清 tenant_users 三列 PII（email/phone/real_name）；username 保留（登录锚+审计锚，本身非敏感 PII）。
// 账号注销现有"次日禁登"逻辑保留（见 admin/account/cancel），本函数只做 PII 匿名化那一层。
func anonymizeUser(tx *gorm.DB, tenantID, userID uint) error {
	return tx.Model(&model.User{}).Where("id = ? AND tenant_id = ?", userID, tenantID).
		Updates(map[string]interface{}{
			"email":     "",
			"phone":     "",
			"real_name": "",
		}).Error
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
