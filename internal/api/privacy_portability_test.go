// PIPL 数据可携带权端点的判别力测试（FIX-9，2026-09-27）。
//
// 这组用例盯的不是"能不能拿到数据"，而是三件一旦写歪就**不会报错**的事：
//  1. 身份闸在取数之前（摘掉 visitor_key 校验 → 用例②红）；
//  2. 副本读的是**热表 + 冷归档表**（只读 messages → 用例①的归档那条消失，红）；
//  3. 投影是显式清单（改成整行 json.Marshal model.Customer → 用例①的"不含内部字段"红，
//     而且是"以后加列自动泄露"那种延迟爆雷，只有负向断言挡得住）。
//
// 另有一条产品口径值得写死：**对本人不掩码**。D7 的商家导出掩手机号是对的，
// 把同一套掩码搬到本人副本上就会得到一份"我自己填的号码被改掉"的假副本（用例①断明文）。
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/testutil"
)

// seedPortabilityData 造一个带内部字段的客户 + 两条热消息 + 一条已归档消息。
// 回 (客户ID, 全部消息ID〔按 id 升序，含归档那条〕)。
//
// 清理：testutil.CleanupTenant 覆盖的是带 tenant_id 的模型表，messages_archive 是
// 迁移011 的裸归档表、不在其清单里，故这里按客户自己收尾（残留会让下一轮"三行齐全"变四行）。
func seedPortabilityData(t *testing.T, tenantID uint, vk string, assigned uint) (uint, []uint) {
	t.Helper()
	cu := model.Customer{
		TenantID:    tenantID,
		Name:        "可携带测试客",
		Phone:       "13800002222",
		Remark:      "内部备注：销售判断他预算不足", // 商家侧判断，绝不该出现在本人副本里
		IntentScore: 77,               // 商家给这个人打的排序分：故意给一个**非零可识别值**，
		//                                             这样"字段被整行 Marshal 带出去"时断言必红，
		//                                             而不是靠"0 恰好看不见"蒙过去
		TVectorJSON:    `{"dims":[0.1,0.2]}`, // 策略引擎中间产物
		VisitorKey:     vk,                   // 凭证本身更不能回显
		AssignedUserID: assigned,             // 0=未归属（登录态用例要一个"归属别人"的客户，传非零）
		JourneyStage:   "lead_captured",
		Source:         "unit_test",
		Tags:           `["高意向"]`,
	}
	if err := db.DB.Create(&cu).Error; err != nil {
		t.Fatalf("造客户失败: %v", err)
	}
	conv := model.Conversation{TenantID: tenantID, CustomerID: cu.ID, Status: "closed", Channel: "web"}
	if err := db.DB.Create(&conv).Error; err != nil {
		t.Fatalf("造会话失败: %v", err)
	}
	var ids []uint
	for i, m := range []model.Message{
		{SenderType: "customer", Content: "我的第一句 =SUM(1,2)", MessageType: "text", RouteResult: "ai", TemplateID: "TPL_SECRET"},
		{SenderType: "human", Content: "顾问的第二句", MessageType: "text"},
	} {
		m.TenantID, m.CustomerID, m.ConversationID = tenantID, cu.ID, conv.ID
		m.CreatedAt = time.Now().Add(time.Duration(i) * time.Second)
		if err := db.DB.Create(&m).Error; err != nil {
			t.Fatalf("造热消息失败: %v", err)
		}
		ids = append(ids, m.ID)
	}
	// 归档那条**故意给一个比热表更小的 id**：这才是生产形态——归档搬的是**老消息**，
	// 搬移时保留原主键（迁移011 结构逐列对齐），所以冷表里的 id 天然排在热表前面。
	// 若把归档 id 造在热表之后，"两表合并后必须显式按 id 重排"这条就永远测不到
	// （append 序恰好也是 id 序），摘掉 sort.Slice 用例照样全绿。
	arcID := ids[0] - 1
	if err := db.DB.Table("messages_archive").Create(map[string]any{
		"id": arcID, "tenant_id": tenantID, "conversation_id": conv.ID, "customer_id": cu.ID,
		"sender_type": "customer", "content": "归档里的历史", "message_type": "text",
		"created_at": time.Now(), "updated_at": time.Now(),
	}).Error; err != nil {
		t.Fatalf("造归档消息失败: %v", err)
	}
	// 归档那条的 id 更小（见上），所以它排在最前面
	ids = append([]uint{arcID}, ids...)
	t.Cleanup(func() {
		db.DB.Where("customer_id = ?", cu.ID).Delete(&model.Message{})
		db.DB.Table("messages_archive").Where("customer_id = ?", cu.ID).Delete(nil)
		db.DB.Where("customer_id = ?", cu.ID).Delete(&model.Conversation{})
		db.DB.Delete(&model.Customer{}, cu.ID)
	})
	return cu.ID, ids
}

