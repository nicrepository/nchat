package ws

import (
	"context"
	"errors"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// AuthorizeSubjects is the batch half of the same authority CanAccess uses: it
// answers "which of these users may see this target". Everything it returns is
// then treated as authorized, so each case below protects against a way of
// authorizing somebody the single-subject path would have refused.

// ── test doubles that DO support batch filtering ─────────────────────────────

type filteringChannelChecker struct {
	fakeChannelChecker
	visible     []string
	err         error
	workspaceID string
	channelID   string
	userIDs     []string
	calls       int
}

func (f *filteringChannelChecker) FilterUsersVisibleToChannel(
	_ context.Context, workspaceID, channelID string, userIDs []string,
) ([]string, error) {
	f.calls++
	f.workspaceID = workspaceID
	f.channelID = channelID
	f.userIDs = userIDs
	if f.err != nil {
		// A failing query returns no rows, the way the real store does. The
		// point of the error cases is that the error is propagated rather than
		// read as a decision — not that a list is discarded.
		return nil, f.err
	}
	return f.visible, nil
}

type filteringDMStore struct {
	fakeDMStore
	inConversation []string
	filterErr      error
	workspaceID    string
	conversationID string
	userIDs        []string
	calls          int
}

func (f *filteringDMStore) FilterUsersInConversation(
	_ context.Context, workspaceID, conversationID string, userIDs []string,
) ([]string, error) {
	f.calls++
	f.workspaceID = workspaceID
	f.conversationID = conversationID
	f.userIDs = userIDs
	if f.filterErr != nil {
		return nil, f.filterErr
	}
	return f.inConversation, nil
}

// newSubjectAuthorizer builds the production authorizer and asserts the batch
// interface off it. NewServiceAuthorizer returns the single-subject interface,
// so this assertion is itself part of what the tests below protect: losing
// AuthorizeSubjects would silently fall the callers back to the one-at-a-time
// path rather than fail to compile.
func newSubjectAuthorizer(t *testing.T, channels channelVisibilityChecker, dms storage.DMStore) SubjectAuthorizer {
	t.Helper()
	subjects, ok := NewServiceAuthorizer(channels, dms).(SubjectAuthorizer)
	if !ok {
		t.Fatal("the production authorizer no longer satisfies SubjectAuthorizer")
	}
	return subjects
}

// ── delegation ───────────────────────────────────────────────────────────────

func TestAuthorizeSubjects_ChannelDelegatesToVisibilityFilterAndReturnsOnlyItsAnswer(t *testing.T) {
	channels := &filteringChannelChecker{visible: []string{"user-1"}}
	auth := newSubjectAuthorizer(t, channels, &fakeDMStore{})

	got, err := auth.AuthorizeSubjects(
		context.Background(), "ws-1", TargetTypeChannel, "ch-1", []string{"user-1", "user-2"},
	)
	if err != nil {
		t.Fatalf("AuthorizeSubjects: %v", err)
	}

	// The store is asked about this workspace, this channel and exactly the
	// users the caller named — never a wider question.
	if channels.workspaceID != "ws-1" || channels.channelID != "ch-1" {
		t.Fatalf("asked about workspace %q channel %q, want ws-1/ch-1", channels.workspaceID, channels.channelID)
	}
	if len(channels.userIDs) != 2 || channels.userIDs[0] != "user-1" || channels.userIDs[1] != "user-2" {
		t.Fatalf("asked about %v, want [user-1 user-2]", channels.userIDs)
	}
	// And the answer is the store's, not the caller's list: user-2 was asked
	// about and not returned, so user-2 is not authorized.
	if len(got) != 1 || got[0] != "user-1" {
		t.Fatalf("authorized %v, want only [user-1]", got)
	}
}

func TestAuthorizeSubjects_DMDelegatesToConversationMembership(t *testing.T) {
	dms := &filteringDMStore{inConversation: []string{"user-2"}}
	auth := newSubjectAuthorizer(t, &fakeChannelChecker{}, dms)

	got, err := auth.AuthorizeSubjects(
		context.Background(), "ws-1", TargetTypeDM, "conv-1", []string{"user-1", "user-2"},
	)
	if err != nil {
		t.Fatalf("AuthorizeSubjects: %v", err)
	}
	if dms.workspaceID != "ws-1" || dms.conversationID != "conv-1" {
		t.Fatalf("asked about workspace %q conversation %q, want ws-1/conv-1", dms.workspaceID, dms.conversationID)
	}
	if len(got) != 1 || got[0] != "user-2" {
		t.Fatalf("authorized %v, want only [user-2]", got)
	}
}

// A channel target must never be answered by the DM store, or the other way
// round: each kind has its own authority and they enforce different rules.
func TestAuthorizeSubjects_DoesNotCrossTargetKinds(t *testing.T) {
	channels := &filteringChannelChecker{visible: []string{"user-1"}}
	dms := &filteringDMStore{inConversation: []string{"user-1"}}
	auth := newSubjectAuthorizer(t, channels, dms)

	if _, err := auth.AuthorizeSubjects(
		context.Background(), "ws-1", TargetTypeChannel, "ch-1", []string{"user-1"},
	); err != nil {
		t.Fatalf("AuthorizeSubjects(channel): %v", err)
	}
	if dms.calls != 0 {
		t.Fatalf("a channel target reached the DM store %d time(s)", dms.calls)
	}

	if _, err := auth.AuthorizeSubjects(
		context.Background(), "ws-1", TargetTypeDM, "conv-1", []string{"user-1"},
	); err != nil {
		t.Fatalf("AuthorizeSubjects(dm): %v", err)
	}
	if channels.calls != 1 {
		t.Fatalf("the DM target reached the channel checker; channel calls = %d, want 1", channels.calls)
	}
}

// ── fail-secure paths ────────────────────────────────────────────────────────

// An unrecognised kind is not a reason to authorize anybody, exactly as in
// CanAccess. Returning the caller's list here would authorize every one of them.
func TestAuthorizeSubjects_UnknownTargetTypeAuthorizesNobody(t *testing.T) {
	channels := &filteringChannelChecker{visible: []string{"user-1", "user-2"}}
	dms := &filteringDMStore{inConversation: []string{"user-1", "user-2"}}
	auth := newSubjectAuthorizer(t, channels, dms)

	got, err := auth.AuthorizeSubjects(
		context.Background(), "ws-1", TargetType("thread"), "t-1", []string{"user-1", "user-2"},
	)
	if err != nil {
		t.Fatalf("AuthorizeSubjects: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("an unknown target type authorized %v, want nobody", got)
	}
	// It must also not have consulted either store for a kind it does not know.
	if channels.calls != 0 || dms.calls != 0 {
		t.Fatalf("unknown target type consulted stores: channel=%d dm=%d", channels.calls, dms.calls)
	}
}

// When the wired store cannot answer for a list, the caller has to be told to
// ask one at a time. Answering "nobody" would silently drop legitimate
// subscribers; answering "everybody" would leak. Only the error is correct.
func TestAuthorizeSubjects_UnsupportedBatchIsReportedNotGuessed(t *testing.T) {
	auth := newSubjectAuthorizer(t, &fakeChannelChecker{result: true}, &fakeDMStore{})

	for _, test := range []struct {
		name       string
		targetType TargetType
	}{
		{name: "channel store without a filter", targetType: TargetTypeChannel},
		{name: "dm store without a filter", targetType: TargetTypeDM},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := auth.AuthorizeSubjects(
				context.Background(), "ws-1", test.targetType, "target-1", []string{"user-1"},
			)
			if !errors.Is(err, ErrSubjectBatchUnsupported) {
				t.Fatalf("error = %v, want ErrSubjectBatchUnsupported", err)
			}
			if len(got) != 0 {
				t.Fatalf("an unanswerable batch authorized %v, want nobody", got)
			}
		})
	}
}

