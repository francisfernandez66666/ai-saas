// Package api 2026-09-08 审计修复批次自动化测试
//
// 覆盖本次四个修复点：
//   P0-1 /chat/history 鉴权（顾问端拉聊天历史 403）：OptionalJWTAuth 注入登录态放行，
//        匿名 visitor_key 校验不变、错 visitor_key 仍 403
//   P1-1 顾问标签弹窗恒空（/admin/tags 403）：advisor 只读路由 200、admin 路由对 sales 仍 403
//   P1-2 注册选行业落包：register-config 下发 industries（general 兜底 + active 行业包）
//   P0-2 简单消息延迟（chat_main.go:582）：延迟依赖的 GetSimpleReplyDelay/CancellableSleep
//        已在 internal/service/delay_rules_test.go 与 internal/chatflow 单测覆盖，本文件不再重复
//
// 依赖：本地 PostgreSQL（testutil.SetupTestDB，DB 不可用时自动 Skip）。
package api

import (
	"ai-scrm/internal/db"
	"ai-scrm/internal/middleware"
	"ai-scrm/internal/model"
	"ai-scrm/internal/testutil"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// initBatchFixRouter 组装与 cmd/server/main.go 等价的批量路由（仅本批涉及的三条）
// tenantCtx 前置设置租户（等价 TenantResolver），再按生产顺序叠加 OptionalJWTAuth / JWTAuth / AdminRequired
func initBatchFixRouter(tid uint) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	// 匿名+C端共用的 /chat/history：先租户解析，再可选鉴权（P0-1）
	r.GET("/api/v1/chat/history",
		func(c *gin.Context) { c.Set("tenant_id", tid) }, // 租户解析桩（等价 TenantResolver）
		middleware.OptionalJWTAuth(),
		GetChatHistory)

	// advisor 组：登录鉴权，无 AdminRequired（P1-1 新增只读标签路由）
	adv := r.Group("/api/v1/advisor")
	adv.Use(middleware.JWTAuth())
	adv.GET("/tags", GetTagList)

	// admin 组：登录鉴权 + AdminRequired（P1-1 对照组：sales 访问必须仍 403）
	adm := r.Group("/api/v1/admin")
	adm.Use(middleware.JWTAuth(), middleware.AdminRequired())
	adm.GET("/tags", GetTagList)

	return r
}

