// Package middleware OptionalJWTAuth 可选鉴权中间件单元测试（2026-09-08，P0-1 聊天历史鉴权回归锁定）
package middleware

import (
	"ai-scrm/config"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// buildOptionalRouter 组装带 OptionalJWTAuth 的路由，返回 handler 内读取的上下文快照
func buildOptionalRouter(t *testing.T) *gin.Engine {
	t.Helper()
	config.LoadConfig() // 初始化全局配置（测试环境 GlobalConfig 为 nil 指针，需先加载）
	config.GlobalConfig.JWT.Secret = "test-secret-optional-auth"
	config.GlobalConfig.JWT.ExpireHours = 24
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/chat/history", OptionalJWTAuth(), func(c *gin.Context) {
		uid, _ := c.Get("user_id")
		role, _ := c.Get("role")
		tid, _ := c.Get("tenant_id")
		c.JSON(http.StatusOK, gin.H{
			"has_user":  uid != nil,
			"user_id":   uid,
			"role":      role,
			"tenant_id": tid,
		})
	})
	return r
}

// TestOptionalJWTAuthValidToken 携带合法 Bearer：注入 user_id/role/tenant_id
func TestOptionalJWTAuthValidToken(t *testing.T) {
	r := buildOptionalRouter(t)
	tok, err := GenerateToken(7, "sales1", "sales", 42)
	if err != nil {
		t.Fatalf("GenerateToken failed: %v", err)
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/chat/history", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !containsStr(body, `"has_user":true`) || !containsStr(body, `"user_id":7`) || !containsStr(body, `"tenant_id":42`) {
		t.Fatalf("valid token claims not injected: %s", body)
	}
}

// TestOptionalJWTAuthAnonymous 匿名请求：放行且不注入身份（交由调用方 visitor_key 兜底）
func TestOptionalJWTAuthAnonymous(t *testing.T) {
	r := buildOptionalRouter(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/chat/history", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("anonymous request must pass through, got %d", w.Code)
	}
	if !containsStr(w.Body.String(), `"has_user":false`) {
		t.Fatalf("anonymous must not inject user: %s", w.Body.String())
	}
}

// TestOptionalJWTAuthInvalidToken 坏 token：不拒绝（匿名路径仍由 visitor_key 校验），也不注入身份
func TestOptionalJWTAuthInvalidToken(t *testing.T) {
	r := buildOptionalRouter(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/chat/history", nil)
	req.Header.Set("Authorization", "Bearer broken-token")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("invalid token must pass through as anonymous, got %d", w.Code)
	}
	if !containsStr(w.Body.String(), `"has_user":false`) {
		t.Fatalf("invalid token must not inject user: %s", w.Body.String())
	}
}

func containsStr(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
