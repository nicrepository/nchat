// Package scanner speaks to an antimalware daemon (RF-22).
//
// There is exactly one implementation and it is a network client: clamd's
// INSTREAM command over TCP. Nothing here runs a process, builds a command line
// or writes a temporary file, so no filename, MIME type or byte of content ever
// reaches a shell, a path or an argument — the content travels as a length-
// prefixed byte stream on a socket and nothing else does.
package scanner

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

// Verdict is what the daemon decided about one stream. It has two values and no
// third: "the scanner could not rule" is an error, never a verdict, because a
// verdict is what makes content reachable and an error must never do that.
type Verdict int

const (
	// VerdictClean means the daemon inspected the whole stream and found
	// nothing. It is the only value that can make an attachment downloadable.
	VerdictClean Verdict = iota
	// VerdictInfected means the daemon matched a signature.
	VerdictInfected
)

// ErrStreamTooLarge reports a stream the daemon refused to finish reading
// because it exceeded its own StreamMaxLength.
//
// It is a scan *failure* — the file was never inspected — and it is named
// separately from every other failure for one reason: it is the only one an
// operator fixes by changing configuration rather than by restarting something.
// A file-service that accepts uploads larger than clamd's limit produces
// attachments that can never be scanned and therefore never be downloaded, so
// this error is what makes that misconfiguration visible instead of looking
// like a daemon that is merely flaky.
var ErrStreamTooLarge = errors.New("scanner: stream exceeds the daemon's size limit")

// ErrProtocol reports a reply the client could not interpret.
//
// Treated exactly like any other failure — not approved — because a response
// this build does not understand is not evidence of anything. It never degrades
// to "clean".
var ErrProtocol = errors.New("scanner: unexpected daemon response")

// Scanner inspects one stream and reports what the daemon decided.
//
// It takes an io.Reader rather than a path or a []byte, which is the whole
// point: the caller hands it a decrypting reader over the stored object, so the
// plaintext exists only in the chunk buffer below and is never written anywhere.
type Scanner interface {
	Scan(ctx context.Context, content io.Reader) (Verdict, error)
}

const (
	// chunkSize bounds how much plaintext is in memory at once. clamd accepts
	// any chunk size; 64 KiB is a syscall-efficient buffer that keeps a 512 MiB
	// attachment costing 64 KiB of RAM rather than 512 MiB.
	chunkSize = 64 << 10

	// maxResponseBytes bounds the reply. A daemon that answers with megabytes
	// is either broken or not clamd, and either way the client must not read it
	// into memory to find that out.
	maxResponseBytes = 4 << 10
)

// Clamd is a clamd client. It holds no connection between scans: one scan is
// one connection, opened and closed, so a daemon restart costs the scan in
// flight and nothing else — there is no pooled socket to go stale, and no
// shared state for two concurrent scans to corrupt.
type Clamd struct {
	address string
	timeout time.Duration
	dialer  net.Dialer
}

// New builds a client for a "host:port" address.
//
// The address comes from configuration and is validated there; it is used only
// as a dial target and never interpolated into anything.
func New(address string, timeout time.Duration) (*Clamd, error) {
	if strings.TrimSpace(address) == "" {
		return nil, errors.New("scanner: address is required")
	}
	if timeout <= 0 {
		return nil, errors.New("scanner: timeout must be positive")
	}
	return &Clamd{address: address, timeout: timeout}, nil
}

