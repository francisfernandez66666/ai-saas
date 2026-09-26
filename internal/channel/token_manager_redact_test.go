// 通道层错误串脱敏收口的单测（FIX-2，2026-09-26）。
// 反证方向：删掉 redactErr 后本文件第一条用例会红（错误串里出现密钥明文）。
package channel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestFetchWecomTokenRedactsSecretInNetworkError 取 token 的网络失败必须"留着线索、抹掉密钥"：
// Go 的 *url.Error 会把完整 URL（含 corpsecret）拼进 Error()，这条串会落进
// channel_outbound.error 并在 /admin/channels/dead-letters 整行回显——所以脱敏必须在产生处做。
func TestFetchWecomTokenRedactsSecretInNetworkError(t *testing.T) {
	const secret = "UNITTEST_CORPSECRET_PLAINTEXT"
	tm := NewTokenManager()
	// 已关闭的端口 ⇒ 必定传输层失败（不依赖外网），错误形态即真实泄露形态 *url.Error
	_, _, err := tm.FetchWecomToken(context.Background(), "http://127.0.0.1:1", "ww1234567", secret)
	if err == nil {
		t.Fatal("期望网络错误，却返回 nil")
	}
	msg := err.Error()
	if strings.Contains(msg, secret) {
		t.Errorf("corpsecret 明文泄进错误串: %q", msg)
	}
	if strings.Contains(msg, "corpid=ww1234567&corpsecret="+secret[:4]) {
		t.Errorf("query 未脱敏: %q", msg)
	}
	// 排障线索必须活着：运维要靠 host + 路径 + 真因判断是网关问题还是本地网络问题
	for _, want := range []string{"127.0.0.1:1", "gettoken", "corpid=ww1234567", "corpsecret=***"} {
		if !strings.Contains(msg, want) {
			t.Errorf("错误串缺少 %q（脱敏过度或形态判断错）: %q", want, msg)
		}
	}
}

// TestFetchMPWechatTokenRedactsSecret 公众号侧同一口径（secret 参数名不同，名单要都覆盖）。
func TestFetchMPWechatTokenRedactsSecret(t *testing.T) {
	const secret = "UNITTEST_APPSECRET_PLAINTEXT"
	tm := NewTokenManager()
	_, _, err := tm.FetchMPWechatToken(context.Background(), "http://127.0.0.1:1", "wxapp", secret)
	if err == nil {
		t.Fatal("期望网络错误，却返回 nil")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("公众号 secret 明文泄进错误串: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "secret=***") {
		t.Errorf("期望 secret=***，实际 %q", err.Error())
	}
}

// TestTokenFailedTypePreserved 反向护栏：脱敏只能作用于"确实含凭据"的错误。
// 渠道业务错误（*ErrTokenFailed）靠 errors.As 判 -1 触发 token 强制刷新重试，
// 若图省事无脑 errors.New 包一层，isTokenInvalidErr 会永久判 false，出站重试链一起坏掉。
func TestTokenFailedTypePreserved(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"errcode": 40001, "errmsg": "invalid credential"})
	}))
	defer srv.Close()

	tm := NewTokenManager()
	_, code, err := tm.FetchWecomToken(context.Background(), srv.URL, "ww1", "sec")
	if err == nil {
		t.Fatal("业务错误码应返回 error")
	}
	var tf *ErrTokenFailed
	if !errors.As(err, &tf) {
		t.Fatalf("业务错误类型被洗掉了（应为 *ErrTokenFailed）: %T %v", err, err)
	}
	if tf.Code != 40001 || code != 40001 {
		t.Errorf("错误码丢失: code=%d tf.Code=%d", code, tf.Code)
	}
	if !isTokenInvalidErr(err) {
		t.Error("40001 应判 token 失效（触发强制刷新重试），判定链已断")
	}
}

// TestTruncateErrRedactsStoredError 落库最后一道闸：即使某个新调用点忘了在产生处脱敏，
// 进 channel_outbound.error 列时也必须已经被抹掉密钥。
func TestTruncateErrRedactsStoredError(t *testing.T) {
	const secret = "LATEX_CORPSECRET"
	raw := errors.New(`Get "https://qyapi.weixin.qq.com/cgi-bin/gettoken?corpid=ww1&corpsecret=` + secret + `": dial tcp`)
	got := truncateErr(raw)
	if strings.Contains(got, secret) {
		t.Errorf("落库侧兜底失效，密钥进了数据列: %q", got)
	}
	if !strings.Contains(got, "corpsecret=***") {
		t.Errorf("期望落库串已脱敏: %q", got)
	}
	// 空错误与纯文本错误不得被改动（防误伤）
	if truncateErr(nil) != "" {
		t.Error("nil 错误应返回空串")
	}
	if s := truncateErr(errors.New("通道不存在 id=3")); s != "通道不存在 id=3" {
		t.Errorf("无凭据错误被改写: %q", s)
	}
}
