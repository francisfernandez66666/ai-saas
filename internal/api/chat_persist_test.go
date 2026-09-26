// Package api FIX-5(2026-09-27) 行为回归：对话主链"写不成必须报错"与聊天记录门禁 fail-closed。
//
// 钉住三件事：
//  1. 客户入站消息落库失败 → HTTP 500「消息处理失败，请重试」，且会话里**不产生半条消息**
//     （旧写法把 GORM 的 error 丢掉，照样回 200，客户那句话在网页/顾问端/E8 留痕三处都看不见）；
//  2. /chat/history 的归属门禁是 fail-closed：目标客户行读失败时一律 403，
//     不再"读不到就当没这个客户"把 CheckVisitorKey + 四级数据范围两道路径整段跳过（旧写法回 200）；
//  3. /chat/history 的**读**同样不许静默：消息查询失败回 500，而不是 200 + 空列表
//     （库抖一下不该被前端解读成"这个客户什么都没说"）。
//
// 注错方式用 GORM 回调 + 原子开关（不改产品代码留测试口子），且每条用例都先自证开关有牙：
// 开关打开时那一句 Create/Query 必须真的失败，否则后面的 500/403 断言全是空转。
//
// 依赖本地 PostgreSQL（testutil.SetupTestDB，DB 不可用自动 Skip）；不真实外呼 AI
// ——注错点在入站消息落库那一步，请求走不到策略引擎与合并队列。
package api

