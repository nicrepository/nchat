package service

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

const (
	oidcPKCEInfo     = "nchat-oidc-pkce-v1"
	oidcExchangeInfo = "nchat-oidc-exchange-v1"
)

type oidcCrypto struct {
	pkceKey     []byte
	exchangeKey []byte
}

func newOIDCCrypto(secret []byte) (*oidcCrypto, error) {
	pkceKey, err := deriveOIDCKey(secret, oidcPKCEInfo)
	if err != nil {
		return nil, err
	}
	exchangeKey, err := deriveOIDCKey(secret, oidcExchangeInfo)
	if err != nil {
		return nil, err
	}
	return &oidcCrypto{pkceKey: pkceKey, exchangeKey: exchangeKey}, nil
}

func deriveOIDCKey(secret []byte, info string) ([]byte, error) {
	reader := hkdf.New(sha256.New, secret, nil, []byte(info))
	key := make([]byte, 32)
	if _, err := io.ReadFull(reader, key); err != nil {
		return nil, fmt.Errorf("derive oidc key: %w", err)
	}
	return key, nil
}

func (c *oidcCrypto) encryptPKCEVerifier(provider, id, verifier string) (string, error) {
	return encryptOIDCValue(c.pkceKey, []byte("oidc-pkce:"+provider+":"+id), verifier)
}

func (c *oidcCrypto) decryptPKCEVerifier(provider, id, encrypted string) (string, error) {
	return decryptOIDCValue(c.pkceKey, []byte("oidc-pkce:"+provider+":"+id), encrypted)
}

func (c *oidcCrypto) encryptExchangeValue(provider, id, value string) (string, error) {
	return encryptOIDCValue(c.exchangeKey, []byte("oidc-exchange:"+provider+":"+id), value)
}

func (c *oidcCrypto) decryptExchangeValue(provider, id, encrypted string) (string, error) {
	return decryptOIDCValue(c.exchangeKey, []byte("oidc-exchange:"+provider+":"+id), encrypted)
}

func encryptOIDCValue(key, aad []byte, value string) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("create oidc cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create oidc gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generate oidc nonce: %w", err)
	}
	ciphertext := gcm.Seal(nil, nonce, []byte(value), aad)
	envelope := make([]byte, 0, len(nonce)+len(ciphertext))
	envelope = append(envelope, nonce...)
	envelope = append(envelope, ciphertext...)
	return base64.RawURLEncoding.EncodeToString(envelope), nil
}

func decryptOIDCValue(key, aad []byte, encrypted string) (string, error) {
	envelope, err := base64.RawURLEncoding.DecodeString(encrypted)
	if err != nil {
		return "", fmt.Errorf("decode oidc envelope: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("create oidc cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create oidc gcm: %w", err)
	}
	if len(envelope) < gcm.NonceSize() {
		return "", fmt.Errorf("invalid oidc envelope")
	}
	nonce := envelope[:gcm.NonceSize()]
	ciphertext := envelope[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return "", fmt.Errorf("decrypt oidc value: %w", err)
	}
	return string(plaintext), nil
}
