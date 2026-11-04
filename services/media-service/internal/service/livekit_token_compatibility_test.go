package service

import (
	"errors"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	josejwt "github.com/go-jose/go-jose/v4/jwt"
	"github.com/golang-jwt/jwt/v5"
	"github.com/livekit/protocol/auth"
)

const compatibilityTestRoom = "production:channel:aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"

func TestLiveKitTokenEncoderInteroperatesWithGoJose(t *testing.T) {
	result, err := encodeLiveKitToken("livekit-test-key", liveKitSignerTestSecret,
		signerTestIdentity, "Ana Lima", compatibilityTestRoom, 5*time.Minute)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	parsed, err := josejwt.ParseSigned(result.Token, []jose.SignatureAlgorithm{jose.HS256})
	if err != nil {
		t.Fatalf("parse with go-jose: %v", err)
	}
	var claims josejwt.Claims
	var grants auth.ClaimGrants
	if err := parsed.Claims([]byte(liveKitSignerTestSecret), &claims, &grants); err != nil {
		t.Fatalf("verify with go-jose: %v", err)
	}
	if err := claims.Validate(josejwt.Expected{
		Issuer: "livekit-test-key", Subject: signerTestIdentity, Time: time.Now(),
	}); err != nil {
		t.Fatalf("validate with go-jose: %v", err)
	}
	if grants.Video == nil || grants.Video.Room != compatibilityTestRoom || !grants.Video.RoomJoin {
		t.Fatal("go-jose did not preserve the room grant")
	}
}

func TestLiveKitTokenEncoderPreservesJWTContract(t *testing.T) {
	result, err := encodeLiveKitToken("livekit-test-key", liveKitSignerTestSecret,
		signerTestIdentity, "Ana Lima", compatibilityTestRoom, 5*time.Minute)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	after := time.Now().UTC().Truncate(time.Second)
	claims := &jwt.RegisteredClaims{}
	token, err := jwt.ParseWithClaims(result.Token, claims, func(*jwt.Token) (any, error) {
		return []byte(liveKitSignerTestSecret), nil
	}, jwt.WithValidMethods([]string{"HS256"}), jwt.WithIssuer("livekit-test-key"),
		jwt.WithSubject(signerTestIdentity), jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(func() time.Time { return after }), jwt.WithIssuedAt())
	if err != nil {
		t.Fatalf("verify SDK token with independent JWT parser: %v", err)
	}
	if !token.Valid || token.Header["typ"] != "JWT" {
		t.Fatalf("unexpected JWT header: %+v", token.Header)
	}
	if claims.IssuedAt == nil || claims.NotBefore == nil || claims.ExpiresAt == nil {
		t.Fatal("expected iat, nbf and exp claims")
	}
	if !claims.IssuedAt.Equal(claims.NotBefore.Time) {
		t.Fatal("iat and nbf must use the same timestamp")
	}
	if claims.ExpiresAt.Sub(claims.NotBefore.Time) != 5*time.Minute {
		t.Fatal("signed lifetime differs from the authorized TTL")
	}
}

func TestLiveKitTokenVerifierAcceptsLegacyClaims(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	// The previous SDK emitted HS256 with iss/sub/nbf/exp and no iat.
	claims := jwt.MapClaims{
		"iss": "livekit-test-key", "sub": signerTestIdentity,
		"nbf": now.Add(-5 * time.Minute).Unix(), "exp": now.Add(5 * time.Minute).Unix(),
		"name": "Ana Lima", "video": map[string]any{
			"roomJoin": true, "room": compatibilityTestRoom,
			"canPublish": true, "canSubscribe": true, "canPublishData": false,
		},
	}
	raw := signedCompatibilityToken(t, jwt.SigningMethodHS256, claims)
	verifier, err := auth.ParseAPIToken(raw)
	if err != nil {
		t.Fatalf("parse legacy claims: %v", err)
	}
	registered, grants, err := verifier.Verify(liveKitSignerTestSecret)
	if err != nil {
		t.Fatalf("verify legacy claims: %v", err)
	}
	if grants.Identity != signerTestIdentity || grants.Name != "Ana Lima" {
		t.Fatal("legacy participant claims changed")
	}
	if registered.IssuedAt != nil {
		t.Fatal("legacy token must not require iat")
	}
	if grants.Video == nil || !grants.Video.RoomJoin || grants.Video.Room != compatibilityTestRoom {
		t.Fatal("legacy room grant changed")
	}
}

func TestLiveKitTokenVerifierRejectsInvalidSignedTokens(t *testing.T) {
	now := time.Now().UTC()
	validClaims := jwt.RegisteredClaims{
		Issuer: "livekit-test-key", Subject: signerTestIdentity,
		NotBefore: jwt.NewNumericDate(now.Add(-5 * time.Minute)),
		ExpiresAt: jwt.NewNumericDate(now.Add(5 * time.Minute)),
	}
	expired := validClaims
	expired.ExpiresAt = jwt.NewNumericDate(now.Add(-5 * time.Minute))
	future := validClaims
	future.NotBefore = jwt.NewNumericDate(now.Add(5 * time.Minute))
	valid := signedCompatibilityToken(t, jwt.SigningMethodHS256, validClaims)
	for _, tt := range []struct {
		name    string
		token   string
		key     string
		wantErr error
	}{
		{"wrong secret", valid, "incorrect-secret", jwt.ErrTokenSignatureInvalid},
		{"missing secret", valid, "", auth.ErrKeysMissing},
		{"expired", signedCompatibilityToken(t, jwt.SigningMethodHS256, expired), liveKitSignerTestSecret, jwt.ErrTokenExpired},
		{"not yet valid", signedCompatibilityToken(t, jwt.SigningMethodHS256, future), liveKitSignerTestSecret, jwt.ErrTokenNotValidYet},
		{"HS384", signedCompatibilityToken(t, jwt.SigningMethodHS384, validClaims), liveKitSignerTestSecret, jwt.ErrTokenSignatureInvalid},
		{"tampered signature", valid + "invalid", liveKitSignerTestSecret, jwt.ErrTokenSignatureInvalid},
	} {
		t.Run(tt.name, func(t *testing.T) {
			verifier, err := auth.ParseAPIToken(tt.token)
			if err != nil {
				t.Fatalf("parse signed token: %v", err)
			}
			claims, grants, err := verifier.Verify(tt.key)
			if !errors.Is(err, tt.wantErr) || claims != nil || grants != nil {
				t.Fatalf("expected fail closed with %v, got claims=%v grants=%v err=%v", tt.wantErr, claims, grants, err)
			}
		})
	}
}

func TestLiveKitTokenVerifierRejectsMalformedTokens(t *testing.T) {
	for _, raw := range []string{"", "invalid", "a.b.c", "eyJhbGciOiJub25lIn0.e30."} {
		if verifier, err := auth.ParseAPIToken(raw); err == nil {
			// Parsing alone is not authentication, including for alg=none.
			if _, _, err := verifier.Verify(liveKitSignerTestSecret); err == nil {
				t.Fatal("malformed or unsigned token was accepted")
			}
		}
	}
}

func signedCompatibilityToken(t *testing.T, method jwt.SigningMethod, claims jwt.Claims) string {
	t.Helper()
	raw, err := jwt.NewWithClaims(method, claims).SignedString([]byte(liveKitSignerTestSecret))
	if err != nil {
		t.Fatalf("sign compatibility fixture: %v", err)
	}
	return raw
}
