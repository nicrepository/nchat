package domain

import "testing"

func TestCallTypesAndStatuses(t *testing.T) {
	for _, callType := range []CallType{CallTypeAudio, CallTypeVideo} {
		if !callType.Valid() {
			t.Fatalf("expected valid call type %q", callType)
		}
	}
	if CallType("screen").Valid() {
		t.Fatal("unexpected valid call type")
	}

	for _, status := range []CallStatus{
		CallStatusRinging,
		CallStatusActive,
		CallStatusDeclined,
		CallStatusCancelled,
		CallStatusTimedOut,
		CallStatusEnded,
	} {
		if !status.Valid() {
			t.Fatalf("expected valid status %q", status)
		}
	}
}

func TestCallTerminalAndParticipant(t *testing.T) {
	call := Call{CallerID: "caller", CalleeID: "callee"}
	if !call.IsParticipant("caller") || !call.IsParticipant("callee") {
		t.Fatal("participants must be recognized")
	}
	if call.IsParticipant("other") {
		t.Fatal("non-participant must be rejected")
	}

	for _, status := range []CallStatus{
		CallStatusDeclined,
		CallStatusCancelled,
		CallStatusTimedOut,
		CallStatusEnded,
	} {
		if !status.Terminal() {
			t.Fatalf("expected terminal status %q", status)
		}
	}
	for _, status := range []CallStatus{CallStatusRinging, CallStatusActive} {
		if status.Terminal() {
			t.Fatalf("expected non-terminal status %q", status)
		}
	}
}

func TestCallTargetsKeepDirectAndResourceSemanticsSeparate(t *testing.T) {
	for _, target := range []CallTargetType{CallTargetUser, CallTargetChannel, CallTargetDM} {
		if !target.Valid() {
			t.Fatalf("expected valid call target %q", target)
		}
	}
	if CallTargetType("workspace").Valid() {
		t.Fatal("unexpected valid call target")
	}

	direct := Call{CallerID: "caller", CalleeID: "callee", TargetType: CallTargetUser}
	resource := Call{CallerID: "caller", TargetType: CallTargetChannel, TargetID: "channel"}
	if !direct.IsDirect() || direct.IsResource() || !direct.IsParticipant("callee") {
		t.Fatal("direct call semantics changed")
	}
	if resource.IsDirect() || !resource.IsResource() || resource.IsParticipant("channel") {
		t.Fatal("resource identifiers must never become user participants")
	}
}
