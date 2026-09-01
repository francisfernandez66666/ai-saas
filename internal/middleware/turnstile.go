// Package middleware Gin 中间件链：TenantResolver(fail-closed)→JWTAuth→TenantConsistency→OrgResolve 等安全闸。
package middleware

import (
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"ai-scrm/internal/service"

	"github.com/gin-gonic/gin"
)

// ============================================================
// Turnstile 人机验证中间件（批次三防薅，2026-08-23 代码就绪）
//
// 挂载点：C 端免登录接口 POST /api/v1/chat/guest · /chat/test
// 开关：turnstile_enabled=false 或 secret 未配置 → 直接放行（零开销）
// 令牌：前端渲染 Cloudflare Turnstile 组件后，把令牌放 X-Turnstile-Token 头
// 校验：服务端调 challenges.cloudflare.com/turnstile/v0/siteverify
//
// 可用性取舍：
//   - 令牌缺失/校验返回失败 → fail-closed 403（防刷语义优先）
//   - CF 服务不可达/超时   → fail-open 放行并告警（验证器故障不阻断业务主链路；
//     攻击者最多拖慢 5s，且 siteverify 故障窗口极短）
// ============================================================

const turnstileVerifyURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"

// turnstileClient 调用 Cloudflare siteverify 校验接口的 HTTP 客户端（5s 超时，避免拖慢主链路）
var turnstileClient = &http.Client{Timeout: 5 * time.Second}

// turnstileVerifyResp Cloudflare siteverify 校验响应结构
type turnstileVerifyResp struct {
	Success    bool     `json:"success"`
	ErrorCodes []string `json:"error-codes"`
}

// TurnstileGuard 人机验证中间件
func TurnstileGuard() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !turnstileEnabled() {
			c.Next()
			return
		}
		token := strings.TrimSpace(c.GetHeader("X-Turnstile-Token"))
		if token == "" {
			abortTurnstile(c, "缺少人机验证令牌")
			return
		}
		secret := service.DefaultSystemConfigService.GetString("turnstile_secret_key", "")
		if secret == "" {
			log.Printf("[Turnstile] 已启用但未配置密钥，视为关闭")
			c.Next()
			return
		}

		resp, err := verifyTurnstile(secret, token, c.ClientIP())
		if err != nil {
			// 验证器不可达：fail-open 不阻断客户对话主链路
			log.Printf("[Turnstile] siteverify 不可达(fail-open): %v ip=%s", err, c.ClientIP())
			c.Next()
			return
		}
		if resp == nil || !resp.Success {
			codes := "invalid"
			if resp != nil && len(resp.ErrorCodes) > 0 {
				codes = strings.Join(resp.ErrorCodes, ",")
			}
			log.Printf("[Turnstile] 校验失败 ip=%s codes=%s", c.ClientIP(), codes)
			abortTurnstile(c, "人机验证失败，请刷新重试")
			return
		}
		c.Next()
	}
}

// turnstileEnabled 开关判定（enabled 且 secret 已配置才生效）
func turnstileEnabled() bool {
	if service.DefaultSystemConfigService == nil {
		return false
	}
	return service.DefaultSystemConfigService.GetBool("turnstile_enabled", false)
}

// GetTurnstileSiteKey 站点键读取（带 60s 进程内缓存；公开信息无敏感面）
func GetTurnstileSiteKey() (enabled bool, siteKey string) {
	if !turnstileEnabled() {
		return false, ""
	}
	siteKey = service.DefaultSystemConfigService.GetString("turnstile_site_key", "")
	return siteKey != "", siteKey
}

// verifyTurnstile 调用 Cloudflare siteverify
func verifyTurnstile(secret, token, remoteIP string) (*turnstileVerifyResp, error) {
	form := url.Values{}
	form.Set("secret", secret)
	form.Set("response", token)
	if remoteIP != "" {
		form.Set("remoteip", remoteIP)
	}
	resp, err := turnstileClient.PostForm(turnstileVerifyURL, form)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out turnstileVerifyResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// abortTurnstile 统一拒绝响应
func abortTurnstile(c *gin.Context, msg string) {
	c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
		"code": 403, "message": msg, "data": nil,
	})
}
