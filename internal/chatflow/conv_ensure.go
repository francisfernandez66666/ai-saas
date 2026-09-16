// conv_ensure.go G1 收口(2026-09-16C，AUDIT_GAP_REALITY_2026-09-16)：
// 活跃会话唯一性的统一创建入口。
//
// 背景：此前只有 web 主入口 chat_main 接了 Redis 跨实例短锁，guest 欢迎/免登录测试/
// 顾问首发/通道入站/OpenAPI 五路各自"先查后插"，多实例并发首条消息可各建一条 active
// 会话（OneID 重复会话事故的根因之一）；迁移 013 又给 (tenant_id, customer_id) 的部分
// 唯一索引兜了底——本助手统一三条纪律：
//  1. 先查复用（updated_at 最新一条 active）；
//  2. miss 后 Redis 短锁裁决 + 持锁复查（与 chat_main:149 同款，锁键一致互踩为零）；
//     拿不到锁=他实例在途，短暂等待后复查；
//  3. 插入冲突（唯一索引兜底命中）不当错误——复查返回对方建好的会话。
//
// C7 红线合规：本助手走 db.DB（无请求 ctx 的通道/后台也能用），Create 显式设 TenantID，
// 查询强制带 tenant_id 条件，绝不依赖盖章回调。
package chatflow

import (
	"errors"
	"log"
	"strconv"
	"time"

	"gorm.io/gorm"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/redisclient"
)

// EnsureActiveConversation 取该客户当前活跃会话，无则按上述纪律创建（幂等，可并发调用）。
// 返回 (会话, 是否本次新建)。tune 在创建前对会话对象做最后修饰（Mode/SessionID/Channel 等），
// 不得改 CustomerID/TenantID。查询/创建失败返回 err，调用方决定降级语义。
func EnsureActiveConversation(tenantID, customerID uint, assignedUserID uint, tune func(*model.Conversation)) (model.Conversation, bool, error) {
	find := func() (model.Conversation, bool) {
		var conv model.Conversation
		err := db.DB.Where("tenant_id = ? AND customer_id = ? AND status = ?", tenantID, customerID, "active").
			Order("updated_at DESC").First(&conv).Error
		if err == nil {
			return conv, true
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) && err != nil {
			return conv, false
		}
		return conv, false
	}

	if conv, ok := find(); ok {
		return conv, false, nil
	}

	// Redis 短锁裁决（与 chat_main 冷启动同键，两路并发首消息互斥）；未启用 Redis 时维持单机语义直建
	if redisclient.IsEnabled() {
		h := redisclient.TryLock(redisConvLockKey(customerID), 8*time.Second)
		if h == nil {
			// 他实例在途：等 500ms 复查一次，仍未命中再尝试自建（唯一索引+冲突复查兜底）
			time.Sleep(500 * time.Millisecond)
			if conv, ok := find(); ok {
				return conv, false, nil
			}
		} else {
			defer h.Unlock()
			if conv, ok := find(); ok {
				return conv, false, nil // 持锁复查命中：对方已建好
			}
		}
	}

	conv := model.Conversation{
		TenantID:       tenantID,
		CustomerID:     customerID,
		AssignedUserID: assignedUserID,
		Status:         "active",
		Mode:           "ai",
	}
	if tune != nil {
		tune(&conv)
	}
	if err := db.DB.Create(&conv).Error; err != nil {
		// 唯一索引冲突（多实例/双路径竞态）：复查拿到对方那条即视为成功，非错误
		if found, ok := find(); ok {
			log.Printf("[会话保障] 客户%d 创建撞唯一约束，复用并发方会话%d", customerID, found.ID)
			return found, false, nil
		}
		return conv, false, err
	}
	return conv, true, nil
}

// redisConvLockKey 活跃会话创建锁键——与 chat_main.go 冷启动路径保持同一键式。
func redisConvLockKey(customerID uint) string {
	return "conv:create:" + strconv.FormatUint(uint64(customerID), 10)
}
