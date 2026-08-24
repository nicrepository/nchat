package domain_test

import (
	"errors"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

func TestMaxCallParticipantProfileIDsIsAPerRequestBatchCap(t *testing.T) {
	if domain.MaxCallParticipantProfileIDs < 1 {
		t.Fatalf("MaxCallParticipantProfileIDs = %d, must allow at least one id", domain.MaxCallParticipantProfileIDs)
	}
	if domain.MaxCallParticipantProfileIDs > 200 {
		t.Fatalf("MaxCallParticipantProfileIDs = %d, larger than a call room's realistic size", domain.MaxCallParticipantProfileIDs)
	}
}

func TestCallParticipantProfileErrorsWrapInvalidInput(t *testing.T) {
	for name, err := range map[string]error{
		"too many":       domain.ErrTooManyCallParticipantsRequested,
		"none requested": domain.ErrNoCallParticipantsRequested,
	} {
		if !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("%s: want wrapped ErrInvalidInput, got %v", name, err)
		}
	}
}
