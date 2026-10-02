package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/nicrepository/nchat/libs/go/platform/conversationownership"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

type OwnershipScope struct {
	WorkspaceID    string
	Kind           string
	ConversationID string
	ActorID        string
}

type OwnershipMember struct {
	domain.OwnershipParticipant
	DisplayName string                    `json:"display_name"`
	AvatarURL   string                    `json:"avatar_url,omitempty"`
	Actions     domain.ParticipantActions `json:"actions"`
}

type OwnershipDetails struct {
	Enabled      bool                         `json:"enabled"`
	Members      []OwnershipMember            `json:"members"`
	Capabilities domain.OwnershipCapabilities `json:"capabilities"`
	LeavePreview domain.OwnershipLeavePreview `json:"leave_preview"`
}

type OwnershipMutation struct {
	Scope          OwnershipScope
	TargetUserID   string
	Role           domain.ConversationRole
	Operation      string
	IdempotencyKey string
	Name           string
}

type OwnershipMutationResult struct {
	Replayed     bool                    `json:"-"`
	TargetUserID string                  `json:"target_user_id"`
	Role         domain.ConversationRole `json:"role"`
	Left         bool                    `json:"left"`
	EventID      string                  `json:"event_id,omitempty"`
}

type PGXOwnershipStore struct{ pool Pool }

func NewPGXOwnershipStore(pool Pool) *PGXOwnershipStore { return &PGXOwnershipStore{pool: pool} }

