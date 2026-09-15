// Package archive 承载长周期数据冷热分层（2026-09-15 价值增强批）。
// 现状仅 messages→messages_archive 归档；分包红线：领域任务代码不进 internal/service。
package archive

import (
	"log"
	"strconv"
	"strings"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/runtimecfg"

	"gorm.io/gorm"
)

// 归档单批行数：小步快跑，避免一条巨型事务长时间持有 messages 热表的行锁。
const archiveBatchSize = 5000

// MaxRuntimePerRun 单轮归档最长执行时间（main ticker 用同一语义命名空间）
var MaxRuntimePerRun = 30 * time.Minute

// RunMessagesOnce 把 created_at 早于 message_archive_days 天的消息批量搬入 messages_archive。
// 开关：system_config message_archive_days（int，默认 0=关闭）——opt-in 设计，
// 没配就不碰线上任何一行数据；配置热更新，无需重启。
// 灰度范围：message_archive_tenants（逗号分隔租户ID，默认空=全租户）——首次上线建议
// 先圈一个大租户验证查询/备份效果，也保证单测只在测试租户内搬运不误伤共享库存量数据。
// 搬迁用单语句 CTE（DELETE...RETURNING → INSERT...SELECT）保证"移动"原子性，逐批提交；
// 重复行冲突不可能发生（同批先删后插，id 天然唯一）。返回累计归档行数。
func RunMessagesOnce() int {
	var days int
	var scopeStr string
	if runtimecfg.DefaultSystemConfigService != nil {
		days = runtimecfg.DefaultSystemConfigService.GetInt("message_archive_days", 0)
		scopeStr = runtimecfg.DefaultSystemConfigService.GetString("message_archive_tenants", "")
	}
	if days <= 0 {
		return 0
	}
	var tenantIDs []uint
	for _, s := range strings.Split(scopeStr, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		// ParseUint 严格解析，脏配置（非数字/0）静默跳过而非 panic
		if id, err := strconv.ParseUint(s, 10, 32); err == nil && id > 0 {
			tenantIDs = append(tenantIDs, uint(id))
		}
	}
	return runMessagesArchive(days, tenantIDs)
}

// runMessagesArchive 实际搬运：days 热数据保留窗，tenantIDs 空=全租户。单测直连此函数。
func runMessagesArchive(days int, tenantIDs []uint) int {
	cutoff := time.Now().AddDate(0, 0, -days)
	deadline := time.Now().Add(MaxRuntimePerRun)

	total := 0
	for {
		if time.Now().After(deadline) {
			log.Printf("[归档] 单轮超时收工，本轮已归档 %d 条（明日续跑）", total)
			break
		}
		var moved int64
		err := db.DB.Transaction(func(tx *gorm.DB) error {
			// 占位符统一用 ?（gorm 按出现顺序转 $n），参数顺序必须与 SQL 出现顺序一致
			tenantFilter := ""
			args := []interface{}{cutoff}
			if len(tenantIDs) > 0 {
				// ID 均经 ParseUint 严格解析（纯数字），内联字面量无注入面；
				// 不走 ANY(?)——pgx 对切片参数的二进制编码在 gorm 展开路径下不稳定
				parts := make([]string, 0, len(tenantIDs))
				for _, id := range tenantIDs {
					parts = append(parts, strconv.FormatUint(uint64(id), 10))
				}
				tenantFilter = "AND tenant_id IN (" + strings.Join(parts, ",") + ")"
			}
			args = append(args, archiveBatchSize)
			res := tx.Exec(`
				WITH moved AS (
					DELETE FROM messages
					WHERE id IN (
						SELECT id FROM messages
						WHERE created_at < ? `+tenantFilter+`
						ORDER BY created_at
						LIMIT ?
					)
					RETURNING *
				)
				INSERT INTO messages_archive
				SELECT *, now() FROM moved`, args...)
			if res.Error != nil {
				return res.Error
			}
			moved = res.RowsAffected
			return nil
		})
		if err != nil {
			log.Printf("[归档] messages 批次搬迁失败（下轮重试）：%v", err)
			break
		}
		if moved == 0 {
			break
		}
		total += int(moved)
	}
	if total > 0 {
		log.Printf("[归档] messages 归档完成 %d 条（保留近 %d 天热数据，范围=%v）", total, days, tenantIDs)
	}
	return total
}
