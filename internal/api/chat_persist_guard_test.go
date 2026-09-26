// Package api FIX-5(2026-09-27) 结构守卫：对话主链"每条 DB 写都必须过口径单点"。
//
// 为什么要有这一份、而不是只留注释：
// chat_main.go 上千行、二十多处落库点，"写库必须查错"这件事只要有一处漏了就是静默丢消息，
// 而静默丢消息**不会报错**——客户那句话在网页、顾问端、E8 留痕三处都看不见，接口照样回 200。
// 一个只会写在注释里的约定等于没有约定（本仓 2026-09-26 那批的共同教训）。
// 所以这里把它变成**跑在 CI 上的结构断言**：
//
//	guard ①  chat_main.go 里不得再出现以 `db.` 开头的裸语句行（写/查完不接错误的形态）；
//	guard ②  经 persistRequired / persistBypass / persistBypassErr 收口的调用点数量不得低于基线
//	         （防止"把报错口径改回丢掉 error 的裸写"这种倒退悄悄发生）；
//	guard ③  advisor.go 的 GetChatHistory 函数体内不得再出现 `.Error == nil` 形态的
//	         "读得到才校验"门禁（那是这次 fail-closed 真修的原始缺陷）。
//
// 三条守卫都配了**反证**：各自喂一份"坏样本"进同一个判据函数，要求它必须报错——
// 判据抓不住坏样本，那它在真文件上绿着也没有意义（本仓称之为"护栏空转"）。
// 判据在看板前先剥行注释：说明性注释里出现 `db.` 或 `.Error == nil` 是常事，
// 不按字节位置一刀切就会误伤注释、把守卫变成没人敢写注释的东西。
package api

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// chatMainMinPersistCallSites 是 chat_main.go 里三个口径函数的调用点总数下限（2026-09-27 实测 26）。
// 它只升不降：降了就说明有人把"会报错的落库"改回了"丢掉错误的落库"。
// 若确因拆文件把调用点搬走，请把常量与它一起搬过去，别下调。
const chatMainMinPersistCallSites = 26

// fix5GuardedFiles 是本守卫覆盖的对话/顾问侧文件与各自的**口径调用点下限**（FIX-5 二批，2026-09-27）。
//
// 为什么按文件分别给基线，而不是全仓一个总数：全仓总数会被「某个文件加了十处、另一个文件删了十处」
// 互相抵消掉——那正是口径倒退最悄无声息的发生方式。
// 每个数字都是当天实测值，新增落库点时请一并上调；下调必须同时说明这些点搬到了哪个文件。
var fix5GuardedFiles = []struct {
	name   string
	minSit int
}{
	{"chat_main.go", 26},         // 正式链 /chat
	{"chat_unauthorized.go", 28}, // 免登录链 /chat/test（本批收口）
	{"chat_guest.go", 4},         // 访客欢迎：密钥补签 / 流程启动 / 去重读 / 欢迎消息落库
	{"chat_human.go", 5},         // 人工回复：接管态、消息落库、列表读、转人工、切回 AI
	{"advisor.go", 30},           // 顾问台：接管/代答/试驾/打标/工作台统计与详情读
}

// stripLineComments 逐行剥掉 `//` 之后的内容（字符串字面量里的 `//` 只在"行注释起始"位置才算注释，
// 本判据只关心行首 token，误伤面可忽略；剥 URL 的风险由调用方只在"行首判裸语句"时受益）。
func stripLineComments(src string) []string {
	out := make([]string, 0, strings.Count(src, "\n")+1)
	for _, line := range strings.Split(src, "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		out = append(out, line)
	}
	return out
}

