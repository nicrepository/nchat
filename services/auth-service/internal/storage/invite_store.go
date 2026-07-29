package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

// PGXInviteStore implements invite persistence using auth.user_invites.
type PGXInviteStore struct {
	pool Pool
}

func NewPGXInviteStore(pool Pool) *PGXInviteStore {
	return &PGXInviteStore{pool: pool}
}

func (s *PGXInviteStore) GetPolicySettings(ctx context.Context) (domain.PolicySettings, error) {
	var p domain.PolicySettings
	err := s.pool.QueryRow(ctx, `
		SELECT min_password_length, require_uppercase, require_lowercase,
		       require_number, require_symbol, failed_login_limit,
		       failed_login_window_minutes, failed_login_lockout_minutes,
		       session_idle_timeout_minutes, max_devices_per_user,
		       password_reset_token_ttl_minutes, invite_token_ttl_hours
		FROM auth.auth_policy_settings
		WHERE id = 1`,
	).Scan(
		&p.MinPasswordLength, &p.RequireUppercase, &p.RequireLowercase,
		&p.RequireNumber, &p.RequireSymbol, &p.FailedLoginLimit,
		&p.FailedLoginWindowMinutes, &p.FailedLoginLockoutMinutes,
		&p.SessionIdleTimeoutMinutes, &p.MaxDevicesPerUser,
		&p.PasswordResetTokenTTLMinutes, &p.InviteTokenTTLHours,
	)
	if err != nil {
		return domain.PolicySettings{}, fmt.Errorf("get policy settings: %w", err)
	}
	return p, nil
}

// WorkspaceHasAdmin reports whether workspaceID already has an active owner or
// admin — that is, whether it has finished bootstrapping.
//
// It asks the same question RequireWorkspaceAdmin asks, from the same table and
// with the same conditions, so "the bootstrap window is closed" and "somebody
// can now use the authenticated endpoint" are the same fact rather than two
// definitions that could drift apart.
func (s *PGXInviteStore) WorkspaceHasAdmin(ctx context.Context, workspaceID string) (bool, error) {
	var exists int
	err := s.pool.QueryRow(ctx, `
		SELECT 1
		FROM chat.workspace_members wm
		JOIN chat.workspaces w
		  ON w.id = wm.workspace_id AND w.status = 'active'
		WHERE wm.workspace_id = $1::uuid
		  AND wm.status = 'active'
		  AND wm.role IN ('owner', 'admin')
		LIMIT 1`,
		workspaceID,
	).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check workspace admin exists: %w", err)
	}
	return true, nil
}

// memberExistsInWorkspaceTx reports whether email already belongs to a member
// of workspaceID.
//
// This replaced a global "does any user have this email" check. That check made
// identity global *and* onboarding global: once someone held an account in any
// workspace, no other workspace could ever invite them. The account stays
// global — the same person keeps one identity — but the question asked here is
// only ever about one workspace.
//
// Members who left are excluded, so re-inviting someone who departed works.
func memberExistsInWorkspaceTx(ctx context.Context, q queryer, workspaceID, email string) (bool, error) {
	var exists int
	err := q.QueryRow(ctx, `
		SELECT 1
		FROM auth.users u
		JOIN chat.workspace_members wm
		  ON wm.user_id = u.id
		 AND wm.workspace_id = $1::uuid
		 AND wm.status <> 'left'
		WHERE u.email = $2
		  AND u.deleted_at IS NULL
		LIMIT 1`,
		workspaceID, email,
	).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check workspace member exists: %w", err)
	}
	return true, nil
}

// activeInviteExistsInWorkspaceTx reports whether a pending invite for email is
// outstanding *in this workspace*. Scoped, for the same reason as above: a
// pending invite from workspace A must not block workspace B.
func activeInviteExistsInWorkspaceTx(ctx context.Context, q queryer, workspaceID, email string) (bool, error) {
	var exists int
	err := q.QueryRow(ctx, `
		SELECT 1 FROM auth.user_invites
		WHERE workspace_id = $1::uuid
		  AND email = $2
		  AND status = 'pending'
		  AND accepted_at IS NULL
		  AND revoked_at IS NULL
		  AND expires_at > now()
		LIMIT 1`,
		workspaceID, email,
	).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check active invite exists: %w", err)
	}
	return true, nil
}

