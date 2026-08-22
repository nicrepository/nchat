package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
)

// Transactional authorization for privileged writes (CWE-367).
//
// The middleware authorizes a request when it arrives. A write commits some
// time later — after the body was read, after validation, after a plan was
// computed — and in that interval an administrator's authority can be taken
// away: a role revoked, the principal suspended, the account disabled, the
// console session ended. Nothing about the snapshot the middleware produced
// changes when any of that happens, so a write that consults only the snapshot
// can commit with authority that no longer exists.
//
// Closing that window needs two halves, and neither works alone:
//
//   - the write re-derives the answer from the database *inside* the
//     transaction that performs it, so the check and the write cannot be
//     separated by anything;
//   - it takes row locks that the revocation paths also take, so a revocation
//     cannot slip between the check and the commit. Checking without locking
//     would only make the window smaller.
//
// The result is a serialization: a revocation that commits first makes the
// write fail, and a write that takes the locks first makes the revocation wait.
// A write that wins the order genuinely happened before the revocation, which
// is the strongest property available — nothing can retroactively unmake a
// commit that was ordered first.

// LOCK ORDER — the one canonical order for this service.
//
//	1. auth.admin_sessions  (the acting session)
//	2. auth.admin_principals (the acting principal)
//	3. the rows the mutation itself writes
//
// authorizeMutationTx takes 1 and 2 together in a single statement; every
// mutation then takes 3. The revocation paths each take a subset:
//
//	RevokeSession                   -> 1 only (its UPDATE locks the row)
//	GrantAdminRole / RevokeAdminRole -> 2 only
//	UpdateUserStatus                -> auth.users, then 2
//
// No path takes 1 and 2 in the opposite order, and no revocation touches 3, so
// there is no cycle. Adding a privileged write means calling this first and
// keeping that order; adding a revocation means locking the principal row.

// authorizeMutationLockQuery re-proves the acting administrator, and locks the
// two rows a revocation would have to change to take that authority away.
//
// Every clause is a revocation somebody can perform right now:
//
//	s.revoked_at IS NULL              the console session was not ended
//	s.idle/absolute_expires_at        it has not run out
//	p.status = 'active'               the principal is not suspended
//	u.status / u.deleted_at           the account is not suspended or deleted
//	us.revoked_at / us.absolute…      the login behind it is still valid
//
// FOR UPDATE OF s, p locks only those two rows — one session and one principal,
// never a table and never another administrator's rows. auth.users and
// auth.user_sessions are read but not locked: suspension takes the principal
// lock too, so it cannot commit while this transaction holds it.
const authorizeMutationLockQuery = `
	SELECT p.user_id::text
	FROM auth.admin_sessions AS s
	JOIN auth.admin_principals AS p ON p.user_id = s.user_id
	JOIN auth.users AS u ON u.id = s.user_id
	JOIN auth.user_sessions AS us ON us.id = s.auth_session_id
	WHERE s.id = $1::uuid
	  AND s.user_id = $2::uuid
	  AND s.revoked_at IS NULL
	  AND s.idle_expires_at > now()
	  AND s.absolute_expires_at > now()
	  AND p.status = 'active'
	  AND u.status = 'active'
	  AND u.deleted_at IS NULL
	  AND us.revoked_at IS NULL
	  AND (us.absolute_expires_at IS NULL OR us.absolute_expires_at > now())
	FOR UPDATE OF s, p`

// authorizeMutationCapabilityQuery re-reads the capabilities the principal
// holds right now.
//
// A separate statement because PostgreSQL refuses FOR UPDATE alongside an
// aggregate. It is safe to be separate: it runs under the locks the statement
// above already holds, so no role change can land between the two.
//
// auth.admin_role_capabilities is fixed by migration — the platform has no
// endpoint that edits it — so what a role grants cannot change underneath this;
// only which roles the principal holds can, and that is what the lock covers.
const authorizeMutationCapabilityQuery = `
	SELECT COALESCE(array_agg(DISTINCT rc.capability), ARRAY[]::text[])
	FROM auth.admin_principal_roles AS pr
	JOIN auth.admin_role_capabilities AS rc ON rc.role_slug = pr.role_slug
	WHERE pr.user_id = $1::uuid`

// authorizeMutationTx proves, inside the caller's transaction, that this
// administrator may still perform this mutation.
//
// Call it before the first write of any privileged transaction. It returns:
//
//   - ErrUnauthorized when the session, the login or the account is no longer
//     usable — the caller must prove who they are again;
//   - ErrForbidden when the identity holds but the capability does not — the
//     caller is known and still not allowed.
//
// Same two answers the middleware gives, so a revocation that lands mid-request
// looks to a client exactly like one that landed before it, and neither says
// which role went away.
func authorizeMutationTx(ctx context.Context, tx pgx.Tx, authorization domain.MutationAuthorization) error {
	if !authorization.Valid() {
		// A caller that did not populate the authorization is refused rather
		// than checked against nothing.
		return domain.ErrForbidden
	}
	var principalID string
	err := tx.QueryRow(ctx, authorizeMutationLockQuery, authorization.SessionID, authorization.UserID).Scan(&principalID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrUnauthorized
		}
		return fmt.Errorf("authorize mutation: %w", err)
	}

	var granted []string
	if err := tx.QueryRow(ctx, authorizeMutationCapabilityQuery, principalID).Scan(&granted); err != nil {
		return fmt.Errorf("authorize mutation capabilities: %w", err)
	}
	// toCapabilities and NewCapabilitySet are the same pair the session guard
	// uses, so this decision and the middleware's cannot disagree about what a
	// capability grants — including the superuser rule and the refusal of any
	// capability this build does not define.
	if !domain.NewCapabilitySet(toCapabilities(granted)).Has(authorization.Capability) {
		return domain.ErrForbidden
	}
	return nil
}

// lockAdminPrincipalTx takes the authorization anchor for a principal whose
// authority is about to change.
//
// Every revocation path calls it, which is what makes those paths contend with
// privileged writes instead of racing them. A target that is not an
// administrator has no anchor and needs none: there is no authority to take
// away, so an absent row is not an error here.
func lockAdminPrincipalTx(ctx context.Context, tx pgx.Tx, userID string) error {
	_, err := tx.Exec(ctx,
		`SELECT 1 FROM auth.admin_principals WHERE user_id = $1::uuid FOR UPDATE`, userID)
	if err != nil {
		return fmt.Errorf("lock admin principal: %w", err)
	}
	return nil
}
