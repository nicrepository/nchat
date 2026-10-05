package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/nicrepository/nchat/libs/go/platform/channelmembership"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// CreateCategoryInput holds the fields for creating a channel category.
type CreateCategoryInput struct {
	WorkspaceID string
	Name        string
	Position    int
}

// CreateChannelInput holds the fields for creating a channel.
// CategoryID and CreatedBy are optional (empty string = NULL).
type CreateChannelInput struct {
	WorkspaceID string
	CategoryID  string
	Slug        string
	DisplayName string
	Type        domain.ChannelType
	IsGeneral   bool
	Position    int
	CreatedBy   string
	// EnsurePublicWorkspaceMembers adds every active, non-guest workspace
	// member to a newly-created public channel in the creation transaction.
	EnsurePublicWorkspaceMembers bool
	// EnsureCreatorMemberRole, when non-empty, adds CreatedBy to
	// chat.channel_members in the same transaction as the channel insert, the
	// way UpdateChannelInput.EnsureMemberUserID does for a public→private
	// switch. ChannelService sets it for private channels so their creator is
	// represented in the authoritative roster. Honoured by
	// CreateChannelForActiveMember only.
	EnsureCreatorMemberRole domain.ChannelRole
	// InitialMemberIDs are the invitees of a private channel (issue #1025),
	// already normalized by the service and never containing CreatedBy. They
	// are inserted with the ordinary member role in the creation transaction,
	// through channelmembership.EligibleTargetsCTE; one ineligible ID rolls the
	// whole creation back. Honoured by CreateChannelForActiveMember only.
	InitialMemberIDs []string
	// IdempotencyKey, when set, is claimed for (WorkspaceID, CreatedBy) in the
	// creation transaction, bound to RequestHash. Honoured by
	// CreateChannelForActiveMember only.
	IdempotencyKey string
	RequestHash    string
}

// CreateChannelResult is what CreateChannelForActiveMember produced. Replayed
// is true when the Idempotency-Key had already created Channel: nothing was
// written by this call, so nothing new may be announced for it.
type CreateChannelResult struct {
	Channel  domain.Channel
	Replayed bool
}

// UpdateChannelInput holds the complete mutable channel state to persist.
// CategoryID and EnsureMemberUserID are optional (empty string = NULL / disabled).
type UpdateChannelInput struct {
	// CallerID is the authenticated actor, and it is required: UpdateChannel
	// re-derives that actor's workspace management role from the database inside
	// the write transaction. It is deliberately an identity and never a decision
	// — no role, no capability, no boolean — because a decision computed before
	// the transaction opened is exactly what the re-derivation exists to
	// distrust. It comes from the session, never from a request body.
	CallerID           string
	WorkspaceID        string
	ChannelID          string
	CategoryID         string
	Slug               string
	DisplayName        string
	Type               domain.ChannelType
	Position           int
	EnsureMemberUserID string
}

// UpdateChannelResult is what a committed channel update produced.
//
// Event is the system message the same transaction wrote, and it is present only
// when the display name actually changed — an update that touches the slug, the
// category or the position renames nothing, and a PATCH that sets the name it
// already had is not a rename either. Its zero value means "no event", which is
// what lets the caller publish one only when there is one (issue #527).
type UpdateChannelResult struct {
	Channel domain.Channel
	Event   domain.Message
}

// VisibleChannelAccess carries the membership used to evaluate channel policy.
//
// LastMessageAt is the created_at of the channel's newest message, or nil when
// it has none (issue #414) — the same activity instant, with the same
// guarantees, that domain.DMConversationWithParticipantIDs documents for
// conversations.
type VisibleChannelAccess struct {
	Channel       domain.Channel
	ChannelMember *domain.ChannelMember
	LastMessageAt *time.Time
}

// ChannelStore is the persistence interface for channel operations.
type ChannelStore interface {
	CreateCategory(ctx context.Context, input CreateCategoryInput) (domain.ChannelCategory, error)
	CreateChannel(ctx context.Context, input CreateChannelInput) (domain.Channel, error)
	// CreateChannelForActiveMember creates a channel on behalf of input.CreatedBy,
	// serialising the authorization decision with the write itself.
	//
	// Returns domain.ErrForbidden — without saying which condition failed — when
	// the workspace is not active, or input.CreatedBy has no active membership in
	// it at the moment of the INSERT, or when an initial member is not eligible.
	// Returns domain.ErrIdempotencyKeyReused when input.IdempotencyKey was
	// already used by the same actor for a request with another RequestHash.
	CreateChannelForActiveMember(ctx context.Context, input CreateChannelInput) (CreateChannelResult, error)
	GetCategoryByIDInWorkspace(ctx context.Context, workspaceID, id string) (domain.ChannelCategory, error)
	GetChannelByID(ctx context.Context, id string) (domain.Channel, error)
	// GetChannelByIDInWorkspace returns the channel only if it belongs to workspaceID.
	// Returns ErrNotFound when the channel does not exist or belongs to a different workspace.
	GetChannelByIDInWorkspace(ctx context.Context, workspaceID, id string) (domain.Channel, error)
	GetVisibleChannelByID(ctx context.Context, workspaceID, channelID, userID string) (domain.Channel, error)
	// GetChannelAbout returns the channel's description and its creator's
	// resolved display name (issue #894), in one query. The caller's read access
	// to the channel must already have been settled — this is isolation in
	// depth, not the permission.
	GetChannelAbout(ctx context.Context, workspaceID, channelID string) (ConversationAbout, error)
	GetVisibleChannelBySlug(ctx context.Context, workspaceID, slug, userID string) (domain.Channel, error)
	ListChannelsByWorkspace(ctx context.Context, workspaceID string) ([]domain.Channel, error)
	// ListVisibleChannelsByUser returns active channels in workspaceID visible to userID.
	// Visibility is enforced in SQL: active workspace + active workspace membership required;
	// public and general channels are always included; private channels require channel membership.
	// Returns an empty slice when the workspace is disabled or userID is not an active member.
	ListVisibleChannelsByUser(ctx context.Context, workspaceID, userID string) ([]domain.Channel, error)
	// UpdateChannel persists the channel's mutable state, re-deriving
	// input.CallerID's workspace management role inside the same transaction as
	// the write and holding the membership row while it happens.
	//
	// Returns domain.ErrForbidden — without saying which condition failed — when
	// the workspace is not active, or CallerID does not hold an active
	// owner/admin membership in it at the moment of the UPDATE.
	//
	// A change of display name also writes a conversation_renamed system message
	// in the same transaction, returned alongside the channel (issue #527).
	UpdateChannel(ctx context.Context, input UpdateChannelInput) (UpdateChannelResult, error)
	// ArchiveChannel also writes a conversation_archived system message in the
	// same transaction (issue #685). actorID is the caller whose management
	// permission the service already re-derived.
	ArchiveChannel(ctx context.Context, workspaceID, channelID, actorID string) (domain.Channel, error)
	// LeaveChannelSelf removes the actor's own membership and records the
	// departure in the same transaction. Self-leave only, and refused for the
	// general channel in SQL (issue #527).
	LeaveChannelSelf(ctx context.Context, workspaceID, channelID, callerID string) (LeaveConversationResult, error)
}

