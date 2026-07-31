package ws

import (
	"context"
	"encoding/json"
	"testing"
)

func TestCallTerminalEventsNotifyBothParticipantsOnce(t *testing.T) {
	for _, test := range []struct {
		status string
		event  EventType
	}{
		{status: "declined", event: EventTypeCallDeclined},
		{status: "cancelled", event: EventTypeCallCancelled},
		{status: "timed_out", event: EventTypeCallTimedOut},
		{status: "ended", event: EventTypeCallEnded},
	} {
		t.Run(test.status, func(t *testing.T) {
			hub := NewHub(&fakeAuthorizer{}, newTestLogger(), NopBus{}, "call-terminal-"+test.status)
			t.Cleanup(hub.Shutdown)
			caller := newClient("caller-"+test.status, callTestCaller, callTestWorkspace, &fakeSender{})
			callee := newClient("callee-"+test.status, callTestCallee, callTestWorkspace, &fakeSender{})
			if !hub.Register(caller) || !hub.Register(callee) {
				t.Fatal("register participants")
			}
			call := callProtocolCall(callEventStatus(test.event), 2)
			hub.PublishCall(context.Background(), call)
			for _, participant := range []*Client{caller, callee} {
				waitForOutbox(participant, 1)
				var event Event
				if err := json.Unmarshal(<-participant.outbox, &event); err != nil {
					t.Fatal(err)
				}
				if event.Type != test.event {
					t.Fatalf("event=%s want=%s", event.Type, test.event)
				}
			}
		})
	}
}
