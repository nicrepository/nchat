package domain

import (
	"encoding/base64"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestMessageCursorRoundTripAndBinding(t *testing.T) {
	created := time.Date(2026, 8, 18, 12, 0, 0, 123, time.UTC)
	raw, err := EncodeMessageCursor("mensagem segura", 0.75, created, "11111111-1111-4111-8111-111111111111", created)
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

func TestGroupCursorIsItsOwnType(t *testing.T) {
	raw, err := EncodeNameCursor(CursorGroups, "projeto", "projeto nchat", "33333333-3333-4333-8333-333333333333")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := DecodeNameCursor(raw, CursorGroups, "projeto"); err != nil || got.Name != "projeto nchat" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	for _, kind := range []string{CursorUsers, CursorChannels} {
		if _, err := DecodeNameCursor(raw, kind, "projeto"); err == nil {
			t.Fatalf("group cursor accepted as %s", kind)
		}
	}
	if _, err := EncodeNameCursor(CursorFiles, "q", "n", "33333333-3333-4333-8333-333333333333"); err == nil {
		t.Fatal("files is not a name cursor")
	}
}

func TestFileCursorRoundTripAndBinding(t *testing.T) {
	created := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	id := "44444444-4444-4444-8444-444444444444"
	raw, err := EncodeFileCursor("backup", created, id)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeFileCursor(raw, "backup")
	if err != nil || got.ID != id || !got.CreatedAt.Equal(created) {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	if _, err := DecodeFileCursor(raw, "outro"); err == nil {
		t.Fatal("file cursor reused with another query must be rejected")
	}
	if _, err := DecodeNameCursor(raw, CursorGroups, "backup"); err == nil {
		t.Fatal("file cursor accepted as a name cursor")
	}
	msg, _ := EncodeMessageCursor("backup", 1, created, id, created)
	if _, err := DecodeFileCursor(msg, "backup"); err == nil {
		t.Fatal("message cursor accepted as a file cursor")
	}
	if _, err := EncodeFileCursor("backup", time.Time{}, id); err == nil {
		t.Fatal("zero time must not encode")
	}
	if _, err := EncodeFileCursor("backup", created, "not-a-uuid"); err == nil {
		t.Fatal("invalid id must not encode")
	}
}

func TestMessageCursorRequiresRankingClock(t *testing.T) {
	created := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	if _, err := EncodeMessageCursor("q", 1, created, "11111111-1111-4111-8111-111111111111", time.Time{}); err == nil {
		t.Fatal("a cursor without its ranking clock must not encode")
	}
	// A pre-#900 cursor carried no ranking clock and cannot be resumed.
	legacy, _ := encodeCursor(struct {
		V  int       `json:"v"`
		T  string    `json:"t"`
		Q  string    `json:"q"`
		S  float64   `json:"s"`
		C  time.Time `json:"c"`
		ID string    `json:"id"`
	}{CursorVersion, CursorMessages, queryHash("q"), 1, created, "11111111-1111-4111-8111-111111111111"})
	if _, err := DecodeMessageCursor(legacy, "q"); err == nil {
		t.Fatal("legacy message cursor without ranking clock accepted")
	}
}

// The legacy cursor is byte-compatible with the one search-service issued
// before #900, so an old and a new service honour each other's cursors while
// both are live behind the same route.
func TestLegacyMessageCursorKeepsThePreviousWireFormat(t *testing.T) {
	created := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	id := "11111111-1111-4111-8111-111111111111"
	raw, err := EncodeLegacyMessageCursor("backup", 0.5, created, id)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := base64.RawURLEncoding.DecodeString(raw)
	var fields map[string]any
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if strings.Join(keys, ",") != "c,id,q,s,t,v" || fields["t"] != "messages" {
		t.Fatalf("legacy cursor wire format changed: %s", b)
	}
	// A cursor written by the previous release, field for field.
	previous, _ := encodeCursor(struct {
		V  int       `json:"v"`
		T  string    `json:"t"`
		Q  string    `json:"q"`
		S  float64   `json:"s"`
		C  time.Time `json:"c"`
		ID string    `json:"id"`
	}{1, "messages", queryHash("backup"), 0.5, created, id})
	if got, err := DecodeLegacyMessageCursor(previous, "backup"); err != nil || got.ID != id {
		t.Fatalf("previous-release cursor rejected: %v", err)
	}
	if _, err := DecodeMessageCursor(previous, "backup"); err == nil {
		t.Fatal("legacy cursor accepted as a v2 cursor")
	}
	v2, _ := EncodeMessageCursor("backup", 0.5, created, id, created)
	if _, err := DecodeLegacyMessageCursor(v2, "backup"); err == nil {
		t.Fatal("v2 cursor accepted as a legacy cursor")
	}
	if _, err := EncodeLegacyMessageCursor("backup", 0.5, created, "bad"); err == nil {
		t.Fatal("invalid id must not encode")
	}
}
