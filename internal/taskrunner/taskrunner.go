// Package taskrunner 统一后台任务框架（G-11）
// 替换 main.go 中 8 处裸 go func+ticker，提供：
// - 注册式 API：Register(name, interval, leaderElect, fn)
// - 统一 recover + metrics
// - 优雅停止（context cancel）
// - 健康暴露（LastRun/LastError/RunCount）
package taskrunner

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"ai-scrm/internal/redisclient"
)

// Task 定义一个周期性后台任务
type Task struct {
	Name         string
	Interval     time.Duration
	LeaderElect  bool // 是否需要 Redis 选主（多实例场景）
	Fn           func(ctx context.Context)
	// 运行时状态
	lastRun   time.Time
	lastError error
	runCount  int64
	mu        sync.Mutex
}

// RunInfo 暴露给健康检查的运行信息
type RunInfo struct {
	LastRun   time.Time `json:"last_run"`
	LastError string    `json:"last_error,omitempty"`
	RunCount  int64     `json:"run_count"`
}

// Runner 任务调度器
type Runner struct {
	tasks   []*Task
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// New 创建 Runner，传入 context 用于优雅停止
func New(parent context.Context) *Runner {
	ctx, cancel := context.WithCancel(parent)
	return &Runner{
		ctx:    ctx,
		cancel: cancel,
	}
}

// Register 注册一个周期性任务
// name: 任务名（用于日志和健康暴露）
// interval: 执行间隔
// leaderElect: 是否需要 Redis 选主
// fn: 任务函数，接收 ctx 用于检查停止信号
func (r *Runner) Register(name string, interval time.Duration, leaderElect bool, fn func(ctx context.Context)) *Task {
	t := &Task{
		Name:        name,
		Interval:    interval,
		LeaderElect: leaderElect,
		Fn:          fn,
	}
	r.tasks = append(r.tasks, t)
	return t
}

// Start 启动所有已注册的任务
func (r *Runner) Start() {
	for _, t := range r.tasks {
		r.wg.Add(1)
		go r.runTask(t)
	}
	log.Printf("[TaskRunner] 启动 %d 个后台任务", len(r.tasks))
}

// Stop 优雅停止所有任务
func (r *Runner) Stop() {
	r.cancel()
	r.wg.Wait()
	log.Printf("[TaskRunner] 所有任务已停止")
}

// Info 返回所有任务的运行信息（供 /status 端点使用）
func (r *Runner) Info() []map[string]interface{} {
	var result []map[string]interface{}
	for _, t := range r.tasks {
		t.mu.Lock()
		info := map[string]interface{}{
			"name":      t.Name,
			"interval":  t.Interval.String(),
			"run_count": t.runCount,
			"last_run":  t.lastRun,
		}
		if t.lastError != nil {
			info["last_error"] = t.lastError.Error()
		}
		t.mu.Unlock()
		result = append(result, info)
	}
	return result
}

func (r *Runner) runTask(t *Task) {
	defer r.wg.Done()

	// 启动时立即执行一次（如果无需选主或获取到锁）
	lockKey := "task:" + t.Name
	if !t.LeaderElect || redisclient.TryLock(lockKey, t.Interval*2) != nil {
		r.execute(t)
	}

	ticker := time.NewTicker(t.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-r.ctx.Done():
			return
		case <-ticker.C:
			// 选主检查
			if t.LeaderElect && redisclient.TryLock(lockKey, t.Interval*2) == nil {
				continue
			}
			r.execute(t)
		}
	}
}

func (r *Runner) execute(t *Task) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("[TaskRunner] %s panic: %v", t.Name, rec)
			t.mu.Lock()
			t.lastError = fmt.Errorf("panic: %v", rec)
			t.mu.Unlock()
		}
	}()

	start := time.Now()
	t.Fn(r.ctx)

	t.mu.Lock()
	t.lastRun = start
	t.runCount++
	t.lastError = nil
	t.mu.Unlock()
}
