// Package state_machine 流程状态机：实例进度、乐观锁、心跳、超时巡检回收
package state_machine

import (
	"ai-scrm/internal/runtimecfg"
	"context"
	"errors"
	"log"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/mq"

	"gorm.io/gorm"
)

// ============================================================
// 状态机服务（SAAS_PLAN §十七）
// 职责边界：只记录流程实例进度；只被流程引擎读写；不对外暴露 HTTP
// 行为：读写 + version 乐观锁 + 心跳；巡检由 sweeper.go 负责
// ============================================================

// ErrVersionConflict 乐观锁版本冲突错误：并发更新状态机时以此提示调用方重试
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

// sweepHeartbeatWriter FIX-7(2026-09-29 审计批二)：巡检里两处心跳/标记写入的单点，抽成包级
// 变量只为让单测能桩成"必失败"来验证写失败必出 WARN——吞错曾把"发布 requeue 后心跳没刷、
// 下轮会重发"（at-least-once，消费端需幂等）变成"没人知道"。发布语义不变。
var sweepHeartbeatWriter = func(id uint, fields map[string]interface{}) error {
	return db.DB.Model(&model.FlowStateMachine{}).Where("id = ?", id).Updates(fields).Error
}

// SweepOnce 单轮巡检：将心跳超时的 running 实例重新入队
//
// 修复Bug3（2026-08-22）：
//   - 原实现对所有 waiting 实例每 60s 无限 requeue（waiting 是正常暂停语义，不是卡死）
//     且无任何消费者 → 实测半小时产生 30 条垃圾 flow_requeue 事件
//   - 止血：新增系统配置开关 sm_sweep_requeue_enabled（默认 false），
//     关闭时仅重置心跳 + 用 last_event_id 打标记防重复刷日志；
//     真正的断点续跑判定需编排层消费 flow_drive 后按节点语义实现（专项）
//
// 终态收口（2026-10-13 修复批 P2-1 口径更正）：
//
//	实测全表 1021 行里有 598 行 status='running' AND current_node='end'，而其
//	flow_instances.status 早已是 completed——流程跑完了、状态机收尾却漏标。
//	成因是 engine.syncStateMachine 只在「读到的 instance.Status 恰好是 completed」
//	那一刻才调 Complete()，存在时序窗口；Advance() 又已把终态节点 'end' 写进了
//	current_node，于是这批行永久停在 running。
//	危害不在"脏"本身，而在它**吃掉了巡检的扫描配额**：扫描是 Limit(100) 且无序，
//	598 行终态脏数据把每分钟的 100 个名额占满，真正需要续跑的 current_node='ai_chat'
//	实例（实测超时集里仅 34/100）永远排不上，断点续跑能力等于事实关闭。
//	故本轮巡检先按 flow_instances 的真实状态收口终态脏数据，再谈 requeue：
//	既清掉积压，又把扫描窗口让给真正卡住的实例。
//	注意这不是"打开 sm_sweep_requeue_enabled"——那会对已终结实例发无意义的
//	flow_requeue，正是 Bug3 当初要压制的垃圾事件；开关默认仍为 false。
func SweepOnce(heartbeatTimeout time.Duration) int {
	deadline := time.Now().Add(-heartbeatTimeout)
	var stale []model.FlowStateMachine
	if err := db.DB.Where("status = ? AND heartbeat_ts < ?", model.SMStatusRunning, deadline).
		Limit(100).Find(&stale).Error; err != nil {
		log.Printf("[状态机巡检] 扫描失败: %v", err)
		return 0
	}
	// 先收口终态脏数据：这批行不该进 requeue 分支（流程已结束，续跑无意义），
	// 收口后它们 status 不再是 running，下轮自然不再被扫到，扫描配额随之释放。
	stale = terminalReconcile(stale)
	requeueEnabled := isRequeueEnabled()
	requeued := 0
	for _, sm := range stale {
		if !requeueEnabled {
			// 开关关闭：只重置心跳防止反复扫描；首次发现才打日志（last_event_id 兼作已告警标记）
			if sm.LastEventID != "sweep_noticed" {
				log.Printf("[状态机巡检] 实例%d(租户%d) 心跳超时（waiting 语义，未启用requeue，仅记录）",
					sm.FlowInstanceID, sm.TenantID)
			}
			// FIX-7(2026-09-29)：写失败不再吞——刷不上心跳+标记意味着下轮还会重扫重打这条日志，
			// 必须看得见（原先静默时表现为"同一实例反复超时告警"却查不出为什么没被压下去）
			if err := sweepHeartbeatWriter(sm.ID, map[string]interface{}{
				"heartbeat_ts":  time.Now(),
				"last_event_id": "sweep_noticed",
			}); err != nil {
				log.Printf("[状态机巡检] 实例%d 心跳/标记写入失败: %v —— 下轮将重新扫描该实例", sm.FlowInstanceID, err)
			}
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
		// FIX-7(2026-09-29)：发布成功但心跳没刷上 = 下轮会对同一实例再发一条 flow_requeue。
		// 这是 at-least-once 的既定语义（发布语义不动），但必须 WARN 出声，消费端需幂等。
		if err := sweepHeartbeatWriter(sm.ID, map[string]interface{}{"heartbeat_ts": time.Now()}); err != nil {
			log.Printf("[状态机巡检] 实例%d 心跳未刷新: %v —— 下轮将重发（消费端需幂等）", sm.FlowInstanceID, err)
		}
		requeued++
	}
	if len(stale) > 0 {
		log.Printf("[状态机巡检] 本轮扫描 %d 个超时实例，回收 %d 个", len(stale), requeued)
	}
	return requeued
}

// isRequeueEnabled 读巡检 requeue 开关（默认 false，止血 Bug3）
func isRequeueEnabled() bool {
	if runtimecfg.DefaultSystemConfigService == nil {
		return false
	}
	return runtimecfg.DefaultSystemConfigService.GetBool("sm_sweep_requeue_enabled", false)
}

// terminalInstanceStatuses 流程实例的终态取值（model.FlowInstance.Status 的注释自陈：
// running/completed/suspended）。只有 completed 视为"已跑完"——suspended 是人工挂起，
// 不是终态，挂起的实例仍应保留原状态等人来恢复，不能被巡检顺手标完成。
var terminalInstanceStatuses = []string{"completed"}

// terminalReconcile 把「状态机还停在 running、但流程实例其实早已 completed」的脏数据收口。
//
// 为什么必须做：engine.syncStateMachine 只在读到的 instance.Status 恰为 completed 的那一刻
// 才调 Complete()，存在时序窗口漏标；Advance() 已把终态节点 'end' 写进 current_node，
// 于是这批行永久停在 running。实测全表 598/1021 行如此。扫描是 Limit(100) 且无序，
// 它们每轮吃掉全部名额，真正卡住的实例永远进不了窗口——断点续跑等于事实关闭。
//
// 三条实现约束（都是踩过的坑，故写进注释防止后人"简化"）：
//  1. **不逐行查库**：一次 Where(flow_instance_id IN ?) 批量联查，否则 100 行 = 100 次
//     往返，巡检从每分钟一次变成每分钟一百次（实测扫描日志已稳定 60s 一轮，不能拖垮它）。
//  2. **租户条件不可省**：flow_instance_id 非全局唯一（无租户过滤就是跨租户数据污染，
//     本包旧注释自己承认过这个坑），IN 查询与 UPDATE 都必须带 tenant_id。
//  3. **收口失败必出声**：UPDATE 影响行数不足说明并发已被别人改过，属正常竞态不告警；
//     但 DB 报错必须 WARN——静默失败会让这批行永久滞留、悄悄吃扫描配额，
//     正是本函数存在的目的被无声撤销。
func terminalReconcile(stale []model.FlowStateMachine) []model.FlowStateMachine {
	if len(stale) == 0 {
		return stale
	}
	ids := make([]uint, 0, len(stale))
	byInstID := make(map[uint][]model.FlowStateMachine, len(stale))
	for _, sm := range stale {
		// 便宜的本地预筛：只有终态节点才可能是收口对象，省掉绝大多数行的联查
		if sm.CurrentNode != "end" {
			continue
		}
		ids = append(ids, sm.FlowInstanceID)
		byInstID[sm.FlowInstanceID] = append(byInstID[sm.FlowInstanceID], sm)
	}
	if len(ids) == 0 {
		return stale
	}

	var done []model.FlowInstance
	if err := db.DB.Model(&model.FlowInstance{}).
		Select("id, tenant_id, status").
		Where("id IN ? AND status IN ?", ids, terminalInstanceStatuses).
		Find(&done).Error; err != nil {
		// 查不到就当没有可收口的：宁可不收口走原 requeue 分支，也不要把整批行当脏数据误标完成
		log.Printf("[状态机巡检] 终态收口联查失败（%d 个候选实例本轮跳过收口）: %v", len(ids), err)
		return stale
	}

	// 收集真正已完成的 (实例ID, 租户ID) 对，逐个精确收口
	type pair struct{ instID, tenantID uint }
	targets := make([]pair, 0, len(done))
	for _, fi := range done {
		targets = append(targets, pair{fi.ID, fi.TenantID})
	}
	if len(targets) == 0 {
		return stale
	}

	reconciled := make(map[pair]bool, len(targets))
	failed := 0
	for _, tg := range targets {
		key := pair{tg.instID, tg.tenantID}
		rows := byInstID[tg.instID]
		for _, sm := range rows {
			if sm.TenantID != tg.tenantID {
				continue // 实例ID跨租户撞号，只有租户一致的才是同一行
			}
			if err := sweepHeartbeatWriter(sm.ID, map[string]interface{}{
				"status":        model.SMStatusCompleted,
				"heartbeat_ts":  time.Now(),
				"last_event_id": "terminal_reconciled",
			}); err != nil {
				// 失败不静默：这行下轮还会被扫到、继续占名额，必须留可见痕迹
				log.Printf("[状态机巡检] 实例%d(租户%d) 终态收口写失败: %v —— 下轮重试",
					sm.FlowInstanceID, sm.TenantID, err)
				failed++
				continue
			}
			reconciled[key] = true
		}
	}

	if len(reconciled) > 0 || failed > 0 {
		log.Printf("[状态机巡检] 终态收口：%d 个实例已完成（流程早已跑完、状态机漏标）已补标 completed，%d 个写失败待重试；扫描配额随之释放给真正卡住的实例",
			len(reconciled), failed)
	}

	if len(reconciled) == 0 {
		return stale
	}
	// 把已收口的行从待处理集合里剔除：它们不该再进 requeue/重置心跳分支
	remaining := stale[:0]
	for _, sm := range stale {
		if reconciled[pair{sm.FlowInstanceID, sm.TenantID}] {
			continue
		}
		remaining = append(remaining, sm)
	}
	return remaining
}
