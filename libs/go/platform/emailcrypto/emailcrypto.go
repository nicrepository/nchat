package emailcrypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	envelopeAlg        = "AES-256-GCM"
	envelopeKeyVersion = "v1"
	keyBytes           = 32
)

var errUnavailable = errors.New("encryptor is nil")

// Plaintext is the data structure that is encrypted before storage.
type Plaintext struct {
	Kind       string    `json:"kind"`
	Token      string    `json:"token"`
	LinkPath   string    `json:"link_path,omitempty"`
	ActionPath string    `json:"action_path,omitempty"`
	ToEmail    string    `json:"to_email"`
	ExpiresAt  time.Time `json:"expires_at"`
}

type envelope struct {
	Alg        string `json:"alg"`
	KeyVersion string `json:"key_version"`
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
}

// Encryptor encrypts token handoff payloads for the future e-mail worker.
type Encryptor struct {
	aead cipher.AEAD
}

func New(base64Key string) (*Encryptor, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(base64Key))
	if err != nil {
		return nil, fmt.Errorf("encryption key must be base64")
	}
	if len(key) != keyBytes {
		return nil, fmt.Errorf("encryption key must decode to %d bytes", keyBytes)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create gcm: %w", err)
	}
	return &Encryptor{aead: aead}, nil
}

func (e *Encryptor) Encrypt(payload Plaintext) (string, error) {
	if e == nil {
		return "", errUnavailable
	}
	if payload.Kind == "" || payload.Token == "" || payload.ToEmail == "" || payload.ExpiresAt.IsZero() {
		return "", fmt.Errorf("plaintext missing required field")
	}
	if payload.LinkPath == "" && payload.ActionPath == "" {
		return "", fmt.Errorf("plaintext missing action path")
	}

	plain, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal plaintext: %w", err)
	}
	nonce := make([]byte, e.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}
	ciphertext := e.aead.Seal(nil, nonce, plain, aad())
	envelopeData, err := json.Marshal(envelope{
		Alg:        envelopeAlg,
		KeyVersion: envelopeKeyVersion,
		Nonce:      base64.StdEncoding.EncodeToString(nonce),
		Ciphertext: base64.StdEncoding.EncodeToString(ciphertext),
	})
	if err != nil {
		return "", fmt.Errorf("marshal envelope: %w", err)
	}
	return string(envelopeData), nil
}

func (e *Encryptor) Decrypt(envelopeJSON string) (Plaintext, error) {
	if e == nil {
		return Plaintext{}, errUnavailable
	}
	var env envelope
	if err := json.Unmarshal([]byte(envelopeJSON), &env); err != nil {
		return Plaintext{}, fmt.Errorf("decode envelope")
	}
	if env.Alg != envelopeAlg || env.KeyVersion != envelopeKeyVersion {
		return Plaintext{}, fmt.Errorf("unsupported envelope")
	}
	nonce, err := base64.StdEncoding.DecodeString(env.Nonce)
	if err != nil {
		return Plaintext{}, fmt.Errorf("decode nonce")
	}
	ciphertext, err := base64.StdEncoding.DecodeString(env.Ciphertext)
	if err != nil {
		return Plaintext{}, fmt.Errorf("decode ciphertext")
	}
	plain, err := e.aead.Open(nil, nonce, ciphertext, aad())
	if err != nil {
		return Plaintext{}, fmt.Errorf("decrypt payload")
	}
	var payload Plaintext
	if err := json.Unmarshal(plain, &payload); err != nil {
		return Plaintext{}, fmt.Errorf("decode payload")
	}
	return payload, nil
}

func aad() []byte {
	return []byte(envelopeAlg + ":" + envelopeKeyVersion)
}
