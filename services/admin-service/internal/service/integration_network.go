package service

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
)

// The network policy of the active diagnostics (issue #582).
//
// The diagnostics of this issue are the only place in admin-service where an
// operator's click causes an outbound connection, so this file is where the
// answer to "what can that click reach" lives. It has three layers, and each
// one covers something the others cannot:
//
//  1. the target comes from this pod's environment. No handler, body, header or
//     query parameter reaches a dial address — the same rule the health probes
//     of issue #581 follow, and the reason there is no "test this URL" field
//     anywhere in the console;
//  2. the scheme is checked against the integration's own policy before a URL
//     is used at all, so a ConfigMap holding file:// or gopher:// produces a
//     configuration error rather than a request;
//  3. the *resolved address the kernel is about to connect to* is checked, in
//     Dialer.Control. That is the layer that matters most, because it is the
//     only one with no gap between the check and the connection: a name that
//     resolved to an acceptable address a moment ago, and to 169.254.169.254 by
//     the time the socket is opened, is refused at the socket. DNS rebinding
//     and every other resolve-then-connect race die here rather than being
//     argued about.
//
// What is deliberately *not* refused: private and loopback addresses. Every
// NChat dependency is a cluster service — Keycloak, the SMTP relay, LiveKit,
// clamd, SeaweedFS — so a blanket "no RFC 1918" rule would block the real
// deployment while doing nothing about the address range that actually matters.
// That range is link-local, and it is refused for every integration without an
// opt-out, because it is where the cloud metadata endpoints live and no NChat
// dependency has ever been there.

// errBlockedAddress is what the dial control returns for a refused address.
//
// It is its own error so a classifier can tell "the policy said no" apart from
// "the network said no" without matching on text, and so nothing about which
// address was refused reaches a response.
var errBlockedAddress = errors.New("destination refused by integration network policy")

// dialTimeout bounds one TCP or TLS handshake inside a diagnostic.
const dialTimeout = 3 * time.Second

// diagnosticHTTPTimeout bounds one HTTP exchange inside a diagnostic.
const diagnosticHTTPTimeout = 5 * time.Second

// maxDiagnosticBodyBytes bounds the one kind of response body a diagnostic
// reads: a discovery document. Nothing else is read at all, and no body — read
// or not — ever reaches a client.
const maxDiagnosticBodyBytes = 64 << 10

// allowedAddress reports whether the policy permits connecting to this IP.
//
// The ranges refused unconditionally are the ones no NChat dependency can
// legitimately occupy:
//
//   - link-local (169.254.0.0/16, fe80::/10). This is the cloud metadata range:
//     169.254.169.254 on AWS and Azure, and what metadata.google.internal
//     resolves to on GCP. Refusing the range rather than the three addresses is
//     what makes the rule hold for a provider this build has never heard of;
//   - the unspecified address, multicast and broadcast, none of which is a
//     dependency and all of which are ways to make a connection go somewhere
//     other than where it reads as going.
//
// Loopback and private ranges are refused only when the integration's policy
// says so, because for this platform they are where the dependencies live.
func allowedAddress(policy domain.IntegrationNetworkPolicy, ip net.IP) bool {
	switch {
	case ip == nil, ip.IsUnspecified(), ip.IsMulticast(), ip.IsInterfaceLocalMulticast():
		return false
	case ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast():
		return false
	case ip.Equal(net.IPv4bcast):
		return false
	case policy.AllowPrivate:
		return true
	default:
		return !ip.IsLoopback() && !ip.IsPrivate()
	}
}

// newGuardedDialer builds the dialer every diagnostic connection goes through.
//
// Control is called with the address the kernel is about to connect to, after
// resolution and immediately before connect(2). Checking there — rather than
// resolving the name ourselves and hoping the connection uses the same answer —
// is what closes the TOCTOU window that DNS rebinding depends on.
func newGuardedDialer(policy domain.IntegrationNetworkPolicy) *net.Dialer {
	return &net.Dialer{
		Timeout: dialTimeout,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return errBlockedAddress
			}
			if !allowedAddress(policy, net.ParseIP(host)) {
				return errBlockedAddress
			}
			return nil
		},
	}
}

