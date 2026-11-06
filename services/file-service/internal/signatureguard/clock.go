package signatureguard

import (
	"context"
	"log/slog"
	"time"

	"github.com/nicrepository/nchat/services/file-service/internal/signatures"
)

func (g *Guard) timeNow() time.Time {
	if g.now != nil {
		return g.now().UTC()
	}
	return time.Now().UTC()
}

func (g *Guard) failLocked(err error) {
	g.closed = true
	if g.terminal == nil {
		g.terminal = err
		if g.fault == nil {
			g.fault = make(chan struct{})
		}
		close(g.fault)
	}
}

func (g *Guard) failure() error { g.mu.Lock(); defer g.mu.Unlock(); return g.terminal }
func (g *Guard) completionEvidence() (signatures.Evidence, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	e := g.Evidence
	e.CompletedAt = time.Unix(0, g.lastClock).UTC()
	return e, !g.closed
}

func (g *Guard) faultSignal() <-chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.fault == nil {
		g.fault = make(chan struct{})
	}
	return g.fault
}

func awaitClock(ctx context.Context, now func() time.Time, floor time.Time, ticks <-chan time.Time) error {
	for now().Before(floor) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticks:
		}
	}
	return ctx.Err()
}

func waitClock(ctx context.Context, store signatures.Store) (time.Time, error) {
	floor, err := store.ClockWatermark()
	if err != nil {
		return floor, err
	}
	if time.Now().Before(floor) {
		slog.Error("signature clock blocked", "error_type", "clock_rollback")
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	return floor, awaitClock(ctx, time.Now, floor, ticker.C)
}

func beginClock(store signatures.Store, floor time.Time) (time.Time, error) {
	now := time.Now().UTC()
	if now.Before(floor) {
		return floor, signatures.ErrClockRollback
	}
	return now, store.CheckpointClock(now)
}
