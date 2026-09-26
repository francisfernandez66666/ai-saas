// Package api 会话冷启动临界区的锁释放回归（FIX-3，2026-09-27）
//
// 钉住的性质：正式对话链 `/chat` 在「客户没传会话ID → 查不到活跃会话 → 创建失败且复查也无果」
// 这条 500 早退分支上，**不得把该客户的分片互斥锁留在持有态**。
// 旧实现里 convMu.Lock() 与唯一的 convMu.Unlock() 之间夹着这条 `RespErr(500); return true`，
// 跳过了解锁；而锁按 customerID%128 分片，一把锁罩 1/128 的客户，且没有超时自愈——
// 于是一次数据库抖动（本仓 2026-09-26 实测抓到过 SQLSTATE 53300 连接槽打满）之后，
// 同分片客户发消息会永久卡在锁上，用户视角是"聊天再也不回"，只有重启才解。
//
// 四条用例分工：
//  1. TestConvLock_ReleasedAfterColdStartFailure：正向——走到 500 之后锁必须还能拿下，
//     且这次失败的冷启动不得留下半行会话（防"报错了但库里多了条 active"）。
//  2. TestConvLock_LeakedLockStrandsRequests：症状本身——锁被他人持有时 /chat 必须卡住无响应，
//     放锁后同一发立刻走完（证明用例 1 断的那把锁确实在请求路径上）。
//  3. TestConvLock_ProbeHasTeeth：反证——同分片另一把锁被真持有时探针必须报"拿不到"。
//     缺这条，用例 1 会在"探针恒真"上空转假绿。
//  4. TestConvLock_SourceStructureGuard：结构守卫——Lock() 必须紧邻 defer Unlock()、
//     临界区内不得出现裸 Unlock、外层阶段方法不得再手递这把锁；同样配反证（坏样本必须被报出来）。
//
// 依赖本地 PostgreSQL（testutil.SetupTestDB，DB 不可用自动 Skip）；不真实外呼 AI。
package api

