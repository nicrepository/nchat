package service_test

import (
	"bufio"
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
	"github.com/nicrepository/nchat/services/admin-service/internal/service"
)

// The scaffolding the integration specs share.
//
// The dependencies are real local servers rather than mocks of the protocol: a
// fake that answers "PONG" to whatever it is sent proves that the code calls a
// function, and what these specs need to prove is that it speaks a protocol and
// refuses what it should.

// healthStub is the passive collection the integration surface reads. It
// records whether it was asked to force a refresh, because opening the
// integrations page must never contact anything.
type integrationHealthStub struct {
	snapshot domain.HealthSnapshot
	err      error
	forced   []bool
}

func (h *integrationHealthStub) Snapshot(_ context.Context, force bool) (domain.HealthSnapshot, error) {
	h.forced = append(h.forced, force)
	return h.snapshot, h.err
}

// configStub is the configuration catalogue the integration surface reads.
type integrationConfigStub struct {
	view  service.ConfigCatalogView
	err   error
	calls int
}

func (c *integrationConfigStub) Catalog(context.Context) (service.ConfigCatalogView, error) {
	c.calls++
	return c.view, c.err
}

// auditStub captures the trail so a spec can assert what was recorded and, just
// as importantly, what was not.
type integrationAudit struct {
	mu     sync.Mutex
	events []domain.AuditEvent
}

func (a *integrationAudit) Record(_ context.Context, event domain.AuditEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, event)
}

func (a *integrationAudit) all() []domain.AuditEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]domain.AuditEvent{}, a.events...)
}

func (a *integrationAudit) last(t *testing.T) domain.AuditEvent {
	t.Helper()
	events := a.all()
	if len(events) == 0 {
		t.Fatal("expected an audit event")
	}
	return events[len(events)-1]
}

// integrationAuthorizer stands in for the database's view of an administrator's
// current authority.
//
// Specs mutate it mid-run to model a revocation landing while a request is in
// flight, which is the whole point of the re-authorization: the snapshot the
// middleware produced cannot see that happen.
type integrationAuthorizer struct {
	mu    sync.Mutex
	err   error
	calls []domain.Capability
}

func (a *integrationAuthorizer) ReauthorizeAction(_ context.Context, authorization domain.MutationAuthorization) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, authorization.Capability)
	return a.err
}

// revoke is what a role removal, a session revocation or a suspension looks
// like from this service's side: the next read of the database refuses.
func (a *integrationAuthorizer) revoke(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.err = err
}

// observed reports the capability each re-authorization asked about, in order.
func (a *integrationAuthorizer) observed() []domain.Capability {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]domain.Capability{}, a.calls...)
}

// denyLimiter refuses everything, so a spec can exercise the rate-limited path
// without waiting a minute for a token to refill.
type denyLimiter struct{ keys []string }

func (d *denyLimiter) Allow(key string) bool {
	d.keys = append(d.keys, key)
	return false
}

func envOf(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

// fixedNow is a deterministic clock that advances one millisecond per read.
//
// Guarded, because the real clock it stands in for is: two diagnostics run
// concurrently under the process-wide slot cap, and both read it. An unguarded
// counter here is a data race in the scaffolding — not in the service, which
// only ever calls the function it was given — and the race detector is right to
// say so.
func fixedNow() func() time.Time {
	var mu sync.Mutex
	instant := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		instant = instant.Add(time.Millisecond)
		return instant
	}
}

func integrationActor(capabilities ...domain.Capability) service.Actor {
	return service.Actor{
		UserID:        "11111111-1111-1111-1111-111111111111",
		Email:         "operator@example.test",
		SessionID:     "22222222-2222-2222-2222-222222222222",
		CorrelationID: "req-1",
		Capabilities:  domain.NewCapabilitySet(capabilities),
	}
}

// stepOf finds one stage's result in a report.
func stepOf(t *testing.T, report domain.DiagnosticReport, stage domain.DiagnosticStage) domain.DiagnosticStep {
	t.Helper()
	for _, step := range report.Steps {
		if step.Stage == stage {
			return step
		}
	}
	t.Fatalf("report has no %s stage: %+v", stage, report.Steps)
	return domain.DiagnosticStep{}
}

// assertNoSecretLeaked is the invariant every diagnostic obeys: nothing a
// deployment holds as a credential may appear anywhere in a report.
func assertNoSecretLeaked(t *testing.T, report domain.DiagnosticReport, secrets ...string) {
	t.Helper()
	var rendered strings.Builder
	rendered.WriteString(report.Summary)
	rendered.WriteString(report.Version)
	for _, step := range report.Steps {
		rendered.WriteString(step.Detail)
		rendered.WriteString(string(step.Category))
	}
	for _, secret := range secrets {
		if secret != "" && strings.Contains(rendered.String(), secret) {
			t.Fatalf("a credential reached the diagnostic report: %q", secret)
		}
	}
}

// fakeSMTPServer is enough of a relay to exercise the staged diagnostic.
//
// It speaks the commands the diagnostic actually sends and nothing else, so a
// change that started sending something new would hang here rather than pass
// against a mock that agrees with everything.
type fakeSMTPServer struct {
	listener net.Listener
	// offerSTARTTLS advertises STARTTLS in the EHLO response.
	offerSTARTTLS bool
	// greeting replaces the 220 banner, so a spec can model a server that
	// answers but is not an SMTP relay.
	greeting string
	// rejectRecipient makes the relay refuse the envelope, so a spec can model
	// a relay that is reachable and still will not carry the message.
	rejectRecipient bool
	// onNoop fires when the session reaches NOOP — after AUTH and immediately
	// before the diagnostic would write an envelope. It is the deterministic
	// hook a spec uses to revoke authority *during* the SMTP exchange, without
	// timers and without goroutine choreography.
	onNoop func()

	mu       sync.Mutex
	envelope []string
	body     strings.Builder
}

