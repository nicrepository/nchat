package domain

import (
	"encoding/base64"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Cursor is the position of a keyset-paginated listing.
//
// Every administrative listing is ordered newest-first by (created_at, id) —
// or (updated_at, id) for conversations — and resumes with a row-value
// comparison against exactly that pair. The identifier is the tiebreak and not
// decoration: timestamps are not unique, and a keyset without a unique trailing
// column silently repeats or skips rows between pages.
//
// The zero value is the first page.
type Cursor struct {
	At time.Time
	ID string
}

// IsZero reports whether this is the first page.
func (c Cursor) IsZero() bool { return c.ID == "" }

// Encode renders the cursor as one opaque token.
//
// It is opaque to the client but not secret and not a capability: it names a
// position in an ordering the caller is already authorized to read, and the
// authorization is re-evaluated on the request that carries it. Encoding is
// base64url of "<RFC3339Nano>|<uuid>" — deliberately not signed, because a
// forged cursor can only ask for a different page of the same authorized
// listing.
func (c Cursor) Encode() string {
	if c.IsZero() {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte(c.At.UTC().Format(time.RFC3339Nano) + "|" + c.ID))
}

// DecodeCursor parses a cursor token.
//
// An empty token is the first page and is not an error. Anything else must be
// exactly the two well-formed halves: a timestamp and a UUID. A malformed
// cursor is refused rather than treated as "start over", because silently
// restarting a paged read is how a client loops forever over page one.
//
// The UUID check is what keeps this parameter out of the query as anything but
// a bound uuid value.
func DecodeCursor(token string) (Cursor, error) {
	if token == "" {
		return Cursor{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return Cursor{}, ErrInvalidInput
	}
	at, id, found := strings.Cut(string(raw), "|")
	if !found {
		return Cursor{}, ErrInvalidInput
	}
	parsed, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return Cursor{}, ErrInvalidInput
	}
	if _, err := uuid.Parse(id); err != nil {
		return Cursor{}, ErrInvalidInput
	}
	return Cursor{At: parsed.UTC(), ID: id}, nil
}

// ValidUUID reports whether value is a well-formed UUID.
//
// Every identifier that reaches a query goes through it first. The queries bind
// their parameters, so this is not what prevents injection; it is what turns a
// malformed path segment into a 400 instead of a database error, and what keeps
// a caller from using type errors to probe the schema.
func ValidUUID(value string) bool {
	_, err := uuid.Parse(value)
	return err == nil
}
