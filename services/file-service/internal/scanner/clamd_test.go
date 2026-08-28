package scanner_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/file-service/internal/scanner"
)

// fakeDaemon is a clamd stand-in: it accepts one connection, reads the INSTREAM
// framing, and replies with whatever the test scripted.
//
// It speaks the real protocol rather than being a mock of the client's calls,
// so these tests assert what the daemon actually receives and what the client
// makes of a real reply — not that a method was invoked.
type fakeDaemon struct {
	listener net.Listener
	// reply is written after the terminator is read. Empty means "write
	// nothing", which is how a daemon that hangs up silently is simulated.
	reply string
	// hold delays the reply, so a timeout can be exercised without sleeping for
	// the client's real budget.
	hold time.Duration
	// closeEarly hangs up as soon as the command arrives, before the content has
	// been read — clamd's behaviour when a stream exceeds StreamMaxLength.
	closeEarly bool
	// silent keeps the connection open and never answers, which is what clamd
	// does while it is still waiting for the rest of an INSTREAM. It is how a
	// *local* source failure is exercised: the daemon has nothing to say,
	// because the terminator it is waiting for will never be sent.
	silent bool
	// resetAfterCommand aborts the connection the moment the command arrives,
	// with SO_LINGER 0 so the peer sees a reset rather than an orderly close.
	// A reset is the one transport failure an EOF cannot stand in for: it
	// discards whatever was queued, so a client that read its reply optimistically
	// would be left holding nothing at all.
	resetAfterCommand bool

	mu       sync.Mutex
	received []byte
	command  string
}

func startFakeDaemon(t *testing.T, daemon *fakeDaemon) *fakeDaemon {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	daemon.listener = listener
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		daemon.serve(conn)
	}()
	return daemon
}

func (d *fakeDaemon) serve(conn net.Conn) {
	command := make([]byte, len("zINSTREAM\x00"))
	if _, err := io.ReadFull(conn, command); err != nil {
		return
	}
	d.mu.Lock()
	d.command = string(command)
	d.mu.Unlock()

	if d.resetAfterCommand {
		if tcpConn, ok := conn.(*net.TCPConn); ok {
			_ = tcpConn.SetLinger(0)
		}
		_ = conn.Close()
		return
	}

	if d.silent {
		// Read whatever arrives and answer nothing, until the client hangs up.
		// A client that waits for a reply here waits for its whole budget.
		_, _ = io.Copy(io.Discard, conn)
		return
	}

	if d.closeEarly {
		// clamd's shape when a stream exceeds StreamMaxLength: it has read part
		// of the body, answers, and stops accepting more.
		//
		// After replying it keeps draining until the client stops writing, and
		// that is what makes this deterministic rather than a coin flip. Closing
		// with unread data queued produces a TCP reset, and a reset discards the
		// peer's receive buffer — including the reply that was just sent. That
		// race is the transport's, not the client's, and testing it would be
		// testing whether the kernel happens to schedule a goroutine in time.
		//
		// The property under test is the client's: an early reply is captured
		// and classified rather than lost behind the send. The drain ends almost
		// immediately, because the client stops writing as soon as the reply
		// lands.
		buffered := make([]byte, 1<<16)
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		_, _ = conn.Read(buffered)
		if d.reply != "" {
			_, _ = conn.Write([]byte(d.reply))
		}
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, _ = io.Copy(io.Discard, conn)
		return
	}

	var body bytes.Buffer
	var header [4]byte
	for {
		if _, err := io.ReadFull(conn, header[:]); err != nil {
			return
		}
		size := binary.BigEndian.Uint32(header[:])
		if size == 0 {
			break
		}
		if _, err := io.CopyN(&body, conn, int64(size)); err != nil {
			return
		}
	}
	d.mu.Lock()
	d.received = body.Bytes()
	d.mu.Unlock()

	if d.hold > 0 {
		time.Sleep(d.hold)
	}
	if d.reply == "" {
		return
	}
	_, _ = conn.Write([]byte(d.reply))
}

func (d *fakeDaemon) address() string { return d.listener.Addr().String() }

func (d *fakeDaemon) content() []byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.received
}

func (d *fakeDaemon) sentCommand() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.command
}

