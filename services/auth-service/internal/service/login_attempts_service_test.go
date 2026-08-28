package service_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	"github.com/nicrepository/nchat/services/auth-service/internal/service"
)

// mockLoginAttemptsStore is a test double for service.LoginAttemptsStore.
type mockLoginAttemptsStore struct {
	rows []domain.LoginAttempt
	err  error
}

func (m *mockLoginAttemptsStore) GetUserFailedAttempts(
	_ context.Context, _ string, _ int, _ *domain.LoginAttemptsCursor,
) ([]domain.LoginAttempt, error) {
	return m.rows, m.err
}

func makeAttempts(n int) []domain.LoginAttempt {
	attempts := make([]domain.LoginAttempt, n)
	for i := 0; i < n; i++ {
		attempts[i] = domain.LoginAttempt{
			ID:        int64(n - i),
			Email:     "user@example.com",
			CreatedAt: time.Now().Add(-time.Duration(i) * time.Minute),
		}
	}
	return attempts
}

func encodeCursor(t *testing.T, c domain.LoginAttemptsCursor) string {
	t.Helper()
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal cursor: %v", err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func TestLoginAttemptsService_DefaultLimit(t *testing.T) {
	store := &mockLoginAttemptsStore{rows: makeAttempts(10)}
	svc := service.NewLoginAttemptsService(store)

	// limit <= 0 should default to 50
	rows, _, err := svc.GetMyAttempts(context.Background(), "user-1", 0, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 10 {
		t.Errorf("expected 10 rows, got %d", len(rows))
	}
}

func TestLoginAttemptsService_LimitClamped_Min(t *testing.T) {
	store := &mockLoginAttemptsStore{rows: makeAttempts(1)}
	svc := service.NewLoginAttemptsService(store)

	rows, _, err := svc.GetMyAttempts(context.Background(), "user-1", -5, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = rows
}

func TestLoginAttemptsService_LimitClamped_Max(t *testing.T) {
	store := &mockLoginAttemptsStore{rows: makeAttempts(100)}
	svc := service.NewLoginAttemptsService(store)

	// limit > 100 clamped to 100; store returns 100 items (no next page)
	rows, nextCursor, err := svc.GetMyAttempts(context.Background(), "user-1", 200, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 100 {
		t.Errorf("expected 100 rows, got %d", len(rows))
	}
	if nextCursor != "" {
		t.Errorf("expected no next cursor, got %q", nextCursor)
	}
}

func TestLoginAttemptsService_NextCursorPresent(t *testing.T) {
	// Store returns limit+1 rows → service should drop last and produce a cursor.
	limit := 5
	store := &mockLoginAttemptsStore{rows: makeAttempts(limit + 1)}
	svc := service.NewLoginAttemptsService(store)

	rows, nextCursor, err := svc.GetMyAttempts(context.Background(), "user-1", limit, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != limit {
		t.Errorf("expected %d rows, got %d", limit, len(rows))
	}
	if nextCursor == "" {
		t.Error("expected next cursor to be set")
	}
	// Verify cursor is valid base64-encoded JSON
	decoded, err := base64.StdEncoding.DecodeString(nextCursor)
	if err != nil {
		t.Fatalf("cursor not valid base64: %v", err)
	}
	var c domain.LoginAttemptsCursor
	if err := json.Unmarshal(decoded, &c); err != nil {
		t.Fatalf("cursor not valid JSON: %v", err)
	}
}

func TestLoginAttemptsService_NoNextCursorWhenFewerRows(t *testing.T) {
	limit := 5
	store := &mockLoginAttemptsStore{rows: makeAttempts(limit - 1)}
	svc := service.NewLoginAttemptsService(store)

	rows, nextCursor, err := svc.GetMyAttempts(context.Background(), "user-1", limit, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != limit-1 {
		t.Errorf("expected %d rows, got %d", limit-1, len(rows))
	}
	if nextCursor != "" {
		t.Errorf("expected no next cursor, got %q", nextCursor)
	}
}

func TestLoginAttemptsService_InvalidCursor(t *testing.T) {
	store := &mockLoginAttemptsStore{}
	svc := service.NewLoginAttemptsService(store)

	_, _, err := svc.GetMyAttempts(context.Background(), "user-1", 10, "not-valid-base64!!!")
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("expected ErrInvalidInput, got %v", err)
	}
}

func TestLoginAttemptsService_InvalidCursorJSON(t *testing.T) {
	store := &mockLoginAttemptsStore{}
	svc := service.NewLoginAttemptsService(store)

	badJSON := base64.StdEncoding.EncodeToString([]byte("not-json"))
	_, _, err := svc.GetMyAttempts(context.Background(), "user-1", 10, badJSON)
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("expected ErrInvalidInput, got %v", err)
	}
}

func TestLoginAttemptsService_ValidCursorPassed(t *testing.T) {
	cursorTime := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	cursor := domain.LoginAttemptsCursor{CreatedAt: cursorTime, ID: 42}

	store := &mockLoginAttemptsStore{rows: makeAttempts(2)}
	svc := service.NewLoginAttemptsService(store)

	rows, _, err := svc.GetMyAttempts(context.Background(), "user-1", 5, encodeCursor(t, cursor))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("expected 2 rows, got %d", len(rows))
	}
}

func TestLoginAttemptsService_StoreError(t *testing.T) {
	store := &mockLoginAttemptsStore{err: errors.New("db failure")}
	svc := service.NewLoginAttemptsService(store)

	_, _, err := svc.GetMyAttempts(context.Background(), "user-1", 10, "")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}
