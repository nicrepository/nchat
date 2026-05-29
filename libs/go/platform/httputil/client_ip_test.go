package httputil_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
)

func TestClientIP_NoTrustedCIDRs_UsesRemoteAddr(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.1:1234"
	req.Header.Set("X-Forwarded-For", "10.0.0.50")

	got := httputil.ClientIP(req, nil)
	if got != "203.0.113.1" {
		t.Fatalf("expected 203.0.113.1, got %q", got)
	}
}

func TestClientIP_TrustedProxy_UsesXFF(t *testing.T) {
	cidrs := httputil.ParseCIDRs("10.0.0.0/8")
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.50")

	got := httputil.ClientIP(req, cidrs)
	if got != "203.0.113.50" {
		t.Fatalf("expected 203.0.113.50, got %q", got)
	}
}

func TestClientIP_TrustedProxy_MalformedXFF_FallsBackToXRI(t *testing.T) {
	cidrs := httputil.ParseCIDRs("10.0.0.0/8")
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	req.Header.Set("X-Forwarded-For", "not-an-ip")
	req.Header.Set("X-Real-IP", "198.51.100.7")

	got := httputil.ClientIP(req, cidrs)
	if got != "198.51.100.7" {
		t.Fatalf("expected 198.51.100.7, got %q", got)
	}
}

func TestClientIP_TrustedProxy_MalformedXFF_MalformedXRI_FallsBackToRemoteAddr(t *testing.T) {
	cidrs := httputil.ParseCIDRs("10.0.0.0/8")
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	req.Header.Set("X-Forwarded-For", "not-an-ip")
	req.Header.Set("X-Real-IP", "also-not-an-ip")

	got := httputil.ClientIP(req, cidrs)
	if got != "10.0.0.1" {
		t.Fatalf("expected 10.0.0.1, got %q", got)
	}
}

func TestClientIP_TrustedProxy_NoForwardedHeaders_FallsBackToRemoteAddr(t *testing.T) {
	cidrs := httputil.ParseCIDRs("10.0.0.0/8")
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.1:1234"

	got := httputil.ClientIP(req, cidrs)
	if got != "10.0.0.1" {
		t.Fatalf("expected 10.0.0.1, got %q", got)
	}
}

func TestClientIP_RemoteAddrNotInTrustedCIDR_UsesRemoteAddr(t *testing.T) {
	cidrs := httputil.ParseCIDRs("10.0.0.0/8")
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.99:4567"
	req.Header.Set("X-Forwarded-For", "198.51.100.1")

	got := httputil.ClientIP(req, cidrs)
	if got != "203.0.113.99" {
		t.Fatalf("expected 203.0.113.99, got %q", got)
	}
}

func TestClientIP_MultiValueXFF_UsesLeftmost(t *testing.T) {
	cidrs := httputil.ParseCIDRs("10.0.0.0/8")
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.50, 10.0.0.2, 10.0.0.3")

	got := httputil.ClientIP(req, cidrs)
	if got != "203.0.113.50" {
		t.Fatalf("expected 203.0.113.50, got %q", got)
	}
}

func TestParseCIDRs_ValidAndInvalid(t *testing.T) {
	cidrs := httputil.ParseCIDRs("10.0.0.0/8, invalid, 172.16.0.0/12")
	if len(cidrs) != 2 {
		t.Fatalf("expected 2 valid CIDRs, got %d", len(cidrs))
	}
}

func TestParseCIDRs_Empty(t *testing.T) {
	cidrs := httputil.ParseCIDRs("")
	if len(cidrs) != 0 {
		t.Fatalf("expected 0 CIDRs for empty string, got %d", len(cidrs))
	}
}
