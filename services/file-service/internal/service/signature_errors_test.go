package service

import (
	"context"
	"errors"
	"testing"

	"github.com/nicrepository/nchat/services/file-service/internal/scanner"
)

func TestGuardianBodyTimeoutKeepsItsOperationalCategory(t *testing.T) {
	for cause, want := range map[error]string{
		context.DeadlineExceeded: scanErrorTimeout,
		context.Canceled:         scanErrorCanceled,
	} {
		// A bounded response read can have both a protocol wrapper and a context cause.
		if got := scanErrorKind(errors.Join(scanner.ErrProtocol, cause)); got != want {
			t.Fatalf("category=%s want=%s", got, want)
		}
	}
}
