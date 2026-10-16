// Package gateway 测试：P2-2 SSE 流式输出的纯函数部分（分片 + 帧格式）。
// 说明：完整 handler 依赖 ai.Router 真实降级链（需模型），其计量/降级逻辑与非流式
// handleChatCompletions 同源；此处对可确定性单测的分片与帧格式做断言。
package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestSplitForStream 分片：步长正确、可无损重组、rune 安全（不切断多字节）、空串/非法步长。
func TestSplitForStream(t *testing.T) {
	cases := []struct {
		name string
		in   string
		step int
	}{
		{"ASCII 定长", "abcdefghij", 3},
		{"中文 rune 安全", "你好世界你好世界", 4},
		{"空串", "", 4},
		{"单字符", "x", 4},
		{"非法步长回退", "abcdef", 0},
	}
	for _, c := range cases {
		parts := splitForStream(c.in, c.step)
		// 无损重组
		if got := strings.Join(parts, ""); got != c.in {
			t.Errorf("%s: 重组后=%q, want %q", c.name, got, c.in)
		}
		// 每片不超过步长（合法步长时）；中文按 rune 计不切断
		step := c.step
		if step <= 0 {
			step = 24
		}
		for _, p := range parts {
			if len([]rune(p)) > step {
				t.Errorf("%s: 分片 %q 超过步长 %d", c.name, p, step)
			}
		}
	}
	// 中文必须按 rune 边界切（每片都是有效字符串，Join 无损已由上覆盖）
	if parts := splitForStream("你好世界", 1); len(parts) != 4 || parts[0] != "你" {
		t.Errorf("中文单字分片错误: %v", parts)
	}
}

// TestWriteSSEChunk 帧格式：data: 前缀 + 双换行 + OpenAI chunk 结构 + finish_reason null/stop。
func TestWriteSSEChunk(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// 中间帧（content + finish 空 → finish_reason 应为 null）
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	writeSSEChunk(c, "gw-1", "m-x", gin.H{"content": "hi"}, "")
	body := w.Body.String()
	if !strings.HasPrefix(body, "data: ") || !strings.HasSuffix(body, "\n\n") {
		t.Fatalf("SSE 帧格式错误: %q", body)
	}
	var frame struct {
		Object  string `json:"object"`
		Model   string `json:"model"`
		Choices []struct {
			Index        int             `json:"index"`
			Delta        json.RawMessage `json:"delta"`
			FinishReason *string         `json:"finish_reason"`
		} `json:"choices"`
	}
	payload := strings.TrimSuffix(strings.TrimPrefix(body, "data: "), "\n\n")
	if err := json.Unmarshal([]byte(payload), &frame); err != nil {
		t.Fatalf("帧 JSON 解析失败: %v (%q)", err, payload)
	}
	if frame.Object != "chat.completion.chunk" || frame.Model != "m-x" {
		t.Errorf("object/model 错误: %+v", frame)
	}
	if len(frame.Choices) != 1 || frame.Choices[0].Index != 0 {
		t.Fatalf("choices 结构错误: %+v", frame.Choices)
	}
	if frame.Choices[0].FinishReason != nil {
		t.Errorf("中间帧 finish_reason 应为 null, got %v", *frame.Choices[0].FinishReason)
	}

	// 终帧（finish=stop → finish_reason 应为 "stop"）
	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	writeSSEChunk(c2, "gw-1", "m-x", gin.H{}, "stop")
	var frame2 struct {
		Choices []struct {
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
	}
	payload2 := strings.TrimSuffix(strings.TrimPrefix(w2.Body.String(), "data: "), "\n\n")
	if err := json.Unmarshal([]byte(payload2), &frame2); err != nil {
		t.Fatalf("终帧解析失败: %v", err)
	}
	if frame2.Choices[0].FinishReason == nil || *frame2.Choices[0].FinishReason != "stop" {
		t.Errorf("终帧 finish_reason 应为 stop, got %v", frame2.Choices[0].FinishReason)
	}
}
