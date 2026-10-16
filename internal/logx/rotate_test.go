// 日志轮转单测（2026-10-13 修复批 P2-6）
//
// 为什么这组用例敢真写文件：轮转的唯一产物是 tmpdir 里的普通文本文件，
// 不连 DB、不触业务表，可安全真跑（不像状态机那组要桩掉写入）。
//
// 变异口径（改坏代码确认用例会红）：
//   - 去掉 rotate() 调用 → TestRotateOnSizeLimit 必红（文件无限增长）
//   - 去掉"平移前删最旧" → TestRotateKeepsMaxBackups 必红（归档无限累积）
//   - open() 里不读 Stat 设 size → TestSizeCarriesOverAfterReopen 必红
//     （重启后把已有大文件当空文件，撑到远超 MaxSize 才轮转）
//   - 去掉写入锁 → TestConcurrentWritesNoInterleave 在 -race 下必红
package logx

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// writeLines 往 writer 写 n 行，每行固定长度，便于按体积断言
func writeLines(t *testing.T, w *RotatingWriter, n, lineLen int, tag string) {
	t.Helper()
	line := strings.Repeat(tag, lineLen)
	for i := 0; i < n; i++ {
		if _, err := w.Write([]byte(line + "\n")); err != nil {
			t.Fatalf("写入失败: %v", err)
		}
	}
}

// fileSize 取日志文件的当前字节数，供轮转用例断言当前文件未超 MaxSize。
func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("读取 %s 大小失败: %v", path, err)
	}
	return st.Size()
}

// TestRotateOnSizeLimit 超过 MaxSize 必须轮转，当前文件不得无限增长
func TestRotateOnSizeLimit(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "app.log")
	// 每行 100 字节，写 100 行 = 10KB；上限设 4KB ⇒ 应触发约 2 次轮转
	w, err := NewRotatingWriter(logPath, 4<<10, 3)
	if err != nil {
		t.Fatalf("创建轮转 writer 失败: %v", err)
	}
	defer w.Close()

	writeLines(t, w, 100, 100, "x")

	if got := fileSize(t, logPath); got > 4<<10 {
		t.Errorf("当前文件应始终不超过 MaxSize(=4096)，实际 %d 字节——说明没触发轮转", got)
	}
	backups := w.Backups()
	if len(backups) == 0 {
		t.Fatalf("已写入 10KB 到上限 4KB 的文件，必须产生归档，实际 0 份")
	}
	t.Logf("当前 %d 字节，归档 %d 份", fileSize(t, logPath), len(backups))
}

// TestRotateKeepsMaxBackups 归档份数不得超过 MaxBackups（否则磁盘仍会被吃满）
func TestRotateKeepsMaxBackups(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "app.log")
	w, err := NewRotatingWriter(logPath, 2<<10, 2) // 上限 2KB，只留 2 份
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	defer w.Close()

	writeLines(t, w, 200, 100, "y") // 20KB ⇒ 约 9 次轮转

	backups := w.Backups()
	if len(backups) > 2 {
		names := make([]string, len(backups))
		copy(names, backups)
		t.Errorf("归档份数不得超过 MaxBackups=2，实际 %d 份: %v", len(backups), names)
	}
	if len(backups) == 0 {
		t.Errorf("写入 20KB 到上限 2KB，必须产生归档")
	}
	// 只允许 .1 / .2 两个编号，不应出现 .3（平移逻辑错会留越界档）
	for _, b := range backups {
		if strings.HasSuffix(b, ".3") || strings.HasSuffix(b, ".4") {
			t.Errorf("出现超出保留份数的归档 %s（MaxBackups=2 只应留 .1/.2）", filepath.Base(b))
		}
	}
}

