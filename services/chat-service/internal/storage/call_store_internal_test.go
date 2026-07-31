package storage

import (
	"errors"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

func TestAuthorizeCallTransitionCoversActorsStatesAndIdempotency(t *testing.T) {
	call := domain.Call{CallerID: "caller", CalleeID: "callee"}
	tests := []struct {
		name       string
		status     domain.CallStatus
		actor      string
		action     CallAction
		want       domain.CallStatus
		idempotent bool
		wantErr    error
	}{
		{name: "accept", status: domain.CallStatusRinging, actor: "callee", action: CallActionAccept, want: domain.CallStatusActive},
		{name: "duplicate accept", status: domain.CallStatusActive, actor: "callee", action: CallActionAccept, want: domain.CallStatusActive, idempotent: true},
		{name: "decline", status: domain.CallStatusRinging, actor: "callee", action: CallActionDecline, want: domain.CallStatusDeclined},
		{name: "duplicate decline", status: domain.CallStatusDeclined, actor: "callee", action: CallActionDecline, want: domain.CallStatusDeclined, idempotent: true},
		{name: "cancel", status: domain.CallStatusRinging, actor: "caller", action: CallActionCancel, want: domain.CallStatusCancelled},
		{name: "duplicate cancel", status: domain.CallStatusCancelled, actor: "caller", action: CallActionCancel, want: domain.CallStatusCancelled, idempotent: true},
		{name: "end caller", status: domain.CallStatusActive, actor: "caller", action: CallActionEnd, want: domain.CallStatusEnded},
		{name: "end callee", status: domain.CallStatusActive, actor: "callee", action: CallActionEnd, want: domain.CallStatusEnded},
		{name: "duplicate end", status: domain.CallStatusEnded, actor: "caller", action: CallActionEnd, want: domain.CallStatusEnded, idempotent: true},
		{name: "caller cannot accept", status: domain.CallStatusRinging, actor: "caller", action: CallActionAccept, wantErr: domain.ErrNotFound},
		{name: "callee cannot cancel", status: domain.CallStatusRinging, actor: "callee", action: CallActionCancel, wantErr: domain.ErrNotFound},
		{name: "accept after timeout", status: domain.CallStatusTimedOut, actor: "callee", action: CallActionAccept, wantErr: domain.ErrConflict},
		{name: "timeout cannot end", status: domain.CallStatusTimedOut, actor: "caller", action: CallActionEnd, wantErr: domain.ErrConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			call.Status = test.status
			got, idempotent, err := authorizeCallTransition(call, test.actor, test.action)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
			if got != test.want || idempotent != test.idempotent {
				t.Fatalf("got status=%q idempotent=%v", got, idempotent)
			}
		})
	}
}