// bareDBStatementLines 找出"整行就是一条裸 DB 语句"的**语句**起始行号（1 起）。
//
// 判据是「**括号深度为 0 的行**去缩进后以 `db.` 开头」，而不是"行首是 db."（那是本守卫的第一版，已废弃）：
// 收口后的写法是 `persistRequired(c, "kind", …, db.RQ(c).Create(&m))` 跨行展开，
// 续行的行首同样是 `db.`——按行首判会把**已经收口的调用点**报成违规，
// 于是守卫要么逼所有人把长调用挤成一行（gofmt 会再拆开，等于守卫逼人写坏格式），
// 要么被人当误报直接删掉。两种结局都比缺陷静默更糟。
// 深度按 () 计（字符串字面量与行注释里的括号都不参与，见 stripLineComments 与 parenDelta），
// 因此 `db.RQ(c).Create(&m)` 这类自平衡的一行仍是深度 0，照旧被抓。
//
// `x := db.RQ(...)` 这种把句柄取出来的写法不在打击面内（它只是拿句柄，真正的语句在下一行，
// 而下一行必然带 `if err :=` 或 persist* 包裹）。
func bareDBStatementLines(lines []string) []int {
	var hits []int
	depth := 0
	for i, line := range lines {
		t := strings.TrimSpace(line)
		// 先判"这一行是不是从深度 0 起笔"：是，才可能是语句开头。
		atStatementStart := depth == 0
		depth += parenDelta(line)
		if depth < 0 {
			depth = 0 // 判据只看"是否在语句开头"，跨函数/跨行的失衡不该放大打击面
		}
		if atStatementStart && strings.HasPrefix(t, "db.") {
			hits = append(hits, i+1)
		}
	}
	return hits
}

// parenDelta 统计一行里**括号**的净增减，忽略字符串/字符字面量与行注释里的括号
// （SQL 里 `"count(*)"`、注释里的"（见 FIX-5）"都不该影响深度判定）。
func parenDelta(line string) int {
	d := 0
	var quote byte
	for i := 0; i < len(line); i++ {
		ch := line[i]
		switch {
		case quote != 0:
			if ch == '\\' {
				i++ // 字面量内的转义（\" 或 \\）跳过下一字节
			} else if ch == quote {
				quote = 0
			}
		case ch == '"' || ch == '\'':
			quote = ch
		case ch == '/' && i+1 < len(line) && line[i+1] == '/':
			return d // 行注释：后面的都不算
		case ch == '(':
			d++
		case ch == ')':
			d--
		}
	}
	return d
}

// persistCallSites 统计口径函数的调用点总数（按出现次数，不按行，一行两算也如实计）。
//
// 四个名字都要数：persistRequiredMsg 是 persistRequired 的"自定义文案"入口（顾问台的动作类失败
// 要说"接管失败，请重试"而不是"消息处理失败，请重试"），它是同一个机制的另一扇门。
// 漏了它，把调用点从 persistRequired 改成 persistRequiredMsg 就会让基线计数**下降**——
// 判据把一个平白无损的改写报成口径倒退，这种守卫活不过一次评审就会被删。
func persistCallSites(src string) int {
	n := 0
	for _, name := range []string{"persistRequired(", "persistRequiredMsg(", "persistBypass(", "persistBypassErr("} {
		n += strings.Count(src, name)
	}
	return n
}

// errorEqualsNilInFunc 在 src 里切出名为 name 的顶层函数体，返回体内出现 `.Error == nil` 的次数。
// 切法：从 `func Name(` 所在行起，到下一行以 `}` 顶格闭合为止（Go 里顶层函数的收尾大括号必在列 0）。
// 找不到该函数返回 -1，交给调用方判"守卫本身失效"。
func errorEqualsNilInFunc(src, name string) int {
	lines := stripLineComments(src)
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "func "+name+"(") {
			start = i
			break
		}
	}
	if start < 0 {
		return -1
	}
	body := &strings.Builder{}
	for _, line := range lines[start:] {
		body.WriteString(line)
		body.WriteString("\n")
		if line == "}" { // 顶格右花括号＝函数结束
			break
		}
	}
	return strings.Count(body.String(), ".Error == nil")
}