// seedPortabilityHotOnly 造一个**只有热表消息、零归档**的客户（生产默认形态：
// message_archive_days=0 时归档表根本不动）。回 (客户ID, 消息ID 升序切片)。
func seedPortabilityHotOnly(t *testing.T, tenantID uint, vk string, n int) (uint, []uint) {
	t.Helper()
	cu := model.Customer{
		TenantID: tenantID, Name: "纯热种子客", Phone: "13800003333",
		VisitorKey: vk, Source: "unit_test",
	}
	if err := db.DB.Create(&cu).Error; err != nil {
		t.Fatalf("造客户失败: %v", err)
	}
	conv := model.Conversation{TenantID: tenantID, CustomerID: cu.ID, Status: "active", Channel: "web"}
	if err := db.DB.Create(&conv).Error; err != nil {
		t.Fatalf("造会话失败: %v", err)
	}
	ids := make([]uint, 0, n)
	for i := 0; i < n; i++ {
		m := model.Message{
			SenderType: "customer", Content: fmt.Sprintf("纯热第%d句", i+1),
			MessageType: "text",
		}
		m.TenantID, m.CustomerID, m.ConversationID = tenantID, cu.ID, conv.ID
		m.CreatedAt = time.Now().Add(time.Duration(i) * time.Second)
		if err := db.DB.Create(&m).Error; err != nil {
			t.Fatalf("造热消息失败: %v", err)
		}
		ids = append(ids, m.ID)
	}
	t.Cleanup(func() {
		db.DB.Where("customer_id = ?", cu.ID).Delete(&model.Message{})
		db.DB.Where("customer_id = ?", cu.ID).Delete(&model.Conversation{})
		db.DB.Delete(&model.Customer{}, cu.ID)
	})
	return cu.ID, ids
}

// callMyData 调一次本人副本端点；visitorKey 为空表示登录态（不带自证串）。
func callMyData(t *testing.T, tenantID, cid uint, visitorKey string, extra string) (int, string, gin.H) {
	t.Helper()
	c, w := newCtx(t, tenantID)
	body := fmt.Sprintf(`{"customer_id":%d,"visitor_key":"%s"%s}`, cid, visitorKey, extra)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/privacy/my-data", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	PrivacyMyData(c)
	var env map[string]any
	data := gin.H{}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err == nil {
		if d, ok := env["data"].(map[string]any); ok {
			data = gin.H(d)
		}
	}
	return c.Writer.Status(), w.Body.String(), data
}

