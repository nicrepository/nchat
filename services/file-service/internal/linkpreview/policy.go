package linkpreview

import (
	"fmt"
	"net/netip"
	"net/url"
	"strings"
)

// MaxURLLength bounds what a client may submit. It is well past every URL a
// browser will produce and short enough that the value is cheap to carry, log
// nothing of, and use as a cache key.
const MaxURLLength = 2048

// blockedPrefixes are the destinations netip's own predicates do not classify.
//
// The IPv6 entries are the ones worth naming: three of them are translation or
// tunnelling ranges that embed an arbitrary IPv4 address, so reaching them
// would hand back exactly the private destination the IPv4 rules refuse. They
// are blocked whole rather than unwrapped, because this service has no reason
// to speak to any of them.
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),      // "this network"
	netip.MustParsePrefix("100.64.0.0/10"),  // carrier-grade NAT
	netip.MustParsePrefix("192.0.0.0/24"),   // IETF protocol assignments
	netip.MustParsePrefix("198.18.0.0/15"),  // benchmarking
	netip.MustParsePrefix("240.0.0.0/4"),    // reserved, including 255.255.255.255
	netip.MustParsePrefix("::/96"),          // IPv4-compatible IPv6 (deprecated)
	netip.MustParsePrefix("64:ff9b::/96"),   // NAT64
	netip.MustParsePrefix("64:ff9b:1::/48"), // local-use NAT64
	netip.MustParsePrefix("2002::/16"),      // 6to4
	netip.MustParsePrefix("2001:db8::/32"),  // documentation
	netip.MustParsePrefix("100::/64"),       // discard-only
}

// addrAllowed reports whether addr is a public destination this service may
// connect to. It is the single decision the whole SSRF defence rests on, and it
// is asked about the address the connection will actually use — never about a
// hostname.
//
// Cloud metadata services need no entry of their own: 169.254.169.254 is
// link-local and fd00:ec2::254 is a unique local address, so both are already
// refused, and so is any hostname or alias that resolves to them.
func addrAllowed(addr netip.Addr) bool {
	// An IPv4 address written as ::ffff:127.0.0.1 has to be judged as
	// 127.0.0.1, or every IPv4 rule below would silently not apply.
	addr = addr.Unmap()
	switch {
	case !addr.IsValid(), addr.IsUnspecified(), addr.IsLoopback(), addr.IsPrivate(),
		addr.IsLinkLocalUnicast(), addr.IsLinkLocalMulticast(),
		addr.IsInterfaceLocalMulticast(), addr.IsMulticast():
		return false
	case addr.Zone() != "":
		// A scoped address names an interface on this host. Nothing public
		// carries one.
		return false
	}
	for _, prefix := range blockedPrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

// canonicalURL validates a client-supplied URL and returns the form used both
// for the request and as the cache key.
//
// Everything it refuses is refused because allowing it would widen what the
// endpoint can reach: a non-HTTP scheme, credentials the service would replay
// to a stranger, a missing host, or a port that is not the scheme's own. That
// last one is what keeps the endpoint from being an indirect port scanner —
// with it, "fetch this page" cannot become "tell me what is listening on
// 12345".
//
// It is a first line, not the defence: the destination is judged again, by
// address, when the connection is made.
func canonicalURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	switch {
	case raw == "":
		return nil, fmt.Errorf("%w: url is required", ErrInvalidURL)
	case len(raw) > MaxURLLength:
		return nil, fmt.Errorf("%w: url is too long", ErrInvalidURL)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: url is malformed", ErrInvalidURL)
	}
	if err := checkRequestURL(parsed); err != nil {
		return nil, err
	}
	// The fragment never reaches the server, so keeping it would only split the
	// cache between requests that produce the same document.
	canonical := *parsed
	canonical.Fragment = ""
	canonical.RawFragment = ""
	canonical.Host = strings.ToLower(canonical.Host)
	return &canonical, nil
}

// checkRequestURL applies the rules every URL this service connects to must
// satisfy, including the target of each redirect.
//
// It is two questions asked in a fixed order — how this service would speak to
// the URL, and where the URL points — and the order is part of the contract:
// each check's error class is what the caller maps to a status code, so a URL
// that is wrong in two ways must keep reporting the same one it always did.
// Both halves stay in this file so the whole policy is auditable in one place.
func checkRequestURL(parsed *url.URL) error {
	scheme, err := checkRequestScheme(parsed)
	if err != nil {
		return err
	}
	return checkRequestAuthority(parsed, scheme)
}

// checkRequestScheme validates how the connection would be made, and returns
// the normalised scheme the authority check needs.
func checkRequestScheme(parsed *url.URL) (string, error) {
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("%w: only http and https are supported", ErrURLNotAllowed)
	}
	if parsed.Opaque != "" {
		return "", fmt.Errorf("%w: url is malformed", ErrInvalidURL)
	}
	if parsed.User != nil {
		// Credentials in the URL would be sent to whatever the hostname
		// resolves to. No preview needs them.
		return "", fmt.Errorf("%w: url must not carry credentials", ErrURLNotAllowed)
	}
	return scheme, nil
}

// checkRequestAuthority validates where the URL points: the host, the port, and
// an address written literally.
//
// None of this replaces the dial-time check — the destination is judged again,
// by resolved address, when the connection is made. The literal case is refused
// here so an obviously internal target costs neither a DNS lookup nor a socket.
func checkRequestAuthority(parsed *url.URL, scheme string) error {
	host := parsed.Hostname()
	if host == "" || strings.ContainsAny(host, " \t\r\n") {
		return fmt.Errorf("%w: url has no valid host", ErrInvalidURL)
	}
	if port := parsed.Port(); port != "" && port != defaultPort(scheme) {
		return fmt.Errorf("%w: only the default port is supported", ErrURLNotAllowed)
	}
	if addr, err := netip.ParseAddr(host); err == nil && !addrAllowed(addr) {
		return fmt.Errorf("%w: destination is not permitted", ErrURLNotAllowed)
	}
	return nil
}

func defaultPort(scheme string) string {
	if scheme == "https" {
		return "443"
	}
	return "80"
}
