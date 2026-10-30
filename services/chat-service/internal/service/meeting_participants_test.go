package service

import (
	"context"
	"errors"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
	"strings"
	"testing"
)

func (s *msStore) ListMeetingParticipants(_ context.Context, scope storage.MeetingScope, q storage.MeetingPageQuery) (storage.MeetingParticipantPage, error) {
	s.participantCalls++
	return storage.MeetingParticipantPage{}, s.err
}
func (s *msStore) ListMeetingCandidates(_ context.Context, scope storage.MeetingCandidateScope, q storage.MeetingPageQuery) (storage.MeetingCandidatePage, error) {
	s.participantCalls++
	return storage.MeetingCandidatePage{}, s.err
}
func (s *msStore) AddMeetingParticipant(_ context.Context, scope storage.MeetingScope, id string, role domain.MeetingRole) (domain.MeetingParticipant, error) {
	s.participantCalls++
	return domain.MeetingParticipant{UserID: id, Role: role}, s.err
}
func (s *msStore) ChangeMeetingParticipantRole(_ context.Context, scope storage.MeetingScope, id string, role domain.MeetingRole) (domain.MeetingParticipant, error) {
	s.participantCalls++
	return domain.MeetingParticipant{UserID: id, Role: role}, s.err
}
func (s *msStore) RemoveMeetingParticipant(_ context.Context, scope storage.MeetingScope, id string) error {
	s.participantCalls++
	return s.err
}
func (s *msStore) InviteAllMeetingParticipants(_ context.Context, scope storage.MeetingScope) (int64, error) {
	s.participantCalls++
	return 2, s.err
}

func TestMeetingParticipantInputs(t *testing.T) {
	scope := storage.MeetingScope{WorkspaceID: msWorkspace, ActorID: msActor, MeetingID: msOther}
	tests := []struct {
		name  string
		run   func(*MeetingService) error
		valid bool
	}{
		{"add default", func(s *MeetingService) error {
			p, e := s.AddMeetingParticipant(t.Context(), scope, " "+strings.ToUpper(msInvitee)+" ", "")
			if e == nil && (p.UserID != msInvitee || p.Role != domain.MeetingRoleAttendee) {
				t.Fatal(p)
			}
			return e
		}, true},
		{"add cohost", func(s *MeetingService) error {
			_, e := s.AddMeetingParticipant(t.Context(), scope, msInvitee, domain.MeetingRoleCohost)
			return e
		}, true},
		{"invalid id", func(s *MeetingService) error {
			_, e := s.AddMeetingParticipant(t.Context(), scope, "bad", "")
			return e
		}, false},
		{"nil id", func(s *MeetingService) error {
			return s.RemoveMeetingParticipant(t.Context(), scope, "00000000-0000-0000-0000-000000000000")
		}, false},
		{"organizer role", func(s *MeetingService) error {
			_, e := s.AddMeetingParticipant(t.Context(), scope, msInvitee, domain.MeetingRoleOrganizer)
			return e
		}, false},
		{"role required", func(s *MeetingService) error {
			_, e := s.ChangeMeetingParticipantRole(t.Context(), scope, msInvitee, "")
			return e
		}, false},
		{"role id invalid", func(s *MeetingService) error {
			_, e := s.ChangeMeetingParticipantRole(t.Context(), scope, "bad", domain.MeetingRoleAttendee)
			return e
		}, false},
		{"role cohost", func(s *MeetingService) error {
			_, e := s.ChangeMeetingParticipantRole(t.Context(), scope, msInvitee, domain.MeetingRoleCohost)
			return e
		}, true},
		{"remove", func(s *MeetingService) error { return s.RemoveMeetingParticipant(t.Context(), scope, msInvitee) }, true},
		{"all explicit", func(s *MeetingService) error { _, e := s.InviteAllMeetingParticipants(t.Context(), scope); return e }, true},
		{"roster", func(s *MeetingService) error {
			_, e := s.ListMeetingParticipants(t.Context(), scope, storage.MeetingPageQuery{})
			return e
		}, true},
		{"roster search", func(s *MeetingService) error {
			_, e := s.ListMeetingParticipants(t.Context(), scope, storage.MeetingPageQuery{Search: "a"})
			return e
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &msStore{}
			err := tt.run(NewMeetingService(store))
			if tt.valid {
				if err != nil || store.participantCalls != 1 {
					t.Fatalf("calls %d, error %v", store.participantCalls, err)
				}
				store.err = domain.ErrForbidden
				if !errors.Is(tt.run(NewMeetingService(store)), domain.ErrForbidden) {
					t.Fatal("lost storage error")
				}
			} else if !errors.Is(err, domain.ErrInvalidInput) || store.participantCalls != 0 {
				t.Fatalf("invalid input reached storage: %v", err)
			}
		})
	}
}

func TestMeetingCandidateSelectionInputs(t *testing.T) {
	for _, kind := range []domain.MeetingConversationKind{domain.MeetingConversationDM, domain.MeetingConversationChannel, "other"} {
		for _, q := range []storage.MeetingPageQuery{{}, {Limit: 101}, {After: "invalid"}, {Search: strings.Repeat("a", 101)}, {Limit: 1, Search: " Ana "}} {
			scope := storage.MeetingCandidateScope{WorkspaceID: msWorkspace, ActorID: msActor, Conversation: domain.MeetingConversation{Kind: kind, ID: msOther}}
			store := &msStore{}
			_, err := NewMeetingService(store).ListMeetingCandidates(t.Context(), scope, q)
			valid := kind != "other" && q.Limit <= 100 && q.After == "" && len(q.Search) <= 100
			if (err == nil) != valid || (store.participantCalls == 1) != valid {
				t.Fatalf("%s %+v: %v", kind, q, err)
			}
		}
	}
	store := &msStore{err: domain.ErrNotFound}
	scope := storage.MeetingCandidateScope{Conversation: domain.MeetingConversation{Kind: domain.MeetingConversationDM, ID: msOther}}
	_, err := NewMeetingService(store).ListMeetingCandidates(t.Context(), scope, storage.MeetingPageQuery{})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatal(err)
	}
	scope.Conversation.ID = "bad"
	_, err = NewMeetingService(store).ListMeetingCandidates(t.Context(), scope, storage.MeetingPageQuery{})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatal(err)
	}
}
