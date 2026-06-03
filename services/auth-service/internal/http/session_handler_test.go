package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	httpapi "github.com/nicrepository/nchat/services/auth-service/internal/http"
)

// ---- mock -------------------------------------------------------------------

type mockSessionManager struct {
	sessions     []domain.SessionInfo
	listErr      error
	revokeErr    error
	revokeAllErr error

	lastUserID         string
	lastSessionID      string
	lastExceptSession  string
	lastIncludeRevoked bool
	lastLimit          int
}

func (m *mockSessionManager) ListSessions(_ context.Context, userID string, includeRevoked bool, limit int) ([]domain.SessionInfo, error) {
	m.lastUserID = userID
	m.lastIncludeRevoked = includeRevoked
	m.lastLimit = limit
	return m.sessions, m.listErr
}
func (m *mockSessionManager) RevokeSession(_ context.Context, sessionID, userID string) error {
	m.lastSessionID = sessionID
	m.lastUserID = userID
	return m.revokeErr
}
func (m *mockSessionManager) RevokeAllSessionsExcept(_ context.Context, userID, exceptSessionID string) error {
	m.lastUserID = userID
	m.lastExceptSession = exceptSessionID
	return m.revokeAllErr
}
func (m *mockSessionManager) ValidateActiveSession(_ context.Context, _, _ string) error {
	return nil
}

// ---- GET /auth/me/sessions --------------------------------------------------

