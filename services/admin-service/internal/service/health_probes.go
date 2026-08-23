package service

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
)

// Integration probes for the Health Center (issue #581).
//
// Every function in this file has the same two properties, and they are the
// security design of the whole feature:
//
//   - the target is an argument that was resolved from this pod's environment
//     by resolveTarget. No probe parses a request, reads a header or accepts a
//     hostname from anywhere else, so there is no reachable path from a client
//     to a dial address. The Admin API cannot be used to make this pod connect
//     to something an administrator chose;
//   - nothing a dependency says comes back out. Responses are drained and
//     discarded, or read under a byte cap and reduced to a handful of known
//     fields; errors are classified into domain's closed category set and the
//     original text is dropped. A caller learns "connection_timeout", never a
//     driver's message, an internal hostname or a stack trace.
//
// TLS verification is never relaxed. There is no InsecureSkipVerify in this
// package and no configuration that could introduce one: a dependency with a
// certificate this pod does not trust is reported as tls_error, which is the
// honest answer and the one that gets fixed.

// probeOutcome is what one probe learned. It is deliberately not an error:
// a failed check is a *result*, and modelling it as an error is how a single
// unreachable dependency turns into a 500 for the whole page.
type probeOutcome struct {
	State    domain.HealthState
	Category domain.HealthErrorCategory
	Detail   string
	// Version is optional and already sanitized when set.
	Version string
}

func healthy() probeOutcome {
	return probeOutcome{State: domain.HealthHealthy}
}

func failed(category domain.HealthErrorCategory, detail string) probeOutcome {
	return probeOutcome{State: domain.HealthUnavailable, Category: category, Detail: detail}
}

func degraded(category domain.HealthErrorCategory, detail string) probeOutcome {
	return probeOutcome{State: domain.HealthDegraded, Category: category, Detail: detail}
}

// probeTimeout bounds one check.
//
// Short, per check, and not configurable from a request. It is deliberately
// far below any request timeout: the point of the Health Center is to report
// that a dependency is stuck, which it cannot do if being stuck is contagious.
const probeTimeout = 3 * time.Second

// maxDiscoveryBytes bounds the one response body this package reads.
//
// A provider that answers with megabytes of JSON is either broken or not the
// provider, and either way the client must not read it into memory to find
// that out.
const maxDiscoveryBytes = 64 << 10

// classifyNetworkError maps a transport failure onto the closed category set.
//
// This is where a library's error text stops. The mapping looks at the error's
// *type* — a timeout, a TLS verification failure, a refused connection —
// rather than at its message, so it cannot be steered by what a remote host
// puts in a response.
func classifyNetworkError(err error) (domain.HealthErrorCategory, string) {
	switch {
	case err == nil:
		return domain.HealthErrorNone, ""
	case errors.Is(err, context.DeadlineExceeded), isTimeout(err):
		return domain.HealthErrorConnectionTimeout, "A dependência não respondeu dentro do tempo limite do check."
	case errors.Is(err, context.Canceled):
		return domain.HealthErrorConnectionTimeout, "A verificação foi cancelada antes de concluir."
	case isTLSError(err):
		return domain.HealthErrorTLSError, "Não foi possível estabelecer TLS com a dependência."
	default:
		return domain.HealthErrorDependencyUnavailable, "A dependência não aceitou a conexão."
	}
}

func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func isTLSError(err error) bool {
	var verification *tls.CertificateVerificationError
	if errors.As(err, &verification) {
		return true
	}
	var alert tls.RecordHeaderError
	return errors.As(err, &alert)
}

// newProbeHTTPClient builds the one HTTP client this package uses.
//
// Two settings carry weight:
//
//   - CheckRedirect refuses every redirect. A dependency that answers 302 is
//     reported as it is; it does not get to choose a second address for this
//     pod to connect to, which would reintroduce exactly the arbitrary-target
//     problem the resolver exists to prevent;
//   - the transport keeps Go's default TLS configuration. Nothing here sets
//     InsecureSkipVerify, a custom RootCAs or a MinVersion downgrade.
func newProbeHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// probeHTTPEndpoint issues one GET and judges the status code.
//
// The body is drained and discarded without being read into anything: this
// probe answers "does it respond", and the content of the answer is not
// information the Admin API is willing to relay.
func probeHTTPEndpoint(ctx context.Context, client *http.Client, endpoint string) probeOutcome {
	response, outcome, ok := doProbeRequest(ctx, client, endpoint)
	if !ok {
		return outcome
	}
	defer drainAndClose(response)
	return classifyHTTPStatus(response.StatusCode)
}

