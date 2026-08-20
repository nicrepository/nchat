package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nicrepository/nchat/libs/go/platform/authsession"
	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
)

// PGXAdminStore is the only place this service talks to PostgreSQL.
type PGXAdminStore struct {
	pool Pool
}

func NewPGXAdminStore(pool Pool) *PGXAdminStore {
	return &PGXAdminStore{pool: pool}
}

// capabilitiesLateral resolves the union of the capabilities reachable from a
// principal's role bindings. DISTINCT because two roles may grant the same
// capability and the payload must not repeat it.
const capabilitiesLateral = `
	LEFT JOIN LATERAL (
		SELECT COALESCE(array_agg(DISTINCT rc.capability), ARRAY[]::text[]) AS capabilities
		FROM auth.admin_principal_roles AS pr
		JOIN auth.admin_role_capabilities AS rc ON rc.role_slug = pr.role_slug
		WHERE pr.user_id = p.user_id
	) AS caps ON true`

// principalSelect is the identity-and-capability half of both queries below.
// It is one string so the two cannot drift into disagreeing about what a
// capability is or where it comes from.
//
// Capabilities are read on every request rather than stamped into a token,
// which is what makes removing a role take effect immediately instead of at the
// next login.
const principalSelect = `
	SELECT u.email,
	       ` + authsession.DisplayNameExpr + `,
	       COALESCE(u.avatar_url, ''),
	       (p.user_id IS NOT NULL) AS is_admin,
	       COALESCE(caps.capabilities, ARRAY[]::text[])`

const principalJoins = `
	JOIN auth.users AS u ON u.id = s.user_id
	LEFT JOIN auth.admin_principals AS p
	       ON p.user_id = s.user_id AND p.status = 'active'` + capabilitiesLateral

// authorizeHandshakeQuery is used once, at the moment a chat access token is
// exchanged for an administrative session.
//
// It applies authsession.ActiveSessionCTE — the same predicate auth-service,
// chat-service, file-service, search-service and media-service use — so the
// console cannot admit a login the rest of the platform already considers over.
// The idle window is part of that check here because this is a fresh sign-in:
// a token from a session that has already gone idle must not buy a privileged
// session.
var authorizeHandshakeQuery = authsession.ActiveSessionCTE + `
	SELECT u.email,
	       ` + authsession.DisplayNameExpr + `,
	       COALESCE(u.avatar_url, ''),
	       (p.user_id IS NOT NULL) AS is_admin,
	       COALESCE(caps.capabilities, ARRAY[]::text[])
	FROM active_session AS s
	JOIN auth.users AS u ON u.id = s.user_id
	LEFT JOIN auth.admin_principals AS p
	       ON p.user_id = s.user_id AND p.status = 'active'` + capabilitiesLateral

// reauthorizeQuery runs on every subsequent administrative request.
//
// It deliberately drops one clause from the handshake predicate: the chat
// session's *idle* window. The console never calls /auth/refresh — handing it a
// chat credential to keep alive is exactly what this design avoids — so an
// administrator working steadily in the console would otherwise be evicted by a
// timer belonging to a chat tab they are not using. The administrative session
// carries its own idle window, enforced by touchSessionQuery, and it is
// stricter than the chat one.
//
// Everything that is a real revocation is still checked, and checked here
// rather than trusted from the session row: an explicitly revoked login, a
// login past its absolute lifetime, a suspended or deleted user, a principal
// that is no longer an active administrator.
const reauthorizeQuery = principalSelect + `
	FROM auth.user_sessions AS s` + principalJoins + `
	WHERE s.id = $1
	  AND s.user_id = $2
	  AND s.revoked_at IS NULL
	  AND (s.absolute_expires_at IS NULL OR s.absolute_expires_at > now())
	  AND u.status = 'active'
	  AND u.deleted_at IS NULL
	LIMIT 1`

// AuthorizeHandshake returns the administrator behind a live NChat login.
//
// Arguments are (userID, authSessionID) — actor first, session second — for
// every caller in this service. See loadPrincipal for why the SQL binds them
// the other way round.
//
// Status mapping is the security contract of this method:
//   - no active session row  -> ErrUnauthorized (revoked, expired, or the user
//     is suspended/deleted);
//   - active session, no active admin principal -> ErrForbidden.
//
// Being logged in to NChat is therefore never, on its own, administrative
// authority.
func (s *PGXAdminStore) AuthorizeHandshake(ctx context.Context, userID string, authSessionID string) (domain.AdminPrincipal, error) {
	return s.loadPrincipal(ctx, authorizeHandshakeQuery, userID, authSessionID)
}