// inviteBudgetExhaustedTx counts invites this actor already created in this
// workspace inside the window.
//
// Counting rows in the invites table is what makes the limit hold across
// replicas: the budget lives where the invites do, and the count runs in the
// same transaction as the insert that would spend it, so two concurrent
// requests cannot both observe the last remaining slot. The row-level lock
// taken earlier in the transaction is what serialises them.
//
// Only created invites count. A request rejected here writes nothing, so a
// blocked caller never extends their own lockout.
func inviteBudgetExhaustedTx(ctx context.Context, q queryer, workspaceID, actorID string, limit domain.InviteRateLimit) (bool, error) {
	if limit.MaxPerWindow <= 0 || limit.WindowMinutes <= 0 {
		return false, nil
	}
	var used int
	// IS NOT DISTINCT FROM, not `=`: a bootstrap-issued invite stores NULL in
	// invited_by_user_id, and `NULL = NULL` is unknown, so `=` would count zero
	// and hand the bootstrap credential an unlimited budget. This way every
	// bootstrap invite in a workspace shares one budget, which is what we want
	// for a credential with no human behind it.
	err := q.QueryRow(ctx, `
		SELECT count(*)
		FROM auth.user_invites
		WHERE invited_by_user_id IS NOT DISTINCT FROM $1::uuid
		  AND workspace_id = $2::uuid
		  AND created_at > now() - make_interval(mins => $3)`,
		nullableString(actorID), workspaceID, limit.WindowMinutes,
	).Scan(&used)
	if err != nil {
		return false, fmt.Errorf("count recent invites: %w", err)
	}
	return used >= limit.MaxPerWindow, nil
}

// CreateInvite persists a workspace-scoped invite and its outbox handoff.
//
// input.WorkspaceID and input.ActorID come from the session, never from the
// request body — see domain.AdminInviteInput. Everything below is scoped to
// that workspace, which is what keeps one workspace's admin from observing or
// obstructing another's onboarding.
//
// Order matters. The advisory lock is taken first so the budget check, the
// duplicate checks and the insert are one atomic decision; the budget is spent
// before any row or outbox entry is written, so a rejected request leaves no
// invite, no outbox entry and therefore no e-mail.
func (s *PGXInviteStore) CreateInvite(ctx context.Context, input domain.AdminInviteInput, tokenHash string, expiresAt time.Time, encryptedPayload string, limit domain.InviteRateLimit) (domain.InviteResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.InviteResult{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if err := reserveInviteSlotTx(ctx, tx, input, limit); err != nil {
		return domain.InviteResult{}, err
	}
	if err := assertInviteAllowedTx(ctx, tx, input.WorkspaceID, input.Email); err != nil {
		return domain.InviteResult{}, err
	}

	result, err := insertInviteTx(ctx, tx, input, tokenHash, expiresAt)
	if err != nil {
		return domain.InviteResult{}, err
	}
	if err := enqueueInviteEmailTx(ctx, tx, input.Email, result.ID, encryptedPayload); err != nil {
		return domain.InviteResult{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.InviteResult{}, fmt.Errorf("commit tx: %w", err)
	}
	return result, nil
}

// reserveInviteSlotTx takes the locks that make the whole creation one atomic
// decision and spends one unit of the issuer's budget.
//
// Two locks, because they guard different things. The e-mail lock includes the
// workspace: two workspaces inviting the same address are independent
// operations and must not serialise on each other. The budget lock is per
// (workspace, issuer): two invites to *different* addresses by the same issuer
// take different e-mail locks and would otherwise both read the same remaining
// slot.
func reserveInviteSlotTx(ctx context.Context, q txQueryer, input domain.AdminInviteInput, limit domain.InviteRateLimit) error {
	if _, err := q.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtext($1)::bigint)`,
		input.WorkspaceID+"\x00"+input.Email,
	); err != nil {
		return fmt.Errorf("lock invite email: %w", err)
	}
	if limit.MaxPerWindow <= 0 {
		return nil
	}

	if _, err := q.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtext($1)::bigint)`,
		"invite-budget\x00"+input.WorkspaceID+"\x00"+input.ActorID,
	); err != nil {
		return fmt.Errorf("lock invite budget: %w", err)
	}
	exhausted, err := inviteBudgetExhaustedTx(ctx, q, input.WorkspaceID, input.ActorID, limit)
	if err != nil {
		return err
	}
	if exhausted {
		return domain.ErrInviteRateLimited
	}
	return nil
}

