package service_test

import (
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/nicrepository/nchat/services/auth-service/internal/service"
)

const (
	testIssuer   = "test-issuer"
	testAudience = "test-audience"
)

func testTokenConfig(secret string) service.TokenConfig {
	return service.TokenConfig{
		HMACSecret: secret,
		Issuer:     testIssuer,
		Audience:   testAudience,
		AccessTTL:  15 * time.Minute,
		RefreshTTL: 30 * 24 * time.Hour,
	}
}

func newTestTokenManager(t *testing.T, secret string) *service.TokenManager {
	t.Helper()

	manager, err := service.NewTokenManager(testTokenConfig(secret))
	if err != nil {
		t.Fatalf("NewTokenManager: %v", err)
	}
	return manager
}

func TestTokenManager_GenerateAndValidateAccessToken(t *testing.T) {
	secret := strings.Repeat("a", 32)
	manager := newTestTokenManager(t, secret)

	token, expiresIn, err := manager.GenerateAccessToken("user-123", "session-456")
	if err != nil {
		t.Fatalf("GenerateAccessToken: %v", err)
	}
	if token == "" {
		t.Fatal("expected signed token")
	}
	if expiresIn != 900 {
		t.Fatalf("expected expires_in 900, got %d", expiresIn)
	}

	claims, err := manager.ValidateAccessToken(token)
	if err != nil {
		t.Fatalf("ValidateAccessToken: %v", err)
	}
	if claims.Subject != "user-123" {
		t.Fatalf("expected subject user-123, got %q", claims.Subject)
	}
	if claims.SessionID != "session-456" {
		t.Fatalf("expected session id session-456, got %q", claims.SessionID)
	}
	if claims.Issuer != testIssuer {
		t.Fatalf("expected issuer %q, got %q", testIssuer, claims.Issuer)
	}
	if !claimStringsContain(claims.Audience, testAudience) {
		t.Fatalf("expected audience %q in %+v", testAudience, claims.Audience)
	}
	if claims.ID == "" {
		t.Fatal("expected jti claim")
	}
	if claims.IssuedAt == nil || claims.NotBefore == nil || claims.ExpiresAt == nil {
		t.Fatalf("expected iat, nbf and exp claims: %+v", claims.RegisteredClaims)
	}
}

func TestTokenManager_ExpiredAccessTokenInvalid(t *testing.T) {
	secret := strings.Repeat("b", 32)
	manager := newTestTokenManager(t, secret)
	now := time.Now().UTC()
	claims := service.AccessClaims{
		SessionID: "session-456",
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "user-123",
			Issuer:    testIssuer,
			Audience:  jwt.ClaimStrings{testAudience},
			IssuedAt:  jwt.NewNumericDate(now.Add(-2 * time.Hour)),
			NotBefore: jwt.NewNumericDate(now.Add(-2 * time.Hour)),
			ExpiresAt: jwt.NewNumericDate(now.Add(-1 * time.Hour)),
			ID:        "jwt-id",
		},
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}

	if _, err := manager.ValidateAccessToken(token); err == nil {
		t.Fatal("expected expired token to be invalid")
	}
}

func TestTokenManager_WrongSecretInvalid(t *testing.T) {
	signer := newTestTokenManager(t, strings.Repeat("c", 32))
	validator := newTestTokenManager(t, strings.Repeat("d", 32))

	token, _, err := signer.GenerateAccessToken("user-123", "session-456")
	if err != nil {
		t.Fatalf("GenerateAccessToken: %v", err)
	}

	if _, err := validator.ValidateAccessToken(token); err == nil {
		t.Fatal("expected wrong secret to be invalid")
	}
}

func TestTokenManager_RejectsMissingOrShortSecret(t *testing.T) {
	for _, secret := range []string{"", "short"} {
		t.Run(secret, func(t *testing.T) {
			_, err := service.NewTokenManager(testTokenConfig(secret))
			if err == nil {
				t.Fatal("expected secret validation error")
			}
		})
	}
}

func TestTokenManager_RejectsInvalidConfig(t *testing.T) {
	base := testTokenConfig(strings.Repeat("j", 32))
	tests := []struct {
		name   string
		mutate func(*service.TokenConfig)
	}{
		{name: "issuer", mutate: func(cfg *service.TokenConfig) { cfg.Issuer = "" }},
		{name: "audience", mutate: func(cfg *service.TokenConfig) { cfg.Audience = "" }},
		{name: "access ttl", mutate: func(cfg *service.TokenConfig) { cfg.AccessTTL = 0 }},
		{name: "refresh ttl", mutate: func(cfg *service.TokenConfig) { cfg.RefreshTTL = 0 }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := base
			tt.mutate(&cfg)
			if _, err := service.NewTokenManager(cfg); err == nil {
				t.Fatal("expected config validation error")
			}
		})
	}
}

func TestTokenManager_GenerateAccessTokenRequiresSubjectAndSession(t *testing.T) {
	manager := newTestTokenManager(t, strings.Repeat("k", 32))

	if _, _, err := manager.GenerateAccessToken("", "session-456"); err == nil {
		t.Fatal("expected missing subject error")
	}
	if _, _, err := manager.GenerateAccessToken("user-123", ""); err == nil {
		t.Fatal("expected missing session error")
	}
}

