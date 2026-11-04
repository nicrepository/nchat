package storage

import (
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

func TestMeetingPatchPreservesOmittedFields(t *testing.T) {
	start := time.Date(2026, 11, 3, 13, 0, 0, 0, time.UTC)
	current := domain.MeetingDetails{Title: "Title", Description: "Agenda", StartsAt: start,
		EndsAt: start.Add(time.Hour), Timezone: "America/Sao_Paulo", JoinPolicy: domain.MeetingJoinInvitedOnly}
	title, empty, zone, policy := "New", "", "UTC", domain.MeetingJoinWorkspaceLink
	later, end := start.Add(time.Hour), start.Add(3*time.Hour)
	renamed, cleared := current, current
	renamed.Title, cleared.Description = title, empty
	cases := []struct {
		name  string
		patch MeetingPatch
		want  domain.MeetingDetails
	}{
		{"absent", MeetingPatch{}, current},
		{"title", MeetingPatch{Title: &title}, renamed},
		{"explicit empty", MeetingPatch{Description: &empty}, cleared},
		{"all", MeetingPatch{Title: &title, Description: &empty, StartsAt: &later, EndsAt: &end, Timezone: &zone, JoinPolicy: &policy},
			domain.MeetingDetails{Title: title, StartsAt: later, EndsAt: end, Timezone: zone, JoinPolicy: policy}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.patch.details(current); got != tt.want {
				t.Fatalf("details = %+v, want %+v", got, tt.want)
			}
		})
	}
}
