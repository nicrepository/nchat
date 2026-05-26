package service_test

import (
	"strings"
	"testing"

	"github.com/nicrepository/nchat/services/auth-service/internal/service"
)

func TestHashPassword_NotEqualToPlaintext(t *testing.T) {
	hash, err := service.HashPassword("my-secret-password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if hash == "my-secret-password" {
		t.Fatal("hash must not equal plaintext")
	}
}

func TestHashPassword_PHCFormat(t *testing.T) {
	hash, err := service.HashPassword("P@ssword123")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Fatalf("expected PHC argon2id prefix, got %q", hash[:minInt(len(hash), 20)])
	}
}

func TestHashPassword_UniquePerCall(t *testing.T) {
	h1, err := service.HashPassword("same-password")
	if err != nil {
		t.Fatalf("first hash: %v", err)
	}
	h2, err := service.HashPassword("same-password")
	if err != nil {
		t.Fatalf("second hash: %v", err)
	}
	if h1 == h2 {
		t.Fatal("two hashes of the same password must differ (different salts)")
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