// Scan streams content to the daemon and returns its verdict.
//
// Three deadlines apply and they are not redundant:
//
//   - the dial is bounded by ctx, so a daemon that does not accept connections
//     fails at connect time rather than hanging;
//   - the socket carries an absolute deadline covering the whole exchange, so a
//     daemon that accepts the connection and then stops reading cannot hold the
//     worker for longer than the configured budget;
//   - ctx cancellation closes the connection underneath the I/O, so a shutdown
//     interrupts a scan in progress instead of waiting for the deadline.
//
// Every error path returns a non-nil error. There is no branch that returns
// VerdictClean with an error, and no branch that turns a failure into a clean
// verdict — the zero value of Verdict is VerdictClean, so every return here
// names its verdict explicitly rather than relying on it.
func (c *Clamd) Scan(ctx context.Context, content io.Reader) (Verdict, error) {
	if c == nil {
		return VerdictInfected, errors.New("scanner: not configured")
	}
	if content == nil {
		return VerdictInfected, errors.New("scanner: no content to scan")
	}

	dialCtx, cancelDial := context.WithTimeout(ctx, c.timeout)
	defer cancelDial()

	conn, err := c.dialer.DialContext(dialCtx, "tcp", c.address)
	if err != nil {
		// The address is deliberately absent from the message: it names internal
		// topology, and the caller logs a category rather than a target.
		return VerdictInfected, fmt.Errorf("scanner: connect failed: %w", errRedacted(err))
	}
	defer func() { _ = conn.Close() }()

	// Cancellation reaches the blocked read or write by closing the socket,
	// which is the only thing that interrupts net I/O. Stopped on every return.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	if err := conn.SetDeadline(time.Now().Add(c.timeout)); err != nil {
		return VerdictInfected, fmt.Errorf("scanner: set deadline: %w", errRedacted(err))
	}

	// The reply is read concurrently with the send, not after it, and that is a
	// correctness requirement rather than a throughput one.
	//
	// clamd answers "INSTREAM size limit exceeded" and *then* closes, part-way
	// through a stream it has decided not to finish. A peer that closes with
	// unread data queued makes the connection produce a reset, and a reset
	// discards whatever was still in the receive buffer — including that reply.
	// Reading only after the write failed would therefore lose the one message
	// that says which failure this was, and the single misconfiguration an
	// operator has to fix would look like an ordinary flaky socket.
	//
	// Reading in parallel takes the reply out of the buffer the moment it lands.
	// The goroutine is joined before this function returns, so the deferred Close
	// can never run underneath it.
	type reply struct {
		verdict Verdict
		err     error
	}
	replies := make(chan reply, 1)
	go func() {
		verdict, readErr := readVerdict(conn)
		replies <- reply{verdict: verdict, err: readErr}
	}()

	// The send stops the moment a reply is available. A daemon that has already
	// answered — because it matched a signature early, or refused the stream's
	// size — has nothing more to learn from the remaining bytes, and pushing
	// them into a socket the peer is closing is what turns its reply into a
	// connection reset that discards it.
	writeErr := writeStream(conn, content, func() bool { return len(replies) > 0 })

	// # Why a *local* failure has to close the connection here
	//
	// The two ways a send fails are not the same event, and treating them alike
	// is what pinned a worker for the whole budget.
	//
	//   - the socket failed. The daemon did something — almost always it replied
	//     and hung up — so a reply is either already buffered or in flight, and
	//     waiting for it is exactly right;
	//   - the *content* failed: storage went away, the decrypting reader
	//     rejected a chunk, the object was truncated. The daemon has seen a
	//     partial INSTREAM with no terminator, has said nothing, and is waiting
	//     for bytes that will never be written. Nothing is coming. Waiting means
	//     blocking until the socket deadline, which is now the operator's
	//     configured budget — minutes of a worker held by a failure that was
	//     already known at its first instant.
	//
	// So a source failure closes the connection, which is what unblocks the
	// reader, and the goroutine is still joined below. It is not an unconditional
	// close on any write error: that would abandon the early reply this whole
	// arrangement exists to catch.
	//
	// The receive below is still unconditional even when the connection was just
	// closed, and that ordering matters. A reply can land in the buffered channel
	// between writeStream's last check and the content failing, so if the daemon
	// did rule, its verdict is honoured rather than discarded in favour of a
	// storage error.
	var source *sourceError
	if errors.As(writeErr, &source) {
		_ = conn.Close()
	}
	answer := <-replies

	if answer.err == nil {
		return answer.verdict, nil
	}
	if writeErr != nil {
		return VerdictInfected, sendFailure(writeErr)
	}
	return VerdictInfected, answer.err
}

// sourceError marks a failure of the content stream rather than of the socket.
//
// The distinction is the whole point: one means the daemon is still waiting for
// bytes that will never arrive, the other means the daemon has already acted.
// It is a type rather than a sentinel because the cause has to travel with it —
// the caller classifies the failure and the storage error underneath is what
// says which subsystem broke.
type sourceError struct{ err error }

func (e *sourceError) Error() string { return "read content: " + e.err.Error() }
func (e *sourceError) Unwrap() error { return e.err }

// sendFailure describes a send that did not finish, keeping the two causes
// apart in the message as well as in the control flow.
func sendFailure(err error) error {
	var source *sourceError
	if errors.As(err, &source) {
		// Not redacted: this cause is this service's own storage or crypto
		// failure, not the daemon's, so it names nothing about the scanner's
		// topology and the worker classifies on it.
		return fmt.Errorf("scanner: content stream failed: %w", source.err)
	}
	return fmt.Errorf("scanner: send failed: %w", errRedacted(err))
}

