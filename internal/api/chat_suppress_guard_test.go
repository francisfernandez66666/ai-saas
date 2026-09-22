// 相似消息抑制「双链共用 + 留痕必挂会话」静态守卫（A2 / P2-1 / D5 验收护栏，2026-09-23 收尾批）。
//
// 为什么用源码扫描：这几条都是"少写一行"型缺陷，行为测试要制造并发在途批次或
// "有批次无会话"的罕见竞态才复现，成本远高于收益。
//   - A2 的原缺陷：抑制判定只内联在正式链，免登录 C 端链整条没有 → 同一客户连发近似句，
//     正式链答一次、C 端链答两遍（择臂样本也因此不可比）。锁"两条链都调用同一函数"。
//   - P2-1 的原缺陷：无在途批次也抑制 → 历史相似句把无人应答的新消息静默吞掉。
//     锁"C 端链抑制前必先探测 HasInflightBatch"。
//   - D5 的原缺陷：C 端链留痕消息先 First(&conv) 取活跃会话，旧写法丢掉 error 就用零值
//     conv.ID=0 落库 → 既留一条永不关联会话的孤儿行，又把它算进"客户消息数"（D2 看板虚增）。
//     锁"定位失败必须在落库之前提前返回，且留痕消息的会话号取自查到的 conv"。
//
// 自证非空转：负向判定收进纯函数 suppressBodyViolations，用例既喂"真文件体"（必须 0 违规），
// 也喂四段"故意写坏"的合成体（必须各自报违规）——否则锁会退化成恒真的字符串存在性检查。
// 判定本体的行为（阈值/窗口/租户谓词）由 internal/chatflow/similarity_suppress_test.go 覆盖。
package api

import (
	"os"
	"strings"
	"testing"
)

// suppressBodyViolations 对 chatUnauthorizedSimilarSuppress 函数体做 P2-1/D5 纪律检查，
// 返回违规描述列表（空=合规）。纯字符串处理，便于用合成样本自证。
func suppressBodyViolations(body string) []string {
	var bad []string
	if !strings.Contains(body, "HasInflightBatch(") {
		bad = append(bad, "未先探测 HasInflightBatch 即可能写抑制留痕（P2-1 丢答回归）")
	}
	first := strings.Index(body, "First(&conv")
	create := strings.Index(body, "Create(&suppressedMsg)")
	switch {
	case first < 0:
		bad = append(bad, "函数体内找不到会话定位 First(&conv)，D5 锁失去比对基准")
	case create < 0:
		bad = append(bad, "函数体内找不到留痕落库 Create(&suppressedMsg)，D5 锁失去比对基准")
	case create < first:
		bad = append(bad, "留痕落库发生在活跃会话定位之前——此时无从取得 conv.ID，必写 0 号孤儿行（D5）")
	default:
		// 定位失败的 error 分支必须落在"定位→落库"之间并提前返回，否则零值 conv 继续往下流
		window := body[first:create]
		at := strings.Index(window, "!= nil {")
		if at < 0 {
			bad = append(bad, "定位与落库之间未检查 error（D5：丢错误即用零值 conv 落库）")
		} else if !strings.Contains(window[at:], "return false") {
			bad = append(bad, "会话定位失败分支未提前 return false，零值 conv 会继续流向落库（D5）")
		}
	}
	if !strings.Contains(body, "ConversationID: conv.ID") {
		bad = append(bad, "留痕消息会话号未取自查到的 conv（应写 ConversationID: conv.ID，D5）")
	}
	return bad
}

// TestBothEntriesShareSimilarSuppress 锁 1（A2）：两条 C 端入口共用同一抑制判定
func TestBothEntriesShareSimilarSuppress(t *testing.T) {
	for _, name := range []string{"chat_main.go", "chat_unauthorized.go"} {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("读 %s 失败: %v", name, err)
		}
		src := stripComments(string(raw))
		if !strings.Contains(src, "chatflow.FindSimilarInflightMessage(") {
			t.Errorf("%s 未调用 chatflow.FindSimilarInflightMessage，相似消息抑制又回到单链私有（A2）", name)
		}
	}
}