// PGXChannelStore implements ChannelStore using a pgx connection pool.
type PGXChannelStore struct {
	pool Pool
}

func NewPGXChannelStore(pool Pool) *PGXChannelStore {
	return &PGXChannelStore{pool: pool}
}

func (s *PGXChannelStore) CreateCategory(ctx context.Context, input CreateCategoryInput) (domain.ChannelCategory, error) {
	var c domain.ChannelCategory
	err := s.pool.QueryRow(ctx, `
		INSERT INTO chat.channel_categories (workspace_id, name, position)
		VALUES ($1, $2, $3)
		RETURNING id, workspace_id, name, position, created_at, updated_at`,
		input.WorkspaceID, input.Name, input.Position,
	).Scan(&c.ID, &c.WorkspaceID, &c.Name, &c.Position, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return domain.ChannelCategory{}, fmt.Errorf("create category: %w", err)
	}
	return c, nil
}

func (s *PGXChannelStore) CreateChannel(ctx context.Context, input CreateChannelInput) (domain.Channel, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Channel{}, fmt.Errorf("begin create channel: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	ch, err := createChannel(ctx, tx, input)
	if err != nil {
		return domain.Channel{}, err
	}
	// issue #685: as in CreateChannelForActiveMember, but this path (used only
	// for a workspace's bootstrap #geral channel — see WorkspaceService) has no
	// authorization CTE to guarantee an actor, so the event is skipped rather
	// than attributed to nothing when CreatedBy is unset.
	if ch.CreatedBy != "" {
		if _, err := InsertConversationEvent(ctx, tx, ConversationEventInput{
			WorkspaceID: ch.WorkspaceID, ChannelID: ch.ID,
			ActorID: ch.CreatedBy, Event: domain.ConversationEventCreated,
		}); err != nil {
			return domain.Channel{}, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.Channel{}, fmt.Errorf("commit create channel: %w", err)
	}
	committed = true
	return ch, nil
}

// CreateChannelForActiveMember is the authorization-bearing creation path used
// by ChannelService (BUG #393).
//
// Channel creation takes an active membership in an active workspace, and both
// are mutable: checking them and then inserting are two steps, and a membership
// revoked in between would still get its channel. Here the check and the insert
// are one statement — the INSERT draws its rows from an authorized context that
// locks the workspace and the membership, so there is nothing to interleave.
//
// The locks are FOR SHARE rather than FOR UPDATE. Revoking a membership or
// disabling a workspace is an UPDATE of a non-key column, which takes FOR NO KEY
// UPDATE; that conflicts with FOR SHARE, so either one is serialised against a
// creation in flight. Two concurrent creations, however, both take FOR SHARE and
// do not block each other, which FOR UPDATE would have made them do for no
// safety gained.
//
// Whichever side wins is a correct outcome: a revocation that commits first
// makes the locked SELECT re-evaluate its predicate against the new row version,
// find no active membership, and insert nothing; a creation that gets there
// first completes and the revocation waits for the commit.
//
// workspace_id and created_by are read back out of the authorized context rather
// than taken from the parameters, so the row can only ever record the workspace
// and the actor the database itself authorized.
func (s *PGXChannelStore) CreateChannelForActiveMember(ctx context.Context, input CreateChannelInput) (CreateChannelResult, error) {
	if input.CreatedBy == "" {
		return CreateChannelResult{}, domain.ErrForbidden
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return CreateChannelResult{}, fmt.Errorf("begin create channel for active member: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			// Rolled back on every failure below, including a failed secondary
			// insert, so a denied or broken creation never leaves a channel, a
			// claimed idempotency key or a half-populated membership behind.
			_ = tx.Rollback(ctx)
		}
	}()

	result, err := createChannelInTx(ctx, tx, input)
	if err != nil || result.Replayed {
		// A replay wrote nothing; the deferred rollback ends its transaction.
		return result, err
	}
	if err := tx.Commit(ctx); err != nil {
		return CreateChannelResult{}, fmt.Errorf("commit create channel for active member: %w", err)
	}
	committed = true
	return result, nil
}

// createChannelInTx is the body of CreateChannelForActiveMember: the
// idempotency claim (or its replay), the authorized INSERT and the rows that
// belong to the new channel, all on tx.
func createChannelInTx(ctx context.Context, tx pgx.Tx, input CreateChannelInput) (CreateChannelResult, error) {
	var channelID *string
	if input.IdempotencyKey != "" {
		claimed, replay, err := claimChannelCreation(ctx, tx, input)
		if err != nil {
			return CreateChannelResult{}, err
		}
		if replay {
			ch, err := getReplayedChannel(ctx, tx, input.WorkspaceID, claimed, input.CreatedBy)
			return CreateChannelResult{Channel: ch, Replayed: err == nil}, err
		}
		channelID = &claimed
	}

	ch, err := insertAuthorizedChannel(ctx, tx, input, channelID)
	if err != nil {
		return CreateChannelResult{}, err
	}
	if err := populateCreatedChannel(ctx, tx, ch, input); err != nil {
		return CreateChannelResult{}, err
	}
	return CreateChannelResult{Channel: ch}, nil
}

// claimChannelCreation binds input.IdempotencyKey to this transaction (issue
// #1025), before anything else is written or locked.
//
// The primary key of chat.channel_creation_requests is the authority. A second
// request with the same key blocks on this INSERT until the first one ends: if
// it committed, ON CONFLICT DO NOTHING yields no row and the committed claim is
// read back — the same channel for the same RequestHash, ErrIdempotencyKeyReused
// for any other; if it rolled back, this claim simply succeeds. The channel ID
// is allocated here, so the claim and the channel commit together or not at all
// (the foreign key is deferred to commit for exactly that).
//
// The table is touched by nothing else, so taking it first adds no edge to the
// canonical lock order of channel membership.
func claimChannelCreation(ctx context.Context, tx pgx.Tx, input CreateChannelInput) (string, bool, error) {
	var channelID string
	err := tx.QueryRow(ctx, `
		INSERT INTO chat.channel_creation_requests
			(workspace_id, actor_user_id, idempotency_key, request_hash, channel_id)
		VALUES ($1::uuid, $2::uuid, $3, $4, gen_random_uuid())
		ON CONFLICT (workspace_id, actor_user_id, idempotency_key) DO NOTHING
		RETURNING channel_id::text`,
		input.WorkspaceID, input.CreatedBy, input.IdempotencyKey, input.RequestHash,
	).Scan(&channelID)
	if err == nil {
		return channelID, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", false, fmt.Errorf("claim channel creation key: %w", err)
	}

	var requestHash string
	if err := tx.QueryRow(ctx, `
		SELECT request_hash, channel_id::text
		FROM chat.channel_creation_requests
		WHERE workspace_id = $1::uuid AND actor_user_id = $2::uuid AND idempotency_key = $3`,
		input.WorkspaceID, input.CreatedBy, input.IdempotencyKey,
	).Scan(&requestHash, &channelID); err != nil {
		return "", false, fmt.Errorf("read channel creation key: %w", err)
	}
	if requestHash != input.RequestHash {
		return "", false, domain.ErrIdempotencyKeyReused
	}
	return channelID, true, nil
}

// getReplayedChannel returns the channel an earlier request with the same key
// created, provided the actor can still read it. A replay is not a back door:
// an actor who has since lost access gets the same uniform ErrForbidden a
// denied creation gets.
func getReplayedChannel(ctx context.Context, tx pgx.Tx, workspaceID, channelID, actorID string) (domain.Channel, error) {
	var ch domain.Channel
	err := tx.QueryRow(ctx, `
		SELECT id, workspace_id, COALESCE(category_id::text, ''), slug, display_name,
		       type, status, is_general, position, COALESCE(created_by::text, ''),
		       created_at, updated_at
		FROM chat.channels c
		WHERE c.id = $1::uuid AND c.workspace_id = $2::uuid AND c.status = 'active'
		  AND chat.channel_visible_to_user(c.id, $3::uuid)`,
		channelID, workspaceID, actorID,
	).Scan(
		&ch.ID, &ch.WorkspaceID, &ch.CategoryID, &ch.Slug, &ch.DisplayName,
		(*string)(&ch.Type), (*string)(&ch.Status), &ch.IsGeneral, &ch.Position, &ch.CreatedBy,
		&ch.CreatedAt, &ch.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Channel{}, domain.ErrForbidden
	}
	if err != nil {
		return domain.Channel{}, fmt.Errorf("read replayed channel: %w", err)
	}
	return ch, nil
}

// insertAuthorizedChannel is the authorization-bearing INSERT. channelID is
// the ID an idempotency claim allocated, or nil for the database default.
func insertAuthorizedChannel(ctx context.Context, tx pgx.Tx, input CreateChannelInput, channelID *string) (domain.Channel, error) {
	var categoryID *string
	if input.CategoryID != "" {
		categoryID = &input.CategoryID
	}

	var ch domain.Channel
	err := tx.QueryRow(ctx, `
		WITH authorized_context AS (
			SELECT w.id AS workspace_id, wm.user_id
			FROM chat.workspaces w
			JOIN chat.workspace_members wm
			  ON wm.workspace_id = w.id
			WHERE w.id = $1
			  AND w.status = 'active'
			  AND wm.user_id = $8
			  AND wm.status = 'active'
			  -- The SQL statement of domain.CanCreateChannel: every role that
			  -- reaches the workspace's public channels may create one, and a
			  -- guest — whose reach is only the channels it was added to — may
			  -- not. Re-derived here rather than trusted from the service, so a
			  -- demotion to guest that commits mid-flight is serialised against
			  -- the insert by the FOR SHARE below instead of racing it.
			  AND wm.role IN ('owner', 'admin', 'moderator', 'member')
			FOR SHARE OF w, wm
		)
		INSERT INTO chat.channels
			(id, workspace_id, category_id, slug, display_name, type, is_general, position, created_by)
		SELECT COALESCE($9::uuid, gen_random_uuid()), ac.workspace_id, $2, $3, $4, $5, $6, $7, ac.user_id
		FROM authorized_context ac
		RETURNING id, workspace_id, COALESCE(category_id::text, ''), slug, display_name,
		          type, status, is_general, position, COALESCE(created_by::text, ''),
		          created_at, updated_at`,
		input.WorkspaceID, categoryID, input.Slug, input.DisplayName,
		string(input.Type), input.IsGeneral, input.Position, input.CreatedBy, channelID,
	).Scan(
		&ch.ID, &ch.WorkspaceID, &ch.CategoryID, &ch.Slug, &ch.DisplayName,
		(*string)(&ch.Type), (*string)(&ch.Status), &ch.IsGeneral, &ch.Position, &ch.CreatedBy,
		&ch.CreatedAt, &ch.UpdatedAt,
	)
	if err == nil {
		return ch, nil
	}
	// No row from the authorized context means no INSERT: the workspace was
	// not active, or the membership was absent or no longer active. Which of
	// them is deliberately not distinguished — the caller must not learn the
	// workspace exists from a failure to create in it.
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Channel{}, domain.ErrForbidden
	}
	if mapped := mapChannelWriteError(err); mapped != nil {
		return domain.Channel{}, mapped
	}
	return domain.Channel{}, fmt.Errorf("create channel for active member: %w", err)
}

// populateCreatedChannel writes everything that belongs to the new channel in
// its creation transaction: the creation event and the initial membership.
func populateCreatedChannel(ctx context.Context, tx pgx.Tx, ch domain.Channel, input CreateChannelInput) error {
	// issue #685: creation is a conversation event like a rename or a
	// departure, in the same transaction as the row it describes. ch.CreatedBy
	// is always set here — the authorized_context CTE supplies it, never the
	// caller — so there is always an actor to attribute it to.
	if _, err := InsertConversationEvent(ctx, tx, ConversationEventInput{
		WorkspaceID: ch.WorkspaceID, ChannelID: ch.ID,
		ActorID: ch.CreatedBy, Event: domain.ConversationEventCreated,
	}); err != nil {
		return err
	}
	if input.EnsureCreatorMemberRole != "" {
		if err := addChannelMember(ctx, tx, ch.ID, ch.CreatedBy, input.EnsureCreatorMemberRole); err != nil {
			return err
		}
	}
	if err := addInitialChannelMembers(ctx, tx, ch, input.InitialMemberIDs); err != nil {
		return err
	}
	if input.EnsurePublicWorkspaceMembers {
		return addPublicWorkspaceMembers(ctx, tx, ch.ID, ch.WorkspaceID)
	}
	return nil
}

// addInitialChannelMembers inserts the invitees chosen in the creation wizard
// (issue #1025), all or nothing.
//
// Eligibility is channelmembership.EligibleTargetsCTE, the very predicate
// add-members and admin-service use, so who may be invited at creation cannot
// drift from who may be added later. It locks the targets' workspace membership
// and account FOR SHARE after the actor's, which is the canonical order: a
// suspension, departure or deactivation that commits first makes the target
// ineligible here; one that comes later waits for this commit. The channel row
// itself is invisible to every other transaction until then.
//
// Fewer eligible rows than requested IDs is ErrForbidden — the same answer for
// an unknown, cross-workspace, suspended or deleted user, so the error is not
// an account oracle — and the caller's rollback leaves no channel behind.
// Every invitee gets the ordinary channel role; the request carries no role.
func addInitialChannelMembers(ctx context.Context, q channelQuerier, ch domain.Channel, userIDs []string) error {
	if len(userIDs) == 0 {
		return nil
	}
	var eligible int
	err := q.QueryRow(ctx, `
		WITH eligible AS (`+channelmembership.EligibleTargetsCTE+`
		),
		inserted AS (
			INSERT INTO chat.channel_members (channel_id, user_id, role)
			SELECT $2::uuid, user_id, $4
			FROM eligible
			ON CONFLICT (channel_id, user_id) DO NOTHING
		)
		SELECT count(*) FROM eligible`,
		ch.WorkspaceID, ch.ID, userIDs, channelmembership.DefaultChannelRole,
	).Scan(&eligible)
	if err != nil {
		return fmt.Errorf("add initial channel members: %w", err)
	}
	if eligible != len(userIDs) {
		return domain.ErrForbidden
	}
	return nil
}

// addPublicWorkspaceMembers materializes the same population automatically
// joined to #geral: active owners, admins, moderators and members. Guests keep
// their restricted-channel boundary and must still be explicitly invited.
func addPublicWorkspaceMembers(ctx context.Context, q channelQuerier, channelID, workspaceID string) error {
	_, err := q.Exec(ctx, `
		INSERT INTO chat.channel_members (channel_id, user_id, role)
		SELECT $1::uuid, wm.user_id, $3
		FROM chat.workspace_members wm
		WHERE wm.workspace_id = $2::uuid
		  AND wm.status = 'active'
		  AND wm.role IN ('owner', 'admin', 'moderator', 'member')
		ON CONFLICT (channel_id, user_id) DO NOTHING`,
		channelID, workspaceID, string(domain.ChannelRoleMember),
	)
	if err != nil {
		return fmt.Errorf("add public channel workspace members: %w", err)
	}
	return nil
}

func createChannel(ctx context.Context, q channelQuerier, input CreateChannelInput) (domain.Channel, error) {
	var categoryID *string
	if input.CategoryID != "" {
		categoryID = &input.CategoryID
	}
	var createdBy *string
	if input.CreatedBy != "" {
		createdBy = &input.CreatedBy
	}

	var ch domain.Channel
	err := q.QueryRow(ctx, `
		INSERT INTO chat.channels
			(workspace_id, category_id, slug, display_name, type, is_general, position, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, workspace_id, COALESCE(category_id::text, ''), slug, display_name,
		          type, status, is_general, position, COALESCE(created_by::text, ''),
		          created_at, updated_at`,
		input.WorkspaceID, categoryID, input.Slug, input.DisplayName,
		string(input.Type), input.IsGeneral, input.Position, createdBy,
	).Scan(
		&ch.ID, &ch.WorkspaceID, &ch.CategoryID, &ch.Slug, &ch.DisplayName,
		(*string)(&ch.Type), (*string)(&ch.Status), &ch.IsGeneral, &ch.Position, &ch.CreatedBy,
		&ch.CreatedAt, &ch.UpdatedAt,
	)
	if err != nil {
		if mapped := mapChannelWriteError(err); mapped != nil {
			return domain.Channel{}, mapped
		}
		return domain.Channel{}, fmt.Errorf("create channel: %w", err)
	}
	return ch, nil
}

type channelQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func addChannelMember(ctx context.Context, q channelQuerier, channelID, userID string, role domain.ChannelRole) error {
	_, err := q.Exec(ctx, `
		INSERT INTO chat.channel_members (channel_id, user_id, role)
		VALUES ($1, $2, $3)
		ON CONFLICT (channel_id, user_id) DO NOTHING`,
		channelID, userID, string(role),
	)
	if err != nil {
		return fmt.Errorf("add channel member: %w", err)
	}
	return nil
}

func (s *PGXChannelStore) GetCategoryByIDInWorkspace(ctx context.Context, workspaceID, id string) (domain.ChannelCategory, error) {
	var c domain.ChannelCategory
	err := s.pool.QueryRow(ctx, `
		SELECT id, workspace_id, name, position, created_at, updated_at
		FROM chat.channel_categories
		WHERE workspace_id = $1 AND id = $2`,
		workspaceID, id,
	).Scan(&c.ID, &c.WorkspaceID, &c.Name, &c.Position, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ChannelCategory{}, domain.ErrNotFound
		}
		return domain.ChannelCategory{}, fmt.Errorf("get category by id in workspace: %w", err)
	}
	return c, nil
}

