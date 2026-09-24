// 连接写截止延长的机制回归（2026-09-24，收口残项「WriteTimeout 链路预算」）
//
// 这一层的东西最容易"写了个看起来对的调用"：extendWriteDeadline 只是往 ResponseController
// 里塞一个时间点， gin 的包装writer 若哪天不再 Unwrap 到底层 conn，SetWriteDeadline 会静默
// 失败、代码只打一行 WARN、测试照样绿——线上仍然是 60s 截断。所以本文件不测"有没有调用"，
// 而是起**真实 net.Listener + http.Server**，把全局 WriteTimeout 设到比响应写出时刻更早，
// 用一正一反两条用例证明：
//
//	① 对照（不延长）：慢响应的正文真的写不到客户端——这就是修复前用户拿到半截 CSV 的现场；
//	② 延长：同一份延迟 + 同一份正文，延长后客户端拿到完整字节。
//
// 不依赖数据库，也不依赖 AI：测的是 HTTP 写通路本身。
package api

import (
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// startSlowWriteServer 起一个 WriteTimeout 极短的真实 HTTP 服务。
// 处理器先睡过全局写截止、再吐出 bodySize 字节；extend=true 时在睡之前延长本连接写截止。
func startSlowWriteServer(t *testing.T, extend bool, globalTimeout, sleepFor time.Duration, bodySize int) string {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/slow", func(c *gin.Context) {
		if extend {
			extendWriteDeadline(c, 5*time.Second, "unit-test")
		}
		time.Sleep(sleepFor)
		c.String(http.StatusOK, strings.Repeat("x", bodySize))
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("无法监听本地端口: %v", err)
	}
	srv := &http.Server{Handler: r, WriteTimeout: globalTimeout}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return "http://" + ln.Addr().String() + "/slow"
}

// fetchBody 取回完整响应正文；服务端掐断连接时返回 error 而不是空正文。
func fetchBody(t *testing.T, url string) (string, error) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

// TestWriteDeadlineExtensionKeepsSlowResponse 反向对照在前、正向延长在后：
// 只跑正向用例无法区分"机制生效"与"全局超时压根没触发"，对照用例就是把这条退路堵死。
func TestWriteDeadlineExtensionKeepsSlowResponse(t *testing.T) {
	const bodySize = 64 * 1024
	// 全局写截止 100ms，响应在 400ms 后才开始写——不延长必然落在截止之后
	ctrl := startSlowWriteServer(t, false, 100*time.Millisecond, 400*time.Millisecond, bodySize)
	got, err := fetchBody(t, ctrl)
	if err == nil && len(got) == bodySize {
		t.Fatalf("对照用例失败：未延长写截止的慢响应竟完整送达，说明本用例没有真正触发全局 WriteTimeout，测了个空")
	}

	ext := startSlowWriteServer(t, true, 100*time.Millisecond, 400*time.Millisecond, bodySize)
	got, err = fetchBody(t, ext)
	if err != nil {
		t.Fatalf("延长写截止后仍断连: %v（ResponseController 是否已取不到底层 conn？）", err)
	}
	if len(got) != bodySize {
		t.Fatalf("正文被截断: 期望 %d 字节，实得 %d", bodySize, len(got))
	}
}

// TestSyncDeadlinesCoverDesignBudget 钉住两个预算常量的来源，防止有人为了"省事"把它们调小：
// 同步 AI 的 240s 必须容得下整条最坏链路（合并 25 + 延迟 15 + AI 110 + 2min 硬顶余量）；
// CSV 导出的 180s 必须大于全局 WriteTimeout，否则这次修复等于没做。
func TestSyncDeadlinesCoverDesignBudget(t *testing.T) {
	if syncAIWriteDeadline < 25*time.Second+15*time.Second+110*time.Second {
		t.Fatalf("syncAIWriteDeadline=%v 小于链路最坏耗时 150s，慢模型窗口下会回到\"钱花了、响应断了\"", syncAIWriteDeadline)
	}
	if csvExportWriteDeadline <= 60*time.Second {
		t.Fatalf("csvExportWriteDeadline=%v 没有超过全局 WriteTimeout 60s，5 万行导出仍会被静默截断", csvExportWriteDeadline)
	}
}
