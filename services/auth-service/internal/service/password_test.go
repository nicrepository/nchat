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

func TestVerifyPassword_AcceptsMatchingArgon2idPHCHash(t *testing.T) {
	hash, err := service.HashPassword("ChangeMe@123")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	ok, err := service.VerifyPassword("ChangeMe@123", hash)
	if err != nil {
		t.Fatalf("VerifyPassword: %v", err)
	}
	if !ok {
		t.Fatal("expected password to verify")
	}
}

func TestVerifyPassword_RejectsWrongPassword(t *testing.T) {
	hash, err := service.HashPassword("ChangeMe@123")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	ok, err := service.VerifyPassword("wrong-password", hash)
	if err != nil {
		t.Fatalf("VerifyPassword: %v", err)
	}
	if ok {
		t.Fatal("expected wrong password to fail")
	}
}

func TestVerifyPassword_RejectsMalformedHash(t *testing.T) {
	ok, err := service.VerifyPassword("ChangeMe@123", "not-a-phc-hash")
	if err == nil {
		t.Fatal("expected malformed hash error")
	}
	if ok {
		t.Fatal("malformed hash must not verify")
	}
}

func TestRunDummyPasswordVerification_DoesNotReturnPasswordMaterial(t *testing.T) {
	service.RunDummyPasswordVerification("unknown-user-password")
}

func TestVerifyPassword_RejectsWrongAlgorithm(t *testing.T) {
	ok, err := service.VerifyPassword("ChangeMe@123", "$bcrypt$v=19$m=65536,t=3,p=4$c3RhdGljLWR1bW15LXNhbHQ$Vzkb2CNBv8Y7vIgM2XgaXW7J5z")
	if err == nil {
		t.Fatal("expected error for unsupported algorithm")
	}
	if ok {
		t.Fatal("wrong algorithm must not verify")
	}
}

func TestVerifyPassword_RejectsWrongVersion(t *testing.T) {
	ok, err := service.VerifyPassword("ChangeMe@123", "$argon2id$v=18$m=65536,t=3,p=4$c3RhdGljLWR1bW15LXNhbHQ$Vzkb2CNBv8Y7vIgM2XgaXW7J5z")
	if err == nil {
		t.Fatal("expected error for unsupported version")
	}
	if ok {
		t.Fatal("wrong version must not verify")
	}
}

func TestVerifyPassword_RejectsBadParamFormat(t *testing.T) {
	ok, err := service.VerifyPassword("ChangeMe@123", "$argon2id$v=19$not-valid-params$c3RhdGljLWR1bW15LXNhbHQ$Vzkb2CNBv8Y7vIgM2XgaXW7J5z")
	if err == nil {
		t.Fatal("expected error for bad param format")
	}
	if ok {
		t.Fatal("bad param format must not verify")
	}
}

func TestVerifyPassword_RejectsBadSaltBase64(t *testing.T) {
	ok, err := service.VerifyPassword("ChangeMe@123", "$argon2id$v=19$m=65536,t=3,p=4$!!!invalid!!!$Vzkb2CNBv8Y7vIgM2XgaXW7J5z")
	if err == nil {
		t.Fatal("expected error for bad salt base64")
	}
	if ok {
		t.Fatal("bad salt must not verify")
	}
}

func TestVerifyPassword_RejectsBadHashBase64(t *testing.T) {
	ok, err := service.VerifyPassword("ChangeMe@123", "$argon2id$v=19$m=65536,t=3,p=4$c3RhdGljLWR1bW15LXNhbHQ$!!!invalid!!!")
	if err == nil {
		t.Fatal("expected error for bad hash base64")
	}
	if ok {
		t.Fatal("bad hash must not verify")
	}
}

func TestVerifyPassword_NonStandardKeyLength(t *testing.T) {
	// Craft a PHC hash with 16-byte key (non-standard length) to exercise derivedKeyLen fallback.
	// $argon2id$v=19$m=65536,t=3,p=4$<16-byte-salt-b64>$<16-byte-hash-b64>
	// salt: "short-test-salt!" (16 bytes), hash is intentionally wrong length
	// We just verify the function handles it without panic; it will return mismatch.
	salt16 := "c2hvcnQtdGVzdC1z" // "short-test-s" in base64 (12 bytes, but close enough)
	// Use a valid-looking hash with only 16 decoded bytes (22 base64 chars = 16.5 bytes — use 20 chars = 15 bytes)
	// Actually, craft it so len(decoded) != 32 to hit the fallback branch.
	// A 12-char RawStdEncoding base64 string decodes to 9 bytes.
	fakeHash := "$argon2id$v=19$m=65536,t=3,p=4$" + salt16 + "$dGVzdGhhc2g" // "testhash" = 8 bytes
	ok, err := service.VerifyPassword("any-password", fakeHash)
	// Must not panic; result is false because the re-derived hash won't match.
	if err != nil {
		t.Fatalf("unexpected error for non-standard key length: %v", err)
	}
	if ok {
		t.Fatal("crafted hash must not verify")
	}
}
