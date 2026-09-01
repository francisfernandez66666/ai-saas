// Package service 业务服务层测试：话术离线评分（纯函数预筛）与 LLM 深度评估钩子。
package service

import "testing"

// TestScoreReplyOffline 验证离线回复评分：空/过短/命中锚词/含违规词四种情形
func TestScoreReplyOffline(t *testing.T) {
	// 空回复 = 0
	if r := ScoreReplyOffline("", nil, nil); r.Score != 0 {
		t.Errorf("空回复应 0, got %f", r.Score)
	}
	// 过短扣分
	if r := ScoreReplyOffline("好的", nil, nil); r.Score >= 5 {
		t.Errorf("过短回复不应满分, got %f", r.Score)
	}
	// 命中锚词加分（上限5）
	long := "这款越野车有四驱和差速锁，周末去郊外很合适，您留个手机号我发配置单"
	r := ScoreReplyOffline(long, []string{"越野车", "四驱"}, nil)
	if r.Score <= 0 || r.Score > 5 {
		t.Errorf("正常话术评分应在(0,5], got %f", r.Score)
	}
	// 违规词重扣
	if r := ScoreReplyOffline("这是内部价优惠，别外传", nil, []string{"内部价"}); r.Score >= 5 {
		t.Errorf("含违规词不应满分, got %f", r.Score)
	}
}

// TestEvaluateWithLLM 验证 LLM 深度评估钩子：未注入回退0；注入后返回评分与理由
// 通过 service.EvalLLMFunc 包级函数变量注入（main.go 组合根桥接 strategy.GenerateEvals），
// 本测试直接赋桩验证调用链与兜底逻辑，无需 DB/真实模型。
func TestEvaluateWithLLM(t *testing.T) {
	// 1. 未注入钩子 → 回退 0（跳过 LLM 深度评估）
	old := EvalLLMFunc
	EvalLLMFunc = nil
	if score, _ := evaluateWithLLM(1, "你好"); score != 0 {
		t.Errorf("未注入钩子应回退 0, got %f", score)
	}
	// 2. 注入钩子 → 正常返回评分与理由
	EvalLLMFunc = func(tenantID uint, content string) (float64, []string) {
		if tenantID != 42 {
			t.Errorf("tenantID 透传错误: %d", tenantID)
		}
		if content != "素材内容" {
			t.Errorf("content 透传错误: %s", content)
		}
		return 4.5, []string{"需求针对性强"}
	}
	score, reasons := evaluateWithLLM(42, "素材内容")
	if score != 4.5 {
		t.Errorf("注入钩子应返回 4.5, got %f", score)
	}
	if len(reasons) != 1 || reasons[0] != "需求针对性强" {
		t.Errorf("理由透传错误: %v", reasons)
	}
	// 3. 钩子返回非法评分（≤0）→ 回退 0，不污染结果
	EvalLLMFunc = func(tenantID uint, content string) (float64, []string) { return 0, []string{"无效"} }
	if score, _ := evaluateWithLLM(1, "x"); score != 0 {
		t.Errorf("非法评分应回退 0, got %f", score)
	}
	EvalLLMFunc = old
}
