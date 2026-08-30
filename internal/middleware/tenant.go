// Package middleware Gin 中间件链：TenantResolver(fail-closed)→JWTAuth→TenantConsistency→OrgResolve 等安全闸。
package middleware

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"

	"github.com/gin-gonic/gin"
)

// ============================================================
// 租户解析中间件（SaaS 安全红线版）
//
// 设计原则（fail-closed，宁拒勿漏）：
// 1. 租户只能来自 Host 域名解析（子域名 code 或白标自定义域名），查库验证
// 2. 解析失败一律 403，绝不默认注入某个租户（旧版默认租户1是越权漏洞）
// 3. X-Tenant-ID Header 仅在 GIN_MODE=debug 开发模式生效（生产可伪造，不信任）
// 4. 租户状态检查：suspended→403 / expired→非GET返回402 / trial过期→402
// 5. 解析结果带 30s TTL 进程内缓存，避免每请求查库；负缓存 10s 防恶意子域扫描打库
//
// 配套中间件 TenantConsistency（须在 JWTAuth 之后挂载）：
// 校验 JWT Claims 中的租户与 Host 解析租户一致性，并计算最终生效租户。
// ============================================================

// TenantInfo 租户解析结果（对外只暴露必要字段）
type TenantInfo struct {
	ID       uint
	Code     string
	Tier     string
	Status   string
	IsActive bool
}

// ---- 解析缓存（进程内，多实例下各实例独立缓存，30s 内租户封禁延迟生效，可接受）----

// tenantCacheEntry 结构体/类型定义（自动补注释）。
type tenantCacheEntry struct {
	tenant   *model.Tenant // nil = 负缓存（不存在/不可用）
	expireAt time.Time
}

// 常量/变量定义块（自动补注释）。
var (
	resolveCache sync.Map // key: "host:xxx" / "code:xxx" / "id:n" → *tenantCacheEntry
)

// 常量/变量定义块（自动补注释）。
const (
	tenantCacheTTL    = 30 * time.Second // 正缓存：租户信息 30 秒
	tenantNegCacheTTL = 10 * time.Second // 负缓存：不存在的域名 10 秒，防扫描打库
)

// getCachedTenant 读缓存（含过期判断）
func getCachedTenant(key string) *model.Tenant {
	if v, ok := resolveCache.Load(key); ok {
		e := v.(*tenantCacheEntry)
		if time.Now().Before(e.expireAt) {
			return e.tenant
		}
		resolveCache.Delete(key)
	}
	return nil
}

// setCachedTenant 写缓存（tenant 为 nil 时记负缓存）
func setCachedTenant(key string, t *model.Tenant) {
	ttl := tenantCacheTTL
	if t == nil {
		ttl = tenantNegCacheTTL
	}
	resolveCache.Store(key, &tenantCacheEntry{tenant: t, expireAt: time.Now().Add(ttl)})
}

// InvalidateTenantCache 租户信息变更时主动失效（供管理接口调用）
func InvalidateTenantCache() {
	resolveCache.Range(func(k, v any) bool {
		resolveCache.Delete(k)
		return true
	})
}

// ============================================================
// 租户查库加载（带缓存）
// ============================================================

// loadTenantByID 按 ID 加载租户
// 疑点：getCachedTenant 命中过期条目时会先 Delete 再返回 nil，
// 因此走到 cacheHit(key) 时缓存已空，|| cacheHit(key) 恒为 false，属冗余判断。
func loadTenantByID(id uint) *model.Tenant {
	key := "id:" + strconv.FormatUint(uint64(id), 10)
	if t := getCachedTenant(key); t != nil || cacheHit(key) {
		return t
	}
	var t model.Tenant
	err := db.DB.Where("id = ? AND deleted_at IS NULL", id).First(&t).Error
	if err != nil {
		setCachedTenant(key, nil)
		return nil
	}
	setCachedTenant(key, &t)
	return &t
}

// cacheHit 判断缓存是否存在有效条目（含负缓存）
func cacheHit(key string) bool {
	if _, ok := resolveCache.Load(key); ok {
		return true
	}
	return false
}

