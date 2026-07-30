package httpapi

import (
	"context"
	"net/http"
	"time"
)

// WithAdminContext returns r carrying the workspace and actor that
// RequireWorkspaceAdmin resolves from the session.
//
// The context keys are unexported on purpose: no handler may accept a
// workspace or an actor from anywhere but the guard chain. Tests in the
// external httpapi_test package still need to exercise handlers in isolation,
// so this is the one sanctioned way to stand in for the guard — and being
// test-only, it cannot widen the production surface.
func WithAdminContext(r *http.Request, workspaceID, actorID string) *http.Request {
	ctx := context.WithValue(r.Context(), ctxKeyUserID, actorID)
	ctx = context.WithValue(ctx, ctxKeyWorkspaceID, workspaceID)
	return r.WithContext(ctx)
}

// AllowAllBootstrapAttemptsForTest is a recorder that never limits, for tests
// in the external httpapi_test package that exercise routes behind the
// bootstrap guard without being about the limiter. Tests that *are* about the
// limiter script their own recorder.
type AllowAllBootstrapAttemptsForTest struct{}

func (AllowAllBootstrapAttemptsForTest) RecordAttempt(context.Context, string, int, time.Duration) (bool, error) {
	return true, nil
}

// allowAllBootstrapAttempts is the same thing for in-package tests.
type allowAllBootstrapAttempts struct{}

func (allowAllBootstrapAttempts) RecordAttempt(context.Context, string, int, time.Duration) (bool, error) {
	return true, nil
}
