package ws

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// A call change announces itself to presence from inside its own transaction
// (issue #798, HIGH-B/HIGH-C): OpenPresenceFacts moves every named person's
// revision and holds their compositions until it is closed, and an
// announcement that cannot be made refuses the call command.

func callFactsHub(t *testing.T, handler CallHandler) (*Hub, *faultStore, *Client) {
	t.Helper()
	hub := NewHub(&fakeAuthorizer{}, newTestLogger(), NopBus{}, "call-facts", WithCallHandler(handler))
	t.Cleanup(hub.Shutdown)
	client := newClient("callee-client", callTestCallee, callTestWorkspace, &fakeSender{})
	if !hub.Register(client) {
		t.Fatal("register")
	}
	store := &faultStore{UserPresenceStore: hub.userPresence(), fail: map[string]error{}, before: map[string]func(){}}
	hub.userStore = store
	return hub, store, client
}

func TestCallFacts_AnOpenAnnouncementHoldsEveryNamedPerson(t *testing.T) {
	hub, store, _ := callFactsHub(t, &fakeCallHandler{})
	ctx := context.Background()
	available := domain.EffectivePresence{Availability: domain.PresenceAvailable}
	before := revisionIn(t, store, callTestWorkspace, "caller-1")

	factsCtx, closeFacts, err := hub.OpenPresenceFacts(ctx, callTestWorkspace, []string{"caller-1", "joiner-1"})
	if err != nil {
		t.Fatal(err)
	}
	if deadline, ok := factsCtx.Deadline(); !ok || deadline.After(time.Now().Add(presenceFactsMutationTimeout)) {
		t.Fatalf("the rest of the change is not bounded by the mutation timeout: %v %v", deadline, ok)
	}
	for _, userID := range []string{"caller-1", "joiner-1"} {
		fresh := revisionIn(t, store, callTestWorkspace, userID)
		if _, outcome, _ := store.Project(ctx, callTestWorkspace, userID, commitOf(available, fresh, time.Now())); outcome != projectionConflict {
			t.Fatalf("%s committed while the announcement was open: %v", userID, outcome)
		}
	}
	if revisionIn(t, store, callTestWorkspace, "caller-1") == before {
		t.Fatal("the announcement did not move the revision")
	}
	closeFacts()
	fresh := revisionIn(t, store, callTestWorkspace, "caller-1")
	if _, outcome, _ := store.Project(ctx, callTestWorkspace, "caller-1", commitOf(available, fresh, time.Now())); outcome != projectionApplied {
		t.Fatalf("after the announcement closed = %v", outcome)
	}
}

// refusingCallHandler is a call store whose announcement could not be made.
type refusingCallHandler struct{ *fakeCallHandler }

func (refusingCallHandler) TransitionCall(context.Context, string, string, string, ClientMessageType) (domain.Call, error) {
	return domain.Call{}, domain.ErrPresenceFactsUnavailable
}

func TestCallFacts_AnUnopenableAnnouncementRefusesTheCommand(t *testing.T) {
	hub, store, _ := callFactsHub(t, &fakeCallHandler{})
	store.set("begin", errors.New("valkey unavailable"))
	if _, _, err := hub.OpenPresenceFacts(context.Background(), callTestWorkspace, []string{"caller-1"}); !errors.Is(err, domain.ErrPresenceFactsUnavailable) {
		t.Fatalf("an unannounceable change = %v", err)
	}

	refusing, _, client := callFactsHub(t, refusingCallHandler{&fakeCallHandler{}})
	err := refusing.handleClientMessage(context.Background(), client, ClientMessage{Type: ClientMessageTypeCallEnd, CallID: callTestID})
	if !errors.Is(err, ErrCallFeatureDisabled) {
		t.Fatalf("err = %v; want refused as unavailable", err)
	}
	if !handleCallClientError(client, ClientMessageTypeCallEnd, callTestID, "", err) {
		t.Fatal("the refusal was not answered as a call error")
	}
}
