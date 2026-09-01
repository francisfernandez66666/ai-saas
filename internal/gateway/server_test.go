// Package gateway AI 网关测试：租户签名校验（HMAC）等网关鉴权路径。
package gateway

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
)

// 独立复现客户端（ai/gateway_client.go:signTenant）的 HMAC-SHA256 签名，
// 验证网关侧 verifyToken 能还原租户；证明签名方案两端一致。
func TestTenantSignVerify(t *testing.T) {
	secret := "test-gateway-secret"
	s := &Server{secret: secret}

	sign := func(tenantID uint) string {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(fmt.Sprintf("%d", tenantID)))
		return fmt.Sprintf("%d.%s", tenantID, hex.EncodeToString(mac.Sum(nil)))
	}

	for _, id := range []uint{1, 42, 9999} {
		token := sign(id)
		gotID, ok := s.verifyToken(token)
		if !ok || gotID != id {
			t.Errorf("verifyToken(%q) = (%d, %v), want (%d, true)", token, gotID, ok, id)
		}
	}

	// 篡改签名应被拒绝
	good := sign(7)
	tampered := "7.deadbeef"
	if _, ok := s.verifyToken(tampered); ok {
		t.Errorf("verifyToken(%q) accepted tampered token, want reject", tampered)
	}
	_ = good

	// 空 token 视为平台内部调用（tenant 0，放行）
	if _, ok := s.verifyToken(""); !ok {
		t.Errorf("verifyToken(\"\") rejected, want allow")
	}
}