// TestPortabilitySelfCopyContent ①正向：本人副本 = 明文自证字段 + 逐字正文 + 含冷归档，
// 且不含任何商家内部字段。
func TestPortabilitySelfCopyContent(t *testing.T) {
	testutil.SetupTestDB(t)
	home := testutil.CreateTenantCode(t, "pp_self")
	defer testutil.CleanupTenant(t, home)
	vk := "unit_test_portable_vk_a"
	cid, ids := seedPortabilityData(t, home, vk, 0)
	_, raw, data := callMyData(t, home, cid, vk, "")

	if !strings.Contains(raw, "13800002222") {
		t.Fatalf("本人副本里手机号必须明文（掩码会给出一份'我自己填的号被改掉'的假副本）：%s", raw)
	}
	if !strings.Contains(raw, "=SUM(1,2)") {
		t.Fatalf("正文必须逐字回显（前缀单引号是 CSV 防注入的写法，JSON 副本不该有）")
	}
	// 三行齐全：两条热 + 一条归档。只读热表的实现会在这里少一条。
	msgs, _ := data["messages"].([]any)
	if len(msgs) != len(ids) {
		gotIDs := make([]any, 0, len(msgs))
		for _, m := range msgs {
			mm, _ := m.(map[string]any)
			gotIDs = append(gotIDs, mm["id"])
		}
		t.Fatalf("副本消息条数=%d 期望=%d（归档那条丢了＝只读了 messages 热表）seed=%v got=%v",
			len(msgs), len(ids), ids, gotIDs)
	}
	var gotArchived int
	for _, m := range msgs {
		mm, _ := m.(map[string]any)
		if mm["archived"] == true {
			gotArchived++
		}
	}
	if gotArchived != 1 {
		t.Fatalf("恰好一条来自冷归档表，实际 %d（archived 标记必须如实，否则客户无法核对副本完整性）", gotArchived)
	}
	// 负向：内部字段一个都不许出现（改成整行 Marshal 结构体，这里必红）。
	// intent_score 与 remark 同族＝商家侧给这个人打的评估，与 t_vector（策略中间产物）、
	// visitor_key（凭证）、assigned_user_id（员工身份）一起不属于本人副本。
	for _, banned := range []string{
		"内部备注", "t_vector", "dims", "visitor_key", "TPL_SECRET", "route_result",
		"assigned_user_id", "state_json", "tenant_id", "anchor_type", "template_id", "pending_handoff",
		"intent_score",
	} {
		if strings.Contains(raw, banned) {
			t.Fatalf("副本泄露内部字段 %q（投影必须是显式清单，不能整行 Marshal 模型）", banned)
		}
	}
	// 正向口径同时钉住（防后人"顺手把画像全删了"把这项权利做成空壳）：
	// tags 与 journey_stage 是客户自己参与产生的归类与流程位置，副本该带上——
	// 划线的依据是"谁产生的"，不是"看起来敏不敏感"。
	for _, must := range []string{"高意向", "lead_captured"} {
		if !strings.Contains(raw, must) {
			t.Fatalf("副本缺少本人参与产生的字段 %q（可携带权交的是他提供+他参与产生的数据）", must)
		}
	}
}

// TestPortabilityLoggedPathFollowsDataScope ⑤登录态不看 visitor_key，看数据范围。
// 删除权那边 B8 已经封掉"登录即放行且不看归属"（任何销售可对租户内任意客户发起删除），
// 可携带端点读的是同一个客户的聊天正文，判据必须同源：
// 归属别人的客户 → 403；tenant_admin → 200（否则这条闸只能证明"匿名进不来"）。
func TestPortabilityLoggedPathFollowsDataScope(t *testing.T) {
	testutil.SetupTestDB(t)
	home := testutil.CreateTenantCode(t, "pp_scope")
	defer testutil.CleanupTenant(t, home)
	vk := "unit_test_portable_vk_scope"
	cid, _ := seedPortabilityData(t, home, vk, 777) // 归属 user 777

	call := func(role string, uid uint) int {
		c, _ := newCtx(t, home)
		c.Set("user_id", uid)
		c.Set("role", role)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/privacy/my-data",
			strings.NewReader(fmt.Sprintf(`{"customer_id":%d}`, cid)))
		c.Request.Header.Set("Content-Type", "application/json")
		PrivacyMyData(c)
		return c.Writer.Status()
	}
	if got := call(model.RoleUser, 888); got != http.StatusForbidden {
		t.Fatalf("销售打非本人名下客户期望 403，实际 %d（登录即放行＝删除权 B8 那个洞在副本上重开一次）", got)
	}
	if got := call(model.RoleUser, 777); got != http.StatusOK {
		t.Fatalf("销售打本人名下客户期望 200，实际 %d（403 用例失去正向对照）", got)
	}
	if got := call(model.RoleTenantAdmin, 1); got != http.StatusOK {
		t.Fatalf("租户管理员全租户可取，期望 200，实际 %d", got)
	}
}

