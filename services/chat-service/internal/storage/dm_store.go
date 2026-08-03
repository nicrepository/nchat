package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// CreateDirectConversationInput holds storage-only fields for creating or
// reactivating a canonical 1:1 DM.
type CreateDirectConversationInput struct {
	WorkspaceID        string
	CreatedBy          string
	DirectPairKey      string
	ParticipantUserIDs []string
}

type CreateDirectConversationResult struct {
	Conversation domain.DMConversation
	Created      bool
}

// CreateGroupConversationInput holds storage-only fields for group DM creation.
type CreateGroupConversationInput struct {
	WorkspaceID        string
	CreatedBy          string
	Title              string
	ParticipantUserIDs []string
}

// AddGroupParticipantsInput holds storage-only fields for adding participants to
// an existing group conversation (issue #398).
//
// There is deliberately no maximum-participants field. Groups have no fixed
// capacity in this product: the only bound is how many IDs one request may
// carry (domain.MaxAddMembersPerRequest), which the service applies, and
// successive requests may grow a conversation without limit.
type AddGroupParticipantsInput struct {
	WorkspaceID    string
	ConversationID string
	// CallerID is the authenticated actor, passed in so the transaction can
	// re-establish their participation itself instead of trusting that the
	// service checked it moments ago. It never comes from a request body.
	CallerID string
	UserIDs  []string
}

// DMStore is the persistence interface for direct and group DM conversations.
type DMStore interface {
	CreateDirectConversation(ctx context.Context, input CreateDirectConversationInput) (CreateDirectConversationResult, error)
	CreateGroupConversation(ctx context.Context, input CreateGroupConversationInput) (domain.DMConversation, error)
	// AddGroupParticipants adds every user in userIDs to an existing group
	// conversation, or none (issue #398). Returns domain.ErrForbidden when any
	// user is ineligible. There is no capacity conflict: groups have no fixed
	// participant ceiling.
	AddGroupParticipants(ctx context.Context, input AddGroupParticipantsInput) (AddMembersResult, error)
	// ListParticipantProfiles returns up to limit active participants of
	// conversationID in workspaceID plus the total number of active
	// participants, in one round trip. The caller's access to the conversation
	// must already have been settled.
	ListParticipantProfiles(ctx context.Context, workspaceID, conversationID string, limit int) (DMParticipantPage, error)
	// SearchGroupParticipantCandidates returns active workspace members who are
	// not already active participants of conversationID (issue #398). The
	// exclusion is a NOT EXISTS in the same statement, so the panel's capped
	// 30-participant preview is never used to decide who is offerable.
	SearchGroupParticipantCandidates(ctx context.Context, workspaceID, conversationID, callerID, prefix string, limit int) ([]domain.DMCandidate, error)
	ListVisibleConversationsByUser(ctx context.Context, workspaceID, userID string) ([]domain.DMConversation, error)
	// ListVisibleConversationsWithParticipantIDs returns active DM conversations
	// visible to userID, each annotated with the full list of active member user
	// IDs and, for direct 1:1 conversations, the display name of the other
	// participant as seen by userID. A single SQL query (no N+1) is used.
	ListVisibleConversationsWithParticipantIDs(ctx context.Context, workspaceID, userID string) ([]domain.DMConversationWithParticipantIDs, error)
	GetVisibleConversationByID(ctx context.Context, workspaceID, conversationID, userID string) (domain.DMConversation, error)
	// GetDirectCounterpartProfile authorises callerID for conversationID and
	// returns the one active participant who is not them, in a single query.
	// It is the authority for both: any earlier visibility check is a
	// convenience, never the permission this read relies on. It answers
	// ErrNotFound when the caller is not currently authorised *or* no such
	// participant exists — the two are deliberately indistinguishable — and
	// ErrInconsistentDirectConversation when more than one does.
	GetDirectCounterpartProfile(ctx context.Context, workspaceID, conversationID, callerID string) (domain.DMDirectProfile, error)
}

// DMParticipantPage is one capped page of a conversation's participants plus the
// total the same predicate matches. TotalCount is the authoritative figure the
// UI displays; len(Participants) is only how many fit in the page and must never
// be shown as the participant count.
type DMParticipantPage struct {
	Participants []domain.DMParticipantProfile
	TotalCount   int
}

