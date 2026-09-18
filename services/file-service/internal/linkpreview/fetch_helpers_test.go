package linkpreview

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/linkfetch"
)

// Test doubles for the shared fetcher, so the service tests here keep driving
// the real address policy against a local server. The policy's own suite lives
// with the fetcher in libs/go/platform/linkfetch.

// publicAddr is the address every test hostname resolves to. It is a real
// public address and is never dialled — the connector below redirects the
// accepted connection to the local test server.
const publicAddr = "93.184.216.34"

func fixedResolver(addrs ...string) linkfetch.Resolver {
	parsed := make([]netip.Addr, 0, len(addrs))
	for _, raw := range addrs {
		parsed = append(parsed, netip.MustParseAddr(raw))
	}
	return func(context.Context, string) ([]netip.Addr, error) {
		return parsed, nil
	}
}

// recordingConnector sends every accepted connection to target and records the
// address the policy approved.
type recordingConnector struct {
	target string
	mu     sync.Mutex
	dialed []string
}

func (c *recordingConnector) connect(ctx context.Context, network, address string) (net.Conn, error) {
	c.mu.Lock()
	c.dialed = append(c.dialed, address)
	c.mu.Unlock()
	return (&net.Dialer{}).DialContext(ctx, network, c.target)
}

func newFetcherWith(timeout time.Duration, resolve linkfetch.Resolver, connect linkfetch.Connector) *linkfetch.Fetcher {
	return linkfetch.NewFetcherWith(timeout, resolve, connect)
}

func htmlServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(server.Close)
	return server
}
