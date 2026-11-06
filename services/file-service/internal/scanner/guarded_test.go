package scanner

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/file-service/internal/signatures"
)

func TestGuardedBindsEvidenceToCompletedStream(t *testing.T) {
	for _, result := range []string{"clean", "infected", "", "unknown"} {
		t.Run(result, func(t *testing.T) {
			client, evidence := guardedFixture(t, result)
			verdict, proof, err := client.ScanWithEvidence(context.Background(), strings.NewReader("complete object"))
			assertGuardedResult(t, result, verdict, proof, evidence, err)
		})
	}
}

func guardedFixture(t *testing.T, result string) (*Guarded, signatures.Evidence) {
	t.Helper()
	now := time.Now().UTC()
	evidence := signatures.Evidence{Generation: strings.Repeat("a", 64), Engine: signatures.Engine, DailyVersion: 1, DatabaseTime: now.Add(-time.Hour), LoadedAt: now.Add(-time.Minute), CompletedAt: now}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != "complete object" {
			t.Error("stream incomplete")
			w.WriteHeader(503)
			return
		}
		evidence.CompletedAt = time.Now().UTC()
		_ = json.NewEncoder(w).Encode(GuardedReply{Result: result, Evidence: evidence})
	}))
	t.Cleanup(server.Close)
	return guardedClient(t, server.URL), evidence
}

func guardedClient(t *testing.T, endpoint string) *Guarded {
	t.Helper()
	client, err := NewGuarded(strings.TrimPrefix(endpoint, "http://"), time.Second, 2*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func assertGuardedResult(t *testing.T, result string, verdict Verdict, proof, want signatures.Evidence, err error) {
	t.Helper()
	if result == "" || result == "unknown" {
		if err == nil {
			t.Fatal("unknown result approved")
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if proof.Generation != want.Generation || (verdict == VerdictClean) != (result == "clean") {
		t.Fatal("wrong verdict or evidence")
	}
}

func TestGuardedNegativeResponses(t *testing.T) {
	for _, test := range []struct {
		status int
		body   string
	}{
		{503, `{}`}, {200, `{}`}, {200, `{"result":"clean"}`}, {200, `broken`},
		{200, `{"result":"clean","extra":true}`}, {302, `{}`},
		{200, strings.Repeat(" ", 5000)},
	} {
		t.Run(test.body[:min(len(test.body), 30)], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Location", "http://127.0.0.1:1")
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			client := guardedClient(t, server.URL)
			if verdict, err := client.Scan(context.Background(), strings.NewReader("test")); err == nil || verdict == VerdictClean {
				t.Fatal("invalid guardian approved")
			}
		})
	}
}

func TestGuardedInvalidConfigurationAndUnavailableScanner(t *testing.T) {
	if _, err := NewGuarded("", time.Second, time.Hour); err == nil {
		t.Fatal("empty target accepted")
	}
	if _, err := NewGuarded("localhost:3310", time.Second, 0); err == nil {
		t.Fatal("missing policy accepted")
	}
	client, err := NewGuarded("127.0.0.1:1", time.Second, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.Scan(context.Background(), strings.NewReader("test")); err == nil {
		t.Fatal("offline approved")
	}
}

func TestLocalCommandsCannotTreatUnknownReplyAsHealth(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		buffer := make([]byte, 16)
		_, _ = conn.Read(buffer)
		_, _ = io.WriteString(conn, "wrong\x00")
	}()
	client, err := New(listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if client.Ping(context.Background()) == nil {
		t.Fatal("invalid PONG accepted")
	}
	<-done
	if _, err = NewLocal("", time.Second); err == nil {
		t.Fatal("invalid local socket accepted")
	}
}

func TestGuardedRejectsOversizedReplyEvenAfterValidJSON(t *testing.T) {
	now := time.Now().UTC()
	proof := signatures.Evidence{Generation: strings.Repeat("a", 64), Engine: signatures.Engine, DailyVersion: 1, DatabaseTime: now.Add(-time.Hour), LoadedAt: now.Add(-time.Minute), CompletedAt: now}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_ = json.NewEncoder(w).Encode(GuardedReply{Result: "clean", Evidence: proof})
		_, _ = io.WriteString(w, strings.Repeat(" ", 5000)+`{"result":"unknown"}`)
	}))
	defer server.Close()
	if _, err := guardedClient(t, server.URL).Scan(context.Background(), strings.NewReader("test")); err == nil {
		t.Fatal("truncated reply accepted a valid prefix")
	}
}
