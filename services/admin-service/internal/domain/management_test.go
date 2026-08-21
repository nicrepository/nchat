package domain_test

import (
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
)

func TestClampPageSize(t *testing.T) {
	cases := []struct {
		name     string
		limit    int
		expected int
	}{
		{"unspecified takes the default", 0, domain.DefaultPageSize},
		{"negative takes the default", -10, domain.DefaultPageSize},
		{"a modest page is honoured", 10, 10},
		{"the ceiling is honoured", domain.MaxPageSize, domain.MaxPageSize},
		// Capping rather than refusing keeps the bound from being a probe: a
		// caller cannot tell where it is by which values error.
		{"beyond the ceiling is capped, not refused", 10_000, domain.MaxPageSize},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.ClampPageSize(tc.limit); got != tc.expected {
				t.Fatalf("ClampPageSize(%d) = %d, want %d", tc.limit, got, tc.expected)
			}
		})
	}
}

func TestValidUserStatusTransition(t *testing.T) {
	cases := []struct {
		from, to string
		want     bool
	}{
		{"active", "suspended", true},
		{"suspended", "active", true},
		// A "change" onto the current status is refused rather than treated as
		// a no-op success: it would write an audit row claiming something
		// happened.
		{"active", "active", false},
		{"suspended", "suspended", false},
		// Statuses other flows own are not a switch an operator flips.
		{"invited", "active", false},
		{"active", "deleted", false},
		{"locked", "active", false},
		{"active", "locked", false},
		{"", "active", false},
	}
	for _, tc := range cases {
		if got := domain.ValidUserStatusTransition(tc.from, tc.to); got != tc.want {
			t.Fatalf("ValidUserStatusTransition(%q, %q) = %v, want %v", tc.from, tc.to, got, tc.want)
		}
	}
}

func TestValidChannelStatusTransition(t *testing.T) {
	cases := []struct {
		from, to string
		want     bool
	}{
		{"active", "archived", true},
		{"archived", "active", true},
		{"active", "active", false},
		{"archived", "archived", false},
		{"active", "deleted", false},
	}
	for _, tc := range cases {
		if got := domain.ValidChannelStatusTransition(tc.from, tc.to); got != tc.want {
			t.Fatalf("ValidChannelStatusTransition(%q, %q) = %v, want %v", tc.from, tc.to, got, tc.want)
		}
	}
}

// A cursor must survive the round trip exactly, because the next page's
// predicate is a comparison against these two values.
func TestCursor_RoundTrips(t *testing.T) {
	original := domain.Cursor{
		At: time.Date(2026, 8, 20, 10, 30, 0, 123456789, time.UTC),
		ID: "11111111-1111-1111-1111-111111111111",
	}
	decoded, err := domain.DecodeCursor(original.Encode())
	if err != nil {
		t.Fatalf("DecodeCursor: %v", err)
	}
	if !decoded.At.Equal(original.At) || decoded.ID != original.ID {
		t.Fatalf("round trip changed the cursor: %+v -> %+v", original, decoded)
	}
}

func TestCursor_ZeroIsTheFirstPage(t *testing.T) {
	if !(domain.Cursor{}).IsZero() {
		t.Fatal("the zero cursor must be the first page")
	}
	if token := (domain.Cursor{}).Encode(); token != "" {
		t.Fatalf("the first page has no token, got %q", token)
	}
	decoded, err := domain.DecodeCursor("")
	if err != nil || !decoded.IsZero() {
		t.Fatalf("an absent cursor is the first page, got %+v (%v)", decoded, err)
	}
}

// A malformed cursor is refused rather than silently restarting the listing:
// a client that keeps being handed page one loops forever without ever failing.
func TestDecodeCursor_RefusesMalformed(t *testing.T) {
	cases := map[string]string{
		"not base64":               "!!!not-base64!!!",
		"no separator":             encode("2026-08-20T10:00:00Z"),
		"unparseable timestamp":    encode("yesterday|11111111-1111-1111-1111-111111111111"),
		"identifier is not a uuid": encode("2026-08-20T10:00:00Z|'; DROP TABLE auth.users; --"),
		"empty identifier":         encode("2026-08-20T10:00:00Z|"),
	}
	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := domain.DecodeCursor(token); !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("expected ErrInvalidInput, got %v", err)
			}
		})
	}
}

func TestValidUUID(t *testing.T) {
	if !domain.ValidUUID("11111111-1111-1111-1111-111111111111") {
		t.Fatal("a well-formed uuid must be accepted")
	}
	for _, value := range []string{"", "not-a-uuid", "1 OR 1=1", "11111111-1111-1111-1111"} {
		if domain.ValidUUID(value) {
			t.Fatalf("%q must not be accepted as a uuid", value)
		}
	}
}

// Page.HasMore is derived from the cursor so the two can never disagree.
func TestPage_HasMoreFollowsTheCursor(t *testing.T) {
	if (domain.Page[int]{}).HasMore() {
		t.Fatal("a page with no cursor is the last page")
	}
	if !(domain.Page[int]{NextCursor: "abc"}).HasMore() {
		t.Fatal("a page with a cursor has more")
	}
}

// encode builds a cursor token by hand, so a spec can present a token that is
// well-formed base64 and still not a cursor.
func encode(raw string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}