// newDiagnosticHTTPClient builds the one HTTP client the diagnostics use.
//
// Three settings carry the weight:
//
//   - DialContext is the guarded dialer above, so every connection the
//     transport opens — including the one for a request this code did not write
//     by hand — is checked at the socket;
//   - CheckRedirect refuses every redirect. A dependency that answers 302 does
//     not get to nominate a second address for this pod to connect to, which is
//     the same arbitrary-target problem the policy exists to prevent;
//   - the TLS configuration is Go's default. There is no InsecureSkipVerify in
//     this package and no setting that could introduce one: a certificate this
//     pod does not trust is reported as tls_error, which is the honest answer
//     and the one that gets fixed.
func newDiagnosticHTTPClient(policy domain.IntegrationNetworkPolicy) *http.Client {
	dialer := newGuardedDialer(policy)
	return &http.Client{
		Timeout: diagnosticHTTPTimeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: &http.Transport{
			DialContext:           dialer.DialContext,
			DisableKeepAlives:     true,
			TLSHandshakeTimeout:   dialTimeout,
			ResponseHeaderTimeout: diagnosticHTTPTimeout,
		},
	}
}

// diagnosticTarget is one resolved destination.
//
// Produced only from this pod's environment. The struct carries a host and a
// port rather than a URL string so the staged dial and the protocol client
// agree on what is being contacted, and so nothing has to re-parse a URL to
// find out where to connect.
type diagnosticTarget struct {
	scheme string
	host   string
	port   string
	// endpoint is the full URL for an HTTP-shaped target, empty otherwise.
	endpoint string
	// secure reports whether the transport is TLS from the first byte.
	secure bool
}

func (t diagnosticTarget) address() string {
	return net.JoinHostPort(t.host, t.port)
}

// defaultPorts is the port a scheme implies when the URL does not name one.
var defaultPorts = map[string]string{"http": "80", "https": "443"}

// parseIntegrationURL turns a configured URL into a target, under the
// integration's own policy.
//
// The ws and wss schemes are normalized to http and https rather than accepted
// as themselves: LiveKit is configured with the URL a browser connects to, and
// its administrative surface is the same host over HTTP. Rewriting keeps one
// configured target per dependency, which is what makes the target unambiguous.
func parseIntegrationURL(policy domain.IntegrationNetworkPolicy, raw string) (diagnosticTarget, error) {
	parsed, err := url.Parse(normalizeWebSocketURL(raw))
	if err != nil {
		return diagnosticTarget{}, fmt.Errorf("%w: unusable endpoint", domain.ErrInvalidInput)
	}
	if !policy.AllowsScheme(parsed.Scheme) || parsed.Host == "" {
		return diagnosticTarget{}, fmt.Errorf("%w: unsupported endpoint", domain.ErrInvalidInput)
	}
	// A URL carrying credentials is refused rather than stripped: it is a
	// configuration mistake that would otherwise put a password one log line
	// away from disclosure, and silently accepting it hides the mistake.
	if parsed.User != nil {
		return diagnosticTarget{}, fmt.Errorf("%w: endpoint carries credentials", domain.ErrInvalidInput)
	}
	port := parsed.Port()
	if port == "" {
		port = defaultPorts[parsed.Scheme]
	}
	return diagnosticTarget{
		scheme:   parsed.Scheme,
		host:     parsed.Hostname(),
		port:     port,
		endpoint: parsed.String(),
		secure:   parsed.Scheme == "https",
	}, nil
}

// parseIntegrationAddress turns a configured host:port into a target.
//
// Used by the integrations that are not HTTP. A value carrying a scheme, a path
// or an at-sign is refused rather than coerced, because every one of those is a
// sign the variable holds something other than an address.
func parseIntegrationAddress(raw string, secure bool) (diagnosticTarget, error) {
	trimmed := strings.TrimSpace(raw)
	if strings.ContainsAny(trimmed, "/@ ") {
		return diagnosticTarget{}, fmt.Errorf("%w: address is not host:port", domain.ErrInvalidInput)
	}
	host, port, err := net.SplitHostPort(trimmed)
	if err != nil || host == "" || port == "" {
		return diagnosticTarget{}, fmt.Errorf("%w: address is not host:port", domain.ErrInvalidInput)
	}
	return diagnosticTarget{host: host, port: port, secure: secure}, nil
}

// classifyDialError maps a connection failure onto the closed category set of
// issue #581, reusing its vocabulary rather than inventing a second one.
//
// A policy refusal is reported as an invalid configuration and not as an
// unreachable dependency, because that is what it is: the deployment named an
// address this platform will not contact, and telling an operator "it did not
// answer" would send them to debug a network that is behaving correctly.
func classifyDialError(err error) (domain.HealthErrorCategory, string) {
	if errors.Is(err, errBlockedAddress) {
		return domain.HealthErrorInvalidConfiguration,
			"O endereço resolvido está em uma faixa que a plataforma não contata (link-local ou metadata de nuvem)."
	}
	return classifyNetworkError(err)
}
