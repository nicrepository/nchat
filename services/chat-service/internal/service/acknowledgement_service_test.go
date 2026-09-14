package service_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// Issue #824, service layer. What this layer owns is small and is exactly what
// is asserted here: required fields, and the fact that the recipient it forwards
// is the authenticated actor and never anything else. The transitions and the
// concurrency they survive belong to the store, and are proved against a real
// database.

type fakeAcknowledgementStore struct {
	summary domain.AcknowledgementSummary
	route   storage.AcknowledgementRoute
	changed bool
	err     error

	batch            map[string]domain.AcknowledgementSummary
	acknowledgeCalls int
	readCalls        int
	batchCalls       int
	lastBatch        storage.ReadAcknowledgementBatchInput
	lastAcknowledge  storage.AcknowledgeInput
	lastRead         storage.ReadAcknowledgementInput
}

func (f *fakeAcknowledgementStore) Acknowledge(
	_ context.Context, input storage.AcknowledgeInput,
) (storage.AcknowledgeResult, error) {
	f.acknowledgeCalls++
	f.lastAcknowledge = input
	if f.err != nil {
		return storage.AcknowledgeResult{}, f.err
	}
	return storage.AcknowledgeResult{Summary: f.summary, Route: f.route, Changed: f.changed}, nil
}

func (f *fakeAcknowledgementStore) ReadAcknowledgement(
	_ context.Context, input storage.ReadAcknowledgementInput,
) (domain.AcknowledgementSummary, error) {
	f.readCalls++
	f.lastRead = input
	return f.summary, f.err
}

func (f *fakeAcknowledgementStore) ReadAcknowledgementBatch(
	_ context.Context, input storage.ReadAcknowledgementBatchInput,
) (map[string]domain.AcknowledgementSummary, error) {
	f.batchCalls++
	f.lastBatch = input
	return f.batch, f.err
}

const (
	ackWorkspaceID = "11111111-1111-4111-8111-111111111111"
	ackMessageID   = "22222222-2222-4222-8222-222222222222"
	ackActorID     = "33333333-3333-4333-8333-333333333333"
)

func ackAction() service.AcknowledgementActionInput {
	return service.AcknowledgementActionInput{
		WorkspaceID: ackWorkspaceID, MessageID: ackMessageID, ActorUserID: ackActorID,
	}
}

// The recipient the store is asked about is the actor the handler resolved from
// the session. This is the one assertion that matters most in this file: there
// is no other value it could be, and this proves the service does not invent
// one.
func TestAcknowledgementService_AcknowledgeUsesTheAuthenticatedActor(t *testing.T) {
	store := &fakeAcknowledgementStore{summary: domain.AcknowledgementSummary{
		Required: true, Total: 3, Acknowledged: 1, Pending: 2,
		ViewerState: domain.AcknowledgementStateAcknowledged,
	}}
	svc := service.NewAcknowledgementService(store)

	outcome, err := svc.Acknowledge(t.Context(), ackAction())
	if err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}
	summary := outcome.Summary
	if store.lastAcknowledge.RecipientID != ackActorID {
		t.Fatalf("store asked about %q, want the authenticated actor", store.lastAcknowledge.RecipientID)
	}
	if store.lastAcknowledge.MessageID != ackMessageID || store.lastAcknowledge.WorkspaceID != ackWorkspaceID {
		t.Fatalf("store received %+v, want the requested message and workspace", store.lastAcknowledge)
	}
	if summary.ViewerState != domain.AcknowledgementStateAcknowledged {
		t.Fatalf("summary viewer state = %q, want the store's answer verbatim", summary.ViewerState)
	}
}

// Reading uses the same actor as the viewer, and writes nothing.
func TestAcknowledgementService_ReadUsesTheAuthenticatedViewer(t *testing.T) {
	store := &fakeAcknowledgementStore{summary: domain.AcknowledgementSummary{Required: true, Total: 2, Pending: 2}}
	svc := service.NewAcknowledgementService(store)

	if _, err := svc.Read(t.Context(), ackAction()); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if store.lastRead.ViewerID != ackActorID {
		t.Fatalf("store asked as %q, want the authenticated actor", store.lastRead.ViewerID)
	}
	if store.acknowledgeCalls != 0 {
		t.Fatal("reading an acknowledgement must not write one: opening a message is not confirming it")
	}
}

// Whitespace-only identifiers are the same as absent ones, and neither reaches
// the database.
func TestAcknowledgementService_RefusesIncompleteInputWithoutTouchingTheStore(t *testing.T) {
	for name, input := range map[string]service.AcknowledgementActionInput{
		"no workspace": {MessageID: ackMessageID, ActorUserID: ackActorID},
		"no message":   {WorkspaceID: ackWorkspaceID, ActorUserID: ackActorID},
		"no actor":     {WorkspaceID: ackWorkspaceID, MessageID: ackMessageID},
		"blank actor":  {WorkspaceID: ackWorkspaceID, MessageID: ackMessageID, ActorUserID: "   "},
	} {
		t.Run(name, func(t *testing.T) {
			assertBothEndpointsRefuse(t, input)
		})
	}
}

// assertBothEndpointsRefuse proves the refusal is the same on the write and the
// read, and that neither spent a query on it.
func assertBothEndpointsRefuse(t *testing.T, input service.AcknowledgementActionInput) {
	t.Helper()
	store := &fakeAcknowledgementStore{}
	svc := service.NewAcknowledgementService(store)

	if _, err := svc.Acknowledge(t.Context(), input); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("Acknowledge error = %v, want ErrInvalidInput", err)
	}
	if _, err := svc.Read(t.Context(), input); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("Read error = %v, want ErrInvalidInput", err)
	}
	if store.acknowledgeCalls != 0 || store.readCalls != 0 {
		t.Fatal("an incomplete request must not reach the store")
	}
}

