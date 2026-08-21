package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nicrepository/nchat/libs/go/platform/channelmembership"
	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
)

// PGXChannelDirectoryStore serves the platform channel and conversation
// directories.
//
// Nothing in this file reads chat.messages.body_text, and nothing joins
// chat.dm_members to a name. The queries below count rows and read timestamps;
// the console is a map of where conversation happens, never a window into it.
type PGXChannelDirectoryStore struct {
	pool Pool
}

func NewPGXChannelDirectoryStore(pool Pool) *PGXChannelDirectoryStore {
	return &PGXChannelDirectoryStore{pool: pool}
}

// channelSummarySelect projects one channel directory row.
//
// The three laterals are what makes this one query instead of one per row:
//   - the member count is an index-only scan of chat.channel_members' primary
//     key (channel_id, user_id);
//   - the moderator count reads the same index and filters;
//   - last activity is a single backwards step on idx_messages_channel
//     (workspace_id, channel_id, created_at, id), so it costs one index tuple
//     rather than a scan of the channel's history.
//
// created_by is resolved to a name and an e-mail here rather than returned as a
// UUID for the console to look up, which is the N+1 this shape exists to avoid.
const channelSummarySelect = `
	SELECT c.id::text,
	       c.workspace_id::text,
	       w.name,
	       c.slug,
	       c.display_name,
	       c.type,
	       c.status,
	       c.is_general,
	       COALESCE(members.total, 0),
	       COALESCE(members.moderators, 0),
	       COALESCE(creator.display_name, ''),
	       COALESCE(creator.email::text, ''),
	       c.created_at,
	       activity.last_message_at
	FROM chat.channels AS c
	JOIN chat.workspaces AS w ON w.id = c.workspace_id
	LEFT JOIN auth.users AS creator ON creator.id = c.created_by
	LEFT JOIN LATERAL (
	    SELECT count(*) AS total,
	           count(*) FILTER (WHERE cm.role = 'moderator') AS moderators
	    FROM chat.channel_members AS cm
	    WHERE cm.channel_id = c.id
	) AS members ON true
	LEFT JOIN LATERAL (
	    SELECT m.created_at AS last_message_at
	    FROM chat.messages AS m
	    WHERE m.workspace_id = c.workspace_id
	      AND m.channel_id = c.id
	    ORDER BY m.created_at DESC, m.id DESC
	    LIMIT 1
	) AS activity ON true`

// listChannelsQuery pages the directory. Same shape as the user directory: every
// filter is a bound parameter with an IS NULL escape, the order is fixed, and
// resumption is a row-value comparison matching idx_channels_directory_page.
const listChannelsQuery = channelSummarySelect + `
	WHERE ($1::uuid IS NULL OR c.workspace_id = $1)
	  AND ($2::text IS NULL OR c.type = $2)
	  AND ($3::text IS NULL OR c.status = $3)
	  AND ($4::text IS NULL
	       OR c.display_name ILIKE $4 ` + likeEscapeClause + `
	       OR c.slug ILIKE $4 ` + likeEscapeClause + `)
	  AND ($5::int IS NULL OR COALESCE(members.total, 0) >= $5)
	  AND ($6::timestamptz IS NULL OR activity.last_message_at >= $6)
	  AND ($7::uuid IS NULL
	       OR c.created_by = $7
	       OR EXISTS (
	              SELECT 1 FROM chat.channel_members AS admin_cm
	              WHERE admin_cm.channel_id = c.id
	                AND admin_cm.user_id = $7
	                AND admin_cm.role = 'moderator'
	          ))
	  AND ($8::timestamptz IS NULL OR (c.created_at, c.id) < ($8, $9::uuid))
	ORDER BY c.created_at DESC, c.id DESC
	LIMIT $10`

const getChannelQuery = channelSummarySelect + `
	WHERE c.id = $1::uuid
	LIMIT 1`

