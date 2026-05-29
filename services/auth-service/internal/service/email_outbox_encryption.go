package service

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

const (
	emailOutboxEnvelopeAlg        = "AES-256-GCM"
	emailOutboxEnvelopeKeyVersion = "v1"
	emailOutboxKeyBytes           = 32
)

// EmailOutboxPlaintext is encrypted before it is stored in auth.email_outbox.payload.
type EmailOutboxPlaintext struct {
	Kind       string    `json:"kind"`
	Token      string    `json:"token"`
	LinkPath   string    `json:"link_path,omitempty"`
	ActionPath string    `json:"action_path,omitempty"`
	ToEmail    string    `json:"to_email"`
	ExpiresAt  time.Time `json:"expires_at"`
}

type emailOutboxEnvelope struct {
	Alg        string `json:"alg"`
	KeyVersion string `json:"key_version"`
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
}

// EmailOutboxEncryptor encrypts token handoff payloads for the future e-mail worker.
type EmailOutboxEncryptor struct {
	aead cipher.AEAD
}

func NewEmailOutboxEncryptor(base64Key string) (*EmailOutboxEncryptor, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(base64Key))
	if err != nil {
		return nil, fmt.Errorf("email outbox encryption key must be base64")
	}
	if len(key) != emailOutboxKeyBytes {
		return nil, fmt.Errorf("email outbox encryption key must decode to %d bytes", emailOutboxKeyBytes)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create email outbox cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create email outbox gcm: %w", err)
	}
	return &EmailOutboxEncryptor{aead: aead}, nil
}

func (e *EmailOutboxEncryptor) Encrypt(payload EmailOutboxPlaintext) (string, error) {
	if e == nil {
		return "", domain.ErrEmailOutboxUnavailable
	}
	if payload.Kind == "" || payload.Token == "" || payload.ToEmail == "" || payload.ExpiresAt.IsZero() {
		return "", fmt.Errorf("email outbox payload missing required field")
	}
	if payload.LinkPath == "" && payload.ActionPath == "" {
		return "", fmt.Errorf("email outbox payload missing action path")
	}

	plain, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal email outbox payload: %w", err)
	}
	nonce := make([]byte, e.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate email outbox nonce: %w", err)
	}
	ciphertext := e.aead.Seal(nil, nonce, plain, emailOutboxAAD())
	envelope, err := json.Marshal(emailOutboxEnvelope{
		Alg:        emailOutboxEnvelopeAlg,
		KeyVersion: emailOutboxEnvelopeKeyVersion,
		Nonce:      base64.StdEncoding.EncodeToString(nonce),
		Ciphertext: base64.StdEncoding.EncodeToString(ciphertext),
	})
	if err != nil {
		return "", fmt.Errorf("marshal email outbox envelope: %w", err)
	}
	return string(envelope), nil
}

func (e *EmailOutboxEncryptor) Decrypt(envelopeJSON string) (EmailOutboxPlaintext, error) {
	if e == nil {
		return EmailOutboxPlaintext{}, domain.ErrEmailOutboxUnavailable
	}
	var envelope emailOutboxEnvelope
	if err := json.Unmarshal([]byte(envelopeJSON), &envelope); err != nil {
		return EmailOutboxPlaintext{}, fmt.Errorf("decode email outbox envelope")
	}
	if envelope.Alg != emailOutboxEnvelopeAlg || envelope.KeyVersion != emailOutboxEnvelopeKeyVersion {
		return EmailOutboxPlaintext{}, fmt.Errorf("unsupported email outbox envelope")
	}
	nonce, err := base64.StdEncoding.DecodeString(envelope.Nonce)
	if err != nil {
		return EmailOutboxPlaintext{}, fmt.Errorf("decode email outbox nonce")
	}
	ciphertext, err := base64.StdEncoding.DecodeString(envelope.Ciphertext)
	if err != nil {
		return EmailOutboxPlaintext{}, fmt.Errorf("decode email outbox ciphertext")
	}
	plain, err := e.aead.Open(nil, nonce, ciphertext, emailOutboxAAD())
	if err != nil {
		return EmailOutboxPlaintext{}, fmt.Errorf("decrypt email outbox payload")
	}
	var payload EmailOutboxPlaintext
	if err := json.Unmarshal(plain, &payload); err != nil {
		return EmailOutboxPlaintext{}, fmt.Errorf("decode email outbox payload")
	}
	return payload, nil
}

func emailOutboxAAD() []byte {
	return []byte(emailOutboxEnvelopeAlg + ":" + emailOutboxEnvelopeKeyVersion)
}
