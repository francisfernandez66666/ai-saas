// C1 单测：词库加载、BLOCK/MASK 分级、改写、注释/空行、第三方机审叠加
package contentsafety

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withWordFile 临时替换全局词库文件并加载，返回还原函数
func withWordFile(t *testing.T, content string) {
	t.Helper()
	dir := t.TempDir()
	f := filepath.Join(dir, "sensitive_words.txt")
	if err := os.WriteFile(f, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	old := path
	path = f
	Load()
	t.Cleanup(func() {
		path = old
		Load()
		SetReviewer(nil)
	})
}

func TestCheckBlock(t *testing.T) {
	withWordFile(t, "BLOCK:洗钱\nMASK:全球第一\n# 注释\n\n")
	r := Check("这跟洗钱有关系吗")
	if !r.Hit || r.Level != LevelBlock {
		t.Fatalf("应命中 BLOCK, got %+v", r)
	}
}

func TestCheckMaskRewrite(t *testing.T) {
	withWordFile(t, "MASK:全球第一\n")
	r := Check("我们是全球第一的车企")
	if !r.Hit || r.Level != LevelMask {
		t.Fatalf("应命中 MASK, got %+v", r)
	}
	if strings.Contains(r.Cleaned, "全球第一") {
		t.Fatalf("Cleaned 应已替换规则词, got %q", r.Cleaned)
	}
	if !strings.Contains(r.Cleaned, "**") {
		t.Fatalf("Cleaned 应含 ** 占位, got %q", r.Cleaned)
	}
}

func TestBlockShortCircuits(t *testing.T) {
	withWordFile(t, "MASK:全球第一\nBLOCK:洗钱\n")
	r := Check("全球第一且洗钱")
	if r.Level != LevelBlock {
		t.Fatalf("BLOCK 优先级应最高, got %s", r.Level)
	}
}

func TestNoHitPassthrough(t *testing.T) {
	withWordFile(t, "BLOCK:洗钱\n")
	r := Check("你好，我想了解你们的越野车")
	if r.Hit {
		t.Fatal("正常话术不应命中")
	}
	if r.Cleaned != "你好，我想了解你们的越野车" {
		t.Fatal("未命中时 Cleaned 应等于原文")
	}
}

func TestEmptyText(t *testing.T) {
	withWordFile(t, "BLOCK:洗钱\n")
	if r := Check(""); r.Hit {
		t.Fatal("空串不应命中")
	}
}

type fakeReviewer struct {
	violation bool
	err       error
}

func (f fakeReviewer) Review(string) (bool, string, error) {
	return f.violation, "test", f.err
}

func TestReviewerMarksBlock(t *testing.T) {
	withWordFile(t, "BLOCK:洗钱\n")
	SetReviewer(fakeReviewer{violation: true})
	r := CheckFull("普通文本但机审判违规")
	if !r.Hit || r.Level != LevelBlock {
		t.Fatalf("机审违规应升级为 BLOCK, got %+v", r)
	}
}

func TestReviewerFailOpen(t *testing.T) {
	withWordFile(t, "BLOCK:洗钱\n")
	SetReviewer(fakeReviewer{err: errors.New("网络抖动")})
	r := CheckFull("正常文本")
	if r.Hit {
		t.Fatal("机审异常应 fail-open 放行")
	}
}
