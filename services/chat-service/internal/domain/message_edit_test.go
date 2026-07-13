package domain

import (
	"errors"
	"testing"
	"time"
)

func TestValidateMessageEdit(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	window := 900
	base := Message{SenderID: "author", Status: MessageStatusActive, CreatedAt: now.Add(-900 * time.Second)}

	for _, tt := range []struct {
		name      string
		message   Message
		requester string
		window    *int
		want      error
	}{
		{name: "exact boundary included", message: base, requester: "author", window: &window},
		{name: "no limit", message: Message{SenderID: "author", Status: MessageStatusActive, CreatedAt: now.Add(-24 * time.Hour)}, requester: "author"},
		{name: "outside window", message: Message{SenderID: "author", Status: MessageStatusActive, CreatedAt: now.Add(-901 * time.Second)}, requester: "author", window: &window, want: ErrEditWindowExpired},
		{name: "not author", message: base, requester: "other", window: &window, want: ErrEditForbidden},
		{name: "deleted", message: Message{SenderID: "author", Status: MessageStatusDeleted, CreatedAt: base.CreatedAt}, requester: "author", window: &window, want: ErrEditForbidden},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateMessageEdit(tt.message, tt.requester, tt.window, now)
			if !errors.Is(err, tt.want) {
				t.Fatalf("ValidateMessageEdit() error = %v, want %v", err, tt.want)
			}
		})
	}
}
