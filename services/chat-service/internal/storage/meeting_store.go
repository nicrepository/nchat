package storage

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// PGXMeetingStore persists meetings (issue #1052). Every operation reads the
// meeting inside the caller's workspace, resolves the actor's facts in the
// same transaction and lets domain.AuthorizeMeeting decide before anything is
// written. A meeting in another workspace and one the actor may not see are
// the same ErrNotFound.
//
// A write holds the facts its decision rests on until it commits (see
// lockMeetingActors), so a concurrent revocation is either seen or waits. A read
// does not: what it returns was visible to an authorized actor when it read it.
type PGXMeetingStore struct {
	pool Pool
}

func NewPGXMeetingStore(pool Pool) *PGXMeetingStore {
	return &PGXMeetingStore{pool: pool}
}

// MeetingScope names a meeting as one actor sees it. WorkspaceID is the
// session's workspace and ActorID the authenticated user, never request fields.
type MeetingScope struct {
	WorkspaceID string
	ActorID     string
	MeetingID   string
}

// meetingLock is how an operation holds the meeting row and whether it holds
// its actor's authorization facts too. Every change to the meeting or its
// roster takes the row FOR UPDATE, so FOR SHARE is enough for a read to see the
// meeting and its roster as one state, and for an RSVP to know neither changes
// underneath it.
type meetingLock struct {
	row       string
	authority bool
}

var (
	meetingRead  = meetingLock{row: " FOR SHARE"}
	meetingRSVP  = meetingLock{row: " FOR SHARE", authority: true}
	meetingWrite = meetingLock{row: " FOR UPDATE", authority: true}
)

const meetingColumns = `id::text, workspace_id::text, channel_id::text, dm_conversation_id::text,
 organizer_user_id::text, title, description, starts_at, ends_at, timezone, status, join_policy,
 room_key, version, created_at, updated_at`

type meetingTx struct {
	tx      pgx.Tx
	meeting domain.Meeting
	actor   domain.MeetingActor
	target  domain.MeetingActor // only for a participant change
}

// CreateMeeting stores m, built by domain.NewMeeting, with its organizer's
// accepted participant row, atomically.
func (s *PGXMeetingStore) CreateMeeting(ctx context.Context, m domain.Meeting) (domain.Meeting, error) {
	result, err := s.createMeeting(ctx, MeetingCreation{Meeting: m})
	return result.Meeting, err
}

