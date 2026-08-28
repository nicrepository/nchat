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
	CursorMessages        = "messages"
	CursorUsers           = "users"
	CursorChannels        = "channels"
)

var ErrInvalidCursor = errors.New("invalid cursor")

type MessageCursor struct {
	Version   int       `json:"v"`
	Type      string    `json:"t"`
	QueryHash string    `json:"q"`
	Score     float64   `json:"s"`
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

func EncodeMessageCursor(query string, score float64, createdAt time.Time, id string) (string, error) {
	if !validID(id) || math.IsNaN(score) || math.IsInf(score, 0) || createdAt.IsZero() {
		return "", ErrInvalidCursor
	}
	return encodeCursor(MessageCursor{CursorVersion, CursorMessages, queryHash(query), score, createdAt.UTC(), id})
}

func DecodeMessageCursor(raw, query string) (MessageCursor, error) {
	var c MessageCursor
	if err := decodeCursor(raw, &c); err != nil || c.Version != CursorVersion || c.Type != CursorMessages || c.QueryHash != queryHash(query) || !validID(c.ID) || c.CreatedAt.IsZero() || math.IsNaN(c.Score) || math.IsInf(c.Score, 0) {
		return MessageCursor{}, ErrInvalidCursor
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
func validNameKind(kind string) bool { return kind == CursorUsers || kind == CursorChannels }