// writeStream sends the INSTREAM command followed by the length-prefixed
// content and the zero-length terminator.
//
// The "z" prefix selects NUL-terminated commands, which is what makes the reply
// unambiguously delimited: a newline-terminated reply cannot be told apart from
// a signature name that happens to contain one.
// answered reports whether the daemon has already replied, so the send can stop
// instead of streaming into a connection that is being closed.
func writeStream(conn net.Conn, content io.Reader, answered func() bool) error {
	writer := bufio.NewWriterSize(conn, chunkSize+8)
	if _, err := writer.WriteString("zINSTREAM\x00"); err != nil {
		return err
	}

	buffer := make([]byte, chunkSize)
	var header [4]byte
	for {
		if answered() {
			// Whatever the reply says, it is the answer. Returning without the
			// terminator is deliberate: there is nothing left to terminate.
			return nil
		}
		n, readErr := content.Read(buffer)
		if n > 0 {
			// n cannot exceed len(buffer), which is chunkSize, so the conversion
			// is total. It is written as an explicit clamp rather than a bare
			// cast because the value becomes a length prefix: a wrapped one would
			// desynchronise the framing and make the daemon read the next chunk's
			// header as content.
			if n > chunkSize {
				return &sourceError{err: fmt.Errorf(
					"content reader returned %d bytes for a %d-byte buffer", n, chunkSize)}
			}
			binary.BigEndian.PutUint32(header[:], uint32(n))
			if _, err := writer.Write(header[:]); err != nil {
				return err
			}
			if _, err := writer.Write(buffer[:n]); err != nil {
				return err
			}
			if err := writer.Flush(); err != nil {
				return err
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			// The content stream failed — a truncated object, a failed integrity
			// check, a storage read that died. The daemon is told nothing more
			// and the scan is a failure, never a clean verdict on a partial file.
			//
			// Tagged, because the caller has to tell this apart from a socket
			// failure: the daemon here is still waiting for a terminator that
			// will never be sent, so nobody is going to reply and the connection
			// has to be closed rather than waited on.
			return &sourceError{err: readErr}
		}
	}

	// Zero-length chunk: end of stream.
	binary.BigEndian.PutUint32(header[:], 0)
	if _, err := writer.Write(header[:]); err != nil {
		return err
	}
	return writer.Flush()
}

// readVerdict reads the NUL-terminated reply and classifies it.
//
// Classification is by suffix against a closed set, and anything outside it is
// ErrProtocol. In particular there is no "if it does not say FOUND it is clean"
// branch: only the exact OK reply produces VerdictClean.
func readVerdict(conn net.Conn) (Verdict, error) {
	reply, err := readBounded(conn)
	if err != nil {
		return VerdictInfected, fmt.Errorf("scanner: read reply: %w", errRedacted(err))
	}
	response := strings.TrimSpace(strings.TrimSuffix(reply, "\x00"))

	switch {
	case strings.HasSuffix(response, " OK"):
		return VerdictClean, nil
	case strings.HasSuffix(response, " FOUND"):
		// The signature name is deliberately dropped here rather than returned.
		// It is the daemon's description of what it matched in a user's file,
		// and it has no consumer: the persisted state is "rejected" either way,
		// and the client is never told what was found.
		return VerdictInfected, nil
	case strings.Contains(response, "size limit exceeded"):
		return VerdictInfected, ErrStreamTooLarge
	case strings.HasSuffix(response, "ERROR"):
		return VerdictInfected, fmt.Errorf("%w: daemon reported an error", ErrProtocol)
	default:
		// The response text is not echoed: it is attacker-influenced through the
		// file's content in the FOUND case, and unbounded in every other.
		return VerdictInfected, ErrProtocol
	}
}

// readBounded reads up to maxResponseBytes or the NUL terminator, whichever
// comes first, so a daemon that never stops talking cannot exhaust memory.
//
// The terminator is *required*. A reply that ends because the peer hung up, or
// because the response cap was reached, is incomplete — and an incomplete reply
// is indistinguishable from a truncated one, which is exactly the shape that
// would let "stream: OK, and also…" be read as an approval. So a read that ends
// without the NUL is an error, not a shorter answer.
func readBounded(conn net.Conn) (string, error) {
	reader := bufio.NewReader(io.LimitReader(conn, maxResponseBytes))
	reply, err := reader.ReadString(0)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return "", io.ErrUnexpectedEOF
		}
		return "", err
	}
	return reply, nil
}

// errRedacted strips a network error down to its category.
//
// net.OpError carries the dial target — the daemon's host and port — and that
// is internal topology this service does not put in logs or in errors that
// might reach one. The distinction an operator actually acts on (timeout,
// refused, reset) survives; the address does not.
func errRedacted(err error) error {
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		if opErr.Timeout() {
			return errors.New("timeout")
		}
		return errors.New("connection failed")
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return err
	}
	return errors.New("transport failed")
}
