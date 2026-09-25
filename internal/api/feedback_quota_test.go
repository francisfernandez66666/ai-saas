// 反馈每日额度的口径回归测试（2026-09-25 欠账批）。
//
// 背景：满意度评分（POST /feedback/rating）与用户反馈共用 feedbacks 表，靠 target_type
// 区分（'rating' vs 'ai_reply'/'feature'/'other'），各自另有额度：反馈 20 条/天/用户、
// 评分 5 次/天/(用户+客户)。旧写法把评分行也计进「反馈 20 条」，于是——
// 一个顾问当天给七个客户各评三次（21 行 rating、零条真反馈）再提建议，
// 会收到「今日反馈已达上限（20条），请明日再试」：这句话是假的，用户无从解释自己哪里超限。
// 现场来自顾问台回归（uat_advisor 连跑七轮后 /feedback 恒 429）。
//
// 两条用例是一对判别力：只留第一条，把过滤整个删掉也能过（那是回滚到缺陷）；
// 只留第二条，则「干脆不限流」也能过。
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/testutil"
)

// seedFeedbackRows 以直插方式给某用户造若干条今日反馈/评分行（绕过端点，免被端点自身限流干扰）。
func seedFeedbackRows(t *testing.T, tenantID, userID uint, targetType string, n int) {
	t.Helper()
	rows := make([]model.Feedback, 0, n)
	for i := 0; i < n; i++ {
		rows = append(rows, model.Feedback{
			TenantID:   tenantID,
			UserID:     userID,
			TargetType: targetType,
			Content:    fmt.Sprintf("seed-%s-%d", targetType, i),
			Status:     "open",
		})
	}
	if err := db.DB.Create(&rows).Error; err != nil {
		t.Fatalf("造 %s 行失败: %v", targetType, err)
	}
}

// postFeedback 用指定租户+用户会话调 CreateFeedback，回传 HTTP 状态码。
func postFeedback(t *testing.T, tenantID, userID uint, content string) int {
	t.Helper()
	c, w := newCtx(t, tenantID)
	c.Set("user_id", userID)
	c.Set("username", "fb_quota_test")
	c.Set("role", "sales")
	c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"target_type":"feature","content":"`+content+`"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	CreateFeedback(c)
	if c.Writer.Status() == http.StatusOK {
		var resp struct {
			Data model.Feedback `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("解析反馈响应失败: %v body=%s", err, w.Body.String())
		}
	}
	return c.Writer.Status()
}

// TestFeedbackQuotaIgnoresRatingRows 20 条今日评分行不得吃掉反馈额度（修复前恒 429）。
func TestFeedbackQuotaIgnoresRatingRows(t *testing.T) {
	testutil.SetupTestDB(t)
	code := testutil.CreateTenantCode(t, "fbq_rating")
	defer testutil.CleanupTenant(t, code)
	seedFeedbackRows(t, code, 9101, "rating", 20)

	if st := postFeedback(t, code, 9101, "建议顾问台加个批量打标"); st != http.StatusOK {
		t.Fatalf("只有评分行时提交反馈应 200，实际 %d（评分行侵占反馈额度）", st)
	}
}

// TestFeedbackQuotaStillBlocksAfterFeedbacks 20 条今日真反馈仍必须挡在门外（防"干脆去掉过滤"）。
func TestFeedbackQuotaStillBlocksAfterFeedbacks(t *testing.T) {
	testutil.SetupTestDB(t)
	code := testutil.CreateTenantCode(t, "fbq_full")
	defer testutil.CleanupTenant(t, code)
	seedFeedbackRows(t, code, 9102, "feature", 20)

	if st := postFeedback(t, code, 9102, "第 21 条反馈"); st != http.StatusTooManyRequests {
		t.Fatalf("满 20 条真反馈后应 429，实际 %d（额度过滤被去掉=限流失效）", st)
	}
}

// TestFeedbackQuotaIsPerUser 额度按用户计，同租户另一用户不受影响（顺带钉住 user_id 谓词）。
func TestFeedbackQuotaIsPerUser(t *testing.T) {
	testutil.SetupTestDB(t)
	code := testutil.CreateTenantCode(t, "fbq_user")
	defer testutil.CleanupTenant(t, code)
	seedFeedbackRows(t, code, 9103, "feature", 20)

	if st := postFeedback(t, code, 9104, "别人的额度与我无关"); st != http.StatusOK {
		t.Fatalf("另一用户提交反馈应 200，实际 %d", st)
	}
}