func TestTokenManager_RejectsAccessTokenWithMissingClaims(t *testing.T) {
	secret := strings.Repeat("l", 32)
	manager := newTestTokenManager(t, secret)
	now := time.Now().UTC()
	claims := service.AccessClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "user-123",
			Issuer:    testIssuer,
			Audience:  jwt.ClaimStrings{testAudience},
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Minute)),
			ID:        "jwt-id",
		},
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}

	if _, err := manager.ValidateAccessToken(token); err == nil {
		t.Fatal("expected token missing sid to be invalid")
	}
}

func TestTokenManager_RejectsUnexpectedSigningMethod(t *testing.T) {
	secret := strings.Repeat("m", 32)
	manager := newTestTokenManager(t, secret)
	now := time.Now().UTC()
	claims := service.AccessClaims{
		SessionID: "session-456",
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "user-123",
			Issuer:    testIssuer,
			Audience:  jwt.ClaimStrings{testAudience},
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Minute)),
			ID:        "jwt-id",
		},
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS384, claims).SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}

	if _, err := manager.ValidateAccessToken(token); err == nil {
		t.Fatal("expected unexpected signing method to be invalid")
	}
}

func TestTokenManager_GeneratesOpaqueRefreshTokenHash(t *testing.T) {
	manager := newTestTokenManager(t, strings.Repeat("e", 32))

	raw, hash, expiresAt, err := manager.GenerateRefreshToken()
	if err != nil {
		t.Fatalf("GenerateRefreshToken: %v", err)
	}
	if len(raw) < 32 {
		t.Fatalf("expected opaque refresh token length >= 32, got %d", len(raw))
	}
	if hash == raw {
		t.Fatal("refresh token hash must not equal raw token")
	}
	if strings.Contains(hash, raw) {
		t.Fatal("refresh token hash must not contain raw token")
	}
	if len(hash) != 64 {
		t.Fatalf("expected hex SHA-256/HMAC hash length 64, got %d", len(hash))
	}
	if !expiresAt.After(time.Now().Add(29 * 24 * time.Hour)) {
		t.Fatalf("expected refresh expiry about 30 days out, got %s", expiresAt)
	}
}

func TestTokenManager_HashesDeviceFingerprintWithSecret(t *testing.T) {
	first := newTestTokenManager(t, strings.Repeat("f", 32))
	second := newTestTokenManager(t, strings.Repeat("g", 32))

	firstHash := first.HashDeviceFingerprint("raw-device")
	if firstHash == "" || firstHash == "raw-device" {
		t.Fatalf("expected hashed fingerprint, got %q", firstHash)
	}
	if firstHash != first.HashDeviceFingerprint("raw-device") {
		t.Fatal("expected deterministic fingerprint hash for the same secret and raw value")
	}
	if firstHash == second.HashDeviceFingerprint("raw-device") {
		t.Fatal("different secrets must produce different fingerprint hashes")
	}
	if first.HashDeviceFingerprint("") != "" {
		t.Fatal("empty fingerprint should remain empty")
	}
}

func TestTokenManager_GenerateOpaqueToken(t *testing.T) {
	manager := newTestTokenManager(t, strings.Repeat("o", 32))

	raw, err := manager.GenerateOpaqueToken()
	if err != nil {
		t.Fatalf("GenerateOpaqueToken: %v", err)
	}
	if len(raw) < 32 {
		t.Fatalf("expected opaque token length >= 32, got %d", len(raw))
	}
	if strings.ContainsAny(raw, "+/") {
		t.Fatalf("expected base64url token, got %q", raw)
	}
}

func TestTokenManager_HashesPasswordResetAndInviteTokensWithDomainSeparation(t *testing.T) {
	manager := newTestTokenManager(t, strings.Repeat("p", 32))
	raw := makeTestOpaqueValue("domain-separated-hash-input")

	resetHash := manager.HashPasswordResetToken(raw)
	inviteHash := manager.HashInviteToken(raw)

	if resetHash == "" || inviteHash == "" {
		t.Fatal("expected token hashes")
	}
	if resetHash == raw || inviteHash == raw {
		t.Fatal("token hash must not equal raw token")
	}
	if strings.Contains(resetHash, raw) || strings.Contains(inviteHash, raw) {
		t.Fatal("token hashes must not contain raw token")
	}
	if len(resetHash) != 64 || len(inviteHash) != 64 {
		t.Fatalf("expected hex SHA-256/HMAC hashes, got reset=%d invite=%d", len(resetHash), len(inviteHash))
	}
	if resetHash == inviteHash {
		t.Fatal("password reset and invite hashes must be domain-separated")
	}
	if resetHash != manager.HashPasswordResetToken(raw) || inviteHash != manager.HashInviteToken(raw) {
		t.Fatal("token hashes must be deterministic")
	}
}

func claimStringsContain(values jwt.ClaimStrings, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