import (
	"ai-scrm/internal/db"
	"ai-scrm/internal/metrics"
	"ai-scrm/internal/model"
	"ai-scrm/internal/testutil"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// errFix5Injected 注错回调写入的假错误。措辞刻意不含 SQLSTATE/表名，
// 免得用例把"注入的错误文本"误当成被测行为的一部分。
var errFix5Injected = errors.New("fix5 injected fault")

// 三个原子开关：分别打掉 消息写入 / 客户行读取 / 消息列表读取。
// 用 atomic 而不是普通 bool：回调在 GORM 内部执行，跨 goroutine 可见性要成立。
var (
	persistFailMessageCreate atomic.Bool
	persistFailCustomerRead  atomic.Bool
	persistFailMessageRead   atomic.Bool
)

const (
	fix5CreateHook = "test:fix5_fail_message_create"
	fix5QueryHook  = "test:fix5_fail_read"
)

// installFix5Faults 在 db.DB 上挂两个"按开关生效"的 GORM 回调，并在用例结束时摘除+复位。
// 开关全关时回调是纯 no-op，因此挂上它不影响同包其它用例；
// 摘除放 t.Cleanup，保证注错不会泄漏到同二进制的下一条用例。
//
// 注错只调 tx.AddError：GORM 内建的 gorm:create / gorm:query 回调开头就是
// `if db.Error != nil { return }`，所以 Before 钩子里塞进错误即等于真中断，
// 那一行既不会写出去、也不会读回来——这一点由 TestFix5_InjectedFaultHasTeeth 实测自证，
// 不靠注释成立。
func installFix5Faults(t *testing.T) {
	t.Helper()
	if db.DB == nil {
		t.Fatalf("db.DB 未初始化（SetupTestDB 未跑？）")
	}
	if err := db.DB.Callback().Create().Before("gorm:create").Register(fix5CreateHook, func(tx *gorm.DB) {
		if !persistFailMessageCreate.Load() {
			return
		}
		if _, ok := tx.Statement.Dest.(*model.Message); ok {
			tx.AddError(errFix5Injected)
		}
	}); err != nil {
		t.Fatalf("注册消息写入注错回调失败: %v", err)
	}
	if err := db.DB.Callback().Query().Before("gorm:query").Register(fix5QueryHook, func(tx *gorm.DB) {
		if _, ok := tx.Statement.Dest.(*model.Customer); ok {
			if persistFailCustomerRead.Load() {
				tx.AddError(errFix5Injected)
			}
			return
		}
		if _, ok := tx.Statement.Dest.(*[]model.Message); ok {
			if persistFailMessageRead.Load() {
				tx.AddError(errFix5Injected)
			}
		}
	}); err != nil {
		t.Fatalf("注册读取注错回调失败: %v", err)
	}
	t.Cleanup(func() {
		persistFailMessageCreate.Store(false)
		persistFailCustomerRead.Store(false)
		persistFailMessageRead.Store(false)
		_ = db.DB.Callback().Create().Remove(fix5CreateHook)
		_ = db.DB.Callback().Query().Remove(fix5QueryHook)
	})
}

// TestFix5_InjectedFaultHasTeeth 自证注错机器本身有效。
// 没有这一条，后面所有"500/403"都可能在"注入其实没生效"上空转（本仓反复踩过的假绿形态）。
func TestFix5_InjectedFaultHasTeeth(t *testing.T) {
	testutil.SetupTestDB(t)
	installFix5Faults(t)
	tid := testutil.CreateTenantCode(t, "pfx5teeth")
	defer testutil.CleanupTenant(t, tid)

	// ① 开关关：消息能正常落库
	msg := model.Message{TenantID: tid, ConversationID: 1, CustomerID: 1, SenderType: "customer", Content: "开关关闭时的对照消息"}
	if err := db.DB.Create(&msg).Error; err != nil {
		t.Fatalf("开关关闭时消息应能落库，实得: %v", err)
	}
	if msg.ID == 0 {
		t.Fatalf("开关关闭时消息应拿到主键")
	}
	defer db.DB.Unscoped().Delete(&model.Message{}, msg.ID)

	// ② 开关开：同一张表的 Create 必须失败
	msg2 := model.Message{TenantID: tid, ConversationID: 1, CustomerID: 1, SenderType: "customer", Content: "开关打开时的注错消息"}
	persistFailMessageCreate.Store(true)
	if err := db.DB.Create(&msg2).Error; err == nil {
		persistFailMessageCreate.Store(false)
		t.Fatalf("反证失败：注错开关打开后消息 Create 仍成功，后续 500 断言是空转")
	}
	if msg2.ID != 0 {
		persistFailMessageCreate.Store(false)
		t.Fatalf("注错后不应真的写出这一行（ID=%d）", msg2.ID)
	}

	// ③ 注错只打 model.Message：客户行写入不受牵连（否则用例 ② 的失败无从归属）
	cust := model.Customer{TenantID: tid, Name: "注错隔离对照"}
	if err := db.DB.Create(&cust).Error; err != nil {
		persistFailMessageCreate.Store(false)
		t.Fatalf("消息注错不应影响客户写入，实得: %v", err)
	}
	db.DB.Unscoped().Delete(&model.Customer{}, cust.ID)
	persistFailMessageCreate.Store(false)
}

// readCodeAndBody 发一次请求，回 (HTTP 状态码, 响应体)。
func readCodeAndBody(t *testing.T, r *gin.Engine, method, path, token string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	b, _ := io.ReadAll(w.Body)
	return w.Code, string(b)
}

// TestFix5_CustomerInboundPersistFailureReturns500 客户那句话写不进库时，
// 接口必须显式失败，并且**库里确实一条消息都没有**（不许留下"回成功了但半条消息"的状态）。
func TestFix5_CustomerInboundPersistFailureReturns500(t *testing.T) {
	testutil.SetupTestDB(t)
	installFix5Faults(t)
	tid := testutil.CreateTenantCode(t, "pfx5in")
	defer testutil.CleanupTenant(t, tid)

	cust := createChatSplitCustomer(t, tid, "pfx5in", 0)
	r := initChatSplitRouter(tid)
	tok := chatToken(t, 915001, tid)

	persistFailMessageCreate.Store(true)
	w := httptest.NewRecorder()
	body := fmt.Sprintf(`{"customer_id":%d,"content":"这句话写不进库"}`, cust.ID)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/chat", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("客户消息落库失败应回 500，实得 %d body=%s", w.Code, w.Body.String())
	}
	var env struct {
		Code      int    `json:"code"`
		Message   string `json:"message"`
		ErrorCode string `json:"error_code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("响应体解析失败: %v body=%s", err, w.Body.String())
	}
	if env.Message != chatPersistFailMsg {
		t.Fatalf("对外文案应为 %q，实得 %q", chatPersistFailMsg, env.Message)
	}
	if env.ErrorCode != "internal_error" {
		t.Fatalf("错误码应为统一推导的 internal_error（单点在 internal/errcodes），实得 %q", env.ErrorCode)
	}
	if strings.Contains(w.Body.String(), "fix5 injected fault") {
		t.Fatalf("内部错误细节不得回传给客户端: %s", w.Body.String())
	}

	// 会话可以已经建好（它在入站消息之前），但消息行必须一条都没有。
	var n int64
	if err := db.DB.Model(&model.Message{}).Where("customer_id = ?", cust.ID).Count(&n).Error; err != nil {
		t.Fatalf("统计消息行失败: %v", err)
	}
	if n != 0 {
		t.Fatalf("落库失败却留下了 %d 条消息行（半条状态）", n)
	}

	// 观测位必须真的数到了这一笔——旁路只留痕，必查同样要可数。
	if !strings.Contains(metrics.RenderPrometheus(), `ai_scrm_chat_persist_error_total{kind="customer_inbound"}`) {
		t.Fatalf("必查落库失败未进计数器观测位：/metrics 里找不到 ai_scrm_chat_persist_error_total{kind=\"customer_inbound\"}")
	}
	persistFailMessageCreate.Store(false)
}

// TestFix5_ChatHistoryFailClosedOnCustomerReadError /chat/history 的两道门禁不得被 DB 读错误绕过。
// 三条腿缺一不可：对照腿证明这条请求本来走得通（否则 403 是白给的），
// 注错腿证明读失败现在回 403（旧写法整段跳过门禁回 200 全量聊天记录），
// 反向腿证明访客密钥防线没被放宽。
func TestFix5_ChatHistoryFailClosedOnCustomerReadError(t *testing.T) {
	testutil.SetupTestDB(t)
	installFix5Faults(t)
	tid := testutil.CreateTenantCode(t, "pfx5hist")
	defer testutil.CleanupTenant(t, tid)

	cust := createChatSplitCustomer(t, tid, "pfx5hist", 0)
	r := initBatchFixRouter(tid)
	path := fmt.Sprintf("/api/v1/chat/history?customer_id=%d&visitor_key=%s", cust.ID, cust.VisitorKey)

	// 对照腿：正确密钥 → 200
	code, body := readCodeAndBody(t, r, http.MethodGet, path, "")
	if code != http.StatusOK {
		t.Fatalf("对照腿应先为 200（否则后面的 403 断言没有意义），实得 %d body=%s", code, body)
	}

	// 注错腿：客户行读失败 → 403（fail-closed）
	persistFailCustomerRead.Store(true)
	code, body = readCodeAndBody(t, r, http.MethodGet, path, "")
	persistFailCustomerRead.Store(false)
	if code != http.StatusForbidden {
		t.Fatalf("客户行读取失败应 fail-closed 回 403，实得 %d body=%s（旧写法在这里回 200 并放行全量记录）", code, body)
	}

	// 反向腿：错误密钥 + 不注错 → 仍 403
	bad := fmt.Sprintf("/api/v1/chat/history?customer_id=%d&visitor_key=%s", cust.ID, "wrong-key-xxxxxxxxxxxx")
	code, _ = readCodeAndBody(t, r, http.MethodGet, bad, "")
	if code != http.StatusForbidden {
		t.Fatalf("错误 visitor_key 仍应 403，实得 %d", code)
	}

	// 目标客户不存在 → 403（解析不出归属就不该放行）
	code, _ = readCodeAndBody(t, r, http.MethodGet, "/api/v1/chat/history?customer_id=99999999&visitor_key=whatever", "")
	if code != http.StatusForbidden {
		t.Fatalf("不存在的客户应 403（不回 200 空列表，避免把探测面留给枚举 ID 的人）")
	}
}

// TestFix5_ChatHistoryReadFailureReturns500 消息列表读失败必须显式失败。
// 旧写法 `query.Find(&messages)` 丢掉 error → 200 + []，
// 界面把它渲染成"这个客户一条消息都没发过"，与落库丢失那个缺陷是同一个现象。
func TestFix5_ChatHistoryReadFailureReturns500(t *testing.T) {
	testutil.SetupTestDB(t)
	installFix5Faults(t)
	tid := testutil.CreateTenantCode(t, "pfx5hist2")
	defer testutil.CleanupTenant(t, tid)

	cust := createChatSplitCustomer(t, tid, "pfx5hist2", 0)
	// 造一条活跃会话 + 一条消息，让对照腿读到非空
	conv := model.Conversation{TenantID: tid, CustomerID: cust.ID, Status: "active", Mode: "ai", Channel: "web"}
	if err := db.DB.Create(&conv).Error; err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	msg := model.Message{TenantID: tid, ConversationID: conv.ID, CustomerID: cust.ID, SenderType: "customer", Content: "历史里的真消息"}
	if err := db.DB.Create(&msg).Error; err != nil {
		t.Fatalf("建消息失败: %v", err)
	}

	r := initBatchFixRouter(tid)
	path := fmt.Sprintf("/api/v1/chat/history?conversation_id=%d&visitor_key=%s", conv.ID, cust.VisitorKey)

	code, body := readCodeAndBody(t, r, http.MethodGet, path, "")
	if code != http.StatusOK || !strings.Contains(body, "历史里的真消息") {
		t.Fatalf("对照腿应读到那条消息，实得 %d body=%s", code, body)
	}

	persistFailMessageRead.Store(true)
	code, body = readCodeAndBody(t, r, http.MethodGet, path, "")
	persistFailMessageRead.Store(false)
	if code != http.StatusInternalServerError {
		t.Fatalf("消息读取失败应回 500，实得 %d body=%s（旧写法回 200 + 空列表）", code, body)
	}
}

// ============================================================
// 口径函数本体的直接断言（守卫"能不能真的报错"的最小单元）
// ============================================================
//
// 上面的用例打的是整条 HTTP 链，能证明"客户那句话写不进库时接口回 500"，
// 但证明不了 persistRequired 这一格本身的行为——链上还有十几处别的可能。
// 这三条直接喂 res 给口径函数，把"必查回 500 / 旁路只计数 / 无响应器不得 panic"
// 三件事钉在同一个可判定的点上。

// fix5FailingDB 造一条必定失败的 GORM 结果（查一个不存在的列）。
// 用真 DB 而不是手搓 &gorm.DB{Error:…}：后者绕过了"错误确实来自驱动"这一层。
func fix5FailingDB(t *testing.T) *gorm.DB {
	t.Helper()
	if db.DB == nil {
		t.Fatalf("db.DB 未初始化（SetupTestDB 未跑？）")
	}
	res := db.DB.Table("messages").Where("fix5_no_such_column = 1").Find(&[]model.Message{})
	if res.Error == nil {
		t.Fatalf("反证失败：不存在的列竟然查成功了，注错机器没牙，本文件的 500/403 断言全是空转")
	}
	return res
}

// fix5GinCtx 造一个"能写响应"的 gin 上下文（CreateTestContext 的 Request 是 nil，
// 不补上就会走进 persistRequired 的"无响应器"分支，断不到应答本身）。
func fix5GinCtx() (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/chat", strings.NewReader("{}"))
	return c, w
}

// TestFix5_PersistRequiredResponds500 必查类：失败即 500、返回 true、文案与错误码统一。
func TestFix5_PersistRequiredResponds500(t *testing.T) {
	testutil.SetupTestDB(t)
	res := fix5FailingDB(t)
	c, w := fix5GinCtx()

	if !persistRequired(c, "customer_inbound", 1, 1, res) {
		t.Fatalf("落库失败必须返回 true（调用方据此中止本次答复）")
	}
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("应回 500，实得 %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), chatPersistFailMsg) {
		t.Fatalf("对外文案缺失: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "fix5_no_such_column") {
		t.Fatalf("数据库内部结构不得回传（探测面）: %s", w.Body.String())
	}

	// 反向：写成功时不得应答、必须返回 false 让调用方继续走。
	okC, okW := fix5GinCtx()
	if persistRequired(okC, "customer_inbound", 1, 1, db.DB.Where("1 = 0").Find(&[]model.Customer{})) {
		t.Fatalf("res.Error 为 nil 时 persistRequired 不得判定为失败")
	}
	if okW.Body.Len() != 0 {
		t.Fatalf("成功路径上口径函数不应写任何响应，实得 %s", okW.Body.String())
	}
}

// TestFix5_PersistRequiredWithoutResponderDoesNotPanic 无响应器时只留痕，不得 panic。
// 这一条防的是"把可诊断的落库失败升级成进程崩溃"——RespErr 内部是 c.JSON，喂 nil 即炸。
func TestFix5_PersistRequiredWithoutResponderDoesNotPanic(t *testing.T) {
	testutil.SetupTestDB(t)
	res := fix5FailingDB(t)

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("persistRequired 在无 gin 上下文时 panic 了（应降级为只留痕）: %v", r)
		}
	}()
	if !persistRequired(nil, "ai_reply", 1, 1, res) {
		t.Fatalf("即使无法应答也必须返回 true，否则调用方会把不存在的行拼进成功响应")
	}
	// 只带 Writer、不带 Request 的半成品上下文同样不得应答成 200。
	bare := &gin.Context{}
	if !persistRequired(bare, "ai_reply", 1, 1, res) {
		t.Fatalf("nil Request 同样必须中止答复")
	}
}

// TestFix5_PersistBypassCountsButStaysSilent 旁路类：不打断答复，但必须进计数器。
// "只打日志"的守卫等于没有守卫——计数是这类路径唯一的线上可见性。
func TestFix5_PersistBypassCountsButStaysSilent(t *testing.T) {
	testutil.SetupTestDB(t)
	res := fix5FailingDB(t)

	// kind 用测试专用名：生产 kind 的计数会被同包其它用例（真跑过旁路失败的话）累加，
	// 断"等于 1"就会依赖执行顺序——本仓称这类为"等值锁写成了单向锁"。
	const bypassKind = "fix5_bypass_probe"
	_, w := fix5GinCtx()
	persistBypass(bypassKind, 1, 1, res)
	if w.Body.Len() != 0 {
		t.Fatalf("旁路失败不得对客户报错，实得 %s", w.Body.String())
	}
	// 错误为 nil 时必须完全静默（防"每条正常写库都记一笔失败"这种反向自伤）。
	persistBypassErr(bypassKind, 1, 1, nil)

	rendered := metrics.RenderPrometheus()
	if !strings.Contains(rendered, `ai_scrm_chat_persist_error_total{kind="`+bypassKind+`"} 1`) {
		t.Fatalf("旁路失败应恰好计数 1（失败没计数、或成功路径被误计都会红）：\n%s", rendered)
	}
}