// A store failure must not degrade into an authorization decision.
func TestAuthorizeSubjects_StoreErrorAuthorizesNobody(t *testing.T) {
	storeErr := errors.New("query failed")

	t.Run("channel", func(t *testing.T) {
		channels := &filteringChannelChecker{err: storeErr}
		auth := newSubjectAuthorizer(t, channels, &fakeDMStore{})

		got, err := auth.AuthorizeSubjects(
			context.Background(), "ws-1", TargetTypeChannel, "ch-1", []string{"user-1"},
		)
		if !errors.Is(err, storeErr) {
			t.Fatalf("error = %v, want the store's error", err)
		}
		if len(got) != 0 {
			t.Fatalf("a failed lookup authorized %v, want nobody", got)
		}
	})

	t.Run("dm", func(t *testing.T) {
		dms := &filteringDMStore{filterErr: storeErr}
		auth := newSubjectAuthorizer(t, &fakeChannelChecker{}, dms)

		got, err := auth.AuthorizeSubjects(
			context.Background(), "ws-1", TargetTypeDM, "conv-1", []string{"user-1"},
		)
		if !errors.Is(err, storeErr) {
			t.Fatalf("error = %v, want the store's error", err)
		}
		if len(got) != 0 {
			t.Fatalf("a failed lookup authorized %v, want nobody", got)
		}
	})
}

// An empty request is answered without consulting anything — there is nothing
// to authorize, and it must not be reported as an unsupported batch either.
func TestAuthorizeSubjects_EmptySubjectListShortCircuits(t *testing.T) {
	channels := &filteringChannelChecker{}
	dms := &filteringDMStore{}
	auth := newSubjectAuthorizer(t, channels, dms)

	got, err := auth.AuthorizeSubjects(context.Background(), "ws-1", TargetTypeChannel, "ch-1", nil)
	if err != nil {
		t.Fatalf("AuthorizeSubjects: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("authorized %v for an empty request, want nobody", got)
	}
	if channels.calls != 0 || dms.calls != 0 {
		t.Fatalf("an empty request consulted stores: channel=%d dm=%d", channels.calls, dms.calls)
	}
}

// Guards the wiring itself: the batch authorizer must be the same object the
// subscription layer already trusts for single-subject decisions.
func TestAuthorizeSubjects_IsImplementedByTheProductionAuthorizer(t *testing.T) {
	var auth any = NewServiceAuthorizer(&filteringChannelChecker{}, &filteringDMStore{})
	if _, ok := auth.(SubjectAuthorizer); !ok {
		t.Fatal("the production authorizer no longer satisfies SubjectAuthorizer")
	}
	if _, ok := auth.(SubscriptionAuthorizer); !ok {
		t.Fatal("the production authorizer no longer satisfies SubscriptionAuthorizer")
	}
	// Sanity: the single-subject path still refuses an unknown kind too, which
	// is the invariant the batch path above mirrors.
	ok, err := auth.(SubscriptionAuthorizer).CanAccess(
		context.Background(), "user-1", "ws-1", TargetType("thread"), "t-1",
	)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("CanAccess(unknown kind): %v", err)
	}
	if ok {
		t.Fatal("CanAccess authorized an unknown target kind")
	}
}