// ReauthorizeSession re-derives the administrator behind an existing
// administrative session. Same argument order and same status mapping as
// AuthorizeHandshake.
func (s *PGXAdminStore) ReauthorizeSession(ctx context.Context, userID string, authSessionID string) (domain.AdminPrincipal, error) {
	return s.loadPrincipal(ctx, reauthorizeQuery, userID, authSessionID)
}

// loadPrincipal runs one of the two authorization queries above.
//
// Note the bind order. Both queries take $1 = session id and $2 = user id,
// because that is the order authsession.ActiveSessionCTE fixes for every
// service in the platform, while every Go caller here names the *actor* first
// — userID, then authSessionID — to match how the rest of this service reads.
// The swap happens exactly once, here, and nowhere else.
//
// Getting it backwards would not fail loudly: both columns hold UUIDs, so the
// query would simply match nothing and every administrator would be refused.
// TestAuthorizeHandshake_BindsSessionThenUser pins the order against the mock,
// and the PostgreSQL integration test proves it end to end by swapping real
// values and requiring a refusal.
func (s *PGXAdminStore) loadPrincipal(ctx context.Context, query string, userID string, authSessionID string) (domain.AdminPrincipal, error) {
	if s == nil || s.pool == nil {
		return domain.AdminPrincipal{}, domain.ErrUnavailable
	}
	var email, displayName, avatarURL string
	var isAdmin bool
	var capabilities []string
	err := s.pool.QueryRow(ctx, query, authSessionID, userID).
		Scan(&email, &displayName, &avatarURL, &isAdmin, &capabilities)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.AdminPrincipal{}, domain.ErrUnauthorized
		}
		return domain.AdminPrincipal{}, fmt.Errorf("load admin principal: %w", err)
	}
	if !isAdmin {
		return domain.AdminPrincipal{}, domain.ErrForbidden
	}
	return domain.AdminPrincipal{
		UserID:       userID,
		Email:        email,
		DisplayName:  displayName,
		AvatarURL:    avatarURL,
		Capabilities: domain.NewCapabilitySet(toCapabilities(capabilities)),
	}, nil
}

const createSessionQuery = `
	INSERT INTO auth.admin_sessions
	    (user_id, auth_session_id, session_hash, ip_address, user_agent, idle_expires_at, absolute_expires_at)
	VALUES ($1, $2, $3, NULLIF($4, '')::inet, NULLIF($5, ''), $6, $7)
	RETURNING id, idle_expires_at, absolute_expires_at`

// CreateSession records a new administrative session.
//
// The caller passes a hash, never the credential: the opaque value handed to
// the browser exists only in the Set-Cookie header and in the browser.
func (s *PGXAdminStore) CreateSession(ctx context.Context, input domain.AdminSessionInput) (domain.AdminSession, error) {
	if s == nil || s.pool == nil {
		return domain.AdminSession{}, domain.ErrUnavailable
	}
	session := domain.AdminSession{UserID: input.UserID, AuthSessionID: input.AuthSessionID}
	err := s.pool.QueryRow(ctx, createSessionQuery,
		input.UserID, input.AuthSessionID, input.SessionHash,
		input.IPAddress, input.UserAgent,
		input.IdleExpiresAt, input.AbsoluteExpiresAt,
	).Scan(&session.ID, &session.IdleExpiresAt, &session.AbsoluteExpiresAt)
	if err != nil {
		return domain.AdminSession{}, fmt.Errorf("create admin session: %w", err)
	}
	return session, nil
}

// touchSessionQuery is both the lookup and the idle-window renewal.
//
// Doing them in one statement is what removes the window where a request is
// authorized against a row that a concurrent expiry has already invalidated:
// the WHERE clause is evaluated by the UPDATE itself, so a session that is
// revoked, idle-expired or past its absolute lifetime matches no row and
// returns nothing to authorize with.
//
// The renewed idle deadline is capped at the absolute one, so activity can
// never push a session past the lifetime it was created with.
const touchSessionQuery = `
	UPDATE auth.admin_sessions AS s
	SET last_seen_at = now(),
	    idle_expires_at = LEAST(now() + make_interval(secs => $2), s.absolute_expires_at)
	WHERE s.session_hash = $1
	  AND s.revoked_at IS NULL
	  AND s.idle_expires_at > now()
	  AND s.absolute_expires_at > now()
	RETURNING s.id, s.user_id, s.auth_session_id, s.idle_expires_at, s.absolute_expires_at`

