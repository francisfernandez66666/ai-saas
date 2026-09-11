// Package gateway AI 网关测试：租户签名校验（HMAC + ts 防重放）等网关鉴权路径。
package gateway

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
	"time"
)

// 独立复现客户端（ai/gateway_client.go:signTenant）的 HMAC-SHA256 签名，
// 验证网关侧 verifyToken 能还原租户；证明签名方案两端一致。
// P0-5/P1-40 后协议为 <tenantID>.<ts>.<sig>，sig=HMAC(secret, "tenantID.ts")；空/旧两段式拒绝。
func TestTenantSignVerify(t *testing.T) {
	secret := "test-gateway-secret"
	s := &Server{secret: secret}

	sign := func(tenantID uint, ts int64) string {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(fmt.Sprintf("%d.%d", tenantID, ts)))
		return fmt.Sprintf("%d.%d.%s", tenantID, ts, hex.EncodeToString(mac.Sum(nil)))
	}

	for _, id := range []uint{1, 42, 9999} {
		token := sign(id, time.Now().Unix())
		gotID, ok := s.verifyToken(token)
		if !ok || gotID != id {
			t.Errorf("verifyToken(%q) = (%d, %v), want (%d, true)", token, gotID, ok, id)
		}
	}

	// 篡改签名应被拒绝
	if _, ok := s.verifyToken("7.1234567.deadbeef"); ok {
		t.Errorf("verifyToken(tampered) accepted, want reject")
	}
	// 旧两段式协议（无 ts）应被拒绝
	if _, ok := s.verifyToken("7.deadbeefdeadbeef"); ok {
		t.Errorf("verifyToken(legacy 2-part) accepted, want reject")
	}
	// ts 超出 ±5min 窗口应被拒绝（P1-40 防重放）
	oldTok := sign(7, time.Now().Unix()-3600)
	if _, ok := s.verifyToken(oldTok); ok {
		t.Errorf("verifyToken(stale ts) accepted, want reject")
	}
	// 空 token 拒绝（P0-5 fail-closed）
	if _, ok := s.verifyToken(""); ok {
		t.Errorf("verifyToken(\"\") accepted, want reject")
	}
}
