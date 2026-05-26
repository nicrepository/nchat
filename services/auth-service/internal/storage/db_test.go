package storage_test

import (
	"context"
	"testing"

	"github.com/nicrepository/nchat/services/auth-service/internal/storage"
)

func TestOpenDB_InvalidDSN_ReturnsError(t *testing.T) {
	_, err := storage.OpenDB(context.Background(), "not://a::valid::dsn", 1)
	if err == nil {
		t.Fatal("expected error for invalid DSN, got nil")
	}
}