// TouchSession validates the administrative session behind a cookie and
// renews its idle window. It returns ErrUnauthorized for every reason a
// session might not be usable, without distinguishing them to the caller.
func (s *PGXAdminStore) TouchSession(ctx context.Context, sessionHash string, idleTTL time.Duration) (domain.AdminSession, error) {
	if s == nil || s.pool == nil {
		return domain.AdminSession{}, domain.ErrUnavailable
	}
	var session domain.AdminSession
	err := s.pool.QueryRow(ctx, touchSessionQuery, sessionHash, idleTTL.Seconds()).
		Scan(&session.ID, &session.UserID, &session.AuthSessionID, &session.IdleExpiresAt, &session.AbsoluteExpiresAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.AdminSession{}, domain.ErrUnauthorized
		}
		return domain.AdminSession{}, fmt.Errorf("touch admin session: %w", err)
	}
	return session, nil
}

const revokeSessionQuery = `
	UPDATE auth.admin_sessions
	SET revoked_at = now(), revoked_reason = $2
	WHERE session_hash = $1 AND revoked_at IS NULL`

// RevokeSession ends an administrative session. Revoking an already-revoked or
// unknown session is not an error: logout must be idempotent, and answering
// differently would tell a caller whether a credential it holds is real.
func (s *PGXAdminStore) RevokeSession(ctx context.Context, sessionHash string, reason string) error {
	if s == nil || s.pool == nil {
		return domain.ErrUnavailable
	}
	if _, err := s.pool.Exec(ctx, revokeSessionQuery, sessionHash, reason); err != nil {
		return fmt.Errorf("revoke admin session: %w", err)
	}
	return nil
}

const insertAuditQuery = `
	INSERT INTO auth.admin_audit_events
	    (actor_user_id, action, resource, result, correlation_id, metadata)
	VALUES (NULLIF($1, '')::uuid, $2, NULLIF($3, ''), $4, NULLIF($5, ''), $6::jsonb)`

// AppendAudit writes one audit row. Metadata is marshalled from a
// map[string]string the producer built field by field, so nothing reaches the
// trail that a caller did not name explicitly.
func (s *PGXAdminStore) AppendAudit(ctx context.Context, event domain.AuditEvent) error {
	if s == nil || s.pool == nil {
		return domain.ErrUnavailable
	}
	metadata := []byte("{}")
	if len(event.Metadata) > 0 {
		encoded, err := json.Marshal(event.Metadata)
		if err != nil {
			return fmt.Errorf("encode audit metadata: %w", err)
		}
		metadata = encoded
	}
	_, err := s.pool.Exec(ctx, insertAuditQuery,
		event.ActorUserID, event.Action, event.Resource, string(event.Result), event.CorrelationID, string(metadata),
	)
	if err != nil {
		return fmt.Errorf("append admin audit event: %w", err)
	}
	return nil
}

const listAuditQuery = `
	SELECT e.id, e.occurred_at, COALESCE(e.actor_user_id::text, ''), COALESCE(u.email::text, ''),
	       e.action, COALESCE(e.resource, ''), e.result, COALESCE(e.correlation_id, '')
	FROM auth.admin_audit_events AS e
	LEFT JOIN auth.users AS u ON u.id = e.actor_user_id
	ORDER BY e.occurred_at DESC, e.id DESC
	LIMIT $1`

// ListAuditEvents returns the most recent audit rows. The limit is applied by
// the caller's validated value, never by a raw query parameter.
func (s *PGXAdminStore) ListAuditEvents(ctx context.Context, limit int) ([]domain.AuditEntry, error) {
	if s == nil || s.pool == nil {
		return nil, domain.ErrUnavailable
	}
	rows, err := s.pool.Query(ctx, listAuditQuery, limit)
	if err != nil {
		return nil, fmt.Errorf("list admin audit events: %w", err)
	}
	defer rows.Close()

	entries := make([]domain.AuditEntry, 0, limit)
	for rows.Next() {
		var entry domain.AuditEntry
		var result string
		if err := rows.Scan(&entry.ID, &entry.OccurredAt, &entry.ActorUserID, &entry.ActorEmail,
			&entry.Action, &entry.Resource, &result, &entry.CorrelationID); err != nil {
			return nil, fmt.Errorf("scan admin audit event: %w", err)
		}
		entry.Result = domain.AuditResult(result)
		entry.OccurredAt = entry.OccurredAt.UTC()
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read admin audit events: %w", err)
	}
	return entries, nil
}

// Ping exposes the pool's health for the readiness probe.
func (s *PGXAdminStore) Ping(ctx context.Context) error {
	if s == nil || s.pool == nil {
		return domain.ErrUnavailable
	}
	return s.pool.Ping(ctx)
}

func toCapabilities(raw []string) []domain.Capability {
	capabilities := make([]domain.Capability, 0, len(raw))
	for _, value := range raw {
		capabilities = append(capabilities, domain.Capability(value))
	}
	return capabilities
}
