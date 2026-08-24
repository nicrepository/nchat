package service_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
)

// The probes are exercised through the service against real local servers
// rather than through mocks of the protocol. A fake that answers "PONG"
// because it was told to proves nothing about whether the client speaks RESP;
// a listener that reads the bytes off the wire does.

// textServer accepts one connection at a time and hands it to a handler. It is
// how the RESP, clamd and SMTP probes are exercised against something that
// actually reads what they write.
type textServer struct {
	listener net.Listener
	// received records what the probe sent, so a spec can assert that the
	// credential travelled as a RESP bulk string rather than as an inline
	// argument, or that no message was ever offered to the SMTP relay.
	received atomic.Pointer[string]
}

func startTextServer(t *testing.T, handle func(conn net.Conn, reader *bufio.Reader) string) *textServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	server := &textServer{listener: listener}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				seen := handle(conn, bufio.NewReader(conn))
				server.received.Store(&seen)
			}()
		}
	}()
	t.Cleanup(func() { _ = listener.Close() })
	return server
}

func (s *textServer) hostPort(t *testing.T) (string, string) {
	t.Helper()
	host, port, err := net.SplitHostPort(s.listener.Addr().String())
	if err != nil {
		t.Fatalf("SplitHostPort: %v", err)
	}
	return host, port
}

func (s *textServer) address() string { return s.listener.Addr().String() }

func (s *textServer) sent() string {
	if value := s.received.Load(); value != nil {
		return *value
	}
	return ""
}

// waitForSent blocks until the handler has recorded what the probe sent.
//
// The probe closes the connection as soon as it has what it needs, so the
// handler's last read can still be in flight when the collection returns.
// Polling here rather than asserting immediately is what keeps the assertion
// about the bytes on the wire instead of about scheduling.
func (s *textServer) waitForSent(t *testing.T) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if value := s.sent(); value != "" {
			return value
		}
		time.Sleep(5 * time.Millisecond)
	}
	return s.sent()
}

// readRESPCommand consumes one RESP array and returns its parts.
func readRESPCommand(reader *bufio.Reader) ([]string, error) {
	header, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	var count int
	if _, err := fmt.Sscanf(strings.TrimSpace(header), "*%d", &count); err != nil {
		return nil, err
	}
	parts := make([]string, 0, count)
	for i := 0; i < count; i++ {
		if _, err := reader.ReadString('\n'); err != nil { // the $<len> line
			return nil, err
		}
		value, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		parts = append(parts, strings.TrimRight(value, "\r\n"))
	}
	return parts, nil
}

func TestValkeyProbeSpeaksRESPAndReportsHealthy(t *testing.T) {
	server := startTextServer(t, func(conn net.Conn, reader *bufio.Reader) string {
		parts, err := readRESPCommand(reader)
		if err != nil {
			return ""
		}
		_, _ = io.WriteString(conn, "+PONG\r\n")
		return strings.Join(parts, " ")
	})
	host, port := server.hostPort(t)
	health := newHealth(t, &stubDatabase{}, environment{"VALKEY_HOST": host, "VALKEY_PORT": port})

	valkey := serviceNamed(t, snapshotOf(t, health, false), domain.HealthServiceValkey)
	if valkey.State != domain.HealthHealthy {
		t.Fatalf("expected healthy, got %s (%s)", valkey.State, valkey.Detail)
	}
	if valkey.LatencyMS == nil {
		t.Fatal("a completed round trip must report its latency")
	}
	if sent := server.waitForSent(t); sent != "PING" {
		t.Fatalf("expected the probe to send exactly PING, got %q", sent)
	}
}

// A password with a space is the case an inline command would corrupt — and
// corrupt by leaking half the secret as a stray argument. This asserts it
// travels as one bulk string.
func TestValkeyProbeSendsTheCredentialAsASingleArgument(t *testing.T) {
	server := startTextServer(t, func(conn net.Conn, reader *bufio.Reader) string {
		auth, err := readRESPCommand(reader)
		if err != nil {
			return ""
		}
		_, _ = io.WriteString(conn, "+OK\r\n")
		if _, err := readRESPCommand(reader); err != nil {
			return strings.Join(auth, "|")
		}
		_, _ = io.WriteString(conn, "+PONG\r\n")
		return strings.Join(auth, "|")
	})
	host, port := server.hostPort(t)
	health := newHealth(t, &stubDatabase{}, environment{
		"VALKEY_HOST": host, "VALKEY_PORT": port, "VALKEY_PASSWORD": "two words",
	})

	valkey := serviceNamed(t, snapshotOf(t, health, false), domain.HealthServiceValkey)
	if valkey.State != domain.HealthHealthy {
		t.Fatalf("expected healthy, got %s (%s)", valkey.State, valkey.Detail)
	}
	if got := server.waitForSent(t); got != "AUTH|two words" {
		t.Fatalf("the credential did not arrive as one argument: %q", got)
	}
}