// ListChannels returns one page of the platform channel directory.
//
// Private channels are listed. That is not a widening of the channel read
// policy: the row says a private channel exists, who administers it and how
// busy it is, and the capability admin.channels.read is what authorizes that.
// No message, member name or content of a private channel is reachable from
// here, and chat.channel_visible_to_user remains the only thing that decides
// who may read one.
func (s *PGXChannelDirectoryStore) ListChannels(ctx context.Context, filter domain.AdminChannelFilter) (domain.Page[domain.AdminChannelSummary], error) {
	if s == nil || s.pool == nil {
		return domain.Page[domain.AdminChannelSummary]{}, domain.ErrUnavailable
	}
	limit := domain.ClampPageSize(filter.Limit)

	var activeSince any
	if window, ok := domain.ChannelActivityFilter[filter.ActiveWithin]; ok {
		activeSince = time.Now().UTC().Add(-window)
	}
	var minMembers any
	if filter.MinMembers > 0 {
		minMembers = filter.MinMembers
	}
	rows, err := s.pool.Query(ctx, listChannelsQuery,
		nullableText(filter.WorkspaceID),
		nullableText(filter.Type),
		nullableText(filter.Status),
		nullableText(likePattern(filter.Query)),
		minMembers,
		activeSince,
		nullableText(filter.AdministeredBy),
		nullableCursorTime(filter.Cursor),
		nullableText(filter.Cursor.ID),
		limit+1,
	)
	if err != nil {
		return domain.Page[domain.AdminChannelSummary]{}, fmt.Errorf("list admin channels: %w", err)
	}
	defer rows.Close()

	items := make([]domain.AdminChannelSummary, 0, limit)
	for rows.Next() {
		channel, err := scanChannelSummary(rows)
		if err != nil {
			return domain.Page[domain.AdminChannelSummary]{}, err
		}
		items = append(items, channel)
	}
	if err := rows.Err(); err != nil {
		return domain.Page[domain.AdminChannelSummary]{}, fmt.Errorf("read admin channels: %w", err)
	}
	return paginate(items, limit, func(c domain.AdminChannelSummary) domain.Cursor {
		return domain.Cursor{At: c.CreatedAt, ID: c.ID}
	}), nil
}

// channelPeopleQuery lists the people who can administer one channel.
//
// Two roles in one result set, distinguished by the `role` column and kept in
// separate fields by the caller, because they are not the same authority:
// 'moderator' is chat.channel_members.role, a per-channel role, while 'owner'
// and 'admin' are chat.workspace_members.role, which governs the workspace.
// Merging them into one "admins" list is exactly the collapse
// docs/security/rbac-matrix.md exists to prevent.
//
// It is bounded: an operator reviewing who administers a channel needs the
// list, not the membership export, and an unbounded IN-page join is how a
// listing becomes a directory dump.
const channelPeopleQuery = `
	(
	    SELECT u.id::text, u.display_name, u.email::text, cm.role
	    FROM chat.channel_members AS cm
	    JOIN auth.users AS u ON u.id = cm.user_id AND u.deleted_at IS NULL
	    WHERE cm.channel_id = $1::uuid AND cm.role = 'moderator'
	    ORDER BY u.display_name, u.id
	    LIMIT $3
	)
	UNION ALL
	(
	    SELECT u.id::text, u.display_name, u.email::text, wm.role
	    FROM chat.workspace_members AS wm
	    JOIN auth.users AS u ON u.id = wm.user_id AND u.deleted_at IS NULL
	    WHERE wm.workspace_id = $2::uuid
	      AND wm.status = 'active'
	      AND wm.role IN ('owner', 'admin')
	    ORDER BY u.display_name, u.id
	    LIMIT $3
	)`

const channelPeopleLimit = 50