// insertMeeting persists a new meeting and its accepted organizer on tx.
func insertMeeting(ctx context.Context, tx pgx.Tx, m domain.Meeting) (domain.Meeting, error) {
	channelID, dmID := meetingConversationColumns(m.Conversation)
	err := tx.QueryRow(ctx, `INSERT INTO chat.meetings (id, workspace_id, channel_id, dm_conversation_id,
 organizer_user_id, title, description, starts_at, ends_at, timezone, status, join_policy, room_key, version)
 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
 RETURNING created_at, updated_at`,
		m.ID, m.WorkspaceID, channelID, dmID, m.OrganizerUserID, m.Title, m.Description,
		m.StartsAt, m.EndsAt, m.Timezone, m.Status, m.JoinPolicy, m.RoomKey, m.Version,
	).Scan(&m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return domain.Meeting{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO chat.meeting_participants (meeting_id, user_id, role, rsvp, invited_at, updated_at)
 VALUES ($1, $2, $3, $4, $5, $5)`, m.ID, m.OrganizerUserID, domain.MeetingRoleOrganizer, domain.MeetingRSVPAccepted, m.CreatedAt); err != nil {
		return domain.Meeting{}, err
	}
	m.CreatedAt, m.UpdatedAt = m.CreatedAt.UTC(), m.UpdatedAt.UTC()
	return m, nil
}

// GetMeeting returns a meeting the actor may see, with its whole roster. The
// roster is not paginated; #1054 owns rosters the size of a large channel.
func (s *PGXMeetingStore) GetMeeting(ctx context.Context, scope MeetingScope) (domain.Meeting, []domain.MeetingParticipant, error) {
	var meeting domain.Meeting
	var participants []domain.MeetingParticipant
	err := s.withMeeting(ctx, scope, meetingRead, "", func(st *meetingTx) error {
		if err := domain.AuthorizeMeeting(st.meeting, st.actor, domain.MeetingCapabilityView); err != nil {
			return err
		}
		meeting = st.meeting
		var err error
		participants, err = listMeetingParticipants(ctx, st.tx, st.meeting.ID)
		return err
	})
	return meeting, participants, err
}

// AuthorizeMeetingJoin returns the meeting when the actor may join it now. It
// decides eligibility only; it creates no room and issues no media token.
func (s *PGXMeetingStore) AuthorizeMeetingJoin(ctx context.Context, scope MeetingScope) (domain.Meeting, error) {
	var meeting domain.Meeting
	err := s.withMeeting(ctx, scope, meetingRead, "", func(st *meetingTx) error {
		meeting = st.meeting
		return domain.AuthorizeMeeting(st.meeting, st.actor, domain.MeetingCapabilityJoin)
	})
	if err != nil {
		return domain.Meeting{}, err
	}
	return meeting, nil
}

// UpdateMeeting replaces the meeting's details when expectedVersion is current.
func (s *PGXMeetingStore) UpdateMeeting(ctx context.Context, scope MeetingScope, expectedVersion int64, details domain.MeetingDetails) (domain.Meeting, error) {
	return s.PatchMeeting(ctx, scope, expectedVersion, MeetingPatch{
		Title: &details.Title, Description: &details.Description, StartsAt: &details.StartsAt,
		EndsAt: &details.EndsAt, Timezone: &details.Timezone, JoinPolicy: &details.JoinPolicy,
	})
}

// TransitionMeeting moves the meeting to next. Repeating the transition that
// already happened returns the meeting unchanged.
func (s *PGXMeetingStore) TransitionMeeting(ctx context.Context, scope MeetingScope, expectedVersion int64, next domain.MeetingStatus) (domain.Meeting, error) {
	var meeting domain.Meeting
	err := s.withMeeting(ctx, scope, meetingWrite, "", func(st *meetingTx) error {
		if err := domain.AuthorizeMeeting(st.meeting, st.actor, domain.MeetingTransitionCapability(next)); err != nil {
			return err
		}
		moved, changed, err := st.meeting.Transition(next, expectedVersion)
		if err != nil {
			return err
		}
		if !changed {
			meeting = moved
			return nil
		}
		meeting, err = writeMeeting(ctx, st.tx, moved)
		return err
	})
	return meeting, err
}

// AddMeetingParticipant invites userID as cohost or attendee, with a pending RSVP.
func (s *PGXMeetingStore) AddMeetingParticipant(ctx context.Context, scope MeetingScope, userID string, role domain.MeetingRole) (domain.MeetingParticipant, error) {
	if role == "" {
		return domain.MeetingParticipant{}, domain.ErrInvalidInput
	}
	var participant domain.MeetingParticipant
	err := s.withParticipant(ctx, scope, userID, func(st *meetingTx, target domain.MeetingActor) error {
		if err := domain.AuthorizeMeetingParticipantChange(st.meeting, st.actor, target.Participant, role); err != nil {
			return err
		}
		if target.Participant != nil {
			return domain.ErrMeetingParticipantExists
		}
		if !domain.MeetingInviteeEligible(st.meeting, target) {
			return domain.ErrMeetingParticipantNotEligible
		}
		var err error
		participant, err = scanMeetingParticipant(st.tx.QueryRow(ctx, `INSERT INTO chat.meeting_participants (meeting_id, user_id, role)
 VALUES ($1, $2, $3) RETURNING meeting_id::text, user_id::text, role, rsvp, invited_at, updated_at`,
			st.meeting.ID, target.UserID, role))
		return err
	})
	return participant, err
}

// ChangeMeetingParticipantRole sets an existing participant's role.
func (s *PGXMeetingStore) ChangeMeetingParticipantRole(ctx context.Context, scope MeetingScope, userID string, role domain.MeetingRole) (domain.MeetingParticipant, error) {
	if role == "" {
		return domain.MeetingParticipant{}, domain.ErrInvalidInput
	}
	var participant domain.MeetingParticipant
	err := s.withParticipant(ctx, scope, userID, func(st *meetingTx, target domain.MeetingActor) error {
		if err := domain.AuthorizeMeetingParticipantChange(st.meeting, st.actor, target.Participant, role); err != nil {
			return err
		}
		if target.Participant == nil {
			return domain.ErrNotFound
		}
		if target.Participant.Role == role {
			participant = *target.Participant
			return nil
		}
		var err error
		participant, err = scanMeetingParticipant(st.tx.QueryRow(ctx, `UPDATE chat.meeting_participants
 SET role = $3, updated_at = now() WHERE meeting_id = $1 AND user_id = $2
 RETURNING meeting_id::text, user_id::text, role, rsvp, invited_at, updated_at`,
			st.meeting.ID, target.UserID, role))
		return err
	})
	return participant, err
}

// RemoveMeetingParticipant removes a participant other than the organizer.
func (s *PGXMeetingStore) RemoveMeetingParticipant(ctx context.Context, scope MeetingScope, userID string) error {
	return s.withParticipant(ctx, scope, userID, func(st *meetingTx, target domain.MeetingActor) error {
		if err := domain.AuthorizeMeetingParticipantChange(st.meeting, st.actor, target.Participant, ""); err != nil {
			return err
		}
		if target.Participant == nil {
			return domain.ErrNotFound
		}
		_, err := st.tx.Exec(ctx, `DELETE FROM chat.meeting_participants WHERE meeting_id = $1 AND user_id = $2`,
			st.meeting.ID, target.UserID)
		return err
	})
}

// SetMeetingRSVP records the actor's own answer; there is no way to name
// another participant.
func (s *PGXMeetingStore) SetMeetingRSVP(ctx context.Context, scope MeetingScope, rsvp domain.MeetingRSVP) (domain.MeetingParticipant, error) {
	if !rsvp.Valid() {
		return domain.MeetingParticipant{}, domain.ErrInvalidInput
	}
	var participant domain.MeetingParticipant
	err := s.withMeeting(ctx, scope, meetingRSVP, "", func(st *meetingTx) error {
		if err := domain.AuthorizeMeeting(st.meeting, st.actor, domain.MeetingCapabilityRSVP); err != nil {
			return err
		}
		var err error
		participant, err = scanMeetingParticipant(st.tx.QueryRow(ctx, `UPDATE chat.meeting_participants
 SET rsvp = $3, updated_at = now() WHERE meeting_id = $1 AND user_id = $2
 RETURNING meeting_id::text, user_id::text, role, rsvp, invited_at, updated_at`,
			st.meeting.ID, st.actor.UserID, rsvp))
		return err
	})
	return participant, err
}

func (s *PGXMeetingStore) withParticipant(ctx context.Context, scope MeetingScope, userID string, fn func(*meetingTx, domain.MeetingActor) error) error {
	target, err := uuid.Parse(userID)
	if err != nil {
		return domain.ErrInvalidInput
	}
	return s.withMeeting(ctx, scope, meetingWrite, target.String(), func(st *meetingTx) error {
		return fn(st, st.target)
	})
}

// withMeeting runs fn in a transaction holding the scoped meeting and the facts
// of its actor and of target (unless empty), and commits only when fn succeeds.
func (s *PGXMeetingStore) withMeeting(ctx context.Context, scope MeetingScope, lock meetingLock, target string, fn func(*meetingTx) error) error {
	scope, err := scope.canonical()
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	meeting, err := scanMeeting(tx.QueryRow(ctx, `SELECT `+meetingColumns+`
 FROM chat.meetings WHERE id = $1 AND workspace_id = $2`+lock.row, scope.MeetingID, scope.WorkspaceID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	if err != nil {
		return err
	}
	st := &meetingTx{tx: tx, meeting: meeting}
	if lock.authority {
		st.actor, st.target, err = lockMeetingActors(ctx, tx, meeting, scope.ActorID, target)
	} else {
		st.actor, err = loadMeetingActor(ctx, tx, meeting, scope.ActorID)
	}
	if err != nil {
		return err
	}
	if err = fn(st); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (sc MeetingScope) canonical() (MeetingScope, error) {
	workspace, errWorkspace := uuid.Parse(sc.WorkspaceID)
	actor, errActor := uuid.Parse(sc.ActorID)
	meeting, errMeeting := uuid.Parse(sc.MeetingID)
	if errors.Join(errWorkspace, errActor, errMeeting) != nil || workspace == uuid.Nil || actor == uuid.Nil || meeting == uuid.Nil {
		return MeetingScope{}, domain.ErrInvalidInput
	}
	return MeetingScope{WorkspaceID: workspace.String(), ActorID: actor.String(), MeetingID: meeting.String()}, nil
}

// meetingAuthority is what the statements that locked a meeting's
// authorization facts returned, and nothing else: a row that appears after its
// statement ran was not locked, so it is not seen and grants nothing.
type meetingAuthority struct {
	meeting         domain.Meeting
	workspaceActive bool
	conversationOK  bool // active and in the meeting's workspace
	public          bool
	users           map[string]*meetingFacts
}

type meetingFacts struct {
	member      *domain.WorkspaceMember
	account     bool // active and undeleted
	listed      bool // a channel member, or an active DM member
	participant *domain.MeetingParticipant
}

// lockMeetingActors holds the authorization facts of actorID and, when it is
// not empty, targetID relative to m FOR SHARE, and decides from the rows those
// same statements returned. Revoking a fact is an UPDATE or DELETE of a locked
// row, which conflicts with FOR SHARE (FOR KEY SHARE would let a status or role
// change through): a revocation that commits first is the row version the lock
// returns, and a later one waits for this commit. Concurrent meeting writes
// only share the locks.
//
// The order is the one migration 000061 sets for every writer that also takes
// account locks, account invalidation among them: the conversation before
// workspace memberships and accounts, then the users' places in the
// conversation. The workspace comes first, as in a workspace update, whose
// commit locks conversations. Within a table the users are locked in id
// order, not actor first, so two operations whose actor and target are
// swapped take them in the same order.
func lockMeetingActors(ctx context.Context, tx pgx.Tx, m domain.Meeting, actorID, targetID string) (actor, target domain.MeetingActor, err error) {
	ids := []string{actorID}
	if targetID != "" {
		ids = append(ids, targetID)
	}
	a, err := lockMeetingAuthority(ctx, tx, m, ids)
	if err != nil {
		return domain.MeetingActor{}, domain.MeetingActor{}, err
	}
	return a.actor(actorID), a.actor(targetID), nil
}

// lockMeetingAuthority locks all users together, table by table in canonical
// order. Repeating actor-first locks per invitee would invert that order.
func lockMeetingAuthority(ctx context.Context, tx pgx.Tx, m domain.Meeting, ids []string) (*meetingAuthority, error) {
	a := meetingAuthority{meeting: m, users: map[string]*meetingFacts{}}
	batch := &pgx.Batch{}
	batch.Queue(`SELECT status = 'active' FROM chat.workspaces WHERE id = $1 FOR SHARE`, m.WorkspaceID).
		QueryRow(optionalRow(&a.workspaceActive))
	a.queueConversation(batch)
	batch.Queue(`SELECT user_id::text, role, status FROM chat.workspace_members
 WHERE workspace_id = $1 AND user_id = ANY($2::uuid[]) ORDER BY user_id FOR SHARE`, m.WorkspaceID, ids).Query(a.scanMembers)
	batch.Queue(`SELECT id::text, status = 'active' AND deleted_at IS NULL FROM auth.users
 WHERE id = ANY($1::uuid[]) ORDER BY id FOR SHARE`, ids).Query(a.scanAccounts)
	a.queuePlaces(batch, ids)
	batch.Queue(`SELECT meeting_id::text, user_id::text, role, rsvp, invited_at, updated_at
 FROM chat.meeting_participants WHERE meeting_id = $1 AND user_id = ANY($2::uuid[])`, m.ID, ids).Query(a.scanParticipants)
	if err := tx.SendBatch(ctx, batch).Close(); err != nil {
		return nil, err
	}
	return &a, nil
}

func (a *meetingAuthority) queueConversation(batch *pgx.Batch) {
	c := a.meeting.Conversation
	switch {
	case c == nil:
	case c.Kind == domain.MeetingConversationChannel:
		batch.Queue(`SELECT workspace_id = $2 AND status = 'active', type = 'public' FROM chat.channels
 WHERE id = $1 FOR SHARE`, c.ID, a.meeting.WorkspaceID).QueryRow(optionalRow(&a.conversationOK, &a.public))
	default:
		batch.Queue(`SELECT workspace_id = $2 AND status = 'active' FROM chat.dm_conversations
 WHERE id = $1 FOR SHARE`, c.ID, a.meeting.WorkspaceID).QueryRow(optionalRow(&a.conversationOK))
	}
}

func (a *meetingAuthority) queuePlaces(batch *pgx.Batch, ids []string) {
	c := a.meeting.Conversation
	switch {
	case c == nil:
	case c.Kind == domain.MeetingConversationChannel:
		batch.Queue(`SELECT user_id::text, true FROM chat.channel_members
 WHERE channel_id = $1 AND user_id = ANY($2::uuid[]) ORDER BY user_id FOR SHARE`, c.ID, ids).Query(a.scanPlaces)
	default:
		batch.Queue(`SELECT user_id::text, status = 'active' FROM chat.dm_members
 WHERE conversation_id = $1 AND user_id = ANY($2::uuid[]) ORDER BY user_id FOR SHARE`, c.ID, ids).Query(a.scanPlaces)
	}
}

// optionalRow scans a row that may not exist; a missing row leaves dest false.
func optionalRow(dest ...any) func(pgx.Row) error {
	return func(row pgx.Row) error {
		if err := row.Scan(dest...); !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		return nil
	}
}

func (a *meetingAuthority) scanMembers(rows pgx.Rows) error {
	var id, role, status string
	_, err := pgx.ForEachRow(rows, []any{&id, &role, &status}, func() error {
		a.user(id).member = &domain.WorkspaceMember{WorkspaceID: a.meeting.WorkspaceID, UserID: id,
			Role: domain.WorkspaceRole(role), Status: domain.MemberStatus(status)}
		return nil
	})
	return err
}

func (a *meetingAuthority) scanAccounts(rows pgx.Rows) error {
	var id string
	var active bool
	_, err := pgx.ForEachRow(rows, []any{&id, &active}, func() error {
		a.user(id).account = active
		return nil
	})
	return err
}

func (a *meetingAuthority) scanPlaces(rows pgx.Rows) error {
	var id string
	var listed bool
	_, err := pgx.ForEachRow(rows, []any{&id, &listed}, func() error {
		a.user(id).listed = listed
		return nil
	})
	return err
}

func (a *meetingAuthority) scanParticipants(rows pgx.Rows) error {
	for rows.Next() {
		p, err := scanMeetingParticipant(rows)
		if err != nil {
			return err
		}
		a.user(p.UserID).participant = &p
	}
	return rows.Err()
}

func (a *meetingAuthority) user(id string) *meetingFacts {
	f, ok := a.users[id]
	if !ok {
		f = &meetingFacts{}
		a.users[id] = f
	}
	return f
}

// actor builds what loadMeetingActor reads in one statement, from the locked
// rows instead.
func (a *meetingAuthority) actor(id string) domain.MeetingActor {
	f := a.user(id)
	actor := domain.MeetingActor{UserID: id, Participant: f.participant}
	if f.member != nil && a.workspaceActive && f.account {
		actor.Membership = f.member
	}
	actor.ConversationAccess = a.conversationOK && a.reaches(f)
	return actor
}

// reaches is chat.channel_visible_to_user for a channel, evaluated on the
// locked rows: an explicit membership, or a non-guest active member in a
// public channel. A DM takes an active DM membership.
func (a *meetingAuthority) reaches(f *meetingFacts) bool {
	if a.meeting.Conversation.Kind != domain.MeetingConversationChannel {
		return f.listed
	}
	return f.member != nil && f.member.Status == domain.MemberStatusActive &&
		(f.listed || a.public && f.member.Role != domain.WorkspaceRoleGuest)
}

// loadMeetingActor reads userID's facts relative to m in one statement:
// eligible membership (active workspace, member and account), roster row, and
// current access to m's conversation, scoped to m's workspace.
func loadMeetingActor(ctx context.Context, tx pgx.Tx, m domain.Meeting, userID string) (domain.MeetingActor, error) {
	var conversationKind, conversationID *string
	if m.Conversation != nil {
		kind := string(m.Conversation.Kind)
		conversationKind, conversationID = &kind, &m.Conversation.ID
	}
	var memberRole, memberStatus, role, rsvp *string
	var invitedAt, updatedAt *time.Time
	actor := domain.MeetingActor{UserID: userID}
	err := tx.QueryRow(ctx, `SELECT wm.role, wm.status, p.role, p.rsvp, p.invited_at, p.updated_at,
 CASE $4::text
  WHEN 'channel' THEN EXISTS (SELECT 1 FROM chat.channels c
   WHERE c.id = $5::uuid AND c.workspace_id = $1::uuid AND c.status = 'active'
     AND chat.channel_visible_to_user(c.id, $2::uuid))
  WHEN 'dm' THEN EXISTS (SELECT 1 FROM chat.dm_conversations d
   JOIN chat.dm_members dm ON dm.conversation_id = d.id AND dm.user_id = $2::uuid AND dm.status = 'active'
   WHERE d.id = $5::uuid AND d.workspace_id = $1::uuid AND d.status = 'active')
  ELSE false
 END
 FROM (SELECT 1) one
 LEFT JOIN (chat.workspace_members wm
  JOIN chat.workspaces w ON w.id = wm.workspace_id AND w.status = 'active'
  JOIN auth.users u ON u.id = wm.user_id AND u.status = 'active' AND u.deleted_at IS NULL)
  ON wm.workspace_id = $1::uuid AND wm.user_id = $2::uuid
 LEFT JOIN chat.meeting_participants p ON p.meeting_id = $3::uuid AND p.user_id = $2::uuid`,
		m.WorkspaceID, userID, m.ID, conversationKind, conversationID,
	).Scan(&memberRole, &memberStatus, &role, &rsvp, &invitedAt, &updatedAt, &actor.ConversationAccess)
	if err != nil {
		return domain.MeetingActor{}, err
	}
	if memberRole != nil {
		actor.Membership = &domain.WorkspaceMember{WorkspaceID: m.WorkspaceID, UserID: userID,
			Role: domain.WorkspaceRole(*memberRole), Status: domain.MemberStatus(*memberStatus)}
	}
	if role != nil {
		actor.Participant = &domain.MeetingParticipant{MeetingID: m.ID, UserID: userID,
			Role: domain.MeetingRole(*role), RSVP: domain.MeetingRSVP(*rsvp),
			InvitedAt: invitedAt.UTC(), UpdatedAt: updatedAt.UTC()}
	}
	return actor, nil
}

// writeMeeting persists m's details and status, advancing the version. The
// version predicate repeats the locked read's check as the write's own guard.
func writeMeeting(ctx context.Context, tx pgx.Tx, m domain.Meeting) (domain.Meeting, error) {
	err := tx.QueryRow(ctx, `UPDATE chat.meetings SET title = $3, description = $4, starts_at = $5,
 ends_at = $6, timezone = $7, join_policy = $8, status = $9, version = version + 1, updated_at = now()
 WHERE id = $1 AND workspace_id = $2 AND version = $10
 RETURNING version, updated_at`,
		m.ID, m.WorkspaceID, m.Title, m.Description, m.StartsAt, m.EndsAt, m.Timezone, m.JoinPolicy,
		m.Status, m.Version,
	).Scan(&m.Version, &m.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Meeting{}, domain.ErrMeetingStaleVersion
	}
	if err != nil {
		return domain.Meeting{}, err
	}
	m.UpdatedAt = m.UpdatedAt.UTC()
	return m, nil
}

func listMeetingParticipants(ctx context.Context, tx pgx.Tx, meetingID string) ([]domain.MeetingParticipant, error) {
	rows, err := tx.Query(ctx, `SELECT meeting_id::text, user_id::text, role, rsvp, invited_at, updated_at
 FROM chat.meeting_participants WHERE meeting_id = $1 ORDER BY invited_at, user_id`, meetingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var participants []domain.MeetingParticipant
	for rows.Next() {
		participant, err := scanMeetingParticipant(rows)
		if err != nil {
			return nil, err
		}
		participants = append(participants, participant)
	}
	return participants, rows.Err()
}

// scanMeeting returns every instant in UTC: pgx decodes timestamptz into the
// process's local zone, which must not leak into the domain.
func scanMeeting(row pgx.Row) (domain.Meeting, error) {
	var m domain.Meeting
	var channelID, dmID *string
	err := row.Scan(&m.ID, &m.WorkspaceID, &channelID, &dmID, &m.OrganizerUserID, &m.Title, &m.Description,
		&m.StartsAt, &m.EndsAt, &m.Timezone, &m.Status, &m.JoinPolicy, &m.RoomKey, &m.Version,
		&m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return domain.Meeting{}, err
	}
	switch {
	case channelID != nil:
		m.Conversation = &domain.MeetingConversation{Kind: domain.MeetingConversationChannel, ID: *channelID}
	case dmID != nil:
		m.Conversation = &domain.MeetingConversation{Kind: domain.MeetingConversationDM, ID: *dmID}
	}
	m.StartsAt, m.EndsAt = m.StartsAt.UTC(), m.EndsAt.UTC()
	m.CreatedAt, m.UpdatedAt = m.CreatedAt.UTC(), m.UpdatedAt.UTC()
	return m, nil
}

func scanMeetingParticipant(row pgx.Row) (domain.MeetingParticipant, error) {
	var p domain.MeetingParticipant
	if err := row.Scan(&p.MeetingID, &p.UserID, &p.Role, &p.RSVP, &p.InvitedAt, &p.UpdatedAt); err != nil {
		return domain.MeetingParticipant{}, err
	}
	p.InvitedAt, p.UpdatedAt = p.InvitedAt.UTC(), p.UpdatedAt.UTC()
	return p, nil
}

func meetingConversationColumns(c *domain.MeetingConversation) (channelID, dmID *string) {
	if c == nil {
		return nil, nil
	}
	if c.Kind == domain.MeetingConversationChannel {
		return &c.ID, nil
	}
	return nil, &c.ID
}
