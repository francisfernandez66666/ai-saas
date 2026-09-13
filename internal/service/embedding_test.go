// Package service 业务服务层测试：向量嵌入客户端降级与关键词回退路径。
package service

import (
	"encoding/json"
	"testing"

	"ai-scrm/internal/model"
)

// fakeEmbeddingClient 测试用向量客户端：返回固定向量，不请求外部 embedding 服务。
type fakeEmbeddingClient struct {
	vec []float32
}

func (c fakeEmbeddingClient) Embed(string) []float32 {
	return c.vec
}

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

// TestKbVectorSearchEnabled 验证 D3 热开关缺省为 true，显式 false 时关闭向量请求。
func TestKbVectorSearchEnabled(t *testing.T) {
	old := DefaultSystemConfigService
	defer func() { DefaultSystemConfigService = old }()

	if !kbVectorSearchEnabled() {
		t.Fatal("未初始化配置中心时应默认启用向量检索")
	}
	DefaultSystemConfigService = &SystemConfigService{cache: map[string]string{"kb_vector_search": "false"}}
	if kbVectorSearchEnabled() {
		t.Fatal("kb_vector_search=false 时应关闭向量检索")
	}
}

// TestEmbedAndSetFragmentUnsavedStoresJSON 验证未落库片段只暂存 JSON，不触发向量列回写。
func TestEmbedAndSetFragmentUnsavedStoresJSON(t *testing.T) {
	oldClient := DefaultEmbeddingClient
	oldPg := pgvectorEnabled
	defer func() {
		DefaultEmbeddingClient = oldClient
		pgvectorEnabled = oldPg
	}()
	DefaultEmbeddingClient = fakeEmbeddingClient{vec: []float32{0.5, 0.25}}
	pgvectorEnabled = false

	frag := &model.KnowledgeFragment{Title: "续航", Content: "高速续航"}
	EmbedAndSetFragment(frag)

	var got []float32
	if err := json.Unmarshal([]byte(frag.EmbeddingJSON), &got); err != nil {
		t.Fatalf("embedding_json 应为合法 JSON: %v", err)
	}
	if len(got) != 2 || got[0] != 0.5 || got[1] != 0.25 {
		t.Fatalf("embedding_json 写入异常: %v", got)
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
