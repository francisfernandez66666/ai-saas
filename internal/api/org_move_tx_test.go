// FIX-8 反证单测（2026-09-29 审计批二）：部门移动事务内"读新父 path"失败必须整笔回滚。
// 旧写法 tx.Select("path").First(&p, pid) 不查错——前置 404 校验只覆盖事务外那一次读，
// 校验后父被删（或事务内读抖动）时 newParentPath=""，子树被按根前缀整体重写：
// 部门"挂到根下"、depth 被改、全程无任何报错。
// 变异口径：把 applyDeptUpdateTx 里那句 `if err != nil { return err }` 删掉，①必红
// （事务会带着 parent_id=不存在ID 与"挂根"重写一起提交，返回 nil）。
package api

import (
	"fmt"
	"testing"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/testutil"

	"gorm.io/gorm"
)

// mkDept 直插一个部门行（物化路径按真实 ID 拼，模拟 seed 的正常形态）
func mkDept(t *testing.T, tid uint, name string, parentID *uint, path string, depth int) *model.Department {
	t.Helper()
	d := &model.Department{TenantID: tid, Name: name, ParentID: parentID, Path: path, Depth: depth}
	if err := db.DB.Create(d).Error; err != nil {
		t.Fatalf("建部门 %s 失败: %v", name, err)
	}
	return d
}

// TestDeptMoveTxParentPathReadFailRollsBack 事务内读不到新父 → 上抛错误，整笔移动（改父+子树重写）不得落库
func TestDeptMoveTxParentPathReadFailRollsBack(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)

	a := mkDept(t, tid, "fix8-A", nil, fmt.Sprintf("/%d/", 0), 1)
	a.Path = fmt.Sprintf("/%d/", a.ID)
	db.DB.Model(a).Update("path", a.Path)
	b := mkDept(t, tid, "fix8-B", &a.ID, fmt.Sprintf("/%d/%d/", a.ID, 0), 2)
	b.Path = fmt.Sprintf("/%d/%d/", a.ID, b.ID)
	db.DB.Model(b).Update("path", b.Path)

	ghost := uint(999999999) // 不存在的"新父"：模拟前置校验后父行被删
	updates := map[string]interface{}{"parent_id": ghost, "depth": 2}
	err := db.DB.Transaction(func(tx *gorm.DB) error {
		return applyDeptUpdateTx(tx, a, updates, true, tid)
	})
	if err == nil {
		t.Fatal("事务内读新父路径失败必须回整笔错误——回 nil 即吞错未除根")
	}

	// 回滚核验：A 的父/深度/路径与 B 的路径必须逐字如旧（旧实现在这里会把子树重写挂到根下）
	var ra, rb model.Department
	if err := db.DB.First(&ra, a.ID).Error; err != nil {
		t.Fatalf("回读 A 失败: %v", err)
	}
	if ra.ParentID != nil || ra.Depth != a.Depth || ra.Path != fmt.Sprintf("/%d/", a.ID) {
		t.Errorf("A 应逐字未动（parent=nil depth=1 path=/%d/），实得 parent=%v depth=%d path=%q", a.ID, ra.ParentID, ra.Depth, ra.Path)
	}
	if err := db.DB.First(&rb, b.ID).Error; err != nil {
		t.Fatalf("回读 B 失败: %v", err)
	}
	if rb.Path != fmt.Sprintf("/%d/%d/", a.ID, b.ID) {
		t.Errorf("B 的子树路径不应被重写，实得 %q（挂根缺陷现场）", rb.Path)
	}
}

