package storage

import (
	"context"
	"fmt"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// Target kinds for a notification preference. The same two strings the sidebar
// pin store uses, declared separately so neither store depends on the other's
// vocabulary.
const (
	NotificationPrefTargetChannel = "channel"
	NotificationPrefTargetDM      = "dm"
)

// The notification levels chat.conversation_notification_prefs accepts, matching
// the CHECK constraint migration 000050 added (issue #136).
//
// Restated here rather than imported from libs/go/platform/notificationpolicy,
// for the same reason the target kinds above are restated: these are the values
// this table stores, and the policy engine's identical set is the values that
// engine decides with. They agree today and a test proves it; neither package
// owning the other's vocabulary is what keeps that agreement visible rather
// than accidental.
const (
	// NotificationLevelAll is every message. It is the product default, and the
	// state the absence of a row means.
	NotificationLevelAll = "all"
	// NotificationLevelMentionsReplies is mentions and replies only.
	NotificationLevelMentionsReplies = "mentions_replies"
)

// ValidNotificationLevel reports whether level is one this table can hold.
//
// The database refuses anything else through the CHECK constraint, so this is
// the early half of one closed set: it keeps an invalid value from reaching a
// statement at all, and is never the only thing standing between a caller and
// the column.
func ValidNotificationLevel(level string) bool {
	return level == NotificationLevelAll || level == NotificationLevelMentionsReplies
}

// ConversationNotificationPref is one preference this user actually expressed
// about one conversation.
//
// Two independent fields, exactly like the two columns behind them: Level says
// which events are worth an alert, Muted says whether alerts are silenced right
// now. Deriving one presentable state from the pair is precedence — muted wins —
// and it belongs to whoever presents, not here.
type ConversationNotificationPref struct {
	TargetType string
	TargetID   string
	Level      string
	Muted      bool
}

// UserConversationNotificationPref is the same preference read the other way
// round: one conversation is fixed and the user varies. See PreferencesForUsers.
type UserConversationNotificationPref struct {
	UserID string
	Level  string
	Muted  bool
}

// NotificationPrefStore persists the caller's private notification preference
// for one conversation.
//
// # Two dimensions and three operations
//
// The table holds a level and a mute (issue #136), and the operations are
// written so that neither can silently overwrite the other:
//
//	Mute      sets the mute, leaves the level exactly as it was
//	Unmute    clears the mute, leaves the level exactly as it was
//	SetLevel  sets the level and clears the mute
//
// That is what makes the sidebar's quick "silence this" shortcut
// non-destructive: somebody who chose mentions-and-replies in their profile,
// silenced the conversation from the row menu and then turned notifications
// back on is returned to mentions-and-replies, because nothing ever wrote over
// it.
//
// # Authorization
//
// The write statements repeat the current visibility predicate, exactly like
// SidebarPinStore's do, so a stale client-side list cannot turn a revoked
// membership into a preference row — and, for a channel *mute*, they also
// repeat the general-channel invariant, so that structural refusal holds in SQL
// and not only in the service.
//
// The refusal is on the mute specifically. #geral may carry a level: being
// reachable by name in the one channel nobody may leave is what the invariant
// protects, and mentions-and-replies keeps precisely that. Silence does not, so
// silence stays refused.
type NotificationPrefStore interface {
	// Mute silences targetID for userID and preserves the notification level.
	// Idempotent: muting twice is one row, and the second call does not move the
	// instant the mute began.
	Mute(ctx context.Context, workspaceID, userID, targetType, targetID string) error
	// Unmute restores notifications and preserves the notification level.
	// Idempotent, and deliberately unguarded by visibility: a user must always
	// be able to undo their own preference, even for a conversation they can no
	// longer see.
	Unmute(ctx context.Context, userID, targetType, targetID string) error
	// SetLevel records which events of this conversation the user wants alerts
	// for, and clears any mute — choosing a level is choosing to hear something.
	//
	// The default level with no mute is the absence of a row, so setting
	// NotificationLevelAll removes the preference rather than writing one. That
	// path needs no visibility check for the same reason Unmute needs none: it
	// can only delete the caller's own row, and can create nothing.
	SetLevel(ctx context.Context, workspaceID, userID, targetType, targetID, level string) error
	// ListPreferences returns every preference of this (workspace, user) that
	// still points at something they can see.
	ListPreferences(ctx context.Context, workspaceID, userID string) ([]ConversationNotificationPref, error)
	// PreferencesForUsers returns, of the users given, those with a preference
	// row for this one target. It is ListPreferences asked the other way round,
	// for the realtime fan-out: there the target is fixed and the recipients
	// vary, so asking per user would be one query per subscriber of every
	// message.
	//
	// No visibility predicate, and that is the same reasoning the notification
	// worker's projection is written on: the write path already established
	// membership, and re-checking it on read would let a revoked membership
	// *undo* a preference — the direction that alerts somebody who asked not to
	// be. The caller has separately authorised every user it passes in.
	PreferencesForUsers(
		ctx context.Context, workspaceID, targetType, targetID string, userIDs []string,
	) ([]UserConversationNotificationPref, error)
}

type PGXNotificationPrefStore struct{ pool Pool }

func NewPGXNotificationPrefStore(pool Pool) *PGXNotificationPrefStore {
	return &PGXNotificationPrefStore{pool: pool}
}

// authorizedChannelHead, generalChannelGuard and authorizedChannelTail compose
// the CTE that admits the channels this user may write a preference for.
//
// Split into three parts rather than written out twice, because the mute and
// the level differ in exactly one condition and nothing else. Sharing the rest
// is what keeps the workspace, the membership, the status and the visibility
// predicate from drifting apart between two statements that must authorise
// identically.
//
// The identity test for the general channel is the column, never the display
// name: a channel called "Geral" that is not the general one is ordinary, and
// the general one renamed to anything else is still structural.
const authorizedChannelHead = `
	WITH authorized AS (
		SELECT c.id, c.workspace_id
		FROM chat.channels c
		JOIN chat.workspaces w ON w.id = c.workspace_id AND w.status = 'active'
		JOIN chat.workspace_members wm
		  ON wm.workspace_id = c.workspace_id AND wm.user_id = $2 AND wm.status = 'active'
		WHERE c.id = $3 AND c.workspace_id = $1 AND c.status = 'active'`

const generalChannelGuard = `
		  AND c.is_general = false`

const authorizedChannelTail = `
		  AND chat.channel_visible_to_user(c.id, $2::uuid)
	)`

// authorizedDMSQL admits a DM or group the user actively participates in. There
// is no general-channel analogue here: a conversation is configurable for
// whoever is in it, and a 1:1 is no different from a group in that respect.
//
// Participation is the whole rule, and it is participation and not a role: no
// branch here grants an admin or a moderator a preference over a conversation
// they are not in.
const authorizedDMSQL = `
	WITH authorized AS (
		SELECT dc.id, dc.workspace_id
		FROM chat.dm_conversations dc
		JOIN chat.workspaces w ON w.id = dc.workspace_id AND w.status = 'active'
		JOIN chat.workspace_members wm
		  ON wm.workspace_id = dc.workspace_id AND wm.user_id = $2 AND wm.status = 'active'
		JOIN chat.dm_members dm
		  ON dm.conversation_id = dc.id AND dm.user_id = $2 AND dm.status = 'active'
		WHERE dc.id = $3 AND dc.workspace_id = $1 AND dc.status = 'active'
	)`

// muteChannelUpsert and muteDMUpsert set the mute and touch nothing else.
//
// notification_level is absent from the column list, so an insert takes the
// column default and a conflict leaves the stored level exactly as it was. That
// omission is the non-destructive guarantee, in SQL.
//
// COALESCE on the conflict path is what makes a repeated mute properly
// idempotent: a conversation already silenced keeps the instant it was silenced
// at, rather than reporting itself as freshly muted every time the endpoint is
// called.
const muteChannelUpsert = `, ins AS (
		INSERT INTO chat.conversation_notification_prefs AS p
			(user_id, workspace_id, channel_id, muted_at)
		SELECT $2, workspace_id, id, now() FROM authorized
		ON CONFLICT (user_id, channel_id) WHERE channel_id IS NOT NULL
		DO UPDATE SET muted_at = COALESCE(p.muted_at, now())
	)
	SELECT EXISTS (SELECT 1 FROM authorized)`

const muteDMUpsert = `, ins AS (
		INSERT INTO chat.conversation_notification_prefs AS p
			(user_id, workspace_id, dm_conversation_id, muted_at)
		SELECT $2, workspace_id, id, now() FROM authorized
		ON CONFLICT (user_id, dm_conversation_id) WHERE dm_conversation_id IS NOT NULL
		DO UPDATE SET muted_at = COALESCE(p.muted_at, now())
	)
	SELECT EXISTS (SELECT 1 FROM authorized)`

// setLevelChannelUpsert and setLevelDMUpsert write the level and clear the mute.
//
// muted_at = NULL is explicit on both paths, because choosing what to be
// alerted about is choosing to be alerted: a profile that selects "mentions and
// replies" on a conversation it had silenced must come back unsilenced, which is
// what the canonical endpoint promises.
const setLevelChannelUpsert = `, ins AS (
		INSERT INTO chat.conversation_notification_prefs AS p
			(user_id, workspace_id, channel_id, notification_level, muted_at)
		SELECT $2, workspace_id, id, $4, NULL FROM authorized
		ON CONFLICT (user_id, channel_id) WHERE channel_id IS NOT NULL
		DO UPDATE SET notification_level = $4, muted_at = NULL
	)
	SELECT EXISTS (SELECT 1 FROM authorized)`

const setLevelDMUpsert = `, ins AS (
		INSERT INTO chat.conversation_notification_prefs AS p
			(user_id, workspace_id, dm_conversation_id, notification_level, muted_at)
		SELECT $2, workspace_id, id, $4, NULL FROM authorized
		ON CONFLICT (user_id, dm_conversation_id) WHERE dm_conversation_id IS NOT NULL
		DO UPDATE SET notification_level = $4, muted_at = NULL
	)
	SELECT EXISTS (SELECT 1 FROM authorized)`

const (
	muteChannelSQL = authorizedChannelHead + generalChannelGuard + authorizedChannelTail + muteChannelUpsert
	muteDMSQL      = authorizedDMSQL + muteDMUpsert
	// No general-channel guard: see NotificationPrefStore's doc comment for why
	// #geral may carry a level but may not be silenced.
	setLevelChannelSQL = authorizedChannelHead + authorizedChannelTail + setLevelChannelUpsert
	setLevelDMSQL      = authorizedDMSQL + setLevelDMUpsert
)

// Mute silences one conversation and leaves the level untouched.
func (s *PGXNotificationPrefStore) Mute(ctx context.Context, workspaceID, userID, targetType, targetID string) error {
	query, err := muteSQLFor(targetType)
	if err != nil {
		return err
	}
	return s.applyAuthorizedWrite(ctx, "mute conversation", query, workspaceID, userID, targetID)
}

// SetLevel records the level, or removes the preference when the level asked
// for is the default one.
func (s *PGXNotificationPrefStore) SetLevel(
	ctx context.Context, workspaceID, userID, targetType, targetID, level string,
) error {
	if !ValidNotificationLevel(level) {
		return fmt.Errorf("%w: unknown notification level %q", domain.ErrInvalidInput, level)
	}
	if level == NotificationLevelAll {
		// The pure default is the absence of a row, so "all, not muted" is
		// expressed by deleting rather than by storing it. Sparse by
		// construction: a conversation nobody expressed anything about costs
		// nothing, which is the principle 000037 established.
		return s.clearPreference(ctx, userID, targetType, targetID)
	}
	query, err := setLevelSQLFor(targetType)
	if err != nil {
		return err
	}
	return s.applyAuthorizedWrite(
		ctx, "set conversation notification level", query, workspaceID, userID, targetID, level,
	)
}

// applyAuthorizedWrite runs one of the authorize-and-write statements above and
// turns "the CTE admitted nothing" into the refusal the endpoints answer with.
//
// Shared by every guarded write, so the authorization *decision* is read in one
// place: a second method reading `allowed` for itself would be a second place
// the refusal could be forgotten.
func (s *PGXNotificationPrefStore) applyAuthorizedWrite(
	ctx context.Context, operation, query, workspaceID, userID, targetID string, extra ...any,
) error {
	args := append([]any{workspaceID, userID, targetID}, extra...)
	var allowed bool
	if err := s.pool.QueryRow(ctx, query, args...).Scan(&allowed); err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	if !allowed {
		// One answer for "no such conversation", "you cannot see it" and, on the
		// mute path, "it is the general channel". The first two must stay
		// indistinguishable so the endpoint cannot be used to probe which IDs
		// exist; the third is folded in because the UI never offers the action
		// for #geral anyway, and naming it here would say more than the caller
		// needs.
		return domain.ErrNotFound
	}
	return nil
}

func muteSQLFor(targetType string) (string, error) {
	switch targetType {
	case NotificationPrefTargetChannel:
		return muteChannelSQL, nil
	case NotificationPrefTargetDM:
		return muteDMSQL, nil
	default:
		return "", domain.ErrInvalidInput
	}
}

func setLevelSQLFor(targetType string) (string, error) {
	switch targetType {
	case NotificationPrefTargetChannel:
		return setLevelChannelSQL, nil
	case NotificationPrefTargetDM:
		return setLevelDMSQL, nil
	default:
		return "", domain.ErrInvalidInput
	}
}

// unmuteChannelSQL and unmuteDMSQL are one statement that expresses both halves
// of unmuting.
//
// # Why one statement and not two
//
// Unmuting has two outcomes depending on what the row says, and an earlier
// version ran them as two round trips — a DELETE for the default level, then an
// UPDATE clearing the mute. Between the two, a concurrent Mute could insert:
//
//  1. the DELETE finds no row
//  2. a concurrent Mute inserts `all` + muted_at = now()
//  3. the UPDATE clears muted_at
//  4. `all` + muted_at IS NULL is persisted
//
// That state is forbidden by the sparse representation: `all` and unsilenced is
// the *absence* of a row, so a row saying it is a row the sidebar reads as a
// preference nobody expressed — and, during a blue/green window, a row a
// release slot from before issue #136 reads as a mute.
//
// # Why this one cannot produce it
//
// The two data-modifying CTEs partition the rows by level: the UPDATE takes
// only `notification_level <> 'all'` and the DELETE only `= 'all'`. They can
// never address the same row, and — this is the part that closes the race —
// there is no predicate under which this statement clears muted_at on an `all`
// row. Whatever a concurrent Mute does, before or after, the outcome is one of
// the serialisations that statement alone could produce:
//
//	Mute last    the row exists with a timestamp: muted
//	Unmute last  the `all` row is gone, or the non-default row has a NULL
//
// Under READ COMMITTED an UPDATE that waits on a concurrent writer re-evaluates
// its predicate against the committed version, so even a row that appeared
// mid-statement is re-tested for `<> 'all'` before anything is written to it.
//
// The database holds the same invariant independently — see migration 000050's
// conversation_notification_prefs_sparse_default_check — so this is the first of
// two defences rather than the only one.
//
// # Authorization
//
// Deliberately unguarded by visibility, exactly as before: a user must always be
// able to undo their own preference, even for a conversation they can no longer
// see, and re-checking membership here would let a revoked membership *keep*
// somebody muted. The authority is `user_id = $1`, which the handler took from
// the session, so the statement can only ever reach the caller's own row.
const unmuteChannelSQL = `
	WITH cleared AS (
		UPDATE chat.conversation_notification_prefs
		SET muted_at = NULL
		WHERE user_id = $1 AND channel_id = $2
		  AND notification_level <> 'all'
		  AND muted_at IS NOT NULL
		RETURNING user_id
	)
	DELETE FROM chat.conversation_notification_prefs
	WHERE user_id = $1 AND channel_id = $2
	  AND notification_level = 'all'`

const unmuteDMSQL = `
	WITH cleared AS (
		UPDATE chat.conversation_notification_prefs
		SET muted_at = NULL
		WHERE user_id = $1 AND dm_conversation_id = $2
		  AND notification_level <> 'all'
		  AND muted_at IS NOT NULL
		RETURNING user_id
	)
	DELETE FROM chat.conversation_notification_prefs
	WHERE user_id = $1 AND dm_conversation_id = $2
	  AND notification_level = 'all'`

// The row-removing statements, one per target kind and written out rather than
// assembled from a column name, so nothing in this file interpolates an
// identifier into SQL.
const (
	clearChannelPrefSQL = `DELETE FROM chat.conversation_notification_prefs
		WHERE user_id = $1 AND channel_id = $2`
	clearDMPrefSQL = `DELETE FROM chat.conversation_notification_prefs
		WHERE user_id = $1 AND dm_conversation_id = $2`
)

// Unmute clears the mute and preserves the level, in one statement.
func (s *PGXNotificationPrefStore) Unmute(ctx context.Context, userID, targetType, targetID string) error {
	var query string
	switch targetType {
	case NotificationPrefTargetChannel:
		query = unmuteChannelSQL
	case NotificationPrefTargetDM:
		query = unmuteDMSQL
	default:
		return domain.ErrInvalidInput
	}
	if _, err := s.pool.Exec(ctx, query, userID, targetID); err != nil {
		return fmt.Errorf("unmute conversation: %w", err)
	}
	return nil
}

// clearPreference removes the row outright, which is how the default level with
// no mute is stored.
func (s *PGXNotificationPrefStore) clearPreference(ctx context.Context, userID, targetType, targetID string) error {
	var query string
	switch targetType {
	case NotificationPrefTargetChannel:
		query = clearChannelPrefSQL
	case NotificationPrefTargetDM:
		query = clearDMPrefSQL
	default:
		return domain.ErrInvalidInput
	}
	if _, err := s.pool.Exec(ctx, query, userID, targetID); err != nil {
		return fmt.Errorf("clear conversation notification preference: %w", err)
	}
	return nil
}

// ListPreferences is the sidebar's one read: every preference this user
// expressed, for both target kinds, in a single statement.
//
// muted is projected as `muted_at IS NOT NULL` rather than as the existence of
// the row, and that is the change issue #136 makes to this read. A row is no
// longer proof of a mute: "mentions and replies, not silenced" is a row with a
// NULL muted_at, and reading its presence as silence would be this migration
// quietly muting people.
func (s *PGXNotificationPrefStore) ListPreferences(
	ctx context.Context, workspaceID, userID string,
) ([]ConversationNotificationPref, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT 'channel', p.channel_id::text, p.notification_level, (p.muted_at IS NOT NULL)
		FROM chat.conversation_notification_prefs p
		JOIN chat.channels c ON c.id = p.channel_id AND c.workspace_id = $1 AND c.status = 'active'
		JOIN chat.workspaces w ON w.id = c.workspace_id AND w.status = 'active'
		JOIN chat.workspace_members wm
		  ON wm.workspace_id = c.workspace_id AND wm.user_id = $2 AND wm.status = 'active'
		WHERE p.user_id = $2 AND p.workspace_id = $1
		  AND chat.channel_visible_to_user(c.id, $2::uuid)
		UNION ALL
		SELECT 'dm', p.dm_conversation_id::text, p.notification_level, (p.muted_at IS NOT NULL)
		FROM chat.conversation_notification_prefs p
		JOIN chat.dm_conversations dc ON dc.id = p.dm_conversation_id AND dc.workspace_id = $1 AND dc.status = 'active'
		JOIN chat.workspaces w ON w.id = dc.workspace_id AND w.status = 'active'
		JOIN chat.workspace_members wm
		  ON wm.workspace_id = dc.workspace_id AND wm.user_id = $2 AND wm.status = 'active'
		JOIN chat.dm_members dm ON dm.conversation_id = dc.id AND dm.user_id = $2 AND dm.status = 'active'
		WHERE p.user_id = $2 AND p.workspace_id = $1`, workspaceID, userID)
	if err != nil {
		return nil, fmt.Errorf("list conversation notification preferences: %w", err)
	}
	defer rows.Close()
	prefs := make([]ConversationNotificationPref, 0)
	for rows.Next() {
		var item ConversationNotificationPref
		if err := rows.Scan(&item.TargetType, &item.TargetID, &item.Level, &item.Muted); err != nil {
			return nil, fmt.Errorf("scan conversation notification preference: %w", err)
		}
		prefs = append(prefs, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate conversation notification preferences: %w", err)
	}
	return prefs, nil
}

// prefsForChannelSQL and prefsForDMSQL read the one target against a list of
// recipients. Keyed by (user_id, target), which is exactly the shape of the
// partial unique indexes 000037 created, so this is an index lookup per user
// rather than a scan.
const prefsForChannelSQL = `
	SELECT p.user_id::text, p.notification_level, (p.muted_at IS NOT NULL)
	FROM chat.conversation_notification_prefs p
	WHERE p.workspace_id = $1::uuid
	  AND p.channel_id = $2::uuid
	  AND p.user_id = ANY($3::uuid[])`

const prefsForDMSQL = `
	SELECT p.user_id::text, p.notification_level, (p.muted_at IS NOT NULL)
	FROM chat.conversation_notification_prefs p
	WHERE p.workspace_id = $1::uuid
	  AND p.dm_conversation_id = $2::uuid
	  AND p.user_id = ANY($3::uuid[])`

// PreferencesForUsers reads one target's preferences for a whole recipient list.
func (s *PGXNotificationPrefStore) PreferencesForUsers(
	ctx context.Context, workspaceID, targetType, targetID string, userIDs []string,
) ([]UserConversationNotificationPref, error) {
	if len(userIDs) == 0 {
		return nil, nil
	}
	var query string
	switch targetType {
	case NotificationPrefTargetChannel:
		query = prefsForChannelSQL
	case NotificationPrefTargetDM:
		query = prefsForDMSQL
	default:
		// Fail closed the way the rest of this store does: an unrecognised kind
		// is not a reason to report anybody as muted, and it is not a reason to
		// report anybody as unmuted either — so it is an error, not an answer.
		return nil, domain.ErrInvalidInput
	}
	rows, err := s.pool.Query(ctx, query, workspaceID, targetID, userIDs)
	if err != nil {
		return nil, fmt.Errorf("read conversation notification preferences: %w", err)
	}
	defer rows.Close()
	prefs := make([]UserConversationNotificationPref, 0, len(userIDs))
	for rows.Next() {
		var pref UserConversationNotificationPref
		if err := rows.Scan(&pref.UserID, &pref.Level, &pref.Muted); err != nil {
			return nil, fmt.Errorf("scan recipient notification preference: %w", err)
		}
		prefs = append(prefs, pref)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recipient notification preferences: %w", err)
	}
	return prefs, nil
}
