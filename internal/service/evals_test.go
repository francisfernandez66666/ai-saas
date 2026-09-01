// Package service：internal/service 模块（自动补包注释）。
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