import (
	"ai-scrm/internal/chatflow"
	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/testutil"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// injectFailingConvCreate 把冷启动建会话那一笔换成恒错，返回还原函数。
// 只有这样才能真的落到"创建失败 + 复查也无果"那条早退：库里本来就查不到活跃会话，
// 复查自然也是 ErrRecordNotFound，两个条件同时成立才走得到 FIX-3 的现场。
func injectFailingConvCreate(t *testing.T, msg string) func() {
	t.Helper()
	orig := createConversationForChat
	createConversationForChat = func(s *chatSessionCtx, conv *model.Conversation) error {
		return fmt.Errorf("%s", msg)
	}
	return func() { createConversationForChat = orig }
}

// convLockFree 非阻塞探测某客户的分片会话锁是否可用（拿得到立刻放回）。
// 用 TryLock 而不是再打一次 /chat：泄漏时后者会永久阻塞，测试挂死比红灯更难定位。
func convLockFree(customerID uint) bool {
	mu := chatflow.GetConversationMutex(customerID)
	if !mu.TryLock() {
		return false
	}
	mu.Unlock()
	return true
}

// postChat 以登录态向 /chat 发一句话，返回响应记录器。
// 不接 *testing.T：用例里有"从别的 goroutine 打一发"的场景，goroutine 里不能调 t.Fatalf。
func postChat(r *gin.Engine, tok string, customerID uint, content string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/chat",
		strings.NewReader(fmt.Sprintf(`{"customer_id":%d,"content":%q}`, customerID, content)))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

// TestConvLock_ReleasedAfterColdStartFailure 冷启动 500 之后锁必须已释放。
func TestConvLock_ReleasedAfterColdStartFailure(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenantCode(t, "convlk")
	defer testutil.CleanupTenant(t, tid)

	uid := uint(83)
	cust := createChatSplitCustomer(t, tid, "lk", uid)
	defer func() { _ = db.DB.Delete(&model.Customer{}, cust.ID).Error }()

	restore := injectFailingConvCreate(t, "注入：冷启动建会话失败")
	defer restore()

	r := initChatSplitRouter(tid)
	tok := chatToken(t, uid, tid)

	// 前置自检：这个客户此刻确实没有活跃会话——否则链路走"复用"分支，根本到不了 500，
	// 后面的锁断言就是在一条没走过的路上空转。
	var pre int64
	if err := db.DB.Model(&model.Conversation{}).
		Where("tenant_id = ? AND customer_id = ? AND status = ?", tid, cust.ID, "active").
		Count(&pre).Error; err != nil {
		t.Fatalf("前置会话计数失败: %v", err)
	}
	if pre != 0 {
		t.Fatalf("前置自检失败：客户%d 已有 %d 条活跃会话，走不到冷启动分支", cust.ID, pre)
	}

	w := postChat(r, tok, cust.ID, "你好")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("创建失败且复查无果应 500，实得 %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "会话初始化失败") {
		t.Fatalf("500 文案应为「会话初始化失败」，实得 %s", w.Body.String())
	}

	// 核心断言：临界区已退出，同一把分片锁还能拿下来。
	// （修复前此处必红——锁被永久持有；TryLock 非阻塞，所以是红灯而不是挂死。）
	if !convLockFree(cust.ID) {
		t.Fatalf("冷启动 500 后分片互斥锁仍在持有态：客户%d 后续请求会永久卡住（FIX-3 回归）", cust.ID)
	}

	// 同一笔失败请求不得留下半行会话
	var left int64
	if err := db.DB.Model(&model.Conversation{}).
		Where("tenant_id = ? AND customer_id = ?", tid, cust.ID).Count(&left).Error; err != nil {
		t.Fatalf("残留会话计数失败: %v", err)
	}
	if left != 0 {
		t.Fatalf("失败的冷启动不得留下会话行，实际残留 %d 条", left)
	}

	// 业务面复核：撤掉注错后，同一个客户必须还能正常建会话
	// （只断"锁能拿"会漏掉另一种坏法——锁释放了但会话永远建不出来）。
	restore()
	var reCust model.Customer
	if err := db.DB.First(&reCust, cust.ID).Error; err != nil {
		t.Fatalf("重读客户失败: %v", err)
	}
	gin.SetMode(gin.TestMode)
	gc, _ := gin.CreateTestContext(nil)
	gc.Set("tenant_id", tid)
	s := &chatSessionCtx{c: gc, tenantID: tid, customer: reCust}
	conv, replied := s.ensureConversationLocked()
	if replied {
		t.Fatalf("撤掉注错后再次冷启动不应仍判失败（已响应）")
	}
	if conv.ID == 0 {
		t.Fatalf("撤掉注错后应建成会话，实得零ID")
	}
	defer func() { _ = db.DB.Delete(&model.Conversation{}, conv.ID).Error }()
}

// TestConvLock_LeakedLockStrandsRequests 把"锁泄漏＝客户永久卡住"这件事本身钉住：
// 模拟上一个请求把分片锁带走不放手（FIX-3 修复前的退出状态），此时同分片客户的 /chat
// 必须**拿不到响应**；放掉这把锁后同一次调用必须立刻走完。
//
// 这条是探针用例的反证补充：只断 `TryLock()==false` 说的是"锁被占着"，
// 而本条说的是"锁被占着时用户侧的症状真的是无响应"——两件事都要在场，
// 否则用例 1 换成一个不看锁的实现也可能照样绿。
// 建会话那一笔仍注错，所以放开锁之后这一发落到 500 而不是走完整条 AI 链。
func TestConvLock_LeakedLockStrandsRequests(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenantCode(t, "convst")
	defer testutil.CleanupTenant(t, tid)

	uid := uint(84)
	cust := createChatSplitCustomer(t, tid, "st", uid)
	defer func() { _ = db.DB.Delete(&model.Customer{}, cust.ID).Error }()

	restore := injectFailingConvCreate(t, "注入：本用例只关心锁会不会卡住请求")
	defer restore()

	r := initChatSplitRouter(tid)
	tok := chatToken(t, uid, tid)

	type resp struct{ code int }
	done := make(chan resp, 1)

	// 先占锁再起请求（顺序反了会有竞态：请求抢先把锁拿走就变成"正常走完"，用例假红）。
	// 同分片别名（customerID+128 与它同一把锁）由探针用例自证。
	strand := chatflow.GetConversationMutex(cust.ID + 128)
	strand.Lock()
	go func() { done <- resp{postChat(r, tok, cust.ID, "在吗").Code} }()

	select {
	case got := <-done:
		strand.Unlock()
		t.Fatalf("分片锁被他人持有时请求仍完成了（code=%d）：说明 /chat 没走这把锁的临界区，前提不成立", got.code)
	case <-time.After(2 * time.Second):
		// 预期：请求卡在 convMu.Lock() 上——这就是修复后不可能再出现的"永久无响应"形态
	}

	// 放掉锁，同一发请求必须走完（本用例里是 500，因为建会话被注错）
	strand.Unlock()
	select {
	case got := <-done:
		if got.code != http.StatusInternalServerError {
			t.Fatalf("放开锁后这一发应落到冷启动失败的 500，实得 %d", got.code)
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("释放模拟持锁者后请求仍未返回，链路或本用例实现有问题")
	}
}

// TestConvLock_ProbeHasTeeth 反证：探针在锁真被持有时必须报"拿不到"，
// 同时证明「同分片」这件事是真的——customerID 与 customerID+128 拿到的是同一把锁，
// 这正是"一次泄漏锁住 1/128 客户"的 blast radius 来源。
func TestConvLock_ProbeHasTeeth(t *testing.T) {
	const cid = uint(10001)
	sameShard := cid + 128
	if chatflow.GetConversationMutex(cid) != chatflow.GetConversationMutex(sameShard) {
		t.Fatalf("分片前提不成立：GetConversationMutex 不再是 customerID%%128")
	}

	// 先自证"空闲时探针为真"，否则下面的 false 没有意义
	if !convLockFree(cid) {
		t.Fatalf("探针在空闲锁上返回 false，用例本身坏了")
	}

	mu := chatflow.GetConversationMutex(cid)
	mu.Lock()
	// 持锁期间，用「同分片另一客户」的身份去探——必须拿不到。
	// 修复后的坏形态已经不存在，这里模拟的是修复前那种"被别人持着且永不放手"。
	held := !chatflow.GetConversationMutex(sameShard).TryLock()
	if !held {
		mu.Unlock()
		t.Fatalf("反证失败：锁被持有时探针仍报可用，用例 1 的断言是空转")
	}
	mu.Unlock() // 探针没拿到锁，也就没有它的解锁权——只放自己锁住的那一把

	if !convLockFree(cid) {
		t.Fatalf("解锁后探针应恢复可用，实得 false（探针实现有误）")
	}
}

// ============================================================
// 结构守卫：把"加锁即配 defer"变成机器检查，而不是靠下一个人记得
// ============================================================

// convLockStructureIssues 扫描对话链路源码文本，返回违反锁纪律的行描述。三条纪律：
//  1. 每个 `convMu.Lock()` 的下一行必须是 `defer convMu.Unlock()`；
//  2. 不得出现非 defer 的 `convMu.Unlock()`（手动解锁点＝早退分支可绕过的口子）；
//  3. `chatEnsureConversation` 函数体内不得再出现 convMu（锁的归属收在临界区方法里）。
func convLockStructureIssues(src string) []string {
	var bad []string
	lines := strings.Split(src, "\n")
	for i, ln := range lines {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "//") {
			continue // 注释里提锁是允许的（本仓注释规范要求写清为什么）
		}
		if strings.Contains(t, "convMu.Lock()") {
			if i+1 >= len(lines) || !strings.HasPrefix(strings.TrimSpace(lines[i+1]), "defer convMu.Unlock()") {
				bad = append(bad, fmt.Sprintf("第%d行 Lock() 后未紧跟 defer Unlock()", i+1))
			}
			continue
		}
		if strings.Contains(t, "convMu.Unlock()") && !strings.HasPrefix(t, "defer ") {
			bad = append(bad, fmt.Sprintf("第%d行出现裸 Unlock()（早退分支可绕过）", i+1))
		}
	}
	if body := topFuncBody(lines, "func (s *chatSessionCtx) chatEnsureConversation() bool {"); body != "" {
		for _, l := range strings.Split(body, "\n") {
			if strings.Contains(l, "convMu") {
				bad = append(bad, "chatEnsureConversation 内仍手递 convMu（锁应由 ensureConversationLocked 独占）")
				break
			}
		}
	}
	return bad
}

// topFuncBody 从签名行起取到该函数的收尾 `}`（顶格）为止。
// 必须停在收尾花括号而不是"下一个 func"：函数之间的**文档注释**也要算在上一段里，
// 而本仓注释规范恰恰要求把旧缺陷写在下一个方法的注释里（含 "convMu" 字样），
// 截多了会让纪律 3 误伤。找不到签名返回空串（调用方须自证非空，防空转守卫）。
func topFuncBody(lines []string, signature string) string {
	start := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == signature {
			start = i
			break
		}
	}
	if start < 0 {
		return ""
	}
	var sb strings.Builder
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "}") {
			break
		}
		sb.WriteString(lines[i])
		sb.WriteString("\n")
	}
	return sb.String()
}

