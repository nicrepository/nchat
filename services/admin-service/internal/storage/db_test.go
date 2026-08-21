package storage

import (
	"context"
	"strings"
	"testing"
)

// A DSN carries the database password. A failure to open must never put it in
// an error string that will end up in a log line.
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

func TestOpenDBFailsWhenTheDatabaseIsUnreachable(t *testing.T) {
	// Port 1 on the loopback interface is not listening, so the ping fails
	// without this test needing a database or a network.
	pool, err := OpenDB(context.Background(), "postgres://nchat:secret@127.0.0.1:1/nchat", 1)
	if err == nil {
		pool.Close()
		t.Fatal("expected the ping to fail")
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("database error leaked credentials: %v", err)
	}
}