func startFakeSMTP(t *testing.T, server *fakeSMTPServer) *fakeSMTPServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server.listener = listener
	t.Cleanup(func() { _ = listener.Close() })
	go server.serve()
	return server
}

func (s *fakeSMTPServer) address() string { return s.listener.Addr().String() }

func (s *fakeSMTPServer) host() string {
	host, _, _ := net.SplitHostPort(s.address())
	return host
}

func (s *fakeSMTPServer) port() string {
	_, port, _ := net.SplitHostPort(s.address())
	return port
}

func (s *fakeSMTPServer) recorded() ([]string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.envelope...), s.body.String()
}

func (s *fakeSMTPServer) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *fakeSMTPServer) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	greeting := s.greeting
	if greeting == "" {
		greeting = "220 fake ESMTP ready\r\n"
	}
	if _, err := conn.Write([]byte(greeting)); err != nil {
		return
	}
	reader := bufio.NewReader(conn)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		if !s.respond(conn, reader, strings.TrimSpace(line)) {
			return
		}
	}
}

// respond answers one command and reports whether the session continues.
func (s *fakeSMTPServer) respond(conn net.Conn, reader *bufio.Reader, line string) bool {
	command := strings.ToUpper(line)
	switch {
	case strings.HasPrefix(command, "EHLO"):
		_, _ = conn.Write([]byte(s.ehlo()))
	case strings.HasPrefix(command, "HELO"):
		_, _ = conn.Write([]byte("250 fake\r\n"))
	case strings.HasPrefix(command, "STARTTLS"):
		// The 220 is written and the connection is then left speaking plain
		// text, so the client's handshake fails. That is the point: it proves
		// verification is on and cannot be talked out of.
		_, _ = conn.Write([]byte("220 go ahead\r\n"))
		return false
	case strings.HasPrefix(command, "AUTH"):
		_, _ = conn.Write([]byte("535 authentication failed\r\n"))
	case strings.HasPrefix(command, "QUIT"):
		_, _ = conn.Write([]byte("221 bye\r\n"))
		return false
	default:
		s.respondToEnvelope(conn, reader, command, line)
	}
	return true
}

// respondToEnvelope answers the commands that carry the message itself.
func (s *fakeSMTPServer) respondToEnvelope(conn net.Conn, reader *bufio.Reader, command, line string) {
	switch {
	case strings.HasPrefix(command, "RCPT"):
		s.record(line)
		if s.rejectRecipient {
			_, _ = conn.Write([]byte("550 no such mailbox\r\n"))
			return
		}
		_, _ = conn.Write([]byte("250 ok\r\n"))
	case strings.HasPrefix(command, "MAIL"):
		s.record(line)
		_, _ = conn.Write([]byte("250 ok\r\n"))
	case strings.HasPrefix(command, "DATA"):
		_, _ = conn.Write([]byte("354 end with .\r\n"))
		s.readBody(reader)
		_, _ = conn.Write([]byte("250 queued\r\n"))
	case strings.HasPrefix(command, "NOOP"):
		if s.onNoop != nil {
			s.onNoop()
		}
		_, _ = conn.Write([]byte("250 ok\r\n"))
	default:
		_, _ = conn.Write([]byte("500 unknown\r\n"))
	}
}

func (s *fakeSMTPServer) ehlo() string {
	if s.offerSTARTTLS {
		return "250-fake\r\n250-STARTTLS\r\n250 AUTH PLAIN\r\n"
	}
	return "250-fake\r\n250 AUTH PLAIN\r\n"
}

func (s *fakeSMTPServer) record(line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.envelope = append(s.envelope, line)
}

func (s *fakeSMTPServer) readBody(reader *bufio.Reader) {
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		if strings.TrimSpace(line) == "." {
			return
		}
		s.mu.Lock()
		s.body.WriteString(line)
		s.mu.Unlock()
	}
}

// fakeClamd answers the two commands the diagnostic sends.
type fakeClamd struct {
	listener net.Listener
	version  string
	// pong replaces the PING reply, so a spec can model a daemon that answers
	// something this build does not interpret.
	pong string
}

func startFakeClamd(t *testing.T, daemon *fakeClamd) *fakeClamd {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	daemon.listener = listener
	t.Cleanup(func() { _ = listener.Close() })
	go daemon.serve()
	return daemon
}

func (c *fakeClamd) address() string { return c.listener.Addr().String() }

func (c *fakeClamd) serve() {
	for {
		conn, err := c.listener.Accept()
		if err != nil {
			return
		}
		go c.handle(conn)
	}
}

func (c *fakeClamd) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	reader := bufio.NewReader(conn)
	for {
		command, err := reader.ReadString(0)
		if err != nil {
			return
		}
		switch strings.TrimRight(command, "\x00") {
		case "zPING":
			reply := c.pong
			if reply == "" {
				reply = "PONG"
			}
			_, _ = conn.Write([]byte(reply + "\x00"))
		case "zVERSION":
			_, _ = conn.Write([]byte(c.version + "\x00"))
		default:
			return
		}
	}
}