// loadTenantByCode 按子域名标识加载租户
func loadTenantByCode(code string) *model.Tenant {
	key := "code:" + code
	if v, ok := resolveCache.Load(key); ok {
		e := v.(*tenantCacheEntry)
		if time.Now().Before(e.expireAt) {
			return e.tenant
		}
		resolveCache.Delete(key)
	}
	var t model.Tenant
	err := db.DB.Where("code = ? AND deleted_at IS NULL", strings.ToLower(code)).First(&t).Error
	if err != nil {
		setCachedTenant(key, nil)
		return nil
	}
	setCachedTenant(key, &t)
	return &t
}

// loadTenantByCustomDomain 按白标自定义域名加载租户
func loadTenantByCustomDomain(host string) *model.Tenant {
	key := "host:" + host
	if v, ok := resolveCache.Load(key); ok {
		e := v.(*tenantCacheEntry)
		if time.Now().Before(e.expireAt) {
			return e.tenant
		}
		resolveCache.Delete(key)
	}
	var t model.Tenant
	err := db.DB.Where("custom_domain = ? AND deleted_at IS NULL", host).First(&t).Error
	if err != nil {
		setCachedTenant(key, nil)
		return nil
	}
	setCachedTenant(key, &t)
	return &t
}

// loadDefaultTenant 默认租户（id 最小的 active/trial 租户，即 rox-sales）
// 用途：debug 本地开发兜底、super_admin 未显式指定目标租户时的默认落点
func loadDefaultTenant() *model.Tenant {
	const key = "default"
	if v, ok := resolveCache.Load(key); ok {
		e := v.(*tenantCacheEntry)
		if time.Now().Before(e.expireAt) {
			return e.tenant
		}
		resolveCache.Delete(key)
	}
	var t model.Tenant
	err := db.DB.Where("status IN ? AND deleted_at IS NULL", []string{"active", "trial"}).
		Order("id ASC").First(&t).Error
	if err != nil {
		setCachedTenant(key, nil)
		return nil
	}
	setCachedTenant(key, &t)
	return &t
}

// ============================================================
// Host 解析
// ============================================================

// normalizeHost 去掉端口，转小写
func normalizeHost(host string) string {
	h := strings.ToLower(strings.TrimSpace(host))
	if i := strings.LastIndex(h, ":"); i > -1 {
		// 处理 IPv6 [::1]:8080 与普通 host:port
		if !strings.Contains(h, "]") || strings.Index(h, "]") < i {
			if !strings.Contains(h[i:], "]") {
				h = h[:i]
			}
		}
	}
	h = strings.Trim(h, "[]")
	return h
}

// extractSubdomainCode 从子域名提取租户码
// acme.example.com → acme；acme.localhost → acme（本地开发）；example.com → ""
func extractSubdomainCode(host string) string {
	parts := strings.Split(host, ".")
	if len(parts) < 2 {
		return ""
	}
	// 纯主域名（example.com）或 localhost：无子域
	if len(parts) == 2 && parts[1] != "localhost" {
		return ""
	}
	code := parts[0]
	// 主域自身（www/example 等）不是租户码
	if code == "" || code == "www" {
		return ""
	}
	return code
}

// isLocalDevHost 是否本地开发地址
func isLocalDevHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1" ||
		strings.HasSuffix(host, ".localhost")
}

// resolveTenant 解析租户（带缓存），失败返回 nil
func resolveTenant(c *gin.Context) *model.Tenant {
	host := normalizeHost(c.Request.Host)

	// 1. 开发模式：允许 X-Tenant-ID Header 指定租户（生产环境忽略，防伪造）
	if gin.Mode() == gin.DebugMode {
		if xt := strings.TrimSpace(c.GetHeader("X-Tenant-ID")); xt != "" {
			if id, err := strconv.ParseUint(xt, 10, 64); err == nil {
				if t := loadTenantByID(uint(id)); t != nil {
					return t
				}
				log.Printf("[TenantResolver] debug 模式 X-Tenant-ID=%s 无效，回退 Host 解析", xt)
			}
		}
	}

	// 2. 白标自定义域名精确匹配
	if t := loadTenantByCustomDomain(host); t != nil {
		return t
	}

	// 3. 子域名 → code 匹配
	if code := extractSubdomainCode(host); code != "" {
		if t := loadTenantByCode(code); t != nil {
			return t
		}
	}

	// 4. 本地开发兜底：localhost/127.0.0.1 → 默认租户（仅 debug 模式）
	if gin.Mode() == gin.DebugMode && isLocalDevHost(host) {
		return loadDefaultTenant()
	}

	return nil
}

