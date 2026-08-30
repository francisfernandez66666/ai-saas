// Package state_machine 流程状态机：实例进度、乐观锁、心跳、超时巡检回收
package state_machine

import (
	"context"
	"errors"
	"log"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/mq"
	"ai-scrm/internal/service"

	"gorm.io/gorm"
)

// ============================================================
// 状态机服务（SAAS_PLAN §十七）
// 职责边界：只记录流程实例进度；只被流程引擎读写；不对外暴露 HTTP
// 行为：读写 + version 乐观锁 + 心跳；巡检由 sweeper.go 负责
// ============================================================

// ErrVersionConflict 变量定义（自动补注释）。
var ErrVersionConflict = errors.New("状态机版本冲突（并发更新），请重试")

// Get 按流程实例ID读取状态机（不存在返回 nil, nil）
func Get(tenantID uint, flowInstanceID uint) (*model.FlowStateMachine, error) {
	var sm model.FlowStateMachine
	err := db.DB.Where("flow_instance_id = ? AND tenant_id = ?", flowInstanceID, tenantID).First(&sm).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &sm, nil
}

// Init 初始化状态机（幂等：已存在则返回现有记录）
func Init(tenantID uint, oneID string, flowInstanceID uint, startNode string) (*model.FlowStateMachine, error) {
	existing, err := Get(tenantID, flowInstanceID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}
	sm := &model.FlowStateMachine{
		TenantID:       tenantID,
		OneID:          oneID,
		FlowInstanceID: flowInstanceID,
		CurrentNode:    startNode,
		HeartbeatTS:    time.Now(),
		Status:         model.SMStatusRunning,
		Version:        1,
	}
	if err := db.DB.Create(sm).Error; err != nil {
		// 并发初始化竞争：回落到读取已有记录
		if again, e2 := Get(tenantID, flowInstanceID); e2 == nil && again != nil {
			return again, nil
		}
		return nil, err
	}
	return sm, nil
}

// Advance 推进状态机：写当前节点 + 自增版本（乐观锁）
// expectedVersion 为调用方读到的版本；冲突时返回 ErrVersionConflict 由流程引擎重试
func Advance(tenantID uint, flowInstanceID uint, currentNode string, expectedVersion int64, stateJSON string) error {
	res := db.DB.Model(&model.FlowStateMachine{}).
		Where("flow_instance_id = ? AND tenant_id = ? AND version = ?", flowInstanceID, tenantID, expectedVersion).
		Updates(map[string]interface{}{
			"current_node": currentNode,
			"state_json":   stateJSON,
			"heartbeat_ts": time.Now(),
			"version":      expectedVersion + 1,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrVersionConflict
	}
	return nil
}

// Heartbeat 心跳续期（长处理中防巡检误回收）
func Heartbeat(tenantID uint, flowInstanceID uint) error {
	return db.DB.Model(&model.FlowStateMachine{}).
		Where("flow_instance_id = ? AND tenant_id = ?", flowInstanceID, tenantID).
		Update("heartbeat_ts", time.Now()).Error
}

// Complete 标记完成
func Complete(tenantID uint, flowInstanceID uint) error {
	return db.DB.Model(&model.FlowStateMachine{}).
		Where("flow_instance_id = ? AND tenant_id = ?", flowInstanceID, tenantID).
		Updates(map[string]interface{}{"status": model.SMStatusCompleted, "heartbeat_ts": time.Now()}).Error
}

// SweepOnce 单轮巡检：将心跳超时的 running 实例重新入队
//
// 修复Bug3（2026-08-22）：
//   - 原实现对所有 waiting 实例每 60s 无限 requeue（waiting 是正常暂停语义，不是卡死）
//     且无任何消费者 → 实测半小时产生 30 条垃圾 flow_requeue 事件
//   - 止血：新增系统配置开关 sm_sweep_requeue_enabled（默认 false），
//     关闭时仅重置心跳 + 用 last_event_id 打标记防重复刷日志；
//     真正的断点续跑判定需编排层消费 flow_drive 后按节点语义实现（专项）
func SweepOnce(heartbeatTimeout time.Duration) int {
	deadline := time.Now().Add(-heartbeatTimeout)
	var stale []model.FlowStateMachine
	if err := db.DB.Where("status = ? AND heartbeat_ts < ?", model.SMStatusRunning, deadline).
		Limit(100).Find(&stale).Error; err != nil {
		log.Printf("[状态机巡检] 扫描失败: %v", err)
		return 0
	}
	requeueEnabled := isRequeueEnabled()
	requeued := 0
	for _, sm := range stale {
		if !requeueEnabled {
			// 开关关闭：只重置心跳防止反复扫描；首次发现才打日志（last_event_id 兼作已告警标记）
			if sm.LastEventID != "sweep_noticed" {
				log.Printf("[状态机巡检] 实例%d(租户%d) 心跳超时（waiting 语义，未启用requeue，仅记录）",
					sm.FlowInstanceID, sm.TenantID)
			}
			_ = db.DB.Model(&model.FlowStateMachine{}).
				Where("id = ?", sm.ID).
				Updates(map[string]interface{}{
					"heartbeat_ts":  time.Now(),
					"last_event_id": "sweep_noticed",
				}).Error
			continue
		}
		log.Printf("[状态机巡检] 实例%d(租户%d) 心跳超时，发 flow_drive 断点续跑", sm.FlowInstanceID, sm.TenantID)
		if err := mq.Publish(context.Background(), mq.TopicFlowDrive, sm.TenantID, sm.OneID, "flow_requeue",
			mq.FlowDriveEvent{
				InstanceID: sm.FlowInstanceID,
				TaskType:   "requeue",
				Payload:    map[string]any{"current_node": sm.CurrentNode, "last_event_id": sm.LastEventID},
			}); err != nil {
			log.Printf("[状态机巡检] flow_drive 发布失败: %v", err)
			continue // 发布失败不重置心跳，下轮重试
		}
		// 重置心跳防重复发布
		_ = db.DB.Model(&model.FlowStateMachine{}).
			Where("id = ?", sm.ID).
			Updates(map[string]interface{}{"heartbeat_ts": time.Now()}).Error
		requeued++
	}
	if len(stale) > 0 {
		log.Printf("[状态机巡检] 本轮扫描 %d 个超时实例，回收 %d 个", len(stale), requeued)
	}
	return requeued
}

// isRequeueEnabled 读巡检 requeue 开关（默认 false，止血 Bug3）
func isRequeueEnabled() bool {
	if service.DefaultSystemConfigService == nil {
		return false
	}
	return service.DefaultSystemConfigService.GetBool("sm_sweep_requeue_enabled", false)
}
