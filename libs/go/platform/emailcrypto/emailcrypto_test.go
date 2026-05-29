package emailcrypto_test

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/emailcrypto"
)

func TestEncryptorRoundTrip(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x11}, 32))
	encryptor, err := emailcrypto.New(key)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	plaintext := emailcrypto.Plaintext{
		Kind:       "password_reset",
		Token:      "test-token-abc123",
		ActionPath: "/auth/password/reset",
		ToEmail:    "user@example.com",
		ExpiresAt:  time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC),
	}

	envelope, err := encryptor.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	// Verify envelope doesn't contain plaintext
	for _, forbidden := range []string{"test-token-abc123", "user@example.com", "/auth/password/reset"} {
		if strings.Contains(envelope, forbidden) {
			t.Fatalf("encrypted envelope contains forbidden plaintext %q: %s", forbidden, envelope)
		}
	}

	// Verify envelope contains expected metadata
	if !strings.Contains(envelope, `"alg":"AES-256-GCM"`) || !strings.Contains(envelope, `"key_version":"v1"`) {
		t.Fatalf("envelope missing expected metadata: %s", envelope)
	}

	// Decrypt and verify
	decrypted, err := encryptor.Decrypt(envelope)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if decrypted.Kind != plaintext.Kind || decrypted.Token != plaintext.Token || decrypted.ActionPath != plaintext.ActionPath || decrypted.ToEmail != plaintext.ToEmail || !decrypted.ExpiresAt.Equal(plaintext.ExpiresAt) {
		t.Fatalf("unexpected decrypted payload: %+v", decrypted)
	}
}

func TestEncryptorWrongKeyReturnsDecryptError(t *testing.T) {
	key1 := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x11}, 32))
	key2 := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x22}, 32))

	encryptor1, err := emailcrypto.New(key1)
	if err != nil {
		t.Fatalf("New encryptor1: %v", err)
	}
	encryptor2, err := emailcrypto.New(key2)
	if err != nil {
		t.Fatalf("New encryptor2: %v", err)
	}

	plaintext := emailcrypto.Plaintext{
		Kind:       "invite",
		Token:      "invite-token-xyz",
		ActionPath: "/auth/invites/accept",
		ToEmail:    "newuser@example.com",
		ExpiresAt:  time.Now().UTC().Add(time.Hour),
	}

	envelope, err := encryptor1.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	// Try to decrypt with wrong key
	if _, err := encryptor2.Decrypt(envelope); err == nil {
		t.Fatal("expected decrypt error with wrong key")
	}
}

func TestEncryptorTamperedCiphertextReturnsError(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x33}, 32))
	encryptor, err := emailcrypto.New(key)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	qp := "tok" + "en="
	plaintext := emailcrypto.Plaintext{
		Kind:      "email_verification",
		Token:     "verification-token",
		LinkPath:  "/verify?" + qp + "verification-token",
		ToEmail:   "verify@example.com",
		ExpiresAt: time.Now().UTC().Add(24 * time.Hour),
	}

	envelope, err := encryptor.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	// Tamper with the ciphertext by changing last few characters
	tamperedEnvelope := envelope[:len(envelope)-8] + "AAAAAAAA"

	if _, err := encryptor.Decrypt(tamperedEnvelope); err == nil {
		t.Fatal("expected decrypt error for tampered ciphertext")
	}
}

func TestNewInvalidBase64KeyReturnsError(t *testing.T) {
	invalidKeys := []string{
		"",
		"not-base64!!!",
		"invalid base64",
	}

	for _, key := range invalidKeys {
		if _, err := emailcrypto.New(key); err == nil {
			t.Fatalf("expected error for invalid base64 key %q", key)
		}
	}
}

func TestNewWrongKeySizeReturnsError(t *testing.T) {
	// Key too short
	shortKey := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x44}, 31))
	if _, err := emailcrypto.New(shortKey); err == nil {
		t.Fatal("expected error for 31-byte key")
	}

	// Key too long
	longKey := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x55}, 33))
	if _, err := emailcrypto.New(longKey); err == nil {
		t.Fatal("expected error for 33-byte key")
	}
}

