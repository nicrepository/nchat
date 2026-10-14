package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	CursorVersion         = 1
	MaxCursorEncodedBytes = 1024
	// CursorMessages is the legacy message cursor, byte-compatible with the
	// one search-service issued before #900: an old and a new service accept
	// each other's cursors while both are live.
	CursorMessages = "messages"
	// CursorMessagesV2 pages /v2/messages. A distinct type, because the two
	// endpoints search different sets and rank against different clocks.
	CursorMessagesV2 = "messages.v2"
	CursorUsers      = "users"
	CursorChannels   = "channels"
	CursorGroups     = "groups"
	CursorFiles      = "files"
	CursorLinks      = "links"
	// MaxLinkRank is the weakest link match (see storage.Links).
	MaxLinkRank = 3
)

var ErrInvalidCursor = errors.New("invalid cursor")

// LegacyMessageCursor is the pre-#900 message cursor, unchanged.
type LegacyMessageCursor struct {
	Version   int       `json:"v"`
	Type      string    `json:"t"`
	QueryHash string    `json:"q"`
	Score     float64   `json:"s"`
	CreatedAt time.Time `json:"c"`
	ID        string    `json:"id"`
}

// MessageCursor (V2) carries RankedAt, the reference time the first page was
// ranked at: the score has a recency factor, so a later page ranked against a
// later clock would no longer order rows the way the cursor saw them.
type MessageCursor struct {
	Version   int       `json:"v"`
	Type      string    `json:"t"`
	QueryHash string    `json:"q"`
	Score     float64   `json:"s"`
	CreatedAt time.Time `json:"c"`
	ID        string    `json:"id"`
	RankedAt  time.Time `json:"r"`
}

// TimeCursor pages newest first by (CreatedAt, ID).
type TimeCursor struct {
	Version   int       `json:"v"`
	Type      string    `json:"t"`
	QueryHash string    `json:"q"`
	CreatedAt time.Time `json:"c"`
	ID        string    `json:"id"`
}
type NameCursor struct {
	Version   int    `json:"v"`
	Type      string `json:"t"`
	QueryHash string `json:"q"`
	Name      string `json:"n"`
	ID        string `json:"id"`
}

// LinkCursor pages links by (Rank, CreatedAt desc, MessageID desc, TargetKey
// desc). It names the target by its key, never by its URL.
type LinkCursor struct {
	Version   int       `json:"v"`
	Type      string    `json:"t"`
	QueryHash string    `json:"q"`
	Rank      int       `json:"k"`
	CreatedAt time.Time `json:"c"`
	MessageID string    `json:"id"`
	TargetKey string    `json:"u"`
}

func EncodeLinkCursor(query string, rank int, createdAt time.Time, messageID, targetKey string) (string, error) {
	if !validLinkCursor(rank, createdAt, messageID, targetKey) {
		return "", ErrInvalidCursor
	}
	return encodeCursor(LinkCursor{CursorVersion, CursorLinks, queryHash(query), rank, createdAt.UTC(), messageID, targetKey})
}

func DecodeLinkCursor(raw, query string) (LinkCursor, error) {
	var c LinkCursor
	if err := decodeCursor(raw, &c); err != nil || c.Version != CursorVersion || c.Type != CursorLinks || c.QueryHash != queryHash(query) || !validLinkCursor(c.Rank, c.CreatedAt, c.MessageID, c.TargetKey) {
		return LinkCursor{}, ErrInvalidCursor
	}
	return c, nil
}

func validLinkCursor(rank int, createdAt time.Time, messageID, targetKey string) bool {
	return rank >= 0 && rank <= MaxLinkRank && !createdAt.IsZero() && validID(messageID) && validTargetKey(targetKey)
}

// validTargetKey accepts chat-service's LinkTargetKey shape: 32 lowercase hex.
func validTargetKey(key string) bool {
	if len(key) != 32 {
		return false
	}
	_, err := hex.DecodeString(key)
	return err == nil && strings.ToLower(key) == key
}