// ListParticipantProfiles returns a capped page of a conversation's active
// participants and the full total, in a single query.
//
// Every predicate that makes a participant "real" is applied here and nowhere
// else: the conversation must be active and belong to workspaceID, the
// participant's dm_members row must be status 'active' (so someone who left is
// gone), they must still be an active member of the workspace, and their
// account must be active and not deleted. That is the same shape
// GetVisibleConversationByID uses to decide the caller may read the
// conversation at all, so "who is in this group" cannot mean one thing for
// access and another for display.
//
// The dc.workspace_id filter is what keeps a conversation UUID from another
// tenant from ever resolving here, and the conversation_id filter is what keeps
// participants of another group out of this one's list.
//
// Unlike the channel panel, presence does not select rows: a group lists every
// active participant and the caller annotates presence afterwards, so an
// offline participant is never dropped and never loses a slot.
//
// COUNT(*) OVER () is evaluated before LIMIT, so the total describes the whole
// matching set and not the page; a second COUNT query would be a second chance
// to drift from this one's predicate.
func (s *PGXDMStore) ListParticipantProfiles(
	ctx context.Context, workspaceID, conversationID string, limit int,
) (DMParticipantPage, error) {
	if limit <= 0 || limit > domain.MaxDMDetailsParticipants {
		limit = domain.MaxDMDetailsParticipants
	}
	rows, err := s.pool.Query(ctx, `
		SELECT u.id::text,
		       COALESCE(
		           NULLIF(BTRIM(u.full_name), ''),
		           NULLIF(BTRIM(u.display_name), ''),
		           ''
		       ) AS display_name,
		       COALESCE(u.avatar_url, '') AS avatar_url,
		       COUNT(*) OVER () AS total_count
		FROM chat.dm_members dm
		JOIN chat.dm_conversations dc
		  ON dc.id = dm.conversation_id
		 AND dc.workspace_id = $1::uuid
		 AND dc.status = 'active'
		JOIN chat.workspace_members wm
		  ON wm.workspace_id = dc.workspace_id
		 AND wm.user_id = dm.user_id
		 AND wm.status = 'active'
		JOIN auth.users u ON u.id = dm.user_id AND u.status = 'active' AND u.deleted_at IS NULL
		WHERE dm.conversation_id = $2::uuid
		  AND dm.status = 'active'
		ORDER BY lower(u.display_name), u.id
		LIMIT $3`, workspaceID, conversationID, limit)
	if err != nil {
		return DMParticipantPage{}, fmt.Errorf("list dm participant profiles: %w", err)
	}
	defer rows.Close()

	page := DMParticipantPage{Participants: make([]domain.DMParticipantProfile, 0, limit)}
	for rows.Next() {
		var profile domain.DMParticipantProfile
		var total int
		if err := rows.Scan(&profile.UserID, &profile.DisplayName, &profile.AvatarURL, &total); err != nil {
			return DMParticipantPage{}, fmt.Errorf("scan dm participant profile: %w", err)
		}
		page.TotalCount = total
		page.Participants = append(page.Participants, profile)
	}
	if err := rows.Err(); err != nil {
		return DMParticipantPage{}, fmt.Errorf("iterate dm participant profiles: %w", err)
	}
	return page, nil
}

