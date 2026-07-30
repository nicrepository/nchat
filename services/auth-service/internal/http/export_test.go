package httpapi

import (
	"context"
	"net/http"
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
