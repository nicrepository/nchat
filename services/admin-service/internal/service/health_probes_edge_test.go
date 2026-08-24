package service_test

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
	"github.com/nicrepository/nchat/services/admin-service/internal/service"
)

// The failure modes each probe has to tell apart. They are separate specs
// rather than one table because the interesting part is the *distinction*: a
// dependency that is loading, one that refuses a credential and one that
// speaks a protocol this build does not understand are three different
// operator actions, and collapsing any two of them makes the Health Center
// worse than no page at all.

func valkeyAnswering(t *testing.T, reply string) domain.ServiceHealth {
	t.Helper()
	server := startTextServer(t, func(conn net.Conn, reader *bufio.Reader) string {
		_, _ = readRESPCommand(reader)
		_, _ = io.WriteString(conn, reply)
		return ""
	})
	host, port := server.hostPort(t)
	health := newHealth(t, &stubDatabase{}, environment{"VALKEY_HOST": host, "VALKEY_PORT": port})
	return serviceNamed(t, snapshotOf(t, health, false), domain.HealthServiceValkey)
}

func TestValkeyLoadingIsACapacityWarningRatherThanAnOutage(t *testing.T) {
	result := valkeyAnswering(t, "-LOADING Valkey is loading the dataset in memory\r\n")
	if result.State != domain.HealthDegraded || result.ErrorCategory != domain.HealthErrorCapacityWarning {
		t.Fatalf("expected degraded/capacity_warning, got %s/%s", result.State, result.ErrorCategory)
	}
}

func TestValkeyAnsweringSomethingUnrecognizedIsNotHealthy(t *testing.T) {
	result := valkeyAnswering(t, "$5\r\nhello\r\n")
	if result.State == domain.HealthHealthy {
		t.Fatal("an uninterpretable reply is not evidence of health")
	}
	if result.ErrorCategory != domain.HealthErrorProtocolError {
		t.Fatalf("expected protocol_error, got %s", result.ErrorCategory)
	}
}

func TestValkeyRefusingTheUnauthenticatedPingIsAnAuthenticationFailure(t *testing.T) {
	result := valkeyAnswering(t, "-NOAUTH Authentication required.\r\n")
	if result.ErrorCategory != domain.HealthErrorAuthenticationFailed {
		t.Fatalf("expected authentication_failed, got %s", result.ErrorCategory)
	}
}

func storageAnswering(t *testing.T, status int) domain.ServiceHealth {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}))
	t.Cleanup(server.Close)
	health := newHealth(t, &stubDatabase{}, environment{"SEAWEEDFS_FILER_URL": server.URL})
	return serviceNamed(t, snapshotOf(t, health, false), domain.HealthServiceStorage)
}

func TestHTTPStatusClassesAreToldApart(t *testing.T) {
	cases := map[int]struct {
		state    domain.HealthState
		category domain.HealthErrorCategory
	}{
		http.StatusOK:                  {domain.HealthHealthy, domain.HealthErrorNone},
		http.StatusUnauthorized:        {domain.HealthDegraded, domain.HealthErrorAuthenticationFailed},
		http.StatusForbidden:           {domain.HealthDegraded, domain.HealthErrorAuthenticationFailed},
		http.StatusNotFound:            {domain.HealthDegraded, domain.HealthErrorProtocolError},
		http.StatusServiceUnavailable:  {domain.HealthUnavailable, domain.HealthErrorDependencyUnavailable},
		http.StatusInternalServerError: {domain.HealthUnavailable, domain.HealthErrorDependencyUnavailable},
	}
	for status, want := range cases {
		result := storageAnswering(t, status)
		if result.State != want.state || result.ErrorCategory != want.category {
			t.Errorf("status %d: expected %s/%s, got %s/%s",
				status, want.state, want.category, result.State, result.ErrorCategory)
		}
	}
}

// TLS verification is never relaxed, so a certificate this pod does not trust
// is a tls_error and not something the check works around.
func TestAnUntrustedCertificateIsATLSErrorAndNotAWorkaround(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	health := newHealth(t, &stubDatabase{}, environment{"SEAWEEDFS_FILER_URL": server.URL})
	result := serviceNamed(t, snapshotOf(t, health, false), domain.HealthServiceStorage)

	if result.State == domain.HealthHealthy {
		t.Fatal("an unverifiable certificate was accepted")
	}
	if result.ErrorCategory != domain.HealthErrorTLSError {
		t.Fatalf("expected tls_error, got %s (%s)", result.ErrorCategory, result.Detail)
	}
}

// The same rule on the SMTP path, which has its own dialler for implicit TLS.
func TestImplicitTLSSMTPVerifiesTheCertificate(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	host, port, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "https://"))
	if err != nil {
		t.Fatalf("SplitHostPort: %v", err)
	}
	health := newHealth(t, &stubDatabase{}, environment{
		"SMTP_WORKER_ENABLED": "true", "SMTP_HOST": host, "SMTP_PORT": port, "SMTP_TLS_MODE": "tls",
	})
	result := serviceNamed(t, snapshotOf(t, health, false), domain.HealthServiceSMTP)

	if result.State == domain.HealthHealthy {
		t.Fatal("an unverifiable relay certificate was accepted")
	}
	if result.ErrorCategory != domain.HealthErrorTLSError {
		t.Fatalf("expected tls_error, got %s (%s)", result.ErrorCategory, result.Detail)
	}
}