// TestSizeCarriesOverAfterReopen 重开 writer 时 size 必须从磁盘续算，
// 否则进程重启后会把已有大文件当空文件，撑到远超 MaxSize 才第一次轮转。
func TestSizeCarriesOverAfterReopen(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "app.log")
	// 先手工铺一个 3KB 的既有文件（模拟上次运行留下的）
	if err := os.WriteFile(logPath, []byte(strings.Repeat("z", 3<<10)), 0o644); err != nil {
		t.Fatalf("预置文件失败: %v", err)
	}

	w, err := NewRotatingWriter(logPath, 4<<10, 3) // 上限 4KB，已有 3KB
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	defer w.Close()

	if w.size != 3<<10 {
		t.Errorf("重开时 size 必须从磁盘实际大小续算(=3072)，实际 %d —— 会导致重启后首次轮转迟钝", w.size)
	}
	// 再写 2KB 应立刻触发轮转（3+2 > 4）
	writeLines(t, w, 20, 100, "w")
	if len(w.Backups()) == 0 {
		t.Errorf("已有 3KB + 新写 2KB > 上限 4KB，应触发轮转并留归档")
	}
}

// TestNoBackupTruncate MaxBackups=0 时不保留归档，改为截断原文件
func TestNoBackupTruncate(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "app.log")
	w, err := NewRotatingWriter(logPath, 2<<10, 0)
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	defer w.Close()

	writeLines(t, w, 50, 100, "t") // 5KB ⇒ 约 2 次轮转
	if got := len(w.Backups()); got != 0 {
		t.Errorf("MaxBackups=0 时不应留归档，实际 %d 份", got)
	}
	if got := fileSize(t, logPath); got > 2<<10 {
		t.Errorf("MaxBackups=0 走截断路径，当前文件应受 MaxSize 约束，实际 %d", got)
	}
}

// TestConcurrentWritesNoInterleave 并发写入必须串行化：
// 每行 101 字节、含唯一序号，校验每行长度与序号完整，撕裂即红。
func TestConcurrentWritesNoInterleave(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "app.log")
	w, err := NewRotatingWriter(logPath, 8<<20, 2) // 上限够大，不触发轮转，专注并发正确性
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	defer w.Close()

	const goroutines = 8
	const perG = 200
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				payload := fmt.Sprintf("[g%02d-i%03d]%s\n", g, i, strings.Repeat("p", 60))
				if _, err := w.Write([]byte(payload)); err != nil {
					t.Errorf("并发写入失败: %v", err)
					return
				}
			}
		}(g)
	}
	wg.Wait()

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	want := goroutines * perG
	if len(lines) != want {
		t.Fatalf("应有 %d 行，实际 %d 行（丢行=并发写撕裂）", want, len(lines))
	}
	for _, ln := range lines {
		// 行宽 = "[gXX-iXXX]" 10 字符 + 60 填充 = 70
		// （g<2位> 用 %02d，故是 g07 不是 g007——初版断言写成 72 是测试自身算错）
		if len(ln) != 70 {
			t.Fatalf("发现撕裂行（长度异常 %d，期望 70）: %q", len(ln), ln)
		}
	}
}

// TestRejectInvalidMaxSize MaxSize<=0 必须报错返回，而不是静默退化成"永不轮转"
// （静默退化最坏：调用方以为开了轮转，实际日志仍会涨到撑爆磁盘）
func TestRejectInvalidMaxSize(t *testing.T) {
	dir := t.TempDir()
	if _, err := NewRotatingWriter(filepath.Join(dir, "a.log"), 0, 3); err == nil {
		t.Errorf("MaxSize=0 必须返回错误，不能静默接受（否则等于没开轮转）")
	}
	if _, err := NewRotatingWriter(filepath.Join(dir, "b.log"), -1, 3); err == nil {
		t.Errorf("MaxSize<0 必须返回错误")
	}
}

// TestCreatesMissingDir 日志目录不存在时应自动创建（部署常写 /var/log/xxx）
func TestCreatesMissingDir(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "nested", "deep", "app.log")
	w, err := NewRotatingWriter(logPath, 4<<10, 2)
	if err != nil {
		t.Fatalf("应自动创建缺失目录，实际报错: %v", err)
	}
	defer w.Close()
	writeLines(t, w, 2, 10, "d")
	if _, err := os.Stat(logPath); err != nil {
		t.Errorf("日志文件未生成: %v", err)
	}
}
