package domain

import "time"

// PinnedMessage is a channel pin (RF-05): a message pinned for the whole
// channel, plus who pinned it and when. A pinned message that is later deleted
// (RF-14) stays pinned and is rendered with the standard "removed" placeholder.
type PinnedMessage struct {
	Message        Message
	PinnedByUserID string
	PinnedAt       time.Time
}

// CanPinInChannel reports whether the user may pin/unpin messages in ch.
//
// Pinning is channel-wide and higher impact than a private favorite, so it is
// gated to elevated roles (RF-05 default policy): workspace owner/admin, or a
// channel moderator. Regular members and guests are denied. Read access is a
// prerequisite. Relaxing this to any member is a one-line policy change here.
func CanPinInChannel(wm *WorkspaceMember, cm *ChannelMember, ch Channel) bool {
	if !CanReadChannel(wm, cm, ch) {
		return false
	}
	if wm.Role == WorkspaceRoleOwner || wm.Role == WorkspaceRoleAdmin {
		return true
	}
	return cm != nil && cm.ChannelID == ch.ID && cm.UserID == wm.UserID && cm.Role == ChannelRoleModerator
}
