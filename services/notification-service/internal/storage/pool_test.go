package storage_test

import (
	"context"
	"testing"

	"github.com/nicrepository/nchat/services/notification-service/internal/storage"
)

func TestOpenDB_InvalidDSN_ReturnsError(t *testing.T) {
	_, err := storage.OpenDB(context.Background(), "not://a::valid::dsn", 1)
	if err == nil {
		t.Fatal("expected error for invalid DSN, got nil")
	}
}

func TestOpenDB_PingFailure_ReturnsError(t *testing.T) {
	_, err := storage.OpenDB(context.Background(), "postgres://user:pass@127.0.0.1:1/nchat?sslmode=disable", 1)
	if err == nil {
		t.Fatal("expected ping error, got nil")
	}
}
