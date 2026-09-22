// Package middleware 测试：验证 P2-9 gzip 中间件仅压缩 SPA 静态资源。
package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// newGzipTestEngine 构造带 GzipStatic 中间件的测试引擎，静态资源用 c.File 模拟 SPA 托管。
func newGzipTestEngine() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(GzipStatic())
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	r.GET("/api/v1/status", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	r.GET("/assets/app.js", func(c *gin.Context) {
		c.Header("Content-Type", "application/javascript")
		c.String(http.StatusOK, "console.log('hello world');")
	})
	return r
}

// TestGzipStaticAssets 验证 /assets/*.js 在 Accept-Encoding: gzip 下被压缩。
func TestGzipStaticAssets(t *testing.T) {
	r := newGzipTestEngine()
	req := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("期望 /assets/app.js 被 gzip 压缩，实际 Content-Encoding=%q", w.Header().Get("Content-Encoding"))
	}
}

// TestGzipStaticHealthNotCompressed 验证 /health 即使带 gzip 也不被压缩。
func TestGzipStaticHealthNotCompressed(t *testing.T) {
	r := newGzipTestEngine()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Header().Get("Content-Encoding") == "gzip" {
		t.Fatalf("期望 /health 不被压缩，实际 Content-Encoding=gzip")
	}
}

// TestGzipStaticAPINotCompressed 验证 /api 路由不被压缩。
func TestGzipStaticAPINotCompressed(t *testing.T) {
	r := newGzipTestEngine()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Header().Get("Content-Encoding") == "gzip" {
		t.Fatalf("期望 /api/v1/status 不被压缩，实际 Content-Encoding=gzip")
	}
}

// TestGzipStaticNoAcceptEncoding 验证不带 gzip 请求头时不压缩。
func TestGzipStaticNoAcceptEncoding(t *testing.T) {
	r := newGzipTestEngine()
	req := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Header().Get("Content-Encoding") == "gzip" {
		t.Fatalf("期望无 Accept-Encoding 时不压缩，实际 Content-Encoding=gzip")
	}
	body, _ := io.ReadAll(w.Body)
	if string(body) != "console.log('hello world');" {
		t.Fatalf("透传内容异常: %q", string(body))
	}
}
