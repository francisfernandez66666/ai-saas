// Package configcenter 系统配置中心：Seed/Upgrade/Rollback 生命周期与 tenant_cfg_event 热加载。
package configcenter

// ============================================================
// tenant_cfg_event 消费者（M3 修复，2026-08-25）
//
// 此前 Seed/Upgrade/Rollback 三动作发布事件后全仓零订阅者——事件链断裂，
// "各引擎据此热加载"名存实亡。本文件补齐消费端：
//
//	事件 → 解包(action/scope) → 依次调用注册的热加载钩子
//
// 钩子由 main.go 启动时注册（策略引擎 ReloadData / 标签知识缓存 Reload 等），
// 采用注册制而非直接 import 各引擎——避免 config_center→strategy→llm→chatflow
// 潜在依赖环，保持配置中心中立。
//
// 已知边界（沿袭 PLAN_ORCHESTRATION 留档）：LogCenter 进程内总线不跨进程，
// 多实例部署下仅收到事件的实例执行热加载；跨实例一致性依赖 MQ_TYPE=kafka，
// 或各表自身的 Redis cachever 版本戳轮询路径（tag/knowledge 缓存已具备）。
// ============================================================

import (
	"context"
	"encoding/json"
	"log"

	"ai-scrm/internal/mq"
)

// HotReloadHook 热加载钩子签名：tenantID=触发租户(0=系统层)，action=seed/upgrade/rollback
type HotReloadHook func(tenantID uint, action string, scope string)

var hotReloadHooks []HotReloadHook

// RegisterHotReloadHook 注册热加载钩子（main 启动时调用，须在 StartCfgEventConsumer 之前）
func RegisterHotReloadHook(fn HotReloadHook) {
	hotReloadHooks = append(hotReloadHooks, fn)
}

// StartCfgEventConsumer 订阅 tenant_cfg_event（main 启动时调用一次；天然幂等可重复触发）
func StartCfgEventConsumer() {
	mq.Subscribe(mq.TopicTenantCfgEvt, func(ctx context.Context, env mq.Envelope) error {
		var payload struct {
			EventType string `json:"event_type"`
			Data      struct {
				Action   string `json:"action"`
				Scope    string `json:"scope"`
				Affected int    `json:"affected"`
			} `json:"data"`
		}
		if err := json.Unmarshal(env.Payload, &payload); err != nil {
			log.Printf("[ConfigCenter] cfg_event 解包失败: %v", err)
			return nil // 脏消息不阻塞总线
		}
		action := payload.Data.Action
		if action == "" {
			action = payload.EventType
		}
		for i, h := range hotReloadHooks {
			func() {
				defer func() { // 单钩子 panic 不影响其他钩子与总线
					if r := recover(); r != nil {
						log.Printf("[ConfigCenter] 热加载钩子%d panic: %v", i, r)
					}
				}()
				h(env.Header.TenantID, action, payload.Data.Scope)
			}()
		}
		log.Printf("[ConfigCenter] cfg_event 已消费：tenant=%d action=%s scope=%s 钩子=%d",
			env.Header.TenantID, action, payload.Data.Scope, len(hotReloadHooks))
		return nil
	})
	log.Println("[ConfigCenter] tenant_cfg_event 消费者已注册（热加载链路接通）")
}
