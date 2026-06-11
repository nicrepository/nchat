package storage_test

import (
	"context"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

func TestOpenDB_InvalidDSN_ReturnsError(t *testing.T) {
	_, err := storage.OpenDB(context.Background(), "not://a::valid::dsn", 1)
	if err == nil {
		t.Fatal("expected error for invalid DSN, got nil")
	}
}

func TestOpenDB_UnreachableHost_PingError(t *testing.T) {
	// Port 9999 is unlikely to be in use; connection refused is immediate.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := storage.OpenDB(ctx, "postgres://user:pass@localhost:9999/dbtest?sslmode=disable", 1)
	if err == nil {
		t.Fatal("expected ping error for unreachable host, got nil")
	}
}
