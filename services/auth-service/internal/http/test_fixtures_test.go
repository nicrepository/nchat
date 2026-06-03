package httpapi_test

import (
	"crypto/sha256"
	"encoding/base64"
	"testing"

	"github.com/nicrepository/nchat/services/auth-service/internal/service"
)

func makeTestOpaqueValue(label string) string {
	sum := sha256.Sum256([]byte("nchat-http-test:" + label))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func mustAccessToken(t *testing.T, userID, sessionID string) (string, *service.TokenManager) {
	t.Helper()
	tokens := makeTestTokenManager(t)
	accessToken, _, err := tokens.GenerateAccessToken(userID, sessionID)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	return accessToken, tokens
}
