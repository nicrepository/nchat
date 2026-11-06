package signatures

import (
	"errors"
	"os"
	"path/filepath"
	"time"
)

var ErrClockRollback = errors.New("signature clock rollback")

// The single Recreate consumer owns this durable admission watermark. It must
// survive container/pod restarts; /tmp and the signature generation are not it.
func (s Store) ClockWatermark() (time.Time, error) {
	var clock struct {
		At time.Time `json:"at"`
	}
	err := readJSON(filepath.Join(s.ackRoot(), "clock.json"), &clock)
	if errors.Is(err, os.ErrNotExist) {
		if _, ackErr := os.Lstat(filepath.Join(s.ackRoot(), "loaded.json")); errors.Is(ackErr, os.ErrNotExist) {
			return time.Time{}, nil // Only a store that has never activated may bootstrap.
		}
		return time.Time{}, ErrUntrusted
	}
	if err != nil {
		return time.Time{}, err
	}
	if clock.At.IsZero() {
		return time.Time{}, ErrUntrusted
	}
	return clock.At, nil
}

func (s Store) CheckpointClock(at time.Time) error {
	if at.IsZero() {
		return ErrUntrusted
	}
	if err := os.MkdirAll(s.ackRoot(), 0700); err != nil {
		return err
	}
	return atomicJSON(s.ackRoot(), "clock.json", struct {
		At time.Time `json:"at"`
	}{at.UTC()})
}
