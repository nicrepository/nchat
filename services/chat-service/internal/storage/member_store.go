package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nicrepository/nchat/libs/go/platform/channelmembership"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// MemberStore is the persistence interface for workspace and channel membership.
type MemberStore interface {
	// AddWorkspaceMember inserts an active workspace membership and syncs #geral.
	// ErrAlreadyMember may be returned after a successful commit; the call may
	// have repaired missing #geral membership before returning it. Callers must
	// not treat ErrAlreadyMember as proof that no side effects occurred.
	AddWorkspaceMember(ctx context.Context, workspaceID, userID string, role domain.WorkspaceRole) (domain.WorkspaceMember, error)
	ActivateWorkspaceMember(ctx context.Context, workspaceID, userID string) (domain.WorkspaceMember, error)
	GetWorkspaceMember(ctx context.Context, workspaceID, userID string) (domain.WorkspaceMember, error)
	GetEligibleDMMember(ctx context.Context, workspaceID, userID string) (domain.WorkspaceMember, error)
	AddChannelMember(ctx context.Context, channelID, userID string, role domain.ChannelRole) (domain.ChannelMember, error)
	// AddChannelMembers adds every user in userIDs to channelID, or none (issue
	// #398). callerID is the authenticated actor: the transaction re-establishes
	// their owner/admin membership itself rather than trusting the service's
	// earlier check, so a role revoked in between persists nothing. Eligibility
	// of the targets is decided by the same statement that writes. Returns
	// domain.ErrForbidden — without naming anyone — for a revoked actor or an
	// ineligible target.
	AddChannelMembers(ctx context.Context, workspaceID, channelID, callerID string, userIDs []string) (AddMembersResult, error)
	GetChannelMember(ctx context.Context, channelID, userID string) (domain.ChannelMember, error)
	SearchChannelMembers(ctx context.Context, workspaceID, channelID, prefix string, limit int) ([]domain.MentionCandidate, error)
	SearchDMConversationMembers(ctx context.Context, workspaceID, conversationID, callerID, prefix string, limit int) ([]domain.MentionCandidate, error)
	// ListOnlineChannelMemberProfiles returns the channel's member totals plus up
	// to limit of the members in onlineUserIDs, in one round trip. The presence
	// filter is applied before the limit, so an online member never loses a slot
	// to an offline one. The caller's read access to the channel must already
	// have been settled.
	ListOnlineChannelMemberProfiles(
		ctx context.Context, workspaceID, channelID string, onlineUserIDs []string, limit int,
	) (ChannelMemberPage, error)
	// ListChannelMemberProfilesByIDs resolves the subset of userIDs that are
	// active members of channelID, for the call-participant avatar/name
	// lookup (issue #612). Unlike ListOnlineChannelMemberProfiles this is not
	// online-filtered or capped/ordered — the caller already knows exactly
	// which identities it wants (a LiveKit room's current participant list,
	// bounded by MaxCallParticipantProfileIDs) and gets back only the ones
	// that are actually members of this channel; anyone else is silently
	// omitted rather than invented.
	ListChannelMemberProfilesByIDs(ctx context.Context, workspaceID, channelID string, userIDs []string) ([]domain.CallParticipantProfile, error)
	SearchDMCandidates(ctx context.Context, workspaceID, callerID, prefix string, limit int) ([]domain.DMCandidate, error)
	// SearchChannelMemberCandidates returns active workspace members who are not
	// already active members of channelID (issue #398). The exclusion is a
	// NOT EXISTS in the same statement, so the panel's capped preview is never
	// used to decide who is offerable.
	SearchChannelMemberCandidates(ctx context.Context, workspaceID, channelID, callerID, prefix string, limit int) ([]domain.DMCandidate, error)
	// RemoveChannelMember deletes the channel membership for userID in channelID, scoped to
	// workspaceID. Returns ErrCannotLeaveGeneralChannel if the channel has is_general=true.
	// Returns nil when the membership does not exist (idempotent).
	RemoveChannelMember(ctx context.Context, workspaceID, channelID, userID string) error
	EnsureGeneralMembership(ctx context.Context, workspaceID, userID string) error
	SyncGeneralMemberships(ctx context.Context, workspaceID string) (int64, error)
}

// PGXMemberStore implements MemberStore using a pgx connection pool.
type PGXMemberStore struct {
	pool Pool
}

func NewPGXMemberStore(pool Pool) *PGXMemberStore {
	return &PGXMemberStore{pool: pool}
}

type memberQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// AddWorkspaceMember inserts an active workspace membership and atomically syncs
// the user to that workspace's #geral channel. Existing active members are also
// synced idempotently before ErrAlreadyMember is returned. ErrAlreadyMember may
// be returned after a successful commit; callers must not assume it means
// rollback or no side effects.
func (s *PGXMemberStore) AddWorkspaceMember(ctx context.Context, workspaceID, userID string, role domain.WorkspaceRole) (domain.WorkspaceMember, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.WorkspaceMember{}, fmt.Errorf("begin add workspace member: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	if err := ensureWorkspaceActive(ctx, tx, workspaceID); err != nil {
		return domain.WorkspaceMember{}, err
	}

	m, inserted, err := addWorkspaceMember(ctx, tx, workspaceID, userID, role)
	if err != nil {
		return domain.WorkspaceMember{}, err
	}
	if m.Status == domain.MemberStatusActive {
		if err := ensureGeneralMembership(ctx, tx, workspaceID, userID); err != nil {
			return domain.WorkspaceMember{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.WorkspaceMember{}, fmt.Errorf("commit add workspace member: %w", err)
	}
	committed = true
	if !inserted {
		return domain.WorkspaceMember{}, domain.ErrAlreadyMember
	}
	return m, nil
}

func addWorkspaceMember(ctx context.Context, q memberQuerier, workspaceID, userID string, role domain.WorkspaceRole) (domain.WorkspaceMember, bool, error) {
	var m domain.WorkspaceMember
	err := q.QueryRow(ctx, `
		INSERT INTO chat.workspace_members (workspace_id, user_id, role, status)
		VALUES ($1, $2, $3, 'active')
		ON CONFLICT (workspace_id, user_id) DO NOTHING
		RETURNING workspace_id, user_id, role, status, joined_at`,
		workspaceID, userID, string(role),
	).Scan(&m.WorkspaceID, &m.UserID, (*string)(&m.Role), (*string)(&m.Status), &m.JoinedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			existing, getErr := getWorkspaceMember(ctx, q, workspaceID, userID)
			if getErr != nil {
				return domain.WorkspaceMember{}, false, getErr
			}
			return existing, false, nil
		}
		return domain.WorkspaceMember{}, false, fmt.Errorf("add workspace member: %w", err)
	}
	return m, true, nil
}

// ActivateWorkspaceMember marks an existing workspace member active and
// atomically syncs them to that workspace's #geral channel.
func (s *PGXMemberStore) ActivateWorkspaceMember(ctx context.Context, workspaceID, userID string) (domain.WorkspaceMember, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.WorkspaceMember{}, fmt.Errorf("begin activate workspace member: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	if err := ensureWorkspaceActive(ctx, tx, workspaceID); err != nil {
		return domain.WorkspaceMember{}, err
	}

	var m domain.WorkspaceMember
	err = tx.QueryRow(ctx, `
		UPDATE chat.workspace_members
		SET status = 'active'
		WHERE workspace_id = $1 AND user_id = $2
		RETURNING workspace_id, user_id, role, status, joined_at`,
		workspaceID, userID,
	).Scan(&m.WorkspaceID, &m.UserID, (*string)(&m.Role), (*string)(&m.Status), &m.JoinedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.WorkspaceMember{}, domain.ErrNotFound
		}
		return domain.WorkspaceMember{}, fmt.Errorf("activate workspace member: %w", err)
	}
	if err := ensureGeneralMembership(ctx, tx, workspaceID, userID); err != nil {
		return domain.WorkspaceMember{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.WorkspaceMember{}, fmt.Errorf("commit activate workspace member: %w", err)
	}
	committed = true
	return m, nil
}

func (s *PGXMemberStore) GetWorkspaceMember(ctx context.Context, workspaceID, userID string) (domain.WorkspaceMember, error) {
	return getWorkspaceMember(ctx, s.pool, workspaceID, userID)
}

func getWorkspaceMember(ctx context.Context, q memberQuerier, workspaceID, userID string) (domain.WorkspaceMember, error) {
	var m domain.WorkspaceMember
	err := q.QueryRow(ctx, `
		SELECT wm.workspace_id, wm.user_id, wm.role, wm.status, wm.joined_at
		FROM chat.workspace_members wm
		JOIN chat.workspaces w ON wm.workspace_id = w.id AND w.status = 'active'
		WHERE wm.workspace_id = $1 AND wm.user_id = $2`,
		workspaceID, userID,
	).Scan(&m.WorkspaceID, &m.UserID, (*string)(&m.Role), (*string)(&m.Status), &m.JoinedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.WorkspaceMember{}, domain.ErrNotFound
		}
		return domain.WorkspaceMember{}, fmt.Errorf("get workspace member: %w", err)
	}
	return m, nil
}

// EnsureGeneralMembership idempotently adds an active workspace member to that
// workspace's #geral channel. Suspended and left members return
// ErrMemberInactive and are not inserted.
func (s *PGXMemberStore) EnsureGeneralMembership(ctx context.Context, workspaceID, userID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin ensure general membership: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	if err := ensureWorkspaceActive(ctx, tx, workspaceID); err != nil {
		return err
	}
	member, err := getWorkspaceMember(ctx, tx, workspaceID, userID)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.ErrForbidden
	}
	if err != nil {
		return err
	}
	if member.Status != domain.MemberStatusActive {
		return domain.ErrMemberInactive
	}
	if err := ensureGeneralMembership(ctx, tx, workspaceID, userID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit ensure general membership: %w", err)
	}
	committed = true
	return nil
}

