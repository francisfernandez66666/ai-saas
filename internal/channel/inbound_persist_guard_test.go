// Package channel FIX-5(2026-09-27) 结构守卫：入站消息落库不得再写成"丢掉错误的裸 Create"。
//
// 为什么钉这一条：`db.DB.Create(&inMsg)` 单独成行时，GORM 的 error 被就地丢弃，
// 于是"客户那句话没写进库"与"写进去了"在代码层面完全同形——回调照样 ack success，
// worker 照样带着 ID=0 往下跑。这类缺陷不会报错，只会事后没人能解释"他说过什么"。
// 注释挡不住回潮（本仓 2026-09-26 那批的共同教训：只会打日志并返回成功的守卫等于没有守卫），
// 所以把它变成跑在单测里的结构断言，并配反证样本。
package channel

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// bareCreateRe 匹配"整行就是一条不接错误值的 Create 语句"（行首即 db.DB.Create / db.RQ(...).Create）。
// `if err := db.DB.Create(...)` 与 `persistX(..., db.DB.Create(...))` 都不命中——它们把错误接住了。
var bareCreateRe = regexp.MustCompile(`(?m)^[[:space:]]*db\.(DB|RQ\([^)]*\)|PQ\([^)]*\))\.[A-Za-z.]*Create\(`)

// TestInboundGuardNoBareCreate 结构守卫：inbound.go 里不得再出现"行首即 db.*.Create(" 的不查错写库。
// 两条反证各自在场——坏样本必须恰好被抓到 1 处（否则真文件的零命中是空转）、
// 说明性注释行必须不被抓到（否则守卫会误伤注释，最后被人连注释一起删掉，守卫反而消失）。
func TestInboundGuardNoBareCreate(t *testing.T) {
	// 反证之一：判据必须抓得住坏样本，否则真文件上的"零命中"是空转。
	bad := "func f() {\n\tdb.DB.Create(&inMsg)\n\tif err := db.DB.Create(&m).Error; err != nil {\n\t\tpanic(err)\n\t}\n}\n"
	hits := bareCreateRe.FindAllString(bad, -1)
	if len(hits) != 1 {
		t.Fatalf("反证失败：坏样本应恰好抓到 1 处裸 Create，实得 %d 处（判据已失效）:\n%s", len(hits), strings.Join(hits, "\n"))
	}
	// 反证之二：说明性注释不得被抓。判据锚定"行首（去缩进后）就是 db."，
	// 所以 `// db.DB.Create(&inMsg) 以前不查错` 这种写法天然是 0 命中——
	// 不锚行首的负向 grep 会把注释也毙掉，最后被人连注释一起删掉，守卫反而消失了。
	commented := "// db.DB.Create(&inMsg) 以前不查错\nfunc f() {\n}\n"
	if n := len(bareCreateRe.FindAllString(commented, -1)); n != 0 {
		t.Fatalf("判据误伤注释行（命中 %d 处），会挡住正常提交:", n)
	}

	// 真文件：inbound.go 的每一次落库都必须接住错误。
	raw, err := os.ReadFile("inbound.go")
	if err != nil {
		t.Fatalf("读 inbound.go 失败（守卫本身失效）: %v", err)
	}
	if hits := bareCreateRe.FindAllString(string(raw), -1); len(hits) > 0 {
		t.Fatalf("inbound.go 又出现不查错误的裸 Create 语句（%d 处）：\n%s\n"+
			"入站消息行是这轮对话唯一的存在证明，写失败必须让本轮判 failed（台账 attempts<5 时重推会重新认领）。",
			len(hits), strings.Join(hits, "\n"))
	}
}