func (s *PGXChannelStore) GetChannelByID(ctx context.Context, id string) (domain.Channel, error) {
	var ch domain.Channel
	err := s.pool.QueryRow(ctx, `
		SELECT id, workspace_id, COALESCE(category_id::text, ''), slug, display_name,
		       type, status, is_general, position, COALESCE(created_by::text, ''),
		       created_at, updated_at
		FROM chat.channels
		WHERE id = $1 AND status = 'active'`,
		id,
	).Scan(
		&ch.ID, &ch.WorkspaceID, &ch.CategoryID, &ch.Slug, &ch.DisplayName,
		(*string)(&ch.Type), (*string)(&ch.Status), &ch.IsGeneral, &ch.Position, &ch.CreatedBy,
		&ch.CreatedAt, &ch.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Channel{}, domain.ErrNotFound
		}
		return domain.Channel{}, fmt.Errorf("get channel by id: %w", err)
	}
	return ch, nil
}

func (s *PGXChannelStore) GetChannelByIDInWorkspace(ctx context.Context, workspaceID, id string) (domain.Channel, error) {
	var ch domain.Channel
	err := s.pool.QueryRow(ctx, `
		SELECT id, workspace_id, COALESCE(category_id::text, ''), slug, display_name,
		       type, status, is_general, position, COALESCE(created_by::text, ''),
		       created_at, updated_at
		FROM chat.channels
		WHERE id = $1 AND workspace_id = $2 AND status = 'active'`,
		id, workspaceID,
	).Scan(
		&ch.ID, &ch.WorkspaceID, &ch.CategoryID, &ch.Slug, &ch.DisplayName,
		(*string)(&ch.Type), (*string)(&ch.Status), &ch.IsGeneral, &ch.Position, &ch.CreatedBy,
		&ch.CreatedAt, &ch.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Channel{}, domain.ErrNotFound
		}
		return domain.Channel{}, fmt.Errorf("get channel by id in workspace: %w", err)
	}
	return ch, nil
}

