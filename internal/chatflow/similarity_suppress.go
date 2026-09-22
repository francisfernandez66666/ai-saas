// 相似消息抑制共用实现（A2 实装，2026-09-22 全量修复批·批四）：
// 这段"客户连发近似消息 → 只答一次"的判定此前只存在于 web 正式链（chat_main 内联 60 行），
// 免登录 C 端链（chat_unauthorized，即 /chat/unauthorized 正式客户入口）完全没有它：
// 同一客户 3 秒内连发两句近似话，正式链合并成一次回答、C 端链各答一遍，
// 体验与统计口径都不一致（批五择臂要求同状态同样本可比，故先收口）。
//
// 纪律沿用 P2-1（2026-09-20 批三）：抑制**只在确有在途批次会回答时**才成立——
// inflight 参数由调用方用 service.HasInflightBatch 探测后传入（chatflow 不得 import
// service，会成环），无在途批次一律不抑制、正常入队，绝不再出现"历史相似句静默丢答"。
package chatflow

import (
	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/runtimecfg"
	"log"
	"time"

	"gorm.io/gorm"
)

// SimilarMergeThreshold 关键词重叠度超过此值即判为"同一件事"。
// 与 CountSimilarQuestions 的重复提问阈值保持一致（0.5），两处口径不分叉。
const SimilarMergeThreshold = 0.5

// MergeSuppressLookback 相似抑制回看窗口 = 合并窗口×2，钳到 [2min, 10min]。
// 倍数为历史经验值（合并窗只覆盖"还在攒消息"的阶段，抑制要再多撑一轮才不重复答）。
func MergeSuppressLookback(tenantID uint) time.Duration {
	window := 25 // 默认合并窗秒数
	// GetIntForTenant 经 G3 修复后对 nil 服务安全（回落默认值），此处不再手写判空
	if v := runtimecfg.DefaultSystemConfigService.GetIntForTenant(tenantID, "merge_window_seconds", 25); v > 0 {
		window = v
	}
	d := time.Duration(window*2) * time.Second
	if d < 2*time.Minute {
		d = 2 * time.Minute
	}
	if d > 10*time.Minute {
		d = 10 * time.Minute
	}
	return d
}

// SimilarSuppressHit 一次相似抑制命中的留痕信息（落库 route_result 与日志共用）
type SimilarSuppressHit struct {
	PastMsgID   uint    // 被比对到的历史消息 ID
	PastContent string  // 历史消息原文（日志截断用）
	OverlapRate float64 // 关键词重叠度
}

// FindSimilarInflightMessage 在回看窗内找与 content 关键词重叠度 > SimilarMergeThreshold 的
// 最近两条客户消息之一。inflight=false 时直接不判命中（P2-1 护栏）。
// excludeMsgID 用于排除"本条消息已先入库"的场景（C 端链是先存后判）。
//
// gdb 由调用方给：正式链传 db.RQ(c)（带请求租户作用域，RLS_ENABLED=true 时才读得到），
// 无请求 ctx 的链路传 db.DB——本函数不猜作用域，避免"后台读不到 / RLS 下漏读"两类偏差。
func FindSimilarInflightMessage(gdb *gorm.DB, tenantID, customerID uint, content string, inflight bool, excludeMsgID uint) (SimilarSuppressHit, bool) {
	if customerID == 0 {
		return SimilarSuppressHit{}, false
	}
	currentKeywords := ExtractKeywords(content)
	if len(currentKeywords) == 0 {
		return SimilarSuppressHit{}, false
	}
	var recent []model.Message
	// 租户条件显式带上：本函数走 db.DB（后台/无 ctx 也可用），不依赖请求盖章作用域
	if err := db.DB.Where("tenant_id = ? AND customer_id = ? AND sender_type = ? AND created_at > ? AND id <> ?",
		tenantID, customerID, "customer", time.Now().Add(-MergeSuppressLookback(tenantID)), excludeMsgID).
		Order("id DESC").Limit(2).Find(&recent).Error; err != nil {
		// 查库失败按"不抑制"处理：宁可多答一句，也不能把客户的话吞掉
		log.Printf("[相似消息合并] 客户%d 历史消息查询失败，按不抑制处理: %v", customerID, err)
		return SimilarSuppressHit{}, false
	}
	for _, past := range recent {
		pastKeywords := ExtractKeywords(past.Content)
		if len(pastKeywords) == 0 {
			continue
		}
		overlap := 0
		for _, w := range currentKeywords {
			for _, pw := range pastKeywords {
				if w == pw {
					overlap++
					break
				}
			}
		}
		rate := float64(overlap) / float64(len(currentKeywords))
		if rate > SimilarMergeThreshold {
			hit := SimilarSuppressHit{PastMsgID: past.ID, PastContent: past.Content, OverlapRate: rate}
			if !inflight {
				// P2-1 护栏留痕：相似但不抑制——历史批次早已回完，这句没人接就等于丢答
				log.Printf("[相似消息合并] 客户%d 重叠度%.0f%%但无在途批次，不抑制、正常入队（P2-1）",
					customerID, rate*100)
				return SimilarSuppressHit{}, false
			}
			return hit, true
		}
	}
	return SimilarSuppressHit{}, false
}