// A refused credential is a real, actionable condition — and the response must
// name the category without repeating anything the server said.
func TestValkeyProbeClassifiesARefusedCredential(t *testing.T) {
	server := startTextServer(t, func(conn net.Conn, reader *bufio.Reader) string {
		_, _ = readRESPCommand(reader)
		_, _ = io.WriteString(conn, "-WRONGPASS invalid username-password pair for user 'nchat-prod'\r\n")
		return ""
	})
	host, port := server.hostPort(t)
	health := newHealth(t, &stubDatabase{}, environment{
		"VALKEY_HOST": host, "VALKEY_PORT": port, "VALKEY_PASSWORD": "wrong",
	})

	valkey := serviceNamed(t, snapshotOf(t, health, false), domain.HealthServiceValkey)
	if valkey.ErrorCategory != domain.HealthErrorAuthenticationFailed {
		t.Fatalf("expected authentication_failed, got %s", valkey.ErrorCategory)
	}
	if strings.Contains(valkey.Detail, "nchat-prod") {
		t.Fatalf("the server's message reached the response: %q", valkey.Detail)
	}
}

func TestClamAVProbePingsAndReportsASanitizedVersion(t *testing.T) {
	server := startTextServer(t, func(conn net.Conn, reader *bufio.Reader) string {
		sent := make([]string, 0, 2)
		for i := 0; i < 2; i++ {
			command, err := reader.ReadString(0)
			if err != nil {
				break
			}
			command = strings.TrimRight(command, "\x00")
			sent = append(sent, command)
			if strings.Contains(command, "PING") {
				_, _ = io.WriteString(conn, "PONG\x00")
				continue
			}
			// A version reply carrying markup: it is remote input, and it must
			// be filtered rather than trusted.
			_, _ = io.WriteString(conn, "ClamAV 1.4.1/27000/<script>alert(1)</script>\x00")
		}
		return strings.Join(sent, " ")
	})
	health := newHealth(t, &stubDatabase{}, environment{"FILE_MALWARE_SCANNER_ADDRESS": server.address()})

	clamav := serviceNamed(t, snapshotOf(t, health, false), domain.HealthServiceClamAV)
	if clamav.State != domain.HealthHealthy {
		t.Fatalf("expected healthy, got %s (%s)", clamav.State, clamav.Detail)
	}
	if !strings.Contains(clamav.Version, "ClamAV 1.4.1") {
		t.Fatalf("expected the version to survive sanitizing, got %q", clamav.Version)
	}
	if strings.ContainsAny(clamav.Version, "<>") {
		t.Fatalf("markup from the daemon reached the response: %q", clamav.Version)
	}
	// The one thing a periodic antimalware check must never do.
	commands := server.waitForSent(t)
	if strings.Contains(commands, "INSTREAM") || strings.Contains(commands, "EICAR") {
		t.Fatalf("the probe submitted content to the scanner: %q", commands)
	}
}

func TestSMTPProbeReadsTheGreetingAndSendsNoMail(t *testing.T) {
	server := startTextServer(t, func(conn net.Conn, reader *bufio.Reader) string {
		_, _ = io.WriteString(conn, "220 relay.example.test ESMTP ready\r\n")
		line, _ := reader.ReadString('\n')
		return strings.TrimSpace(line)
	})
	host, port := server.hostPort(t)
	health := newHealth(t, &stubDatabase{}, environment{
		"SMTP_WORKER_ENABLED": "true", "SMTP_HOST": host, "SMTP_PORT": port,
	})

	smtp := serviceNamed(t, snapshotOf(t, health, false), domain.HealthServiceSMTP)
	if smtp.State != domain.HealthHealthy {
		t.Fatalf("expected healthy, got %s (%s)", smtp.State, smtp.Detail)
	}
	if sent := server.waitForSent(t); sent != "QUIT" {
		t.Fatalf("the probe must only greet and quit, it sent %q", sent)
	}
}

func TestSMTPProbeRefusesAServerThatIsNotReady(t *testing.T) {
	server := startTextServer(t, func(conn net.Conn, _ *bufio.Reader) string {
		_, _ = io.WriteString(conn, "421 relay.example.test service not available\r\n")
		return ""
	})
	host, port := server.hostPort(t)
	health := newHealth(t, &stubDatabase{}, environment{
		"SMTP_WORKER_ENABLED": "true", "SMTP_HOST": host, "SMTP_PORT": port,
	})

	smtp := serviceNamed(t, snapshotOf(t, health, false), domain.HealthServiceSMTP)
	if smtp.State != domain.HealthDegraded {
		t.Fatalf("expected degraded, got %s", smtp.State)
	}
	if strings.Contains(smtp.Detail, "relay.example.test") {
		t.Fatalf("the relay's hostname reached the response: %q", smtp.Detail)
	}
}

