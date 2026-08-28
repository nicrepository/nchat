package service_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/uploadpolicy"
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
)

// RF-32 (issue #458): the size limit an upload is judged against is the
// destination workspace's administrative policy, resolved in the same query
// that authorises the destination — not deployment configuration.
//
// These tests drive the policy through the authorizer, which is where it
// arrives in production. The policy used is the domain floor (1 MiB) rather
// than an arbitrary small number: anything below the floor is not a storable
// policy — the database CHECK refuses it and uploadpolicy.Effective resolves it
// to the default — so a 512-byte "limit" would silently test the fallback
// instead of the limit. One mebibyte keeps every buffer here trivial and still
// nowhere near the real 250 MiB default.

// policyBytes is the smallest limit an administrator can actually store.
const policyBytes = uploadpolicy.MinMaxUploadBytes

// withWorkspacePolicy points the fixture's authorizer at a workspace carrying
// maxUploadBytes, leaving the deployment ceiling wide open.
func withWorkspacePolicy(f *fixture, maxUploadBytes int64) {
	f.authorizer.result = service.AuthorizedDestination{
		ID:               testChannelID,
		WorkspaceID:      testWorkspaceID,
		MaxUploadBytes:   maxUploadBytes,
		SessionExpiresAt: time.Now().Add(time.Hour),
	}
}

func TestUploadAcceptsExactlyTheWorkspacePolicy(t *testing.T) {
	f := newFixture(t)
	withWorkspacePolicy(f, policyBytes)

	view, err := f.upload(context.Background(), bytes.NewReader(bytes.Repeat([]byte("x"), int(policyBytes))), "exact.txt")
	if err != nil {
		t.Fatalf("a file exactly at the limit must be accepted, got %v", err)
	}
	if view.Size != policyBytes {
		t.Fatalf("expected %d bytes recorded, got %d", policyBytes, view.Size)
	}
}

func TestUploadRejectsOneByteOverTheWorkspacePolicy(t *testing.T) {
	f := newFixture(t)
	withWorkspacePolicy(f, policyBytes)

	_, err := f.upload(context.Background(), bytes.NewReader(bytes.Repeat([]byte("x"), int(policyBytes+1))), "over.txt")
	if !errors.Is(err, domain.ErrTooLarge) {
		t.Fatalf("expected ErrTooLarge, got %v", err)
	}
	// The row is terminal, storage holds nothing, and the object that was
	// started is gone: an oversized upload leaves no orphan behind.
	assertCompensated(t, f)
}

// An oversized upload must leave nothing behind and must never become
// scannable.
//
// The size is not known in advance — Content-Length is not trusted and a
// chunked body has none — so the excess is discovered while the encrypted
// stream is already being written. What the requirement guarantees is the end
// state, and it is guaranteed on every path: the partial object is deleted, the
// row is terminal, and it never advances to pending_scan, which is the only
// status the antimalware worker looks at. There is no window in which a
// rejected upload is downloadable or queued.
func TestUploadOverTheLimitLeavesNoStoredObjectAndIsNeverScannable(t *testing.T) {
	f := newFixture(t)
	withWorkspacePolicy(f, policyBytes)

	_, err := f.upload(context.Background(), bytes.NewReader(bytes.Repeat([]byte("x"), int(policyBytes+1))), "over.txt")
	if !errors.Is(err, domain.ErrTooLarge) {
		t.Fatalf("expected ErrTooLarge, got %v", err)
	}
	assertCompensated(t, f)

	_, uploaded, _ := f.store.snapshot()
	for _, update := range uploaded {
		if update.Status == domain.StatusPendingScan || update.Status == domain.StatusClean {
			t.Fatalf("a rejected upload must never reach %q", update.Status)
		}
	}
}

// Rejected before the first byte reaches storage when the excess is visible
// inside the detection window: no row, no object, no cleanup to run. The cap
// here comes from the deployment ceiling, which is the only control that can be
// set below the domain floor.
func TestUploadOverATightCeilingNeverCallsStorageAtAll(t *testing.T) {
	f := newFixture(t, fixtureOptions{maxUploadBytes: 128, scanRequired: true})

	_, err := f.upload(context.Background(), bytes.NewReader(bytes.Repeat([]byte("x"), 129)), "over.txt")
	if !errors.Is(err, domain.ErrTooLarge) {
		t.Fatalf("expected ErrTooLarge, got %v", err)
	}
	assertNothingPersisted(t, f)
}

// A workspace row written before migration 000020 reads as zero. That is not
// "no limit": it resolves to the RF-32 default.
func TestUploadFallsBackToTheDefaultWhenTheWorkspaceHasNoPolicy(t *testing.T) {
	f := newFixture(t)
	withWorkspacePolicy(f, 0)

	// Small enough to pass under the default and large enough to prove the
	// budget is not zero.
	view, err := f.upload(context.Background(), bytes.NewReader(bytes.Repeat([]byte("x"), 4096)), "ok.txt")
	if err != nil {
		t.Fatalf("an absent policy must fall back to the default, got %v", err)
	}
	if view.Size != 4096 {
		t.Fatalf("expected 4096 bytes, got %d", view.Size)
	}
}