func newClient(t *testing.T, address string, timeout time.Duration) scanner.Scanner {
	t.Helper()
	client, err := scanner.New(address, timeout)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client
}

func TestScanReportsCleanForAnOKReply(t *testing.T) {
	daemon := startFakeDaemon(t, &fakeDaemon{reply: "stream: OK\x00"})
	client := newClient(t, daemon.address(), 5*time.Second)

	verdict, err := client.Scan(context.Background(), strings.NewReader("harmless bytes"))
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if verdict != scanner.VerdictClean {
		t.Fatalf("verdict = %v, want clean", verdict)
	}
	if got := string(daemon.content()); got != "harmless bytes" {
		t.Fatalf("daemon received %q, want the whole stream", got)
	}
	// The framing matters as much as the verdict: a client that sent the
	// newline form would get replies this one cannot delimit.
	if got := daemon.sentCommand(); got != "zINSTREAM\x00" {
		t.Fatalf("command = %q, want the NUL-terminated INSTREAM", got)
	}
}

func TestScanReportsInfectedForAFOUNDReply(t *testing.T) {
	daemon := startFakeDaemon(t, &fakeDaemon{reply: "stream: Eicar-Test-Signature FOUND\x00"})
	client := newClient(t, daemon.address(), 5*time.Second)

	verdict, err := client.Scan(context.Background(), strings.NewReader("X5O!P%@AP"))
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if verdict != scanner.VerdictInfected {
		t.Fatalf("verdict = %v, want infected", verdict)
	}
}

// A large body is streamed in several chunks, and every one of them has to
// arrive. A client that dropped the tail would hand clamd a prefix and call the
// verdict on it clean.
func TestScanStreamsContentLargerThanOneChunk(t *testing.T) {
	daemon := startFakeDaemon(t, &fakeDaemon{reply: "stream: OK\x00"})
	client := newClient(t, daemon.address(), 10*time.Second)

	payload := bytes.Repeat([]byte("ab"), 200_000)
	if _, err := client.Scan(context.Background(), bytes.NewReader(payload)); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if got := daemon.content(); !bytes.Equal(got, payload) {
		t.Fatalf("daemon received %d bytes, want %d", len(got), len(payload))
	}
}

// The whole security property of this client in one test: nothing that is not
// an explicit OK may come back as clean.
func TestScanNeverReportsCleanForAnythingButOK(t *testing.T) {
	for name, reply := range map[string]string{
		"error":             "INSTREAM read error. ERROR\x00",
		"unknown":           "stream: something else entirely\x00",
		"empty":             "\x00",
		"truncated verdict": "stream: O\x00",
		"no terminator":     "stream: OK",
	} {
		t.Run(name, func(t *testing.T) {
			daemon := startFakeDaemon(t, &fakeDaemon{reply: reply})
			client := newClient(t, daemon.address(), 5*time.Second)

			verdict, err := client.Scan(context.Background(), strings.NewReader("payload"))
			if err == nil {
				t.Fatalf("Scan succeeded with verdict %v, want an error", verdict)
			}
			if verdict == scanner.VerdictClean {
				t.Fatal("an unreadable reply produced a clean verdict")
			}
		})
	}
}

func TestScanReportsTheSizeLimitDistinctly(t *testing.T) {
	daemon := startFakeDaemon(t, &fakeDaemon{
		closeEarly: true,
		reply:      "INSTREAM size limit exceeded. ERROR\x00",
	})
	client := newClient(t, daemon.address(), 5*time.Second)

	// Large enough that the client is still writing when the daemon hangs up,
	// which is the case this test exists for: the write fails and the reply is
	// what carries the diagnosis.
	_, err := client.Scan(context.Background(), bytes.NewReader(bytes.Repeat([]byte("z"), 4<<20)))
	if !errors.Is(err, scanner.ErrStreamTooLarge) {
		t.Fatalf("err = %v, want ErrStreamTooLarge", err)
	}
}

