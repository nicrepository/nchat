package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
)

type fakePresenceSettingsStore struct {
	setState   domain.PresenceManualState
	setExpires time.Time
	setErr     error
	clearErr   error
	manual     domain.PresenceOverride
	calls      int
}

func (f *fakePresenceSettingsStore) SetManual(
	_ context.Context, _, _ string, state domain.PresenceManualState, expiresAt time.Time,
) (domain.PresenceOverride, error) {
	f.calls++
	f.setState, f.setExpires = state, expiresAt
	return domain.PresenceOverride{State: state, ExpiresAt: expiresAt}, f.setErr
}

func (f *fakePresenceSettingsStore) ClearManual(context.Context, string, string) error {
	f.calls++
	return f.clearErr
}

func (f *fakePresenceSettingsStore) Manual(context.Context, string, string) (domain.PresenceOverride, error) {
	return f.manual, nil
}

type fakePresencePublisher struct {
	announced []string
	// changes is every facts change opened; refuse makes opening one fail.
	changes []string
	refuse  error
}

func (f *fakePresencePublisher) ChangePresenceFacts(
	ctx context.Context, _ string, userIDs []string, mutate func(context.Context) error,
) error {
	if f.refuse != nil {
		return f.refuse
	}
	f.changes = append(f.changes, userIDs...)
	return mutate(ctx)
}

func (f *fakePresencePublisher) PublishPresenceSettingsChanged(_ context.Context, workspaceID, userID string) {
	f.announced = append(f.announced, workspaceID+"/"+userID)
}

func TestPresenceService_SetValidatesStoresAndAnnounces(t *testing.T) {
	store := &fakePresenceSettingsStore{}
	publisher := &fakePresencePublisher{}
	presence := service.NewPresenceService(store, publisher)
	expires := time.Now().Add(time.Hour).In(time.FixedZone("BRT", -3*3600))

	stored, err := presence.SetManual(context.Background(), "ws-1", "user-1", "dnd", expires)
	if err != nil {
		t.Fatalf("SetManual: %v", err)
	}
	if stored.State != domain.PresenceManualDoNotDisturb || store.setExpires.Location() != time.UTC {
		t.Fatalf("stored = %+v at %v", stored, store.setExpires.Location())
	}
	if len(publisher.announced) != 1 || publisher.announced[0] != "ws-1/user-1" {
		t.Fatalf("announced = %v", publisher.announced)
	}
}

func TestPresenceService_RefusesWhatTheDomainRefuses(t *testing.T) {
	store := &fakePresenceSettingsStore{}
	publisher := &fakePresencePublisher{}
	presence := service.NewPresenceService(store, publisher)
	cases := map[string]struct {
		state   string
		expires time.Time
	}{
		"unknown state":  {"invisible", time.Now().Add(time.Hour)},
		"offline":        {"offline", time.Now().Add(time.Hour)},
		"past expiry":    {"busy", time.Now().Add(-time.Minute)},
		"distant expiry": {"busy", time.Now().Add(40 * 24 * time.Hour)},
	}
	for name, tc := range cases {
		if _, err := presence.SetManual(context.Background(), "ws-1", "user-1", tc.state, tc.expires); !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("%s: err = %v, want ErrInvalidInput", name, err)
		}
	}
	if store.calls != 0 || len(publisher.announced) != 0 {
		t.Fatal("a refused request reached the store or the realtime layer")
	}
}

func TestPresenceService_AFailedWriteAnnouncesNothing(t *testing.T) {
	store := &fakePresenceSettingsStore{setErr: domain.ErrForbidden, clearErr: errors.New("down")}
	publisher := &fakePresencePublisher{}
	presence := service.NewPresenceService(store, publisher)

	if _, err := presence.SetManual(context.Background(), "ws-1", "user-1", "busy", time.Now().Add(time.Hour)); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("set err = %v", err)
	}
	if err := presence.ClearManual(context.Background(), "ws-1", "user-1"); err == nil {
		t.Fatal("clear err was swallowed")
	}
	if len(publisher.announced) != 0 {
		t.Fatalf("announced after a failure: %v", publisher.announced)
	}
}

func TestPresenceService_ClearAnnouncesAndManualReads(t *testing.T) {
	store := &fakePresenceSettingsStore{manual: domain.PresenceOverride{State: domain.PresenceManualBusy}}
	publisher := &fakePresencePublisher{}
	presence := service.NewPresenceService(store, publisher)

	if err := presence.ClearManual(context.Background(), "ws-1", "user-1"); err != nil {
		t.Fatalf("ClearManual: %v", err)
	}
	if len(publisher.announced) != 1 {
		t.Fatalf("announced = %v", publisher.announced)
	}
	if got, _ := presence.Manual(context.Background(), "ws-1", "user-1"); got.State != domain.PresenceManualBusy {
		t.Fatalf("Manual = %+v", got)
	}
	// Without a publisher the write still succeeds.
	quiet := service.NewPresenceService(store, nil)
	if err := quiet.ClearManual(context.Background(), "ws-1", "user-1"); err != nil {
		t.Fatalf("ClearManual without publisher: %v", err)
	}
}

// A write whose presence facts change cannot be opened is not made (issue
// #798): the store is never called, nothing is announced, and the caller learns
// it is unavailable.
func TestPresenceService_RefusedBoundaryChangesNothing(t *testing.T) {
	store := &fakePresenceSettingsStore{}
	publisher := &fakePresencePublisher{refuse: domain.ErrPresenceFactsUnavailable}
	presence := service.NewPresenceService(store, publisher)
	_, setErr := presence.SetManual(context.Background(), "ws-1", "user-1", "dnd", time.Now().Add(time.Hour))
	clearErr := presence.ClearManual(context.Background(), "ws-1", "user-1")
	for _, err := range []error{setErr, clearErr} {
		if !errors.Is(err, domain.ErrPresenceFactsUnavailable) {
			t.Fatalf("err = %v", err)
		}
	}
	if store.calls != 0 || len(publisher.announced) != 0 {
		t.Fatalf("a refused write reached the store (%d) or was announced (%v)", store.calls, publisher.announced)
	}
}

func TestPresenceService_WritesRunInsideTheBoundary(t *testing.T) {
	store := &fakePresenceSettingsStore{}
	publisher := &fakePresencePublisher{}
	presence := service.NewPresenceService(store, publisher)
	if _, err := presence.SetManual(context.Background(), "ws-1", "user-1", "dnd", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := presence.ClearManual(context.Background(), "ws-1", "user-1"); err != nil {
		t.Fatal(err)
	}
	if len(publisher.changes) != 2 || publisher.changes[0] != "user-1" {
		t.Fatalf("boundary opened for %v", publisher.changes)
	}
}
