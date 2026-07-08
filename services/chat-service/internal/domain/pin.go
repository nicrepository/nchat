package domain

import "time"

// PinnedMessage is a container pin (RF-05): a message pinned for the whole
// channel or DM, plus who pinned it and when. A pinned message that is later deleted
// (RF-14) stays pinned and is rendered with the standard "removed" placeholder.
type PinnedMessage struct {
	Message        Message
	PinnedByUserID string
	PinnedAt       time.Time
}
