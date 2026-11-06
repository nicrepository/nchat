package signatureguard

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/file-service/internal/scanner"
	"github.com/nicrepository/nchat/services/file-service/internal/signatures"
)

func TestRollbackIsTerminalEvenForOneNanosecond(t *testing.T) {
	for _, delta := range []time.Duration{time.Nanosecond, time.Hour} {
		g := testGuard()
		now := time.Now().UTC()
		g.now = func() time.Time { return now }
		if !g.ready() {
			t.Fatal("stable clock closed")
		}
		now = now.Add(-delta)
		if g.ready() || !errors.Is(g.failure(), signatures.ErrClockRollback) {
			t.Fatal("rollback approved")
		}
		assertClockFault(t, g)
		now = now.Add(2 * time.Hour)
		if g.ready() {
			t.Fatal("terminal cycle resumed without reactivation")
		}
	}
}

func assertClockFault(t *testing.T, g *Guard) {
	t.Helper()
	select {
	case <-g.faultSignal():
	default:
		t.Fatal("supervisor not signaled")
	}
	w := httptest.NewRecorder()
	g.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if w.Code != 503 {
		t.Fatal("terminal cycle healthy")
	}
}

func TestRollbackDuringScanCannotReturnClean(t *testing.T) {
	g := testGuard()
	now := time.Now().UTC()
	g.now = func() time.Time { return now }
	g.Scanner = scanFunc(func(_ context.Context, r io.Reader) (scanner.Verdict, error) {
		_, err := io.Copy(io.Discard, r)
		now = now.Add(-time.Minute)
		return scanner.VerdictClean, err
	})
	w := httptest.NewRecorder()
	g.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/scan", strings.NewReader("benign")))
	if w.Code != 503 || strings.Contains(w.Body.String(), "clean") {
		t.Fatal("rollback returned approval")
	}
}

func TestClockWaitRecoversWithoutRestartLoop(t *testing.T) {
	floor := time.Now().UTC()
	var at atomic.Int64
	at.Store(floor.Add(-time.Hour).UnixNano())
	now := func() time.Time { return time.Unix(0, at.Load()) }
	ticks := make(chan time.Time, 1)
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { done <- awaitClock(ctx, now, floor, ticks) }()
	select {
	case <-done:
		t.Fatal("old clock authorized restart")
	default:
	}
	at.Store(floor.UnixNano())
	ticks <- floor
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	cancel()
	if !errors.Is(awaitClock(ctx, now, floor.Add(time.Hour), ticks), context.Canceled) {
		t.Fatal("clock wait leaked")
	}
}

func TestPersistentClockSurvivesRestartAndGenerationChange(t *testing.T) {
	store := signatures.Store{Root: t.TempDir()}
	now := time.Now().UTC()
	requireSuccess(t, store.CheckpointClock(now))
	floor, err := store.ClockWatermark()
	requireSuccess(t, err)
	g := testGuard()
	g.now = func() time.Time { return now.Add(-time.Second) }
	g.lastClock = floor.UnixNano()
	g.checkpoint = store.CheckpointClock
	if g.ready() {
		t.Fatal("restart erased watermark")
	}
	recovered := testGuard()
	recovered.Evidence.Generation = strings.Repeat("b", 64)
	recovered.now = func() time.Time { return now.Add(time.Second) }
	recovered.lastClock = floor.UnixNano()
	recovered.checkpoint = store.CheckpointClock
	if !recovered.ready() {
		t.Fatal("trusted clock did not recover fresh generation")
	}
	recovered.MaxAge = time.Second
	if recovered.ready() {
		t.Fatal("expired generation authorized during recovery")
	}
}

func TestSupervisorJoinsAfterClockFailure(t *testing.T) {
	g := testGuard()
	g.lastClock = time.Now().Add(time.Hour).UnixNano()
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() {
		done <- serveGeneration(ctx, signatures.Store{Root: t.TempDir()}, g, "127.0.0.1:0", make(chan struct{}))
	}()
	if !errors.Is(<-done, signatures.ErrClockRollback) {
		t.Fatal("supervisor did not close failed cycle")
	}
}

func TestClockCheckpointFailureClosesAdmission(t *testing.T) {
	g := testGuard()
	g.checkpoint = func(time.Time) error { return io.ErrShortWrite }
	if g.ready() || !errors.Is(g.failure(), io.ErrShortWrite) {
		t.Fatal("failed durable clock approved")
	}
}

func TestGuardianMapsInternalFailuresWithoutDetails(t *testing.T) {
	for cause, status := range map[error]int{context.Canceled: 408, context.DeadlineExceeded: 504, scanner.ErrStreamTooLarge: 413, scanner.ErrProtocol: 502, io.ErrUnexpectedEOF: 503} {
		g := testGuard()
		g.Scanner = scanFunc(func(context.Context, io.Reader) (scanner.Verdict, error) { return scanner.VerdictInfected, cause })
		w := httptest.NewRecorder()
		g.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/scan", strings.NewReader("body")))
		if w.Code != status || w.Body.Len() != 0 {
			t.Fatal("wrong internal error mapping", w.Code, status)
		}
	}
}

func TestRestartWaitsForDurableClockAndJoinsOnCancel(t *testing.T) {
	store := signatures.Store{Root: t.TempDir()}
	future := time.Now().Add(time.Hour)
	requireSuccess(t, store.CheckpointClock(future))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := Serve(ctx, store, "missing", "127.0.0.1:0", time.Hour); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("restart bypassed persistent clock", err)
	}
	if _, err := beginClock(store, future); !errors.Is(err, signatures.ErrClockRollback) {
		t.Fatal("startup race erased watermark")
	}
	floor, err := store.ClockWatermark()
	requireSuccess(t, err)
	if !floor.Equal(future) {
		t.Fatal("rollback lowered durable clock")
	}
}

func TestExpiredCycleStillRemembersObservedWallTime(t *testing.T) {
	g := testGuard()
	g.MaxAge = time.Second
	now := time.Now().UTC()
	g.now = func() time.Time { return now }
	if g.ready() {
		t.Fatal("expired base approved")
	}
	now = now.Add(-time.Second)
	if g.ready() || !errors.Is(g.failure(), signatures.ErrClockRollback) {
		t.Fatal("expiration forgot watermark")
	}
}

func TestCorruptClockAndClosedCompletionAreNotHealth(t *testing.T) {
	store := signatures.Store{Root: t.TempDir()}
	requireSuccess(t, store.Acknowledge(testGuard().Evidence))
	if _, err := waitClock(context.Background(), store); err == nil {
		t.Fatal("lost durable clock reset")
	}
	g := testGuard()
	g.Close()
	if _, ok := g.completionEvidence(); ok {
		t.Fatal("closed completion authorized")
	}
	w := httptest.NewRecorder()
	g.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/scan", nil))
	if w.Code != 404 {
		t.Fatal("unexpected method accepted")
	}
}