// TestFix5_GuardChatMainHasNoBareDBStatements 守卫 ① + ②（覆盖 fix5GuardedFiles 全部五份文件），并各自带反证。
func TestFix5_GuardChatMainHasNoBareDBStatements(t *testing.T) {
	// —— 反证之一：深度 0 的裸语句必须被抓（这是要根除的形态本身）
	bad := "func f() {\n\tdb.RQ(c).Create(&msg)\n\tx := 1\n}\n"
	got := bareDBStatementLines(stripLineComments(bad))
	if len(got) != 1 || got[0] != 2 {
		t.Fatalf("反证失败：坏样本里的裸语句行没被抓到，实得 %v（判据已失效，真文件上的绿不可信）", got)
	}
	// —— 反证之二：注释行不得被抓（防"说明注释里写 db.RQ"变成守卫误伤）
	commented := "// db.RQ(c).Create 以前不查错\nfunc f() {\n}\n"
	if hits := bareDBStatementLines(stripLineComments(commented)); len(hits) != 0 {
		t.Fatalf("判据误伤注释行：%v（剥注释逻辑失效，会挡住正常提交）", hits)
	}
	// —— 反证之三（本判据的**关键**一侧）：已经收口、只是跨行展开的调用点不得被误伤。
	// 缺这条反证，判据就退化成"行首不许出现 db."，等于逼人把长调用挤成一行去绕守卫——
	// 那种写法 gofmt 会再拆回去，守卫与格式器互相打架，最后输的一定是守卫。
	wrapped := "func f() {\n\tif persistRequiredMsg(c, \"human_reply\", 1, 2,\n\t\tdb.RQ(c).Create(&m), \"失败\") {\n\t\treturn\n\t}\n}\n"
	if hits := bareDBStatementLines(stripLineComments(wrapped)); len(hits) != 0 {
		t.Fatalf("判据误伤收口后的续行：%v（括号深度判定失效，会把合规写法报成违规）", hits)
	}
	// —— 反证之四：SQL 字符串里的括号不得参与深度计算（`"count(*)"`、`"(a)"`），
	// 否则深度会被字符串带偏，真正的裸语句反而漏网。
	inString := "func f() {\n\tx := Count(\"select count(*) from t\")\n\tdb.RQ(c).Create(&msg)\n}\n"
	if hits := bareDBStatementLines(stripLineComments(inString)); len(hits) != 1 || hits[0] != 3 {
		t.Fatalf("字符串字面量里的括号影响了深度判定，实得 %v（应为 [3]）", hits)
	}

	for _, f := range fix5GuardedFiles {
		src := readOrFatal(t, f.name)

		// —— 真文件：不得有任何深度 0 的裸语句行
		if hits := bareDBStatementLines(stripLineComments(src)); len(hits) > 0 {
			t.Fatalf("%s 又出现未接错误口径的裸 DB 语句（行号 %v）。\n"+
				"对话/顾问链的每一次读写都必须经 persistRequired*（失败要显式报错）或 persistBypass*（失败只留痕计数），"+
				"分类见 chat_persist.go 的口径表。", f.name, hits)
		}

		// —— 调用点数量不得低于基线（前置自检：基线本身必须≤实测值，否则这条等式在空转）
		n := persistCallSites(src)
		if n < f.minSit {
			t.Fatalf("%s 的落库口径调用点从基线 %d 降到了 %d——有地方把\"会报错的读写\"改回了\"丢掉错误的裸写\"",
				f.name, f.minSit, n)
		}
		if n == f.minSit {
			t.Logf("%s 落库口径调用点 = 基线值 %d（新增读写点时请一并上调 fix5GuardedFiles）", f.name, n)
		}
	}
}