// SyncGeneralMemberships backfills missing #geral memberships for active
// workspace members only, excluding guests (RF-74). It returns the number of
// inserted channel_members rows.
//
// It never removes a row. A guest that already holds a #geral membership — one
// written before RF-74, or one a manager added deliberately — keeps it; the
// backfill only stops creating new ones.
func (s *PGXMemberStore) SyncGeneralMemberships(ctx context.Context, workspaceID string) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin sync general memberships: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	if err := ensureWorkspaceActive(ctx, tx, workspaceID); err != nil {
		return 0, err
	}
	generalChannelID, err := getGeneralChannelID(ctx, tx, workspaceID)
	if err != nil {
		return 0, err
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO chat.channel_members (channel_id, user_id, role)
		SELECT $1, wm.user_id, $3
		FROM chat.workspace_members wm
		WHERE wm.workspace_id = $2
		  AND wm.status = 'active'
		  AND wm.role IN `+generalMembershipRoles+`
		ON CONFLICT (channel_id, user_id) DO NOTHING`,
		generalChannelID, workspaceID, string(domain.ChannelRoleMember),
	)
	if err != nil {
		return 0, fmt.Errorf("sync general memberships: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit sync general memberships: %w", err)
	}
	committed = true
	return tag.RowsAffected(), nil
}

func ensureWorkspaceActive(ctx context.Context, q memberQuerier, workspaceID string) error {
	var status domain.WorkspaceStatus
	err := q.QueryRow(ctx, `
		SELECT status
		FROM chat.workspaces
		WHERE id = $1
		FOR SHARE`,
		workspaceID,
	).Scan((*string)(&status))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		return fmt.Errorf("get workspace status: %w", err)
	}
	if status != domain.WorkspaceStatusActive {
		return domain.ErrForbidden
	}
	return nil
}

// ensureGeneralMembership adds userID to the workspace's #geral channel, unless
// userID is a guest.
//
// The exclusion is the RF-74 guest boundary applied to the one channel every
// member is joined to automatically. #geral is where a workspace's traffic
// lives; auto-joining a guest to it would mean "restricted to the channels it
// was explicitly added to" started with the busiest channel in the workspace
// already granted. A guest reaches #geral the same way it reaches any other
// channel: somebody with domain.CanManageChannelMembers adds it.
//
// The role is not passed in and not read separately: the insert selects it from
// the membership row inside the caller's transaction, so the decision cannot be
// taken against a role that has since changed, and a membership row that is not
// there writes nothing.
func ensureGeneralMembership(ctx context.Context, q memberQuerier, workspaceID, userID string) error {
	generalChannelID, err := getGeneralChannelID(ctx, q, workspaceID)
	if err != nil {
		return err
	}
	if err := addGeneralChannelMember(ctx, q, generalChannelID, workspaceID, userID); err != nil {
		return err
	}
	return nil
}

func getGeneralChannelID(ctx context.Context, q memberQuerier, workspaceID string) (string, error) {
	var channelID string
	err := q.QueryRow(ctx, `
		SELECT id
		FROM chat.channels
		WHERE workspace_id = $1
		  AND is_general = true
		  AND type = 'public'
		  AND status = 'active'
		FOR SHARE`,
		workspaceID,
	).Scan(&channelID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", domain.ErrGeneralChannelMissing
		}
		return "", fmt.Errorf("get general channel: %w", err)
	}
	return channelID, nil
}

// generalMembershipRoles is the role list that receives #geral automatically,
// and the SQL statement of domain.CanReachPublicChannels: a guest is excluded,
// and so is any role this list does not recognise. Shared by the single-user
// path and the backfill so the two cannot disagree about who #geral belongs to.
const generalMembershipRoles = `('owner', 'admin', 'moderator', 'member')`

func addGeneralChannelMember(ctx context.Context, q memberQuerier, channelID, workspaceID, userID string) error {
	_, err := q.Exec(ctx, `
		INSERT INTO chat.channel_members (channel_id, user_id, role)
		SELECT $1, wm.user_id, $4
		FROM chat.workspace_members wm
		WHERE wm.workspace_id = $2
		  AND wm.user_id = $3
		  AND wm.role IN `+generalMembershipRoles+`
		ON CONFLICT (channel_id, user_id) DO NOTHING`,
		channelID, workspaceID, userID, string(domain.ChannelRoleMember),
	)
	if err != nil {
		return fmt.Errorf("add general channel member: %w", err)
	}
	return nil
}

// AddChannelMember inserts a channel membership. Returns ErrAlreadyMember when
// ON CONFLICT DO NOTHING fires (no row returned).
//
// It joins the same serialization protocol as every other writer of
// chat.channel_members. This path returns no count of its own, but it moves the
// count that admin-service reports, so running outside the protocol would let
// it change a channel's membership between another transaction's write and its
// count — and that answer is the one the Admin API promises is the total.
func (s *PGXMemberStore) AddChannelMember(ctx context.Context, channelID, userID string, role domain.ChannelRole) (domain.ChannelMember, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.ChannelMember{}, fmt.Errorf("begin add channel member: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	if _, err := tx.Exec(ctx, channelmembership.LockChannelSQL, channelID); err != nil {
		return domain.ChannelMember{}, fmt.Errorf("lock channel for membership: %w", err)
	}

	var m domain.ChannelMember
	err = tx.QueryRow(ctx, `
		INSERT INTO chat.channel_members (channel_id, user_id, role)
		VALUES ($1, $2, $3)
		ON CONFLICT (channel_id, user_id) DO NOTHING
		RETURNING channel_id, user_id, role, joined_at`,
		channelID, userID, string(role),
	).Scan(&m.ChannelID, &m.UserID, (*string)(&m.Role), &m.JoinedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ChannelMember{}, domain.ErrAlreadyMember
		}
		return domain.ChannelMember{}, fmt.Errorf("add channel member: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ChannelMember{}, fmt.Errorf("commit add channel member: %w", err)
	}
	committed = true
	return m, nil
}

// AddMembersResult reports what one add-members call actually changed.
//
// Added and AlreadyMembers are reported separately because they are different
// outcomes for the UI and, more importantly, because collapsing them would make
// a retry indistinguishable from a first attempt. TotalCount is read after the
// insert, inside the same transaction, so it is the count the caller's own write
// produced rather than a value another writer may already have moved.
type AddMembersResult struct {
	Added          int
	AlreadyMembers int
	TotalCount     int
	// AddedUserIDs is exactly who the transaction inserted — the RETURNING of
	// the INSERT, never the requested list (issue #398).
	//
	// The distinction is the whole point: the request may carry someone who was
	// already a participant, a duplicate, or a user who lost eligibility, and
	// none of those results in a new membership. Fanning a "you were added"
	// signal out to the input would tell people about conversations they were
	// already in, or were never added to. len(AddedUserIDs) == Added.
	AddedUserIDs []string
}

// AddChannelMembers adds every user in userIDs to channelID, or none.
//
// The eligibility test and the insert are one statement, not a check followed by
// a write: a user suspended, deleted or removed from the workspace between the
// service's validation and this call simply produces no row in the `eligible`
// CTE. Because the row count is then compared against the requested count, that
// mismatch aborts the whole transaction — there is no window in which some
// members land and others do not.
//
// Eligibility mirrors the predicate the rest of the channel surface uses: active
// workspace, active workspace membership, an active channel *in that same
// workspace*, and an active, non-deleted auth.users row. The join on
// chat.channels is what makes a channel UUID from another tenant resolve to
// nothing here, so the scoping is in SQL rather than in a Go filter applied
// afterwards.
//
// ON CONFLICT DO NOTHING is what makes the call safe to repeat and safe to run
// concurrently: the (channel_id, user_id) primary key is the arbiter, so a
// double click, a retry after a timeout, and two managers adding the same person
// at the same moment all converge on one row instead of raising a unique
// violation. A user who was already a member is therefore counted in
// AlreadyMembers rather than treated as an error — re-adding somebody is
// idempotent, not a conflict.
//
// Callers must pass canonicalised, de-duplicated IDs. A repeated ID would make
// the requested count exceed what `eligible` can return and the whole batch
// would be refused, which is a confusing way to report a client-side mistake;
// the service de-duplicates before calling for that reason.
func (s *PGXMemberStore) AddChannelMembers(
	ctx context.Context, workspaceID, channelID, callerID string, userIDs []string,
) (AddMembersResult, error) {
	if len(userIDs) == 0 {
		return AddMembersResult{}, domain.ErrNoMembersRequested
	}
	if strings.TrimSpace(callerID) == "" {
		// A missing actor is a wiring bug, never an anonymous add.
		return AddMembersResult{}, domain.ErrForbidden
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return AddMembersResult{}, fmt.Errorf("begin add channel members: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	// Serialize every membership mutation on this channel, before anything else
	// in this transaction. channelmembership.LockChannelSQL is the protocol
	// admin-service obeys too — TotalCount below is read after the insert and
	// before the commit, so two concurrent adds report eleven and twelve rather
	// than eleven twice.
	//
	// It is also the first lock taken, which is what keeps the order canonical:
	// channel, then the actor's membership, then the target rows.
	if _, err := tx.Exec(ctx, channelmembership.LockChannelSQL, channelID); err != nil {
		return AddMembersResult{}, fmt.Errorf("lock channel for membership: %w", err)
	}

	// Re-establish the actor's authority inside the transaction, before anything
	// is written.
	//
	// The service checked this before the transaction opened; in between, the
	// actor can be demoted from admin to member, suspended, or removed from the
	// workspace outright, and without this they would still get to write
	// memberships. Locking the row also serialises this against a concurrent
	// role change rather than merely observing it.
	//
	// The role list is the SQL statement of domain.CanManageChannelMembers,
	// which RF-74 widened from owner/admin to include the workspace moderator.
	// The two must agree; the service's decision is deliberately not passed down
	// as a boolean, because a boolean computed a moment ago is exactly the thing
	// this query exists to distrust.
	//
	// FOR SHARE rather than FOR UPDATE, matching managerAuthorizedWorkspace in
	// channel_category_store.go: demoting a role, suspending a membership and
	// deleting it are all UPDATE/DELETE of that row, which take FOR NO KEY
	// UPDATE and conflict with FOR SHARE — so a revocation is still serialised
	// against an add in flight. Two managers adding people to the same channel
	// have no reason to block each other, which FOR UPDATE would have made them
	// do for no safety gained.
	//
	// Lock order across this file and dm_store.go is the same: conversation or
	// channel scope first, then the actor's membership, then the target rows.
	var actorAuthorized bool
	err = tx.QueryRow(ctx, `
		SELECT true
		FROM chat.workspace_members wm
		JOIN chat.workspaces w
		  ON w.id = wm.workspace_id AND w.status = 'active'
		JOIN chat.channels c
		  ON c.id = $2::uuid AND c.workspace_id = wm.workspace_id AND c.status = 'active'
		WHERE wm.workspace_id = $1::uuid
		  AND wm.user_id = $3::uuid
		  AND wm.status = 'active'
		  AND wm.role IN ('owner', 'admin', 'moderator')
		FOR SHARE OF wm`,
		workspaceID, channelID, callerID,
	).Scan(&actorAuthorized)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Covers a revoked role, a suspended or removed membership, a
			// disabled workspace and a channel that stopped being reachable.
			// One answer for all of them, so the error cannot be used to tell
			// which.
			return AddMembersResult{}, domain.ErrForbidden
		}
		return AddMembersResult{}, fmt.Errorf("lock actor workspace membership: %w", err)
	}

	var eligible, inserted int
	var addedUserIDs []string
	// The eligibility half of this statement is channelmembership.EligibleTargetsCTE,
	// shared with admin-service (issue #579). Who may be added to a channel is a
	// fact about the channel and the person, not about who is asking, so the two
	// writers of chat.channel_members must not each carry their own copy of it.
	// The actor check above stays here: that half really is different in the two
	// services.
	err = tx.QueryRow(ctx, `
		WITH eligible AS (`+channelmembership.EligibleTargetsCTE+`
		),
		inserted AS (
			INSERT INTO chat.channel_members (channel_id, user_id, role)
			SELECT $2::uuid, user_id, $4
			FROM eligible
			ON CONFLICT (channel_id, user_id) DO NOTHING
			RETURNING user_id
		)
		SELECT (SELECT count(*) FROM eligible),
		       (SELECT count(*) FROM inserted),
		       (SELECT COALESCE(array_agg(user_id::text), '{}') FROM inserted)`,
		workspaceID, channelID, userIDs, string(domain.ChannelRoleMember),
	).Scan(&eligible, &inserted, &addedUserIDs)
	if err != nil {
		return AddMembersResult{}, fmt.Errorf("add channel members: %w", err)
	}
	// Fewer eligible rows than requested means at least one user is not an active
	// member of this workspace, or their account is gone, or the channel stopped
	// being reachable. Which one is deliberately not said, and the transaction is
	// rolled back so nothing partial survives.
	if eligible != len(userIDs) {
		return AddMembersResult{}, domain.ErrForbidden
	}

	var total int
	if err := tx.QueryRow(ctx, `
		SELECT count(*)
		FROM chat.channel_members cm
		JOIN chat.channels c
		  ON c.id = cm.channel_id AND c.workspace_id = $1::uuid AND c.status = 'active'
		JOIN chat.workspace_members wm
		  ON wm.workspace_id = c.workspace_id AND wm.user_id = cm.user_id AND wm.status = 'active'
		JOIN auth.users u
		  ON u.id = cm.user_id AND u.status = 'active' AND u.deleted_at IS NULL
		WHERE cm.channel_id = $2::uuid`,
		workspaceID, channelID,
	).Scan(&total); err != nil {
		return AddMembersResult{}, fmt.Errorf("count channel members: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return AddMembersResult{}, fmt.Errorf("commit add channel members: %w", err)
	}
	committed = true
	return AddMembersResult{
		Added:          inserted,
		AlreadyMembers: eligible - inserted,
		TotalCount:     total,
		AddedUserIDs:   addedUserIDs,
	}, nil
}

func (s *PGXMemberStore) GetChannelMember(ctx context.Context, channelID, userID string) (domain.ChannelMember, error) {
	var m domain.ChannelMember
	err := s.pool.QueryRow(ctx, `
		SELECT channel_id, user_id, role, joined_at
		FROM chat.channel_members
		WHERE channel_id = $1 AND user_id = $2`,
		channelID, userID,
	).Scan(&m.ChannelID, &m.UserID, (*string)(&m.Role), &m.JoinedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ChannelMember{}, domain.ErrNotFound
		}
		return domain.ChannelMember{}, fmt.Errorf("get channel member: %w", err)
	}
	return m, nil
}

func (s *PGXMemberStore) SearchChannelMembers(ctx context.Context, workspaceID, channelID, prefix string, limit int) ([]domain.MentionCandidate, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT u.id::text, u.display_name
		FROM chat.channel_members cm
		JOIN chat.channels c
		  ON c.id = cm.channel_id
		 AND c.workspace_id = $1::uuid
		 AND c.status = 'active'
		JOIN chat.workspace_members wm
		  ON wm.workspace_id = c.workspace_id
		 AND wm.user_id = cm.user_id
		 AND wm.status = 'active'
		JOIN auth.users u ON u.id = cm.user_id AND u.status = 'active' AND u.deleted_at IS NULL
		WHERE cm.channel_id = $2::uuid
		  AND left(lower(u.display_name), length($3)) = lower($3)
		ORDER BY lower(u.display_name), u.id
		LIMIT $4`, workspaceID, channelID, prefix, limit)
	if err != nil {
		return nil, fmt.Errorf("search channel members: %w", err)
	}
	defer rows.Close()
	results := make([]domain.MentionCandidate, 0, limit)
	for rows.Next() {
		var candidate domain.MentionCandidate
		candidate.Type = domain.MentionTypeUser
		if err := rows.Scan(&candidate.ID, &candidate.Label); err != nil {
			return nil, fmt.Errorf("scan channel member mention: %w", err)
		}
		results = append(results, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate channel member mentions: %w", err)
	}
	return results, nil
}

