package domain

import (
	"errors"
	"testing"
)

// The three values are the whole vocabulary (issue #821). The empty string is
// deliberately not one of them: absence is a question NormalizeMessagePriority
// answers, and Valid answers a different one — "is this stated value a priority
// at all".
func TestMessagePriority_Valid(t *testing.T) {
	for _, tt := range []struct {
		priority MessagePriority
		want     bool
	}{
		{priority: MessagePriorityStandard, want: true},
		{priority: MessagePriorityImportant, want: true},
		{priority: MessagePriorityUrgent, want: true},
		{priority: "", want: false},
		{priority: "critical", want: false},
		{priority: "URGENT", want: false},
		{priority: "urgent ", want: false},
		{priority: "high", want: false},
	} {
		t.Run(string(tt.priority), func(t *testing.T) {
			if got := tt.priority.Valid(); got != tt.want {
				t.Fatalf("MessagePriority(%q).Valid() = %v, want %v", tt.priority, got, tt.want)
			}
		})
	}
}

// assertNormalizes is the positive half of NormalizeMessagePriority's contract,
// stated once so each case below is one line of intent rather than four of
// bookkeeping.
func assertNormalizes(t *testing.T, priority, want MessagePriority) {
	t.Helper()
	got, err := NormalizeMessagePriority(priority)
	if err != nil {
		t.Fatalf("NormalizeMessagePriority(%q): %v", priority, err)
	}
	if got != want {
		t.Fatalf("NormalizeMessagePriority(%q) = %q, want %q", priority, got, want)
	}
}

// assertRefuses is the negative half. A refusal must also yield no value: a
// caller that ignored the error must not find a usable priority waiting for it.
func assertRefuses(t *testing.T, priority MessagePriority) {
	t.Helper()
	got, err := NormalizeMessagePriority(priority)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("NormalizeMessagePriority(%q) error = %v, want ErrInvalidInput", priority, err)
	}
	if got != "" {
		t.Fatalf("a refused priority must not yield a value, got %q", got)
	}
}

// The domain's rule for an absent priority, which is what an internal caller
// that states nothing gets.
//
// This is deliberately NOT the HTTP contract for an empty `"priority": ""`.
// The boundary decides presence before it calls this — see
// parseCreateMessagePriority — because only the boundary can still tell a field
// the client left out from one they filled with an empty string. Here there is
// no request and no field, only a caller that named no priority.
func TestNormalizeMessagePriority_AbsentDefaultsToStandard(t *testing.T) {
	assertNormalizes(t, "", MessagePriorityStandard)
}

func TestNormalizeMessagePriority_AcceptsTheThreeDeclaredValues(t *testing.T) {
	for _, priority := range []MessagePriority{
		MessagePriorityStandard,
		MessagePriorityImportant,
		MessagePriorityUrgent,
	} {
		t.Run(string(priority), func(t *testing.T) {
			assertNormalizes(t, priority, priority)
		})
	}
}

// Anything else is an error, never a quiet demotion to standard.
func TestNormalizeMessagePriority_RejectsAnythingElse(t *testing.T) {
	for name, priority := range map[string]MessagePriority{
		"unknown word":              "critical",
		"wrong case":                "Urgent",
		"notification vocabulary":   "high",
		"trailing whitespace":       "urgent ",
		"sql injection attempt":     "urgent'); DROP TABLE chat.messages;--",
		"the three joined together": "standard,important,urgent",
	} {
		t.Run(name, func(t *testing.T) {
			assertRefuses(t, priority)
		})
	}
}

// OrStandard is the reader's half of the same rule: a Message built by a
// projection that does not carry the column must still serialise a priority,
// and the safe filling is the one that claims nothing.
func TestMessagePriority_OrStandard(t *testing.T) {
	for _, tt := range []struct {
		priority MessagePriority
		want     MessagePriority
	}{
		{priority: "", want: MessagePriorityStandard},
		{priority: MessagePriorityStandard, want: MessagePriorityStandard},
		{priority: MessagePriorityImportant, want: MessagePriorityImportant},
		{priority: MessagePriorityUrgent, want: MessagePriorityUrgent},
	} {
		t.Run(string(tt.priority), func(t *testing.T) {
			if got := tt.priority.OrStandard(); got != tt.want {
				t.Fatalf("MessagePriority(%q).OrStandard() = %q, want %q", tt.priority, got, tt.want)
			}
		})
	}
}

// Priority is not part of the edit decision. Editing is about authorship, state
// and the edit window; a message's priority neither unlocks nor blocks it, so
// an urgent message is exactly as editable as a standard one and no more.
func TestValidateMessageEdit_IgnoresPriority(t *testing.T) {
	base := Message{SenderID: "author", Kind: MessageKindUser, Status: MessageStatusActive}
	for _, priority := range []MessagePriority{MessagePriorityStandard, MessagePriorityImportant, MessagePriorityUrgent} {
		t.Run(string(priority), func(t *testing.T) {
			message := base
			message.Priority = priority
			if err := ValidateMessageEdit(message, "author", nil, message.CreatedAt); err != nil {
				t.Fatalf("author edit of a %s message: %v", priority, err)
			}
			if err := ValidateMessageEdit(message, "someone-else", nil, message.CreatedAt); !errors.Is(err, ErrEditForbidden) {
				t.Fatalf("non-author edit of a %s message = %v, want ErrEditForbidden", priority, err)
			}
		})
	}
}