func (s *PGXChannelStore) GetVisibleChannelByID(ctx context.Context, workspaceID, channelID, userID string) (domain.Channel, error) {
	return s.getVisibleChannel(ctx, `
		SELECT c.id, c.workspace_id, COALESCE(c.category_id::text, ''), c.slug, c.display_name,
		       c.type, c.status, c.is_general, c.position, COALESCE(c.created_by::text, ''),
		       c.created_at, c.updated_at
		FROM chat.channels c
		JOIN chat.workspaces w
		  ON c.workspace_id = w.id AND w.status = 'active'
		JOIN chat.workspace_members wm
		  ON wm.workspace_id = c.workspace_id AND wm.user_id = $3 AND wm.status = 'active'
		WHERE c.workspace_id = $1
		  AND c.id = $2
		  AND c.status = 'active'
		  AND chat.channel_visible_to_user(c.id, $3::uuid)`,
		workspaceID, channelID, userID,
	)
}

// FilterUsersVisibleToChannel returns, of the given users, those who may read
// channelID in workspaceID — in one query.
//
// It is the same predicate GetVisibleChannelByID applies, asked about a list
// instead of one person: the identical workspace and channel conditions plus
// chat.channel_visible_to_user, which is the canonical definition of channel
// read access. Nothing here reimplements that rule; it calls it.
//
// The list is server-supplied (presence assertions this service wrote), never a
// client's, and the answer is a subset of it — so the query can only ever narrow
// what the caller already had.
func (s *PGXChannelStore) FilterUsersVisibleToChannel(
	ctx context.Context, workspaceID, channelID string, userIDs []string,
) ([]string, error) {
	if len(userIDs) == 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT candidate.user_id::text
		FROM unnest($3::uuid[]) AS candidate(user_id)
		JOIN chat.channels c
		  ON c.workspace_id = $1
		 AND c.id = $2
		 AND c.status = 'active'
		JOIN chat.workspaces w
		  ON c.workspace_id = w.id AND w.status = 'active'
		JOIN chat.workspace_members wm
		  ON wm.workspace_id = c.workspace_id
		 AND wm.user_id = candidate.user_id
		 AND wm.status = 'active'
		WHERE chat.channel_visible_to_user(c.id, candidate.user_id)`,
		workspaceID, channelID, userIDs,
	)
	if err != nil {
		return nil, fmt.Errorf("filter users visible to channel: %w", err)
	}
	defer rows.Close()

	allowed := make([]string, 0, len(userIDs))
	for rows.Next() {
		var userID string
		if scanErr := rows.Scan(&userID); scanErr != nil {
			return nil, fmt.Errorf("filter users visible to channel: %w", scanErr)
		}
		allowed = append(allowed, userID)
	}
	if rows.Err() != nil {
		return nil, fmt.Errorf("filter users visible to channel: %w", rows.Err())
	}
	return allowed, nil
}

func (s *PGXChannelStore) GetVisibleChannelBySlug(ctx context.Context, workspaceID, slug, userID string) (domain.Channel, error) {
	return s.getVisibleChannel(ctx, `
		SELECT c.id, c.workspace_id, COALESCE(c.category_id::text, ''), c.slug, c.display_name,
		       c.type, c.status, c.is_general, c.position, COALESCE(c.created_by::text, ''),
		       c.created_at, c.updated_at
		FROM chat.channels c
		JOIN chat.workspaces w
		  ON c.workspace_id = w.id AND w.status = 'active'
		JOIN chat.workspace_members wm
		  ON wm.workspace_id = c.workspace_id AND wm.user_id = $3 AND wm.status = 'active'
		WHERE c.workspace_id = $1
		  AND c.slug = $2
		  AND c.status = 'active'
		  AND chat.channel_visible_to_user(c.id, $3::uuid)`,
		workspaceID, slug, userID,
	)
}

func (s *PGXChannelStore) getVisibleChannel(ctx context.Context, query string, args ...any) (domain.Channel, error) {
	var ch domain.Channel
	err := s.pool.QueryRow(ctx, query, args...).Scan(
		&ch.ID, &ch.WorkspaceID, &ch.CategoryID, &ch.Slug, &ch.DisplayName,
		(*string)(&ch.Type), (*string)(&ch.Status), &ch.IsGeneral, &ch.Position, &ch.CreatedBy,
		&ch.CreatedAt, &ch.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Channel{}, domain.ErrNotFound
		}
		return domain.Channel{}, fmt.Errorf("get visible channel: %w", err)
	}
	return ch, nil
}

func (s *PGXChannelStore) ListChannelsByWorkspace(ctx context.Context, workspaceID string) ([]domain.Channel, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, workspace_id, COALESCE(category_id::text, ''), slug, display_name,
		       type, status, is_general, position, COALESCE(created_by::text, ''),
		       created_at, updated_at
		FROM chat.channels
		WHERE workspace_id = $1 AND status = 'active'
		ORDER BY position, display_name`,
		workspaceID,
	)
	if err != nil {
		return nil, fmt.Errorf("list channels: %w", err)
	}
	defer rows.Close()

	var channels []domain.Channel
	for rows.Next() {
		var ch domain.Channel
		if err := rows.Scan(
			&ch.ID, &ch.WorkspaceID, &ch.CategoryID, &ch.Slug, &ch.DisplayName,
			(*string)(&ch.Type), (*string)(&ch.Status), &ch.IsGeneral, &ch.Position, &ch.CreatedBy,
			&ch.CreatedAt, &ch.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan channel: %w", err)
		}
		channels = append(channels, ch)
	}
	return channels, rows.Err()
}

