package domain

import (
	"errors"
	"sort"
	"time"
)

// ConversationRole is local to a private channel or group, independently of
// workspace authority. A manually promoted guest may be an owner.
type ConversationRole string

const (
	ConversationOwner  ConversationRole = "owner"
	ConversationAdmin  ConversationRole = "admin"
	ConversationMember ConversationRole = "member"
)

var ErrOwnershipConflict = errors.New("conversation ownership conflict")

func (r ConversationRole) Valid() bool {
	return r == ConversationOwner || r == ConversationAdmin || r == ConversationMember
}

type OwnershipParticipant struct {
	UserID    string           `json:"user_id"`
	Role      ConversationRole `json:"role"`
	JoinedAt  time.Time        `json:"-"`
	HasAccess bool             `json:"-"`
	Guest     bool             `json:"-"`
}

type ParticipantActions struct {
	Remove     bool `json:"remove"`
	AssignRole bool `json:"assign_role"`
	Transfer   bool `json:"transfer"`
}

type OwnershipCapabilities struct {
	AddMembers   bool `json:"add_members"`
	ManageRoles  bool `json:"manage_roles"`
	EditMetadata bool `json:"edit_metadata"`
	Leave        bool `json:"leave"`
}

func PrivateConversationCapabilities(actor OwnershipParticipant, canAdd bool) OwnershipCapabilities {
	if !actor.HasAccess || !actor.Role.Valid() {
		return OwnershipCapabilities{}
	}
	return OwnershipCapabilities{
		AddMembers:   canAdd,
		ManageRoles:  actor.Role == ConversationOwner,
		EditMetadata: actor.Role == ConversationOwner || actor.Role == ConversationAdmin,
		Leave:        true,
	}
}

func PrivateParticipantActions(actor, target OwnershipParticipant) ParticipantActions {
	if !actor.HasAccess || !target.HasAccess || !actor.Role.Valid() || !target.Role.Valid() {
		return ParticipantActions{}
	}
	owner := actor.Role == ConversationOwner
	other := actor.UserID != target.UserID
	return ParticipantActions{
		Remove:     other && target.Role != ConversationOwner && (owner || actor.Role == ConversationAdmin && target.Role == ConversationMember),
		AssignRole: owner,
		Transfer:   owner && other,
	}
}

type OwnershipLeavePreview struct {
	LastOwner       bool   `json:"last_owner"`
	SuccessorUserID string `json:"successor_user_id,omitempty"`
	Blocked         bool   `json:"blocked"`
}

// PreviewOwnershipLeave uses current active membership age, never presentation
// order. The store must repeat this decision under its transaction locks.
func PreviewOwnershipLeave(participants []OwnershipParticipant, leavingUserID string) OwnershipLeavePreview {
	remaining := make([]OwnershipParticipant, 0, len(participants))
	var leavingOwner, remainingOwner bool
	for _, p := range participants {
		if !p.HasAccess {
			continue
		}
		if p.UserID == leavingUserID {
			leavingOwner = p.Role == ConversationOwner
			continue
		}
		remaining = append(remaining, p)
		remainingOwner = remainingOwner || p.Role == ConversationOwner
	}
	preview := OwnershipLeavePreview{LastOwner: leavingOwner && !remainingOwner}
	if !preview.LastOwner || len(remaining) == 0 {
		return preview
	}
	successor := SelectOwnershipSuccessor(remaining)
	preview.SuccessorUserID = successor
	preview.Blocked = successor == ""
	return preview
}

func SelectOwnershipSuccessor(participants []OwnershipParticipant) string {
	candidates := make([]OwnershipParticipant, 0, len(participants))
	for _, p := range participants {
		if p.HasAccess && !p.Guest && (p.Role == ConversationAdmin || p.Role == ConversationMember) {
			candidates = append(candidates, p)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.Role != b.Role {
			return a.Role == ConversationAdmin
		}
		if !a.JoinedAt.Equal(b.JoinedAt) {
			return a.JoinedAt.Before(b.JoinedAt)
		}
		return a.UserID < b.UserID
	})
	if len(candidates) == 0 {
		return ""
	}
	return candidates[0].UserID
}

func CanDemoteConversationOwner(participants []OwnershipParticipant, targetUserID string) bool {
	for _, p := range participants {
		if p.UserID != targetUserID && p.HasAccess && p.Role == ConversationOwner {
			return true
		}
	}
	return false
}
