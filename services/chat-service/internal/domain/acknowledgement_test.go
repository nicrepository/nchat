package domain_test

import (
	"errors"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// Issue #824. The state machine is the whole of this file: which states exist,
// which of them are questions, and which transitions the domain will perform.
// Everything else about acknowledgement is somebody else's layer.

// declaredAcknowledgementStates is the vocabulary these tests hold the domain
// to, written once so a value added to the domain and forgotten here is a
// compile-time change rather than a silent gap.
var declaredAcknowledgementStates = []domain.AcknowledgementState{
	domain.AcknowledgementStatePending,
	domain.AcknowledgementStateAcknowledged,
	domain.AcknowledgementStateResponded,
	domain.AcknowledgementStateExpired,
	domain.AcknowledgementStateCancelled,
}

// terminalAcknowledgementStates is everything that is not pending. It is the
// set every "may not be reopened" assertion below iterates.
var terminalAcknowledgementStates = []domain.AcknowledgementState{
	domain.AcknowledgementStateAcknowledged,
	domain.AcknowledgementStateResponded,
	domain.AcknowledgementStateExpired,
	domain.AcknowledgementStateCancelled,
}

func TestAcknowledgementState_ValidAcceptsExactlyTheDeclaredStates(t *testing.T) {
	for _, state := range declaredAcknowledgementStates {
		if !state.Valid() {
			t.Errorf("%q must be a declared state", state)
		}
	}
}

// Everything else is refused, including the empty value. Empty is how a
// projection says "this viewer is not a recipient", which is a different answer
// from any state and must never be mistaken for one.
func TestAcknowledgementState_ValidRefusesEverythingElse(t *testing.T) {
	for _, state := range []domain.AcknowledgementState{
		"", "PENDING", "ACKNOWLEDGED", "acknowledge", "read", "delivered", "done", " pending",
	} {
		if state.Valid() {
			t.Errorf("%q must not be a declared state", state)
		}
	}
}

// Pending is the one unresolved state, and it is the only one.
func TestAcknowledgementState_OnlyPendingIsUnresolved(t *testing.T) {
	if domain.AcknowledgementStatePending.Resolved() {
		t.Error("pending is the state that has not been answered")
	}
	for _, state := range terminalAcknowledgementStates {
		if !state.Resolved() {
			t.Errorf("%q is terminal and must count as resolved", state)
		}
	}
}

// An undeclared value is not resolved, because it is not a state at all.
// Reporting it as resolved would let a typo silently retire a live request.
func TestAcknowledgementState_UndeclaredIsNotResolved(t *testing.T) {
	for _, state := range []domain.AcknowledgementState{"", "confirmed", "PENDING"} {
		if state.Resolved() {
			t.Errorf("%q is not a state and must not report as resolved", state)
		}
	}
}

// Every transition the issue asks for is allowed, and they are the only ones
// leaving pending.
func TestCanResolveAcknowledgement_PendingReachesEveryTerminalState(t *testing.T) {
	for _, to := range terminalAcknowledgementStates {
		if !domain.CanResolveAcknowledgement(domain.AcknowledgementStatePending, to) {
			t.Errorf("pending -> %q must be allowed", to)
		}
	}
}

// The forbidden transitions #824 enumerates, and the ones it did not: nothing
// leaves a terminal state, for any destination at all.
func TestCanResolveAcknowledgement_TerminalStatesNeverChange(t *testing.T) {
	for _, from := range terminalAcknowledgementStates {
		for _, to := range declaredAcknowledgementStates {
			if domain.CanResolveAcknowledgement(from, to) {
				t.Errorf("%q -> %q must be refused: a terminal state is final", from, to)
			}
		}
	}
}

// Nothing returns to pending — not from a terminal state, and not from pending
// itself. "Resolve" means stop being a question; re-asking is not a transition
// this domain performs.
func TestCanResolveAcknowledgement_NothingReturnsToPending(t *testing.T) {
	for _, from := range declaredAcknowledgementStates {
		if domain.CanResolveAcknowledgement(from, domain.AcknowledgementStatePending) {
			t.Errorf("%q -> pending must be refused", from)
		}
	}
}

// An undeclared destination is refused rather than treated as terminal, so a
// typo cannot resolve a request into a state nothing else understands.
func TestCanResolveAcknowledgement_RefusesUndeclaredDestinations(t *testing.T) {
	for _, to := range []domain.AcknowledgementState{"", "confirmed", "ACKNOWLEDGED", "seen"} {
		if domain.CanResolveAcknowledgement(domain.AcknowledgementStatePending, to) {
			t.Errorf("pending -> %q must be refused", to)
		}
	}
}

// Resolved() on the summary is the subtraction every caller would otherwise
// perform for themselves, and differently.
func TestAcknowledgementSummary_ResolvedIsEverythingNotPending(t *testing.T) {
	summary := domain.AcknowledgementSummary{
		Required: true, Total: 7, Pending: 2,
		Acknowledged: 4, Responded: 1,
	}
	if got := summary.Resolved(); got != 5 {
		t.Fatalf("Resolved() = %d, want 5", got)
	}
}

// A message that asked nobody resolves nobody. The zero summary is a legitimate
// answer — a message that never requested confirmation — and must not divide by
// or subtract into a negative.
func TestAcknowledgementSummary_ZeroSummaryResolvesNobody(t *testing.T) {
	if got := (domain.AcknowledgementSummary{}).Resolved(); got != 0 {
		t.Fatalf("Resolved() on an empty summary = %d, want 0", got)
	}
}

// The bound is a validation failure, so every caller that already maps
// ErrInvalidInput to 400 keeps working with no new branch — and the message
// states the public limit without revealing the conversation's real size.
func TestErrAcknowledgementRecipientsExceeded_IsInvalidInput(t *testing.T) {
	if !errors.Is(domain.ErrAcknowledgementRecipientsExceeded, domain.ErrInvalidInput) {
		t.Fatal("the recipient bound must wrap ErrInvalidInput")
	}
	if domain.MaxAcknowledgementRecipients <= 0 {
		t.Fatalf("MaxAcknowledgementRecipients = %d, want a positive bound", domain.MaxAcknowledgementRecipients)
	}
}
