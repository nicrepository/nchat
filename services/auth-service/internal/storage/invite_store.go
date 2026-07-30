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

// BootstrapWorkspaceState reports whether the bootstrap window for workspaceID
// is still open, and why not when it is closed.
func (s *PGXInviteStore) BootstrapWorkspaceState(ctx context.Context, workspaceID string) (domain.BootstrapWorkspaceState, error) {
	return bootstrapWorkspaceStateTx(ctx, s.pool, workspaceID)
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
func activeInviteExistsInWorkspaceTx(ctx context.Context, q queryer, workspaceID, email string, now time.Time) (bool, error) {
	var exists int
	// now is the caller's canonical instant, not the server's now(): the same
	// value decides which rows expirePendingInvitesTx just retired, so a row
	// cannot fall between the two clocks and be neither expired nor active.
	err := q.QueryRow(ctx, `
		SELECT 1 FROM auth.user_invites
		WHERE workspace_id = $1::uuid
		  AND email = $2
		  AND status = 'pending'
		  AND accepted_at IS NULL
		  AND revoked_at IS NULL
		  AND expires_at > $3
		LIMIT 1`,
		workspaceID, email, now,
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
// Order matters, and every step below the lock depends on it:
//
//	lock → expire lapsed invites → check for an active one → insert → outbox → commit
//
// The advisory lock is taken first so the budget check, the retirement, the
// duplicate checks and the insert are one atomic decision. The budget is spent
// before any row or outbox entry is written, so a rejected request leaves no
// invite, no outbox entry and therefore no e-mail.
//
// now is the caller's canonical instant, shared with the expiresAt derived from
// it. Reading the clock again here would let a row sit between two readings and
// be judged neither lapsed nor live.
func (s *PGXInviteStore) CreateInvite(ctx context.Context, input domain.AdminInviteInput, tokenHash string, now, expiresAt time.Time, encryptedPayload string, limit domain.InviteRateLimit) (domain.InviteResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.InviteResult{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if err := reserveInviteSlotTx(ctx, tx, input, limit); err != nil {
		return domain.InviteResult{}, err
	}
	// Under the lock, and before the check that would otherwise reject on a row
	// that is only nominally pending.
	if err := expirePendingInvitesTx(ctx, tx, input.WorkspaceID, input.Email, now); err != nil {
		return domain.InviteResult{}, err
	}
	if err := assertInviteAllowedTx(ctx, tx, input.WorkspaceID, input.Email, now); err != nil {
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
// The separator between the parts of every advisory-lock key. It must be a
// byte that cannot occur in a UUID or an e-mail address, so two different keys
// cannot be assembled into the same string — and it must be legal in a
// PostgreSQL text parameter, which rules out NUL: the server rejects a NUL byte
// in text outright, so a NUL separator makes every one of these locks fail.
const lockKeySeparator = "\x1f"

func reserveInviteSlotTx(ctx context.Context, q txQueryer, input domain.AdminInviteInput, limit domain.InviteRateLimit) error {
	if _, err := q.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtext($1)::bigint)`,
		input.WorkspaceID+lockKeySeparator+input.Email,
	); err != nil {
		return fmt.Errorf("lock invite email: %w", err)
	}
	if limit.MaxPerWindow <= 0 {
		return nil
	}

	if _, err := q.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtext($1)::bigint)`,
		"invite-budget"+lockKeySeparator+input.WorkspaceID+lockKeySeparator+input.ActorID,
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
func assertInviteAllowedTx(ctx context.Context, q queryer, workspaceID, email string, now time.Time) error {
	member, err := memberExistsInWorkspaceTx(ctx, q, workspaceID, email)
	if err != nil {
		return err
	}
	if member {
		return domain.ErrAlreadyMember
	}

	pending, err := activeInviteExistsInWorkspaceTx(ctx, q, workspaceID, email, now)
	if err != nil {
		return err
	}
	if pending {
		return domain.ErrInviteAlreadyPending
	}
	return nil
}

// expirePendingInvitesTx retires invites for this (workspace, address) whose
// TTL has run out, so a new one can be issued.
//
// It exists because two rules disagreed about what "pending" means. The service
// treats a row as usable only while expires_at is in the future, but the partial
// unique index is keyed on status alone, so a timed-out row kept occupying the
// slot: re-inviting an address whose invite had simply lapsed failed with a
// conflict, and nothing short of manual intervention cleared it. Moving the row
// to its true state is what reconciles them — after this, the only row left
// pending for the pair is one that really is.
//
// Deliberately narrow. It touches exactly the (workspace, address) being
// invited, and only rows that are still pending and already past their TTL:
// accepted, revoked and live invites are untouched, as is every other workspace
// and address. This is not a sweep — a global cleanup would make one admin's
// invitation rewrite unrelated tenants' rows.
//
// It must run inside the caller's transaction and after the (workspace, email)
// advisory lock, so the retire/check/insert sequence is one atomic decision.
// Zero rows updated is the ordinary case and not an error.
func expirePendingInvitesTx(ctx context.Context, q txQueryer, workspaceID, email string, now time.Time) error {
	// expires_at <= now, not <: at exactly the boundary the TTL has elapsed,
	// and activeInviteExistsInWorkspaceTx uses `> now` for the same instant, so
	// the two together partition the rows with no gap and no overlap.
	if _, err := q.Exec(ctx, `
		UPDATE auth.user_invites
		SET status     = 'expired',
		    updated_at = $3
		WHERE workspace_id = $1::uuid
		  AND email = $2
		  AND status = 'pending'
		  AND accepted_at IS NULL
		  AND revoked_at IS NULL
		  AND expires_at <= $3`,
		workspaceID, email, now,
	); err != nil {
		return fmt.Errorf("expire stale invites: %w", err)
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
	kind := input.Kind
	if kind == "" {
		kind = domain.InviteKindMember
	}
	err := q.QueryRow(ctx, `
		INSERT INTO auth.user_invites
		  (workspace_id, invited_by_user_id, email, token_hash, status, expires_at, invite_kind)
		VALUES ($1::uuid, $2::uuid, $3, $4, 'pending', $5, $6)
		RETURNING id, email::text, workspace_id::text, created_at`,
		input.WorkspaceID, nullableString(input.ActorID), input.Email, tokenHash, expiresAt, string(kind),
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
	kind        domain.InviteKind
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
	invite, err := selectInvite(ctx, q, tokenHash, true)
	if err != nil {
		return acceptableInvite{}, err
	}
	// A legacy invite predating the workspace binding names no workspace, so
	// there is no membership it could create. Migration auth/000008 leaves such
	// rows exactly as it found them rather than revoking them, which is what
	// makes it reversible — refusing them is this layer's job, not the
	// migration's. Distinct from ErrInvalidToken internally so the case is
	// observable in tests and logs; the HTTP layer reports both identically.
	if invite.workspaceID == "" {
		return acceptableInvite{}, domain.ErrInviteWorkspaceMissing
	}
	return invite, nil
}

// peekInviteScope reads an invite without locking it, to learn which workspace
// lock the acceptance must take.
//
// It exists purely for lock ordering. A bootstrap acceptance needs a
// workspace-wide lock *and* the invite row lock, and taking them in that order
// is what avoids a deadlock between two bootstrap invites in one workspace: if
// each transaction locked its own invite row first and then contended for the
// workspace lock, one would hold what the other needs while waiting for what
// the other holds. The value read here is advisory only — loadAcceptableInvite
// re-reads the row FOR UPDATE afterwards and that read is the authoritative one.
func peekInviteScope(ctx context.Context, q queryer, tokenHash string) (acceptableInvite, error) {
	return selectInvite(ctx, q, tokenHash, false)
}

// selectInvite reads the invite named by tokenHash and returns it only when it
// may still be accepted.
//
// Every rejection is ErrInvalidToken, deliberately: unknown, expired, revoked
// and already-accepted are indistinguishable to the caller, so a token cannot
// be probed for which of those it is.
//
// forUpdate takes the row lock that makes concurrent accepts of the same token
// safe: the second transaction blocks on it, then re-reads the row the first one
// committed and sees status = 'accepted'.
func selectInvite(ctx context.Context, q queryer, tokenHash string, forUpdate bool) (acceptableInvite, error) {
	query := `
		SELECT id, email::text, COALESCE(workspace_id::text, ''), invite_kind,
		       accepted_at IS NOT NULL, revoked_at IS NOT NULL, expires_at, status
		FROM auth.user_invites
		WHERE token_hash = $1`
	if forUpdate {
		query += `
		FOR UPDATE`
	}

	var invite acceptableInvite
	var kind string
	var accepted, revoked bool
	var expiresAt time.Time
	var status string
	err := q.QueryRow(ctx, query, tokenHash).
		Scan(&invite.id, &invite.email, &invite.workspaceID, &kind, &accepted, &revoked, &expiresAt, &status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return acceptableInvite{}, domain.ErrInvalidToken
		}
		return acceptableInvite{}, fmt.Errorf("select invite token: %w", err)
	}
	invite.kind = domain.InviteKind(kind)

	if accepted || revoked || status != "pending" || !expiresAt.After(time.Now().UTC()) {
		return acceptableInvite{}, domain.ErrInvalidToken
	}
	return invite, nil
}

// claimWorkspaceBootstrap serialises bootstrap acceptance for one workspace and
// refuses once the workspace has an administrator.
//
// The advisory lock is taken before the invite row is locked — see
// peekInviteScope — so two concurrent bootstrap acceptances queue here rather
// than deadlocking. Whoever holds it sees a workspace state nobody else can
// change, so the "does this workspace already have an owner or admin?" answer
// stays true through the membership insert that would falsify it. That is what
// makes exactly one owner possible: the loser finds the answer already yes.
func claimWorkspaceBootstrap(ctx context.Context, q txQueryer, workspaceID string) error {
	if _, err := q.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtext($1)::bigint)`,
		"workspace-bootstrap"+lockKeySeparator+workspaceID,
	); err != nil {
		return fmt.Errorf("lock workspace bootstrap: %w", err)
	}
	return nil
}

// bootstrapWorkspaceStateTx answers both bootstrap questions in one statement,
// so they hold against each other and against the write that follows.
//
// The membership subquery deliberately does *not* filter on the workspace's own
// status. An earlier version joined chat.workspaces on status = 'active', which
// meant an archived workspace with a real owner reported as uninitialized: the
// bootstrap credential could then mint a bootstrap_owner invite, and accepting
// it granted ownership that took effect the moment anyone reactivated the
// workspace. Having once had an administrator is a fact about the workspace's
// history and cannot be undone by archiving it.
//
// The workspace's status is still read — as Active, a separate field — because
// bootstrap must also refuse to act on a workspace nobody is running. The two
// answers are different questions and are now stored as different fields.
func bootstrapWorkspaceStateTx(ctx context.Context, q queryer, workspaceID string) (domain.BootstrapWorkspaceState, error) {
	var state domain.BootstrapWorkspaceState
	err := q.QueryRow(ctx, `
		SELECT
		    w.status = 'active',
		    EXISTS (
		        SELECT 1
		        FROM chat.workspace_members wm
		        WHERE wm.workspace_id = w.id
		          AND wm.status = 'active'
		          AND wm.role IN ('owner', 'admin')
		    )
		FROM chat.workspaces w
		WHERE w.id = $1::uuid`,
		workspaceID,
	).Scan(&state.Active, &state.Initialized)
	if errors.Is(err, pgx.ErrNoRows) {
		// A configured workspace that does not exist is not an open window.
		return domain.BootstrapWorkspaceState{}, nil
	}
	if err != nil {
		return domain.BootstrapWorkspaceState{}, fmt.Errorf("check bootstrap workspace state: %w", err)
	}
	state.Exists = true
	return state, nil
}

// revokeOtherBootstrapInvites makes every remaining bootstrap invite for this
// workspace unusable, in the transaction that created the first owner.
//
// Without this a second outstanding bootstrap invite would stay pending
// forever: claimWorkspaceBootstrap already refuses it, but a pending row that
// can never be accepted is a standing invitation to a workspace that no longer
// needs one. Revoking is not accepting — the token is consumed as spent, not as
// honoured.
func revokeOtherBootstrapInvites(ctx context.Context, q txQueryer, workspaceID, keepInviteID string) error {
	if _, err := q.Exec(ctx, `
		UPDATE auth.user_invites
		SET status     = 'revoked',
		    revoked_at = now(),
		    updated_at = now()
		WHERE workspace_id = $1::uuid
		  AND invite_kind = 'bootstrap_owner'
		  AND status = 'pending'
		  AND id <> $2`,
		workspaceID, keepInviteID,
	); err != nil {
		return fmt.Errorf("revoke remaining bootstrap invites: %w", err)
	}
	return nil
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
// sends only a token. The role likewise comes from the invite's kind, which is
// written at issuance by the service and is not expressible in any request.
//
// The workspace write is ON CONFLICT DO NOTHING for an ordinary invite, which
// makes re-acceptance idempotent for someone who is already a member. A
// bootstrap invite upgrades instead: its whole purpose is to produce the first
// owner, and an invitee who already held a plain membership must still end up
// owning the workspace, or the bootstrap window would never close.
//
// The channel insert is an INSERT ... SELECT, so a workspace with no general
// channel is a no-op rather than a failed acceptance.
func ensureInviteMembership(ctx context.Context, q txQueryer, workspaceID, userID string, kind domain.InviteKind) error {
	onConflict := `DO NOTHING`
	if kind == domain.InviteKindBootstrapOwner {
		onConflict = `DO UPDATE SET role = 'owner', status = 'active'`
	}
	if _, err := q.Exec(ctx, `
		INSERT INTO chat.workspace_members (workspace_id, user_id, role, status)
		VALUES ($1::uuid, $2::uuid, $3, 'active')
		ON CONFLICT (workspace_id, user_id) `+onConflict,
		workspaceID, userID, kind.MembershipRole(),
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
// Every step runs in one transaction and receives it explicitly — none opens
// its own, and none commits. A failure at any point rolls the whole thing back,
// so there is no state where an invite is consumed without a membership, a
// membership exists while the token is still reusable, or a workspace is left
// half-initialized.
//
// The unlocked peek that opens it is a lock-ordering device, not a decision:
// for a bootstrap invite the workspace lock must be held before the invite row
// is locked, and the workspace is only knowable by reading the invite. Nothing
// is trusted from that read — loadAcceptableInvite re-reads under FOR UPDATE
// and everything below uses that result.
func (s *PGXInviteStore) AcceptInviteTx(ctx context.Context, tokenHash, displayName, fullName, passwordHash string) (domain.AcceptInviteResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.AcceptInviteResult{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	scope, err := peekInviteScope(ctx, tx, tokenHash)
	if err != nil {
		return domain.AcceptInviteResult{}, err
	}
	if scope.kind == domain.InviteKindBootstrapOwner && scope.workspaceID != "" {
		if err := claimWorkspaceBootstrap(ctx, tx, scope.workspaceID); err != nil {
			return domain.AcceptInviteResult{}, err
		}
	}

	invite, err := loadAcceptableInvite(ctx, tx, tokenHash)
	if err != nil {
		return domain.AcceptInviteResult{}, err
	}

	if invite.kind == domain.InviteKindBootstrapOwner {
		state, err := bootstrapWorkspaceStateTx(ctx, tx, invite.workspaceID)
		if err != nil {
			return domain.AcceptInviteResult{}, err
		}
		// Refuse without writing anything: no identity, no membership, and the
		// invite is not marked accepted. Three ways to get here, all closed:
		// the workspace already has an administrator (the usual race, whose
		// winner already revoked this invite's siblings); the workspace is not
		// operational, so granting ownership now would take effect whenever
		// somebody reactivates it; or it no longer exists at all.
		if !state.BootstrapOpen() {
			return domain.AcceptInviteResult{}, domain.ErrInvalidToken
		}
	}

	result, err := resolveInviteIdentity(ctx, tx, invite, displayName, fullName, passwordHash)
	if err != nil {
		return domain.AcceptInviteResult{}, err
	}

	if err := ensureInviteMembership(ctx, tx, invite.workspaceID, result.UserID, invite.kind); err != nil {
		return domain.AcceptInviteResult{}, err
	}

	if err := completeInviteAcceptance(ctx, tx, invite.id, result.UserID); err != nil {
		return domain.AcceptInviteResult{}, err
	}

	if invite.kind == domain.InviteKindBootstrapOwner {
		if err := revokeOtherBootstrapInvites(ctx, tx, invite.workspaceID, invite.id); err != nil {
			return domain.AcceptInviteResult{}, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.AcceptInviteResult{}, fmt.Errorf("commit tx: %w", err)
	}
	return result, nil
}
