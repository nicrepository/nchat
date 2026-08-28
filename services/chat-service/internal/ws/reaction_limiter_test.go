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
	mu     sync.Mutex
	counts map[string]int
	fail   bool
	key    string
	window string
}

func newTestReactionLimiter(t *testing.T, server *reactionLimiterValkey, maxActions, windowSeconds int) *ValkeyReactionLimiter {
	t.Helper()
	url := startTestReactionLimiterValkey(t, server)
	limiter, err := NewValkeyReactionLimiter(url, maxActions, windowSeconds)
	if err != nil {
		t.Fatalf("new reaction limiter: %v", err)
	}
	t.Cleanup(limiter.Close)
	return limiter
}

func startTestReactionLimiterValkey(t *testing.T, server *reactionLimiterValkey) string {
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
	return "valkey://" + listener.Addr().String() + "?protocol=2&client_cache=0"
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
			s.key = command[3]
			s.window = command[4]
			if s.counts == nil {
				s.counts = make(map[string]int)
			}
			s.counts[s.key]++
			count := s.counts[s.key]
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
	for _, tt := range []struct {
		url                string
		maxActions, window int
	}{
		{url: "", maxActions: 60, window: 60},
		{url: "://invalid", maxActions: 60, window: 60},
		{url: "valkey://localhost:6379", maxActions: 0, window: 60},
		{url: "valkey://localhost:6379", maxActions: 60, window: 0},
	} {
		if limiter, err := NewValkeyReactionLimiter(tt.url, tt.maxActions, tt.window); err == nil || limiter != nil {
			t.Fatalf("expected invalid config to be rejected, limiter=%v err=%v", limiter, err)
		}
	}

	_ = newTestReactionLimiter(t, &reactionLimiterValkey{}, 60, 60)
}

func TestValkeyReactionLimiterUsesConfiguredLimitAndWindowWithoutExposingUserID(t *testing.T) {
	server := &reactionLimiterValkey{}
	limiter := newTestReactionLimiter(t, server, 2, 7)
	for i := 0; i < 2; i++ {
		allowed, err := limiter.Allow(t.Context(), "sensitive-user-id")
		if err != nil || !allowed {
			t.Fatalf("action %d: allowed=%v err=%v", i+1, allowed, err)
		}
	}
	allowed, err := limiter.Allow(t.Context(), "sensitive-user-id")
	if err != nil || allowed {
		t.Fatalf("action 3: allowed=%v err=%v", allowed, err)
	}
	if strings.Contains(server.key, "sensitive-user-id") {
		t.Fatalf("rate-limit key leaks user ID: %q", server.key)
	}
	if server.window != "7" {
		t.Fatalf("rate-limit window = %q, want 7", server.window)
	}
}

func TestValkeyReactionLimiterPropagatesValkeyFailure(t *testing.T) {
	limiter := newTestReactionLimiter(t, &reactionLimiterValkey{fail: true}, 60, 60)
	if allowed, err := limiter.Allow(t.Context(), "user-1"); err == nil || allowed {
		t.Fatalf("expected Valkey error, allowed=%v err=%v", allowed, err)
	}
}

func TestValkeyReactionLimiterScopesEditMessageActionWithoutExposingUserID(t *testing.T) {
	server := &reactionLimiterValkey{}
	limiter := newTestReactionLimiter(t, server, 2, 60)
	allowed, err := limiter.AllowAction(t.Context(), "sensitive-user-id", "edit_message")
	if err != nil || !allowed {
		t.Fatalf("edit action: allowed=%v err=%v", allowed, err)
	}
	if !strings.Contains(server.key, ":edit_message:") || strings.Contains(server.key, "sensitive-user-id") {
		t.Fatalf("unexpected edit rate-limit key: %q", server.key)
	}
}

func TestValkeyReactionLimiterSharesActionBudgetAcrossClients(t *testing.T) {
	server := &reactionLimiterValkey{}
	url := startTestReactionLimiterValkey(t, server)
	first, err := NewValkeyReactionLimiter(url, 60, 60)
	if err != nil {
		t.Fatalf("first limiter: %v", err)
	}
	t.Cleanup(first.Close)
	second, err := NewValkeyReactionLimiter(url, 60, 60)
	if err != nil {
		t.Fatalf("second limiter: %v", err)
	}
	t.Cleanup(second.Close)

	for range 2 {
		allowed, err := first.AllowActionWithLimit(t.Context(), "user-1", "dm_search", 2, 60)
		if err != nil || !allowed {
			t.Fatalf("within budget: allowed=%v err=%v", allowed, err)
		}
	}
	allowed, err := second.AllowActionWithLimit(t.Context(), "user-1", "dm_search", 2, 60)
	if err != nil || allowed {
		t.Fatalf("shared budget: allowed=%v err=%v", allowed, err)
	}
	allowed, err = second.AllowActionWithLimit(t.Context(), "user-1", "dm_create", 1, 60)
	if err != nil || !allowed {
		t.Fatalf("independent action: allowed=%v err=%v", allowed, err)
	}
}

func TestValkeyReactionLimiterRejectsInvalidActionBudget(t *testing.T) {
	limiter := newTestReactionLimiter(t, &reactionLimiterValkey{}, 60, 60)
	if allowed, err := limiter.AllowActionWithLimit(t.Context(), "user-1", "dm_search", 0, 60); err == nil || allowed {
		t.Fatalf("allowed=%v err=%v", allowed, err)
	}
}
