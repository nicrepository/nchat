// Package signatures defines the identity and freshness of an immutable ClamAV database.
package signatures

import (
	"encoding/hex"
	"errors"
	"time"
)

// These values belong to the pinned engine, not to an updater-selected release.
const Engine = "1.4.6"
const FunctionalityLevel = 216

var ErrUntrusted = errors.New("scanner: signature generation is not authorized")

// Evidence identifies the immutable snapshot used for the entire scan.
// It is received only from the isolated guardian, never from an upload request.
type Evidence struct {
	Generation   string    `json:"generation"`
	Engine       string    `json:"engine"`
	DailyVersion int64     `json:"daily_version"`
	DatabaseTime time.Time `json:"database_time"`
	LoadedAt     time.Time `json:"loaded_at"`
	CompletedAt  time.Time `json:"completed_at"`
}

// Valid has no default age or clock tolerance. Missing policy fails closed.
func (e Evidence) Valid(now time.Time, maxAge time.Duration) error {
	if !ValidGeneration(e.Generation) || e.Engine != Engine || e.DailyVersion <= 0 {
		return ErrUntrusted
	}
	if maxAge <= 0 || e.DatabaseTime.IsZero() || e.LoadedAt.Before(e.DatabaseTime) {
		return ErrUntrusted
	}
	if now.Before(e.LoadedAt) || !now.Before(e.DatabaseTime.Add(maxAge)) {
		return ErrUntrusted
	}
	return nil
}

func ValidGeneration(id string) bool {
	decoded, err := hex.DecodeString(id)
	return err == nil && len(decoded) == 32 && hex.EncodeToString(decoded) == id
}

func (e Evidence) ValidCompletion(now time.Time, maxAge time.Duration) error {
	if err := e.Valid(now, maxAge); err != nil {
		return err
	}
	if e.CompletedAt.Before(e.LoadedAt) || e.CompletedAt.After(now) {
		return ErrUntrusted
	}
	return nil
}
