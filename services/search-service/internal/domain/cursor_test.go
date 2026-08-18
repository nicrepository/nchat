package domain

import (
	"strings"
	"testing"
	"time"
)

func TestMessageCursorRoundTripAndBinding(t *testing.T) {
	created := time.Date(2026, 8, 18, 12, 0, 0, 123, time.UTC)
	raw, err := EncodeMessageCursor("mensagem segura", 0.75, created, "11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeMessageCursor(raw, "mensagem segura")
	if err != nil {
		t.Fatal(err)
	}
	if got.Score != 0.75 || !got.CreatedAt.Equal(created) || got.ID != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("unexpected cursor: %+v", got)
	}
	if _, err := DecodeMessageCursor(raw, "outra consulta"); err == nil {
		t.Fatal("cursor reused with another query must be rejected")
	}
}

func TestNameCursorRejectsWrongTypeUnknownFieldsAndOversize(t *testing.T) {
	raw, err := EncodeNameCursor(CursorUsers, "ana", "ana", "22222222-2222-4222-8222-222222222222")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeNameCursor(raw, CursorChannels, "ana"); err == nil {
		t.Fatal("cursor reused with another result type must be rejected")
	}
	if _, err := DecodeNameCursor("eyJ2IjoxLCJ0IjoidXNlcnMiLCJxIjoieCIsIm4iOiJhIiwiaWQiOiIyMjIyMjIyMi0yMjIyLTQyMjItODIyMi0yMjIyMjIyMjIyMjIiLCJ4Ijp0cnVlfQ", CursorUsers, "ana"); err == nil {
		t.Fatal("unknown cursor field must be rejected")
	}
	if _, err := DecodeNameCursor(strings.Repeat("a", MaxCursorEncodedBytes+1), CursorUsers, "ana"); err == nil {
		t.Fatal("oversized cursor must be rejected before decoding")
	}
}