// assertInviteAllowedTx rejects an address that is already onboarded into this
// workspace, or already has an invite outstanding here. Both questions are
// per-workspace, which is what keeps one tenant from observing or obstructing
// another's onboarding.
func assertInviteAllowedTx(ctx context.Context, q queryer, workspaceID, email string) error {
	member, err := memberExistsInWorkspaceTx(ctx, q, workspaceID, email)
	if err != nil {
		return err
	}
	if member {
		return domain.ErrAlreadyMember
	}

	pending, err := activeInviteExistsInWorkspaceTx(ctx, q, workspaceID, email)
	if err != nil {
		return err
	}
	if pending {
		return domain.ErrInviteAlreadyPending
	}
	return nil
}

// insertInviteTx writes the invite row.
//
// invited_by_user_id is NULL when input.ActorID is empty, which is how a
// bootstrap-issued invite records "no human actor" — see
// domain.BootstrapInviteIssuer. Every other caller supplies a session-derived
// actor, so NULL is not reachable from a browser request.
func insertInviteTx(ctx context.Context, q queryer, input domain.AdminInviteInput, tokenHash string, expiresAt time.Time) (domain.InviteResult, error) {
	var result domain.InviteResult
	err := q.QueryRow(ctx, `
		INSERT INTO auth.user_invites
		  (workspace_id, invited_by_user_id, email, token_hash, status, expires_at)
		VALUES ($1::uuid, $2::uuid, $3, $4, 'pending', $5)
		RETURNING id, email::text, workspace_id::text, created_at`,
		input.WorkspaceID, nullableString(input.ActorID), input.Email, tokenHash, expiresAt,
	).Scan(&result.ID, &result.Email, &result.WorkspaceID, &result.CreatedAt)
	if err != nil {
		// The partial unique index is the backstop for the pending check: it
		// holds even if a caller reaches this store by another path.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgCodeUniqueViolation {
			return domain.InviteResult{}, domain.ErrInviteAlreadyPending
		}
		return domain.InviteResult{}, fmt.Errorf("insert invite: %w", err)
	}
	return result, nil
}

// enqueueInviteEmailTx hands the encrypted invite link to the outbox. It runs
// in the creating transaction, so a rolled-back invite never leaves an e-mail
// queued for an invitation that does not exist.
func enqueueInviteEmailTx(ctx context.Context, q txQueryer, email, inviteID, encryptedPayload string) error {
	if _, err := q.Exec(ctx, `
		INSERT INTO auth.email_outbox
		  (kind, to_email, subject, template_key, invite_id, payload)
		VALUES ('invite', $1, 'You have been invited to NChat', 'auth.invite', $2, $3::jsonb)`,
		email, inviteID, encryptedPayload,
	); err != nil {
		return fmt.Errorf("insert invite outbox: %w", err)
	}
	return nil
}