// The store's refusal is passed through unchanged, so the handler's single
// non-enumerating mapping still applies.
func TestAcknowledgementService_PropagatesTheStoreRefusal(t *testing.T) {
	store := &fakeAcknowledgementStore{err: domain.ErrNotFound}
	svc := service.NewAcknowledgementService(store)

	if _, err := svc.Acknowledge(t.Context(), ackAction()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Acknowledge error = %v, want ErrNotFound", err)
	}
	if _, err := svc.Read(t.Context(), ackAction()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Read error = %v, want ErrNotFound", err)
	}
}

// Identifiers are trimmed before they are used, so a stray space in a path
// segment is not a different message.
func TestAcknowledgementService_TrimsIdentifiers(t *testing.T) {
	store := &fakeAcknowledgementStore{summary: domain.AcknowledgementSummary{
		ViewerState: domain.AcknowledgementStatePending,
	}}
	svc := service.NewAcknowledgementService(store)

	_, err := svc.Acknowledge(t.Context(), service.AcknowledgementActionInput{
		WorkspaceID: "  " + ackWorkspaceID + " ", MessageID: " " + ackMessageID, ActorUserID: ackActorID + "  ",
	})
	if err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}
	if store.lastAcknowledge.MessageID != ackMessageID || store.lastAcknowledge.RecipientID != ackActorID {
		t.Fatalf("store received untrimmed identifiers: %+v", store.lastAcknowledge)
	}
}

// ── the page batch ───────────────────────────────────────────────────────────
//
// Issue #824: opening a conversation used to cost one request and one
// aggregation per message that asked for confirmation. What the service owns
// here is the batch rule — bounded, well-formed, deduplicated — and the rest is
// one statement in the store.

func batchInput(ids ...string) service.ReadAcknowledgementBatchInput {
	return service.ReadAcknowledgementBatchInput{
		WorkspaceID: ackWorkspaceID, MessageIDs: ids, ViewerID: ackActorID,
	}
}

func TestAcknowledgementService_ReadBatch_AsksOnceForTheWholePage(t *testing.T) {
	second := "44444444-4444-4444-8444-444444444444"
	store := &fakeAcknowledgementStore{batch: map[string]domain.AcknowledgementSummary{
		ackMessageID: {Required: true, Total: 3, Pending: 2},
		second:       {Required: true, Total: 1, Pending: 1},
	}}
	svc := service.NewAcknowledgementService(store)

	summaries, err := svc.ReadBatch(t.Context(), batchInput(ackMessageID, second))
	if err != nil {
		t.Fatalf("ReadBatch: %v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("got %d summaries, want 2", len(summaries))
	}
	// The whole point: one call into storage, whatever the page holds.
	if store.batchCalls != 1 {
		t.Fatalf("storage was asked %d times for one page", store.batchCalls)
	}
	if store.lastBatch.ViewerID != ackActorID {
		t.Fatalf("asked as %q, want the authenticated viewer", store.lastBatch.ViewerID)
	}
}

// Repeating an id cannot multiply the work; the shared batch rule collapses it
// before any query runs.
func TestAcknowledgementService_ReadBatch_CollapsesDuplicates(t *testing.T) {
	store := &fakeAcknowledgementStore{batch: map[string]domain.AcknowledgementSummary{}}
	svc := service.NewAcknowledgementService(store)

	if _, err := svc.ReadBatch(t.Context(),
		batchInput(ackMessageID, ackMessageID, ackMessageID)); err != nil {
		t.Fatalf("ReadBatch: %v", err)
	}
	if len(store.lastBatch.MessageIDs) != 1 {
		t.Fatalf("storage received %v, want the id once", store.lastBatch.MessageIDs)
	}
}

// Everything the batch refuses, refused before a query runs.
func TestAcknowledgementService_ReadBatch_RefusesMalformedRequests(t *testing.T) {
	oversized := make([]string, service.MaxAcknowledgementBatchSize+1)
	for i := range oversized {
		oversized[i] = ackMessageID
	}
	for name, input := range map[string]service.ReadAcknowledgementBatchInput{
		"no ids":         batchInput(),
		"not a uuid":     batchInput("not-a-uuid"),
		"past the bound": batchInput(oversized...),
		"no workspace": {
			MessageIDs: []string{ackMessageID}, ViewerID: ackActorID,
		},
		"no viewer": {
			WorkspaceID: ackWorkspaceID, MessageIDs: []string{ackMessageID},
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := &fakeAcknowledgementStore{}
			svc := service.NewAcknowledgementService(store)

			if _, err := svc.ReadBatch(t.Context(), input); !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("error = %v, want ErrInvalidInput", err)
			}
			if store.batchCalls != 0 {
				t.Fatal("a malformed batch reached the store")
			}
		})
	}
}

// Exactly at the bound is allowed: the ceiling is the page size, not one less.
func TestAcknowledgementService_ReadBatch_AcceptsExactlyTheBound(t *testing.T) {
	ids := make([]string, 0, service.MaxAcknowledgementBatchSize)
	for i := range service.MaxAcknowledgementBatchSize {
		ids = append(ids, fmt.Sprintf("55555555-5555-4555-8555-%012d", i))
	}
	store := &fakeAcknowledgementStore{batch: map[string]domain.AcknowledgementSummary{}}
	svc := service.NewAcknowledgementService(store)

	if _, err := svc.ReadBatch(t.Context(), batchInput(ids...)); err != nil {
		t.Fatalf("a batch of exactly the bound was refused: %v", err)
	}
	if len(store.lastBatch.MessageIDs) != service.MaxAcknowledgementBatchSize {
		t.Fatalf("storage received %d ids", len(store.lastBatch.MessageIDs))
	}
}
