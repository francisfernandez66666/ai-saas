// 包内自包含的日志轮转 writer（2026-10-13 修复批 P2-6）
//
// 为什么不用 lumberjack：本机 go proxy 不可达（Bad Gateway），gopkg.in/natefinch/lumberjack
// 拉不到；强改代码会直接编译失败。故用标准库 os 实现同等语义，零新依赖。
//
// 为什么需要：InitStructuredLogging 原先把 handler 硬编码在 os.Stderr，实际日志经
// shell 重定向（./ai-scrm >> ai-scrm.log）落盘且**永不轮转**——本机实测单文件 34 MB，
// 长运行可达数百 MB，会撑爆磁盘并让运维 tail 检索失效。
//
// 语义（对齐 lumberjack 的核心行为）：
//   - 按大小轮转：单文件超 MaxSize 即改名归档
//   - 保留 MaxBackups 份，超出丢弃最旧（.1 最新 → .N 最旧）
//   - 并发安全：写锁串行化，多 goroutine 写日志不交错撕裂
//   - 不因轮转失败而中断业务：轮转出错时退回"追加写原文件"并记一次告警，
//     日志丢失远好于服务不可用
package logx

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// RotatingWriter 按大小轮转的文件 writer，实现 io.Writer。
type RotatingWriter struct {
	mu sync.Mutex

	filename   string
	maxSize    int64
	maxBackups int

	size   int64 // 当前文件字节数，由本 writer 自行累加
	file   *os.File
	warned bool // 轮转失败是否已告警过（只告警一次，避免刷屏）
}

// NewRotatingWriter 创建轮转 writer 并打开首个文件。
// maxSize <= 0 时不做大小轮转（纯追加），maxBackups < 0 视为 0（不保留归档）。
func NewRotatingWriter(filename string, maxSize int64, maxBackups int) (*RotatingWriter, error) {
	if maxSize <= 0 {
		return nil, fmt.Errorf("logx: NewRotatingWriter maxSize 必须 > 0，收到 %d", maxSize)
	}
	if maxBackups < 0 {
		maxBackups = 0
	}
	w := &RotatingWriter{
		filename:   filename,
		maxSize:    maxSize,
		maxBackups: maxBackups,
	}
	// 确保目录存在（部署常把日志放 /var/log/xxx）
	if dir := filepath.Dir(filename); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("logx: 创建日志目录 %s: %w", dir, err)
		}
	}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

// open 以追加模式打开当前日志文件，并把 size 初始化为已有字节数。
// 关键：进程重启后必须从磁盘实际大小续算，否则会把已有大文件当空文件、
// 撑到远超 MaxSize 才触发第一次轮转（这正是"重启后轮转失灵"的经典成因）。
func (w *RotatingWriter) open() error {
	f, err := os.OpenFile(w.filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("logx: 打开日志文件 %s: %w", w.filename, err)
	}
	w.file = f
	if st, err := f.Stat(); err == nil {
		w.size = st.Size()
	} else {
		w.size = 0
	}
	return nil
}

// Write 写入一条日志，超过 MaxSize 时先轮转。并发安全。
func (w *RotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	// 轮转前先判阈值：本条写不写得过线（多写一条不影响正确性）
	if w.size+int64(len(p)) > w.maxSize && w.size > 0 {
		w.rotate()
	}
	n, err := w.file.Write(p)
	w.size += int64(n) // 只累加实际写入量：部分写入失败时 size 不虚高
	if err != nil {
		return n, err
	}
	return n, nil
}

// rotate 归档当前文件并重开。调用方必须已持有锁。
// 任何一步失败都只告警不 panic——服务可用性优先于日志完整性。
func (w *RotatingWriter) rotate() {
	// 1) 关闭当前句柄（重命名前必须关，否则 Windows 上会失败；Unix 上也避免句柄泄漏）
	if w.file != nil {
		_ = w.file.Close()
		w.file = nil
	}

	// 2) 归档序号平移：.N-1 → .N，.1 → .2 …… 最后把当前文件挪成 .1
	//    先删最旧再逐个改名，避免平移中途失败导致中间态（有洞的编号）
	if w.maxBackups > 0 {
		// 2a) 删超出保留份数的最旧档
		if err := os.Remove(w.filename + fmt.Sprintf(".%d", w.maxBackups)); err != nil && !os.IsNotExist(err) {
			w.warnRotate("删除最旧归档失败", err)
		}
		// 2b) 倒序平移 N-1 → N
		for i := w.maxBackups - 1; i >= 1; i-- {
			from := w.filename + fmt.Sprintf(".%d", i)
			to := w.filename + fmt.Sprintf(".%d", i+1)
			if _, err := os.Stat(from); err != nil {
				continue // 该档不存在，正常
			}
			if err := os.Rename(from, to); err != nil {
				w.warnRotate("归档平移失败", err)
			}
		}
		// 2c) 当前文件 → .1
		if err := os.Rename(w.filename, w.filename+".1"); err != nil {
			w.warnRotate("当前日志改名失败", err)
		}
	} else {
		// 不保留归档：直接截断（等价于 lumberjack MaxBackups=0）
		if err := os.Truncate(w.filename, 0); err != nil {
			w.warnRotate("截断日志失败", err)
		}
	}

	// 3) 重开；失败则退回追加写原文件（不阻断业务）
	if err := w.open(); err != nil {
		w.warnRotate("轮转后重开日志失败", err)
	}
}

// warnRotate 轮转异常只告警一次（避免每条日志都刷一遍同样的错）
func (w *RotatingWriter) warnRotate(action string, err error) {
	if w.warned {
		return
	}
	w.warned = true
	fmt.Fprintf(os.Stderr, "[logx][WARN] 日志轮转%s失败: %v —— 已退回追加写原文件，业务不受影响\n", action, err)
}

// Close 关闭当前句柄（进程退出/测试收尾用）
func (w *RotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

// Backups 列出当前已存在的归档文件名（升序，最旧在前）。测试与运维观测用。
func (w *RotatingWriter) Backups() []string {
	matches, err := filepath.Glob(w.filename + ".*")
	if err != nil {
		return nil
	}
	// 按尾部序号数值排序（字符串排序会把 .10 排在 .2 前面）
	sort.Slice(matches, func(i, j int) bool {
		return backupIndex(matches[i]) < backupIndex(matches[j])
	})
	return matches
}

// backupIndex 从 "path/to/app.log.3" 提取尾部序号；无序号返回较大值排到末尾
func backupIndex(name string) int {
	idx := strings.LastIndex(name, ".")
	if idx < 0 {
		return 1 << 30
	}
	var n int
	if _, err := fmt.Sscanf(name[idx+1:], "%d", &n); err != nil {
		return 1 << 30
	}
	return n
}
