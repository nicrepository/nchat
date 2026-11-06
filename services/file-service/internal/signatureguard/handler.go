// Package signatureguard owns admission to a single immutable clamd process.
package signatureguard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/nicrepository/nchat/services/file-service/internal/scanner"
	"github.com/nicrepository/nchat/services/file-service/internal/signatures"
)

// Guard is constructed only after snapshot verification, VERSION and synthetic
// detection. Its generation never changes for the lifetime of the child clamd.
type Guard struct {
	Scanner    scanner.Scanner
	Evidence   signatures.Evidence
	MaxAge     time.Duration
	LastUpdate time.Time
	mu         sync.Mutex
	closed     bool
	lastClock  int64
	now        func() time.Time
	checkpoint func(time.Time) error
	terminal   error
	fault      chan struct{}
}

func (g *Guard) Close() { g.mu.Lock(); defer g.mu.Unlock(); g.closed = true }

func (g *Guard) ready() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.timeNow()
	if now.UnixNano() < g.lastClock {
		g.failLocked(signatures.ErrClockRollback)
		return false
	}
	if g.closed {
		return false
	}
	if g.checkpoint != nil {
		if err := g.checkpoint(now); err != nil {
			g.failLocked(err)
			return false
		}
	}
	g.lastClock = now.UnixNano()
	return g.Evidence.Valid(now, g.MaxAge) == nil
}

func (g *Guard) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		g.serveGet(w, r.URL.Path)
		return
	}
	if r.Method == http.MethodPost && r.URL.Path == "/scan" {
		g.scan(w, r)
		return
	}
	w.WriteHeader(http.StatusNotFound)
}

func (g *Guard) serveGet(w http.ResponseWriter, path string) {
	switch path {
	case "/healthz":
		_ = g.ready()
		if g.failure() != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	case "/readyz":
		if !g.ready() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	case "/metrics":
		g.metrics(w)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// completionReader counts actual EOF: an early OK or truncated HTTP stream
// cannot produce evidence, even when a daemon misbehaves.
type completionReader struct {
	io.Reader
	eof bool
}

func (c *completionReader) Read(p []byte) (int, error) {
	n, err := c.Reader.Read(p)
	if err == io.EOF {
		c.eof = true
	}
	return n, err
}

func (g *Guard) scan(w http.ResponseWriter, r *http.Request) {
	if !g.ready() {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	content := &completionReader{Reader: http.MaxBytesReader(w, r.Body, 512<<20)}
	verdict, err := g.Scanner.Scan(r.Context(), content)
	if err != nil {
		w.WriteHeader(scanFailureStatus(err))
		return
	}
	if !content.eof || !g.ready() {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if verdict != scanner.VerdictClean && verdict != scanner.VerdictInfected {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	evidence, admitted := g.completionEvidence()
	if !admitted {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	result := "clean"
	if verdict == scanner.VerdictInfected {
		result = "infected"
	}
	_ = json.NewEncoder(w).Encode(scanner.GuardedReply{Result: result, Evidence: evidence})
}

func scanFailureStatus(err error) int {
	var large *http.MaxBytesError
	switch {
	case errors.Is(err, scanner.ErrStreamTooLarge), errors.As(err, &large):
		return http.StatusRequestEntityTooLarge
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout
	case errors.Is(err, context.Canceled):
		return http.StatusRequestTimeout
	case errors.Is(err, scanner.ErrProtocol):
		return http.StatusBadGateway
	default:
		return http.StatusServiceUnavailable
	}
}

func (g *Guard) recordUpdate(at time.Time) { g.mu.Lock(); defer g.mu.Unlock(); g.LastUpdate = at }

func (g *Guard) metrics(w http.ResponseWriter) {
	now := time.Now()
	ready := 0
	if g.ready() {
		ready = 1
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	_, _ = fmt.Fprintf(w, "# TYPE nchat_clamav_signature_max_age_seconds gauge\nnchat_clamav_signature_max_age_seconds %.0f\n", g.MaxAge.Seconds())
	_, _ = fmt.Fprintf(w, "# TYPE nchat_clamav_loaded_timestamp_seconds gauge\nnchat_clamav_loaded_timestamp_seconds %d\n", g.Evidence.LoadedAt.Unix())
	_, _ = fmt.Fprintf(w, "# TYPE nchat_clamav_signatures_ready gauge\nnchat_clamav_signatures_ready %d\n", ready)
	_, _ = fmt.Fprintf(w, "# TYPE nchat_clamav_signature_version gauge\nnchat_clamav_signature_version %d\n", g.Evidence.DailyVersion)
	_, _ = fmt.Fprintf(w, "# TYPE nchat_clamav_signature_age_seconds gauge\nnchat_clamav_signature_age_seconds %.0f\n", now.Sub(g.Evidence.DatabaseTime).Seconds())
	_, _ = fmt.Fprintf(w, "# TYPE nchat_clamav_last_successful_update_timestamp_seconds gauge\nnchat_clamav_last_successful_update_timestamp_seconds %d\n", g.lastUpdate().Unix())
	_, _ = fmt.Fprintf(w, "# TYPE nchat_clamav_engine_info gauge\nnchat_clamav_engine_info{version=%q} 1\n", signatures.Engine)
}

func (g *Guard) lastUpdate() time.Time { g.mu.Lock(); defer g.mu.Unlock(); return g.LastUpdate }