// TestRegisterConfigIndustries P1-2：register-config 下发行业列表
// 断言：general 恒为首项兜底；active 的行业级包可见；disabled 或非行业级包不可见
func TestRegisterConfigIndustries(t *testing.T) {
	testutil.SetupTestDB(t)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/v1/auth/register-config", RegisterConfig)

	// 造行业包数据：一个 active 行业包、一个 disabled 行业包、一个 enterprise 级包
	seedPacks := []*model.IndustryPack{
		{Code: "testind_active", Name: "测试行业包", Industry: "testind_active", Version: "1.0.0", PackLevel: "industry", Status: "active"},
		{Code: "testind_disabled", Name: "下架行业包", Industry: "testind_disabled", Version: "1.0.0", PackLevel: "industry", Status: "disabled"},
		{Code: "testent", Name: "企业级包", Industry: "testent", Version: "1.0.0", PackLevel: "enterprise", Status: "active"},
	}
	var ids []uint
	for _, p := range seedPacks {
		if err := db.DB.Create(p).Error; err != nil {
			t.Fatalf("seed industry_packs 失败: %v", err)
		}
		ids = append(ids, p.ID)
	}
	defer func() {
		// 收尾清理：只删本测试造的包，不动 dev 库既有行业包数据
		for _, id := range ids {
			_ = db.DB.Delete(&model.IndustryPack{}, id).Error
		}
	}()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/register-config", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("register-config 期望 200，实得 %d", w.Code)
	}

	var resp struct {
		Code int `json:"code"`
		Data struct {
			EmailVerifyEnabled bool `json:"email_verify_enabled"`
			Industries         []struct {
				Code string `json:"code"`
				Name string `json:"name"`
			} `json:"industries"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v body=%s", err, w.Body.String())
	}
	if resp.Code != 0 {
		t.Fatalf("期望 code=0，实得 %d body=%s", resp.Code, w.Body.String())
	}
	inds := resp.Data.Industries
	if len(inds) == 0 || inds[0].Code != "general" {
		t.Fatalf("industries 首项必须是 general 兜底，实得 %+v", inds)
	}
	// active 行业包必须可见
	if !hasIndustryCode(inds, "testind_active") {
		t.Fatalf("active 行业包 testind_active 应出现在列表中: %+v", inds)
	}
	// disabled / 非行业级 包不可见
	if hasIndustryCode(inds, "testind_disabled") {
		t.Fatalf("disabled 行业包不应下发: %+v", inds)
	}
	if hasIndustryCode(inds, "testent") {
		t.Fatalf("enterprise 级包不应下发: %+v", inds)
	}
}

// hasIndustryCode 判断行业列表中是否含指定 code
func hasIndustryCode(inds []struct {
	Code string `json:"code"`
	Name string `json:"name"`
}, code string) bool {
	for _, i := range inds {
		if i.Code == code {
			return true
		}
	}
	return false
}

// TestChatHistoryAuthFix P0-1：/chat/history 三方契约
//  1. 顾问带 Bearer token → 200（修复前恒 403）
//  2. 匿名带正确 visitor_key → 200（C 端契约不回归）
//  3. 匿名带错误 visitor_key → 403（横向越权防线不松）
//  4. 缺参数 → 400
func TestChatHistoryAuthFix(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenantCode(t, "histfix")
	defer testutil.CleanupTenant(t, tid)

	suffix := fmt.Sprintf("h%d", tid)
	cust := &model.Customer{
		TenantID: tid, Name: "历史测试客户-" + suffix, Phone: "139" + suffix,
		VisitorKey: "vk-hist-" + suffix,
	}
	if err := db.DB.Create(cust).Error; err != nil {
		t.Fatalf("创建客户失败: %v", err)
	}
	conv := &model.Conversation{TenantID: tid, CustomerID: cust.ID, Status: "active", Channel: "web", Mode: "ai"}
	if err := db.DB.Create(conv).Error; err != nil {
		t.Fatalf("创建会话失败: %v", err)
	}
	msg := &model.Message{
		TenantID: tid, ConversationID: conv.ID, CustomerID: cust.ID,
		Content: "这是历史消息-" + suffix, SenderType: "customer", SenderID: cust.ID,
	}
	if err := db.DB.Create(msg).Error; err != nil {
		t.Fatalf("创建消息失败: %v", err)
	}
	defer func() {
		_ = db.DB.Delete(&model.Message{}, msg.ID).Error
		_ = db.DB.Delete(&model.Conversation{}, conv.ID).Error
		_ = db.DB.Delete(&model.Customer{}, cust.ID).Error
	}()

	r := initBatchFixRouter(tid)
	url := fmt.Sprintf("/api/v1/chat/history?customer_id=%d", cust.ID)

	// 场景1：顾问登录态（带 Bearer）→ 必须 200（本次修复的核心回归）
	salesTok, err := middleware.GenerateToken(42, "sales1", "sales", tid)
	if err != nil {
		t.Fatalf("生成销售 token 失败: %v", err)
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+salesTok)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !containsStr(w.Body.String(), "这是历史消息-"+suffix) {
		t.Fatalf("顾问登录态应 200 且返回消息，实得 %d body=%s", w.Code, w.Body.String())
	}

	// 场景2：匿名 + 正确 visitor_key → 200（C 端不回归）
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, url+"&visitor_key="+cust.VisitorKey, nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !containsStr(w.Body.String(), "这是历史消息-"+suffix) {
		t.Fatalf("匿名正确 visitor_key 应 200，实得 %d body=%s", w.Code, w.Body.String())
	}

	// 场景3：匿名 + 错误 visitor_key → 403（越权防线不松）
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, url+"&visitor_key=wrong", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("匿名错误 visitor_key 应 403，实得 %d body=%s", w.Code, w.Body.String())
	}

	// 场景4：顾问登录态但缺参数 → 400
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/chat/history", nil)
	req.Header.Set("Authorization", "Bearer "+salesTok)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("缺 customer_id/conversation_id 应 400，实得 %d body=%s", w.Code, w.Body.String())
	}
}

// TestAdvisorTagsRouteAccess P1-1：顾问标签只读路由放开、管理端路由不放
//  1. sales 访问 /api/v1/advisor/tags → 200 且返回标签（修复前弹窗恒空）
//  2. sales 访问 /api/v1/admin/tags → 403（管理端写权限不放开）
//  3. 匿名访问两条路由 → 401（登录闸仍生效）
func TestAdvisorTagsRouteAccess(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenantCode(t, "tagfix")
	defer testutil.CleanupTenant(t, tid)

	suffix := fmt.Sprintf("t%d", tid)
	tag := &model.Tag{
		TenantID: tid, Name: "高意向-" + suffix, Code: "high-" + suffix,
		Category: "意向", Weight: 1.0, Status: 1,
	}
	if err := db.DB.Create(tag).Error; err != nil {
		t.Fatalf("创建标签失败: %v", err)
	}
	defer func() { _ = db.DB.Delete(&model.Tag{}, tag.ID).Error }()

	r := initBatchFixRouter(tid)
	salesTok, err := middleware.GenerateToken(43, "sales2", "sales", tid)
	if err != nil {
		t.Fatalf("生成销售 token 失败: %v", err)
	}

	// 场景1：sales 走 advisor 只读路由 → 200
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/advisor/tags", nil)
	req.Header.Set("Authorization", "Bearer "+salesTok)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !containsStr(w.Body.String(), tag.Name) {
		t.Fatalf("sales 访问 /advisor/tags 应 200 且含标签，实得 %d body=%s", w.Code, w.Body.String())
	}

	// 场景2：sales 访问 admin 路由 → 403（AdminRequired 不放）
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/admin/tags", nil)
	req.Header.Set("Authorization", "Bearer "+salesTok)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("sales 访问 /admin/tags 应 403，实得 %d body=%s", w.Code, w.Body.String())
	}

	// 场景3：匿名访问两条路由 → 401（登录闸不因本批修复被放开）
	for _, p := range []string{"/api/v1/advisor/tags", "/api/v1/admin/tags"} {
		w = httptest.NewRecorder()
		req = httptest.NewRequest(http.MethodGet, p, nil)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("匿名访问 %s 应 401，实得 %d", p, w.Code)
		}
	}
}

// containsStr 简易子串判断（避免引入额外依赖）
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
