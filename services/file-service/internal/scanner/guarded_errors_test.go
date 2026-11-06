package scanner

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/nicrepository/nchat/services/file-service/internal/signatures"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type failedReader struct{ err error }

func (f failedReader) Read([]byte) (int, error) { return 0, f.err }

func TestGuardedPreservesTransportCauses(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded, io.ErrUnexpectedEOF} {
		t.Run(cause.Error(), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.Copy(io.Discard, r.Body) }))
			defer server.Close()
			_, err := guardedClient(t, server.URL).Scan(context.Background(), failedReader{cause})
			if !errors.Is(err, cause) {
				t.Fatal("lost reader cause", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client, err := NewGuarded("127.0.0.1:1", time.Second, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.Scan(ctx, strings.NewReader("x")); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost", err)
	}
}

func TestGuardedHTTPFailureMapping(t *testing.T) {
	for status, want := range map[int]error{413: ErrStreamTooLarge, 503: ErrGuardianUnavailable, 504: context.DeadlineExceeded, 408: context.Canceled, 502: ErrProtocol, 400: ErrProtocol} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.WriteHeader(status)
			}))
			defer server.Close()
			verdict, err := guardedClient(t, server.URL).Scan(context.Background(), strings.NewReader("x"))
			if verdict == VerdictClean || !errors.Is(err, want) {
				t.Fatal("wrong HTTP cause", status, err)
			}
		})
	}
	_, err := decodeGuardedReply(failedReader{io.ErrUnexpectedEOF})
	if !errors.Is(err, ErrProtocol) || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatal("reply read cause lost", err)
	}
}

func TestGuardedRejectsEvidenceFromBeforeThisRequest(t *testing.T) {
	now := time.Now().UTC()
	proof := signatures.Evidence{Generation: strings.Repeat("a", 64), Engine: signatures.Engine, DailyVersion: 1, DatabaseTime: now.Add(-time.Hour), LoadedAt: now.Add(-time.Minute), CompletedAt: now}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_ = json.NewEncoder(w).Encode(GuardedReply{Result: "clean", Evidence: proof})
	}))
	defer server.Close()
	for _, body := range []string{"original A", "different attachment B"} {
		verdict, err := guardedClient(t, server.URL).Scan(context.Background(), strings.NewReader(body))
		if verdict == VerdictClean || !errors.Is(err, signatures.ErrUntrusted) {
			t.Fatal("old proof replay accepted")
		}
	}
}

func TestGuardedHTTPTimeoutKeepsDeadlineCause(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-time.After(100 * time.Millisecond):
		}
	}))
	defer server.Close()
	client := guardedClient(t, server.URL)
	client.client.Timeout = 10 * time.Millisecond
	verdict, err := client.Scan(context.Background(), strings.NewReader("body"))
	if verdict == VerdictClean || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("HTTP timeout lost cause", err)
	}
}