func (s *PGXMemberStore) SearchDMConversationMembers(ctx context.Context, workspaceID, conversationID, callerID, prefix string, limit int) ([]domain.MentionCandidate, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT u.id::text, u.display_name
		FROM chat.dm_conversations dc
		JOIN chat.workspaces w
		  ON w.id = dc.workspace_id AND w.status = 'active'
		JOIN chat.dm_members caller
		  ON caller.conversation_id = dc.id AND caller.user_id = $3::uuid AND caller.status = 'active'
		JOIN chat.workspace_members caller_wm
		  ON caller_wm.workspace_id = dc.workspace_id AND caller_wm.user_id = caller.user_id AND caller_wm.status = 'active'
		JOIN chat.dm_members candidate
		  ON candidate.conversation_id = dc.id AND candidate.status = 'active'
		JOIN chat.workspace_members candidate_wm
		  ON candidate_wm.workspace_id = dc.workspace_id AND candidate_wm.user_id = candidate.user_id AND candidate_wm.status = 'active'
		JOIN auth.users u
		  ON u.id = candidate.user_id AND u.status = 'active' AND u.deleted_at IS NULL
		WHERE dc.id = $2::uuid
		  AND dc.workspace_id = $1::uuid
		  AND dc.type = 'group'
		  AND dc.status = 'active'
		  AND left(lower(u.display_name), length($4)) = lower($4)
		ORDER BY lower(u.display_name), u.id
		LIMIT $5`, workspaceID, conversationID, callerID, prefix, limit)
	if err != nil {
		return nil, fmt.Errorf("search dm conversation members: %w", err)
	}
	defer rows.Close()
	results := make([]domain.MentionCandidate, 0, limit)
	for rows.Next() {
		candidate := domain.MentionCandidate{Type: domain.MentionTypeUser}
		if err := rows.Scan(&candidate.ID, &candidate.Label); err != nil {
			return nil, fmt.Errorf("scan dm conversation member mention: %w", err)
		}
		results = append(results, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate dm conversation member mentions: %w", err)
	}
	return results, nil
}

// ChannelMemberPage is the channel-details member section: a capped preview of
// the members who are online right now, plus two totals.
//
// The three fields answer three different questions and must never be derived
// from one another:
//   - TotalCount is every active member of the channel, online or not. It is
//     what "12 membros" in the panel reports.
//   - OnlineCount is how many of those are currently online. It can exceed
//     len(Online) when more members are online than the preview shows.
//   - Online is the capped, ordered preview itself.
type ChannelMemberPage struct {
	Online      []domain.ChannelMemberProfile
	OnlineCount int
	TotalCount  int
}

// ListOnlineChannelMemberProfiles returns a channel's member totals and a capped
// preview of the members who are online right now.
//
// onlineUserIDs is the presence snapshot, resolved by the caller from the
// presence source in one batch. Restricting the *rows* by it — rather than
// annotating a page that was already cut to `limit` — is the whole point: an
// online member who sorts 31st alphabetically must still appear, and an offline
// member must never occupy one of the preview's slots. The filter is therefore
// applied inside online_members, before ORDER BY and LIMIT run.
//
// The active-membership predicate lives in exactly one place (the
// active_members CTE) and is the same one SearchChannelMembers uses — active
// channel in the given workspace, active workspace membership, active
// non-deleted user. Both counts and the preview all read from it, so the total,
// the online total and the list cannot disagree about who belongs to the
// channel. The workspace_id filter on chat.channels is what keeps a channel
// UUID from another tenant from ever resolving here, and the user_id filter is
// an intersection: a presence entry for someone who is not a member of this
// channel selects nothing.
//
// The LEFT JOIN LATERAL onto a one-row source guarantees exactly one row even
// when nobody is online, so the totals still come back for an empty preview —
// the same idiom file-service uses to keep a count and an optional payload in a
// single round trip. There is one query per request: no per-member lookup, and
// no second query that could drift from the first one's predicate.
func (s *PGXMemberStore) ListOnlineChannelMemberProfiles(
	ctx context.Context, workspaceID, channelID string, onlineUserIDs []string, limit int,
) (ChannelMemberPage, error) {
	if limit <= 0 || limit > domain.MaxChannelDetailsMembers {
		limit = domain.MaxChannelDetailsMembers
	}
	// A nil slice would be sent as NULL, and `= ANY(NULL)` is NULL rather than
	// false; an empty array is what makes "nobody online" select no rows.
	if onlineUserIDs == nil {
		onlineUserIDs = []string{}
	}
	rows, err := s.pool.Query(ctx, `
		WITH active_members AS (
			SELECT cm.user_id,
			       cm.role::text AS role,
			       COALESCE(
			           NULLIF(BTRIM(u.full_name), ''),
			           NULLIF(BTRIM(u.display_name), ''),
			           ''
			       ) AS display_name,
			       COALESCE(u.avatar_url, '') AS avatar_url
			FROM chat.channel_members cm
			JOIN chat.channels c
			  ON c.id = cm.channel_id
			 AND c.workspace_id = $1::uuid
			 AND c.status = 'active'
			JOIN chat.workspace_members wm
			  ON wm.workspace_id = c.workspace_id
			 AND wm.user_id = cm.user_id
			 AND wm.status = 'active'
			JOIN auth.users u ON u.id = cm.user_id AND u.status = 'active' AND u.deleted_at IS NULL
			WHERE cm.channel_id = $2::uuid
		),
		online_members AS (
			SELECT * FROM active_members WHERE user_id = ANY($3::uuid[])
		)
		SELECT
			(SELECT count(*) FROM active_members) AS total_count,
			(SELECT count(*) FROM online_members) AS online_count,
			page.user_id::text,
			page.display_name,
			page.avatar_url,
			page.role
		FROM (SELECT 1) AS single_row
		LEFT JOIN LATERAL (
			SELECT * FROM online_members
			ORDER BY lower(display_name), user_id
			LIMIT $4
		) AS page ON true`,
		workspaceID, channelID, onlineUserIDs, limit)
	if err != nil {
		return ChannelMemberPage{}, fmt.Errorf("list online channel member profiles: %w", err)
	}
	defer rows.Close()

	page := ChannelMemberPage{Online: make([]domain.ChannelMemberProfile, 0, limit)}
	for rows.Next() {
		var (
			profile     domain.ChannelMemberProfile
			total       int
			online      int
			userID      pgtype.Text
			displayName pgtype.Text
			avatarURL   pgtype.Text
			role        pgtype.Text
		)
		if err := rows.Scan(&total, &online, &userID, &displayName, &avatarURL, &role); err != nil {
			return ChannelMemberPage{}, fmt.Errorf("scan channel member profile: %w", err)
		}
		page.TotalCount = total
		page.OnlineCount = online
		// The lateral join contributes NULL columns when nobody is online. That
		// is the "totals only" row, not a member.
		if !userID.Valid {
			continue
		}
		profile.UserID = userID.String
		profile.DisplayName = displayName.String
		profile.AvatarURL = avatarURL.String
		profile.Role = domain.ChannelRole(role.String)
		page.Online = append(page.Online, profile)
	}
	if err := rows.Err(); err != nil {
		return ChannelMemberPage{}, fmt.Errorf("iterate channel member profiles: %w", err)
	}
	return page, nil
}

// ListChannelMemberProfilesByIDs resolves presentation identities for a
// specific set of user IDs against one channel's active membership (issue
// #612). It reuses the same active-membership predicate as
// ListOnlineChannelMemberProfiles's active_members CTE — workspace-scoped
// active channel, active workspace membership, active non-deleted user — so
// "who may this caller see identities for" never drifts from "who may this
// caller see at all". There is no ORDER BY/LIMIT: the caller already named
// the exact set it wants (a LiveKit room's participants), bounded by
// MaxCallParticipantProfileIDs before this is ever called.
func (s *PGXMemberStore) ListChannelMemberProfilesByIDs(
	ctx context.Context, workspaceID, channelID string, userIDs []string,
) ([]domain.CallParticipantProfile, error) {
	// A nil slice sends NULL, and `= ANY(NULL)` is NULL rather than false;
	// an empty array is what makes "nobody requested" select no rows.
	if userIDs == nil {
		userIDs = []string{}
	}
	rows, err := s.pool.Query(ctx, `
		SELECT cm.user_id::text,
		       COALESCE(
		           NULLIF(BTRIM(u.full_name), ''),
		           NULLIF(BTRIM(u.display_name), ''),
		           ''
		       ) AS display_name,
		       COALESCE(u.avatar_url, '') AS avatar_url
		FROM chat.channel_members cm
		JOIN chat.channels c
		  ON c.id = cm.channel_id
		 AND c.workspace_id = $1::uuid
		 AND c.status = 'active'
		JOIN chat.workspace_members wm
		  ON wm.workspace_id = c.workspace_id
		 AND wm.user_id = cm.user_id
		 AND wm.status = 'active'
		JOIN auth.users u ON u.id = cm.user_id AND u.status = 'active' AND u.deleted_at IS NULL
		WHERE cm.channel_id = $2::uuid
		  AND cm.user_id = ANY($3::uuid[])`,
		workspaceID, channelID, userIDs)
	if err != nil {
		return nil, fmt.Errorf("list channel member profiles by ids: %w", err)
	}
	defer rows.Close()

	profiles := make([]domain.CallParticipantProfile, 0, len(userIDs))
	for rows.Next() {
		var profile domain.CallParticipantProfile
		if err := rows.Scan(&profile.UserID, &profile.DisplayName, &profile.AvatarURL); err != nil {
			return nil, fmt.Errorf("scan channel member profile: %w", err)
		}
		profiles = append(profiles, profile)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate channel member profiles: %w", err)
	}
	return profiles, nil
}

func (s *PGXMemberStore) SearchDMCandidates(ctx context.Context, workspaceID, callerID, prefix string, limit int) ([]domain.DMCandidate, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT u.id::text, u.display_name
		FROM chat.workspace_members wm
		JOIN chat.workspaces w
		  ON w.id = wm.workspace_id AND w.status = 'active'
		JOIN auth.users u
		  ON u.id = wm.user_id AND u.status = 'active' AND u.deleted_at IS NULL
		WHERE wm.workspace_id = $1::uuid
		  AND wm.status = 'active'
		  AND wm.user_id <> $2::uuid
		  AND left(lower(u.display_name), length($3)) = lower($3)
		  AND EXISTS (
		      SELECT 1
		      FROM chat.workspace_members caller
		      WHERE caller.workspace_id = wm.workspace_id
		        AND caller.user_id = $2::uuid
		        AND caller.status = 'active'
		  )
		ORDER BY lower(u.display_name), u.id
		LIMIT $4`, workspaceID, callerID, prefix, limit)
	if err != nil {
		return nil, fmt.Errorf("search dm candidates: %w", err)
	}
	defer rows.Close()

	results := make([]domain.DMCandidate, 0, limit)
	for rows.Next() {
		var candidate domain.DMCandidate
		if err := rows.Scan(&candidate.UserID, &candidate.DisplayName); err != nil {
			return nil, fmt.Errorf("scan dm candidate: %w", err)
		}
		results = append(results, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate dm candidates: %w", err)
	}
	return results, nil
}

