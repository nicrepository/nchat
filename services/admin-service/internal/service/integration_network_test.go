package service_test

import (
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
	"github.com/nicrepository/nchat/services/admin-service/internal/service"
)

// The address policy is the security decision of issue #582, so it is stated as
// a table rather than inferred from a few diagnostics that happened to pass.
//
// The rule it encodes: link-local, multicast, unspecified and broadcast are
// refused for every integration without an opt-out, because that is where the
// cloud metadata endpoints live and no NChat dependency has ever been there.
// Private and loopback are refused only when an integration says so — every
// current one is a cluster service and says the opposite.
var (
	permissivePolicy = domain.IntegrationNetworkPolicy{AllowPrivate: true}
	strictPolicy     = domain.IntegrationNetworkPolicy{}
)

func TestAllowedAddressRefusesTheRangesNoDependencyOccupies(t *testing.T) {
	refused := []string{
		// AWS and Azure instance metadata, and the address
		// metadata.google.internal resolves to.
		"169.254.169.254",
		"169.254.170.2",
		"fe80::1",
		"ff02::1",
		"224.0.0.1",
		"0.0.0.0",
		"::",
		"255.255.255.255",
	}
	for _, address := range refused {
		ip := net.ParseIP(address)
		if service.AllowedAddressForTest(permissivePolicy, ip) {
			t.Fatalf("%s must be refused even by a policy that allows private addresses", address)
		}
		if service.AllowedAddressForTest(strictPolicy, ip) {
			t.Fatalf("%s must be refused by the strict policy", address)
		}
	}
	if service.AllowedAddressForTest(permissivePolicy, nil) {
		t.Fatal("an unparseable address must be refused")
	}
}

// Private and loopback are where every NChat dependency lives, so a blanket
// refusal would break the real deployment. The policy decides, per integration.
func TestAllowedAddressPermitsClusterRangesOnlyWhenThePolicySaysSo(t *testing.T) {
	for _, address := range []string{"10.42.0.7", "192.168.1.10", "172.16.5.4", "127.0.0.1", "::1", "fd00::1"} {
		ip := net.ParseIP(address)
		if !service.AllowedAddressForTest(permissivePolicy, ip) {
			t.Fatalf("%s is where NChat dependencies live and must be allowed", address)
		}
		if service.AllowedAddressForTest(strictPolicy, ip) {
			t.Fatalf("%s must be refused by a policy that does not allow private addresses", address)
		}
	}
}

func TestAllowedAddressPermitsPublicAddressesUnderEveryPolicy(t *testing.T) {
	for _, address := range []string{"93.184.216.34", "2606:2800:220:1:248:1893:25c8:1946"} {
		ip := net.ParseIP(address)
		if !service.AllowedAddressForTest(permissivePolicy, ip) || !service.AllowedAddressForTest(strictPolicy, ip) {
			t.Fatalf("%s is a public address and must be allowed by both policies", address)
		}
	}
}

// The refusal must happen at the socket and not only in a parser: that is the
// layer with no window between the check and connect(2), which is what closes
// the DNS rebinding race.
func TestGuardedDialerRefusesAtTheSocket(t *testing.T) {
	policy := domain.IntegrationNetworkPolicy{AllowPrivate: true}
	err := service.GuardedDialForTest(policy, "169.254.169.254:80")
	if err == nil {
		t.Fatal("the dialer must refuse a metadata address")
	}
	if strings.Contains(err.Error(), "169.254.169.254") && !strings.Contains(err.Error(), "policy") {
		t.Fatalf("the refusal must name the policy rather than only the address: %v", err)
	}
}

// A loopback dial has to actually reach the guard's allow branch, so the
// permissive policy is exercised against a real listener rather than asserted
// only through the table above.
func TestGuardedDialerAllowsALoopbackDependency(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = listener.Close() }()
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			_ = conn.Close()
		}
	}()

	if err := service.GuardedDialForTest(domain.IntegrationNetworkPolicy{AllowPrivate: true}, listener.Addr().String()); err != nil {
		t.Fatalf("a cluster dependency on loopback must be reachable: %v", err)
	}
	if err := service.GuardedDialForTest(domain.IntegrationNetworkPolicy{}, listener.Addr().String()); err == nil {
		t.Fatal("a policy that refuses private addresses must refuse loopback")
	}
}

func TestParseIntegrationURLAppliesTheSchemeAllowlist(t *testing.T) {
	policy := domain.IntegrationNetworkPolicy{Schemes: []string{"http", "https"}, AllowPrivate: true}

	refused := []string{
		"file:///etc/passwd",
		"gopher://internal:70/",
		"ftp://internal/",
		"jar:http://internal/!/",
		"https:///no-host",
		"",
		"   ",
		// A URL with an embedded credential is refused rather than stripped: it
		// is a configuration mistake that would otherwise put a password one log
		// line away from disclosure.
		"https://user:secret@keycloak.internal/realms/nchat",
	}
	for _, raw := range refused {
		if _, _, _, err := service.ParseIntegrationURLForTest(policy, raw); err == nil {
			t.Fatalf("%q must be refused", raw)
		} else if !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("%q must be refused as invalid input, got %v", raw, err)
		}
	}

	cases := map[string]struct{ scheme, host, port string }{
		"https://keycloak.internal/realms/nchat": {"https", "keycloak.internal", "443"},
		"http://seaweedfs-filer:8888":            {"http", "seaweedfs-filer", "8888"},
		// LiveKit is configured with the URL a browser connects to; its
		// administrative surface is the same host over HTTP.
		"wss://livekit.example.test":  {"https", "livekit.example.test", "443"},
		"ws://livekit.internal:7880":  {"http", "livekit.internal", "7880"},
		"https://[fd00::1]:8443/base": {"https", "fd00::1", "8443"},
	}
	for raw, want := range cases {
		scheme, host, port, err := service.ParseIntegrationURLForTest(policy, raw)
		if err != nil {
			t.Fatalf("%q must parse: %v", raw, err)
		}
		if scheme != want.scheme || host != want.host || port != want.port {
			t.Fatalf("%q parsed as %s/%s/%s, expected %s/%s/%s", raw, scheme, host, port, want.scheme, want.host, want.port)
		}
	}
}

func TestParseIntegrationAddressRefusesAnythingButHostPort(t *testing.T) {
	for _, raw := range []string{
		"", "clamav", "clamav:", ":3310",
		"http://clamav:3310", "clamav:3310/scan", "user@clamav:3310", "clamav 3310",
	} {
		if _, err := service.ParseIntegrationAddressForTest(raw); err == nil {
			t.Fatalf("%q must be refused", raw)
		}
	}
	for raw, want := range map[string]string{
		"clamav:3310":    "clamav:3310",
		"  smtp:587  ":   "smtp:587",
		"[fd00::1]:3310": "[fd00::1]:3310",
		"10.42.0.7:3310": "10.42.0.7:3310",
	} {
		address, err := service.ParseIntegrationAddressForTest(raw)
		if err != nil {
			t.Fatalf("%q must parse: %v", raw, err)
		}
		if address != want {
			t.Fatalf("%q parsed as %q, expected %q", raw, address, want)
		}
	}
}