// TestFix5_GuardChatHistoryGateIsFailClosed 守卫 ③：/chat/history 的归属门禁不得再用"读得到才校验"。
func TestFix5_GuardChatHistoryGateIsFailClosed(t *testing.T) {
	raw, err := os.ReadFile("advisor.go")
	if err != nil {
		t.Fatalf("读 advisor.go 失败（守卫本身失效）: %v", err)
	}

	// —— 反证：坏样本（把门禁整段包在"读成功"里）必须被抓
	badSrc := "" +
		"func GetChatHistory(c *gin.Context) {\n" +
		"\tif db.RQ(c).First(&cust, id).Error == nil {\n" +
		"\t\tcheckKey()\n" +
		"\t}\n" +
		"}\n"
	if n := errorEqualsNilInFunc(badSrc, "GetChatHistory"); n != 1 {
		t.Fatalf("反证失败：坏样本里的 `.Error == nil` 门禁没被抓到，实得 %d（判据已失效）", n)
	}

	got := errorEqualsNilInFunc(string(raw), "GetChatHistory")
	if got < 0 {
		t.Fatalf("守卫失效：advisor.go 里找不到 func GetChatHistory（改名或挪文件请同步本判据）")
	}
	if got > 0 {
		t.Fatalf("GetChatHistory 里又出现 %d 处 `.Error == nil`：把归属校验包在\"客户读成功\"里，"+
			"等于在库最抖的时候把 CheckVisitorKey 和数据范围两道路径整段跳过（原缺陷，见 FIX-5）。", got)
	}
}

// TestFix5_GuardKindNamesAreStable 守卫 ④：kind 维度不得漂成自由字符串。
//
// /metrics 的 ai_scrm_chat_persist_error_total{kind} 是告警口径的落点，
// 一旦同一个失败点被写成 "customer_inbound" / "cust_inbound" 两种拼法，
// 看板上的曲线会各算一半、告警阈值永远打不到。这里把 chat_main.go 实际用到的 kind
// 与 chat_persist.go 口径表里登记的 kind 做**双向**对账。
func TestFix5_GuardKindNamesAreStable(t *testing.T) {
	// 行内形态有两种：persistBypass("kind", …) 与 persistRequired(s.c, "kind", …)，
	// 故 kind 前允许一段不含引号的实参前缀；kind 之后必须是逗号或右括号，
	// 免得把口径函数自己的名字（"persistRequired("）当成 kind 收进来。
	kindRe := regexp.MustCompile(`persist(?:Required|RequiredMsg|Bypass|BypassErr)\([^"]*"([a-z_]+)"[,)]`)
	used := map[string]string{} // kind → 首次出现在哪份文件（报错时要能指回去）
	for _, f := range fix5GuardedFiles {
		for _, line := range stripLineComments(readOrFatal(t, f.name)) {
			for _, m := range kindRe.FindAllStringSubmatch(line, -1) {
				if _, dup := used[m[1]]; !dup {
					used[m[1]] = f.name
				}
			}
		}
	}
	if len(used) == 0 {
		t.Fatalf("五份受守卫文件里一个 persist* 的 kind 都没抓到——判据失效（正则或写法变了），本条守卫正在空转")
	}
	// 反证：判据必须真的能抓到 kind，否则下面的"每个 kind 都登记过"是空转。
	probe := `persistRequired(c, "probe_kind_x", 1, 2, res)`
	if m := kindRe.FindAllStringSubmatch(probe, -1); len(m) != 1 || m[0][1] != "probe_kind_x" {
		t.Fatalf("反证失败：kind 抽取判据抓不住标准写法（实得 %v），本条守卫正在空转", m)
	}
	// 口径表：chat_persist.go 的注释表里逐条列了 kind，用于人工核对；这里只要求每个 kind 都出现过一次。
	doc := readOrFatal(t, "chat_persist.go")
	for kind, file := range used {
		if !strings.Contains(doc, kind) {
			t.Fatalf("kind %q 在 %s 里用了，但 chat_persist.go 的口径表没登记它——"+
				"新加读写点要同时说明它是\"必查\"还是\"旁路\"，否则下一个人无从判断该不该报错", kind, file)
		}
	}
}

func readOrFatal(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("读 %s 失败: %v", name, err)
	}
	return string(b)
}
