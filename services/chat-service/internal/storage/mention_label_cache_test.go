package storage

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	valkey "github.com/valkey-io/valkey-go"
)

type valkeyTestEntry struct {
	value     string
	expiresAt time.Time
}

type valkeyTestServer struct {
	mu      sync.Mutex
	now     time.Time
	entries map[string]valkeyTestEntry
}

func newValkeyTestCache(t *testing.T) (*valkeyTestServer, *ValkeyMentionLabelCache) {
	t.Helper()
	server := &valkeyTestServer{
		now:     time.Unix(1_700_000_000, 0),
		entries: map[string]valkeyTestEntry{},
	}
	client, err := valkey.NewClient(valkey.ClientOption{
		InitAddress:  []string{"pipe"},
		AlwaysRESP2:  true,
		DisableCache: true,
		DialCtxFn: func(context.Context, string, *net.Dialer, *tls.Config) (net.Conn, error) {
			clientConn, serverConn := net.Pipe()
			go server.serveConn(serverConn)
			return clientConn, nil
		},
	})
	if err != nil {
		t.Fatalf("new test Valkey client: %v", err)
	}
	cache := newValkeyMentionLabelCache(client)
	t.Cleanup(cache.Close)
	return server, cache
}

func (s *valkeyTestServer) serveConn(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	reader := bufio.NewReader(conn)
	for {
		command, err := readRESPCommand(reader)
		if err != nil {
			return
		}
		switch strings.ToUpper(command[0]) {
		case "HELLO":
			_, _ = io.WriteString(conn, "*2\r\n+proto\r\n:2\r\n")
		case "CLIENT":
			_, _ = io.WriteString(conn, "+OK\r\n")
		case "SET":
			ttlSeconds, _ := strconv.Atoi(command[4])
			s.mu.Lock()
			s.entries[command[1]] = valkeyTestEntry{value: command[2], expiresAt: s.now.Add(time.Duration(ttlSeconds) * time.Second)}
			s.mu.Unlock()
			_, _ = io.WriteString(conn, "+OK\r\n")
		case "MGET":
			s.mu.Lock()
			_, _ = fmt.Fprintf(conn, "*%d\r\n", len(command)-1)
			for _, key := range command[1:] {
				entry, ok := s.entries[key]
				if !ok || !s.now.Before(entry.expiresAt) {
					_, _ = io.WriteString(conn, "$-1\r\n")
					continue
				}
				_, _ = fmt.Fprintf(conn, "$%d\r\n%s\r\n", len(entry.value), entry.value)
			}
			s.mu.Unlock()
		default:
			_, _ = fmt.Fprintf(conn, "-ERR unsupported test command %s\r\n", command[0])
		}
	}
}

func (s *valkeyTestServer) advance(duration time.Duration) {
	s.mu.Lock()
	s.now = s.now.Add(duration)
	s.mu.Unlock()
}

func readRESPCommand(reader *bufio.Reader) ([]string, error) {
	header, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	count, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(header, "*")))
	if err != nil {
		return nil, err
	}
	command := make([]string, count)
	for i := range count {
		lengthLine, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		length, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(lengthLine, "$")))
		if err != nil {
			return nil, err
		}
		value := make([]byte, length+2)
		if _, err := io.ReadFull(reader, value); err != nil {
			return nil, err
		}
		command[i] = string(value[:length])
	}
	return command, nil
}

func TestValkeyMentionLabelCache_HitMissAndExpiry(t *testing.T) {
	server, cache := newValkeyTestCache(t)

	labels := map[string]string{"user:user-1": "Alice"}
	if err := cache.Set(t.Context(), "workspace-1", labels, 45*time.Second); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := cache.Get(t.Context(), "workspace-1", []string{"user:user-1", "channel:missing"})
	if err != nil || got["user:user-1"] != "Alice" {
		t.Fatalf("cached labels=%v err=%v", got, err)
	}

	server.advance(46 * time.Second)
	got, err = cache.Get(t.Context(), "workspace-1", []string{"user:user-1"})
	if err != nil || len(got) != 0 {
		t.Fatalf("expired labels=%v err=%v", got, err)
	}
}

func TestNewValkeyMentionLabelCache_RejectsInvalidURL(t *testing.T) {
	if _, err := NewValkeyMentionLabelCache("not-a-valkey-url"); err == nil {
		t.Fatal("expected invalid URL error")
	}
}

func TestValkeyMentionLabelCache_EmptyGetSkipsValkey(t *testing.T) {
	cache := &ValkeyMentionLabelCache{}
	labels, err := cache.Get(t.Context(), "workspace-1", nil)
	if err != nil || len(labels) != 0 {
		t.Fatalf("labels=%v err=%v", labels, err)
	}
	if err := cache.Set(t.Context(), "workspace-1", nil, 45*time.Second); err != nil {
		t.Fatalf("empty Set: %v", err)
	}
}
