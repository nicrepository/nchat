package service_test

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"testing"

	"github.com/nicrepository/nchat/services/notification-service/internal/domain"
	"github.com/nicrepository/nchat/services/notification-service/internal/service"
)

// Issue #745: the use cases, driven against a fake store.
//
// What is under test is the ordering and the delegation: that a registration is
// validated before anything is written, that the principal the store receives is
// the one the caller was resolved to, and that a provider status reaches the
// store already classified.

type recordedUpsert struct {
	principal    domain.Principal
	registration domain.Registration
}

// recordedDelivery is one call to the store: the outcome and the generation it
// was attributed to. Both are asserted, because attributing a result to the
// wrong generation is exactly the defect the generation exists to prevent.
type recordedDelivery struct {
	subscriptionID string
	generation     int64
	result         domain.DeliveryResult
}

type fakeStore struct {
	upserts   []recordedUpsert
	listed    []domain.Principal
	disabled  []string
	delivered []recordedDelivery

	subscription domain.PushSubscription
	applied      bool
	err          error
}

func (f *fakeStore) Upsert(
	_ context.Context, principal domain.Principal, registration domain.Registration,
) (domain.PushSubscription, error) {
	f.upserts = append(f.upserts, recordedUpsert{principal, registration})
	return f.subscription, f.err
}

func (f *fakeStore) List(
	_ context.Context, principal domain.Principal,
) ([]domain.PushSubscription, error) {
	f.listed = append(f.listed, principal)
	return []domain.PushSubscription{f.subscription}, f.err
}

func (f *fakeStore) Disable(_ context.Context, _ domain.Principal, subscriptionID string) error {
	f.disabled = append(f.disabled, subscriptionID)
	return f.err
}

func (f *fakeStore) RecordDelivery(
	_ context.Context, subscriptionID string, generation int64, result domain.DeliveryResult,
) (bool, error) {
	f.delivered = append(f.delivered, recordedDelivery{subscriptionID, generation, result})
	return f.applied, f.err
}

