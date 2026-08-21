package service

import (
	"context"
	"log/slog"
	"time"

	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
)

// Fields that may never appear in an audit row, enforced by construction: the
// producers below build metadata from named, server-derived values only. No
// header, cookie, token or message body is reachable from here.

const (
	defaultAuditLimit = 50
	maxAuditLimit     = 200
	auditWriteTimeout = 3 * time.Second
)

// AuditStore is the audit persistence this service needs.
type AuditStore interface {
	AppendAudit(ctx context.Context, event domain.AuditEvent) error
	ListAuditEvents(ctx context.Context, limit int) ([]domain.AuditEntry, error)
}

// AuditService records and reads the administrative audit trail.
type AuditService struct {
	store  AuditStore
	logger *slog.Logger
}

func NewAuditService(store AuditStore, logger *slog.Logger) *AuditService {
	if logger == nil {
		logger = slog.Default()
	}
	return &AuditService{store: store, logger: logger}
}

// Record writes one event.
//
// A failed write is logged and swallowed rather than propagated. The trail is
// evidence, not a precondition: making a successful logout depend on an
// available audit table would turn a database hiccup into an administrator who
// cannot end their own session. The failure is loud in the service log, which
// is where an operator would look for a gap in the trail.
//
// It runs on its own timeout so a slow write cannot outlive the request that
// produced it.
func (s *AuditService) Record(ctx context.Context, event domain.AuditEvent) {
	if s == nil || s.store == nil {
		return
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), auditWriteTimeout)
	defer cancel()
	if err := s.store.AppendAudit(writeCtx, event); err != nil {
		// The action and result are safe to log; the metadata map is not
		// re-logged here because the row it failed to write is the only place
		// it belongs.
		s.logger.Error("admin audit write failed", "action", event.Action, "result", string(event.Result))
	}
}

// List returns the most recent audit entries, with the limit clamped to a
// range this endpoint is willing to serve.
func (s *AuditService) List(ctx context.Context, limit int) ([]domain.AuditEntry, error) {
	if s == nil || s.store == nil {
		return nil, domain.ErrUnavailable
	}
	return s.store.ListAuditEvents(ctx, ClampAuditLimit(limit))
}

// ClampAuditLimit normalizes a requested page size. Zero or negative means
// "unspecified" and gets the default; anything above the ceiling is capped
// rather than rejected, so a client cannot turn the parameter into a way to
// ask for the whole table.
func ClampAuditLimit(limit int) int {
	switch {
	case limit <= 0:
		return defaultAuditLimit
	case limit > maxAuditLimit:
		return maxAuditLimit
	default:
		return limit
	}
}
