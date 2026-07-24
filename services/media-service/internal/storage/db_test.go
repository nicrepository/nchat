package storage

import (
	"context"
	"strings"
	"testing"
)

func TestOpenDBRejectsInvalidDSNWithoutLeakingCredentials(t *testing.T) {
	const dsn = "postgres://user:secret@%gh&%ij"
	pool, err := OpenDB(context.Background(), dsn, 1)
	if err == nil || pool != nil {
		t.Fatalf("expected invalid DSN failure, pool=%v err=%v", pool, err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("database error leaked credentials: %v", err)
	}
}