// An out-of-range value cannot come from the admin endpoint or survive the
// database CHECK, but it must not widen the limit if it ever appears.
func TestUploadIgnoresAnOutOfRangeWorkspacePolicy(t *testing.T) {
	for _, policy := range []int64{-1, uploadpolicy.MaxMaxUploadBytes + 1} {
		f := newFixture(t)
		withWorkspacePolicy(f, policy)

		view, err := f.upload(context.Background(), bytes.NewReader(bytes.Repeat([]byte("x"), 4096)), "ok.txt")
		if err != nil {
			t.Fatalf("policy %d must resolve to the default, got %v", policy, err)
		}
		if view.Size != 4096 {
			t.Fatalf("expected 4096 bytes, got %d", view.Size)
		}
	}
}

// The deployment ceiling is the operator's control and can only narrow the
// administrative policy, never widen it.
func TestDeploymentCeilingNarrowsAWiderWorkspacePolicy(t *testing.T) {
	f := newFixture(t, fixtureOptions{maxUploadBytes: 1024, scanRequired: true})
	withWorkspacePolicy(f, uploadpolicy.MaxMaxUploadBytes)

	if _, err := f.upload(context.Background(), bytes.NewReader(bytes.Repeat([]byte("x"), 1024)), "at.txt"); err != nil {
		t.Fatalf("a file at the ceiling must be accepted, got %v", err)
	}

	over := newFixture(t, fixtureOptions{maxUploadBytes: 1024, scanRequired: true})
	withWorkspacePolicy(over, uploadpolicy.MaxMaxUploadBytes)
	_, err := over.upload(context.Background(), bytes.NewReader(bytes.Repeat([]byte("x"), 1025)), "over.txt")
	if !errors.Is(err, domain.ErrTooLarge) {
		t.Fatalf("the ceiling must still refuse, got %v", err)
	}
}

// A narrower workspace policy wins over a wider ceiling, which is the ordinary
// case: the administrator decides, the operator only caps.
func TestWorkspacePolicyNarrowerThanTheCeilingWins(t *testing.T) {
	f := newFixture(t, fixtureOptions{maxUploadBytes: uploadpolicy.MaxMaxUploadBytes})
	withWorkspacePolicy(f, policyBytes)

	_, err := f.upload(context.Background(), bytes.NewReader(bytes.Repeat([]byte("x"), int(policyBytes+1))), "over.txt")
	if !errors.Is(err, domain.ErrTooLarge) {
		t.Fatalf("the workspace policy must be enforced, got %v", err)
	}
}

// A stream that breaks is a client error, never a size rejection: confusing the
// two would tell a user to shrink a file that was fine.
func TestAReadFailureIsNotReportedAsTooLarge(t *testing.T) {
	f := newFixture(t)
	withWorkspacePolicy(f, 4096)
	source := &failingReader{data: bytes.Repeat([]byte("x"), 1024), err: errors.New("connection reset")}

	_, err := f.upload(context.Background(), source, "broken.bin")
	if errors.Is(err, domain.ErrTooLarge) {
		t.Fatal("a transport failure must not be reported as an oversized file")
	}
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
}

// Authorization is the step that decides whether the body may be read at all,
// so a half-wired service must refuse there rather than later.
func TestAuthorizeUploadRefusesWhenDependenciesAreMissing(t *testing.T) {
	var unwired *service.AttachmentService
	_, err := unwired.AuthorizeUpload(context.Background(), service.AuthorizeUploadInput{
		Destination: domain.Destination{Kind: domain.DestinationKindChannel, ID: testChannelID},
		UserID:      testUserID, SessionID: testSessionID,
	})
	if !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
}

// Upload takes an already-authorised target, so a caller that skipped
// AuthorizeUpload hands it a zero value. That must be refused, not defaulted:
// a permissive fallback here would be an unauthenticated write with no size
// budget at all.
func TestUploadRefusesATargetThatWasNeverAuthorized(t *testing.T) {
	f := newFixture(t)

	for _, tt := range []struct {
		name   string
		target service.UploadTarget
	}{
		{name: "zero value"},
		{name: "no uploader", target: service.UploadTarget{
			WorkspaceID: testWorkspaceID, MaxUploadBytes: policyBytes,
		}},
		{name: "no workspace", target: service.UploadTarget{
			UploaderID: testUserID, MaxUploadBytes: policyBytes,
		}},
		{name: "no budget", target: service.UploadTarget{
			UploaderID: testUserID, WorkspaceID: testWorkspaceID,
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := f.service.Upload(context.Background(), service.UploadInput{
				Target:   tt.target,
				Filename: "x.txt",
				Content:  bytes.NewReader([]byte("data")),
			})
			if !errors.Is(err, domain.ErrUnauthorized) {
				t.Fatalf("expected ErrUnauthorized, got %v", err)
			}
			assertNothingPersisted(t, f)
		})
	}
}
