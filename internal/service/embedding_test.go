// Package service 业务服务层测试：向量嵌入客户端降级与关键词回退路径。
package service

import "testing"

// TestCosineSimilarity 验证余弦相似度：相同向量≈1、正交=0、维度不一致=0
func TestCosineSimilarity(t *testing.T) {
	a := []float32{1, 0, 0}
	b := []float32{1, 0, 0}
	if s := cosineSimilarity(a, b); s < 0.999 {
		t.Errorf("相同向量相似度应≈1, got %f", s)
	}
	c := []float32{0, 1, 0}
	if s := cosineSimilarity(a, c); s != 0 {
		t.Errorf("正交向量相似度应=0, got %f", s)
	}
	// 维度不一致返回0
	if s := cosineSimilarity(a, []float32{1, 0}); s != 0 {
		t.Errorf("维度不一致应=0, got %f", s)
	}
}

// TestSqrt32 验证 float32 开平方边界（0 与常规值）
func TestSqrt32(t *testing.T) {
	if s := sqrt32(0); s != 0 {
		t.Errorf("sqrt(0)=0 got %f", s)
	}
	if s := sqrt32(16); s < 3.99 || s > 4.01 {
		t.Errorf("sqrt(16)≈4 got %f", s)
	}
}

// TestToVectorLiteral 验证向量字面量格式化：缺省维度补零到 1536 维
func TestToVectorLiteral(t *testing.T) {
	// 缺省维度 1536：短向量补零，长向量裁剪
	got := toVectorLiteral([]float32{0.1, 0.2})
	// 应形如 [0.1,0.2,0,...] 共1536个元素
	if len(got) < 5 || got[0] != '[' || got[len(got)-1] != ']' {
		t.Fatalf("格式异常: %s", got)
	}
	// 计数逗号+1 = 元素数
	commas := 0
	for i := 0; i < len(got); i++ {
		if got[i] == ',' {
			commas++
		}
	}
	if commas+1 != 1536 {
		t.Errorf("期望1536维, 实际 %d", commas+1)
	}
}

// TestBigramSet 验证中文字符二元组分词（用于关键词分词回退）
func TestBigramSet(t *testing.T) {
	bs := bigramSet("越野车")
	// 相邻两字二元组：越野、野车（共2）
	if len(bs) != 2 {
		t.Errorf("bigramSet(\"越野车\") 期望2, got %d (%v)", len(bs), bs)
	}
	if !bs["越野"] || !bs["野车"] {
		t.Errorf("缺少预期二元组: %v", bs)
	}
}
