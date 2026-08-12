package linkpreview

import (
	"errors"
	"net/netip"
	"testing"
)

func TestCanonicalURLAcceptsPublicHTTPAndHTTPS(t *testing.T) {
	for _, raw := range []string{
		"http://example.com",
		"https://example.com/page?a=1",
		"https://example.com:443/page",
		"http://example.com:80/",
		"https://sub.example.co.uk/path/to/page",
	} {
		if _, err := canonicalURL(raw); err != nil {
			t.Fatalf("expected %q to be accepted, got %v", raw, err)
		}
	}
}

func TestCanonicalURLNormalizesForCacheKey(t *testing.T) {
	// The fragment never reaches the server and the host is case-insensitive,
	// so both must collapse into one cache key. The query must not.
	sameKey := []string{
		"https://Example.COM/page?a=1#top",
		"https://example.com/page?a=1",
		"  https://example.com/page?a=1  ",
	}
	var first string
	for index, raw := range sameKey {
		parsed, err := canonicalURL(raw)
		if err != nil {
			t.Fatalf("canonicalURL(%q): %v", raw, err)
		}
		if index == 0 {
			first = parsed.String()
			continue
		}
		if parsed.String() != first {
			t.Fatalf("expected %q to canonicalise to %q, got %q", raw, first, parsed.String())
		}
	}

	differing, err := canonicalURL("https://example.com/page?a=2")
	if err != nil {
		t.Fatalf("canonicalURL: %v", err)
	}
	if differing.String() == first {
		t.Fatal("URLs differing only in the query must not share a cache key")
	}
}

func TestCanonicalURLRefusesInvalidInput(t *testing.T) {
	cases := map[string]struct {
		raw  string
		want error
	}{
		"empty":            {"", ErrInvalidURL},
		"whitespace only":  {"   ", ErrInvalidURL},
		"no host":          {"http:///path", ErrInvalidURL},
		"relative":         {"/just/a/path", ErrURLNotAllowed},
		"malformed":        {"http://a b.com/", ErrInvalidURL},
		"control char":     {"http://example.com/\x7f\x00", ErrInvalidURL},
		"too long":         {"https://example.com/" + longPath(MaxURLLength), ErrInvalidURL},
		"opaque":           {"http:example.com", ErrInvalidURL},
		"file scheme":      {"file:///etc/passwd", ErrURLNotAllowed},
		"ftp scheme":       {"ftp://example.com/x", ErrURLNotAllowed},
		"gopher scheme":    {"gopher://example.com/x", ErrURLNotAllowed},
		"data scheme":      {"data:text/html,<h1>x</h1>", ErrURLNotAllowed},
		"javascript":       {"javascript:alert(1)", ErrURLNotAllowed},
		"unix scheme":      {"unix:///var/run/docker.sock", ErrURLNotAllowed},
		"userinfo":         {"http://admin:secret@example.com/", ErrURLNotAllowed},
		"user only":        {"http://admin@example.com/", ErrURLNotAllowed},
		"nondefault port":  {"http://example.com:8080/", ErrURLNotAllowed},
		"https wrong port": {"https://example.com:8443/", ErrURLNotAllowed},
		"ssh port":         {"http://example.com:22/", ErrURLNotAllowed},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := canonicalURL(testCase.raw)
			if !errors.Is(err, testCase.want) {
				t.Fatalf("expected %v for %q, got %v", testCase.want, testCase.raw, err)
			}
		})
	}
}

// TestCanonicalURLRefusesNonPublicLiterals covers the addresses a caller can
// name without any DNS at all. The dialer would refuse them too; refusing them
// here means no socket is opened to find out.
func TestCanonicalURLRefusesNonPublicLiterals(t *testing.T) {
	for _, raw := range []string{
		"http://127.0.0.1/",
		"http://127.1.2.3/admin",
		"http://0.0.0.0/",
		"http://10.0.0.1/",
		"http://172.16.0.1/",
		"http://192.168.1.1/",
		"http://169.254.169.254/latest/meta-data/",
		"http://100.64.0.1/",
		"http://198.18.0.1/",
		"http://255.255.255.255/",
		"http://[::1]/",
		"http://[::]/",
		"http://[fe80::1]/",
		"http://[fc00::1]/",
		"http://[fd00:ec2::254]/",
		"http://[::ffff:127.0.0.1]/",
		"http://[::ffff:169.254.169.254]/",
		"http://[64:ff9b::7f00:1]/",
		"http://[2002:7f00:1::]/",
		"http://[ff02::1]/",
	} {
		if _, err := canonicalURL(raw); !errors.Is(err, ErrURLNotAllowed) {
			t.Fatalf("expected %q to be refused, got %v", raw, err)
		}
	}
}

// TestAddrAllowed is the policy itself, stated as a table. It is the single
// decision every other control in this package depends on.
func TestAddrAllowed(t *testing.T) {
	blocked := []string{
		// IPv4 loopback, private, link-local, CGNAT, reserved.
		"127.0.0.1", "127.255.255.254", "0.0.0.0", "0.1.2.3",
		"10.255.255.255", "172.16.0.1", "172.31.255.255", "192.168.0.1",
		"169.254.169.254", "169.254.0.1", "100.64.0.1", "100.127.255.255",
		"192.0.0.1", "198.18.0.1", "224.0.0.1", "239.255.255.255",
		"240.0.0.1", "255.255.255.255",
		// IPv6 loopback, unspecified, ULA, link-local, multicast.
		"::1", "::", "fc00::1", "fd12:3456:789a::1", "fe80::1",
		"ff02::1", "ff01::1",
		// IPv6 forms that carry an IPv4 address in disguise.
		"::ffff:127.0.0.1", "::ffff:10.0.0.1", "::ffff:169.254.169.254",
		"::127.0.0.1", "64:ff9b::a00:1", "2002:c0a8:0101::",
		// Reserved documentation and discard ranges.
		"2001:db8::1", "100::1",
	}
	for _, raw := range blocked {
		addr := netip.MustParseAddr(raw)
		if addrAllowed(addr) {
			t.Fatalf("expected %s to be blocked", raw)
		}
	}

	allowed := []string{
		"1.1.1.1", "8.8.8.8", "93.184.216.34", "203.0.113.10",
		"172.32.0.1", "172.15.255.255", "100.128.0.1", "9.255.255.255",
		"2606:4700:4700::1111", "2001:4860:4860::8888",
	}
	for _, raw := range allowed {
		addr := netip.MustParseAddr(raw)
		if !addrAllowed(addr) {
			t.Fatalf("expected %s to be allowed", raw)
		}
	}
}

// TestAddrAllowedRefusesZonedAddresses covers a scoped address, which names an
// interface on this host and is never a public destination.
func TestAddrAllowedRefusesZonedAddresses(t *testing.T) {
	addr := netip.MustParseAddr("2606:4700:4700::1111").WithZone("eth0")
	if addrAllowed(addr) {
		t.Fatal("expected a zoned address to be blocked")
	}
}

func TestAddrAllowedRefusesInvalidAddress(t *testing.T) {
	if addrAllowed(netip.Addr{}) {
		t.Fatal("expected the zero address to be blocked")
	}
}

func longPath(length int) string {
	path := make([]byte, length)
	for index := range path {
		path[index] = 'a'
	}
	return string(path)
}