// TestPortabilityIdentityGate ②身份闸：错密钥/缺密钥都必须 403 且**不返回任何正文**。
// 这条是整个端点的判别力所在——匿名可携带端点一旦不看 visitor_key，
// 任何人拿一个客户 ID 就能拖走他的全部聊天正文。
func TestPortabilityIdentityGate(t *testing.T) {
	testutil.SetupTestDB(t)
	home := testutil.CreateTenantCode(t, "pp_gate")
	defer testutil.CleanupTenant(t, home)
	vk := "unit_test_portable_vk_gate"
	cid, _ := seedPortabilityData(t, home, vk, 0)
	for name, key := range map[string]string{"错误密钥": "definitely-not-the-key", "空密钥": ""} {
		code, raw, data := callMyData(t, home, cid, key, "")
		if code != http.StatusForbidden {
			t.Fatalf("%s：期望 403，实际 %d（body=%s）", name, code, raw)
		}
		if _, ok := data["messages"]; ok {
			t.Fatalf("%s：被拒的请求仍带回了 messages（先取数后认人＝正文已经进了响应体）", name)
		}
		if strings.Contains(raw, "我的第一句") {
			t.Fatalf("%s：响应体里出现聊天正文", name)
		}
	}
	// 对照：同一条正确密钥必须 200，否则上面两条 403 可能只是"端点根本不通"
	if code, _, _ := callMyData(t, home, cid, vk, ""); code != http.StatusOK {
		t.Fatalf("正确密钥期望 200，实际 %d（403 用例失去对照即无判别力）", code)
	}
}

// TestPortabilityCrossTenantAndParam ③跨租户不回显存在性（404）、缺参 400。
func TestPortabilityCrossTenantAndParam(t *testing.T) {
	testutil.SetupTestDB(t)
	home := testutil.CreateTenantCode(t, "pp_home")
	other := testutil.CreateTenantCode(t, "pp_other")
	defer testutil.CleanupTenant(t, home)
	defer testutil.CleanupTenant(t, other)
	vk := "unit_test_portable_vk_x"
	cid, _ := seedPortabilityData(t, home, vk, 0)
	code, raw, _ := callMyData(t, other, cid, vk, "")
	if code != http.StatusNotFound {
		t.Fatalf("跨租户取副本期望 404，实际 %d body=%s", code, raw)
	}
	// 缺 customer_id：400，且不能退化成"把整个租户的客户都给我"
	c, w := newCtx(t, home)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/privacy/my-data", strings.NewReader(`{"visitor_key":"x"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	PrivacyMyData(c)
	if c.Writer.Status() != http.StatusBadRequest {
		t.Fatalf("缺 customer_id 期望 400，实际 %d body=%s", c.Writer.Status(), w.Body.String())
	}
}

