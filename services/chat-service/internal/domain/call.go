package domain

import "time"

type CallType string

const (
	CallTypeAudio CallType = "audio"
	CallTypeVideo CallType = "video"
)

func (t CallType) Valid() bool {
	return t == CallTypeAudio || t == CallTypeVideo
}

type CallStatus string

const (
	CallStatusRinging   CallStatus = "ringing"
	CallStatusActive    CallStatus = "active"
	CallStatusDeclined  CallStatus = "declined"
	CallStatusCancelled CallStatus = "cancelled"
	CallStatusTimedOut  CallStatus = "timed_out"
	CallStatusEnded     CallStatus = "ended"
)

func (s CallStatus) Valid() bool {
	switch s {
	case CallStatusRinging, CallStatusActive, CallStatusDeclined,
		CallStatusCancelled, CallStatusTimedOut, CallStatusEnded:
		return true
	default:
		return false
	}
}

func (s CallStatus) Terminal() bool {
	return s == CallStatusDeclined || s == CallStatusCancelled ||
		s == CallStatusTimedOut || s == CallStatusEnded
}

type Call struct {
	ID          string
	WorkspaceID string
	RequestID   string
	CallerID    string
	CalleeID    string
	Type        CallType
	Status      CallStatus
	Version     int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
	ExpiresAt   time.Time
	AcceptedAt  *time.Time
	EndedAt     *time.Time
}

func (c Call) IsParticipant(userID string) bool {
	return userID == c.CallerID || userID == c.CalleeID
}