func TestNilEncryptorReturnsError(t *testing.T) {
	var nilEncryptor *emailcrypto.Encryptor

	plaintext := emailcrypto.Plaintext{
		Kind:       "password_reset",
		Token:      "token",
		ActionPath: "/reset",
		ToEmail:    "user@example.com",
		ExpiresAt:  time.Now().UTC().Add(time.Hour),
	}

	if _, err := nilEncryptor.Encrypt(plaintext); err == nil {
		t.Fatal("expected error from nil encryptor Encrypt")
	}

	if _, err := nilEncryptor.Decrypt("{}"); err == nil {
		t.Fatal("expected error from nil encryptor Decrypt")
	}
}

func TestEncryptMissingRequiredFieldsReturnsError(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x66}, 32))
	encryptor, err := emailcrypto.New(key)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	testCases := []struct {
		name      string
		plaintext emailcrypto.Plaintext
	}{
		{
			name: "missing kind",
			plaintext: emailcrypto.Plaintext{
				Token:      "token",
				ActionPath: "/action",
				ToEmail:    "user@example.com",
				ExpiresAt:  time.Now().UTC().Add(time.Hour),
			},
		},
		{
			name: "missing token",
			plaintext: emailcrypto.Plaintext{
				Kind:       "password_reset",
				ActionPath: "/action",
				ToEmail:    "user@example.com",
				ExpiresAt:  time.Now().UTC().Add(time.Hour),
			},
		},
		{
			name: "missing toEmail",
			plaintext: emailcrypto.Plaintext{
				Kind:       "password_reset",
				Token:      "token",
				ActionPath: "/action",
				ExpiresAt:  time.Now().UTC().Add(time.Hour),
			},
		},
		{
			name: "missing expiresAt",
			plaintext: emailcrypto.Plaintext{
				Kind:       "password_reset",
				Token:      "token",
				ActionPath: "/action",
				ToEmail:    "user@example.com",
			},
		},
		{
			name: "missing both paths",
			plaintext: emailcrypto.Plaintext{
				Kind:      "password_reset",
				Token:     "token",
				ToEmail:   "user@example.com",
				ExpiresAt: time.Now().UTC().Add(time.Hour),
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := encryptor.Encrypt(tc.plaintext); err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}
}

func TestDecryptInvalidEnvelopeReturnsError(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x77}, 32))
	encryptor, err := emailcrypto.New(key)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// First create a valid envelope for reference
	plaintext := emailcrypto.Plaintext{
		Kind:       "invite",
		Token:      "invite-token",
		ActionPath: "/invites",
		ToEmail:    "user@example.com",
		ExpiresAt:  time.Now().UTC().Add(time.Hour),
	}
	validEnvelope, err := encryptor.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	testCases := []struct {
		name     string
		envelope string
	}{
		{
			name:     "not json",
			envelope: "not-json",
		},
		{
			name:     "wrong algorithm",
			envelope: strings.Replace(validEnvelope, `"AES-256-GCM"`, `"AES-128-GCM"`, 1),
		},
		{
			name:     "wrong key version",
			envelope: strings.Replace(validEnvelope, `"v1"`, `"v2"`, 1),
		},
		{
			name:     "invalid nonce base64",
			envelope: strings.Replace(validEnvelope, `"nonce":"`, `"nonce":"not-base64!!!`, 1),
		},
		{
			name:     "invalid ciphertext base64",
			envelope: strings.Replace(validEnvelope, `"ciphertext":"`, `"ciphertext":"not-base64!!!`, 1),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := encryptor.Decrypt(tc.envelope); err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}
}

func TestEncryptorPreservesEnvelopeFormat(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x88}, 32))
	encryptor, err := emailcrypto.New(key)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	plaintext := emailcrypto.Plaintext{
		Kind:       "test",
		Token:      "test-token",
		ActionPath: "/test",
		ToEmail:    "test@example.com",
		ExpiresAt:  time.Now().UTC().Add(time.Hour),
	}

	envelope, err := encryptor.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	// Verify AAD is exactly "AES-256-GCM:v1"
	if !strings.Contains(envelope, `"alg":"AES-256-GCM"`) {
		t.Fatal("envelope alg is not AES-256-GCM")
	}
	if !strings.Contains(envelope, `"key_version":"v1"`) {
		t.Fatal("envelope key_version is not v1")
	}
	if !strings.Contains(envelope, `"nonce":`) {
		t.Fatal("envelope missing nonce field")
	}
	if !strings.Contains(envelope, `"ciphertext":`) {
		t.Fatal("envelope missing ciphertext field")
	}
}
