// 触达派发层：到点任务的合规裁决 + 复用通道出站队列投递 + 投递结果对账。
//
// 两段式而不是一次做完，是因为通道出站本身就是异步队列（channel:outbound ticker 3s 一轮）：
// 本包只负责"该不该发"和"发出去了没"，中间那段真实投递与退避重试归通道层，不重复造轮子。
//
//	① DispatchDue：pending →（开关/客户/通道/窗口四道裁决）→ queued（带 outbound_id）或 skipped；
//	② SyncResults：queued → 回读 channel_outbound 终态 → sent / failed。
//
// 频控只数 sent（见 service.countSentInWindow）：通道抖动导致的 queued 滞留不该永久吃掉客户额度。
package outreach

import (
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"ai-scrm/internal/metrics"
	"ai-scrm/internal/model"
)

// maxDispatchAttempts 入队阶段（DB 写入等瞬时故障）的累计尝试上限，
// 超过即判 failed 终态——否则一条永远入不了队的任务会每轮被重扫，把调度预算空转干净。
const maxDispatchAttempts = 5

// staleQueuedAfter queued 任务回不到终态多久算"投递记录丢了"（出站行被清理/人工删除）。
// 取 24h：出站队列最坏路径是 5 次指数退避（约 5min 间隔）+ 死信人工重发窗口，24h 足够走完。
const staleQueuedAfter = 24 * time.Hour

// SendHook 出站投递钩子：由 main.go 注入 channel.Enqueue 的包装。
// 做成变量而不是直接 import internal/channel：outreach 与通道层解耦后，
// 单测可注入替身验"裁决对不对"而无需真起通道，也避免领域包反向依赖投递实现。
// 未注入（nil）时 DispatchDue 整轮不动任务——宁可不发，也不把 pending 改成无法解释的中间态。
var SendHook func(tenantID, channelID, customerID, conversationID uint, content, msgType string) (uint, error)

// DispatchResult 一轮派发账目（供日志与 /metrics 观测）。
type DispatchResult struct {
	Scanned int
	Queued  int // 裁决通过并入站出站队列
	Skipped int // 合规/可达性拦下（终态，带 reason）
	Failed  int // 判失败终态
	Retried int // 瞬时故障留待下轮（未改变状态）
}

// dueTasks 取一批到点的 pending 任务（跨租户，按 scheduled_at 升序保发送顺序）。
func dueTasks(gdb *gorm.DB, now time.Time, limit int) ([]model.OutreachTask, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var rows []model.OutreachTask
	err := gdb.Where("status = ? AND scheduled_at <= ?", model.OutreachStatusPending, now).
		Order("scheduled_at ASC, id ASC").Limit(limit).Find(&rows).Error
	return rows, err
}

// DispatchDue 一轮派发：逐条裁决到点任务并投递到通道出站队列。
//
// gdb 由调用方给（main.go 传 db.DB；单测传自己的句柄）。租户归属一律显式按行内
// tenant_id 判定，不依赖请求 context（后台任务无 gin ctx，C7 红线）。
func DispatchDue(gdb *gorm.DB, now time.Time, limit int) (DispatchResult, error) {
	var res DispatchResult
	if SendHook == nil {
		return res, nil
	}
	// 一轮派发会对同一句柄发起多条查询（扫描/客户/身份/通道/来句/回写），
	// 调用方若传派生句柄（如带租户 where 的 RQ 会话）必须复位，否则条件跨查询累加。
	gdb = isolate(gdb)
	rows, err := dueTasks(gdb, now, limit)
	if err != nil {
		return res, err
	}
	res.Scanned = len(rows)
	for i := range rows {
		task := rows[i]
		verdict, err := decideForDispatch(gdb, task, now)
		if err != nil {
			// 查库/入队类瞬时故障：只累加 attempts，状态保持 pending 下轮再试；
			// 到上限才判 failed，避免一次 DB 抖动把整批计划打成死档。
			res.Retried++
			if task.Attempts+1 >= maxDispatchAttempts {
				if uerr := markTerminal(gdb, task.ID, model.OutreachStatusFailed, "", err.Error()); uerr != nil {
					return res, uerr
				}
				res.Retried--
				res.Failed++
				metrics.IncOutreachFailed()
				continue
			}
			if uerr := gdb.Model(&model.OutreachTask{}).Where("id = ?", task.ID).
				Updates(map[string]any{"attempts": task.Attempts + 1, "updated_at": now}).Error; uerr != nil {
				return res, uerr
			}
			continue
		}
		switch verdict.kind {
		case verdictSkip:
			if err := markTerminal(gdb, task.ID, model.OutreachStatusSkipped, verdict.reason, ""); err != nil {
				return res, err
			}
			res.Skipped++
			metrics.IncOutreachSkipped()
		case verdictQueued:
			if err := gdb.Model(&model.OutreachTask{}).Where("id = ?", task.ID).
				Updates(map[string]any{
					"status":      model.OutreachStatusQueued,
					"channel_id":  verdict.channelID,
					"outbound_id": verdict.outboundID,
					"attempts":    task.Attempts + 1,
					"updated_at":  now,
				}).Error; err != nil {
				return res, err
			}
			res.Queued++
			metrics.IncOutreachQueued()
		}
	}
	return res, nil
}

// verdictKind 裁决结论类别
type verdictKind int