// upsertInvitedUserTx returns the account for email, creating it when absent.
//
// Identity stays global: one address is one account, with memberships in as
// many workspaces as it was invited to. When the account already exists its
// display name and full name are left alone — the invite's form fields describe
// how the *inviter* labelled the address, and must not overwrite what the
// person already chose for themselves elsewhere.
//
// created reports whether this call inserted the row, which decides whether a
// password credential is written.
func upsertInvitedUserTx(ctx context.Context, q queryer, email, displayName, fullName string) (domain.AcceptInviteResult, bool, error) {
	var result domain.AcceptInviteResult
	err := q.QueryRow(ctx, `
		SELECT id, email::text, display_name, COALESCE(full_name, ''), created_at
		FROM auth.users
		WHERE email = $1
		  AND deleted_at IS NULL`,
		email,
	).Scan(&result.UserID, &result.Email, &result.DisplayName, &result.FullName, &result.CreatedAt)
	if err == nil {
		return result, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.AcceptInviteResult{}, false, fmt.Errorf("select invited user: %w", err)
	}

	err = q.QueryRow(ctx, `
		INSERT INTO auth.users (email, display_name, full_name, status, auth_source, email_verified_at)
		VALUES ($1, $2, $3, 'active', 'manual', now())
		RETURNING id, email::text, display_name, COALESCE(full_name, ''), created_at`,
		email, displayName, nullableString(fullName),
	).Scan(&result.UserID, &result.Email, &result.DisplayName, &result.FullName, &result.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgCodeUniqueViolation {
			// Only reachable if a soft-deleted row holds the address: the
			// advisory lock excludes a concurrent insert.
			return domain.AcceptInviteResult{}, false, domain.ErrDuplicateEmail
		}
		return domain.AcceptInviteResult{}, false, fmt.Errorf("insert invited user: %w", err)
	}
	return result, true, nil
}

// acceptableInvite is a pending invite that passed every state check, reduced
// to the two fields the rest of the acceptance needs. Narrowing here is what
// lets the steps below take a value instead of seven loose variables.
type acceptableInvite struct {
	id          string
	email       string
	workspaceID string
}

// loadAcceptableInvite locks the invite named by tokenHash and returns it only
// when it may still be accepted.
//
// FOR UPDATE is what makes concurrent accepts of the same token safe: the
// second transaction blocks here, then re-reads the row the first one committed
// and sees status = 'accepted'.
//
// Every rejection is ErrInvalidToken, deliberately: unknown, expired, revoked
// and already-accepted are indistinguishable to the caller, so a token cannot
// be probed for which of those it is.
func loadAcceptableInvite(ctx context.Context, q queryer, tokenHash string) (acceptableInvite, error) {
	var invite acceptableInvite
	var accepted, revoked bool
	var expiresAt time.Time
	var status string
	err := q.QueryRow(ctx, `
		SELECT id, email::text, COALESCE(workspace_id::text, ''), accepted_at IS NOT NULL,
		       revoked_at IS NOT NULL, expires_at, status
		FROM auth.user_invites
		WHERE token_hash = $1
		FOR UPDATE`,
		tokenHash,
	).Scan(&invite.id, &invite.email, &invite.workspaceID, &accepted, &revoked, &expiresAt, &status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return acceptableInvite{}, domain.ErrInvalidToken
		}
		return acceptableInvite{}, fmt.Errorf("select invite token: %w", err)
	}

	usable := !accepted && !revoked && status == "pending" && expiresAt.After(time.Now().UTC())
	// A pending invite without a workspace cannot be honoured: there is no
	// membership to create. Migration auth/000008 revoked every such row, so
	// this only triggers on a partially applied database.
	if !usable || invite.workspaceID == "" {
		return acceptableInvite{}, domain.ErrInvalidToken
	}
	return invite, nil
}

