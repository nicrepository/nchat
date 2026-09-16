package storage_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	httpapi "github.com/nicrepository/nchat/services/auth-service/internal/http"
	"github.com/nicrepository/nchat/services/auth-service/internal/service"
	"github.com/nicrepository/nchat/services/auth-service/internal/storage"
)

// auth.user_sessions.ip_address is PostgreSQL inet, and inet::text renders a
// host address in CIDR form ("203.0.113.10/32"). net.ParseIP rejects that, so
// the handler's mask fell back to "" and omitempty dropped ip_address from the
// response altogether (issue #859). A mock row cannot reproduce the cast — only
// the real column can — which is why this runs against PostgreSQL.
//
// Gated on AUTH_TEST_DATABASE_URL like the other storage integration tests.

func insertSessionWithIP(t *testing.T, pool *pgxpool.Pool, userID, tokenHash string, ip *string, createdAt time.Time) string {
	t.Helper()
	var id string
	err := pool.QueryRow(context.Background(), `
		INSERT INTO auth.user_sessions (user_id, refresh_token_hash, ip_address, user_agent,
		                                created_at, last_seen_at, idle_expires_at)
		VALUES ($1, $2, $3::inet, 'Mozilla/5.0', $4, $4, $4::timestamptz + interval '1 hour')
		RETURNING id::text`, userID, tokenHash, ip, createdAt).Scan(&id)
	if err != nil {
		t.Fatalf("insert session: %v", err)
	}
	return id
}

func TestPGXDeviceSessionStore_ListSessionsInetToMaskedResponsePostgreSQL(t *testing.T) {
	pool := connectAuthTestDB(t)
	applyAuthMigrations(t, pool)
	ctx := context.Background()
	userID := insertActiveUser(t, pool, "sessions-ip@example.test")

	const rawIPv4 = "203.0.113.10"
	const rawIPv6 = "2001:db8:85a3::8a2e:370:7334"
	ipv4, ipv6 := rawIPv4, rawIPv6
	now := time.Now().UTC().Truncate(time.Second)
	// Newest first is the listing order, so creation times fix the index of each row.
	v4ID := insertSessionWithIP(t, pool, userID, "hash-v4", &ipv4, now)
	insertSessionWithIP(t, pool, userID, "hash-v6", &ipv6, now.Add(-time.Minute))
	insertSessionWithIP(t, pool, userID, "hash-null", nil, now.Add(-2*time.Minute))

	// Pin the PostgreSQL behaviour the bug came from, so the test keeps proving
	// something if the column type or the cast ever changes.
	var castText string
	if err := pool.QueryRow(ctx, `SELECT ip_address::text FROM auth.user_sessions WHERE id = $1`, v4ID).Scan(&castText); err != nil {
		t.Fatalf("read cast: %v", err)
	}
	if castText != rawIPv4+"/32" {
		t.Fatalf("expected inet::text to carry the CIDR suffix, got %q", castText)
	}

	store := storage.NewPGXDeviceSessionStore(pool)
	sessions, err := store.ListSessions(ctx, userID, false, 50)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 3 {
		t.Fatalf("expected 3 sessions, got %d", len(sessions))
	}
	for i, want := range []string{rawIPv4, rawIPv6, ""} {
		if sessions[i].IPAddress != want {
			t.Fatalf("session %d: expected bare host address %q, got %q", i, want, sessions[i].IPAddress)
		}
	}

	// Store → handler: the response carries the mask, never the address.
	tokens, err := service.NewTokenManager(service.TokenConfig{
		HMACSecret: strings.Repeat("a", 32),
		Issuer:     "test-issuer",
		Audience:   "test-audience",
		AccessTTL:  15 * time.Minute,
		RefreshTTL: time.Hour,
	})
	if err != nil {
		t.Fatalf("token manager: %v", err)
	}
	accessToken, _, err := tokens.GenerateAccessToken(userID, v4ID)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/auth/me/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	rec := httptest.NewRecorder()
	httpapi.BearerAuth(tokens)(httpapi.GetMySessions(store)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`"ip_address":"203.0.*.*"`, `"ip_address":"2001:*"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected %s in response: %s", want, body)
		}
	}
	if n := strings.Count(body, `"ip_address"`); n != 2 {
		t.Fatalf("expected ip_address only on the two sessions that have one, got %d: %s", n, body)
	}
	for _, leak := range []string{rawIPv4, "113.10", rawIPv6, "db8", "/32", "/128"} {
		if strings.Contains(body, leak) {
			t.Fatalf("response must not contain %q: %s", leak, body)
		}
	}
}
