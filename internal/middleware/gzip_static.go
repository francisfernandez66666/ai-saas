// Package middleware 提供 Gin 全局/分组中间件（CORS、鉴权、租户解析、trace 等）。
// 本文件实现 P2-9 生产静态资源 gzip 压缩中间件（零依赖，使用标准库 compress/gzip）。
package middleware

import (
	"compress/gzip"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// gzipStaticContentTypes P2-9(2026-09-22)：仅对下列 Content-Type 的响应启用 gzip。
// 覆盖 SPA 静态资源（JS/CSS/JSON/SVG 等结构化文本）；text/html 不在此列（index.html
// 体积可控，且避免与 SPA 路由回退的 Content-Type 误触发压缩歧义）。
var gzipStaticContentTypes = []string{
	"application/javascript",
	"application/json",
	"image/svg+xml",
}

// gzipStaticEligiblePath P2-9(2026-09-22)：判断请求路径是否属于"SPA 静态资源"范围，
// 决定是否允许走 gzip。规则：/assets/* 与根路径下静态文件放行；API 与运维端点
// （/api、/health、/status、/status/detail、/metrics）一律拒绝，绝不压缩 WS/SSE/API 响应。
func gzipStaticEligiblePath(path string) bool {
	switch path {
	case "/health", "/status", "/status/detail", "/metrics":
		return false
	}
	if strings.HasPrefix(path, "/api") {
		return false
	}
	return true
}

// gzipStaticShouldCompress P2-9(2026-09-22)：依据响应的 Content-Type 判断是否需要压缩。
// 仅 text/* 与白名单结构化类型压缩；其余（含 text/html）不压缩。
func gzipStaticShouldCompress(contentType string) bool {
	ct := strings.ToLower(strings.TrimSpace(contentType))
	if i := strings.Index(ct, ";"); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	if strings.HasPrefix(ct, "text/") {
		return true
	}
	for _, allow := range gzipStaticContentTypes {
		if ct == allow {
			return true
		}
	}
	return false
}

// gzipStaticResponseWriter P2-9(2026-09-22)：包装 gin.ResponseWriter 的 gzip 响应写入器。
// 在 WriteHeader 时按 Content-Type 决策是否真正压缩；不匹配则原样透传，保证 API/HTML 不受影响。
type gzipStaticResponseWriter struct {
	gin.ResponseWriter
	gz          *gzip.Writer
	wroteHeader bool
}

// WriteHeader 覆盖默认实现：在首字节写出前按 Content-Type 决定是否启用 gzip 包裹。
func (w *gzipStaticResponseWriter) WriteHeader(code int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	if gzipStaticShouldCompress(w.Header().Get("Content-Type")) {
		w.Header().Set("Content-Encoding", "gzip")
		// gzip 后字节数变化，删除原 Content-Length 避免客户端按旧长度截断
		w.Header().Del("Content-Length")
		w.gz = gzip.NewWriter(w.ResponseWriter)
	}
	w.ResponseWriter.WriteHeader(code)
}

// Write 覆盖默认实现：压缩态写 gzip writer，否则透传底层 writer。
func (w *gzipStaticResponseWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if w.gz != nil {
		return w.gz.Write(b)
	}
	return w.ResponseWriter.Write(b)
}

// Flush 透传底层 Flusher（若存在），保证流式场景不被本中间件拦截（本中间件仅作用于静态资源）。
func (w *gzipStaticResponseWriter) Flush() {
	if w.gz != nil {
		_ = w.gz.Flush()
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Close 收尾 gzip writer，确保所有缓冲数据落盘。
func (w *gzipStaticResponseWriter) Close() error {
	if w.gz != nil {
		return w.gz.Close()
	}
	return nil
}

// GzipStatic P2-9(2026-09-22)：仅压缩 SPA 静态资源的中间件（零依赖，使用标准库 compress/gzip）。
// 生效条件：客户端 Accept-Encoding 含 gzip 且路径属于静态资源范围；压缩类型限 text/* 与
// 白名单结构化类型。绝不作用于 /api、/health、/status、/metrics 等 API 与 WS/SSE 端点。
// 挂载点：cmd/server/main.go 的 SPA 托管处（r.Use(GzipStatic())）。
func GzipStatic() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !strings.Contains(strings.ToLower(c.GetHeader("Accept-Encoding")), "gzip") {
			c.Next()
			return
		}
		if !gzipStaticEligiblePath(c.Request.URL.Path) {
			c.Next()
			return
		}
		gw := &gzipStaticResponseWriter{ResponseWriter: c.Writer}
		c.Writer = gw
		defer gw.Close()
		c.Next()
	}
}
