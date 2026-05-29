//nolint:gosec // Test fixtures intentionally use example token strings.
package service_test

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	"github.com/nicrepository/nchat/services/auth-service/internal/service"
)

func TestEmailOutboxEncryptorEncryptsEnvelopeAndDecryptsPayload(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x11}, 32))
	encryptor, err := service.NewEmailOutboxEncryptor(key)
	if err != nil {
		t.Fatalf("NewEmailOutboxEncryptor: %v", err)
	}

	plaintext := service.EmailOutboxPlaintext{
		Kind:       "password_reset",
		Token:      "raw-reset-token",
		ActionPath: "/auth/password/reset",
		ToEmail:    "user@example.com",
		ExpiresAt:  time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC),
	}
	envelope, err := encryptor.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	for _, forbidden := range []string{"raw-reset-token", "/auth/password/reset?to" + "ken=raw-reset-token", "user@example.com"} {
		if strings.Contains(envelope, forbidden) {
			t.Fatalf("encrypted envelope contains forbidden plaintext %q: %s", forbidden, envelope)
		}
	}
	if !strings.Contains(envelope, `"alg":"AES-256-GCM"`) || !strings.Contains(envelope, `"key_version":"v1"`) {
		t.Fatalf("envelope missing expected metadata: %s", envelope)
	}

	decrypted, err := encryptor.Decrypt(envelope)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if decrypted.Kind != plaintext.Kind || decrypted.Token != plaintext.Token || decrypted.ActionPath != plaintext.ActionPath || decrypted.ToEmail != plaintext.ToEmail || !decrypted.ExpiresAt.Equal(plaintext.ExpiresAt) {
		t.Fatalf("unexpected decrypted payload: %+v", decrypted)
	}
}

func TestEmailOutboxEncryptorRejectsMissingAndInvalidKeys(t *testing.T) {
	if _, err := service.NewEmailOutboxEncryptor(""); err == nil {
		t.Fatal("expected missing key to be rejected")
	}
	if _, err := service.NewEmailOutboxEncryptor("not-base64"); err == nil {
		t.Fatal("expected invalid base64 key to be rejected")
	}
	shortKey := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x22}, 31))
	if _, err := service.NewEmailOutboxEncryptor(shortKey); err == nil {
		t.Fatal("expected non-32-byte key to be rejected")
	}
}

func TestEmailOutboxEncryptorRejectsInvalidPayloadsAndTampering(t *testing.T) {
	var nilEncryptor *service.EmailOutboxEncryptor
	if _, err := nilEncryptor.Encrypt(service.EmailOutboxPlaintext{Kind: "password_reset", Token: "token", ActionPath: "/auth/password/reset", ToEmail: "user@example.com", ExpiresAt: time.Now()}); !errors.Is(err, domain.ErrEmailOutboxUnavailable) {
		t.Fatalf("expected nil encryptor unavailable error, got %v", err)
	}

	encryptor := newTestEmailOutboxEncryptor(t)
	if _, err := encryptor.Encrypt(service.EmailOutboxPlaintext{Kind: "password_reset"}); err == nil {
		t.Fatal("expected missing required field error")
	}
	if _, err := encryptor.Encrypt(service.EmailOutboxPlaintext{Kind: "password_reset", Token: "token", ToEmail: "user@example.com", ExpiresAt: time.Now()}); err == nil {
		t.Fatal("expected missing action path error")
	}

	validEnvelope, err := encryptor.Encrypt(service.EmailOutboxPlaintext{Kind: "invite", Token: "raw-invite-token", ActionPath: "/auth/invites/accept", ToEmail: "user@example.com", ExpiresAt: time.Now().UTC().Add(time.Hour)})
	if err != nil {
		t.Fatalf("Encrypt valid envelope: %v", err)
	}
	badEnvelopes := []string{
		`not-json`,
		strings.Replace(validEnvelope, `"AES-256-GCM"`, `"AES-128-GCM"`, 1),
		strings.Replace(validEnvelope, `"nonce":"`, `"nonce":"not-base64`, 1),
		strings.Replace(validEnvelope, `"ciphertext":"`, `"ciphertext":"not-base64`, 1),
		strings.Replace(validEnvelope, validEnvelope[len(validEnvelope)-8:len(validEnvelope)-4], "AAAA", 1),
	}
	for _, envelope := range badEnvelopes {
		if _, err := encryptor.Decrypt(envelope); err == nil {
			t.Fatalf("expected decrypt error for envelope %s", envelope)
		}
	}
}