// resolveInviteIdentity returns the account that will accept invite, creating
// it when the address is new, and sets the password only in that case.
//
// The advisory lock serialises account creation for this address: two invites
// from different workspaces accepted at the same instant would otherwise both
// try to insert the same e-mail and one would fail on the unique constraint.
//
// An address that already has an account keeps its credential. Honouring the
// submitted password here would turn any invite to an existing user into a
// password reset, which is an account takeover with extra steps.
func resolveInviteIdentity(ctx context.Context, q txQueryer, invite acceptableInvite, displayName, fullName, passwordHash string) (domain.AcceptInviteResult, error) {
	if _, err := q.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1)::bigint)`, invite.email); err != nil {
		return domain.AcceptInviteResult{}, fmt.Errorf("lock invited email: %w", err)
	}

	result, created, err := upsertInvitedUserTx(ctx, q, invite.email, displayName, fullName)
	if err != nil {
		return domain.AcceptInviteResult{}, err
	}
	if !created {
		return result, nil
	}

	if _, err := q.Exec(ctx, `
		INSERT INTO auth.user_password_credentials (user_id, password_hash)
		VALUES ($1, $2)`,
		result.UserID, passwordHash,
	); err != nil {
		return domain.AcceptInviteResult{}, fmt.Errorf("insert invited user credential: %w", err)
	}
	return result, nil
}

// ensureInviteMembership places userID in the invite's workspace and its #geral
// channel.
//
// The membership is the point of the whole flow, and it goes to the workspace
// recorded on the invite — never to one supplied by the accepting client, which
// sends only a token. 'member' is the only role an invite can confer; there is
// no field on the invite that could ask for more.
//
// Both writes are ON CONFLICT DO NOTHING, which is what makes re-acceptance
// idempotent for someone who is already a member. The channel insert is an
// INSERT ... SELECT, so a workspace with no general channel is a no-op rather
// than a failed acceptance.
func ensureInviteMembership(ctx context.Context, q txQueryer, workspaceID, userID string) error {
	if _, err := q.Exec(ctx, `
		INSERT INTO chat.workspace_members (workspace_id, user_id, role, status)
		VALUES ($1::uuid, $2::uuid, 'member', 'active')
		ON CONFLICT (workspace_id, user_id) DO NOTHING`,
		workspaceID, userID,
	); err != nil {
		return fmt.Errorf("insert workspace membership: %w", err)
	}

	if _, err := q.Exec(ctx, `
		INSERT INTO chat.channel_members (channel_id, user_id, role)
		SELECT c.id, $2::uuid, 'member'
		FROM chat.channels c
		WHERE c.workspace_id = $1::uuid
		  AND c.is_general = true
		  AND c.type = 'public'
		  AND c.status = 'active'
		ON CONFLICT (channel_id, user_id) DO NOTHING`,
		workspaceID, userID,
	); err != nil {
		return fmt.Errorf("insert general channel membership: %w", err)
	}
	return nil
}

// completeInviteAcceptance consumes the invite.
//
// The WHERE clause repeats the state checks loadAcceptableInvite already made:
// zero rows updated means another transaction consumed the token first, which
// is the last line of defence against a concurrent double accept producing two
// memberships from one invite.
func completeInviteAcceptance(ctx context.Context, q txQueryer, inviteID, userID string) error {
	update, err := q.Exec(ctx, `
		UPDATE auth.user_invites
		SET accepted_at = now(),
		    accepted_by_user_id = $2,
		    status = 'accepted',
		    updated_at = now()
		WHERE id = $1
		  AND accepted_at IS NULL
		  AND revoked_at IS NULL
		  AND status = 'pending'`,
		inviteID, userID,
	)
	if err != nil {
		return fmt.Errorf("mark invite accepted: %w", err)
	}
	if update.RowsAffected() != 1 {
		return domain.ErrInvalidToken
	}
	return nil
}

// AcceptInviteTx turns an invite token into a workspace membership.
//
// The four steps run in one transaction, and every one of them receives that
// transaction explicitly — none opens its own, and none commits. A failure at
// any point rolls the whole thing back, so there is no state where an invite is
// consumed without a membership, or a membership exists while the token is
// still reusable.
func (s *PGXInviteStore) AcceptInviteTx(ctx context.Context, tokenHash, displayName, fullName, passwordHash string) (domain.AcceptInviteResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.AcceptInviteResult{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	invite, err := loadAcceptableInvite(ctx, tx, tokenHash)
	if err != nil {
		return domain.AcceptInviteResult{}, err
	}

	result, err := resolveInviteIdentity(ctx, tx, invite, displayName, fullName, passwordHash)
	if err != nil {
		return domain.AcceptInviteResult{}, err
	}

	if err := ensureInviteMembership(ctx, tx, invite.workspaceID, result.UserID); err != nil {
		return domain.AcceptInviteResult{}, err
	}

	if err := completeInviteAcceptance(ctx, tx, invite.id, result.UserID); err != nil {
		return domain.AcceptInviteResult{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.AcceptInviteResult{}, fmt.Errorf("commit tx: %w", err)
	}
	return result, nil
}