// encodedKey builds a structurally valid key of the right size. Built rather
// than pasted: the length is the property the domain checks, and no fixture in
// this repository should read as a real Web Push credential.
func encodedKey(size int, first byte) string {
	raw := make([]byte, size)
	raw[0] = first
	for i := 1; i < size; i++ {
		raw[i] = byte(i)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func validRegistration() domain.Registration {
	return domain.Registration{
		DeviceID: "device-1",
		Endpoint: "https://push.example.com/subscription/abc123",
		P256dh:   encodedKey(65, 0x04),
		Auth:     encodedKey(16, 0x01),
	}
}

func TestRegisterPassesTheResolvedPrincipalToTheStore(t *testing.T) {
	store := &fakeStore{subscription: domain.PushSubscription{ID: "sub-1"}}
	principal := domain.Principal{UserID: "user-1", WorkspaceID: "ws-1"}

	if _, err := service.NewPushSubscriptions(store).
		Register(context.Background(), principal, validRegistration()); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if len(store.upserts) != 1 || store.upserts[0].principal != principal {
		t.Fatalf("store received %+v", store.upserts)
	}
}

// Validation precedes the write and is not merely reported after it. A
// registration the domain refuses must never reach a statement, or a malformed
// key would surface as a constraint violation the caller has to interpret.
func TestRegisterRefusesAnInvalidRegistrationWithoutWriting(t *testing.T) {
	store := &fakeStore{}
	registration := validRegistration()
	registration.Endpoint = "http://push.example.com/subscription/abc123"

	_, err := service.NewPushSubscriptions(store).Register(
		context.Background(), domain.Principal{UserID: "user-1", WorkspaceID: "ws-1"}, registration)
	if !errors.Is(err, domain.ErrInvalidRegistration) {
		t.Fatalf("Register = %v, want ErrInvalidRegistration", err)
	}
	if len(store.upserts) != 0 {
		t.Fatalf("store was written to: %+v", store.upserts)
	}
}

func TestRegisterReportsAStoreFailure(t *testing.T) {
	store := &fakeStore{err: domain.ErrEndpointConflict}

	_, err := service.NewPushSubscriptions(store).Register(
		context.Background(), domain.Principal{UserID: "user-1", WorkspaceID: "ws-1"},
		validRegistration())
	if !errors.Is(err, domain.ErrEndpointConflict) {
		t.Fatalf("Register = %v, want ErrEndpointConflict", err)
	}
}

func TestListDelegatesWithThePrincipal(t *testing.T) {
	store := &fakeStore{subscription: domain.PushSubscription{ID: "sub-1"}}
	principal := domain.Principal{UserID: "user-1", WorkspaceID: "ws-1"}

	subscriptions, err := service.NewPushSubscriptions(store).List(context.Background(), principal)
	if err != nil || len(subscriptions) != 1 {
		t.Fatalf("List = %v, %v", subscriptions, err)
	}
	if len(store.listed) != 1 || store.listed[0] != principal {
		t.Fatalf("store received %+v", store.listed)
	}
}

func TestDisableDelegates(t *testing.T) {
	store := &fakeStore{}

	if err := service.NewPushSubscriptions(store).Disable(
		context.Background(), domain.Principal{UserID: "user-1", WorkspaceID: "ws-1"}, "sub-1",
	); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if len(store.disabled) != 1 || store.disabled[0] != "sub-1" {
		t.Fatalf("store received %+v", store.disabled)
	}
}

// The lifecycle rule, seen from the use case: 404 and 410 retire a subscription
// and nothing else does. A 429 or a 5xx reaching the store as anything but a
// transient result would unsubscribe a real person during a provider incident.
func TestRecordDeliveryStatusClassifiesBeforeWriting(t *testing.T) {
	cases := map[int]domain.DeliveryResult{
		http.StatusCreated:             {Outcome: domain.OutcomeSucceeded},
		http.StatusNotFound:            {Outcome: domain.OutcomeInvalidated, Reason: domain.ReasonNotFound},
		http.StatusGone:                {Outcome: domain.OutcomeInvalidated, Reason: domain.ReasonGone},
		http.StatusTooManyRequests:     {Outcome: domain.OutcomeTransient},
		http.StatusInternalServerError: {Outcome: domain.OutcomeTransient},
		0:                              {Outcome: domain.OutcomeTransient},
	}
	for status, want := range cases {
		store := &fakeStore{applied: true}
		applied, err := service.NewPushSubscriptions(store).
			RecordDeliveryStatus(context.Background(), "sub-1", 7, status)
		if err != nil || !applied {
			t.Fatalf("RecordDeliveryStatus(%d) = %v, %v", status, applied, err)
		}
		if len(store.delivered) != 1 ||
			store.delivered[0] != (recordedDelivery{"sub-1", 7, want}) {
			t.Fatalf("status %d recorded %+v, want %+v", status, store.delivered, want)
		}
	}
}

// The generation the caller captured is carried through untouched. The service
// must not resolve, refresh or infer it: doing so would let a result be
// attributed to the endpoint that is on file now rather than the one the attempt
// was actually made against.
func TestRecordDeliveryStatusCarriesTheAttemptsGeneration(t *testing.T) {
	store := &fakeStore{applied: false}

	applied, err := service.NewPushSubscriptions(store).
		RecordDeliveryStatus(context.Background(), "sub-1", 3, http.StatusGone)
	if err != nil {
		t.Fatalf("RecordDeliveryStatus: %v", err)
	}
	// A stale result is an ordinary outcome, reported as false and never as an
	// error: there is nothing for the caller to log or retry.
	if applied {
		t.Fatal("a stale result was reported as applied")
	}
	if len(store.delivered) != 1 || store.delivered[0].generation != 3 {
		t.Fatalf("store saw %+v, want generation 3", store.delivered)
	}
}

func TestRecordDeliveryStatusReportsAStoreFailure(t *testing.T) {
	store := &fakeStore{err: errors.New("connection refused")}

	_, err := service.NewPushSubscriptions(store).
		RecordDeliveryStatus(context.Background(), "sub-1", 1, http.StatusOK)
	if err == nil {
		t.Fatal("RecordDeliveryStatus = nil, want the database failure")
	}
}
