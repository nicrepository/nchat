package storage

import (
	"context"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// MeetingPatch contains only mutable details. Nil preserves the locked value;
// a non-nil pointer supplies a value, including an empty string.
type MeetingPatch struct {
	Title       *string
	Description *string
	StartsAt    *time.Time
	EndsAt      *time.Time
	Timezone    *string
	JoinPolicy  *domain.MeetingJoinPolicy
}

func (p MeetingPatch) details(d domain.MeetingDetails) domain.MeetingDetails {
	if p.Title != nil {
		d.Title = *p.Title
	}
	if p.Description != nil {
		d.Description = *p.Description
	}
	if p.StartsAt != nil {
		d.StartsAt = *p.StartsAt
	}
	if p.EndsAt != nil {
		d.EndsAt = *p.EndsAt
	}
	if p.Timezone != nil {
		d.Timezone = *p.Timezone
	}
	if p.JoinPolicy != nil {
		d.JoinPolicy = *p.JoinPolicy
	}
	return d
}

// PatchMeeting merges only after locking the row, checking basic update
// authority and comparing the client's version. Omitted fields come from that row.
func (s *PGXMeetingStore) PatchMeeting(ctx context.Context, scope MeetingScope, expectedVersion int64, patch MeetingPatch) (domain.Meeting, error) {
	var meeting domain.Meeting
	err := s.withMeeting(ctx, scope, meetingWrite, "", func(st *meetingTx) error {
		if err := domain.AuthorizeMeeting(st.meeting, st.actor, domain.MeetingCapabilityUpdate); err != nil {
			return err
		}
		if st.meeting.Version != expectedVersion {
			return domain.ErrMeetingStaleVersion
		}
		details := patch.details(st.meeting.MeetingDetails)
		if err := domain.AuthorizeMeetingUpdate(st.meeting, st.actor, details.JoinPolicy); err != nil {
			return err
		}
		next, err := st.meeting.Update(details, expectedVersion)
		if err != nil {
			return err
		}
		meeting, err = writeMeeting(ctx, st.tx, next)
		return err
	})
	return meeting, err
}
