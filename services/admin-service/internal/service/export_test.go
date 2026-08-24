package service

import (
	"net"

	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
)

// Test-only handles on the internals of the integration diagnostics.
//
// The address policy and the target parsers are the security decisions of issue
// #582, and they are decided per input rather than per run: a table test over a
// hundred addresses is the only way to state what the rule actually is, and
// driving each one through a full diagnostic would be slower, less readable and
// would prove less. Nothing here is reachable outside a test binary.

// AllowedAddressForTest reports whether the policy permits dialling this IP.
func AllowedAddressForTest(policy domain.IntegrationNetworkPolicy, ip net.IP) bool {
	return allowedAddress(policy, ip)
}

// ParseIntegrationURLForTest resolves a configured URL under a policy.
func ParseIntegrationURLForTest(policy domain.IntegrationNetworkPolicy, raw string) (string, string, string, error) {
	target, err := parseIntegrationURL(policy, raw)
	return target.scheme, target.host, target.port, err
}

// ParseIntegrationAddressForTest resolves a configured host:port.
func ParseIntegrationAddressForTest(raw string) (string, error) {
	target, err := parseIntegrationAddress(raw, false)
	if err != nil {
		return "", err
	}
	return target.address(), nil
}

// GuardedDialForTest attempts one connection under a policy, so a spec can
// assert that the refusal happens at the socket and not merely in a parser.
func GuardedDialForTest(policy domain.IntegrationNetworkPolicy, address string) error {
	conn, err := newGuardedDialer(policy).Dial("tcp", address)
	if conn != nil {
		_ = conn.Close()
	}
	return err
}

// ReadinessFromStatusForTest exposes the rule that judges an HTTP answer
// without relaying it.
func ReadinessFromStatusForTest(status int) (string, string) {
	diagnostic, category, _ := readinessFromStatus(status)
	return string(diagnostic), string(category)
}

// ValidateTestRecipientForTest exposes the recipient guard of the SMTP test
// message.
func ValidateTestRecipientForTest(address string) (string, error) {
	return validateTestRecipient(address)
}

// SignLiveKitDiagnosticTokenForTest exposes the diagnostic token minting so a
// spec can assert the grant is minimal and the lifetime short.
var SignLiveKitDiagnosticTokenForTest = signLiveKitDiagnosticToken

// DiagnosticTokenTTLForTest is the lifetime of that token.
const DiagnosticTokenTTLForTest = diagnosticTokenTTL