// doProbeRequest performs the request and returns either a live response or
// the outcome that replaces it.
func doProbeRequest(ctx context.Context, client *http.Client, endpoint string) (*http.Response, probeOutcome, bool) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, failed(domain.HealthErrorInvalidConfiguration, "O endpoint configurado não é utilizável."), false
	}
	response, err := client.Do(request)
	if err != nil {
		category, detail := classifyNetworkError(err)
		return nil, failed(category, detail), false
	}
	return response, probeOutcome{}, true
}

// classifyHTTPStatus turns a status code into a state.
//
// A redirect is degraded rather than healthy: the probe deliberately does not
// follow it, so the platform has confirmed reachability and nothing else, and
// saying "healthy" would be claiming a check that did not happen.
func classifyHTTPStatus(status int) probeOutcome {
	switch {
	case status >= 500:
		return failed(domain.HealthErrorDependencyUnavailable, "A dependência respondeu com erro interno.")
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return degraded(domain.HealthErrorAuthenticationFailed, "A dependência respondeu, mas recusou a credencial deste serviço.")
	case status >= 300 && status < 400:
		return degraded(domain.HealthErrorProtocolError, "A dependência respondeu com um redirecionamento, que o check não segue.")
	case status >= 400:
		return degraded(domain.HealthErrorProtocolError, "A dependência respondeu, mas não da forma esperada para este check.")
	default:
		return healthy()
	}
}

func drainAndClose(response *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxDiscoveryBytes))
	_ = response.Body.Close()
}

// oidcDiscovery is the subset of an OpenID discovery document this service
// reads. Two fields, both compared and neither echoed.
type oidcDiscovery struct {
	Issuer  string `json:"issuer"`
	JWKSURI string `json:"jwks_uri"`
}

// probeOIDC checks the discovery document and the key set behind it.
//
// It never performs a login: a health check that authenticates a real user
// would be a health check that needs a credential and produces audit noise on
// every collection.
//
// The security-relevant step is the jwks_uri handling. That value comes from
// the provider's response, not from this deployment's configuration, so
// fetching it blindly would let whatever answers the issuer URL nominate the
// next address this pod connects to. It is therefore required to share the
// issuer's scheme and host; anything else is reported as a configuration
// problem and no second request is made.
func probeOIDC(ctx context.Context, client *http.Client, issuer string) probeOutcome {
	issuerURL, err := parseProbeURL(issuer)
	if err != nil {
		return failed(domain.HealthErrorInvalidConfiguration, "O issuer configurado não é uma URL http(s) utilizável.")
	}
	discovery, outcome, ok := fetchDiscovery(ctx, client, strings.TrimSuffix(issuerURL.String(), "/")+"/.well-known/openid-configuration")
	if !ok {
		return outcome
	}
	if strings.TrimSuffix(discovery.Issuer, "/") != strings.TrimSuffix(issuerURL.String(), "/") {
		return degraded(domain.HealthErrorInvalidConfiguration,
			"O provedor respondeu com um issuer diferente do configurado; tokens emitidos por ele serão recusados.")
	}
	jwks, err := parseProbeURL(discovery.JWKSURI)
	if err != nil || jwks.Scheme != issuerURL.Scheme || jwks.Host != issuerURL.Host {
		return degraded(domain.HealthErrorInvalidConfiguration,
			"O provedor apontou o conjunto de chaves para fora da própria origem; o check não segue esse endereço.")
	}
	return probeHTTPEndpoint(ctx, client, jwks.String())
}

// fetchDiscovery reads the discovery document under a byte cap.
func fetchDiscovery(ctx context.Context, client *http.Client, endpoint string) (oidcDiscovery, probeOutcome, bool) {
	response, outcome, ok := doProbeRequest(ctx, client, endpoint)
	if !ok {
		return oidcDiscovery{}, outcome, false
	}
	defer drainAndClose(response)
	if status := classifyHTTPStatus(response.StatusCode); status.State != domain.HealthHealthy {
		return oidcDiscovery{}, status, false
	}
	var discovery oidcDiscovery
	if err := json.NewDecoder(io.LimitReader(response.Body, maxDiscoveryBytes)).Decode(&discovery); err != nil {
		return oidcDiscovery{}, degraded(domain.HealthErrorProtocolError,
			"O provedor respondeu, mas o documento de discovery não pôde ser interpretado."), false
	}
	return discovery, probeOutcome{}, true
}

// parseProbeURL accepts only the two schemes this service is willing to dial.
//
// The allowlist is what stops a configuration mistake — or a compromised
// ConfigMap — from turning a health check into a file:// read or a gopher://
// request. It is applied to every URL this package touches, including the one
// a provider nominates.
func parseProbeURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("unsupported scheme")
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("no host")
	}
	return parsed, nil
}