// stallingReader delivers its payload and then declines to make progress once
// before reporting EOF.
//
// (0, nil) is explicitly legal for an io.Reader — it means "nothing this time,
// ask again", not EOF and not "there is more". A client that read it as either
// would be wrong in a different direction each way: as EOF it would hand clamd a
// truncated stream and call the verdict on a prefix, and as evidence of extra
// content it would manufacture a size failure for a file that is not too large.
// It stalls twice: once part-way through the payload, so a client that mistook
// the stall for EOF would visibly truncate the stream, and once exactly at the
// end, which is the position the review is about.
type stallingReader struct {
	payload []byte
	// stallAt is the offset the mid-stream stall is served at, so the bytes
	// after it only arrive if the client asks again.
	stallAt int
	offset  int
	// Each stall is served once, so the next call makes progress or ends. That
	// is what keeps the reader from spinning and the test from hanging.
	stalledMid bool
	stalledEnd bool
	reads      int
}

func (r *stallingReader) Read(p []byte) (int, error) {
	r.reads++
	if r.offset == r.stallAt && !r.stalledMid {
		r.stalledMid = true
		return 0, nil
	}
	if r.offset < len(r.payload) {
		limit := len(r.payload)
		if r.offset < r.stallAt {
			limit = r.stallAt
		}
		n := copy(p, r.payload[r.offset:limit])
		r.offset += n
		return n, nil
	}
	if !r.stalledEnd {
		// The payload is exactly consumed. This is the moment the reviewed
		// behaviour turns on: no bytes, no error, and nothing has ended.
		r.stalledEnd = true
		return 0, nil
	}
	return 0, io.EOF
}

// A reader that stalls exactly where the content ends must still produce an
// ordinary scan.
//
// The two failure modes this pins down are the ones an io.Reader contract
// violation would produce here: treating (0, nil) as EOF, which would terminate
// the stream early and rule on a prefix, or treating it as proof of a byte
// beyond the content, which would fail the scan as too large. Neither may
// happen — the client must simply read again.
func TestScanTreatsANoProgressReadAsNeitherEOFNorOverflow(t *testing.T) {
	daemon := startFakeDaemon(t, &fakeDaemon{reply: "stream: OK\x00"})
	client := newClient(t, daemon.address(), 5*time.Second)

	payload := []byte("content that ends exactly where the reader stalls")
	content := &stallingReader{payload: payload, stallAt: 20}

	verdict, err := client.Scan(context.Background(), content)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if errors.Is(err, scanner.ErrStreamTooLarge) {
		t.Fatal("a no-progress read was reported as a stream over the size limit")
	}
	if verdict != scanner.VerdictClean {
		t.Fatalf("verdict = %v, want clean", verdict)
	}
	// The stall must not have truncated the stream: the daemon ruled on the
	// whole content, not on the prefix that had arrived when the reader paused.
	if got := daemon.content(); !bytes.Equal(got, payload) {
		t.Fatalf("daemon received %q, want the whole payload", got)
	}
	// The client asked again rather than concluding anything from either stall.
	if !content.stalledMid || !content.stalledEnd {
		t.Fatalf("a no-progress read was never reached (mid=%v end=%v); the test proves nothing",
			content.stalledMid, content.stalledEnd)
	}
	if content.reads < 5 {
		t.Fatalf("reader was called %d times, want both halves, both stalls and the EOF",
			content.reads)
	}
}

func TestScanFailsWhenTheDaemonIsUnreachable(t *testing.T) {
	// A listener that is closed immediately gives an address nothing answers on.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	address := listener.Addr().String()
	_ = listener.Close()

	client := newClient(t, address, 2*time.Second)
	verdict, err := client.Scan(context.Background(), strings.NewReader("payload"))
	if err == nil {
		t.Fatal("Scan succeeded against a dead daemon")
	}
	if verdict == scanner.VerdictClean {
		t.Fatal("an unreachable daemon produced a clean verdict")
	}
	// The dial target must not travel with the error: it is internal topology.
	if strings.Contains(err.Error(), address) {
		t.Fatalf("error names the daemon address: %v", err)
	}
}

func TestScanTimesOutWhenTheDaemonNeverReplies(t *testing.T) {
	daemon := startFakeDaemon(t, &fakeDaemon{hold: time.Minute, reply: "stream: OK\x00"})
	client := newClient(t, daemon.address(), 200*time.Millisecond)

	started := time.Now()
	verdict, err := client.Scan(context.Background(), strings.NewReader("payload"))
	if err == nil {
		t.Fatal("Scan succeeded against a daemon that never replied")
	}
	if verdict == scanner.VerdictClean {
		t.Fatal("a timeout produced a clean verdict")
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("Scan took %v, want it bounded by the configured timeout", elapsed)
	}
}