// ============================================================
// 导出给公开接口：按 Host 解析租户（可能返回 nil，由调用方决定兜底）
// 注意：此函数仅做查库解析，不做 fail-closed 拦截，供 /api/v1/public/branding 等
// 需要"解析但不拒绝"的场景使用
// ============================================================

// ResolveTenantFromHost 按当前请求 Host 解析租户（白标自定义域名 / 子域名 code / 默认租户）
func ResolveTenantFromHost(c *gin.Context) *model.Tenant {
	return resolveTenant(c)
}

// ============================================================
// 中间件本体
// ============================================================

// skipTenantPaths 不需要租户上下文的路径（精确匹配）
var skipTenantPaths = map[string]bool{
	"/health":      true,
	"/status":      true, // M4 状态页：免鉴权无敏感信息
	"/":            true,
	"/pricing":     true,
	"/register":    true,
	"/login":       true,
	"/favicon.ico": true,
	// 认证入口：登录时尚无租户身份（登录后由 TenantConsistency 绑定）
	"/api/v1/auth/login":             true,
	"/api/v1/auth/register":          true,
	"/api/v1/auth/register-config":   true, // 注册页配置下发（M-邮箱）
	"/api/v1/auth/email-code":        true, // 注册验证码发送（M-邮箱）
	"/api/v1/auth/reset-password":    true,
	"/api/v1/auth/verify-reset-code": true,
	// 租户入驻闭环（SaaS 注册漏斗）
	"/api/v1/tenant/signup":     true,
	"/api/v1/tenant/check-code": true,
	"/api/v1/plans":             true,
	// 公开品牌配置下发（按 Host 解析租户白标；平台域无租户时返回平台默认品牌）
	"/api/v1/public/branding": true,
	// 支付网关异步回调：服务端到服务端，靠签名校验而非租户上下文，跳过 Host 解析
	"/api/v1/billing/webhook/mock":    true,
	"/api/v1/billing/webhook/gateway": true,
	"/api/v1/billing/webhook/wechat":  true,
	"/api/v1/billing/webhook/alipay":  true,
}

// skipTenantPrefixes 免租户解析的路径前缀（P-FE：Vue SPA 托管目录）
// SPA 自身仅含登录/注册等免登页与静态资源；业务数据由页面内的 API 调用
// 走各自的 JWT/TenantResolver 逻辑，不受此放行影响
var skipTenantPrefixes = []string{"/app/"}

