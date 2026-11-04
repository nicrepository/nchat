package service

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

const (
	msWorkspace = "5e110000-0000-4000-8000-000000000001"
	msActor     = "5e110000-0000-4000-8000-0000000000a1"
	msInvitee   = "5e110000-0000-4000-8000-0000000000a2"
	msOther     = "5e110000-0000-4000-8000-0000000000a3"
)

func msInput() CreateMeetingInput {
	start := time.Date(2026, 11, 3, 13, 0, 0, 123456789, time.UTC)
	return CreateMeetingInput{WorkspaceID: msWorkspace, ActorID: msActor, IdempotencyKey: "intent-1053",
		Details: domain.MeetingDetails{Title: " Planejamento ", Description: " Pauta ", StartsAt: start,
			EndsAt: start.Add(time.Hour), Timezone: " America/Sao_Paulo "},
		Participants: []storage.MeetingInvitee{{UserID: msOther}, {UserID: strings.ToUpper(msInvitee), Role: domain.MeetingRoleCohost}}}
}

func TestMeetingCreationCanonicalIntent(t *testing.T) {
	base, err := normalizeMeetingCreation(msInput())
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 11, 3, 13, 0, 0, 123456000, time.UTC)
	expected := domain.Meeting{ID: base.Meeting.ID, WorkspaceID: msWorkspace, OrganizerUserID: msActor,
		MeetingDetails: domain.MeetingDetails{Title: "Planejamento", Description: "Pauta", StartsAt: start,
			EndsAt: start.Add(time.Hour), Timezone: "America/Sao_Paulo", JoinPolicy: domain.MeetingJoinInvitedOnly},
		Status: domain.MeetingStatusScheduled, RoomKey: domain.MeetingRoomKey(base.Meeting.ID), Version: 1}
	if base.Meeting != expected {
		t.Fatalf("normalized meeting = %+v", base.Meeting)
	}
	want := []storage.MeetingInvitee{{UserID: msInvitee, Role: domain.MeetingRoleCohost}, {UserID: msOther, Role: domain.MeetingRoleAttendee}}
	if !reflect.DeepEqual(base.Participants, want) || len(base.RequestHash) != 64 {
		t.Fatalf("participants = %+v, hash = %q", base.Participants, base.RequestHash)
	}
	equivalent := msInput()
	equivalent.Details = base.Meeting.MeetingDetails
	equivalent.Details.StartsAt = equivalent.Details.StartsAt.In(time.FixedZone("offset", -3*3600))
	equivalent.Participants = want
	equivalent.WorkspaceID = strings.ToUpper(msWorkspace)
	equivalent.ActorID = strings.ToUpper(msActor)
	normalized, err := normalizeMeetingCreation(equivalent)
	if err != nil || normalized.RequestHash != base.RequestHash || normalized.Meeting.ID == base.Meeting.ID {
		t.Fatalf("equivalent intent did not hash equally with independent identity: %v", err)
	}
}

func TestMeetingCreationFingerprintsAllPersistedIntent(t *testing.T) {
	base, err := normalizeMeetingCreation(msInput())
	if err != nil {
		t.Fatal(err)
	}
	changes := map[string]func(*CreateMeetingInput){
		"title":       func(i *CreateMeetingInput) { i.Details.Title = "Outra" },
		"description": func(i *CreateMeetingInput) { i.Details.Description = "Outra" },
		"start":       func(i *CreateMeetingInput) { i.Details.StartsAt = i.Details.StartsAt.Add(time.Second) },
		"end":         func(i *CreateMeetingInput) { i.Details.EndsAt = i.Details.EndsAt.Add(time.Second) },
		"timezone":    func(i *CreateMeetingInput) { i.Details.Timezone = "UTC" },
		"policy":      func(i *CreateMeetingInput) { i.Details.JoinPolicy = domain.MeetingJoinWorkspaceLink },
		"conversation": func(i *CreateMeetingInput) {
			i.Conversation = &domain.MeetingConversation{Kind: domain.MeetingConversationChannel, ID: msOther}
		},
		"invitees": func(i *CreateMeetingInput) { i.Participants = nil },
		"role":     func(i *CreateMeetingInput) { i.Participants[0].Role = domain.MeetingRoleCohost },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			input := msInput()
			change(&input)
			got, err := normalizeMeetingCreation(input)
			if err != nil || got.RequestHash == base.RequestHash {
				t.Fatalf("material change hashed equally: %v", err)
			}
		})
	}
	// The two conversation kinds are material even with the same UUID.
	input := msInput()
	input.Conversation = &domain.MeetingConversation{Kind: domain.MeetingConversationDM, ID: msOther}
	dm, err := normalizeMeetingCreation(input)
	if err != nil {
		t.Fatal(err)
	}
	input.Conversation.Kind = domain.MeetingConversationChannel
	channel, err := normalizeMeetingCreation(input)
	if err != nil || dm.RequestHash == channel.RequestHash {
		t.Fatalf("conversation kind omitted: %v", err)
	}
}