// GetDirectCounterpartProfile authorises the caller and resolves who the other
// side of a 1:1 conversation is, in one query (issue #443).
//
// It is authoritative, not a projection of a decision taken earlier. Access is
// re-established here, in the same statement and against the same snapshot that
// reads the profile, so a membership revoked between an earlier visibility check
// and this call cannot leave a stale "yes" standing: the two EXISTS clauses put
// the caller's own dm_members and workspace_members rows into the predicate that
// selects the counterpart, and losing either yields zero rows rather than a
// profile.
//
// The caller's user ID is a *predicate*, never a selector: the query asks for
// the active participants of this conversation who are not the caller, so the
// counterpart cannot be chosen by the client and the caller can never be
// returned as their own profile. Two people with the same display name are
// still two rows with two IDs — identity here is dm.user_id, never a name.
//
// The membership predicates are the same set ListParticipantProfiles applies,
// for the same reason: "who is in this conversation" must not mean one thing
// for access and another for display. The conversation must be active and in
// workspaceID, dc.type must be 'direct' as the database recorded it, each
// participant's dm_members row must be 'active' (so someone who left is gone),
// they must still be an active workspace member, and the counterpart's account
// must be active and not deleted.
//
// The row limit is 2, not 1: one row is the normal case, and a second is what
// distinguishes a corrupt 'direct' row from a healthy one. Taking LIMIT 1 would
// silently pick an arbitrary "other participant" out of a conversation the
// domain says cannot have several.
//
// The projection is explicit and minimal — no SELECT * — so a future column on
// auth.users cannot start flowing into a profile response by accident.
//
// Zero rows is deliberately one answer for several causes — no such
// conversation, another workspace, not a direct row, caller not (or no longer)
// a participant, caller suspended, counterpart gone. Telling them apart would
// take a second query whose only product is a more precise denial, which is
// exactly the thing a caller must not be given.
func (s *PGXDMStore) GetDirectCounterpartProfile(
	ctx context.Context, workspaceID, conversationID, callerID string,
) (domain.DMDirectProfile, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT u.id::text,
		       COALESCE(
		           NULLIF(BTRIM(u.full_name), ''),
		           NULLIF(BTRIM(u.display_name), ''),
		           ''
		       ) AS display_name,
		       COALESCE(u.avatar_url, '') AS avatar_url,
		       COALESCE(u.email::text, '') AS email
		FROM chat.dm_conversations dc
		JOIN chat.dm_members dm
		  ON dm.conversation_id = dc.id
		 AND dm.status = 'active'
		 AND dm.user_id <> $3::uuid
		JOIN chat.workspace_members wm
		  ON wm.workspace_id = dc.workspace_id
		 AND wm.user_id = dm.user_id
		 AND wm.status = 'active'
		JOIN auth.users u ON u.id = dm.user_id AND u.status = 'active' AND u.deleted_at IS NULL
		WHERE dc.id = $2::uuid
		  AND dc.workspace_id = $1::uuid
		  AND dc.status = 'active'
		  AND dc.type = 'direct'
		  AND EXISTS (
		      SELECT 1
		      FROM chat.dm_members caller_dm
		      WHERE caller_dm.conversation_id = dc.id
		        AND caller_dm.user_id = $3::uuid
		        AND caller_dm.status = 'active'
		  )
		  AND EXISTS (
		      SELECT 1
		      FROM chat.workspace_members caller_wm
		      WHERE caller_wm.workspace_id = dc.workspace_id
		        AND caller_wm.user_id = $3::uuid
		        AND caller_wm.status = 'active'
		  )
		LIMIT 2`, workspaceID, conversationID, callerID)
	if err != nil {
		return domain.DMDirectProfile{}, fmt.Errorf("get direct counterpart profile: %w", err)
	}
	defer rows.Close()

	profiles := make([]domain.DMDirectProfile, 0, 2)
	for rows.Next() {
		var profile domain.DMDirectProfile
		if err := rows.Scan(&profile.UserID, &profile.DisplayName, &profile.AvatarURL, &profile.Email); err != nil {
			return domain.DMDirectProfile{}, fmt.Errorf("scan direct counterpart profile: %w", err)
		}
		profiles = append(profiles, profile)
	}
	if err := rows.Err(); err != nil {
		return domain.DMDirectProfile{}, fmt.Errorf("iterate direct counterpart profile: %w", err)
	}
	switch len(profiles) {
	case 1:
		return profiles[0], nil
	case 0:
		return domain.DMDirectProfile{}, domain.ErrNotFound
	default:
		return domain.DMDirectProfile{}, domain.ErrInconsistentDirectConversation
	}
}

// PGXDMStore implements DMStore using a pgx connection pool.
type PGXDMStore struct {
	pool Pool
}

func NewPGXDMStore(pool Pool) *PGXDMStore {
	return &PGXDMStore{pool: pool}
}

func (s *PGXDMStore) CreateDirectConversation(ctx context.Context, input CreateDirectConversationInput) (CreateDirectConversationResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return CreateDirectConversationResult{}, fmt.Errorf("begin create direct conversation: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	result, err := createDirectConversation(ctx, tx, input)
	if err != nil {
		return CreateDirectConversationResult{}, err
	}
	// Which participants this reactivated is not reported: a 1:1 is defined by its
	// pair, so both users belong to it whether the row was written now or before.
	if _, err := upsertEligibleDMMembers(ctx, tx, result.Conversation.ID, input.WorkspaceID, input.ParticipantUserIDs); err != nil {
		return CreateDirectConversationResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return CreateDirectConversationResult{}, fmt.Errorf("commit create direct conversation: %w", err)
	}
	committed = true
	return result, nil
}

func createDirectConversation(ctx context.Context, q dmQuerier, input CreateDirectConversationInput) (CreateDirectConversationResult, error) {
	var conversation domain.DMConversation
	err := q.QueryRow(ctx, `
		INSERT INTO chat.dm_conversations
			(workspace_id, type, title, status, created_by, direct_pair_key)
		VALUES ($1, 'direct', NULL, 'active', $2, $3)
		ON CONFLICT (workspace_id, direct_pair_key) WHERE type = 'direct'
		DO NOTHING
		RETURNING id, workspace_id, type, COALESCE(title, ''), status, created_by,
		          created_at, updated_at`,
		input.WorkspaceID, input.CreatedBy, input.DirectPairKey,
	).Scan(
		&conversation.ID, &conversation.WorkspaceID, (*string)(&conversation.Type),
		&conversation.Title, (*string)(&conversation.Status), &conversation.CreatedBy,
		&conversation.CreatedAt, &conversation.UpdatedAt,
	)
	if err == nil {
		return CreateDirectConversationResult{Conversation: conversation, Created: true}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return CreateDirectConversationResult{}, fmt.Errorf("create direct conversation: %w", err)
	}

	err = q.QueryRow(ctx, `
		UPDATE chat.dm_conversations
		SET status = 'active', updated_at = now()
		WHERE workspace_id = $1
		  AND direct_pair_key = $2
		  AND type = 'direct'
		RETURNING id, workspace_id, type, COALESCE(title, ''), status, created_by,
		          created_at, updated_at`,
		input.WorkspaceID, input.DirectPairKey,
	).Scan(
		&conversation.ID, &conversation.WorkspaceID, (*string)(&conversation.Type),
		&conversation.Title, (*string)(&conversation.Status), &conversation.CreatedBy,
		&conversation.CreatedAt, &conversation.UpdatedAt,
	)
	if err != nil {
		return CreateDirectConversationResult{}, fmt.Errorf("get existing direct conversation: %w", err)
	}
	return CreateDirectConversationResult{Conversation: conversation}, nil
}

func (s *PGXDMStore) CreateGroupConversation(ctx context.Context, input CreateGroupConversationInput) (domain.DMConversation, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.DMConversation{}, fmt.Errorf("begin create group conversation: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	conversation, err := createGroupConversation(ctx, tx, input)
	if err != nil {
		return domain.DMConversation{}, err
	}
	// The conversation was created by the statement above, so every participant
	// here is new by construction and there is nothing to report separately.
	if _, err := upsertEligibleDMMembers(ctx, tx, conversation.ID, input.WorkspaceID, input.ParticipantUserIDs); err != nil {
		return domain.DMConversation{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.DMConversation{}, fmt.Errorf("commit create group conversation: %w", err)
	}
	committed = true
	return conversation, nil
}

func createGroupConversation(ctx context.Context, q dmQuerier, input CreateGroupConversationInput) (domain.DMConversation, error) {
	var title *string
	if input.Title != "" {
		title = &input.Title
	}

	var conversation domain.DMConversation
	err := q.QueryRow(ctx, `
		INSERT INTO chat.dm_conversations
			(workspace_id, type, title, status, created_by)
		VALUES ($1, 'group', $2, 'active', $3)
		RETURNING id, workspace_id, type, COALESCE(title, ''), status, created_by,
		          created_at, updated_at`,
		input.WorkspaceID, title, input.CreatedBy,
	).Scan(
		&conversation.ID, &conversation.WorkspaceID, (*string)(&conversation.Type),
		&conversation.Title, (*string)(&conversation.Status), &conversation.CreatedBy,
		&conversation.CreatedAt, &conversation.UpdatedAt,
	)
	if err != nil {
		return domain.DMConversation{}, fmt.Errorf("create group conversation: %w", err)
	}
	return conversation, nil
}

// dmQuerier is the transaction surface the helpers below need. QueryRow alone,
// because every one of them is a single statement that returns its own result:
// the participant upsert reports what it wrote through RETURNING rather than
// through a row count, so there is nothing here that writes blind.
type dmQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// AddGroupParticipants adds every user in input.UserIDs to an existing group, or
// none.
//
// Lock order is conversation → actor membership → target rows, the same order
// the rest of this file acquires them, so two of these running concurrently
// cannot deadlock against each other.
//
// Both locks are FOR SHARE. They exist to pin the *authorization context* while
// the write happens — archiving the conversation or removing the actor are
// UPDATEs that conflict with FOR SHARE and so are serialised against an add in
// flight — and for nothing else. There is no participant ceiling to serialise,
// so two people adding different users to the same large group proceed in
// parallel rather than queueing behind each other.
//
// Two things are re-established under those locks, each here rather than in the
// service because the service's copy would be a check the write could outrun:
//
//  1. The conversation, from the row the database returns — active workspace,
//     active conversation, type 'group'. One that is archived, direct, or in
//     another tenant has no lockable row to find.
//  2. The *actor's* participation. The service checked it before the
//     transaction opened; between those two moments they can be removed from
//     the group, and without this they would still get to add people to a
//     conversation they no longer belong to.
//
// Neither lock serialises the participant rows themselves, and deliberately so:
// what this call reports is not derived from reading them. Eligibility, the
// insert, the reactivation and the answer to "who did this actually add" are all
// one statement in upsertEligibleDMMembers — the same one group creation writes
// through, so both paths obey one rule about who may be a participant — and the
// answer comes from that statement's RETURNING. Two people adding the same
// person concurrently therefore need no lock between them to produce one
// membership and one claim; see that function for why.
func (s *PGXDMStore) AddGroupParticipants(
	ctx context.Context, input AddGroupParticipantsInput,
) (AddMembersResult, error) {
	if len(input.UserIDs) == 0 {
		return AddMembersResult{}, domain.ErrNoMembersRequested
	}
	if strings.TrimSpace(input.CallerID) == "" {
		// A missing actor is a wiring bug, never an anonymous add.
		return AddMembersResult{}, domain.ErrForbidden
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return AddMembersResult{}, fmt.Errorf("begin add group participants: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	// 1. Lock the conversation.
	var conversationID string
	err = tx.QueryRow(ctx, `
		SELECT dc.id::text
		FROM chat.dm_conversations dc
		JOIN chat.workspaces w ON w.id = dc.workspace_id AND w.status = 'active'
		WHERE dc.id = $1::uuid
		  AND dc.workspace_id = $2::uuid
		  AND dc.status = 'active'
		  AND dc.type = 'group'
		FOR SHARE OF dc`,
		input.ConversationID, input.WorkspaceID,
	).Scan(&conversationID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AddMembersResult{}, domain.ErrNotFound
		}
		return AddMembersResult{}, fmt.Errorf("lock group conversation: %w", err)
	}

	// 2. Re-establish the actor's own participation, inside this transaction.
	//
	// This is the authoritative authorization for the write, not a second
	// opinion: the policy for groups is "an active participant may add", and it
	// is re-read here from chat.dm_members rather than taken as a boolean the
	// service computed earlier. The workspace membership is joined too, because
	// a dm_members row outlives the workspace membership that justified it.
	//
	// FOR SHARE, not FOR UPDATE, for the same reason the channel path uses it:
	// removing the actor from the group is an UPDATE of this row and conflicts
	// with FOR SHARE, so a revocation is still serialised against this add,
	// while two participants adding people concurrently do not block each other.
	// Nothing here is taken FOR UPDATE: there is no ceiling to serialise, and the
	// uniqueness of a membership is settled by the primary key at write time.
	var actorParticipates bool
	err = tx.QueryRow(ctx, `
		SELECT true
		FROM chat.dm_members dm
		JOIN chat.workspace_members wm
		  ON wm.workspace_id = $2::uuid AND wm.user_id = dm.user_id AND wm.status = 'active'
		WHERE dm.conversation_id = $1::uuid
		  AND dm.user_id = $3::uuid
		  AND dm.status = 'active'
		FOR SHARE OF dm`,
		conversationID, input.WorkspaceID, input.CallerID,
	).Scan(&actorParticipates)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Authorization revoked (or never held) between the service check and
			// here. Deliberately ErrForbidden and not the ceiling conflict: the
			// caller must not read a revocation as "the group is full".
			return AddMembersResult{}, domain.ErrForbidden
		}
		return AddMembersResult{}, fmt.Errorf("lock actor dm membership: %w", err)
	}

	// 3. Write, and let the write itself say who it made a participant.
	//
	// Not a capacity check — groups have no fixed size in this product, and a
	// conversation may keep growing across successive requests. This exists so
	// the result reports, and the user-scoped fan-out targets, exactly the
	// people who newly became participants.
	addedUserIDs, err := upsertEligibleDMMembers(ctx, tx, conversationID, input.WorkspaceID, input.UserIDs)
	if err != nil {
		return AddMembersResult{}, err
	}

	total, err := countActiveDMParticipants(ctx, tx, conversationID)
	if err != nil {
		return AddMembersResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return AddMembersResult{}, fmt.Errorf("commit add group participants: %w", err)
	}
	committed = true

	return AddMembersResult{
		Added:          len(addedUserIDs),
		AlreadyMembers: len(input.UserIDs) - len(addedUserIDs),
		TotalCount:     total,
		AddedUserIDs:   addedUserIDs,
	}, nil
}

func countActiveDMParticipants(ctx context.Context, q dmQuerier, conversationID string) (int, error) {
	var count int
	if err := q.QueryRow(ctx, `
		SELECT count(*)
		FROM chat.dm_members
		WHERE conversation_id = $1::uuid AND status = 'active'`,
		conversationID,
	).Scan(&count); err != nil {
		return 0, fmt.Errorf("count dm participants: %w", err)
	}
	return count, nil
}

// upsertEligibleDMMembers adds every user in userIDs to conversationID, or none,
// and returns exactly the ones it made active.
//
// This is the transactional backstop for DM participation, shared by the 1:1 and
// the ad-hoc group flows so both obey exactly one rule. The service layer checks
// eligibility before calling, but that check and the write are separate steps: an
// account suspended or deleted in between would otherwise still receive
// membership. Here the eligibility test and the insert are the same statement, so
// there is no window to interleave — a user who stopped being eligible simply
// produces no row in `eligible`.
//
// Eligibility mirrors MemberStore.GetEligibleDMMember: active workspace, active
// workspace membership, conversation in that same workspace, and an active,
// non-deleted auth.users row. Callers must pass de-duplicated IDs (both flows
// canonicalise and de-duplicate upstream); a repeated ID would make Postgres
// reject the whole statement rather than write a partial result.
//
// Fewer eligible rows than requested means at least one participant was not
// eligible. The generic domain.ErrForbidden is returned without naming them —
// the caller must not be able to probe account state — and the surrounding
// transaction is rolled back, leaving neither an orphan conversation nor a
// partial membership list.
//
// The returned IDs are the RETURNING of this statement and nothing else, which
// is what makes them true under concurrency. The ON CONFLICT branch fires only
// for a row that is not already active, and PostgreSQL evaluates that condition
// against the *latest committed* version of the conflicting row after waiting
// for whoever holds it. So when two transactions add the same person at the same
// moment, the one that gets there first inserts (or reactivates) and returns
// them; the second finds an active row, updates nothing, and returns nothing.
// Exactly one call reports the addition, exactly one membership exists, and no
// unique violation escapes.
//
// Deriving it any other way does not survive that race. Counting active
// participants before and after, or reading who was already a member ahead of
// the insert, both read a snapshot the concurrent writer is about to invalidate:
// both transactions would observe the person as absent and both would claim the
// addition — which then becomes two "you were added" signals for one membership.
//
// The condition is also what keeps a re-add from disturbing a live membership:
// an already-active participant's row is left exactly as it is, and only a
// genuine 'left' → 'active' transition clears left_at. Reactivation is the
// wanted behaviour for an add — the panel offers a name, the person rejoins, and
// no second row appears — and it is reported as an addition, because for
// everyone else in the conversation that is what it is.
func upsertEligibleDMMembers(
	ctx context.Context, q dmQuerier, conversationID, workspaceID string, userIDs []string,
) ([]string, error) {
	var eligible int
	var addedUserIDs []string
	err := q.QueryRow(ctx, `
		WITH eligible AS (
			SELECT wm.user_id
			FROM unnest($3::uuid[]) AS candidate(user_id)
			JOIN chat.workspace_members wm
			  ON wm.workspace_id = $2 AND wm.user_id = candidate.user_id AND wm.status = 'active'
			JOIN chat.workspaces w
			  ON w.id = wm.workspace_id AND w.status = 'active'
			JOIN chat.dm_conversations dc
			  ON dc.id = $1 AND dc.workspace_id = wm.workspace_id
			JOIN auth.users u
			  ON u.id = wm.user_id AND u.status = 'active' AND u.deleted_at IS NULL
		),
		upserted AS (
			INSERT INTO chat.dm_members AS dm (conversation_id, user_id, role, status, left_at)
			SELECT $1, user_id, 'member', 'active', NULL
			FROM eligible
			ON CONFLICT (conversation_id, user_id)
			DO UPDATE SET role = 'member',
			              status = 'active',
			              left_at = NULL
			WHERE dm.status <> 'active'
			RETURNING user_id
		)
		SELECT (SELECT count(*) FROM eligible),
		       (SELECT COALESCE(array_agg(user_id::text), '{}') FROM upserted)`,
		conversationID, workspaceID, userIDs,
	).Scan(&eligible, &addedUserIDs)
	if err != nil {
		return nil, fmt.Errorf("upsert dm members: %w", err)
	}
	if eligible != len(userIDs) {
		return nil, domain.ErrForbidden
	}
	return addedUserIDs, nil
}

func (s *PGXDMStore) ListVisibleConversationsByUser(ctx context.Context, workspaceID, userID string) ([]domain.DMConversation, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT dc.id, dc.workspace_id, dc.type, COALESCE(dc.title, ''), dc.status,
		       dc.created_by, dc.created_at, dc.updated_at
		FROM chat.dm_conversations dc
		JOIN chat.workspaces w
		  ON w.id = dc.workspace_id AND w.status = 'active'
		JOIN chat.workspace_members wm
		  ON wm.workspace_id = dc.workspace_id AND wm.user_id = $2 AND wm.status = 'active'
		JOIN chat.dm_members dm
		  ON dm.conversation_id = dc.id AND dm.user_id = $2 AND dm.status = 'active'
		WHERE dc.workspace_id = $1
		  AND dc.status = 'active'
		ORDER BY dc.updated_at DESC, dc.created_at DESC`,
		workspaceID, userID,
	)
	if err != nil {
		return nil, fmt.Errorf("list visible dm conversations: %w", err)
	}
	defer rows.Close()

	var conversations []domain.DMConversation
	for rows.Next() {
		var conversation domain.DMConversation
		if err := rows.Scan(
			&conversation.ID, &conversation.WorkspaceID, (*string)(&conversation.Type),
			&conversation.Title, (*string)(&conversation.Status), &conversation.CreatedBy,
			&conversation.CreatedAt, &conversation.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan visible dm conversation: %w", err)
		}
		conversations = append(conversations, conversation)
	}
	return conversations, rows.Err()
}