func TestImplicitTLSSMTPRefusesAMalformedAddress(t *testing.T) {
	health := newHealth(t, &stubDatabase{}, environment{
		"SMTP_WORKER_ENABLED": "true", "SMTP_HOST": "relay", "SMTP_PORT": "", "SMTP_TLS_MODE": "tls",
	})
	result := serviceNamed(t, snapshotOf(t, health, false), domain.HealthServiceSMTP)
	// An empty port makes the whole target unobservable rather than malformed:
	// a half-resolved address is never dialled.
	if result.State != domain.HealthUnknown {
		t.Fatalf("expected unknown, got %s", result.State)
	}
}

// A discovery document this build cannot parse is not evidence of a working
// identity provider, and the body must not be echoed while saying so.
func TestOIDCDiscoveryThatIsNotJSONIsAProtocolError(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "<html><body>keycloak realm nchat-prod is temporarily down</body></html>")
	}))
	defer provider.Close()

	result := oidcHealth(t, provider.URL)
	if result.State == domain.HealthHealthy {
		t.Fatal("an unparseable discovery document was accepted")
	}
	if result.ErrorCategory != domain.HealthErrorProtocolError {
		t.Fatalf("expected protocol_error, got %s", result.ErrorCategory)
	}
	if strings.Contains(result.Detail, "nchat-prod") {
		t.Fatalf("the provider's body reached the response: %q", result.Detail)
	}
}

func TestOIDCDiscoveryThatFailsIsReportedAsSuch(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer provider.Close()

	result := oidcHealth(t, provider.URL)
	if result.State != domain.HealthUnavailable {
		t.Fatalf("expected unavailable, got %s", result.State)
	}
}

func TestOIDCWithAnUnusableIssuerIsAConfigurationFault(t *testing.T) {
	result := oidcHealth(t, "keycloak.internal")
	if result.ErrorCategory != domain.HealthErrorInvalidConfiguration {
		t.Fatalf("expected invalid_configuration, got %s", result.ErrorCategory)
	}
}

func TestClamAVAnsweringSomethingElseIsNotHealthy(t *testing.T) {
	server := startTextServer(t, func(conn net.Conn, reader *bufio.Reader) string {
		_, _ = reader.ReadString(0)
		_, _ = io.WriteString(conn, "UNKNOWN COMMAND\x00")
		return ""
	})
	health := newHealth(t, &stubDatabase{}, environment{"FILE_MALWARE_SCANNER_ADDRESS": server.address()})
	result := serviceNamed(t, snapshotOf(t, health, false), domain.HealthServiceClamAV)

	if result.State == domain.HealthHealthy {
		t.Fatal("a daemon that did not answer PONG was reported healthy")
	}
	if result.ErrorCategory != domain.HealthErrorProtocolError {
		t.Fatalf("expected protocol_error, got %s", result.ErrorCategory)
	}
}

// A daemon that pings and then refuses VERSION is still answering: the version
// is extra information, so failing to read it must degrade nothing.
func TestClamAVWithoutAVersionIsStillHealthy(t *testing.T) {
	server := startTextServer(t, func(conn net.Conn, reader *bufio.Reader) string {
		_, _ = reader.ReadString(0)
		_, _ = io.WriteString(conn, "PONG\x00")
		_ = conn.Close()
		return ""
	})
	health := newHealth(t, &stubDatabase{}, environment{"FILE_MALWARE_SCANNER_ADDRESS": server.address()})
	result := serviceNamed(t, snapshotOf(t, health, false), domain.HealthServiceClamAV)

	if result.State != domain.HealthHealthy {
		t.Fatalf("expected healthy, got %s (%s)", result.State, result.Detail)
	}
	if result.Version != "" {
		t.Fatalf("a version was reported that the daemon never sent: %q", result.Version)
	}
}

// A daemon that answers with a wall of text must not put a wall of text on an
// operator's screen.
func TestAnOversizedVersionIsTruncated(t *testing.T) {
	server := startTextServer(t, func(conn net.Conn, reader *bufio.Reader) string {
		for i := 0; i < 2; i++ {
			command, err := reader.ReadString(0)
			if err != nil {
				return ""
			}
			if strings.Contains(command, "PING") {
				_, _ = io.WriteString(conn, "PONG\x00")
				continue
			}
			_, _ = io.WriteString(conn, strings.Repeat("ClamAV ", 200)+"\x00")
		}
		return ""
	})
	health := newHealth(t, &stubDatabase{}, environment{"FILE_MALWARE_SCANNER_ADDRESS": server.address()})
	result := serviceNamed(t, snapshotOf(t, health, false), domain.HealthServiceClamAV)

	if len(result.Version) > 48 {
		t.Fatalf("the version was not truncated: %d characters", len(result.Version))
	}
}

// The collectors are the whole instrumentation contract, and a nil metrics
// holder must behave exactly like a wired one so instrumentation is never the
// reason a health check differs.
func TestHealthMetricsExposeTheirCollectorsAndToleratebeingAbsent(t *testing.T) {
	metrics := service.NewHealthMetrics()
	if len(metrics.Collectors()) != 4 {
		t.Fatalf("expected four collectors, got %d", len(metrics.Collectors()))
	}
	var absent *service.HealthMetrics
	if absent.Collectors() != nil {
		t.Fatal("a nil metrics holder must expose no collectors")
	}
	// A service built without metrics must still collect.
	health := service.NewHealthServiceWithEnv(&stubDatabase{}, nil, environment{}.lookup, nil)
	if snapshot := snapshotOf(t, health, false); len(snapshot.Services) == 0 {
		t.Fatal("a service without metrics collected nothing")
	}
}