func TestMeetingCreationRejectsInvalidBoundaryInput(t *testing.T) {
	changes := map[string]func(*CreateMeetingInput){
		"missing key":            func(i *CreateMeetingInput) { i.IdempotencyKey = "" },
		"bad key":                func(i *CreateMeetingInput) { i.IdempotencyKey = "key has spaces" },
		"long key":               func(i *CreateMeetingInput) { i.IdempotencyKey = strings.Repeat("x", 129) },
		"invalid workspace":      func(i *CreateMeetingInput) { i.WorkspaceID = "bad" },
		"invalid actor":          func(i *CreateMeetingInput) { i.ActorID = "bad" },
		"title":                  func(i *CreateMeetingInput) { i.Details.Title = "" },
		"interval":               func(i *CreateMeetingInput) { i.Details.EndsAt = i.Details.StartsAt },
		"timezone":               func(i *CreateMeetingInput) { i.Details.Timezone = "Local" },
		"policy":                 func(i *CreateMeetingInput) { i.Details.JoinPolicy = "public" },
		"conversation":           func(i *CreateMeetingInput) { i.Conversation = &domain.MeetingConversation{Kind: "other", ID: msOther} },
		"uuid":                   func(i *CreateMeetingInput) { i.Participants[0].UserID = "bad" },
		"nil uuid":               func(i *CreateMeetingInput) { i.Participants[0].UserID = "00000000-0000-0000-0000-000000000000" },
		"organizer repeated":     func(i *CreateMeetingInput) { i.Participants[0].UserID = strings.ToUpper(msActor) },
		"duplicate":              func(i *CreateMeetingInput) { i.Participants[0].UserID = msInvitee },
		"organizer role":         func(i *CreateMeetingInput) { i.Participants[0].Role = domain.MeetingRoleOrganizer },
		"unknown role":           func(i *CreateMeetingInput) { i.Participants[0].Role = "admin" },
		"unserializable instant": func(i *CreateMeetingInput) { i.Details.EndsAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			input := msInput()
			change(&input)
			store := &msStore{}
			_, err := NewMeetingService(store).CreateMeeting(t.Context(), input)
			if !errors.Is(err, domain.ErrInvalidInput) || store.creates != 0 {
				t.Fatalf("error = %v, writes = %d", err, store.creates)
			}
		})
	}
}

type msStore struct {
	participantCalls int
	creation         storage.MeetingCreation
	result           storage.MeetingCreationResult
	err              error
	creates          int
	m                domain.Meeting
	scope            storage.MeetingScope
	version          int64
	patch            storage.MeetingPatch
	updates          int
	next             domain.MeetingStatus
}

func (s *msStore) CreateMeetingWithParticipants(_ context.Context, in storage.MeetingCreation) (storage.MeetingCreationResult, error) {
	s.creation = in
	s.creates++
	return s.result, s.err
}
func (s *msStore) PatchMeeting(_ context.Context, scope storage.MeetingScope, version int64, patch storage.MeetingPatch) (domain.Meeting, error) {
	s.scope = scope
	s.version = version
	s.patch = patch
	s.updates++
	return s.m, s.err
}
func (s *msStore) TransitionMeeting(_ context.Context, scope storage.MeetingScope, version int64, next domain.MeetingStatus) (domain.Meeting, error) {
	s.scope = scope
	s.version = version
	s.next = next
	return s.m, s.err
}

