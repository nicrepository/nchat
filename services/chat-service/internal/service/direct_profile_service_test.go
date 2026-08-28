package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
)

// Direct-profile service tests (issue #443).
//
// Two properties are being defended here. First, authorisation happens before
// identity: the conversation is resolved against the caller's own membership,
// and only a conversation that survived that is allowed to produce a profile.
// Second, the profile is the *other* participant, chosen by the store from the
// membership rows — the service never takes a user ID from anywhere.

func directConversation() domain.DMConversation {
	return domain.DMConversation{
		ID:          "conv-1",
		WorkspaceID: "ws-1",
		Type:        domain.DMConversationTypeDirect,
		Status:      domain.DMConversationStatusActive,
		CreatedBy:   "user-1",
		CreatedAt:   time.Date(2024, 3, 4, 15, 0, 0, 0, time.UTC),
	}
}

func counterpartProfile() domain.DMDirectProfile {
	return domain.DMDirectProfile{
		UserID:      "user-other",
		DisplayName: "Juliane Lino",
		AvatarURL:   "/media/juliane.png",
		Email:       "juliane@nic.test",
	}
}

func directProfileInput() service.DirectProfileInput {
	return service.DirectProfileInput{
		WorkspaceID:    "ws-1",
		CallerID:       "user-1",
		ConversationID: "conv-1",
	}
}

func TestDMService_GetDirectProfile_ReturnsTheOtherParticipant(t *testing.T) {
	dms := &fakeDMStore{visibleConversation: directConversation(), counterpart: counterpartProfile()}
	svc := service.NewDMService(dms, &fakeMemberStore{})

	got, err := svc.GetDirectProfile(context.Background(), directProfileInput())
	if err != nil {
		t.Fatalf("GetDirectProfile: %v", err)
	}
	if got.Profile != counterpartProfile() {
		t.Fatalf("profile = %+v, want the counterpart", got.Profile)
	}
	if got.Conversation.ID != "conv-1" {
		t.Fatalf("conversation = %+v", got.Conversation)
	}
	// The caller is never the profile: the store was asked for "not this user",
	// and it was asked with the server-side workspace and the ID of the
	// conversation the access check returned — not the raw request value.
	if len(dms.counterpartCalls) != 1 {
		t.Fatalf("expected exactly one counterpart lookup, got %d", len(dms.counterpartCalls))
	}
	call := dms.counterpartCalls[0]
	if call.workspaceID != "ws-1" || call.conversationID != "conv-1" || call.callerID != "user-1" {
		t.Fatalf("unexpected counterpart lookup: %+v", call)
	}
	if got.Profile.UserID == directProfileInput().CallerID {
		t.Fatalf("the caller must never be returned as their own profile")
	}
}

func TestDMService_GetDirectProfile_SettlesAccessBeforeReadingAnyProfile(t *testing.T) {
	for name, visibilityErr := range map[string]error{
		// Every denial the visibility predicate folds together: a missing
		// conversation, one in another workspace, an archived one, and one the
		// caller is not (or is no longer) an active participant of.
		"unknown, foreign, archived or not a participant": domain.ErrNotFound,
		"explicitly forbidden":                            domain.ErrForbidden,
	} {
		t.Run(name, func(t *testing.T) {
			dms := &fakeDMStore{getVisibleErr: visibilityErr, counterpart: counterpartProfile()}
			svc := service.NewDMService(dms, &fakeMemberStore{})

			if _, err := svc.GetDirectProfile(context.Background(), directProfileInput()); !errors.Is(err, visibilityErr) {
				t.Fatalf("err = %v, want %v", err, visibilityErr)
			}
			// The point: no profile row is read for a caller who was refused, so
			// an unauthorised request never reaches anybody's e-mail.
			if len(dms.counterpartCalls) != 0 {
				t.Fatalf("a refused caller must not reach the profile query, got %d lookups", len(dms.counterpartCalls))
			}
		})
	}
}

func TestDMService_GetDirectProfile_RefusesAGroupAsIfItDidNotExist(t *testing.T) {
	dms := &fakeDMStore{visibleConversation: groupConversation(), counterpart: counterpartProfile()}
	svc := service.NewDMService(dms, &fakeMemberStore{})

	// The type comes from the row the database returned, never from the client.
	// A group answers the same ErrNotFound as a missing conversation, so a group
	// ID cannot be laundered into a profile read and the two are indistinguishable.
	if _, err := svc.GetDirectProfile(context.Background(), directProfileInput()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if len(dms.counterpartCalls) != 0 {
		t.Fatalf("a group must not reach the profile query, got %d lookups", len(dms.counterpartCalls))
	}
}

func TestDMService_GetDirectProfile_PassesStoreVerdictsThrough(t *testing.T) {
	for name, tt := range map[string]struct {
		storeErr error
		want     error
	}{
		// No active counterpart left: a denial, folded into the common 404.
		"counterpart gone": {storeErr: domain.ErrNotFound, want: domain.ErrNotFound},
		// A 'direct' row with several active counterparts is corrupt data, and it
		// must keep its own identity all the way to the handler so it can be
		// reported instead of disappearing into the 404 bucket.
		"corrupt conversation": {
			storeErr: domain.ErrInconsistentDirectConversation,
			want:     domain.ErrInconsistentDirectConversation,
		},
	} {
		t.Run(name, func(t *testing.T) {
			dms := &fakeDMStore{visibleConversation: directConversation(), counterpartErr: tt.storeErr}
			svc := service.NewDMService(dms, &fakeMemberStore{})

			if _, err := svc.GetDirectProfile(context.Background(), directProfileInput()); !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestDMService_GetDirectProfile_WrapsUnexpectedStoreFailures(t *testing.T) {
	boom := errors.New("connection reset")
	dms := &fakeDMStore{visibleConversation: directConversation(), counterpartErr: boom}
	svc := service.NewDMService(dms, &fakeMemberStore{})

	_, err := svc.GetDirectProfile(context.Background(), directProfileInput())
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the store failure", err)
	}
	// An infrastructure failure is not a denial: it must not be mistakable for
	// ErrNotFound, which would tell the caller the conversation does not exist.
	if errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("a store failure must not present as a denial: %v", err)
	}
}

func TestDMService_GetDirectProfile_RefusesWhenMembershipIsRevokedBetweenReads(t *testing.T) {
	// The visibility check says yes — it ran before the revocation. The profile
	// query, which re-establishes the caller's membership in the statement that
	// projects the counterpart, says no.
	dms := &fakeDMStore{
		visibleConversation: directConversation(),
		counterpart:         counterpartProfile(),
		counterpartErr:      domain.ErrNotFound,
	}
	svc := service.NewDMService(dms, &fakeMemberStore{})

	got, err := svc.GetDirectProfile(context.Background(), directProfileInput())
	// The second query is the authority: a stale "visible" verdict must not be
	// enough to hand out a name and an e-mail.
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if got.Profile != (domain.DMDirectProfile{}) {
		t.Fatalf("no profile may survive a revoked membership, got %+v", got.Profile)
	}
	// Nothing from the first read may be used to populate the answer either.
	if got.Conversation != (domain.DMConversation{}) {
		t.Fatalf("no conversation data may leak from the first read, got %+v", got.Conversation)
	}
}