// SearchChannelMemberCandidates lists people who could still be added to a
// channel: active members of the workspace, with active accounts, who are not
// already in it.
//
// The exclusion is a NOT EXISTS against chat.channel_members in this very
// statement, and that is the point of the method. The panel's member section is
// a *presence-filtered, capped preview* — an offline member is simply not in it
// — so using it to decide who is offerable made existing members show up as
// selectable. The database knows the full membership; the preview never did.
//
// Everything else mirrors SearchDMCandidates so the two searches cannot drift
// about who counts as an eligible person: the workspace must be active, the
// membership active, the account active and not deleted, and the caller must
// still hold an active membership in the same workspace (the EXISTS below).
// The caller is also excluded from their own results.
//
// Ordering is the same deterministic (lower(display_name), id) the rest of the
// candidate surface uses, so paging is stable.
func (s *PGXMemberStore) SearchChannelMemberCandidates(
	ctx context.Context, workspaceID, channelID, callerID, prefix string, limit int,
) ([]domain.DMCandidate, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT u.id::text, u.display_name
		FROM chat.workspace_members wm
		JOIN chat.workspaces w
		  ON w.id = wm.workspace_id AND w.status = 'active'
		JOIN auth.users u
		  ON u.id = wm.user_id AND u.status = 'active' AND u.deleted_at IS NULL
		WHERE wm.workspace_id = $1::uuid
		  AND wm.status = 'active'
		  AND wm.user_id <> $3::uuid
		  AND left(lower(u.display_name), length($4)) = lower($4)
		  AND EXISTS (
		      SELECT 1
		      FROM chat.workspace_members caller
		      WHERE caller.workspace_id = wm.workspace_id
		        AND caller.user_id = $3::uuid
		        AND caller.status = 'active'
		  )
		  AND NOT EXISTS (
		      SELECT 1
		      FROM chat.channel_members cm
		      JOIN chat.channels c
		        ON c.id = cm.channel_id
		       AND c.workspace_id = wm.workspace_id
		      WHERE cm.channel_id = $2::uuid
		        AND cm.user_id = wm.user_id
		  )
		ORDER BY lower(u.display_name), u.id
		LIMIT $5`, workspaceID, channelID, callerID, prefix, limit)
	if err != nil {
		return nil, fmt.Errorf("search channel member candidates: %w", err)
	}
	defer rows.Close()

	results := make([]domain.DMCandidate, 0, limit)
	for rows.Next() {
		var candidate domain.DMCandidate
		if err := rows.Scan(&candidate.UserID, &candidate.DisplayName); err != nil {
			return nil, fmt.Errorf("scan channel member candidate: %w", err)
		}
		results = append(results, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate channel member candidates: %w", err)
	}
	return results, nil
}

func (s *PGXMemberStore) GetEligibleDMMember(ctx context.Context, workspaceID, userID string) (domain.WorkspaceMember, error) {
	var member domain.WorkspaceMember
	err := s.pool.QueryRow(ctx, `
		SELECT wm.workspace_id, wm.user_id, wm.role, wm.status, wm.joined_at
		FROM chat.workspace_members wm
		JOIN chat.workspaces w
		  ON w.id = wm.workspace_id AND w.status = 'active'
		JOIN auth.users u
		  ON u.id = wm.user_id AND u.status = 'active' AND u.deleted_at IS NULL
		WHERE wm.workspace_id = $1::uuid
		  AND wm.user_id = $2::uuid
		  AND wm.status = 'active'`, workspaceID, userID).Scan(
		&member.WorkspaceID, &member.UserID, (*string)(&member.Role), (*string)(&member.Status), &member.JoinedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.WorkspaceMember{}, domain.ErrNotFound
		}
		return domain.WorkspaceMember{}, fmt.Errorf("get eligible dm member: %w", err)
	}
	return member, nil
}

// RemoveChannelMember deletes a channel membership, scoped to workspaceID.
// Checks is_general first and returns ErrCannotLeaveGeneralChannel to prevent
// bypassing the service-level guard via direct storage calls.
// Returns nil when the channel is not in the workspace or the user is not a member.
func (s *PGXMemberStore) RemoveChannelMember(ctx context.Context, workspaceID, channelID, userID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin remove channel member: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	// Same protocol as every other writer, and taken first: this removal moves
	// the total that admin-service reports as `member_count`, so it must not
	// land between another transaction's write and its count.
	var isGeneral bool
	err = tx.QueryRow(ctx, `
		SELECT is_general FROM chat.channels
		WHERE id = $1 AND workspace_id = $2
		FOR UPDATE`,
		channelID, workspaceID,
	).Scan(&isGeneral)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("check channel for remove: %w", err)
	}
	if isGeneral {
		return domain.ErrCannotLeaveGeneralChannel
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM chat.channel_members
		WHERE channel_id = $1 AND user_id = $2`,
		channelID, userID,
	); err != nil {
		return fmt.Errorf("remove channel member: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit remove channel member: %w", err)
	}
	committed = true
	return nil
}