const (
	verdictQueued verdictKind = iota // 已入出站队列
	verdictSkip                      // 合规拦下（终态）
)

// verdict 单条任务的裁决留痕（reason 是稳定字面量，前端与 smoke 按它判）。
type verdict struct {
	kind       verdictKind
	reason     string
	channelID  uint
	outboundID uint
}

// decideForDispatch 单条任务的裁决链：开关 → 客户还在 → 可达通道 → 通道窗口 → 入队。
// 返回 error 仅代表"本轮判不了、下轮再试"（瞬时故障），业务性拒绝一律走 verdict。
func decideForDispatch(gdb *gorm.DB, task model.OutreachTask, now time.Time) (verdict, error) {
	p := LoadParams(task.TenantID)
	if !p.Enabled {
		return verdict{kind: verdictSkip, reason: model.OutreachReasonDisabled}, nil
	}
	var custCnt int64
	if err := gdb.Model(&model.Customer{}).
		Where("id = ? AND tenant_id = ?", task.CustomerID, task.TenantID).Count(&custCnt).Error; err != nil {
		return verdict{}, err
	}
	if custCnt == 0 {
		// 客户被删（含 PIPL 删除权匿名化后清除）：计划失去对象，直接终态
		return verdict{kind: verdictSkip, reason: model.OutreachReasonNoChannel}, nil
	}
	tgt, err := ResolveTarget(gdb, task.TenantID, task.CustomerID)
	if errors.Is(err, ErrChannelInactive) {
		return verdict{kind: verdictSkip, reason: model.OutreachReasonChannelInactive}, nil
	}
	if err != nil {
		return verdict{}, err
	}
	if tgt == nil {
		return verdict{kind: verdictSkip, reason: model.OutreachReasonNoChannel}, nil
	}
	if ok, reason := WindowAllows(tgt.ChannelType, tgt.LastInbound, now, p.WindowHours); !ok {
		return verdict{kind: verdictSkip, reason: reason}, nil
	}
	// 触达不绑会话：它是客户级主动开口，不塞 conversation_id=0 进消息表（批六刚治理过孤儿行，
	// 出站表同样不该出现"看起来像会话消息却没有会话"的行）。
	outboundID, err := SendHook(task.TenantID, tgt.ChannelID, task.CustomerID, 0, task.Content, "text")
	if err != nil {
		return verdict{}, fmt.Errorf("入出站队列失败: %w", err)
	}
	return verdict{kind: verdictQueued, channelID: tgt.ChannelID, outboundID: outboundID}, nil
}

// markTerminal 写终态（字段级 Updates，不整行覆写——防 stale 行把并发改动的状态盖回去）。
func markTerminal(gdb *gorm.DB, id uint, status, reason, errMsg string) error {
	return gdb.Model(&model.OutreachTask{}).Where("id = ?", id).
		Updates(map[string]any{
			"status":     status,
			"reason":     reason,
			"error":      truncErr(errMsg),
			"updated_at": time.Now(),
		}).Error
}

// truncErr 错误文本按字节钳到列宽内（error 列 size:500，中文错误串按字节存会超）。
func truncErr(s string) string {
	if len(s) > 480 {
		return s[:480]
	}
	return s
}

// SyncResults 回读 queued 任务的出站终态：sent→sent（并落 sent_at，频控窗口以它为锚）、
// failed→failed（带通道错误）。出站行查不到且已滞留超 24h 判 failed，
// 否则这类任务会永远停在 queued 占着列表与对账。
func SyncResults(gdb *gorm.DB, now time.Time, limit int) (sent, failed int, err error) {
	// 同 DispatchDue：扫描 + 每条任务的出站回读 + 回写共用句柄，派生句柄必须先复位
	gdb = isolate(gdb)
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	var rows []model.OutreachTask
	if err = gdb.Where("status = ? AND outbound_id > 0", model.OutreachStatusQueued).
		Order("id ASC").Limit(limit).Find(&rows).Error; err != nil {
		return 0, 0, err
	}
	for _, task := range rows {
		var ob model.ChannelOutbound
		getErr := gdb.Where("id = ? AND tenant_id = ?", task.OutboundID, task.TenantID).First(&ob).Error
		switch {
		case errors.Is(getErr, gorm.ErrRecordNotFound):
			if now.Sub(task.UpdatedAt) > staleQueuedAfter {
				if e := markTerminal(gdb, task.ID, model.OutreachStatusFailed, "", "出站投递记录缺失（可能被清理）"); e != nil {
					return sent, failed, e
				}
				failed++
				metrics.IncOutreachFailed()
			}
		case getErr != nil:
			return sent, failed, getErr
		case ob.Status == model.OutboundSent:
			at := ob.UpdatedAt
			if err = gdb.Model(&model.OutreachTask{}).Where("id = ?", task.ID).
				Updates(map[string]any{
					"status":     model.OutreachStatusSent,
					"sent_at":    &at,
					"updated_at": now,
				}).Error; err != nil {
				return sent, failed, err
			}
			sent++
			metrics.IncOutreachSent()
		case ob.Status == model.OutboundFailed:
			if err = markTerminal(gdb, task.ID, model.OutreachStatusFailed, model.OutreachReasonSendFailed, ob.Error); err != nil {
				return sent, failed, err
			}
			failed++
			metrics.IncOutreachFailed()
		}
		// pending/sending：仍在通道退避重试中，等下一轮对账
	}
	return sent, failed, nil
}