// TenantResolver 全局租户解析中间件（fail-closed）
func TenantResolver() gin.HandlerFunc {
	return func(c *gin.Context) {
		// db 未初始化（极端场景防御）：放行但不注入租户
		if db.DB == nil {
			c.Next()
			return
		}

		path := c.Request.URL.Path
		if skipTenantPaths[path] {
			c.Next()
			return
		}
		for _, pre := range skipTenantPrefixes { // P-FE：SPA 静态资源放行
			if strings.HasPrefix(path, pre) {
				c.Next()
				return
			}
		}
		// OpenAPI 独立路由组（M4）：租户来自 API Key 归属而非 Host，
		// 由 OpenAPIAuth 中间件自行注入，跳过 Host 解析（渠道回调无租户头同理）
		if strings.HasPrefix(path, "/openapi/") {
			c.Next()
			return
		}

		tenant := resolveTenant(c)
		if tenant == nil {
			// fail-closed：解析不到租户一律拒绝，绝不默认注入
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"code":    403,
				"message": "无法识别访问租户",
				"data":    nil,
			})
			return
		}

		// P4 账号注销：cancel_at 次日生效——status 未变更同样拦截（数据保留不删除）
		if tenant.CancelAt != nil && time.Now().Format("2006-01-02") != tenant.CancelAt.Format("2006-01-02") {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"code":    403,
				"message": "账号已注销，不可访问（数据保留期请联系平台处理）",
				"data":    nil,
			})
			return
		}

		// 租户状态检查
		switch tenant.Status {
		case "active":
			// 正常
		case "trial":
			// 试用过期检查
			if tenant.TrialEndAt != nil && time.Now().After(*tenant.TrialEndAt) {
				c.AbortWithStatusJSON(http.StatusPaymentRequired, gin.H{
					"code":    402,
					"message": "试用期已结束，请续费开通",
					"data":    nil,
				})
				return
			}
		case "suspended":
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"code":    403,
				"message": "租户已停用，请联系平台",
				"data":    nil,
			})
			return
		case "review":
			// M1 注册审核：待超管发放试用前业务接口全拦（登录本身不受限）
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"code":    403,
				"message": "账号审核中，审核通过后即可使用（通常1个工作日）",
				"data":    nil,
			})
			return
		case "expired":
			// 宽限期语义：只读放行（GET 可查看导出），写操作拒绝
			if c.Request.Method != http.MethodGet {
				c.AbortWithStatusJSON(http.StatusPaymentRequired, gin.H{
					"code":    402,
					"message": "租期已过，请续费后继续使用",
					"data":    nil,
				})
				return
			}
		case "cancelled":
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"code":    403,
				"message": "租户已注销",
				"data":    nil,
			})
			return
		default:
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"code":    403,
				"message": "租户状态异常",
				"data":    nil,
			})
			return
		}

		// 注入 Context：
		// tenant_id 先给 Host 解析值（匿名/C端免登录接口的生效租户）；
		// 登录态路由随后由 JWTAuth（覆盖为 claims 值）+ TenantConsistency（最终裁决）接力
		applyTenantContext(c, tenant)

		c.Next()
	}
}

// applyTenantContext 把解析出的租户写进请求上下文（Resolver 与一致性中间件共用）
func applyTenantContext(c *gin.Context, tenant *model.Tenant) {
	c.Set("tenant_id", tenant.ID)
	c.Set("host_tenant_id", tenant.ID)
	c.Set("tenant_code", tenant.Code)
	c.Set("tenant_tier", tenant.Tier)
	c.Set("tenant_plan", tenant.PlanID)
}

// ============================================================
// JWT ↔ Host 一致性校验（须挂在 JWTAuth 之后）
// ============================================================