// The counterpart identity (id, resolved name, avatar) is produced by a single
// LATERAL join in the same round-trip, so the sidebar never needs a
// per-conversation lookup and the counterpart row is scanned once, not once per
// column. It is excluded from the requesting user by comparing against
// dm.user_id — the caller's own membership row — so no client-supplied
// identifier and no UUID text formatting is involved.
// The visual name prefers full_name and falls back to display_name. full_name is
// used whenever it is present: OIDC users can receive full_name from the
// provider's profile claims (name, or given_name + family_name), and manual
// users may set it too, while display_name remains the fallback when full_name
// is absent. Blank and whitespace-only values are treated as absent.
// avatar_url is passed through verbatim and never fetched server-side; the
// client validates the scheme and falls back to initials.
// auth.users is joined without a status filter on purpose: the conversation is
// already authorized by membership, and hiding the name of a deactivated
// colleague would degrade a working conversation to a meaningless label.
// Anonymization of removed users is owned by auth-service at the source, which
// is also where avatar_url must be cleared.
// No column beyond id/full_name/display_name/avatar_url is selected: e-mail,
// status, auth source and external subject stay out of the sidebar contract.
func (s *PGXDMStore) ListVisibleConversationsWithParticipantIDs(ctx context.Context, workspaceID, userID string) ([]domain.DMConversationWithParticipantIDs, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT dc.id, dc.workspace_id, dc.type, COALESCE(dc.title, ''), dc.status,
		       dc.created_by, dc.created_at, dc.updated_at,
		       ARRAY(
		           SELECT dm2.user_id::text
		           FROM chat.dm_members dm2
		           WHERE dm2.conversation_id = dc.id AND dm2.status = 'active'
		           ORDER BY dm2.user_id
		       ) AS participant_ids,
		       COALESCE(cp.user_id, '')      AS counterpart_user_id,
		       COALESCE(cp.display_name, '') AS counterpart_display_name,
		       COALESCE(cp.avatar_url, '')   AS counterpart_avatar_url
		FROM chat.dm_conversations dc
		JOIN chat.workspaces w
		  ON w.id = dc.workspace_id AND w.status = 'active'
		JOIN chat.workspace_members wm
		  ON wm.workspace_id = dc.workspace_id AND wm.user_id = $2 AND wm.status = 'active'
		JOIN chat.dm_members dm
		  ON dm.conversation_id = dc.id AND dm.user_id = $2 AND dm.status = 'active'
		LEFT JOIN LATERAL (
		    SELECT other.user_id::text AS user_id,
		           COALESCE(
		               NULLIF(BTRIM(u.full_name), ''),
		               NULLIF(BTRIM(u.display_name), '')
		           ) AS display_name,
		           u.avatar_url AS avatar_url
		    FROM chat.dm_members other
		    JOIN auth.users u ON u.id = other.user_id
		    WHERE dc.type = 'direct'
		      AND other.conversation_id = dc.id
		      AND other.status = 'active'
		      AND other.user_id <> dm.user_id
		    ORDER BY other.user_id
		    LIMIT 1
		) cp ON true
		WHERE dc.workspace_id = $1
		  AND dc.status = 'active'
		ORDER BY dc.updated_at DESC, dc.created_at DESC`,
		workspaceID, userID,
	)
	if err != nil {
		return nil, fmt.Errorf("list visible dm conversations with participants: %w", err)
	}
	defer rows.Close()

	var conversations []domain.DMConversationWithParticipantIDs
	for rows.Next() {
		var c domain.DMConversationWithParticipantIDs
		if err := rows.Scan(
			&c.ID, &c.WorkspaceID, (*string)(&c.Type),
			&c.Title, (*string)(&c.Status), &c.CreatedBy,
			&c.CreatedAt, &c.UpdatedAt, &c.ParticipantIDs,
			&c.CounterpartUserID, &c.CounterpartDisplayName, &c.CounterpartAvatarURL,
		); err != nil {
			return nil, fmt.Errorf("scan visible dm conversation with participants: %w", err)
		}
		conversations = append(conversations, c)
	}
	return conversations, rows.Err()
}

func (s *PGXDMStore) GetVisibleConversationByID(ctx context.Context, workspaceID, conversationID, userID string) (domain.DMConversation, error) {
	var conversation domain.DMConversation
	err := s.pool.QueryRow(ctx, `
		SELECT dc.id, dc.workspace_id, dc.type, COALESCE(dc.title, ''), dc.status,
		       dc.created_by, dc.created_at, dc.updated_at
		FROM chat.dm_conversations dc
		JOIN chat.workspaces w
		  ON w.id = dc.workspace_id AND w.status = 'active'
		JOIN chat.workspace_members wm
		  ON wm.workspace_id = dc.workspace_id AND wm.user_id = $3 AND wm.status = 'active'
		JOIN chat.dm_members dm
		  ON dm.conversation_id = dc.id AND dm.user_id = $3 AND dm.status = 'active'
		WHERE dc.workspace_id = $1
		  AND dc.id = $2
		  AND dc.status = 'active'`,
		workspaceID, conversationID, userID,
	).Scan(
		&conversation.ID, &conversation.WorkspaceID, (*string)(&conversation.Type),
		&conversation.Title, (*string)(&conversation.Status), &conversation.CreatedBy,
		&conversation.CreatedAt, &conversation.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.DMConversation{}, domain.ErrNotFound
		}
		return domain.DMConversation{}, fmt.Errorf("get visible dm conversation: %w", err)
	}
	return conversation, nil
}

// GetDirectCounterpartProfile authorises the caller and resolves who the other
// side of a 1:1 conversation is, in one query (issue #443).
//
// It is authoritative, not a projection of a decision taken earlier. Access is
// re-established here, in the same statement and against the same snapshot that
// reads the profile, so a membership revoked between an earlier visibility check
// and this call cannot leave a stale "yes" standing: the two EXISTS clauses put
// the caller's own dm_members and workspace_members rows into the predicate that
// selects the counterpart, and losing either yields zero rows rather than a
// profile.
//
// The caller's user ID is a *predicate*, never a selector: the query asks for
// the active participants of this conversation who are not the caller, so the
// counterpart cannot be chosen by the client and the caller can never be
// returned as their own profile. Two people with the same display name are
// still two rows with two IDs — identity here is dm.user_id, never a name.
//
// The membership predicates are the same set ListParticipantProfiles applies,
// for the same reason: "who is in this conversation" must not mean one thing
// for access and another for display. The conversation must be active and in
// workspaceID, dc.type must be 'direct' as the database recorded it, each
// participant's dm_members row must be 'active' (so someone who left is gone),
// they must still be an active workspace member, and the counterpart's account
// must be active and not deleted.
//
// The row limit is 2, not 1: one row is the normal case, and a second is what
// distinguishes a corrupt 'direct' row from a healthy one. Taking LIMIT 1 would
// silently pick an arbitrary "other participant" out of a conversation the
// domain says cannot have several.
//
// The projection is explicit and minimal — no SELECT * — so a future column on
// auth.users cannot start flowing into a profile response by accident.
//
// Zero rows is deliberately one answer for several causes — no such
// conversation, another workspace, not a direct row, caller not (or no longer)
// a participant, caller suspended, counterpart gone. Telling them apart would
// take a second query whose only product is a more precise denial, which is
// exactly the thing a caller must not be given.

// SearchGroupParticipantCandidates lists people who could still be added to a
// group: active members of the workspace, with active accounts, who are not
// already active participants of it.
//
// The NOT EXISTS against chat.dm_members is the correction this method exists
// for. The group panel shows at most domain.MaxDMDetailsParticipants (30)
// participants, so in a larger group the 31st onwards were invisible to the
// picker's exclusion list and came back as selectable. The database has no such
// cap.
//
// Only `status = 'active'` participation excludes someone: a member who left is
// not a participant, and offering them again is correct — adding them back
// reactivates their row, which is the domain's existing semantics for a return.
//
// The eligibility predicate is the same one SearchDMCandidates uses, so the two
// cannot disagree about who is an eligible person in this workspace.
func (s *PGXDMStore) SearchGroupParticipantCandidates(
	ctx context.Context, workspaceID, conversationID, callerID, prefix string, limit int,
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
		      FROM chat.dm_members dm
		      JOIN chat.dm_conversations dc
		        ON dc.id = dm.conversation_id
		       AND dc.workspace_id = wm.workspace_id
		      WHERE dm.conversation_id = $2::uuid
		        AND dm.user_id = wm.user_id
		        AND dm.status = 'active'
		  )
		ORDER BY lower(u.display_name), u.id
		LIMIT $5`, workspaceID, conversationID, callerID, prefix, limit)
	if err != nil {
		return nil, fmt.Errorf("search group participant candidates: %w", err)
	}
	defer rows.Close()

	results := make([]domain.DMCandidate, 0, limit)
	for rows.Next() {
		var candidate domain.DMCandidate
		if err := rows.Scan(&candidate.UserID, &candidate.DisplayName); err != nil {
			return nil, fmt.Errorf("scan group participant candidate: %w", err)
		}
		results = append(results, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate group participant candidates: %w", err)
	}
	return results, nil
}
