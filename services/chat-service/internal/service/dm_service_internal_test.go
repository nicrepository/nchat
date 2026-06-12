package service

import "testing"

func TestCanonicalDirectPairKey_OrderIndependentAndLengthDelimited(t *testing.T) {
	first := canonicalDirectPairKey("user-1", "user-2")
	second := canonicalDirectPairKey("user-2", "user-1")
	if first != second {
		t.Fatalf("pair key must be order-independent, first=%q second=%q", first, second)
	}

	if canonicalDirectPairKey("a", "bc") == canonicalDirectPairKey("ab", "c") {
		t.Fatal("pair key must be length-delimited to avoid normal ID concatenation collisions")
	}
}