// TenantConsistency 计算最终生效租户并做一致性校验
// 规则：
//  1. super_admin：跨租户必须显式 X-Tenant-ID 指定目标租户（校验存在且可用）；
//     未指定时落到默认租户（而非全库透传），两种情况均写审计日志
//  2. 普通用户：JWT Claims 的 TenantID 必须等于 Host 解析租户，否则 403
//     （防 A 租户 token 打 B 租户子域名）
//  3. 旧账号过渡兼容：claims 无租户（=0，sys_users 时代数据）→ 绑定到当前访问租户
func TenantConsistency() gin.HandlerFunc {
	return func(c *gin.Context) {
		if db.DB == nil {
			c.Next()
			return
		}

		role, _ := c.Get("role")
		roleStr, _ := role.(string)
		hostV, _ := c.Get("host_tenant_id")
		hostID := toUint(hostV)

		// ---- super_admin 分支 ----
		if roleStr == "super_admin" {
			var effective uint
			var viaHeader bool
			if xt := strings.TrimSpace(c.GetHeader("X-Tenant-ID")); xt != "" {
				// 显式指定了目标租户：无效/停用一律拒绝（fail-closed，不静默回退）
				id, err := strconv.ParseUint(xt, 10, 64)
				if err != nil {
					c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
						"code":    403,
						"message": "X-Tenant-ID 格式非法",
						"data":    nil,
					})
					return
				}
				t := loadTenantByID(uint(id))
				if t == nil || (t.Status != "active" && t.Status != "trial") {
					c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
						"code":    403,
						"message": "目标租户不存在或已停用",
						"data":    nil,
					})
					return
				}
				effective = t.ID
				viaHeader = true
			} else {
				// 未显式指定：落到默认租户（而非全库透传），并记审计
				dt := loadDefaultTenant()
				if dt == nil {
					c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
						"code":    403,
						"message": "无可用租户",
						"data":    nil,
					})
					return
				}
				effective = dt.ID
			}
			auditSuperAdminAccess(c, effective, viaHeader)
			c.Set("tenant_id", effective)
			c.Next()
			return
		}

		// ---- 普通用户分支 ----
		tokV, _ := c.Get("tenant_id") // JWTAuth 写入的 claims 值
		tokID := toUint(tokV)

		switch {
		case tokID == 0:
			// fail-closed：普通用户 claims 无租户（旧 sys_users 数据未迁移）一律拒绝，
			// 绝不绑定到"当前访问的租户"（否则等于凭域名任选租户=越权）
			// 存量账号由 db.BackfillTenantIDs 统一归入默认租户，重新登录即恢复
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"code":    403,
				"message": "账号未绑定租户，请重新登录或联系管理员",
				"data":    nil,
			})
			return
		case hostID != 0 && tokID != hostID && gin.Mode() == gin.DebugMode && isLocalDevHost(normalizeHost(c.Request.Host)):
			// 本地开发特例：localhost 无真实域名归属，以登录租户为准刷新上下文，
			// 使多租户联调可行（生产环境子域名不受影响，仍严格一致性校验）
			if t := loadTenantByID(tokID); t != nil {
				applyTenantContext(c, t)
			}
			c.Set("tenant_id", tokID)
		case hostID != 0 && tokID != hostID:
			// 核心防线：token 租户 ≠ 访问域名租户
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"code":    403,
				"message": "租户不匹配：登录身份与访问站点不一致",
				"data":    nil,
			})
			return
		default:
			c.Set("tenant_id", tokID)
		}

		c.Next()
	}
}

// auditSuperAdminAccess 超管跨租户访问审计（异步写，失败不影响请求）
func auditSuperAdminAccess(c *gin.Context, targetTenantID uint, viaHeader bool) {
	userID, _ := c.Get("user_id")
	uid := toUint(userID)
	detail, _ := json.Marshal(map[string]any{
		"via_header":      viaHeader,
		"path":            c.Request.URL.Path,
		"method":          c.Request.Method,
		"host":            c.Request.Host,
		"x_tenant_header": c.GetHeader("X-Tenant-ID"),
	})
	go func() {
		defer func() { _ = recover() }()
		err := db.DB.Create(&model.TenantAuditLog{
			TenantID:  targetTenantID,
			UserID:    uid,
			Action:    "super_admin_access",
			Resource:  c.Request.URL.Path,
			Detail:    string(detail),
			IP:        c.ClientIP(),
			UserAgent: c.Request.UserAgent(),
		}).Error
		if err != nil {
			log.Printf("[Audit] 超管访问审计写入失败: %v", err)
		}
	}()
}

// ============================================================
// Context 读取工具
// ============================================================

// GetTenantInfo 从 Context 获取租户信息
func GetTenantInfo(c *gin.Context) *TenantInfo {
	id, _ := c.Get("tenant_id")
	code, _ := c.Get("tenant_code")
	tier, _ := c.Get("tenant_tier")
	codeStr, _ := code.(string)
	tierStr, _ := tier.(string)
	idu := toUint(id)
	return &TenantInfo{
		ID:       idu,
		Code:     codeStr,
		Tier:     tierStr,
		Status:   "active",
		IsActive: idu != 0,
	}
}

// EffectiveTenantID 从 Context 取生效租户 ID（无则 0）
func EffectiveTenantID(c *gin.Context) uint {
	v, exists := c.Get("tenant_id")
	if !exists {
		return 0
	}
	return toUint(v)
}

// toUint interface→uint 安全转换（nil/未知类型一律0兜底）
func toUint(v interface{}) uint {
	if v == nil {
		return 0
	}
	switch val := v.(type) {
	case uint:
		return val
	case int:
		return uint(val)
	case int64:
		return uint(val)
	case float64:
		return uint(val)
	}
	return 0
}