func TestScanStopsWhenTheContextIsCancelled(t *testing.T) {
	daemon := startFakeDaemon(t, &fakeDaemon{hold: time.Minute, reply: "stream: OK\x00"})
	client := newClient(t, daemon.address(), time.Minute)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	started := time.Now()
	verdict, err := client.Scan(ctx, strings.NewReader("payload"))
	if err == nil {
		t.Fatal("Scan succeeded after cancellation")
	}
	if verdict == scanner.VerdictClean {
		t.Fatal("a cancelled scan produced a clean verdict")
	}
	// Cancellation has to reach the blocked read, not wait for the minute-long
	// socket deadline.
	if elapsed := time.Since(started); elapsed > 30*time.Second {
		t.Fatalf("cancellation took %v to take effect", elapsed)
	}
}

// A content stream that fails mid-read is a scan failure, never a verdict on
// the prefix that did arrive.
func TestScanFailsWhenTheContentStreamBreaks(t *testing.T) {
	daemon := startFakeDaemon(t, &fakeDaemon{reply: "stream: OK\x00"})
	client := newClient(t, daemon.address(), 5*time.Second)

	broken := io.MultiReader(
		strings.NewReader("first part"),
		&failingReader{err: errors.New("storage went away")},
	)
	verdict, err := client.Scan(context.Background(), broken)
	if err == nil {
		t.Fatal("Scan succeeded over a broken content stream")
	}
	if verdict == scanner.VerdictClean {
		t.Fatal("a broken content stream produced a clean verdict")
	}
}

func TestNewRejectsUnusableConfiguration(t *testing.T) {
	if _, err := scanner.New("", time.Second); err == nil {
		t.Fatal("New accepted an empty address")
	}
	if _, err := scanner.New("127.0.0.1:3310", 0); err == nil {
		t.Fatal("New accepted a zero timeout")
	}
}

type failingReader struct{ err error }

func (r *failingReader) Read([]byte) (int, error) { return 0, r.err }

// A daemon that answers before the stream is finished — a signature matched
// early, or the size limit reached — must stop the send rather than keep
// pushing bytes into a connection that is being closed.
func TestScanStopsSendingOnceTheDaemonHasAnswered(t *testing.T) {
	daemon := startFakeDaemon(t, &fakeDaemon{
		closeEarly: true,
		reply:      "stream: Early-Match FOUND\x00",
	})
	client := newClient(t, daemon.address(), 5*time.Second)

	verdict, err := client.Scan(context.Background(),
		bytes.NewReader(bytes.Repeat([]byte("q"), 8<<20)))
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if verdict != scanner.VerdictInfected {
		t.Fatalf("verdict = %v, want infected", verdict)
	}
}

func TestScanRefusesUnusableCalls(t *testing.T) {
	var uninitialised *scanner.Clamd
	if _, err := uninitialised.Scan(context.Background(), strings.NewReader("x")); err == nil {
		t.Fatal("a nil client reported a verdict")
	}
	client := newClient(t, "127.0.0.1:1", time.Second)
	if _, err := client.Scan(context.Background(), nil); err == nil {
		t.Fatal("a nil content stream reported a verdict")
	}
}