func EncodeMessageCursor(query string, score float64, createdAt time.Time, id string, rankedAt time.Time) (string, error) {
	if !validID(id) || math.IsNaN(score) || math.IsInf(score, 0) || createdAt.IsZero() || rankedAt.IsZero() {
		return "", ErrInvalidCursor
	}
	return encodeCursor(MessageCursor{CursorVersion, CursorMessagesV2, queryHash(query), score, createdAt.UTC(), id, rankedAt.UTC()})
}

func DecodeMessageCursor(raw, query string) (MessageCursor, error) {
	var c MessageCursor
	if err := decodeCursor(raw, &c); err != nil || c.Version != CursorVersion || c.Type != CursorMessagesV2 || c.QueryHash != queryHash(query) || !validID(c.ID) || c.CreatedAt.IsZero() || c.RankedAt.IsZero() || math.IsNaN(c.Score) || math.IsInf(c.Score, 0) {
		return MessageCursor{}, ErrInvalidCursor
	}
	return c, nil
}

func EncodeLegacyMessageCursor(query string, score float64, createdAt time.Time, id string) (string, error) {
	if !validID(id) || math.IsNaN(score) || math.IsInf(score, 0) || createdAt.IsZero() {
		return "", ErrInvalidCursor
	}
	return encodeCursor(LegacyMessageCursor{CursorVersion, CursorMessages, queryHash(query), score, createdAt.UTC(), id})
}

func DecodeLegacyMessageCursor(raw, query string) (LegacyMessageCursor, error) {
	var c LegacyMessageCursor
	if err := decodeCursor(raw, &c); err != nil || c.Version != CursorVersion || c.Type != CursorMessages || c.QueryHash != queryHash(query) || !validID(c.ID) || c.CreatedAt.IsZero() || math.IsNaN(c.Score) || math.IsInf(c.Score, 0) {
		return LegacyMessageCursor{}, ErrInvalidCursor
	}
	return c, nil
}

func EncodeNameCursor(kind, query, name, id string) (string, error) {
	if !validNameKind(kind) || name == "" || !validID(id) {
		return "", ErrInvalidCursor
	}
	return encodeCursor(NameCursor{CursorVersion, kind, queryHash(query), name, id})
}

func DecodeNameCursor(raw, kind, query string) (NameCursor, error) {
	var c NameCursor
	if err := decodeCursor(raw, &c); err != nil || c.Version != CursorVersion || c.Type != kind || !validNameKind(c.Type) || c.QueryHash != queryHash(query) || c.Name == "" || !validID(c.ID) {
		return NameCursor{}, ErrInvalidCursor
	}
	return c, nil
}

func EncodeFileCursor(query string, createdAt time.Time, id string) (string, error) {
	if !validID(id) || createdAt.IsZero() {
		return "", ErrInvalidCursor
	}
	return encodeCursor(TimeCursor{CursorVersion, CursorFiles, queryHash(query), createdAt.UTC(), id})
}

func DecodeFileCursor(raw, query string) (TimeCursor, error) {
	var c TimeCursor
	if err := decodeCursor(raw, &c); err != nil || c.Version != CursorVersion || c.Type != CursorFiles || c.QueryHash != queryHash(query) || !validID(c.ID) || c.CreatedAt.IsZero() {
		return TimeCursor{}, ErrInvalidCursor
	}
	return c, nil
}

func encodeCursor(value any) (string, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func decodeCursor(raw string, dst any) error {
	if raw == "" || len(raw) > MaxCursorEncodedBytes {
		return ErrInvalidCursor
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(raw)
	if err != nil {
		return ErrInvalidCursor
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return ErrInvalidCursor
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return ErrInvalidCursor
	}
	return nil
}

func queryHash(query string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(query)))
	return hex.EncodeToString(sum[:])
}
func validID(id string) bool {
	parsed, err := uuid.Parse(id)
	return err == nil && parsed.String() == strings.ToLower(id)
}
func validNameKind(kind string) bool {
	return kind == CursorUsers || kind == CursorChannels || kind == CursorGroups
}