// TestSuppressBodyLockPassesOnRealCode 真代码必须零违规
func TestSuppressBodyLockPassesOnRealCode(t *testing.T) {
	for _, v := range suppressBodyViolations(suppressFuncBody(t)) {
		t.Errorf("chatUnauthorizedSimilarSuppress 违规: %s", v)
	}
}

// TestSuppressBodyLockIsNotVacuous 故意写坏四种实现，逐一确认本锁真的抓得住
// （防"恒真字符串检查"：只喂真代码的守卫永远不知道自己在不在干活）
func TestSuppressBodyLockIsNotVacuous(t *testing.T) {
	const good = `
	if service.DefaultMessageQueueService == nil || !svc.HasInflightBatch(s.tenantID, s.customer.ID) { return false }
	hit, ok := chatflow.FindSimilarInflightMessage(db.RQ(s.c), s.tenantID, s.customer.ID, s.req.Content, true, 0)
	if !ok { return false }
	var conv model.Conversation
	if ferr := db.RQ(s.c).Where("status = ?", "active").First(&conv).Error; ferr != nil {
		log.Printf("定位失败")
		return false
	}
	suppressedMsg := model.Message{ConversationID: conv.ID, Content: s.req.Content}
	db.RQ(s.c).Create(&suppressedMsg)
	return true
`
	cases := []struct {
		name string
		body string
	}{
		{"丢掉在途批次探测", strings.Replace(good, "HasInflightBatch(", "HasInflightBatchX(", 1)},
		{"定位失败不提前返回", strings.Replace(good, "\t\treturn false\n\t}\n\tsuppressedMsg", "\t}\n\tsuppressedMsg", 1)},
		{"留痕挂零号会话", strings.Replace(good, "ConversationID: conv.ID", "ConversationID: 0", 1)},
		{"落库早于定位", strings.Replace(good,
			"var conv model.Conversation",
			"suppressedMsg := model.Message{ConversationID: conv.ID}\n\tdb.RQ(s.c).Create(&suppressedMsg)\n\tvar conv model.Conversation", 1)},
	}
	for _, tc := range cases {
		if v := suppressBodyViolations(tc.body); len(v) == 0 {
			t.Errorf("%s：本锁未报任何违规（恒真锁）", tc.name)
		}
	}
	// 反向自查：合成"合规样本"本身不得报违规，否则上面四条 FAIL 只是"全都违规"的假绿
	if v := suppressBodyViolations(good); len(v) != 0 {
		t.Errorf("合成合规样本被判违规，说明锁的口径本身写错: %v", v)
	}
}

// suppressFuncBody 取出 chatUnauthorizedSimilarSuppress 的函数体（已去注释）
func suppressFuncBody(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("chat_unauthorized.go")
	if err != nil {
		t.Fatalf("读 chat_unauthorized.go 失败: %v", err)
	}
	src := stripComments(string(raw))
	start := strings.Index(src, "func (s *chatUnauthorizedCtx) chatUnauthorizedSimilarSuppress()")
	if start < 0 {
		t.Fatal("找不到 chatUnauthorizedSimilarSuppress，函数已改名请同步本守卫")
	}
	end := strings.Index(src[start:], "\nfunc ")
	if end < 0 {
		t.Fatal("chatUnauthorizedSimilarSuppress 函数体边界解析失败")
	}
	body := src[start : start+end]
	// 边界自查：截到的必须是单个函数体而非"到文件尾"，否则锁会在别的函数里凑齐关键词
	if strings.Contains(body, "chatUnauthorizedEnqueue") {
		t.Error("函数体越界截取（含后继函数），本锁的比对范围失真")
	}
	return body
}
