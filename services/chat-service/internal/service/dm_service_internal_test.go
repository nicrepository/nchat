package service

import "testing"

const (
	testUser1 = "aabbccdd-1111-2222-3333-000000000001"
	testUser2 = "aabbccdd-1111-2222-3333-000000000002"
)

func TestCanonicalDirectPairKey_OrderIndependentAndLengthDelimited(t *testing.T) {
	first := canonicalDirectPairKey(testUser1, testUser2)
	second := canonicalDirectPairKey(testUser2, testUser1)
	if first != second {
		t.Fatalf("pair key must be order-independent, first=%q second=%q", first, second)
	}

	other := "aabbccdd-1111-2222-3333-000000000099"
	if canonicalDirectPairKey(testUser1, testUser2) == canonicalDirectPairKey(testUser1, other) {
		t.Fatal("pair key must distinguish different UUID pairs")
	}
}