func (s *PGXOwnershipStore) Details(ctx context.Context, scope OwnershipScope) (OwnershipDetails, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return OwnershipDetails{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// A consistent snapshot makes capabilities and preview describe one state.
	if _, err = tx.Exec(ctx, "SET TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY"); err != nil {
		return OwnershipDetails{}, err
	}
	details, err := readOwnershipDetails(ctx, tx, scope)
	if err != nil {
		return OwnershipDetails{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return OwnershipDetails{}, err
	}
	return details, nil
}

func readOwnershipDetails(ctx context.Context, tx pgx.Tx, scope OwnershipScope) (OwnershipDetails, error) {
	var enabled bool
	if err := tx.QueryRow(ctx, `SELECT enabled FROM chat.ownership_rollout WHERE singleton`).Scan(&enabled); err != nil {
		return OwnershipDetails{}, err
	}
	members, err := readOwnershipMembers(ctx, tx, scope)
	if err != nil {
		return OwnershipDetails{}, err
	}
	participants, actor := ownershipFacts(members, strings.ToLower(scope.ActorID))
	if !actor.HasAccess {
		return OwnershipDetails{}, domain.ErrNotFound
	}
	if !enabled {
		return OwnershipDetails{Members: []OwnershipMember{}}, nil
	}
	applyOwnershipActions(members, participants, actor)
	return OwnershipDetails{Enabled: true, Members: members, Capabilities: domain.PrivateConversationCapabilities(actor, true), LeavePreview: domain.PreviewOwnershipLeave(participants, scope.ActorID)}, nil
}

func readOwnershipMembers(ctx context.Context, tx pgx.Tx, scope OwnershipScope) ([]OwnershipMember, error) {
	rows, err := tx.Query(ctx, `
 SELECT p.user_id::text,p.role,p.joined_at,p.guest,
        COALESCE(NULLIF(btrim(u.full_name),''),u.display_name,''),COALESCE(u.avatar_url,'')
 FROM chat.active_ownership_participants p JOIN auth.users u ON u.id = p.user_id
 WHERE p.kind = $1 AND p.conversation_id = $2::uuid AND p.workspace_id = $3::uuid
 ORDER BY (p.user_id = $4::uuid) DESC,
 CASE p.role WHEN 'owner' THEN 0 WHEN 'admin' THEN 1 ELSE 2 END,
 lower(COALESCE(NULLIF(btrim(u.full_name),''),u.display_name,'')),p.user_id`,
		scope.Kind, scope.ConversationID, scope.WorkspaceID, scope.ActorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	members := make([]OwnershipMember, 0)
	for rows.Next() {
		var member OwnershipMember
		if err := rows.Scan(&member.UserID, &member.Role, &member.JoinedAt, &member.Guest, &member.DisplayName, &member.AvatarURL); err != nil {
			return nil, err
		}
		member.HasAccess = true
		members = append(members, member)
	}
	return members, rows.Err()
}

func ownershipFacts(members []OwnershipMember, actorID string) ([]domain.OwnershipParticipant, domain.OwnershipParticipant) {
	participants := make([]domain.OwnershipParticipant, 0, len(members))
	var actor domain.OwnershipParticipant
	for _, member := range members {
		participants = append(participants, member.OwnershipParticipant)
		if member.UserID == actorID {
			actor = member.OwnershipParticipant
		}
	}
	return participants, actor
}

func applyOwnershipActions(members []OwnershipMember, participants []domain.OwnershipParticipant, actor domain.OwnershipParticipant) {
	for i := range members {
		target := members[i].OwnershipParticipant
		members[i].Actions = domain.PrivateParticipantActions(actor, target)
		if target.Role == domain.ConversationOwner && !domain.CanDemoteConversationOwner(participants, target.UserID) {
			members[i].Actions.AssignRole = false
		}
		if target.UserID == actor.UserID {
			members[i].Actions.Transfer = actor.Role == domain.ConversationOwner && len(participants) > 1
		}
	}
}

func (s *PGXOwnershipStore) Mutate(ctx context.Context, input OwnershipMutation) (OwnershipMutationResult, error) {
	input.Scope.ActorID = strings.ToLower(input.Scope.ActorID)
	input.TargetUserID = strings.ToLower(input.TargetUserID)
	if err := validateOwnershipMutation(input); err != nil {
		return OwnershipMutationResult{}, err
	}
	if input.Operation == "role" {
		result, err := conversationownership.Retry(ctx, func() (OwnershipMutationResult, error) { return s.mutateRoleOnce(ctx, input) })
		return result, mapOwnershipError(err)
	}
	result, err := conversationownership.Retry(ctx, func() (OwnershipMutationResult, error) { return s.mutateOnce(ctx, input) })
	return result, mapOwnershipError(err)
}

func validateOwnershipMutation(input OwnershipMutation) error {
	if input.Scope.Kind != "dm" && input.Scope.Kind != "channel" {
		return domain.ErrNotFound
	}
	switch input.Operation {
	case "leave", "remove", "rename":
		return nil
	case "role":
		if !input.Role.Valid() {
			return domain.ErrForbidden
		}
	case "transfer", "transfer-and-leave":
		return validateOwnershipTransfer(input)
	default:
		return domain.ErrForbidden
	}
	return nil
}

func validateOwnershipTransfer(input OwnershipMutation) error {
	if input.Role != domain.ConversationAdmin && input.Role != domain.ConversationMember {
		return domain.ErrForbidden
	}
	if len(input.IdempotencyKey) == 0 || len(input.IdempotencyKey) > 128 {
		return domain.ErrOwnershipConflict
	}
	return nil
}

func (s *PGXOwnershipStore) mutateOnce(ctx context.Context, input OwnershipMutation) (OwnershipMutationResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return OwnershipMutationResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, "SET TRANSACTION ISOLATION LEVEL SERIALIZABLE"); err != nil {
		return OwnershipMutationResult{}, err
	}
	scope := input.Scope
	if _, err = tx.Exec(ctx, `SELECT chat.lock_ownership_conversation($1,$2::uuid)`, scope.Kind, scope.ConversationID); err != nil {
		return OwnershipMutationResult{}, err
	}
	// Replay is checked before role authorization: a committed transfer demoted the
	// actor, and a committed transfer-and-leave removed them. The record is scoped
	// to their authenticated identity and contains no participant profile data.
	result, replayed, err := replayOwnershipRequest(ctx, tx, input)
	if err != nil {
		return OwnershipMutationResult{}, err
	}
	if replayed {
		return result, tx.Commit(ctx)
	}
	result, err = executeOwnershipMutation(ctx, tx, input)
	if err != nil {
		return OwnershipMutationResult{}, err
	}
	if err = persistOwnershipRequest(ctx, tx, input, result); err != nil {
		return OwnershipMutationResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return OwnershipMutationResult{}, err
	}
	return result, nil
}

func authorizeOwnershipMutation(input OwnershipMutation, details OwnershipDetails) error {
	switch input.Operation {
	case "leave":
		return nil
	case "rename":
		if !details.Capabilities.EditMetadata {
			return domain.ErrForbidden
		}
		return nil
	case "remove":
		return authorizeOwnershipRemoval(input, details)
	default:
		return authorizeOwnershipRoles(input, details)
	}
}

func ownershipTarget(members []OwnershipMember, userID string) *OwnershipMember {
	for i := range members {
		if members[i].UserID == userID {
			return &members[i]
		}
	}
	return nil
}

func authorizeOwnershipRemoval(input OwnershipMutation, details OwnershipDetails) error {
	if !details.Capabilities.EditMetadata {
		return domain.ErrForbidden
	}
	target := ownershipTarget(details.Members, input.TargetUserID)
	if target == nil {
		return nil
	}
	if !target.Actions.Remove {
		return domain.ErrForbidden
	}
	return nil
}

func authorizeOwnershipRoles(input OwnershipMutation, details OwnershipDetails) error {
	if !details.Capabilities.ManageRoles {
		return domain.ErrForbidden
	}
	target := ownershipTarget(details.Members, input.TargetUserID)
	if target == nil {
		return domain.ErrNotFound
	}
	if input.Operation != "role" {
		if !target.Actions.Transfer || input.TargetUserID == input.Scope.ActorID {
			return domain.ErrForbidden
		}
		return nil
	}
	participants, _ := ownershipFacts(details.Members, input.Scope.ActorID)
	if target.Role == domain.ConversationOwner && input.Role != domain.ConversationOwner && !domain.CanDemoteConversationOwner(participants, input.TargetUserID) {
		return domain.ErrOwnershipConflict
	}
	return nil
}

func applyOwnershipMutation(ctx context.Context, tx pgx.Tx, input OwnershipMutation) (string, error) {
	switch input.Operation {
	case "leave", "remove":
		return removeOwnershipMembership(ctx, tx, input)
	case "rename":
		return renameOwnershipConversation(ctx, tx, input)
	default:
		if err := changeOwnershipRoles(ctx, tx, input); err != nil {
			return "", err
		}
		if input.Operation == "transfer-and-leave" {
			return removeOwnershipMembership(ctx, tx, input)
		}
		return "", nil
	}
}

func changeOwnershipRoles(ctx context.Context, tx pgx.Tx, input OwnershipMutation) error {
	scope := input.Scope
	role, reason := input.Role, "manual"
	if input.Operation != "role" {
		role, reason = domain.ConversationOwner, "transfer"
	}
	if err := assignOwnership(ctx, tx, scope, input.TargetUserID, role, reason); err != nil {
		return err
	}
	if input.Operation == "role" {
		return nil
	}
	return assignOwnership(ctx, tx, scope, scope.ActorID, input.Role, reason)
}

func ownershipEventInput(scope OwnershipScope, event domain.ConversationEventType) ConversationEventInput {
	input := ConversationEventInput{WorkspaceID: scope.WorkspaceID, ActorID: scope.ActorID, Event: event}
	if scope.Kind == "dm" {
		input.DMConversationID = scope.ConversationID
	} else {
		input.ChannelID = scope.ConversationID
	}
	return input
}

func removeOwnershipMembership(ctx context.Context, tx pgx.Tx, input OwnershipMutation) (string, error) {
	target := input.Scope.ActorID
	eventType := domain.ConversationEventMemberLeft
	if input.Operation == "remove" {
		target, eventType = input.TargetUserID, domain.ConversationEventMemberRemoved
	}
	if input.Operation == "remove" {
		var exists bool
		err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM chat.active_ownership_participants WHERE kind=$1 AND conversation_id=$2::uuid AND user_id=$3::uuid)`, input.Scope.Kind, input.Scope.ConversationID, target).Scan(&exists)
		if err != nil || !exists {
			return "", err
		}
	}
	eventInput := ownershipEventInput(input.Scope, eventType)
	if input.Operation == "remove" {
		targets, err := resolveConversationEventTargetUsers(ctx, tx, []string{target})
		if err != nil {
			return "", err
		}
		eventInput.Payload.TargetUsers = targets
	}
	event, err := InsertConversationEvent(ctx, tx, eventInput)
	if err != nil {
		return "", err
	}
	if input.Scope.Kind == "dm" {
		_, err = tx.Exec(ctx, `UPDATE chat.dm_members SET status='left',left_at=now() WHERE conversation_id=$1::uuid AND user_id=$2::uuid AND status='active'`, input.Scope.ConversationID, target)
	} else {
		_, err = tx.Exec(ctx, `DELETE FROM chat.channel_members WHERE channel_id=$1::uuid AND user_id=$2::uuid`, input.Scope.ConversationID, target)
	}
	return event.ID, err
}

func renameOwnershipConversation(ctx context.Context, tx pgx.Tx, input OwnershipMutation) (string, error) {
	name, err := normalizeOwnershipName(input.Scope.Kind, input.Name)
	if err != nil {
		return "", err
	}
	var previous string
	scope := input.Scope
	if scope.Kind == "dm" {
		err = tx.QueryRow(ctx, `WITH previous AS (SELECT title FROM chat.dm_conversations WHERE id=$1::uuid), updated AS (UPDATE chat.dm_conversations SET title=$2,updated_at=now() WHERE id=$1::uuid) SELECT COALESCE(title,'') FROM previous`, scope.ConversationID, name).Scan(&previous)
	} else {
		err = tx.QueryRow(ctx, `WITH previous AS (SELECT display_name FROM chat.channels WHERE id=$1::uuid), updated AS (UPDATE chat.channels SET display_name=$2,updated_at=now() WHERE id=$1::uuid) SELECT display_name FROM previous`, scope.ConversationID, name).Scan(&previous)
	}
	if err != nil {
		return "", err
	}
	eventInput := ownershipEventInput(scope, domain.ConversationEventRenamed)
	eventInput.Payload = domain.ConversationEventPayload{OldName: previous, NewName: name}
	event, err := InsertConversationEvent(ctx, tx, eventInput)
	return event.ID, err
}

func assignOwnership(ctx context.Context, tx pgx.Tx, scope OwnershipScope, userID string, role domain.ConversationRole, reason string) error {
	_, err := tx.Exec(ctx, `SELECT chat.assign_ownership($1,$2::uuid,$3::uuid,$4,$5::uuid,$6)`, scope.Kind, scope.ConversationID, userID, string(role), scope.ActorID, reason)
	return err
}

func ownershipRequestHash(input OwnershipMutation) string {
	raw, _ := json.Marshal(struct{ Operation, Target, Role string }{input.Operation, input.TargetUserID, string(input.Role)})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func replayOwnershipRequest(ctx context.Context, tx pgx.Tx, input OwnershipMutation) (OwnershipMutationResult, bool, error) {
	if input.Operation != "transfer" && input.Operation != "transfer-and-leave" {
		return OwnershipMutationResult{}, false, nil
	}
	var hash string
	var raw []byte
	scope := input.Scope
	err := tx.QueryRow(ctx, `SELECT request_hash,response FROM chat.ownership_requests
 WHERE workspace_id=$1::uuid AND conversation_kind=$2 AND conversation_id=$3::uuid
 AND actor_user_id=$4::uuid AND idempotency_key=$5`, scope.WorkspaceID, scope.Kind, scope.ConversationID, scope.ActorID, input.IdempotencyKey).Scan(&hash, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return OwnershipMutationResult{}, false, nil
	}
	if err != nil {
		return OwnershipMutationResult{}, false, err
	}
	if hash != ownershipRequestHash(input) {
		return OwnershipMutationResult{}, false, domain.ErrOwnershipConflict
	}
	var result OwnershipMutationResult
	if err = json.Unmarshal(raw, &result); err != nil {
		return result, false, err
	}
	result.Replayed = true
	return result, true, nil
}

func persistOwnershipRequest(ctx context.Context, tx pgx.Tx, input OwnershipMutation, result OwnershipMutationResult) error {
	if input.Operation != "transfer" && input.Operation != "transfer-and-leave" {
		return nil
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	scope := input.Scope
	_, err = tx.Exec(ctx, `INSERT INTO chat.ownership_requests
 (workspace_id,conversation_kind,conversation_id,actor_user_id,idempotency_key,request_hash,response)
 VALUES ($1::uuid,$2,$3::uuid,$4::uuid,$5,$6,$7)`, scope.WorkspaceID, scope.Kind, scope.ConversationID, scope.ActorID, input.IdempotencyKey, ownershipRequestHash(input), raw)
	return err
}

func mapOwnershipError(err error) error {
	var pgerr *pgconn.PgError
	if errors.As(err, &pgerr) && pgerr.Code == "P0953" {
		return domain.ErrOwnershipConflict
	}
	if errors.As(err, &pgerr) && pgerr.Code == "P0954" {
		return domain.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("ownership mutation: %w", err)
	}
	return nil
}

func (s *PGXOwnershipStore) PrivateEnabled(ctx context.Context, scope OwnershipScope) (bool, error) {
	var enabled bool
	err := s.pool.QueryRow(ctx, `SELECT r.enabled AND (
 EXISTS (SELECT 1 FROM chat.dm_conversations d WHERE $1='dm' AND d.id=$2::uuid AND d.workspace_id=$3::uuid AND d.type='group') OR
 EXISTS (SELECT 1 FROM chat.channels c WHERE $1='channel' AND c.id=$2::uuid AND c.workspace_id=$3::uuid AND c.type='private'))
 FROM chat.ownership_rollout r WHERE singleton`, scope.Kind, scope.ConversationID, scope.WorkspaceID).Scan(&enabled)
	return enabled, err
}

func normalizeOwnershipName(kind, name string) (string, error) {
	if kind == "channel" {
		return domain.NormalizeChannelDisplayName(name)
	}
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 120 {
		return "", domain.ErrInvalidInput
	}
	return name, nil
}

func executeOwnershipMutation(ctx context.Context, tx pgx.Tx, input OwnershipMutation) (OwnershipMutationResult, error) {
	details, err := readOwnershipDetails(ctx, tx, input.Scope)
	if err != nil {
		return OwnershipMutationResult{}, err
	}
	if !details.Enabled {
		return OwnershipMutationResult{}, domain.ErrForbidden
	}
	if err = authorizeOwnershipMutation(input, details); err != nil {
		return OwnershipMutationResult{}, err
	}
	eventID, applyErr := applyOwnershipMutation(ctx, tx, input)
	if applyErr != nil {
		return OwnershipMutationResult{}, applyErr
	}
	result := OwnershipMutationResult{EventID: eventID, TargetUserID: input.TargetUserID, Role: domain.ConversationOwner, Left: input.Operation == "transfer-and-leave" || input.Operation == "leave"}
	if input.Operation == "role" {
		result.Role = input.Role
	}
	return result, nil
}