// TestConvLock_SourceStructureGuard 对真实源文件跑守卫，并用坏样本自证守卫不是空转。
func TestConvLock_SourceStructureGuard(t *testing.T) {
	// 好样本＝当前修复形态
	good := `func (s *chatSessionCtx) ensureConversationLocked() (model.Conversation, bool) {
	convMu := chatflow.GetConversationMutex(s.customer.ID)
	convMu.Lock()
	defer convMu.Unlock()
	return conversation, false
}

func (s *chatSessionCtx) chatEnsureConversation() bool {
	conv, replied := s.ensureConversationLocked()
	if replied {
		return true
	}
	_ = conv
	return false
}
`
	if got := convLockStructureIssues(good); len(got) != 0 {
		t.Fatalf("好样本被判违规（守卫误伤）：%v", got)
	}

	// 坏样本＝修复前的真实形态：Lock 在上、Unlock 在函数末尾、中途 return true
	bad := `func (s *chatSessionCtx) chatEnsureConversation() bool {
	convMu := chatflow.GetConversationMutex(s.customer.ID)
	convMu.Lock()
	if fail {
		RespErr(s.c, 500, "会话初始化失败")
		return true
	}
	convMu.Unlock()
	return false
}
`
	if got := convLockStructureIssues(bad); len(got) == 0 {
		t.Fatalf("反证失败：修复前的坏形态没被判违规，守卫是空转")
	}

	src, err := os.ReadFile("chat_main.go")
	if err != nil {
		t.Skipf("读不到 chat_main.go（源码不在位，跳过结构守卫）: %v", err)
	}
	if got := convLockStructureIssues(string(src)); len(got) != 0 {
		t.Fatalf("chat_main.go 违反会话锁纪律：%v", got)
	}
	// 守卫自身也要"真的扫到了东西"：签名没匹配上时 topFuncBody 会返回空串、纪律3 静默失效。
	if topFuncBody(strings.Split(string(src), "\n"), "func (s *chatSessionCtx) chatEnsureConversation() bool {") == "" {
		t.Fatalf("结构守卫失效：在 chat_main.go 里找不到 chatEnsureConversation 签名（改名了？守卫需同步）")
	}
	if !strings.Contains(string(src), "defer convMu.Unlock()") {
		t.Fatalf("结构守卫失效：chat_main.go 里已不存在 defer convMu.Unlock()")
	}
}
