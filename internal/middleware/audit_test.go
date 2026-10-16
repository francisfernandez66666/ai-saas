// Package middleware 测试：P1-2 请求级审计中间件门禁。
//
// 门禁意图（对应 FIXPLAN §P1-2 "代码+门禁"）：
//  1. 经鉴权链的请求必须异步落一行到 request_audit_logs（method/path/tenant/user/ip/ua/status/latency 正确）；
//  2. noise 路径（/health 等）必须被跳过，不落审计。
//
// 这把"中间件真的被注册并生效"从口头承诺变成可回归断言——新增 handler 漏审计不再是零惩罚。
package middleware

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/testutil"

	"github.com/gin-gonic/gin"
)

// TestMain 统一出口：把 DB 跳过/失败计数打到 stderr（与仓库其他测试包一致）。
func TestMain(m *testing.M) {
	os.Exit(testutil.RunMain(m))
}

// TestAuditMiddlewareWritesRequestLog 验证 P1-2 请求级审计中间件：
// 经鉴权链的真实请求异步落审计行，且字段正确；noise 路径不落。
func TestAuditMiddlewareWritesRequestLog(t *testing.T) {
	testutil.SetupTestDB(t)
	gin.SetMode(gin.TestMode)

	r := gin.New()
	// 模拟鉴权中间件：写入 tenant_id / user_id（真实 JWTAuth/TenantConsistency 的职责）
	r.Use(func(c *gin.Context) {
		c.Set("tenant_id", uint(7))
		c.Set("user_id", uint(9))
		c.Next()
	})
	r.Use(AuditMiddleware())

	r.GET("/api/v1/things", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	r.GET("/health", func(c *gin.Context) { c.Status(http.StatusOK) }) // 应被跳过

	// 1) 应被审计的路径
	// 隔离：先记基线计数，再断言"请求后新增了一行匹配记录"——避免历史残留行导致假绿。
	before := auditCount(t, "path = ? AND tenant_id = ?", "/api/v1/things", uint(7))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/things", nil)
	req.Header.Set("User-Agent", "audit-test-agent")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("handler 状态码错误: got %d", w.Code)
	}

	// 异步写库：轮询等待新增行出现（最多 3s），避免 goroutine 未落库就断言
	var rec model.RequestAuditLog
	if !waitForNewAuditRow(t, &rec, before, "path = ? AND tenant_id = ?", "/api/v1/things", uint(7)) {
		t.Fatal("审计行未写入 request_audit_logs（P1-2 中间件未生效？）")
	}
	if rec.Method != http.MethodGet {
		t.Errorf("method 错误: got %q", rec.Method)
	}
	if rec.UserID != 9 {
		t.Errorf("user_id 错误: got %d", rec.UserID)
	}
	if rec.Status != http.StatusOK {
		t.Errorf("status 错误: got %d", rec.Status)
	}
	if rec.LatencyMs < 0 {
		t.Errorf("latency_ms 异常: got %d", rec.LatencyMs)
	}
	if rec.IP == "" {
		t.Error("ip 不应为空")
	}
	if rec.UserAgent != "audit-test-agent" {
		t.Errorf("user_agent 错误: got %q", rec.UserAgent)
	}

	// 2) noise 路径必须跳过：/health 不落审计
	beforeHealth := auditCount(t, "path = ?", "/health")
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, httptest.NewRequest(http.MethodGet, "/health", nil))
	time.Sleep(200 * time.Millisecond) // 给异步窗口
	afterHealth := auditCount(t, "path = ?", "/health")
	if afterHealth != beforeHealth {
		t.Errorf("/health 属 noise 路径不应被审计，却新增了 %d 行", afterHealth-beforeHealth)
	}
}

// TestAuditMiddlewareTruncatesLongUA 验证超长 UA 被截断到 512 且 UTF-8 有效——
// 修复"注释说截断但实现没截"的字节级缺陷：否则超长 UA 撑爆 varchar 致该行审计静默丢失。
func TestAuditMiddlewareTruncatesLongUA(t *testing.T) {
	testutil.SetupTestDB(t)
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("tenant_id", uint(31))
		c.Set("user_id", uint(41))
		c.Next()
	})
	r.Use(AuditMiddleware())
	r.GET("/api/v1/longua", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })

	longUA := strings.Repeat("界", 700) // 700 个 3 字节汉字 = 2100 字节，远超 512
	if len(longUA) <= 512 {
		t.Fatalf("测试前置失败：构造的 UA 未超 512 字节")
	}
	before := auditCount(t, "path = ? AND tenant_id = ?", "/api/v1/longua", uint(31))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/longua", nil)
	req.Header.Set("User-Agent", longUA)
	r.ServeHTTP(w, req)

	var rec model.RequestAuditLog
	if !waitForNewAuditRow(t, &rec, before, "path = ? AND tenant_id = ?", "/api/v1/longua", uint(31)) {
		t.Fatal("超长 UA 请求的审计行未写入（截断失效致 varchar 报错被吞？）")
	}
	if len(rec.UserAgent) > 512 {
		t.Errorf("user_agent 未被截断到 512：len=%d", len(rec.UserAgent))
	}
	if !utf8.ValidString(rec.UserAgent) {
		t.Error("user_agent 截断后不是有效 UTF-8（切断了多字节字符）")
	}
}

// auditCount 统计匹配条件的审计行数。
func auditCount(t *testing.T, where string, args ...interface{}) int64 {
	t.Helper()
	var n int64
	if err := db.DB.Table("request_audit_logs").Where(where, args...).Count(&n).Error; err != nil {
		t.Fatalf("统计审计行失败: %v", err)
	}
	return n
}

// waitForNewAuditRow 轮询等待"基线计数之后"出现匹配的新审计行（异步写库，最多 3s）。
func waitForNewAuditRow(t *testing.T, rec *model.RequestAuditLog, before int64, where string, args ...interface{}) bool {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if auditCount(t, where, args...) > before {
			// 取最新一行校验字段
			if err := db.DB.Where(where, args...).Order("id DESC").First(rec).Error; err == nil {
				return true
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}