func TestStorageProbeReportsAServerErrorAsUnavailable(t *testing.T) {
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "filer volume seaweedfs-volume-2 on node 10.42.0.9 is offline", http.StatusInternalServerError)
	}))
	defer storage.Close()

	health := newHealth(t, &stubDatabase{}, environment{"SEAWEEDFS_FILER_URL": storage.URL})
	result := serviceNamed(t, snapshotOf(t, health, false), domain.HealthServiceStorage)

	if result.State != domain.HealthUnavailable {
		t.Fatalf("expected unavailable, got %s", result.State)
	}
	if strings.Contains(result.Detail, "10.42.0.9") || strings.Contains(result.Detail, "seaweedfs-volume-2") {
		t.Fatalf("the storage response body reached the console: %q", result.Detail)
	}
}

func TestStorageProbeReportsAWorkingFilerAsHealthy(t *testing.T) {
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer storage.Close()

	health := newHealth(t, &stubDatabase{}, environment{"SEAWEEDFS_FILER_URL": storage.URL})
	result := serviceNamed(t, snapshotOf(t, health, false), domain.HealthServiceStorage)
	if result.State != domain.HealthHealthy {
		t.Fatalf("expected healthy, got %s (%s)", result.State, result.Detail)
	}
	// The object store publishes no trustworthy capacity, so nothing may be
	// claimed about one.
	if result.Version != "" {
		t.Errorf("the storage probe invented a version: %q", result.Version)
	}
}

// A redirect is deliberately not followed. It is the shape a compromised or
// misconfigured dependency would use to nominate a second address, and the
// probe must report reachability without ever making that second request.
func TestHTTPProbeDoesNotFollowRedirects(t *testing.T) {
	var elsewhere atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		elsewhere.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()

	health := newHealth(t, &stubDatabase{}, environment{"SEAWEEDFS_FILER_URL": redirector.URL})
	result := serviceNamed(t, snapshotOf(t, health, false), domain.HealthServiceStorage)

	if elsewhere.Load() != 0 {
		t.Fatal("the probe followed a redirect to an address nobody configured")
	}
	if result.State != domain.HealthDegraded {
		t.Fatalf("an unfollowed redirect is not a healthy answer, got %s", result.State)
	}
}