// TestPortabilityCursor ④游标分页：定长配额截断必须如实标记，续取不重不漏。
// 静默截断（不给 truncated / 给了取不回来的游标）是这份副本最容易出的合规问题。
//
// 走两条形态，因为它们的失效方式不同：
//   - 种子客（2 热 + 1 冷，冷行 id 更小）盯"先截断后排序"——砍掉的是冷表那批小 id；
//   - 纯热客（N 条热消息、零归档）盯"每表只取 limit 条"——此时合并长度恰好等于 limit，
//     truncated 会被算成 false，游标走到一半就自称取完了。**默认 message_archive_days=0，
//     绝大多数客户就是这个形态**，所以这条不是边角情况。
func TestPortabilityCursor(t *testing.T) {
	testutil.SetupTestDB(t)
	home := testutil.CreateTenantCode(t, "pp_cur")
	defer testutil.CleanupTenant(t, home)
	vk := "unit_test_portable_vk_cur"
	cid, ids := seedPortabilityData(t, home, vk, 0)
	if got := walkPortabilityCursor(t, home, cid, vk, 2); len(got) != len(ids) {
		t.Fatalf("冷热混合种子分页取回 %d 条，实际 %d 条（漏取＝副本不完整）", len(got), len(ids))
	}

	// 纯热种子：3 条消息、limit=2 走两轮，第二轮只剩 1 条。
	// 少了"每表多取一条探尾"的实现里，第一页 merged 长度恰为 2 ⇒ truncated=false ⇒ 游标停在第二条。
	cid2, ids2 := seedPortabilityHotOnly(t, home, vk+"_hot", 3)
	if got := walkPortabilityCursor(t, home, cid2, vk+"_hot", 2); len(got) != len(ids2) {
		t.Fatalf("纯热种子分页取回 %d 条，实际 %d 条（把每表配额写成 limit 而非 limit+1 就会停在这儿）",
			len(got), len(ids2))
	}

	// 配额只能调低不能调高：?limit 给一个天文数字，回显的 limit 必须仍是硬顶
	_, _, data := callMyData(t, home, cid, vk, `,"limit":99999999`)
	pg, _ := data["page"].(map[string]any)
	if got, _ := pg["limit"].(float64); int(got) != portableRowCap {
		t.Fatalf("limit=99999999 应被钳到 %d，实际回显 %v（定长配额被查询参数绕过）", portableRowCap, pg["limit"])
	}
}

// walkPortabilityCursor 按游标把某客户的全部副本消息取完，回到手的 id 列表。
// 过程中就地校验"页内/跨页严格升序、不重复、游标必须前进"，任何一条不满足直接 t.Fatalf。
func walkPortabilityCursor(t *testing.T, tenantID, cid uint, vk string, limit int) []uint {
	t.Helper()
	var seen []uint
	after := uint(0)
	for page := 1; page <= 10; page++ {
		_, _, data := callMyData(t, tenantID, cid, vk,
			fmt.Sprintf(`,"limit":%d,"after_id":%d`, limit, after))
		msgs, _ := data["messages"].([]any)
		pg, _ := data["page"].(map[string]any)
		if pg == nil {
			t.Fatalf("第%d页缺 page 元信息（客户端无从判断是否取完）", page)
		}
		pageIDs := make([]any, 0, len(msgs))
		for _, m := range msgs {
			mm, _ := m.(map[string]any)
			pageIDs = append(pageIDs, mm["id"])
		}
		t.Logf("第%d页 after=%d ids=%v page=%v", page, after, pageIDs, pg)
		for _, m := range msgs {
			mm, _ := m.(map[string]any)
			id := uint(mm["id"].(float64))
			if len(seen) > 0 && id <= seen[len(seen)-1] {
				t.Fatalf("分页取回的 id 非严格升序（%d→%d）：两表 append 序不等于归并序，"+
					"漏了 sort.Slice 时游标会在'冷表 id 小于热表'这个真实形态上漏行", seen[len(seen)-1], id)
			}
			for _, s := range seen {
				if s == id {
					t.Fatalf("游标续取出现重复消息 %d（两表归并的全序被破坏）", id)
				}
			}
			seen = append(seen, id)
		}
		truncated, _ := pg["truncated"].(bool)
		if !truncated {
			return seen
		}
		next, _ := pg["next_after_id"].(float64)
		if uint(next) <= after {
			t.Fatalf("截断却给了不前进的游标（%d→%d）：客户端会死循环或漏取", after, uint(next))
		}
		after = uint(next)
	}
	t.Fatalf("游标取数在 %d 页内没收敛（truncated 恒真＝截断标记不可信）", 10)
	return nil
}