// normalizeWebSocketURL rewrites a ws(s) endpoint onto its http(s) equivalent.
//
// LiveKit is configured with a wss:// URL because that is what browsers
// connect to; its health surface is the same host over https. Rewriting the
// scheme rather than declaring a second variable keeps one configured target
// for one dependency, which is the property that makes the target
// unambiguous.
func normalizeWebSocketURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	switch {
	case strings.HasPrefix(trimmed, "wss://"):
		return "https://" + strings.TrimPrefix(trimmed, "wss://")
	case strings.HasPrefix(trimmed, "ws://"):
		return "http://" + strings.TrimPrefix(trimmed, "ws://")
	default:
		return trimmed
	}
}

// dialProbe opens a bounded TCP connection to a host:port target.
//
// The target never carries a scheme, a path or credentials — resolveTarget
// produces it from two environment variables — and the deadline covers the
// whole exchange, so a dependency that accepts the connection and then goes
// silent cannot hold the collection open.
func dialProbe(ctx context.Context, target string) (net.Conn, probeOutcome, bool) {
	if _, _, err := net.SplitHostPort(target); err != nil {
		return nil, failed(domain.HealthErrorInvalidConfiguration, "O endereço configurado não está no formato host:porta."), false
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", target)
	if err != nil {
		category, detail := classifyNetworkError(err)
		return nil, failed(category, detail), false
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	return conn, probeOutcome{}, true
}

// probeValkey speaks one PING.
//
// RESP arrays rather than the older inline form, because AUTH carries a
// password: an inline command would break on any password containing a space
// and, worse, would break *by sending part of the secret as a second
// argument*. The password is read from the environment, written to the socket
// and never stored, logged, wrapped in an error or returned.
//
// Writing 40 lines of protocol instead of importing a client is deliberate: a
// PING needs no connection pool, no pipelining and no cluster topology, and
// the client library brings a lifecycle this service would then have to own.
func probeValkey(ctx context.Context, target, password string) probeOutcome {
	conn, outcome, ok := dialProbe(ctx, target)
	if !ok {
		return outcome
	}
	defer func() { _ = conn.Close() }()

	reader := bufio.NewReader(io.LimitReader(conn, maxDiscoveryBytes))
	if password != "" {
		if outcome, ok := valkeyCommand(conn, reader, "AUTH", password); !ok {
			return outcome
		}
	}
	reply, err := valkeyExchange(conn, reader, "PING")
	if err != nil {
		category, detail := classifyNetworkError(err)
		return failed(category, detail)
	}
	if strings.HasPrefix(reply, "+PONG") {
		return healthy()
	}
	return classifyValkeyError(reply)
}

// valkeyCommand runs one command whose only interesting outcome is failure.
func valkeyCommand(conn net.Conn, reader *bufio.Reader, name, argument string) (probeOutcome, bool) {
	reply, err := valkeyExchange(conn, reader, name, argument)
	if err != nil {
		category, detail := classifyNetworkError(err)
		return failed(category, detail), false
	}
	if strings.HasPrefix(reply, "-") {
		return classifyValkeyError(reply), false
	}
	return probeOutcome{}, true
}

// classifyValkeyError maps a RESP error onto the closed category set.
//
// It matches on the error's RESP prefix, which is a protocol constant, and
// never puts the server's message into the response.
func classifyValkeyError(reply string) probeOutcome {
	switch {
	case strings.HasPrefix(reply, "-NOAUTH"), strings.HasPrefix(reply, "-WRONGPASS"), strings.HasPrefix(reply, "-ERR invalid password"):
		return degraded(domain.HealthErrorAuthenticationFailed,
			"O Valkey respondeu, mas recusou a credencial que este serviço observa.")
	case strings.HasPrefix(reply, "-LOADING"):
		return degraded(domain.HealthErrorCapacityWarning, "O Valkey está carregando os dados em memória e ainda não atende.")
	default:
		return degraded(domain.HealthErrorProtocolError, "O Valkey respondeu algo que este check não interpreta.")
	}
}

// valkeyExchange writes one RESP array command and reads one reply line.
func valkeyExchange(conn net.Conn, reader *bufio.Reader, parts ...string) (string, error) {
	var command strings.Builder
	fmt.Fprintf(&command, "*%d\r\n", len(parts))
	for _, part := range parts {
		fmt.Fprintf(&command, "$%d\r\n%s\r\n", len(part), part)
	}
	if _, err := io.WriteString(conn, command.String()); err != nil {
		return "", err
	}
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// clamdVersionPattern is what a version string is allowed to look like before
// it may be shown to an operator.
//
// clamd's VERSION reply is remote input. Even though it is benign in practice,
// it is filtered rather than trusted: only this alphabet survives, and the
// result is truncated, so nothing a daemon returns can reach the console as
// markup, as a control character or as a paragraph.
var clamdVersionPattern = regexp.MustCompile(`[^A-Za-z0-9 ._/:+-]+`)

const maxVersionLength = 48

func sanitizeVersion(raw string) string {
	cleaned := strings.TrimSpace(clamdVersionPattern.ReplaceAllString(raw, ""))
	if len(cleaned) > maxVersionLength {
		return cleaned[:maxVersionLength]
	}
	return cleaned
}

// probeClamAV pings the antimalware daemon and reads its version.
//
// It sends PING and VERSION and nothing else. In particular it never sends an
// EICAR sample: writing a known-malicious pattern to the daemon on a schedule
// would put a signature hit into the security log of every deployment, every
// few seconds, and teach operators to ignore exactly the log line that matters.
func probeClamAV(ctx context.Context, target string) probeOutcome {
	conn, outcome, ok := dialProbe(ctx, target)
	if !ok {
		return outcome
	}
	defer func() { _ = conn.Close() }()

	reader := bufio.NewReader(io.LimitReader(conn, maxDiscoveryBytes))
	reply, err := clamdCommand(conn, reader, "zPING\x00")
	if err != nil {
		category, detail := classifyNetworkError(err)
		return failed(category, detail)
	}
	if !strings.HasPrefix(reply, "PONG") {
		return degraded(domain.HealthErrorProtocolError, "O antimalware respondeu algo que este check não interpreta.")
	}
	result := healthy()
	// A daemon that answers PING and then refuses VERSION is still answering:
	// the version is extra information, so failing to read it degrades nothing.
	if version, err := clamdCommand(conn, reader, "zVERSION\x00"); err == nil {
		result.Version = sanitizeVersion(version)
	}
	return result
}

// clamdCommand writes one null-terminated command and reads its reply.
func clamdCommand(conn net.Conn, reader *bufio.Reader, command string) (string, error) {
	if _, err := io.WriteString(conn, command); err != nil {
		return "", err
	}
	reply, err := reader.ReadString(0)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimRight(reply, "\x00\r\n"), nil
}

// probeSMTP opens a connection and reads the greeting.
//
// It sends no message, authenticates nobody and issues no command beyond QUIT.
// A periodic health check that delivered real mail would put a message in
// somebody's inbox every few seconds and would spend the relay's reputation to
// learn something a TCP handshake already tells us; sending a test message
// stays an explicit, operator-initiated action.
//
// The TLS mode is read from the same configuration notification-service uses.
// Implicit TLS dials with verification on; nothing here downgrades it, and
// STARTTLS is deliberately not negotiated — probing the plaintext greeting is
// all this check claims to do, and claiming more would be reporting a
// handshake that never happened.
func probeSMTP(ctx context.Context, target, tlsMode string) probeOutcome {
	conn, outcome, ok := smtpConnection(ctx, target, tlsMode)
	if !ok {
		return outcome
	}
	defer func() { _ = conn.Close() }()

	reader := bufio.NewReader(io.LimitReader(conn, maxDiscoveryBytes))
	greeting, err := reader.ReadString('\n')
	if err != nil {
		category, detail := classifyNetworkError(err)
		return failed(category, detail)
	}
	_, _ = io.WriteString(conn, "QUIT\r\n")
	if !strings.HasPrefix(greeting, "220") {
		return degraded(domain.HealthErrorProtocolError, "O relay aceitou a conexão mas não se apresentou como um servidor SMTP pronto.")
	}
	return healthy()
}

// smtpConnection dials the relay, wrapping the connection in TLS when the
// deployment says the port speaks it directly.
func smtpConnection(ctx context.Context, target, tlsMode string) (net.Conn, probeOutcome, bool) {
	if !strings.EqualFold(strings.TrimSpace(tlsMode), "tls") {
		return dialProbe(ctx, target)
	}
	host, _, err := net.SplitHostPort(target)
	if err != nil {
		return nil, failed(domain.HealthErrorInvalidConfiguration, "O endereço configurado não está no formato host:porta."), false
	}
	dialer := tls.Dialer{Config: &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}}
	conn, err := dialer.DialContext(ctx, "tcp", target)
	if err != nil {
		category, detail := classifyNetworkError(err)
		return nil, failed(category, detail), false
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	return conn, probeOutcome{}, true
}