func TestMeetingServiceReturnsCommittedCreationAndReplay(t *testing.T) {
	m, err := domain.NewMeeting(msWorkspace, msActor, nil, msInput().Details)
	if err != nil {
		t.Fatal(err)
	}

	for _, replay := range []bool{false, true} {
		t.Run(strconv.FormatBool(replay), func(t *testing.T) {
			store := &msStore{result: storage.MeetingCreationResult{Meeting: m, Replayed: replay}}
			got, err := NewMeetingService(store).CreateMeeting(t.Context(), msInput())
			if err != nil || got != (CreateMeetingResult{Meeting: m, Replayed: replay}) {
				t.Fatalf("result = %+v, %v", got, err)
			}
			store.err = domain.ErrIdempotencyKeyReused
			if _, err = NewMeetingService(store).CreateMeeting(t.Context(), msInput()); !errors.Is(err, store.err) {
				t.Fatalf("storage error = %v", err)
			}
		})
	}
}

func TestMeetingServiceForwardsPatchWithoutReading(t *testing.T) {
	m, err := domain.NewMeeting(msWorkspace, msActor, nil, msInput().Details)
	if err != nil {
		t.Fatal(err)
	}
	title, empty, zone, policy := "Novo", "", "UTC", domain.MeetingJoinWorkspaceLink
	start, end := m.StartsAt.Add(time.Hour), m.EndsAt.Add(2*time.Hour)
	for _, patch := range []MeetingPatch{
		{Title: &title}, {Description: &empty},
		{Title: &title, Description: &empty, StartsAt: &start, EndsAt: &end, Timezone: &zone, JoinPolicy: &policy},
	} {
		scope := storage.MeetingScope{WorkspaceID: msWorkspace, ActorID: msActor, MeetingID: m.ID}
		store := &msStore{m: m}
		got, err := NewMeetingService(store).UpdateMeeting(t.Context(), UpdateMeetingInput{Scope: scope, ExpectedVersion: 7, Patch: patch})
		if err != nil || got != m || store.patch != patch || store.version != 7 || store.scope != scope || store.updates != 1 {
			t.Fatalf("patch delegation = %+v v%d, %v", store.patch, store.version, err)
		}
	}
}

func TestMeetingServiceMutationErrors(t *testing.T) {
	title := "Edited"
	input := UpdateMeetingInput{ExpectedVersion: 1, Patch: MeetingPatch{Title: &title}}
	store := &msStore{err: domain.ErrNotFound}
	svc := NewMeetingService(store)
	if _, err := svc.UpdateMeeting(t.Context(), input); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal(err)
	}
	store.err = domain.ErrMeetingStaleVersion
	if _, err := svc.UpdateMeeting(t.Context(), input); !errors.Is(err, store.err) {
		t.Fatal(err)
	}
	input.ExpectedVersion = 0
	if _, err := svc.UpdateMeeting(t.Context(), input); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatal(err)
	}
	if _, err := svc.UpdateMeeting(t.Context(), UpdateMeetingInput{ExpectedVersion: 1}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatal(err)
	}
	if store.updates != 2 {
		t.Fatalf("invalid input reached storage: updates = %d", store.updates)
	}
}

func TestMeetingServiceCancelDelegation(t *testing.T) {
	store := &msStore{err: domain.ErrMeetingStaleVersion}
	svc := NewMeetingService(store)
	if _, err := svc.CancelMeeting(t.Context(), storage.MeetingScope{}, 0); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatal(err)
	}
	if _, err := svc.CancelMeeting(t.Context(), storage.MeetingScope{}, 1); !errors.Is(err, store.err) {
		t.Fatal(err)
	}
	store.err = nil
	store.m.Version = 8
	store.m.Status = domain.MeetingStatusCancelled
	got, err := svc.CancelMeeting(t.Context(), storage.MeetingScope{}, 1)
	if err != nil || got != store.m || store.version != 1 || store.next != domain.MeetingStatusCancelled {
		t.Fatalf("cancel = %+v %v", got, err)
	}
}