// TestDeptMoveTxHappyPath 正向对照：移到真实父下，子树按 新父path+自身ID 前缀整体重写（防"改成恒回滚"的伪修）
func TestDeptMoveTxHappyPath(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)

	oldRoot := mkDept(t, tid, "fix8-ok-oldroot", nil, "/placeholder/", 1)
	db.DB.Model(oldRoot).Update("path", fmt.Sprintf("/%d/", oldRoot.ID))
	oldRoot.Path = fmt.Sprintf("/%d/", oldRoot.ID)
	a := mkDept(t, tid, "fix8-ok-A", &oldRoot.ID, fmt.Sprintf("/%d/placeholder/", oldRoot.ID), 2)
	db.DB.Model(a).Update("path", fmt.Sprintf("/%d/%d/", oldRoot.ID, a.ID))
	a.Path = fmt.Sprintf("/%d/%d/", oldRoot.ID, a.ID)
	// 内存快照须与库内实况一致（handler 传的是刚读出的 dept：Depth=2）。
	// ⚠ 首跑红的教训：子树 depth 增量 = updates["depth"] - dept.Depth，吃的是**内存快照**；
	// 快照留 0 会把增量算成 2-0=2，整层 +1 是算术必然——"正向对照"用例自己被建歪，不是缺陷。
	a.Depth = 2
	newRoot := mkDept(t, tid, "fix8-ok-newroot", nil, "/placeholder2/", 1)
	db.DB.Model(newRoot).Update("path", fmt.Sprintf("/%d/", newRoot.ID))
	newRoot.Path = fmt.Sprintf("/%d/", newRoot.ID)
	// 给被移动节点留一条后代：子树重写只测"本节点 path 变了"是测不全的——
	// 深度平移量是 `新depth − 快照depth` 算出来的，而这个 delta **同时**加在本节点与后代身上，
	// 本节点又已被上面那条 UPDATE 显式设过，于是"被加两次"。首跑由 smoke_org 抓到（2→4），
	// 此处把两层深度都钉住，防止以后有人把 `id <> 自身` 那条限制去掉。
	c := mkDept(t, tid, "fix8-ok-C", &a.ID, fmt.Sprintf("/%d/%d/placeholder/", oldRoot.ID, a.ID), 3)
	db.DB.Model(c).Update("path", fmt.Sprintf("/%d/%d/%d/", oldRoot.ID, a.ID, c.ID))
	c.Path = fmt.Sprintf("/%d/%d/%d/", oldRoot.ID, a.ID, c.ID)

	updates := map[string]interface{}{"parent_id": newRoot.ID, "depth": 2}
	if err := db.DB.Transaction(func(tx *gorm.DB) error {
		return applyDeptUpdateTx(tx, a, updates, true, tid)
	}); err != nil {
		t.Fatalf("正向移动不应报错: %v", err)
	}
	var ra model.Department
	if err := db.DB.First(&ra, a.ID).Error; err != nil {
		t.Fatalf("回读 A 失败: %v", err)
	}
	if want := fmt.Sprintf("/%d/%d/", newRoot.ID, a.ID); ra.Path != want {
		t.Errorf("A 的新路径应为 %q，实得 %q", want, ra.Path)
	}
	if ra.Depth != newRoot.Depth+1 {
		t.Errorf("A 的深度应为新父深度+1=%d，实得 %d（被批量语句二次平移的现场）", newRoot.Depth+1, ra.Depth)
	}
	var rc model.Department
	if err := db.DB.First(&rc, c.ID).Error; err != nil {
		t.Fatalf("回读 C 失败: %v", err)
	}
	if want := fmt.Sprintf("/%d/%d/%d/", newRoot.ID, a.ID, c.ID); rc.Path != want {
		t.Errorf("C 的新路径应为 %q，实得 %q（子树没跟着搬）", want, rc.Path)
	}
	if rc.Depth != ra.Depth+1 {
		t.Errorf("C 的深度应为 A+1=%d，实得 %d（后代平移量算错）", ra.Depth+1, rc.Depth)
	}
	// 兄弟不被误伤：C 是 a 的孩子，oldRoot 的另一个孩子必须原样（前缀匹配不能吃掉同层邻居）
	sib := mkDept(t, tid, "fix8-ok-sib", &oldRoot.ID, fmt.Sprintf("/%d/placeholder-sib/", oldRoot.ID), 2)
	db.DB.Model(sib).Update("path", fmt.Sprintf("/%d/%d/", oldRoot.ID, sib.ID))
	sib.Path = fmt.Sprintf("/%d/%d/", oldRoot.ID, sib.ID)
	var rsib model.Department
	if err := db.DB.First(&rsib, sib.ID).Error; err != nil {
		t.Fatalf("回读兄弟部门失败: %v", err)
	}
	if rsib.Path != sib.Path || rsib.Depth != 2 {
		t.Errorf("兄弟部门被误伤：path 应 %q 实得 %q，depth 应 2 实得 %d", sib.Path, rsib.Path, rsib.Depth)
	}
}