func oidcProvider(t *testing.T, discovery func(issuer string) any) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(discovery(server.URL))
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"keys":[]}`)
	})
	t.Cleanup(server.Close)
	return server
}

func oidcHealth(t *testing.T, issuer string) domain.ServiceHealth {
	t.Helper()
	health := newHealth(t, &stubDatabase{}, environment{
		"OIDC_ENABLED": "true", "OIDC_ISSUER_URL": issuer,
	})
	return serviceNamed(t, snapshotOf(t, health, false), domain.HealthServiceOIDC)
}

func TestOIDCProbeChecksDiscoveryAndTheKeySet(t *testing.T) {
	provider := oidcProvider(t, func(issuer string) any {
		return map[string]string{"issuer": issuer, "jwks_uri": issuer + "/keys"}
	})
	result := oidcHealth(t, provider.URL)
	if result.State != domain.HealthHealthy {
		t.Fatalf("expected healthy, got %s (%s)", result.State, result.Detail)
	}
}

// An issuer mismatch means every token the provider mints will be refused. It
// is a configuration fault, and it must not be reported as healthy just
// because the endpoint answered.
func TestOIDCProbeRefusesAnIssuerThatContradictsTheConfiguration(t *testing.T) {
	provider := oidcProvider(t, func(issuer string) any {
		return map[string]string{"issuer": "https://other.example.test", "jwks_uri": issuer + "/keys"}
	})
	result := oidcHealth(t, provider.URL)
	if result.State != domain.HealthDegraded || result.ErrorCategory != domain.HealthErrorInvalidConfiguration {
		t.Fatalf("expected degraded/invalid_configuration, got %s/%s", result.State, result.ErrorCategory)
	}
}

// The jwks_uri comes from the provider's response, not from configuration.
// Following it anywhere it pleases would let whatever answers the issuer URL
// choose this pod's next destination — the exact SSRF this design refuses.
func TestOIDCProbeWillNotFollowAKeySetOffTheIssuersOrigin(t *testing.T) {
	var elsewhere atomic.Int32
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		elsewhere.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer internal.Close()

	provider := oidcProvider(t, func(issuer string) any {
		return map[string]string{"issuer": issuer, "jwks_uri": internal.URL + "/latest/meta-data"}
	})
	result := oidcHealth(t, provider.URL)

	if elsewhere.Load() != 0 {
		t.Fatal("the probe fetched a key set the provider pointed at another host")
	}
	if result.ErrorCategory != domain.HealthErrorInvalidConfiguration {
		t.Fatalf("expected invalid_configuration, got %s", result.ErrorCategory)
	}
}

// Only http and https are dialled. A configuration mistake, or a tampered
// ConfigMap, must not turn a health check into a file read or another
// protocol's client.
func TestProbesRefuseSchemesOutsideTheAllowlist(t *testing.T) {
	for _, endpoint := range []string{
		"file:///etc/passwd",
		"gopher://127.0.0.1:6379/_PING",
		"ftp://storage.internal/",
		"not a url at all",
		"https://",
	} {
		health := newHealth(t, &stubDatabase{}, environment{"SEAWEEDFS_FILER_URL": endpoint})
		result := serviceNamed(t, snapshotOf(t, health, false), domain.HealthServiceStorage)
		if result.State == domain.HealthHealthy {
			t.Errorf("%q was accepted as a probe target", endpoint)
		}
		if result.ErrorCategory != domain.HealthErrorInvalidConfiguration {
			t.Errorf("%q produced %s rather than invalid_configuration", endpoint, result.ErrorCategory)
		}
	}
}

// LiveKit is configured with the wss:// URL the browser uses. The probe speaks
// to the same host over https rather than needing a second variable that could
// name a different destination.
func TestLiveKitProbeNormalizesTheWebSocketEndpoint(t *testing.T) {
	livekit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer livekit.Close()

	health := newHealth(t, &stubDatabase{}, environment{
		"LIVEKIT_ENABLED": "true",
		"LIVEKIT_API_URL": "ws://" + strings.TrimPrefix(livekit.URL, "http://"),
	})
	result := serviceNamed(t, snapshotOf(t, health, false), domain.HealthServiceLiveKit)
	if result.State != domain.HealthHealthy {
		t.Fatalf("expected healthy, got %s (%s)", result.State, result.Detail)
	}
}

// A dependency that answers correctly but far too slowly is working and not
// well, which is what degraded is for.
func TestASlowButWorkingDependencyIsDegradedRatherThanHealthy(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(700 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer slow.Close()

	health := newHealth(t, &stubDatabase{}, environment{"SEAWEEDFS_FILER_URL": slow.URL})
	result := serviceNamed(t, snapshotOf(t, health, false), domain.HealthServiceStorage)

	if result.State != domain.HealthDegraded {
		t.Fatalf("expected degraded, got %s", result.State)
	}
	if result.ErrorCategory != domain.HealthErrorCapacityWarning {
		t.Fatalf("expected capacity_warning, got %s", result.ErrorCategory)
	}
	if result.LatencyMS == nil || *result.LatencyMS < 500 {
		t.Fatalf("the measured latency does not support the verdict: %v", result.LatencyMS)
	}
}

// A dependency that accepts the connection and then goes silent is the case a
// per-probe deadline exists for.
func TestAProbeAgainstASilentDependencyTimesOut(t *testing.T) {
	server := startTextServer(t, func(_ net.Conn, _ *bufio.Reader) string {
		time.Sleep(10 * time.Second)
		return ""
	})
	host, port := server.hostPort(t)
	health := newHealth(t, &stubDatabase{}, environment{"VALKEY_HOST": host, "VALKEY_PORT": port})

	started := time.Now()
	result := serviceNamed(t, snapshotOf(t, health, false), domain.HealthServiceValkey)
	if elapsed := time.Since(started); elapsed > 6*time.Second {
		t.Fatalf("the probe waited %s on a silent dependency", elapsed)
	}
	if result.State != domain.HealthUnavailable {
		t.Fatalf("expected unavailable, got %s", result.State)
	}
	if result.ErrorCategory != domain.HealthErrorConnectionTimeout {
		t.Fatalf("expected connection_timeout, got %s", result.ErrorCategory)
	}
}

// A malformed dial target must be refused rather than handed to the dialler,
// and it must be reported as a configuration fault rather than as an outage.
func TestAMalformedDialTargetIsAConfigurationFault(t *testing.T) {
	health := newHealth(t, &stubDatabase{}, environment{"FILE_MALWARE_SCANNER_ADDRESS": "clamav-without-a-port"})
	result := serviceNamed(t, snapshotOf(t, health, false), domain.HealthServiceClamAV)
	if result.ErrorCategory != domain.HealthErrorInvalidConfiguration {
		t.Fatalf("expected invalid_configuration, got %s", result.ErrorCategory)
	}
}
