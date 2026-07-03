package ws

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type reactionLimiterValkey struct {
	mu    sync.Mutex
	count int
	fail  bool
	key   string
}

func newTestReactionLimiter(t *testing.T, server *reactionLimiterValkey) *ValkeyReactionLimiter {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go server.serve(conn)
		}
	}()
	limiter, err := NewValkeyReactionLimiter("valkey://" + listener.Addr().String() + "?protocol=2&client_cache=0")
	if err != nil {
		t.Fatalf("new reaction limiter: %v", err)
	}
	t.Cleanup(limiter.Close)
	return limiter
}

func (s *reactionLimiterValkey) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	reader := bufio.NewReader(conn)
	for {
		command, err := readReactionLimiterCommand(reader)
		if err != nil {
			return
		}
		switch strings.ToUpper(command[0]) {
		case "HELLO":
			_, _ = io.WriteString(conn, "*2\r\n+proto\r\n:2\r\n")
		case "CLUSTER":
			_, _ = io.WriteString(conn, "-ERR unknown command 'cluster'\r\n")
		case "EVALSHA", "EVAL":
			s.mu.Lock()
			if s.fail {
				s.mu.Unlock()
				_, _ = io.WriteString(conn, "-ERR unavailable\r\n")
				continue
			}
			s.count++
			s.key = command[3]
			count := s.count
			s.mu.Unlock()
			_, _ = fmt.Fprintf(conn, ":%d\r\n", count)
		default:
			_, _ = io.WriteString(conn, "+OK\r\n")
		}
	}
}

func readReactionLimiterCommand(reader *bufio.Reader) ([]string, error) {
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

func TestNewValkeyReactionLimiterValidatesConfiguration(t *testing.T) {
	for _, url := range []string{"", "://invalid"} {
		if limiter, err := NewValkeyReactionLimiter(url); err == nil || limiter != nil {
			t.Fatalf("expected %q to be rejected, limiter=%v err=%v", url, limiter, err)
		}
	}

	_ = newTestReactionLimiter(t, &reactionLimiterValkey{})
}

func TestValkeyReactionLimiterAllowsSixtyActionsWithoutExposingUserID(t *testing.T) {
	server := &reactionLimiterValkey{}
	limiter := newTestReactionLimiter(t, server)
	for i := 0; i < reactionActionsPerMinute; i++ {
		allowed, err := limiter.Allow(t.Context(), "sensitive-user-id")
		if err != nil || !allowed {
			t.Fatalf("action %d: allowed=%v err=%v", i+1, allowed, err)
		}
	}
	allowed, err := limiter.Allow(t.Context(), "sensitive-user-id")
	if err != nil || allowed {
		t.Fatalf("action 61: allowed=%v err=%v", allowed, err)
	}
	if strings.Contains(server.key, "sensitive-user-id") {
		t.Fatalf("rate-limit key leaks user ID: %q", server.key)
	}
}

func TestValkeyReactionLimiterPropagatesValkeyFailure(t *testing.T) {
	limiter := newTestReactionLimiter(t, &reactionLimiterValkey{fail: true})
	if allowed, err := limiter.Allow(t.Context(), "user-1"); err == nil || allowed {
		t.Fatalf("expected Valkey error, allowed=%v err=%v", allowed, err)
	}
}
