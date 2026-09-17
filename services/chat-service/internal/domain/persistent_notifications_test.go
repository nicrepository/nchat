package domain_test

import (
	"errors"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// Issue #825: the one rule binding the reminder policy to the priority axis.
//
// #820 says persistent notifications are available only on an urgent message.
// The rule is asserted here, in the domain, because both create paths and the
// database all defer to it: a version of this check that lived in the handler
// would be a check an internal caller could walk past.

func TestPersistentNotificationsRequireAnUrgentMessage(t *testing.T) {
	for _, priority := range []domain.MessagePriority{
		domain.MessagePriorityStandard,
		domain.MessagePriorityImportant,
		// The empty value is the absence of a stated priority, which
		// NormalizeMessagePriority resolves to standard. Asking for reminders
		// without stating a priority is therefore asking for them on a standard
		// message, and is refused as one.
		"",
	} {
		err := domain.ValidatePersistentNotifications(priority, true)
		if !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("priority %q: err = %v, want ErrInvalidInput", priority, err)
		}
		if !errors.Is(err, domain.ErrPersistentNotificationsRequireUrgent) {
			t.Fatalf("priority %q: err = %v, want the specific refusal", priority, err)
		}
	}
}

func TestPersistentNotificationsAreAllowedOnAnUrgentMessage(t *testing.T) {
	if err := domain.ValidatePersistentNotifications(domain.MessagePriorityUrgent, true); err != nil {
		t.Fatalf("urgent + persistent must be allowed, got %v", err)
	}
}

// Not asking for reminders is never a refusal, whatever the priority. The rule
// is about the combination, not about the priority on its own, and a standard
// message is the overwhelming majority of the traffic this runs on.
func TestNotAskingForPersistentNotificationsIsAlwaysAllowed(t *testing.T) {
	for _, priority := range []domain.MessagePriority{
		"", domain.MessagePriorityStandard, domain.MessagePriorityImportant, domain.MessagePriorityUrgent,
	} {
		if err := domain.ValidatePersistentNotifications(priority, false); err != nil {
			t.Fatalf("priority %q without reminders: %v", priority, err)
		}
	}
}

// The refusal names the rule and nothing about the message it refused. A message
// this endpoint declined must not leak its conversation, its recipients or its
// body through the error text.
func TestPersistentNotificationRefusalCarriesNoMessageDetail(t *testing.T) {
	err := domain.ValidatePersistentNotifications(domain.MessagePriorityImportant, true)
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if got := err.Error(); got != "invalid input: persistent notifications require an urgent message" {
		t.Fatalf("message = %q", got)
	}
}
