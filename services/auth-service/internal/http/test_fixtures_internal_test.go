package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/http/httptest"
	"testing"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

func makeInternalTestOpaqueValue(label string) string {
	sum := sha256.Sum256([]byte("nchat-http-internal-test:" + label))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func TestDeleteAllMySessions_NoSidInContext_Returns401(t *testing.T) {
	svc := &fakeSessionManager{}
	handler := DeleteAllMySessions(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("DELETE", "/auth/me/sessions", nil)
	// Inject userID but no sessionID (ctxKeySessionID defaults to "")
	ctx := context.WithValue(req.Context(), ctxKeyUserID, "user-nosid")
	req = req.WithContext(ctx)
	handler.ServeHTTP(rec, req)

	if rec.Code != 401 {
		t.Fatalf("expected 401 when sid absent, got %d: %s", rec.Code, rec.Body.String())
	}
}

// fakeSessionManager is a minimal stub for internal tests.
type fakeSessionManager struct {
	revokeAllErr error
	lastExcept   string
}

func (f *fakeSessionManager) ListSessions(_ context.Context, _ string, _ bool, _ int) ([]domain.SessionInfo, error) {
	return nil, nil
}
func (f *fakeSessionManager) RevokeSession(_ context.Context, _, _ string) error { return nil }
func (f *fakeSessionManager) RevokeAllSessionsExcept(_ context.Context, _, exceptSID string) error {
	f.lastExcept = exceptSID
	return f.revokeAllErr
}
func (f *fakeSessionManager) ValidateActiveSession(_ context.Context, _, _ string) error {
	return nil
}