func TestGetMySessions_NoBearer_Returns401(t *testing.T) {
	tokens := makeTestTokenManager(t)
	handler := httpapi.BearerAuth(tokens)(httpapi.GetMySessions(&mockSessionManager{}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/me/sessions", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	assertErrorCode(t, rec.Body.Bytes(), "unauthorized")
}

func TestGetMySessions_ReturnsOnlyOwnSessions(t *testing.T) {
	tokens := makeTestTokenManager(t)
	userID := "user-sess-1"
	accessToken, _, err := tokens.GenerateAccessToken(userID, "session-curr")
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	now := time.Now().UTC()
	svc := &mockSessionManager{
		sessions: []domain.SessionInfo{
			{ID: "session-curr", CreatedAt: now, LastSeenAt: now, IdleExpiresAt: now.Add(time.Hour)},
			{ID: "session-old", CreatedAt: now.Add(-time.Hour), LastSeenAt: now, IdleExpiresAt: now.Add(time.Hour)},
		},
	}

	handler := httpapi.BearerAuth(tokens)(httpapi.GetMySessions(svc))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/me/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if svc.lastUserID != userID {
		t.Fatalf("expected userID %q passed to service, got %q", userID, svc.lastUserID)
	}

	var envelope struct {
		Data struct {
			Data []struct {
				ID      string `json:"id"`
				Current bool   `json:"current"`
			} `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(envelope.Data.Data) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(envelope.Data.Data))
	}
	var foundCurrent bool
	for _, s := range envelope.Data.Data {
		if s.ID == "session-curr" && s.Current {
			foundCurrent = true
		}
	}
	if !foundCurrent {
		t.Fatal("expected session-curr to have current=true")
	}
}

func TestGetMySessions_HidesSensitiveFields(t *testing.T) {
	tokens := makeTestTokenManager(t)
	accessToken, _, err := tokens.GenerateAccessToken("user-1", "sid-1")
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	now := time.Now().UTC()
	svc := &mockSessionManager{
		sessions: []domain.SessionInfo{
			{ID: "sid-1", IPAddress: "1.2.3.4", UserAgent: "Mozilla/5.0",
				CreatedAt: now, LastSeenAt: now, IdleExpiresAt: now.Add(time.Hour)},
		},
	}

	handler := httpapi.BearerAuth(tokens)(httpapi.GetMySessions(svc))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/me/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()
	if containsAny(body, "refresh_token_hash", "device_fingerprint_hash", "password") {
		t.Fatalf("response must not contain sensitive fields: %s", body)
	}
	if containsAny(body, "1.2.3.4") {
		t.Fatalf("response must not contain raw IP: %s", body)
	}
	if !containsAny(body, "1.2.*.*") {
		t.Fatalf("expected masked IP '1.2.*.*' in response: %s", body)
	}
}

func TestGetMySessions_IncludeRevokedQueryParam(t *testing.T) {
	tokens := makeTestTokenManager(t)
	accessToken, _, err := tokens.GenerateAccessToken("user-1", "sid-1")
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	svc := &mockSessionManager{}
	handler := httpapi.BearerAuth(tokens)(httpapi.GetMySessions(svc))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/me/sessions?include_revoked=true", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !svc.lastIncludeRevoked {
		t.Fatal("expected include_revoked=true passed to service")
	}
}

// ---- DELETE /auth/me/sessions/{session_id} ----------------------------------

func TestDeleteMySession_NoBearer_Returns401(t *testing.T) {
	tokens := makeTestTokenManager(t)
	handler := httpapi.BearerAuth(tokens)(httpapi.DeleteMySession(&mockSessionManager{}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/auth/me/sessions/some-id", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestDeleteMySession_InvalidUUID_Returns400(t *testing.T) {
	tokens := makeTestTokenManager(t)
	accessToken, _, err := tokens.GenerateAccessToken("user-1", "sid-1")
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/auth/me/sessions/not-a-uuid", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.SetPathValue("session_id", "not-a-uuid")

	handler := httpapi.BearerAuth(tokens)(httpapi.DeleteMySession(&mockSessionManager{}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	assertErrorCode(t, rec.Body.Bytes(), "bad_request")
}

func TestDeleteMySession_OwnSession_Returns204(t *testing.T) {
	tokens := makeTestTokenManager(t)
	userID := "user-del-1"
	accessToken, _, err := tokens.GenerateAccessToken(userID, "sid-curr")
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	validSessionID := "123e4567-e89b-12d3-a456-426614174000"
	svc := &mockSessionManager{}

	req := httptest.NewRequest(http.MethodDelete, "/auth/me/sessions/"+validSessionID, nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.SetPathValue("session_id", validSessionID)

	handler := httpapi.BearerAuth(tokens)(httpapi.DeleteMySession(svc))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	if svc.lastUserID != userID {
		t.Fatalf("expected userID %q, got %q", userID, svc.lastUserID)
	}
}

func TestDeleteMySession_CrossUserSession_Returns404(t *testing.T) {
	tokens := makeTestTokenManager(t)
	accessToken, _, err := tokens.GenerateAccessToken("user-1", "sid-1")
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	validSessionID := "123e4567-e89b-12d3-a456-426614174001"
	svc := &mockSessionManager{revokeErr: domain.ErrNotFound}

	req := httptest.NewRequest(http.MethodDelete, "/auth/me/sessions/"+validSessionID, nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.SetPathValue("session_id", validSessionID)

	handler := httpapi.BearerAuth(tokens)(httpapi.DeleteMySession(svc))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
	assertErrorCode(t, rec.Body.Bytes(), "not_found")
}

// ---- DELETE /auth/me/sessions (bulk) ----------------------------------------

func TestDeleteAllMySessions_ValidToken_Returns204(t *testing.T) {
	tokens := makeTestTokenManager(t)
	userID := "user-del-all"
	currentSID := "sid-current-123"
	accessToken, _, err := tokens.GenerateAccessToken(userID, currentSID)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	svc := &mockSessionManager{}
	handler := httpapi.BearerAuth(tokens)(httpapi.DeleteAllMySessions(svc))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/auth/me/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	if svc.lastExceptSession != currentSID {
		t.Fatalf("expected exceptSession %q, got %q", currentSID, svc.lastExceptSession)
	}
}

func TestGetMySessions_ServiceUnavailable_Returns503(t *testing.T) {
	accessToken, tokens := mustAccessToken(t, "user-1", "sid-1")
	handler := httpapi.BearerAuth(tokens)(httpapi.GetMySessions(nil))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/me/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
	assertErrorCode(t, rec.Body.Bytes(), "service_unavailable")
}

func TestGetMySessions_InternalError_Returns500(t *testing.T) {
	accessToken, tokens := mustAccessToken(t, "user-1", "sid-1")
	svc := &mockSessionManager{listErr: errors.New("db down")}
	handler := httpapi.BearerAuth(tokens)(httpapi.GetMySessions(svc))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/me/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
	assertErrorCode(t, rec.Body.Bytes(), "internal_error")
}

func TestGetMySessions_ClampsLimitAndSerializesNullableTimes(t *testing.T) {
	accessToken, tokens := mustAccessToken(t, "user-1", "sid-1")
	now := time.Now().UTC().Truncate(time.Second)
	absolute := now.Add(time.Hour)
	revokedAt := now.Add(-time.Minute)
	svc := &mockSessionManager{sessions: []domain.SessionInfo{{
		ID:                "session-1",
		CreatedAt:         now,
		LastSeenAt:        now,
		IdleExpiresAt:     now.Add(30 * time.Minute),
		AbsoluteExpiresAt: &absolute,
		RevokedAt:         &revokedAt,
	}}}
	handler := httpapi.BearerAuth(tokens)(httpapi.GetMySessions(svc))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/me/sessions?limit=500", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if svc.lastLimit != 100 {
		t.Fatalf("expected limit clamped to 100, got %d", svc.lastLimit)
	}
	body := rec.Body.String()
	if !strings.Contains(body, absolute.Format(time.RFC3339)) || !strings.Contains(body, revokedAt.Format(time.RFC3339)) {
		t.Fatalf("expected absolute_expires_at and revoked_at in response: %s", body)
	}
}

func TestDeleteMySession_ServiceUnavailable_Returns503(t *testing.T) {
	accessToken, tokens := mustAccessToken(t, "user-1", "sid-1")
	sessionID := "123e4567-e89b-12d3-a456-426614174209"
	handler := httpapi.BearerAuth(tokens)(httpapi.DeleteMySession(nil))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/auth/me/sessions/"+sessionID, nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.SetPathValue("session_id", sessionID)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
	assertErrorCode(t, rec.Body.Bytes(), "service_unavailable")
}

func TestDeleteMySession_InternalError_Returns500(t *testing.T) {
	accessToken, tokens := mustAccessToken(t, "user-1", "sid-1")
	sessionID := "123e4567-e89b-12d3-a456-426614174210"
	svc := &mockSessionManager{revokeErr: errors.New("db down")}
	handler := httpapi.BearerAuth(tokens)(httpapi.DeleteMySession(svc))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/auth/me/sessions/"+sessionID, nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.SetPathValue("session_id", sessionID)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
	assertErrorCode(t, rec.Body.Bytes(), "internal_error")
}

func TestDeleteAllMySessions_ServiceUnavailable_Returns503(t *testing.T) {
	accessToken, tokens := mustAccessToken(t, "user-1", "sid-1")
	handler := httpapi.BearerAuth(tokens)(httpapi.DeleteAllMySessions(nil))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/auth/me/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
	assertErrorCode(t, rec.Body.Bytes(), "service_unavailable")
}

func TestDeleteAllMySessions_InternalError_Returns500(t *testing.T) {
	accessToken, tokens := mustAccessToken(t, "user-1", "sid-1")
	svc := &mockSessionManager{revokeAllErr: errors.New("db down")}
	handler := httpapi.BearerAuth(tokens)(httpapi.DeleteAllMySessions(svc))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/auth/me/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
	assertErrorCode(t, rec.Body.Bytes(), "internal_error")
}
