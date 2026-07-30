package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// ErrBootstrapMisconfigured reports a bootstrap configuration the service
// refuses to start with.
//
// It is deliberately a startup failure rather than a runtime refusal. The
// bootstrap credential is a pre-shared secret that can mint an invite conferring
// ownership of a workspace; a deployment that sets it to something guessable is
// not "degraded", it is a deployment whose strongest credential is weaker than
// its operator believes. Refusing to boot puts that in front of whoever is
// deploying, at the only moment they are still looking.
var ErrBootstrapMisconfigured = errors.New("bootstrap misconfigured")

const (
	// The credential is exactly 32 random bytes, encoded as unpadded Base64URL —
	// 43 characters. One canonical format, not a menu: accepting hex as well
	// would mean two parsers, two length rules and a value that is valid under
	// one and nonsense under the other. 32 bytes because that is the same
	// strength the service already demands of AUTH_JWT_HMAC_SECRET, and this
	// credential is not less sensitive than that one.
	bootstrapTokenBytes         = 32
	bootstrapTokenEncodedLength = 43

	bootstrapTokenFormatHint = "ADMIN_BOOTSTRAP_TOKEN must be a 32-byte Base64URL value " +
		"(43 characters, no padding); generate with: openssl rand -base64 32 | tr '+/' '-_' | tr -d '='"
)

// weakBootstrapTokenMarkers are substrings that give away a placeholder left in
// place. Checked as substrings rather than whole values because the length rule
// already rejects a bare "changeme": what this catches is a 43-character string
// padded out of one, which passes every structural test and is still a value
// somebody typed rather than generated.
//
// This is a short deny-list of known development values, not a strength
// heuristic. Entropy is what the length and encoding rules above establish;
// no amount of character-class checking substitutes for it.
var weakBootstrapTokenMarkers = []string{
	"changeme",
	"change-me",
	"secret",
	"admin",
	"bootstrap",
	"token",
	"password",
	"example",
	"placeholder",
}

// BootstrapEnabled reports whether the operator asked for the bootstrap
// endpoint at all. Only meaningful once ValidateBootstrap has passed.
func (c Config) BootstrapEnabled() bool {
	return c.AdminBootstrapToken != ""
}

// ValidateBootstrap enforces the three states the bootstrap configuration may
// be in. There is no fourth, and in particular no partially-configured one:
//
//   - neither variable set — bootstrap is disabled, the service starts, and
//     the route fails closed exactly as it does today;
//   - both set and valid — the service starts and the route works until the
//     workspace has its first owner;
//   - anything else — the service refuses to start.
//
// A token without a workspace, or a workspace without a token, is rejected
// rather than quietly disabled. Both halves individually fail closed at
// runtime, so the deployment would look enabled and answer 503 forever; the
// operator who set one of them meant to enable bootstrap, and the useful
// moment to tell them they have not is startup.
//
// No message includes the credential, any prefix of it, or a hash of it.
func (c Config) ValidateBootstrap() error {
	workspaceID := c.AuthBootstrapWorkspaceID

	if c.AdminBootstrapToken == "" {
		if workspaceID != "" {
			return fmt.Errorf("%w: AUTH_BOOTSTRAP_WORKSPACE_ID is set but ADMIN_BOOTSTRAP_TOKEN is not; set both to enable the bootstrap endpoint, or neither to disable it", ErrBootstrapMisconfigured)
		}
		return nil
	}

	if err := validateBootstrapToken(c.AdminBootstrapToken); err != nil {
		return err
	}

	if workspaceID == "" {
		return fmt.Errorf("%w: ADMIN_BOOTSTRAP_TOKEN is set but AUTH_BOOTSTRAP_WORKSPACE_ID is not; set both to enable the bootstrap endpoint, or neither to disable it", ErrBootstrapMisconfigured)
	}
	if _, err := uuid.Parse(workspaceID); err != nil {
		return fmt.Errorf("%w: AUTH_BOOTSTRAP_WORKSPACE_ID must be a UUID", ErrBootstrapMisconfigured)
	}
	return nil
}

func validateBootstrapToken(token string) error {
	// Checked against the raw value, before any trimming: a credential that
	// only matches once whitespace is stripped is a credential whose exact
	// bytes the operator does not know, and comparing it here but transporting
	// it in a header is how "works locally, 401 in staging" starts.
	if strings.TrimSpace(token) != token {
		return fmt.Errorf("%w: ADMIN_BOOTSTRAP_TOKEN must not contain leading or trailing whitespace", ErrBootstrapMisconfigured)
	}
	if containsWeakMarker(token) {
		return fmt.Errorf("%w: ADMIN_BOOTSTRAP_TOKEN looks like a placeholder; %s", ErrBootstrapMisconfigured, bootstrapTokenFormatHint)
	}
	if len(token) != bootstrapTokenEncodedLength {
		return fmt.Errorf("%w: %s", ErrBootstrapMisconfigured, bootstrapTokenFormatHint)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(decoded) != bootstrapTokenBytes {
		return fmt.Errorf("%w: %s", ErrBootstrapMisconfigured, bootstrapTokenFormatHint)
	}
	// "AAAA…A" is 43 valid Base64URL characters decoding to 32 zero bytes: it
	// passes every structural rule above while carrying no entropy at all.
	if allBytesEqual(decoded) || allCharsEqual(token) {
		return fmt.Errorf("%w: ADMIN_BOOTSTRAP_TOKEN is a repeated-character value carrying no entropy; %s", ErrBootstrapMisconfigured, bootstrapTokenFormatHint)
	}
	return nil
}

func containsWeakMarker(token string) bool {
	lowered := strings.ToLower(token)
	for _, marker := range weakBootstrapTokenMarkers {
		if strings.Contains(lowered, marker) {
			return true
		}
	}
	return false
}

func allBytesEqual(value []byte) bool {
	for _, b := range value {
		if b != value[0] {
			return false
		}
	}
	return len(value) > 0
}

func allCharsEqual(value string) bool {
	for _, r := range value {
		if r != rune(value[0]) {
			return false
		}
	}
	return len(value) > 0
}
