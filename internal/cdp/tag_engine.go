package cdp

import (
	"ai-scrm/internal/model"
	"log"
)

// ============================================================
// CDP 标签引擎（SAAS_PLAN §十六 · 四维标签体系）
//
// 职责边界：
//   - 基于 user_event 事件流计算四维原子标签（身份/行为/环境/态度）
//   - 态度维严禁从行为硬推凑数（动态问卷/NLP语义/零方数据）
//   - 标签赋值落 cdp_tag_assignments，标签字典见 cdp_tag_definitions
//
// ⚠️ 当前状态：P4 待真实化——本文件为接口占位空壳，
//    真实实现需消费 internal/mq 的 user_event 主题后驱动计算；
//    业务系统禁止直写 CDP 表（写收口于 IngestConsumer 消费者）
// ============================================================

// TagEngine 标签引擎接口
type TagEngine interface {
	AutoAssignTags(uint) error                              // 事件驱动：按客户自动计算并赋标签
	GetCustomerTags(uint) ([]model.CdpTagAssignment, error) // 查询客户的全部生效标签
}

// NewTagEngine 创建标签引擎
func NewTagEngine() TagEngine {
	return &tagEngine{}
}

// tagEngine 标签引擎实现（P4 占位）
type tagEngine struct{}

// AutoAssignTags 自动分配标签
// TODO(P4)：接入事件消费链路 → 四维原子标签计算 → 落 cdp_tag_assignments
func (e *tagEngine) AutoAssignTags(customerID uint) error {
	log.Printf("[CDP] Auto assign tags customer=%d", customerID)
	return nil
}

// GetCustomerTags 获取客户标签
// TODO(P4)：查询 cdp_tag_assignments（含未过期过滤与标签字典联查）
func (e *tagEngine) GetCustomerTags(customerID uint) ([]model.CdpTagAssignment, error) {
	log.Printf("[CDP] Get tags customer=%d", customerID)
	return nil, nil
}