// GetChannel loads one channel with the people who administer it and its
// message volume.
//
// The message count is an aggregate, not a listing: it says how much traffic a
// channel carries, which is what an operator deciding whether to archive it
// needs, and it reads no message.
func (s *PGXChannelDirectoryStore) GetChannel(ctx context.Context, channelID string) (domain.AdminChannelDetail, error) {
	if s == nil || s.pool == nil {
		return domain.AdminChannelDetail{}, domain.ErrUnavailable
	}
	rows, err := s.pool.Query(ctx, getChannelQuery, channelID)
	if err != nil {
		return domain.AdminChannelDetail{}, fmt.Errorf("get admin channel: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return domain.AdminChannelDetail{}, fmt.Errorf("get admin channel: %w", err)
		}
		return domain.AdminChannelDetail{}, domain.ErrNotFound
	}
	summary, err := scanChannelSummary(rows)
	if err != nil {
		return domain.AdminChannelDetail{}, err
	}
	rows.Close()

	detail := domain.AdminChannelDetail{
		AdminChannelSummary: summary,
		Moderators:          []domain.ChannelMemberRef{},
		WorkspaceAdmins:     []domain.ChannelMemberRef{},
		Members:             []domain.ChannelMemberRef{},
	}
	if err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(cat.name, ''), COALESCE(counts.total, 0)
		FROM chat.channels AS c
		LEFT JOIN chat.channel_categories AS cat ON cat.id = c.category_id
		LEFT JOIN LATERAL (
		    SELECT count(*) AS total FROM chat.messages AS m
		    WHERE m.workspace_id = c.workspace_id AND m.channel_id = c.id
		) AS counts ON true
		WHERE c.id = $1::uuid`, channelID,
	).Scan(&detail.CategoryName, &detail.MessageCount); err != nil {
		return domain.AdminChannelDetail{}, fmt.Errorf("get admin channel aggregates: %w", err)
	}

	people, err := s.pool.Query(ctx, channelPeopleQuery, channelID, summary.WorkspaceID, channelPeopleLimit)
	if err != nil {
		return domain.AdminChannelDetail{}, fmt.Errorf("list admin channel people: %w", err)
	}
	defer people.Close()
	for people.Next() {
		var ref domain.ChannelMemberRef
		if err := people.Scan(&ref.UserID, &ref.DisplayName, &ref.Email, &ref.Role); err != nil {
			return domain.AdminChannelDetail{}, fmt.Errorf("scan admin channel person: %w", err)
		}
		if ref.Role == "moderator" {
			detail.Moderators = append(detail.Moderators, ref)
			continue
		}
		detail.WorkspaceAdmins = append(detail.WorkspaceAdmins, ref)
	}
	if err := people.Err(); err != nil {
		return domain.AdminChannelDetail{}, fmt.Errorf("read admin channel people: %w", err)
	}

	members, err := s.pool.Query(ctx, listChannelMembersQuery, channelID, channelPeopleLimit)
	if err != nil {
		return domain.AdminChannelDetail{}, fmt.Errorf("list admin channel members: %w", err)
	}
	defer members.Close()
	for members.Next() {
		var ref domain.ChannelMemberRef
		if err := members.Scan(&ref.UserID, &ref.DisplayName, &ref.Email, &ref.Role); err != nil {
			return domain.AdminChannelDetail{}, fmt.Errorf("scan admin channel member: %w", err)
		}
		detail.Members = append(detail.Members, ref)
	}
	if err := members.Err(); err != nil {
		return domain.AdminChannelDetail{}, fmt.Errorf("read admin channel members: %w", err)
	}
	return detail, nil
}

// listChannelMembersQuery is the bounded membership preview the detail view
// administers.
//
// It carries a name, an e-mail and the per-channel role, and nothing about what
// anybody said. Soft-deleted accounts are excluded so a removed person is never
// offered as something to remove again.
const listChannelMembersQuery = `
	SELECT u.id::text, u.display_name, u.email::text, cm.role
	FROM chat.channel_members AS cm
	JOIN auth.users AS u ON u.id = cm.user_id AND u.deleted_at IS NULL
	WHERE cm.channel_id = $1::uuid
	ORDER BY u.display_name, u.id
	LIMIT $2`

// UpdateChannelStatus archives or unarchives a channel.
//
// The row is locked and read first, so the three refusals stay distinguishable:
// a channel that does not exist, the workspace's #geral channel — which
// chat-service treats as immutable and which this console must not be a second
// way around — and a transition that is not a change because another request
// made it first.
//
// Archiving is reversible and destroys nothing. There is no delete here: a
// channel's history is not something an administrative click removes.
func (s *PGXChannelDirectoryStore) UpdateChannelStatus(ctx context.Context, channelID, newStatus string) (domain.AdminChannelSummary, error) {
	if s == nil || s.pool == nil {
		return domain.AdminChannelSummary{}, domain.ErrUnavailable
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.AdminChannelSummary{}, fmt.Errorf("begin channel status transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var current string
	var isGeneral bool
	if err := tx.QueryRow(ctx,
		`SELECT status, is_general FROM chat.channels WHERE id = $1::uuid FOR UPDATE`, channelID,
	).Scan(&current, &isGeneral); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.AdminChannelSummary{}, domain.ErrNotFound
		}
		return domain.AdminChannelSummary{}, fmt.Errorf("lock channel: %w", err)
	}
	if isGeneral {
		return domain.AdminChannelSummary{}, domain.ErrForbidden
	}
	if !domain.ValidChannelStatusTransition(current, newStatus) {
		return domain.AdminChannelSummary{}, domain.ErrConflict
	}
	if _, err := tx.Exec(ctx,
		`UPDATE chat.channels SET status = $2, updated_at = now() WHERE id = $1::uuid`,
		channelID, newStatus,
	); err != nil {
		return domain.AdminChannelSummary{}, fmt.Errorf("update channel status: %w", err)
	}
	rows, err := tx.Query(ctx, getChannelQuery, channelID)
	if err != nil {
		return domain.AdminChannelSummary{}, fmt.Errorf("reread channel: %w", err)
	}
	if !rows.Next() {
		rows.Close()
		return domain.AdminChannelSummary{}, fmt.Errorf("reread channel: %w", domain.ErrNotFound)
	}
	updated, err := scanChannelSummary(rows)
	rows.Close()
	if err != nil {
		return domain.AdminChannelSummary{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.AdminChannelSummary{}, fmt.Errorf("commit channel status transaction: %w", err)
	}
	return updated, nil
}

func scanChannelSummary(rows pgx.Rows) (domain.AdminChannelSummary, error) {
	var channel domain.AdminChannelSummary
	if err := rows.Scan(
		&channel.ID, &channel.WorkspaceID, &channel.WorkspaceName,
		&channel.Slug, &channel.DisplayName, &channel.Type, &channel.Status, &channel.IsGeneral,
		&channel.MemberCount, &channel.ModeratorCount,
		&channel.CreatedByName, &channel.CreatedByEmail,
		&channel.CreatedAt, &channel.LastActivityAt,
	); err != nil {
		return domain.AdminChannelSummary{}, fmt.Errorf("scan admin channel: %w", err)
	}
	channel.CreatedAt = channel.CreatedAt.UTC()
	if channel.LastActivityAt != nil {
		utc := channel.LastActivityAt.UTC()
		channel.LastActivityAt = &utc
	}
	return channel, nil
}

// listConversationsQuery pages private conversation *metadata*.
//
// Read the projection: identifiers, workspace, kind, state, two counts and
// three timestamps. There is no title, no participant name, no body and no
// "latest message" — a group's title is written by its participants and its
// membership is the thing that decides who may read it, so neither belongs to
// an administrator who is not one.
//
// The participant count reads idx_dm_members_conversation_active; the message
// count and last activity read idx_messages_dm, which leads with
// (workspace_id, dm_conversation_id) — so both are index work proportional to
// the page, not to the platform's history.
const listConversationsQuery = `
	SELECT d.id::text,
	       d.workspace_id::text,
	       w.name,
	       d.type,
	       d.status,
	       COALESCE(participants.total, 0),
	       COALESCE(volume.total, 0),
	       d.created_at,
	       d.updated_at,
	       activity.last_message_at
	FROM chat.dm_conversations AS d
	JOIN chat.workspaces AS w ON w.id = d.workspace_id
	LEFT JOIN LATERAL (
	    SELECT count(*) AS total
	    FROM chat.dm_members AS dm
	    WHERE dm.conversation_id = d.id AND dm.status = 'active'
	) AS participants ON true
	LEFT JOIN LATERAL (
	    SELECT count(*) AS total
	    FROM chat.messages AS m
	    WHERE m.workspace_id = d.workspace_id AND m.dm_conversation_id = d.id
	) AS volume ON true
	LEFT JOIN LATERAL (
	    SELECT m.created_at AS last_message_at
	    FROM chat.messages AS m
	    WHERE m.workspace_id = d.workspace_id AND m.dm_conversation_id = d.id
	    ORDER BY m.created_at DESC, m.id DESC
	    LIMIT 1
	) AS activity ON true
	WHERE ($1::uuid IS NULL OR d.workspace_id = $1)
	  AND ($2::text IS NULL OR d.type = $2)
	  AND ($3::text IS NULL OR d.status = $3)
	  AND ($4::timestamptz IS NULL OR (d.updated_at, d.id) < ($4, $5::uuid))
	ORDER BY d.updated_at DESC, d.id DESC
	LIMIT $6`

// ListConversations returns one page of private conversation metadata.
func (s *PGXChannelDirectoryStore) ListConversations(ctx context.Context, filter domain.AdminConversationFilter) (domain.Page[domain.AdminConversationSummary], error) {
	if s == nil || s.pool == nil {
		return domain.Page[domain.AdminConversationSummary]{}, domain.ErrUnavailable
	}
	limit := domain.ClampPageSize(filter.Limit)

	rows, err := s.pool.Query(ctx, listConversationsQuery,
		nullableText(filter.WorkspaceID),
		nullableText(filter.Type),
		nullableText(filter.Status),
		nullableCursorTime(filter.Cursor),
		nullableText(filter.Cursor.ID),
		limit+1,
	)
	if err != nil {
		return domain.Page[domain.AdminConversationSummary]{}, fmt.Errorf("list admin conversations: %w", err)
	}
	defer rows.Close()

	items := make([]domain.AdminConversationSummary, 0, limit)
	for rows.Next() {
		var conversation domain.AdminConversationSummary
		if err := rows.Scan(
			&conversation.ID, &conversation.WorkspaceID, &conversation.WorkspaceName,
			&conversation.Type, &conversation.Status,
			&conversation.ParticipantCount, &conversation.MessageCount,
			&conversation.CreatedAt, &conversation.UpdatedAt, &conversation.LastActivityAt,
		); err != nil {
			return domain.Page[domain.AdminConversationSummary]{}, fmt.Errorf("scan admin conversation: %w", err)
		}
		conversation.CreatedAt = conversation.CreatedAt.UTC()
		conversation.UpdatedAt = conversation.UpdatedAt.UTC()
		if conversation.LastActivityAt != nil {
			utc := conversation.LastActivityAt.UTC()
			conversation.LastActivityAt = &utc
		}
		items = append(items, conversation)
	}
	if err := rows.Err(); err != nil {
		return domain.Page[domain.AdminConversationSummary]{}, fmt.Errorf("read admin conversations: %w", err)
	}
	return paginate(items, limit, func(c domain.AdminConversationSummary) domain.Cursor {
		return domain.Cursor{At: c.UpdatedAt, ID: c.ID}
	}), nil
}

// ---------------------------------------------------------------------------
// Membership
// ---------------------------------------------------------------------------

// addChannelMembersQuery admits a list of candidates to a channel.
//
// The eligibility half is channelmembership.EligibleTargetsCTE, the same string
// chat-service embeds. That is the point: who may be added to a channel is a
// fact about the channel and the person, and the two services that write
// chat.channel_members must not each carry their own copy of it.
//
// What is deliberately NOT shared is the actor check. chat-service re-derives
// the caller's owner/admin/moderator membership inside its transaction, because
// there the authority is a workspace role that can be revoked mid-request. Here
// the authority is a platform capability, re-read from the database by the
// session guard on this very request, and the actor is typically not a member
// of the workspace at all. Copying chat-service's actor predicate would have
// refused every legitimate administrative add; copying it *and* relaxing it
// would have been the divergent second rule this shares the CTE to avoid.
//
// ON CONFLICT DO NOTHING makes a retry a success that adds nobody, and the two
// counts let the caller say which of those happened.
const addChannelMembersQuery = `
	WITH eligible AS (` + channelmembership.EligibleTargetsCTE + `
	),
	inserted AS (
		INSERT INTO chat.channel_members (channel_id, user_id, role)
		SELECT $2::uuid, user_id, $4
		FROM eligible
		ON CONFLICT (channel_id, user_id) DO NOTHING
		RETURNING user_id
	)
	SELECT (SELECT count(*) FROM eligible), (SELECT count(*) FROM inserted)`

const countChannelMembersQuery = `
	SELECT count(*) FROM chat.channel_members WHERE channel_id = $1::uuid`

// lockChannelQuery reads the facts every membership mutation is decided
// against, and serializes the mutation against every other one on the same
// channel.
//
// FOR UPDATE, not FOR SHARE. A shared lock lets two mutations proceed together,
// and then each counts the members it can see — its own write plus what was
// committed when it started — so two adds from ten members both answer eleven
// while the committed result is twelve. `member_count` is contractually the
// total the operation produced, so one of those answers would be a lie.
//
// This is channelmembership.LockChannelSQL widened to also return the two facts
// this store decides against. The locked object, the lock mode and the position
// (first statement of the transaction) are identical to the shared protocol;
// only the projection differs, and TestLockChannelQuery_ObeysTheSharedProtocol
// keeps them from drifting apart.
const lockChannelQuery = `
	SELECT workspace_id::text, is_general
	FROM chat.channels
	WHERE id = $1::uuid
	FOR UPDATE`

// AddChannelMembers admits users to a channel from the platform scope.
//
// All-or-nothing: if any requested target is not eligible — not an active
// member of the channel's workspace, suspended, soft-deleted, or the channel
// itself is archived — nothing is written and the caller gets ErrConflict.
//
// ErrConflict and not ErrForbidden, which is where this deliberately differs
// from chat-service. There, a refusal is deliberately indistinguishable to a
// workspace peer who must not learn which of several reasons applied. Here the
// caller already holds admin.channels.manage and can already list every channel
// and every user, so there is nothing left to conceal — and 403 in this API
// means "you lack the capability", which would be a lie.
//
// This adds membership. It adds no read access for the actor: the actor is not
// a member afterwards, and no message becomes reachable from anywhere in this
// service.
func (s *PGXChannelDirectoryStore) AddChannelMembers(ctx context.Context, channelID string, userIDs []string) (domain.ChannelMembershipChange, error) {
	if s == nil || s.pool == nil {
		return domain.ChannelMembershipChange{}, domain.ErrUnavailable
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.ChannelMembershipChange{}, fmt.Errorf("begin add channel members: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var workspaceID string
	var isGeneral bool
	if err := tx.QueryRow(ctx, lockChannelQuery, channelID).Scan(&workspaceID, &isGeneral); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ChannelMembershipChange{}, domain.ErrNotFound
		}
		return domain.ChannelMembershipChange{}, fmt.Errorf("lock channel for membership: %w", err)
	}
	// The channel's own status is not read here: an archived channel is refused
	// by the shared eligibility rule, which requires c.status = 'active' and is
	// evaluated by the same statement that writes. Checking it twice, in two
	// places, is how the two answers start to disagree.
	//
	// The row lock above is held until commit, so the count below is taken
	// after this transaction's insert and before anybody else may add or
	// remove anyone here.

	change := domain.ChannelMembershipChange{ChannelID: channelID, WorkspaceID: workspaceID}
	var eligible, inserted int
	if err := tx.QueryRow(ctx, addChannelMembersQuery,
		workspaceID, channelID, userIDs, channelmembership.DefaultChannelRole,
	).Scan(&eligible, &inserted); err != nil {
		return domain.ChannelMembershipChange{}, fmt.Errorf("add channel members: %w", err)
	}
	// Fewer eligible rows than requested means at least one target does not
	// belong to this workspace, is suspended or deleted, or the channel is
	// archived. The transaction rolls back, so nothing partial survives.
	if eligible != len(userIDs) {
		return domain.ChannelMembershipChange{}, domain.ErrConflict
	}
	change.Added = inserted
	change.AlreadyMembers = eligible - inserted

	if err := tx.QueryRow(ctx, countChannelMembersQuery, channelID).Scan(&change.MemberCount); err != nil {
		return domain.ChannelMembershipChange{}, fmt.Errorf("count channel members: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ChannelMembershipChange{}, fmt.Errorf("commit add channel members: %w", err)
	}
	return change, nil
}

// RemoveChannelMember removes one membership from the platform scope.
//
// Idempotent, exactly as chat-service's RemoveChannelMember is: removing
// somebody who is not a member reports Removed=false and succeeds, because the
// caller's intent — "this person is not in this channel" — already holds, and a
// 404 would make a safe retry look like a failure.
//
// #geral is refused, mirroring domain.ErrCannotLeaveGeneralChannel in
// chat-service. Every member of a workspace belongs to its general channel by
// construction; a console that could take somebody out of it would be a second
// way around an invariant the chat domain maintains everywhere else.
func (s *PGXChannelDirectoryStore) RemoveChannelMember(ctx context.Context, channelID, userID string) (domain.ChannelMembershipChange, error) {
	if s == nil || s.pool == nil {
		return domain.ChannelMembershipChange{}, domain.ErrUnavailable
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.ChannelMembershipChange{}, fmt.Errorf("begin remove channel member: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var workspaceID string
	var isGeneral bool
	if err := tx.QueryRow(ctx, lockChannelQuery, channelID).Scan(&workspaceID, &isGeneral); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ChannelMembershipChange{}, domain.ErrNotFound
		}
		return domain.ChannelMembershipChange{}, fmt.Errorf("lock channel for membership: %w", err)
	}
	// An archived channel still allows a removal, matching chat-service's
	// RemoveChannelMember: taking somebody out of a channel nobody uses is not
	// an operation that needs the channel to be live.
	if isGeneral {
		return domain.ChannelMembershipChange{}, domain.ErrForbidden
	}

	tag, err := tx.Exec(ctx,
		`DELETE FROM chat.channel_members WHERE channel_id = $1::uuid AND user_id = $2::uuid`,
		channelID, userID,
	)
	if err != nil {
		return domain.ChannelMembershipChange{}, fmt.Errorf("remove channel member: %w", err)
	}
	change := domain.ChannelMembershipChange{
		ChannelID:   channelID,
		WorkspaceID: workspaceID,
		Removed:     tag.RowsAffected() > 0,
	}
	if err := tx.QueryRow(ctx, countChannelMembersQuery, channelID).Scan(&change.MemberCount); err != nil {
		return domain.ChannelMembershipChange{}, fmt.Errorf("count channel members: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ChannelMembershipChange{}, fmt.Errorf("commit remove channel member: %w", err)
	}
	return change, nil
}

// listMemberCandidatesQuery offers the people who could be added to a channel.
//
// The predicate is the same set of conditions channelmembership.EligibleTargetsCTE
// enforces — active workspace membership, active workspace, active channel,
// active non-deleted account — expressed as a search rather than a lookup
// against a candidate array. The two shapes cannot be one string: this one
// takes a search term and returns people, that one takes a list of ids and
// returns the subset that may be written.
//
// The split is safe because they are not peers. This query decides only what to
// *offer*; the add decides what to *admit*, and it does so with the shared rule,
// in the same statement that writes. A candidate this query wrongly listed is
// still refused there. TestMemberCandidateQuery_MirrorsTheSharedEligibilityRule
// pins that the conditions have not drifted apart.
//
// NOT EXISTS rather than a follow-up call per candidate: "is this person
// already in the channel" is answered by the same scan, so the picker costs one
// query however many people it shows. Same shape as chat-service's
// SearchChannelMemberCandidates.
const listMemberCandidatesQuery = `
	SELECT u.id::text,
	       u.display_name,
	       COALESCE(u.full_name, ''),
	       u.email::text,
	       COALESCE(u.avatar_url, ''),
	       wm.role
	FROM chat.workspace_members AS wm
	JOIN chat.workspaces AS w
	  ON w.id = wm.workspace_id AND w.status = 'active'
	JOIN chat.channels AS c
	  ON c.id = $1::uuid
	 AND c.workspace_id = wm.workspace_id
	 AND c.status = 'active'
	JOIN auth.users AS u
	  ON u.id = wm.user_id AND u.status = 'active' AND u.deleted_at IS NULL
	WHERE wm.status = 'active'
	  AND NOT EXISTS (
	          SELECT 1 FROM chat.channel_members AS cm
	          WHERE cm.channel_id = c.id AND cm.user_id = wm.user_id
	      )
	  AND ($2::text IS NULL
	       OR u.display_name ILIKE $2 ` + likeEscapeClause + `
	       OR COALESCE(u.full_name, '') ILIKE $2 ` + likeEscapeClause + `
	       OR u.email::text ILIKE $2 ` + likeEscapeClause + `)
	ORDER BY u.display_name, u.id
	LIMIT $3`

// ListMemberCandidates searches the people who may be added to one channel.
//
// The workspace is never a parameter: it comes from the channel, inside the
// query, so no request can aim the search at another tenant's directory. That
// is the whole reason this is a channel-scoped endpoint rather than a filter on
// the platform user directory.
//
// An unknown or archived channel simply matches nobody. It is a search, and a
// search that finds nothing is an empty list rather than an error — the console
// says "no candidates", which is true either way, and the add endpoint is where
// a bad channel id becomes a 404.
func (s *PGXChannelDirectoryStore) ListMemberCandidates(ctx context.Context, channelID, query string, limit int) ([]domain.ChannelMemberCandidate, error) {
	if s == nil || s.pool == nil {
		return nil, domain.ErrUnavailable
	}
	rows, err := s.pool.Query(ctx, listMemberCandidatesQuery,
		channelID, nullableText(likePattern(query)), limit)
	if err != nil {
		return nil, fmt.Errorf("list member candidates: %w", err)
	}
	defer rows.Close()

	candidates := make([]domain.ChannelMemberCandidate, 0, limit)
	for rows.Next() {
		var candidate domain.ChannelMemberCandidate
		if err := rows.Scan(&candidate.UserID, &candidate.DisplayName, &candidate.FullName,
			&candidate.Email, &candidate.AvatarURL, &candidate.WorkspaceRole); err != nil {
			return nil, fmt.Errorf("scan member candidate: %w", err)
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read member candidates: %w", err)
	}
	return candidates, nil
}
