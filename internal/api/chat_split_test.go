// Package api Chat 链路拆分回归测试（2026-09-22，PLAN_FIX P1-2 配套）
//
// 背景：Chat()/ChatUnauthorized() 两个 god function 已拆为 chatSessionCtx / chatUnauthorizedCtx
// 的阶段方法。拆分只做"剪切-粘贴"，行为零变化；本文件把拆分后各阶段的关键守卫路径
// 钉进自动化回归，防止后续重构或行业包定制时悄悄改变鉴权/越权语义：
//
//  1. chatResolveCustomer：参数绑定 400 / 客户不存在 404；
//  2. chatEnsureConversation：会话 IDOR 双向——跨租户会话 404（RQ 租户过滤）、
//     同租户他人会话且不在数据范围 404（P1-6 归属校验）；
//  3. chatUnauthorizedResolve：绑定 400 / 客户 404 / 访客密钥不匹配 403（P0-7 身份防线）。
//
// 依赖本地 PostgreSQL（testutil.SetupTestDB，DB 不可用自动 Skip）；不真实外呼 AI。
package api

import (
	"ai-scrm/internal/cache"
	"ai-scrm/internal/db"
	"ai-scrm/internal/middleware"
	"ai-scrm/internal/model"
	"ai-scrm/internal/runtimecfg"
	"ai-scrm/internal/testutil"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// initChatSplitRouter 组装 Chat 正式链路的最小路由：租户解析桩 + JWTAuth + Chat，
// 与 routes_tenant.go 的 v1 组生产中间件顺序等价（本组 /chat 由登录态消费）。
func initChatSplitRouter(tid uint) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/v1/chat",
		func(c *gin.Context) { c.Set("tenant_id", tid) },
		middleware.JWTAuth(),
		Chat)
	return r
}

// initChatUnauthRouter 组装匿名测试链路的最小路由：租户解析桩 + ChatUnauthorized
// （生产注册在 IPRateLimit 组内；单测不挂限流，避免用例间互相触发频控）。
func initChatUnauthRouter(tid uint) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/v1/chat/test",
		func(c *gin.Context) { c.Set("tenant_id", tid) },
		ChatUnauthorized)
	return r
}

// chatToken 生成指定租户/用户的登录态 token（C 端 user 角色即可过 JWTAuth）。
func chatToken(t *testing.T, uid uint, tid uint) string {
	t.Helper()
	tok, err := middleware.GenerateToken(uid, fmt.Sprintf("chatuser%d", uid), model.RoleUser, tid)
	if err != nil {
		t.Fatalf("生成 token 失败: %v", err)
	}
	return tok
}

// createChatSplitCustomer 建一个归属指定销售的可对话客户（VisitorKey 预置，供匿名链路用）。
func createChatSplitCustomer(t *testing.T, tid uint, tag string, assigned uint) *model.Customer {
	t.Helper()
	suffix := fmt.Sprintf("cs%d%s", tid, tag)
	cust := &model.Customer{
		TenantID: tid, Name: "拆分回归客户-" + suffix, Phone: "137" + fmt.Sprintf("%08d", tid%100000000),
		VisitorKey:     "vk-split-" + suffix,
		AssignedUserID: assigned,
	}
	if err := db.DB.Create(cust).Error; err != nil {
		t.Fatalf("创建客户失败: %v", err)
	}
	return cust
}

// TestChatSplit_ResolveCustomerGuards 钉住 chatResolveCustomer 的两条早退路径：
// 非法 JSON → 400；合法 JSON 但 customer_id 不存在 → 404（租户隔离查询 First 失败）。
func TestChatSplit_ResolveCustomerGuards(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenantCode(t, "chatsplit")
	defer testutil.CleanupTenant(t, tid)

	r := initChatSplitRouter(tid)
	tok := chatToken(t, 71, tid)

	// 场景1：非 JSON 报文 → 绑定失败 400
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/chat", strings.NewReader("not-json"))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("非法 JSON 应 400，实得 %d body=%s", w.Code, w.Body.String())
	}

	// 场景2：customer_id 不存在 → 404 客户不存在
	w = httptest.NewRecorder()
	body := fmt.Sprintf(`{"customer_id":%d,"content":"你好"}`, tid+100000)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/chat", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("不存在客户应 404，实得 %d body=%s", w.Code, w.Body.String())
	}
}

