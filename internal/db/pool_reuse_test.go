// 连接池不得随 db.Init() 调用次数累积（2026-09-26 根因回归锁）。
//
// 现场：全量单测里 internal/db 六条用例红，报的是 SQLSTATE 53300
// 「remaining connection slots are reserved for non-replication superuser connections」。
// 过去两轮都把这事记成"本机环境噪声"（2026-09-24 那次为此把每池上限从 25 收到 8、
// 包并发收到 -p 3），但**上限管的是单个池，不是池的个数**——
// 旧 db.Init() 直接 `DB, err = gorm.Open(...)` 换掉包级句柄，上一个 *sql.DB 从来没人 Close，
// 它的连接以 idle 态长期挂在服务端；而 testutil.SetupTestDB 是**每条用例**调一次 Init，
// 于是一个 DB 用例密集的包能攒出几十份池。谁先跑到谁红、复跑即绿，就是这么来的。
//
// 本文件钉的是"重复 Init 不再累积连接"这一句结构性质，不依赖并发时序，因此稳定可复现。
// 反证（2026-09-26 实测过）：把 database.go 里"关掉旧池"那段删掉，第一条用例必须红
// （报「第二次 Init 之后旧池仍可 Ping」），同包其余用例照常绿——说明这簇用例真在把关池这一件事。
package db

import (
	"strings"
	"testing"
)

// isPoolClosedErr 判定"这条错误就是池已关闭"。
//
// 为什么按文本判而不是 errors.Is：Go 的 database/sql 只在 DB.Close() 后返回一个
// **未导出**的哨兵（消息为「sql: database is closed」），包外拿不到常量；
// 已导出的 ErrConnDone/ErrTxDone 属于 conn/事务层，用它们判池会恒不匹配。
// 文本判的代价是 Go 若改文案本用例即红——那时错误信息会直接把真相说出来，不会静默放行。
func isPoolClosedErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "database is closed")
}

// TestInitReplacesAndClosesPreviousPool 第二次 Init 之后，第一次拿到的底层池必须已关闭。
//
// 判据取"旧句柄 Ping 必返回 ErrClosed"而不是"pg_stat_activity 连接数变小"：
// 后者受服务端连接回收时延与其它客户端干扰，会把结构断言写成时序赌局（本项目反复踩过）。
func TestInitReplacesAndClosesPreviousPool(t *testing.T) {
	gdb := newTestDB(t) // 连不上即 Skip（本地无 DB 的哲学），连上了才有下面可谈

	first, err := gdb.DB()
	if err != nil {
		t.Fatalf("取第一次的底层 *sql.DB 失败: %v", err)
	}
	if first == nil {
		t.Fatal("第一次的底层 *sql.DB 为 nil，前置不成立")
	}
	// 让旧池真的建出至少一条连接：否则"已关闭"会在一个从未用过的池上白过（假绿形态）
	var n int
	if err := first.QueryRow("SELECT 1").Scan(&n); err != nil {
		t.Fatalf("旧池探测查询失败（前置不成立）: %v", err)
	}

	// 第二次 Init：语义上就是"重新初始化"，必须把旧的关掉
	if err := Init(); err != nil {
		t.Fatalf("第二次 Init 失败: %v", err)
	}
	if DB == gdb {
		t.Fatal("第二次 Init 后包级句柄没换——那关旧池这段分支根本没被执行，本用例判据失效")
	}
	second, err := DB.DB()
	if err != nil {
		t.Fatalf("取第二次的底层 *sql.DB 失败: %v", err)
	}
	if second == first {
		t.Fatal("两次 Init 返回同一个 *sql.DB：池没被重建，关旧池的断言无从谈起")
	}

	if err := first.Ping(); err == nil {
		t.Fatal("第二次 Init 之后旧池仍可 Ping —— 旧池未被关闭，连接会随 Init 次数累积（53300 的成因）")
	} else if !isPoolClosedErr(err) {
		// database/sql 对已 Close 的池只返回一个未导出的哨兵（消息文本「sql: database is closed」），
		// 库里导出的 ErrConnDone/ErrTxDone 都是 conn/Tx 侧的，拿它们判"池已关"会恒不匹配
		// （本用例首跑就是这么红的）。因此按文本判，并把"其它错误"如实点名而不放过。
		t.Fatalf("旧池 Ping 返回的不是「池已关闭」错误，无法据此判定关池是否生效: %v", err)
	}

	// 新池必须真的可用（防止"把两个都关了"这种把红修成更红的假绿）
	var m int
	if err := second.QueryRow("SELECT 1").Scan(&m); err != nil {
		t.Fatalf("第二次 Init 的新池不可用: %v", err)
	}
}

// TestFirstInitHasNoPreviousToClose 首次 Init（prev==nil）不得因为"关旧池"分支而炸。
// 这条是防把 `prev.DB()` 写成无条件解引用——那种写法在真正第一次启动时 panic，
// 而单测进程里 DB 早被 newTestDB 建好了，看不见这个形态。
func TestFirstInitHasNoPreviousToClose(t *testing.T) {
	newTestDB(t)
	// 人造"首次"：把包级句柄置空后走一次完整 Init，等价于进程内第一次调用。
	// 用 defer 还原，避免污染同包后续用例。
	saved := DB
	DB = nil
	t.Cleanup(func() { DB = saved })

	if err := Init(); err != nil {
		t.Fatalf("首次语义的 Init 失败（prev==nil 分支未兜住）: %v", err)
	}
	if DB == nil {
		t.Fatal("首次 Init 后 DB 仍为 nil")
	}
	if _, err := DB.DB(); err != nil {
		t.Fatalf("首次 Init 后取不到底层池: %v", err)
	}
}
