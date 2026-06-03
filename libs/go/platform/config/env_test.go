package config

import "testing"

func TestGetStringReturnsExistingValue(t *testing.T) {
	t.Setenv("NCHAT_TEST_STRING", "configured")

	if got := GetString("NCHAT_TEST_STRING", "fallback"); got != "configured" {
		t.Fatalf("expected configured, got %q", got)
	}
}

func TestGetStringReturnsFallbackWhenMissing(t *testing.T) {
	if got := GetString("NCHAT_TEST_MISSING", "fallback"); got != "fallback" {
		t.Fatalf("expected fallback, got %q", got)
	}
}

func TestGetIntReturnsExistingValue(t *testing.T) {
	t.Setenv("NCHAT_TEST_INT", "42")

	if got := GetInt("NCHAT_TEST_INT", 7); got != 42 {
		t.Fatalf("expected 42, got %d", got)
	}
}

func TestGetIntReturnsFallbackWhenMissing(t *testing.T) {
	if got := GetInt("NCHAT_TEST_INT_MISSING", 7); got != 7 {
		t.Fatalf("expected fallback 7, got %d", got)
	}
}

func TestGetIntReturnsFallbackWhenInvalid(t *testing.T) {
	t.Setenv("NCHAT_TEST_INT_INVALID", "not-an-int")

	if got := GetInt("NCHAT_TEST_INT_INVALID", 7); got != 7 {
		t.Fatalf("expected fallback 7, got %d", got)
	}
}

func TestGetBoolReturnsExistingValue(t *testing.T) {
	t.Setenv("NCHAT_TEST_BOOL", "true")

	if got := GetBool("NCHAT_TEST_BOOL", false); got != true {
		t.Fatalf("expected true, got %t", got)
	}
}

func TestGetBoolReturnsFallbackWhenMissing(t *testing.T) {
	if got := GetBool("NCHAT_TEST_BOOL_MISSING", true); got != true {
		t.Fatalf("expected fallback true, got %t", got)
	}
}

func TestGetBoolReturnsFallbackWhenInvalid(t *testing.T) {
	t.Setenv("NCHAT_TEST_BOOL_INVALID", "not-a-bool")

	if got := GetBool("NCHAT_TEST_BOOL_INVALID", true); got != true {
		t.Fatalf("expected fallback true, got %t", got)
	}
}