func (s *PGXChannelStore) ListVisibleChannelsByUser(ctx context.Context, workspaceID, userID string) ([]domain.Channel, error) {
	accesses, err := s.ListVisibleChannelAccessByUser(ctx, workspaceID, userID)
	if err != nil {
		return nil, err
	}
	channels := make([]domain.Channel, 0, len(accesses))
	for _, access := range accesses {
		channels = append(channels, access.Channel)
	}
	return channels, nil
}

// ListVisibleChannelAccessByUser returns visible channels and the caller's
// optional channel membership in one query.
//
// Each row also carries the channel's activity instant (issue #414), resolved
// by a lateral join in this same statement rather than by one read per channel:
// the cost of the listing does not grow with a query per row however many
// channels the user can see. The lateral hangs off rows the visibility
// predicate has already admitted — the same predicate that decides whether the
// channel appears at all — so a private channel the caller is not a member of
// neither appears here nor has its activity read. It projects created_at and
// nothing else; no message content, author or id is selected.
func (s *PGXChannelStore) ListVisibleChannelAccessByUser(ctx context.Context, workspaceID, userID string) ([]VisibleChannelAccess, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT c.id, c.workspace_id, COALESCE(c.category_id::text, ''), c.slug, c.display_name,
		       c.type, c.status, c.is_general, c.position, COALESCE(c.created_by::text, ''),
		       c.created_at, c.updated_at,
		       COALESCE(cm.channel_id::text, ''), COALESCE(cm.user_id::text, ''),
		       COALESCE(cm.role::text, ''),
		       lm.created_at AS last_message_at
		FROM chat.channels c
		JOIN chat.workspaces w
		  ON c.workspace_id = w.id AND w.status = 'active'
		JOIN chat.workspace_members wm
		  ON wm.workspace_id = c.workspace_id AND wm.user_id = $2 AND wm.status = 'active'
		LEFT JOIN chat.channel_members cm
		  ON cm.channel_id = c.id AND cm.user_id = $2
		LEFT JOIN LATERAL (
		    SELECT m.created_at
		    FROM chat.messages m
		    WHERE m.workspace_id = c.workspace_id
		      AND m.channel_id = c.id
		      -- RF-21: a message still being link-scanned must not reorder
		      -- anyone's sidebar or light up a conversation. It has been shown to
		      -- nobody, so it is not activity yet; it becomes activity when the
		      -- scan promotes it, which updates this timestamp naturally.
		      AND m.status <> 'pending_link_scan'
		    ORDER BY m.created_at DESC, m.id DESC
		    LIMIT 1
		) lm ON true
		WHERE c.workspace_id = $1
		  AND c.status = 'active'
		  AND chat.channel_visible_to_user(c.id, $2::uuid)
		ORDER BY c.position, c.display_name`,
		workspaceID, userID,
	)
	if err != nil {
		return nil, fmt.Errorf("list visible channels: %w", err)
	}
	defer rows.Close()

	var accesses []VisibleChannelAccess
	for rows.Next() {
		var ch domain.Channel
		var memberChannelID, memberUserID, memberRole string
		var lastMessageAt *time.Time
		if err := rows.Scan(
			&ch.ID, &ch.WorkspaceID, &ch.CategoryID, &ch.Slug, &ch.DisplayName,
			(*string)(&ch.Type), (*string)(&ch.Status), &ch.IsGeneral, &ch.Position, &ch.CreatedBy,
			&ch.CreatedAt, &ch.UpdatedAt,
			&memberChannelID, &memberUserID, &memberRole,
			&lastMessageAt,
		); err != nil {
			return nil, fmt.Errorf("scan visible channel: %w", err)
		}
		var member *domain.ChannelMember
		if memberChannelID != "" {
			member = &domain.ChannelMember{
				ChannelID: memberChannelID,
				UserID:    memberUserID,
				Role:      domain.ChannelRole(memberRole),
			}
		}
		accesses = append(accesses, VisibleChannelAccess{Channel: ch, ChannelMember: member, LastMessageAt: lastMessageAt})
	}
	return accesses, rows.Err()
}

// channelManagerRoles is the SQL statement of domain.CanManageWorkspace: the
// roles that may change what a channel *is*.
//
// Owner and admin, and deliberately not the RF-74 workspace moderator — that
// role moderates channel structure and membership, which is a different
// predicate (domain.CanManageChannelMembers, spelled out separately in
// member_store.go). A constant so the allowlist has one definition here, and
// never `role != 'guest'`: an unrecognised role — a row written before a CHECK
// was widened, a value from a future migration — must fail closed.
//
// This is chat.workspace_members.role. The per-channel moderator on
// chat.channel_members is a different scope and is never consulted here.
const channelManagerRoles = `('owner', 'admin')`

// lockActorChannelManagementSQL re-derives the actor's workspace management
// authority and holds the membership row for the rest of the transaction.
//
// FOR SHARE rather than FOR UPDATE, matching managerAuthorizedWorkspace in
// channel_category_store.go and PGXMemberStore.AddChannelMembers: demoting a
// role, suspending a membership and deleting it are all UPDATE/DELETE of that
// row, which take FOR NO KEY UPDATE or FOR UPDATE and conflict with FOR SHARE —
// so a revocation in flight is serialised against the update, in both
// directions. Two managers editing different channels of the same workspace
// both take FOR SHARE and do not block each other, which FOR UPDATE would have
// made them do for no safety gained.
const lockActorChannelManagementSQL = `
	SELECT true
	FROM chat.workspace_members wm
	JOIN chat.workspaces w
	  ON w.id = wm.workspace_id AND w.status = 'active'
	WHERE wm.workspace_id = $1::uuid
	  AND wm.user_id = $2::uuid
	  AND wm.status = 'active'
	  AND wm.role IN ` + channelManagerRoles + `
	FOR SHARE OF wm`

// requireChannelManager is the authorization decision that counts, taken inside
// the caller's transaction so it cannot be overtaken by a concurrent revocation.
//
// The service checks the same predicate first for a legible error; the decision
// is deliberately not passed down as a boolean, because a boolean computed a
// moment ago is exactly the thing this query exists to distrust.
//
// One answer — ErrForbidden — for a revoked role, a suspended or removed
// membership and a disabled workspace, so the error cannot be used to tell them
// apart.
func requireChannelManager(ctx context.Context, q channelQuerier, workspaceID, callerID string) error {
	var authorized bool
	err := q.QueryRow(ctx, lockActorChannelManagementSQL, workspaceID, callerID).Scan(&authorized)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrForbidden
		}
		return fmt.Errorf("lock actor workspace membership: %w", err)
	}
	return nil
}

// UpdateChannel persists the channel's mutable state with the authorization
// serialized against the write.
//
// The service's own check happens before the transaction opens; in between, the
// actor can be demoted from admin to member, suspended, or removed from the
// workspace outright, and without this they would still get to write. Locking
// the membership row also serialises this against a concurrent role change
// rather than merely observing one that already committed.
//
// Lock order is the canonical one channelmembership.LockChannelSQL documents and
// every membership mutation obeys: the channel row first, then the actor's
// membership, then the mutation. Taking the membership first would invert the
// order PGXMemberStore.AddChannelMembers uses on the same two rows and make a
// cycle reachable.
//
// Always a transaction, including when no membership has to be seeded: the
// authorization and the UPDATE are two statements and must not be two
// transactions.
func (s *PGXChannelStore) UpdateChannel(ctx context.Context, input UpdateChannelInput) (UpdateChannelResult, error) {
	if input.CallerID == "" {
		return UpdateChannelResult{}, domain.ErrForbidden
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return UpdateChannelResult{}, fmt.Errorf("begin update channel: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	result, err := updateChannelAuthorized(ctx, tx, input)
	if err != nil {
		return UpdateChannelResult{}, err
	}
	if input.EnsureMemberUserID != "" {
		if err := addChannelMember(ctx, tx, result.Channel.ID, input.EnsureMemberUserID, domain.ChannelRoleMember); err != nil {
			return UpdateChannelResult{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return UpdateChannelResult{}, fmt.Errorf("commit update channel: %w", err)
	}
	committed = true
	return result, nil
}

// updateChannelAuthorized runs the steps in canonical lock order: pin the
// channel, re-derive and hold the actor's authority, write, and record the
// rename when there was one.
//
// A channel that does not exist stops at the first step, so an unauthorized
// caller and a missing channel keep the errors they already had.
//
// The system message is written here rather than by the caller, and that is the
// whole point: it shares this transaction, so a rename that rolls back leaves no
// event claiming it happened, and an event never exists without the change it
// describes (issue #527). A failure to insert it fails the rename.
func updateChannelAuthorized(ctx context.Context, tx pgx.Tx, input UpdateChannelInput) (UpdateChannelResult, error) {
	previousName, err := lockChannelForUpdate(ctx, tx, input.ChannelID)
	if err != nil {
		return UpdateChannelResult{}, err
	}
	if err := requireChannelManager(ctx, tx, input.WorkspaceID, input.CallerID); err != nil {
		return UpdateChannelResult{}, err
	}
	channel, err := updateChannel(ctx, tx, input)
	if err != nil {
		return UpdateChannelResult{}, err
	}
	// Only a real change of name is a rename. An update that moved the channel
	// between categories, or a PATCH that set the name it already had, has
	// nothing to announce and must not put a line in the timeline.
	if channel.DisplayName == previousName {
		return UpdateChannelResult{Channel: channel}, nil
	}
	event, err := InsertConversationEvent(ctx, tx, ConversationEventInput{
		WorkspaceID: input.WorkspaceID,
		ChannelID:   channel.ID,
		ActorID:     input.CallerID,
		Event:       domain.ConversationEventRenamed,
		Payload:     domain.ConversationEventPayload{OldName: previousName, NewName: channel.DisplayName},
	})
	if err != nil {
		return UpdateChannelResult{}, err
	}
	return UpdateChannelResult{Channel: channel, Event: event}, nil
}

// lockChannelForUpdate pins the channel row and returns the name it had.
//
// The previous name has to be read here, under the lock, and not before the
// transaction: a value read outside it could already be stale by the time the
// UPDATE runs, and the event would then describe a rename that never happened.
//
// It is channelmembership.LockChannelSQL, the serialization protocol shared with
// admin-service, and it is first for the reason that file documents. Scoping is
// still the UPDATE's job: a channel in another workspace is locked here and then
// matches nothing below, which keeps "wrong workspace" and "does not exist"
// indistinguishable.
// lockChannelForUpdateSQL is channelmembership.LockChannelSQL widened by one
// column — the same thing admin-service's channel store does when it needs a
// fact alongside the lock. The lock, the row and the predicate are identical;
// only the projection differs, so the shared serialization protocol still holds.
const lockChannelForUpdateSQL = `
	SELECT id, display_name FROM chat.channels WHERE id = $1::uuid FOR UPDATE`

func lockChannelForUpdate(ctx context.Context, tx pgx.Tx, channelID string) (string, error) {
	var lockedID, previousName string
	err := tx.QueryRow(ctx, lockChannelForUpdateSQL, channelID).Scan(&lockedID, &previousName)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", domain.ErrNotFound
		}
		return "", fmt.Errorf("lock channel for update: %w", err)
	}
	return previousName, nil
}

func updateChannel(ctx context.Context, q channelQuerier, input UpdateChannelInput) (domain.Channel, error) {
	var categoryID *string
	if input.CategoryID != "" {
		categoryID = &input.CategoryID
	}
	var ch domain.Channel
	err := q.QueryRow(ctx, `
		UPDATE chat.channels
		SET category_id = $3,
		    slug = $4,
		    display_name = $5,
		    type = $6,
		    position = $7,
		    updated_at = now()
		WHERE workspace_id = $1
		  AND id = $2
		  AND status = 'active'
		  AND is_general = false
		RETURNING id, workspace_id, COALESCE(category_id::text, ''), slug, display_name,
		          type, status, is_general, position, COALESCE(created_by::text, ''),
		          created_at, updated_at`,
		input.WorkspaceID, input.ChannelID, categoryID, input.Slug, input.DisplayName, string(input.Type), input.Position,
	).Scan(
		&ch.ID, &ch.WorkspaceID, &ch.CategoryID, &ch.Slug, &ch.DisplayName,
		(*string)(&ch.Type), (*string)(&ch.Status), &ch.IsGeneral, &ch.Position, &ch.CreatedBy,
		&ch.CreatedAt, &ch.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Channel{}, domain.ErrNotFound
		}
		if mapped := mapChannelWriteError(err); mapped != nil {
			return domain.Channel{}, mapped
		}
		return domain.Channel{}, fmt.Errorf("update channel: %w", err)
	}
	return ch, nil
}

// ArchiveChannel marks a non-general channel archived and records a
// conversation_archived system event in the same transaction (issue #685),
// the same pattern updateChannelAuthorized already follows for a rename:
// the write and the event either both commit or neither does.
func (s *PGXChannelStore) ArchiveChannel(ctx context.Context, workspaceID, channelID, actorID string) (domain.Channel, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Channel{}, fmt.Errorf("begin archive channel: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	var ch domain.Channel
	err = tx.QueryRow(ctx, `
		UPDATE chat.channels
		SET status = 'archived',
		    updated_at = now()
		WHERE workspace_id = $1
		  AND id = $2
		  AND status = 'active'
		  AND is_general = false
		RETURNING id, workspace_id, COALESCE(category_id::text, ''), slug, display_name,
		          type, status, is_general, position, COALESCE(created_by::text, ''),
		          created_at, updated_at`,
		workspaceID, channelID,
	).Scan(
		&ch.ID, &ch.WorkspaceID, &ch.CategoryID, &ch.Slug, &ch.DisplayName,
		(*string)(&ch.Type), (*string)(&ch.Status), &ch.IsGeneral, &ch.Position, &ch.CreatedBy,
		&ch.CreatedAt, &ch.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Channel{}, domain.ErrNotFound
		}
		if mapped := mapChannelWriteError(err); mapped != nil {
			return domain.Channel{}, mapped
		}
		return domain.Channel{}, fmt.Errorf("archive channel: %w", err)
	}

	if _, err := InsertConversationEvent(ctx, tx, ConversationEventInput{
		WorkspaceID: workspaceID, ChannelID: channelID,
		ActorID: actorID, Event: domain.ConversationEventArchived,
	}); err != nil {
		return domain.Channel{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.Channel{}, fmt.Errorf("commit archive channel: %w", err)
	}
	committed = true
	return ch, nil
}

func mapChannelWriteError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return nil
	}
	switch pgErr.ConstraintName {
	case "idx_channels_one_general_per_workspace":
		return domain.ErrGeneralChannelExists
	case "channels_workspace_slug_unique":
		return domain.ErrDuplicateSlug
	case "channels_workspace_category_fk":
		return domain.ErrInvalidInput
	}
	return nil
}
