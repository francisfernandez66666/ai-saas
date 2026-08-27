// Package middleware Gin 中间件链：TenantResolver(fail-closed)→JWTAuth→TenantConsistency→OrgResolve 等安全闸。
package middleware

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ============================================================
// OpenAPI 鉴权中间件（商业化第一批 M4，2026-08-23）
//
// 设计（源自翻译助手 store/apikeys.go，近直接移植）：
//   Bearer sk_xxx → SHA256 → api_keys 查 active → TouchAPIKey(异步)
//   → ctx 注入租户（Key 归属租户，复用 RQ/PQ 链路天然隔离）
//   → RequirePerm(perm) 最小权限校验
//
// 路由组独立：/openapi/v1 不走 JWTAuth/TenantConsistency（租户来自 Key 归属）
// 安全铁律：每请求查库无缓存窗口——disable 即时生效，勿加缓存（实施文档 §九.4）
// ============================================================

// 开放接口权限常量（最小权限集）
const (
	PermChatRead     = "chat.read"
	PermCustomerRead = "customer.read"
	PermCDPRead      = "cdp.read"
	PermAll          = "all"
)

// hashAPIKey SHA256 摘要（库内唯一索引防碰撞）
func hashAPIKey(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}

// GenerateAPIKey 明文 Key 签发：sk_ + 24位随机串；仅签发响应返回一次
func GenerateAPIKey() (raw string, prefix string, hash string) {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err) // 熵源不可用属系统级故障，快速失败
	}
	for i := range b {
		b[i] = charset[int(b[i])%len(charset)]
	}
	raw = "sk_" + string(b)
	return raw, raw[:10], hashAPIKey(raw)
}

// OpenAPIAuth API Key 鉴权中间件
// 通过后注入 user_id=0/tenant_id=Key归属租户，后续 RQ/PQ 自动隔离
func OpenAPIAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		parts := strings.SplitN(authHeader, " ", 2)
		if !(len(parts) == 2 && parts[0] == "Bearer" && strings.HasPrefix(parts[1], "sk_")) {
			abortOpenAPI(c, http.StatusUnauthorized, "缺少有效的 API Key")
			return
		}
		raw := strings.TrimSpace(parts[1])

		var key model.ApiKey
		err := db.DB.Where("key_hash = ? AND is_active = ?", hashAPIKey(raw), true).First(&key).Error
		if err != nil {
			// 无 Key/错 Key/停用 Key 统一 401（不泄露区分信息）
			abortOpenAPI(c, http.StatusUnauthorized, "API Key 无效或已停用")
			return
		}
		if key.TenantID == nil || *key.TenantID == 0 {
			abortOpenAPI(c, http.StatusUnauthorized, "API Key 未绑定租户")
			return
		}

		c.Set("user_id", uint(0))
		c.Set("role", model.RoleReadOnly) // 开放接口只读语义，叠加 DataScope 时按全租户只读处理
		c.Set("tenant_id", *key.TenantID)
		c.Set("api_key_id", key.ID)
		c.Set("api_key_perms", parsePerms(key.Permissions))

		go touchAPIKey(key.ID) // 异步计量：last_used_at/call_count/usage_records(api_calls)
		c.Next()
	}
}

// RequirePerm 权限点校验（挂在 OpenAPIAuth 之后，逐路由声明所需权限）
func RequirePerm(perm string) gin.HandlerFunc {
	return func(c *gin.Context) {
		permsV, _ := c.Get("api_key_perms")
		perms, _ := permsV.([]string)
		for _, p := range perms {
			if p == PermAll || p == perm {
				c.Next()
				return
			}
		}
		abortOpenAPI(c, http.StatusForbidden, "API Key 无该接口权限："+perm)
	}
}

// abortOpenAPI 统一错误响应（M4 规范化：error_code 独立出参，借鉴翻译助手四期 §2.3）
func abortOpenAPI(c *gin.Context, status int, msg string) {
	errCode := "invalid_api_key"
	if status == http.StatusForbidden {
		errCode = "forbidden"
	} else if status == http.StatusTooManyRequests {
		errCode = "rate_limited"
	}
	c.AbortWithStatusJSON(status, gin.H{"code": status, "error_code": errCode, "message": msg, "data": nil})
}

// parsePerms 解析 permissions JSON 数组；脏数据视为空权限集（fail-closed）
func parsePerms(raw string) []string {
	var perms []string
	if err := json.Unmarshal([]byte(raw), &perms); err != nil {
		return []string{}
	}
	return perms
}

// touchAPIKey 异步计量：调用计数 + 用量明细双写（供未来按调用计费对账）
// call_count 与 usage_records(api_calls) 一致增长（验收 §四.4）
func touchAPIKey(keyID uint) {
	defer func() { _ = recover() }()
	if err := db.DB.Model(&model.ApiKey{}).Where("id = ?", keyID).
		Updates(map[string]interface{}{
			"call_count":   gorm.Expr("COALESCE(call_count,0)+1"),
			"last_used_at": time.Now(),
		}).Error; err != nil {
		log.Printf("[OpenAPI] Key 计量失败 id=%d: %v", keyID, err)
	}
	today := time.Now().Format("2006-01-02")
	if err := db.DB.Exec(`INSERT INTO usage_records (tenant_id, date, metric, value, created_at)
		SELECT tenant_id, ?, 'api_calls', 1, NOW() FROM api_keys WHERE id = ?
		ON CONFLICT (tenant_id, date, metric) DO UPDATE SET value = usage_records.value + 1`,
		today, keyID).Error; err != nil {
		log.Printf("[OpenAPI] api_calls 明细累加失败 key=%d: %v", keyID, err)
	}
}

// HasAPIKeyPerm 供 handler 内二次细粒度判断（预留）
func HasAPIKeyPerm(c *gin.Context, perm string) bool {
	v, ok := c.Get("api_key_perms")
	if !ok {
		return false
	}
	perms, _ := v.([]string)
	for _, p := range perms {
		if p == PermAll || p == perm {
			return true
		}
	}
	return false
}