// TestChatSplit_ConversationIDOR 钉住 chatEnsureConversation 的 P1-6 会话归属校验双向：
// ① 同租户他人会话（会话主人不在我的数据范围）→ 404 会话不存在；
// ② 跨租户会话 ID（RQ 租户过滤查不到）→ 404 会话不存在。
func TestChatSplit_ConversationIDOR(t *testing.T) {
	testutil.SetupTestDB(t)
	tidA := testutil.CreateTenantCode(t, "split_a")
	tidB := testutil.CreateTenantCode(t, "split_b")
	defer testutil.CleanupTenant(t, tidA)
	defer testutil.CleanupTenant(t, tidB)

	// 租户A：客户71（归属销售77，登录态即77）与客户72（归属99，不在77的范围）
	uid := uint(77)
	custA := createChatSplitCustomer(t, tidA, "a", uid)
	defer func() { _ = db.DB.Delete(&model.Customer{}, custA.ID).Error }()
	custB := createChatSplitCustomer(t, tidA, "b", 99)
	defer func() { _ = db.DB.Delete(&model.Customer{}, custB.ID).Error }()

	convB := &model.Conversation{TenantID: tidA, CustomerID: custB.ID, Status: "active", Channel: "web", Mode: "ai"}
	if err := db.DB.Create(convB).Error; err != nil {
		t.Fatalf("创建他人会话失败: %v", err)
	}
	defer func() { _ = db.DB.Delete(&model.Conversation{}, convB.ID).Error }()

	// 租户B：独立会话（用于跨租户 IDOR 探测）
	custB2 := createChatSplitCustomer(t, tidB, "x", 31)
	defer func() { _ = db.DB.Delete(&model.Customer{}, custB2.ID).Error }()
	convX := &model.Conversation{TenantID: tidB, CustomerID: custB2.ID, Status: "active", Channel: "web", Mode: "ai"}
	if err := db.DB.Create(convX).Error; err != nil {
		t.Fatalf("创建租户B会话失败: %v", err)
	}
	defer func() { _ = db.DB.Delete(&model.Conversation{}, convX.ID).Error }()

	r := initChatSplitRouter(tidA)
	tok := chatToken(t, uid, tidA)

	// 场景1：同租户他人会话 → P1-6 归属校验 404
	w := httptest.NewRecorder()
	body := fmt.Sprintf(`{"customer_id":%d,"conversation_id":%d,"content":"你好"}`, custA.ID, convB.ID)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/chat", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("同租户他人会话应 404，实得 %d body=%s", w.Code, w.Body.String())
	}

	// 场景2：跨租户会话 ID → 租户过滤 404（防 IDOR 横穿租户边界）
	w = httptest.NewRecorder()
	body = fmt.Sprintf(`{"customer_id":%d,"conversation_id":%d,"content":"你好"}`, custA.ID, convX.ID)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/chat", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("跨租户会话应 404，实得 %d body=%s", w.Code, w.Body.String())
	}
}

// TestChatUnauthorizedResolve_Guards 钉住 chatUnauthorizedResolve（匿名测试链路）的
// 三条身份防线：绑定 400 / 客户 404 / 访客密钥不匹配 403（P0-7 修复回归）。
func TestChatUnauthorizedResolve_Guards(t *testing.T) {
	testutil.SetupTestDB(t)
	// 场景4 正确 visitor_key 会进入合并队列阶段，需要读租户级系统配置（正常启动链在 main 完成）
	runtimecfg.InitSystemConfigService()
	cache.InitTagCache()
	tid := testutil.CreateTenantCode(t, "splitu")
	defer testutil.CleanupTenant(t, tid)

	cust := createChatSplitCustomer(t, tid, "u", 42)
	defer func() { _ = db.DB.Delete(&model.Customer{}, cust.ID).Error }()

	r := initChatUnauthRouter(tid)

	// 场景1：绑定失败 → 400
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/chat/test", strings.NewReader(`{"content":""}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("空 content 应 400，实得 %d body=%s", w.Code, w.Body.String())
	}

	// 场景2：客户不存在 → 404
	w = httptest.NewRecorder()
	body := fmt.Sprintf(`{"customer_id":%d,"content":"你好"}`, tid+200000)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/chat/test", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("不存在客户应 404，实得 %d body=%s", w.Code, w.Body.String())
	}

	// 场景3：客户有 VisitorKey 但请求带错 key → 403（P0-7 访客防线不松）
	w = httptest.NewRecorder()
	body = fmt.Sprintf(`{"customer_id":%d,"content":"你好"}`, cust.ID)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/chat/test?visitor_key=wrong-key", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("错误 visitor_key 应 403，实得 %d body=%s", w.Code, w.Body.String())
	}

	// 场景4：正确 visitor_key → 通过身份防线（后续进入硬边界/队列阶段，不深入断言）
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/chat/test?visitor_key="+cust.VisitorKey, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code == http.StatusForbidden || w.Code == http.StatusNotFound || w.Code == http.StatusBadRequest {
		t.Fatalf("正确 visitor_key 不应在身份防线被拒，实得 %d body=%s", w.Code, w.Body.String())
	}
}
