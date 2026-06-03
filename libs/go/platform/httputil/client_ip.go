package httputil

import (
	"net"
	"net/http"
	"strings"
)

// ParseCIDRs parses a comma-separated list of CIDR strings.
// Invalid or empty entries are silently ignored.
func ParseCIDRs(cidrs string) []*net.IPNet {
	var result []*net.IPNet
	for _, raw := range strings.Split(cidrs, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		_, ipNet, err := net.ParseCIDR(raw)
		if err == nil {
			result = append(result, ipNet)
		}
	}
	return result
}

// ClientIP returns the effective client IP for a request.
//
// When RemoteAddr belongs to a configured trusted-proxy CIDR, the leftmost
// entry of X-Forwarded-For is used — but only after net.ParseIP validation
// and canonicalization (parsed.String()). X-Real-IP is the fallback when XFF
// is absent or its leftmost entry is not a valid IP. If no trusted-proxy CIDRs
// are configured, or if forwarded headers contain no valid IP, RemoteAddr is
// always used. Raw unvalidated header strings are never returned.
func ClientIP(r *http.Request, trustedCIDRs []*net.IPNet) string {
	remoteIP := remoteAddrHost(r.RemoteAddr)
	if len(trustedCIDRs) == 0 {
		return remoteIP
	}
	ip := net.ParseIP(remoteIP)
	if ip == nil {
		return remoteIP
	}
	for _, cidr := range trustedCIDRs {
		if cidr.Contains(ip) {
			// X-Forwarded-For: validate and canonicalize the leftmost entry only.
			if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
				first := strings.TrimSpace(strings.SplitN(xff, ",", 2)[0])
				if parsed := net.ParseIP(first); parsed != nil {
					return parsed.String()
				}
			}
			// X-Real-IP: validate and canonicalize.
			if xri := strings.TrimSpace(r.Header.Get("X-Real-IP")); xri != "" {
				if parsed := net.ParseIP(xri); parsed != nil {
					return parsed.String()
				}
			}
			break
		}
	}
	return remoteIP
}

// remoteAddrHost extracts the host portion from a "host:port" address.
// If splitting fails or the host is empty, the original string is returned.
func remoteAddrHost(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil || host == "" {
		return remoteAddr
	}
	return host
}
