package storage_test

import (
	"errors"
	"testing"

	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// The per-URL stores of issue #807 answer empty input without a round trip,
// and surface a failed round trip as an error rather than as "no rows".

func TestLinkTargetStoresAnswerEmptyInputWithoutTheDatabase(t *testing.T) {
	mock := newMock(t) // no expectations: any query would fail the test
	store := storage.NewPGXMessageStore(mock)
	ctx := t.Context()

	if targets, err := store.LoadLinkTargets(ctx, nil); err != nil || len(targets) != 0 {
		t.Fatalf("LoadLinkTargets: %v %v", targets, err)
	}
	if bodies, err := store.LoadMessageBodies(ctx, nil); err != nil || len(bodies) != 0 {
		t.Fatalf("LoadMessageBodies: %v %v", bodies, err)
	}
	if err := store.QueueLinkPreviews(ctx, "", []string{"https://a.example/"}); err != nil {
		t.Fatalf("QueueLinkPreviews without a workspace: %v", err)
	}
	if jobs, err := store.ClaimDueLinkPreviews(ctx, 0); err != nil || jobs != nil {
		t.Fatalf("ClaimDueLinkPreviews(0): %v %v", jobs, err)
	}
	if fanouts, err := store.ClaimDueLinkFanouts(ctx, 0); err != nil || fanouts != nil {
		t.Fatalf("ClaimDueLinkFanouts(0): %v %v", fanouts, err)
	}
	if previews, err := store.LoadLinkPreviews(ctx, "ws", nil); err != nil || len(previews) != 0 {
		t.Fatalf("LoadLinkPreviews: %v %v", previews, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestLinkTargetStoresSurfaceQueryFailures(t *testing.T) {
	boom := errors.New("boom")
	const url = "https://a.example/"
	// pgxmock matches argument counts strictly, so each case says how many it passes.
	anyArgs := func(n int) []any {
		args := make([]any, n)
		for i := range args {
			args[i] = pgxmock.AnyArg()
		}
		return args
	}
	for name, tc := range map[string]struct {
		args int
		call func(*storage.PGXMessageStore) error
	}{
		"LoadLinkTargets": {2, func(s *storage.PGXMessageStore) error {
			_, err := s.LoadLinkTargets(t.Context(), []string{url})
			return err
		}},
		"TerminalizeExpiredLinkScans": {2, func(s *storage.PGXMessageStore) error {
			_, err := s.TerminalizeExpiredLinkScans(t.Context())
			return err
		}},
		"MessagesReferencingLink": {4, func(s *storage.PGXMessageStore) error {
			_, err := s.MessagesReferencingLink(t.Context(), url, "", "", 0)
			return err
		}},
		"LinkWorkspacesReferencing": {1, func(s *storage.PGXMessageStore) error {
			_, err := s.LinkWorkspacesReferencing(t.Context(), url)
			return err
		}},
		"LoadMessageBodies": {1, func(s *storage.PGXMessageStore) error {
			_, err := s.LoadMessageBodies(t.Context(), []string{"m1"})
			return err
		}},
		"ClaimDueLinkPreviews": {4, func(s *storage.PGXMessageStore) error {
			_, err := s.ClaimDueLinkPreviews(t.Context(), 5)
			return err
		}},
		"RevokeLinkPreviews": {2, func(s *storage.PGXMessageStore) error {
			_, err := s.RevokeLinkPreviews(t.Context(), url)
			return err
		}},
		"LoadLinkPreviews": {2, func(s *storage.PGXMessageStore) error {
			_, err := s.LoadLinkPreviews(t.Context(), "ws", []string{url})
			return err
		}},
		"LinkPreviewImage": {4, func(s *storage.PGXMessageStore) error {
			_, err := s.LinkPreviewImage(t.Context(), "ws", "u", "p")
			return err
		}},
		"LinkPreviewBacklog": {0, func(s *storage.PGXMessageStore) error {
			_, err := s.LinkPreviewBacklog(t.Context())
			return err
		}},
		"BeginLinkFanout": {4, func(s *storage.PGXMessageStore) error {
			_, err := s.BeginLinkFanout(t.Context(), url, "", storage.LinkFanoutKindTarget)
			return err
		}},
		"ClaimDueLinkFanouts": {3, func(s *storage.PGXMessageStore) error {
			_, err := s.ClaimDueLinkFanouts(t.Context(), 5)
			return err
		}},
	} {
		t.Run(name, func(t *testing.T) {
			mock := newMock(t)
			mock.ExpectQuery(".*").WithArgs(anyArgs(tc.args)...).WillReturnError(boom)
			if err := tc.call(storage.NewPGXMessageStore(mock)); !errors.Is(err, boom) {
				t.Fatalf("err = %v, want the query failure", err)
			}
		})
	}

	t.Run("QueueLinkPreviews", func(t *testing.T) {
		mock := newMock(t)
		mock.ExpectExec(".*").WithArgs(anyArgs(3)...).WillReturnError(boom)
		if err := storage.NewPGXMessageStore(mock).QueueLinkPreviews(t.Context(), "ws", []string{url}); !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("AdvanceLinkFanout", func(t *testing.T) {
		mock := newMock(t)
		mock.ExpectExec(".*").WithArgs(anyArgs(3)...).WillReturnError(boom)
		if err := storage.NewPGXMessageStore(mock).AdvanceLinkFanout(t.Context(), storage.LinkFanout{ID: "f", ClaimID: "c"}, "m"); !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("FinishLinkFanout", func(t *testing.T) {
		mock := newMock(t)
		mock.ExpectExec(".*").WithArgs(anyArgs(2)...).WillReturnError(boom)
		if err := storage.NewPGXMessageStore(mock).FinishLinkFanout(t.Context(), storage.LinkFanout{ID: "f", ClaimID: "c"}); !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("RecordLinkTargetTerminal", func(t *testing.T) {
		mock := newMock(t)
		mock.ExpectExec(".*").WithArgs(anyArgs(2)...).WillReturnError(boom)
		if err := storage.NewPGXMessageStore(mock).RecordLinkTargetTerminal(t.Context(), url, storage.TerminalReasonSensitive); !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
	})
}
