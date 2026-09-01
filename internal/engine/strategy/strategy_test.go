// Package strategy 策略中心测试：覆盖抗性检测等核心推理步骤。
package strategy

import "testing"

// 抗性识别：返回非 0 即判定存在抗性
func TestDetectResistance(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"这车太贵了", true},
		{"宝马X5比这个便宜", true},
		{"有点偏贵", true},
		{"这个价格很划算", true},
		{"你好我想了解越野车", false},
		{"今天天气不错", false},
	}
	for _, c := range cases {
		got := DetectResistance(c.text) != 0
		if got != c.want {
			t.Errorf("DetectResistance(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

// 到店意图识别（纯关键词，转发到 strategytypes）
func TestIsStoreVisitIntent(t *testing.T) {
	if !IsStoreVisitIntent("我想去店里看看") {
		t.Errorf("IsStoreVisitIntent(我想去店里看看) = false, want true")
	}
	if IsStoreVisitIntent("今天天气不错") {
		t.Errorf("IsStoreVisitIntent(今天天气不错) = true, want false")
	}
}
