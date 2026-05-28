package service

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	minJWTSecretBytes = 32
	refreshTokenBytes = 32
	bearerTokenType   = "Bearer"
)

// TokenConfig contains the cryptographic token settings used by auth-service.
type TokenConfig struct {
	HMACSecret string
	Issuer     string
	Audience   string
	AccessTTL  time.Duration
	RefreshTTL time.Duration
}

// AccessClaims are the signed JWT access token claims.
type AccessClaims struct {
	SessionID string `json:"sid"`
	jwt.RegisteredClaims
}

// TokenManager signs and validates access tokens and creates opaque refresh tokens.
type TokenManager struct {
	secret     []byte
	issuer     string
	audience   string
	accessTTL  time.Duration
	refreshTTL time.Duration
}

func NewTokenManager(cfg TokenConfig) (*TokenManager, error) {
	if len([]byte(cfg.HMACSecret)) < minJWTSecretBytes {
		return nil, fmt.Errorf("jwt hmac secret must be at least %d bytes", minJWTSecretBytes)
	}
	if cfg.Issuer == "" {
		return nil, fmt.Errorf("jwt issuer is required")
	}
	if cfg.Audience == "" {
		return nil, fmt.Errorf("jwt audience is required")
	}
	if cfg.AccessTTL <= 0 {
		return nil, fmt.Errorf("access token ttl must be positive")
	}
	if cfg.RefreshTTL <= 0 {
		return nil, fmt.Errorf("refresh token ttl must be positive")
	}
	return &TokenManager{
		secret:     []byte(cfg.HMACSecret),
		issuer:     cfg.Issuer,
		audience:   cfg.Audience,
		accessTTL:  cfg.AccessTTL,
		refreshTTL: cfg.RefreshTTL,
	}, nil
}

func (m *TokenManager) GenerateAccessToken(userID string, sessionID string) (string, int, error) {
	if userID == "" {
		return "", 0, fmt.Errorf("access token subject is required")
	}
	if sessionID == "" {
		return "", 0, fmt.Errorf("access token session id is required")
	}

	now := time.Now().UTC()
	jwtID, err := randomOpaqueString(16)
	if err != nil {
		return "", 0, fmt.Errorf("generate jwt id: %w", err)
	}
	claims := AccessClaims{
		SessionID: sessionID,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			Issuer:    m.issuer,
			Audience:  jwt.ClaimStrings{m.audience},
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(m.accessTTL)),
			ID:        jwtID,
		},
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(m.secret)
	if err != nil {
		return "", 0, fmt.Errorf("sign access token: %w", err)
	}
	return signed, int(m.accessTTL.Seconds()), nil
}

func (m *TokenManager) ValidateAccessToken(raw string) (AccessClaims, error) {
	var claims AccessClaims
	parser := jwt.NewParser(jwt.WithIssuer(m.issuer), jwt.WithAudience(m.audience), jwt.WithExpirationRequired())
	token, err := parser.ParseWithClaims(raw, &claims, func(token *jwt.Token) (any, error) {
		if token.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected jwt signing method")
		}
		return m.secret, nil
	})
	if err != nil {
		return AccessClaims{}, fmt.Errorf("validate access token: %w", err)
	}
	if !token.Valid {
		return AccessClaims{}, fmt.Errorf("invalid access token")
	}
	if claims.Subject == "" || claims.SessionID == "" || claims.ID == "" || claims.IssuedAt == nil || claims.NotBefore == nil || claims.ExpiresAt == nil {
		return AccessClaims{}, fmt.Errorf("access token missing required claims")
	}
	return claims, nil
}

func (m *TokenManager) GenerateRefreshToken() (string, string, time.Time, error) {
	raw, err := randomOpaqueString(refreshTokenBytes)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("generate refresh token: %w", err)
	}
	return raw, m.HashRefreshToken(raw), time.Now().UTC().Add(m.refreshTTL), nil
}

func (m *TokenManager) HashRefreshToken(raw string) string {
	mac := hmac.New(sha256.New, m.secret)
	_, _ = mac.Write([]byte(raw))
	return hex.EncodeToString(mac.Sum(nil))
}

func (m *TokenManager) HashDeviceFingerprint(raw string) string {
	if raw == "" {
		return ""
	}
	mac := hmac.New(sha256.New, m.secret)
	_, _ = mac.Write([]byte("nchat-device-fingerprint-v1:"))
	_, _ = mac.Write([]byte(raw))
	return hex.EncodeToString(mac.Sum(nil))
}

func randomOpaqueString(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