// A context already cancelled must not reach the daemon at all.
func TestScanRefusesAnAlreadyCancelledContext(t *testing.T) {
	daemon := startFakeDaemon(t, &fakeDaemon{reply: "stream: OK\x00"})
	client := newClient(t, daemon.address(), 5*time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	verdict, err := client.Scan(ctx, strings.NewReader("payload"))
	if err == nil {
		t.Fatal("a cancelled scan reported a verdict")
	}
	if verdict == scanner.VerdictClean {
		t.Fatal("a cancelled scan produced a clean verdict")
	}
}

// An empty file is still scanned: it is a zero-length stream, not a reason to
// skip the daemon. (The upload path refuses empty bodies well before this, but
// the scanner must not be the thing that assumes so.)
func TestScanHandlesAnEmptyStream(t *testing.T) {
	daemon := startFakeDaemon(t, &fakeDaemon{reply: "stream: OK\x00"})
	client := newClient(t, daemon.address(), 5*time.Second)

	verdict, err := client.Scan(context.Background(), strings.NewReader(""))
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if verdict != scanner.VerdictClean {
		t.Fatalf("verdict = %v, want clean", verdict)
	}
	if got := daemon.content(); len(got) != 0 {
		t.Fatalf("daemon received %d bytes for an empty stream", len(got))
	}
}

// The failure that used to pin a worker for the whole scan budget (RF-22).
//
// A local source failure — SeaweedFS dropping the object mid-read, the
// decrypting reader refusing a chunk, a truncated envelope — means the daemon
// has a partial INSTREAM with no terminator and is waiting for bytes that will
// never be written. It will not answer, so waiting for a reply is waiting for
// the socket deadline: with the operator's budget now reaching minutes, that is
// a worker held hostage by a failure known at its first instant.
//
// The bound in this test is deliberately far below the client's own timeout, so
// the only way it passes is if Scan stops waiting on its own.
func TestScanReturnsImmediatelyWhenTheSourceFailsAndTheDaemonStaysSilent(t *testing.T) {
	daemon := startFakeDaemon(t, &fakeDaemon{silent: true})
	// A budget far longer than the test is willing to wait: if the fix regresses,
	// this blocks for a minute and the deadline below fires.
	client := newClient(t, daemon.address(), time.Minute)

	broken := io.MultiReader(
		strings.NewReader("the part that made it"),
		&failingReader{err: errors.New("storage went away")},
	)

	type outcome struct {
		verdict scanner.Verdict
		err     error
	}
	done := make(chan outcome, 1)
	go func() {
		verdict, err := client.Scan(context.Background(), broken)
		done <- outcome{verdict: verdict, err: err}
	}()

	select {
	case got := <-done:
		if got.err == nil {
			t.Fatal("a broken source produced a verdict")
		}
		if got.verdict == scanner.VerdictClean {
			t.Fatal("a broken source produced a clean verdict")
		}
		// The failure is attributed to the content, not to the socket: an
		// operator reading "transport failed" would go looking at the daemon.
		if !strings.Contains(got.err.Error(), "content stream failed") {
			t.Fatalf("err = %v, want it attributed to the content stream", got.err)
		}
		if !strings.Contains(got.err.Error(), "storage went away") {
			t.Fatalf("err = %v, want the underlying cause preserved", got.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Scan did not return: it is waiting for a reply that will never come")
	}
}

// The reader goroutine must end with the call, not outlive it. A leak here is
// one goroutine and one socket per failed scan, which a retrying queue turns
// into an unbounded accumulation.
func TestScanLeaksNoReaderWhenTheSourceFails(t *testing.T) {
	before := runtime.NumGoroutine()

	for range 20 {
		daemon := startFakeDaemon(t, &fakeDaemon{silent: true})
		client := newClient(t, daemon.address(), time.Minute)
		broken := io.MultiReader(
			strings.NewReader("head"),
			&failingReader{err: errors.New("storage went away")},
		)
		if _, err := client.Scan(context.Background(), broken); err == nil {
			t.Fatal("expected the source failure to surface")
		}
	}

	// The fake daemons' own accept/serve goroutines end when their listener is
	// closed at cleanup, so the margin covers them and the runtime's own churn.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= before+20 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("goroutines grew from %d to %d across 20 failed scans",
		before, runtime.NumGoroutine())
}

// The complement, and the reason the source-failure fix is not an
// unconditional close on any write error: when the daemon has already ruled,
// its verdict must survive the send being cut short. Closing indiscriminately
// would throw away exactly the early reply this arrangement exists to catch.
func TestAnEarlyDaemonVerdictSurvivesAnInterruptedSend(t *testing.T) {
	daemon := startFakeDaemon(t, &fakeDaemon{
		closeEarly: true,
		reply:      "stream: Early-Match FOUND\x00",
	})
	client := newClient(t, daemon.address(), 10*time.Second)

	// Large enough that the client is still writing when the daemon hangs up.
	verdict, err := client.Scan(context.Background(),
		bytes.NewReader(bytes.Repeat([]byte("m"), 8<<20)))
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if verdict != scanner.VerdictInfected {
		t.Fatalf("verdict = %v, want the daemon's early verdict to survive", verdict)
	}
}

// TestScanRejectsALimitTheEngineReportsAsAHeuristic is the client half of the
// threat model's headline finding (TM-01, issue #483): a verdict of clean has
// to mean the daemon finished looking.
//
// clamd stops inspecting when it reaches MaxFileSize, MaxScanSize, MaxFiles or
// MaxRecursion. What it says about that depends on one setting, and the
// difference was measured against the pinned clamav/clamav:1.4 image with a
// 2 MiB file of zeros under MaxFileSize 1M:
//
//	AlertExceedsMax no   ->  "/tmp/big.bin: OK"
//	AlertExceedsMax yes  ->  "/tmp/big.bin: Heuristics.Limits.Exceeded.MaxFileSize FOUND"
//
// The deployment now sets it to yes (infra/k8s/base/services/clamav/clamd.conf),
// which turns an abandoned scan into a rejection. This test pins the client
// side of that contract: the heuristic name is not special-cased anywhere, so
// what makes it a rejection is the FOUND suffix — and it must come back as a
// *verdict*, not an error, or the worker would retry forever instead of
// recording the outcome.
func TestScanRejectsALimitTheEngineReportsAsAHeuristic(t *testing.T) {
	for name, reply := range map[string]string{
		"file size":  "stream: Heuristics.Limits.Exceeded.MaxFileSize FOUND\x00",
		"scan size":  "stream: Heuristics.Limits.Exceeded.MaxScanSize FOUND\x00",
		"file count": "stream: Heuristics.Limits.Exceeded.MaxFiles FOUND\x00",
		"recursion":  "stream: Heuristics.Limits.Exceeded.MaxRecursion FOUND\x00",
	} {
		t.Run(name, func(t *testing.T) {
			daemon := startFakeDaemon(t, &fakeDaemon{reply: reply})
			client := newClient(t, daemon.address(), 5*time.Second)

			verdict, err := client.Scan(context.Background(), strings.NewReader("a composite file"))
			if err != nil {
				t.Fatalf("Scan: %v", err)
			}
			if verdict != scanner.VerdictInfected {
				t.Fatalf("verdict = %v, want infected: a limit the engine hit must not be an approval",
					verdict)
			}
		})
	}
}

// TestScanNeverApprovesWhatTheDaemonDidNotFinishSaying covers the failures that
// end a scan without a terminal reply.
//
// Every one of them must produce an error rather than a verdict, so the
// attachment stays in pending_scan and is retried. The security property is the
// asymmetry: an unfinished exchange may cost a retry, it may never cost an
// approval. Together with the OK/FOUND cases above and
// TestScanNeverReportsCleanForAnythingButOK, this completes the verdict matrix
// the threat model asked to be locked down.
func TestScanNeverApprovesWhatTheDaemonDidNotFinishSaying(t *testing.T) {
	cases := map[string]*fakeDaemon{
		// clamd read the command and the machine went away.
		"connection reset": {resetAfterCommand: true},
		// An orderly close with nothing said. Distinct from a reset: the client
		// sees EOF, not an error from the transport.
		"hangs up without replying": {reply: ""},
		// A reply that never reaches its NUL. Indistinguishable from a
		// truncated one, which is exactly the shape that could smuggle an "OK"
		// prefix past a client that accepted short reads.
		"verdict cut short": {reply: "stream: Heuristics.Limits.Exceeded.MaxFileSize FOU"},
		// clamd's answer when a stream passes StreamMaxLength: it stops reading
		// mid-body. The file was never inspected, so this is a failure and not
		// a verdict — and it is the one an operator fixes with configuration.
		"stream over the daemon's limit": {
			closeEarly: true,
			reply:      "INSTREAM size limit exceeded. ERROR\x00",
		},
	}

	for name, daemon := range cases {
		t.Run(name, func(t *testing.T) {
			started := startFakeDaemon(t, daemon)
			client := newClient(t, started.address(), 5*time.Second)

			verdict, err := client.Scan(context.Background(), strings.NewReader("payload"))
			if err == nil {
				t.Fatalf("Scan succeeded with verdict %v, want an error", verdict)
			}
			if verdict == scanner.VerdictClean {
				t.Fatal("an unfinished exchange produced a clean verdict")
			}
		})
	}
}
