package service_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
)

// ── fakePublisher ─────────────────────────────────────────────────────────────

type fakePublisher struct {
	mu      sync.Mutex
	calls   []publishCall
	updates []publishCall
}

func (p *fakePublisher) PublishMessageUpdated(ctx context.Context, workspaceID, targetType, targetID string, msg domain.Message) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.updates = append(p.updates, publishCall{workspaceID: workspaceID, targetType: targetType, targetID: targetID, msg: msg, ctxErrAtCall: ctx.Err()})
}

func (p *fakePublisher) updateCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.updates)
}

func (p *fakePublisher) updateSnapshot() []publishCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]publishCall(nil), p.updates...)
}

type publishCall struct {
	workspaceID  string
	targetType   string
	targetID     string
	msg          domain.Message
	ctxErrAtCall error // ctx.Err() captured at the moment of publish
}

func (p *fakePublisher) PublishMessageCreated(ctx context.Context, workspaceID, targetType, targetID string, msg domain.Message) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, publishCall{
		workspaceID:  workspaceID,
		targetType:   targetType,
		targetID:     targetID,
		msg:          msg,
		ctxErrAtCall: ctx.Err(),
	})
}

func (p *fakePublisher) snapshot() []publishCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	calls := make([]publishCall, len(p.calls))
	copy(calls, p.calls)
	return calls
}

func (p *fakePublisher) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

func waitForPublishCalls(t *testing.T, pub *fakePublisher, want int) []publishCall {
	t.Helper()
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		calls := pub.snapshot()
		if len(calls) >= want {
			return calls
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("expected %d publish calls, got %d", want, pub.count())
	return nil
}

func TestMessageService_ForwardChannelMessage_PublishesOnlyAfterPersistence(t *testing.T) {
	baseStore := func() *fakeMessageStore {
		return &fakeMessageStore{messagesByKey: map[string]domain.Message{"ws-1:source": {
			ID: "source", WorkspaceID: "ws-1", ChannelID: "origin",
			Kind: domain.MessageKindUser, Status: domain.MessageStatusActive,
		}}}
	}
	t.Run("success targets destination", func(t *testing.T) {
		store := baseStore()
		store.forwardedMessage = domain.Message{
			ID: "forwarded", WorkspaceID: "ws-1", ChannelID: "destination",
			ForwardedFromMessageID: "source", Kind: domain.MessageKindUser,
			Status: domain.MessageStatusActive,
		}
		publisher := &fakePublisher{}
		svc := service.NewMessageService(
			&fakeChannelStore{visibleChannel: publicActiveChannel("ws-1", "destination")}, &fakeDMStore{}, store,
		)
		svc.SetPublisher(publisher)
		_, err := svc.ForwardChannelMessage(t.Context(), service.ForwardChannelMessageInput{
			WorkspaceID: "ws-1", DestinationChannelID: "destination", ActorID: user1, SourceMessageID: "source",
		})
		if err != nil {
			t.Fatalf("ForwardChannelMessage: %v", err)
		}
		call := waitForPublishCalls(t, publisher, 1)[0]
		if call.targetType != "channel" || call.targetID != "destination" || call.msg.ForwardedFromMessageID != "source" {
			t.Fatalf("unexpected publish: %+v", call)
		}
	})
	t.Run("persistence failure does not publish", func(t *testing.T) {
		store := baseStore()
		store.forwardErr = errors.New("database unavailable")
		publisher := &fakePublisher{}
		svc := service.NewMessageService(
			&fakeChannelStore{visibleChannel: publicActiveChannel("ws-1", "destination")}, &fakeDMStore{}, store,
		)
		svc.SetPublisher(publisher)
		_, err := svc.ForwardChannelMessage(t.Context(), service.ForwardChannelMessageInput{
			WorkspaceID: "ws-1", DestinationChannelID: "destination", ActorID: user1, SourceMessageID: "source",
		})
		if err == nil || publisher.count() != 0 {
			t.Fatalf("failed persistence must not publish: err=%v calls=%d", err, publisher.count())
		}
	})
	t.Run("idempotent replay does not publish", func(t *testing.T) {
		store := baseStore()
		store.forwardedMessage = domain.Message{
			ID: "forwarded", WorkspaceID: "ws-1", ChannelID: "destination",
			ForwardedFromMessageID: "source", Kind: domain.MessageKindUser,
			Status: domain.MessageStatusActive,
		}
		store.forwardReplayed = true
		publisher := &fakePublisher{}
		svc := service.NewMessageService(&fakeChannelStore{}, &fakeDMStore{}, store)
		svc.SetPublisher(publisher)

		result, err := svc.ForwardChannelMessage(t.Context(), service.ForwardChannelMessageInput{
			WorkspaceID: "ws-1", DestinationChannelID: "destination", ActorID: user1,
			SourceMessageID: "source", IdempotencyKey: "action-1",
		})

		if err != nil || !result.Replayed || publisher.count() != 0 {
			t.Fatalf("replay must not publish: result=%+v err=%v calls=%d", result, err, publisher.count())
		}
	})
}

func TestMessageService_EditMessage_BroadcastsUpdatedAfterPersist(t *testing.T) {
	store := &fakeMessageStore{
		messagesByKey: map[string]domain.Message{"ws-1:msg-1": {
			ID: "msg-1", WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1, Status: domain.MessageStatusActive,
		}},
		editedMessage: domain.Message{
			ID: "msg-1", WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1,
			Status: domain.MessageStatusActive, BodyText: "edited", BodyFormat: domain.MessageBodyFormatV1, EditCount: 1,
		},
	}
	publisher := &fakePublisher{}
	serviceUnderTest := service.NewMessageService(&fakeChannelStore{}, &fakeDMStore{}, store)
	serviceUnderTest.SetPublisher(publisher)
	if _, err := serviceUnderTest.EditMessage(context.Background(), service.EditMessageInput{
		WorkspaceID: "ws-1", MessageID: "msg-1", EditorID: user1, Body: "edited", BodyFormat: domain.MessageBodyFormatV1,
	}); err != nil {
		t.Fatalf("EditMessage: %v", err)
	}
	deadline := time.Now().Add(500 * time.Millisecond)
	for publisher.updateCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if publisher.updateCount() != 1 {
		t.Fatalf("expected one message.updated publish, got %d", publisher.updateCount())
	}
}

func TestMessageService_DeleteMessage_BroadcastsSanitizedPlaceholderOnce(t *testing.T) {
	store := &fakeMessageStore{
		deletedMessage: domain.Message{
			ID: "msg-1", WorkspaceID: "ws-1", DMConversationID: "dm-1", SenderID: user1,
			Kind: domain.MessageKindUser, Status: domain.MessageStatusDeleted, BodyText: "secret",
		},
		deleteChanged: true,
	}
	publisher := &fakePublisher{}
	svc := service.NewMessageService(&fakeChannelStore{}, &fakeDMStore{}, store)
	svc.SetPublisher(publisher)
	if _, err := svc.DeleteMessage(t.Context(), service.DeleteMessageInput{
		WorkspaceID: "ws-1", MessageID: "msg-1", RequesterID: user1,
	}); err != nil {
		t.Fatalf("DeleteMessage: %v", err)
	}
	deadline := time.Now().Add(500 * time.Millisecond)
	for publisher.updateCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	updates := publisher.updateSnapshot()
	if len(updates) != 1 || updates[0].targetType != "dm" || updates[0].targetID != "dm-1" || updates[0].msg.BodyText != "" {
		t.Fatalf("unexpected delete broadcast: %+v", updates)
	}

	store.deleteChanged = false
	if _, err := svc.DeleteMessage(t.Context(), service.DeleteMessageInput{
		WorkspaceID: "ws-1", MessageID: "msg-1", RequesterID: user1,
	}); err != nil {
		t.Fatalf("idempotent DeleteMessage: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	if publisher.updateCount() != 1 {
		t.Fatalf("idempotent delete published again: %d", publisher.updateCount())
	}
}

func TestMessageService_DeleteMessage_DoesNotPublishOnStorageFailure(t *testing.T) {
	publisher := &fakePublisher{}
	svc := service.NewMessageService(&fakeChannelStore{}, &fakeDMStore{}, &fakeMessageStore{deleteErr: errors.New("db down")})
	svc.SetPublisher(publisher)
	_, err := svc.DeleteMessage(t.Context(), service.DeleteMessageInput{
		WorkspaceID: "ws-1", MessageID: "msg-1", RequesterID: user1,
	})
	if err == nil || publisher.updateCount() != 0 {
		t.Fatalf("storage failure err=%v publishes=%d", err, publisher.updateCount())
	}
}

type blockingPublisher struct {
	started  chan publishCall
	release  chan struct{}
	finished chan publishCall
}

func newBlockingPublisher() *blockingPublisher {
	return &blockingPublisher{
		started:  make(chan publishCall, 1),
		release:  make(chan struct{}),
		finished: make(chan publishCall, 1),
	}
}

func (p *blockingPublisher) PublishMessageCreated(ctx context.Context, workspaceID, targetType, targetID string, msg domain.Message) {
	call := publishCall{
		workspaceID:  workspaceID,
		targetType:   targetType,
		targetID:     targetID,
		msg:          msg,
		ctxErrAtCall: ctx.Err(),
	}
	p.started <- call
	select {
	case <-p.release:
	case <-ctx.Done():
	}
	call.ctxErrAtCall = ctx.Err()
	p.finished <- call
}

type countingBlockingPublisher struct {
	started  chan struct{}
	release  chan struct{}
	finished chan struct{}
}

func newCountingBlockingPublisher(capacity int) *countingBlockingPublisher {
	return &countingBlockingPublisher{
		started:  make(chan struct{}, capacity),
		release:  make(chan struct{}),
		finished: make(chan struct{}, capacity),
	}
}

func (p *countingBlockingPublisher) PublishMessageCreated(ctx context.Context, _, _, _ string, _ domain.Message) {
	p.started <- struct{}{}
	select {
	case <-p.release:
	case <-ctx.Done():
	}
	p.finished <- struct{}{}
}

func waitForStartedPublishes(t *testing.T, pub *countingBlockingPublisher, want int) {
	t.Helper()
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if len(pub.started) >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("expected %d started publishes, got %d", want, len(pub.started))
}

// ── broadcast after persist ───────────────────────────────────────────────────

func TestMessageService_CreateChannelMessage_BroadcastsAfterPersist(t *testing.T) {
	ch := publicActiveChannel("ws-1", "ch-1")
	channels := &fakeChannelStore{visibleChannel: ch}
	persistedMsg := domain.Message{
		ID: "msg-broadcast", WorkspaceID: "ws-1", ChannelID: "ch-1",
		SenderID: user1, Kind: domain.MessageKindUser,
		Status: domain.MessageStatusActive,
	}
	msgs := &fakeMessageStore{createdMessage: persistedMsg}
	pub := &fakePublisher{}

	svc := service.NewMessageService(channels, &fakeDMStore{}, msgs)
	svc.SetPublisher(pub)

	_, err := svc.CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
		WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1, BodyText: "hello",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	calls := waitForPublishCalls(t, pub, 1)
	got := calls[0]
	if got.workspaceID != "ws-1" || got.targetType != "channel" || got.targetID != "ch-1" || got.msg.ID != "msg-broadcast" {
		t.Errorf("unexpected publish call: %+v", got)
	}
}

func TestMessageService_CreateChannelMessage_NoBroadcastWhenPersistFails(t *testing.T) {
	ch := publicActiveChannel("ws-1", "ch-1")
	channels := &fakeChannelStore{visibleChannel: ch}
	msgs := &fakeMessageStore{createErr: errors.New("db down")}
	pub := &fakePublisher{}

	svc := service.NewMessageService(channels, &fakeDMStore{}, msgs)
	svc.SetPublisher(pub)

	_, err := svc.CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
		WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1, BodyText: "hello",
	})
	if err == nil {
		t.Fatal("expected error from failing store")
	}
	if pub.count() != 0 {
		t.Errorf("expected no publish calls on persist failure, got %d", pub.count())
	}
}

func TestMessageService_CreateDMMessage_BroadcastsAfterPersist(t *testing.T) {
	conv := activeDMConversation("ws-1", "dm-1")
	dms := &fakeDMStore{visibleConversation: conv}
	persistedMsg := domain.Message{
		ID: "msg-dm-broadcast", WorkspaceID: "ws-1", DMConversationID: "dm-1",
		SenderID: user1, Kind: domain.MessageKindUser,
		Status: domain.MessageStatusActive,
	}
	msgs := &fakeMessageStore{createdMessage: persistedMsg}
	pub := &fakePublisher{}

	svc := service.NewMessageService(&fakeChannelStore{}, dms, msgs)
	svc.SetPublisher(pub)

	_, err := svc.CreateDMMessage(context.Background(), service.CreateDMMessageInput{
		WorkspaceID: "ws-1", ConversationID: "dm-1", SenderID: user1, BodyText: "hey",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	calls := waitForPublishCalls(t, pub, 1)
	got := calls[0]
	if got.workspaceID != "ws-1" || got.targetType != "dm" || got.targetID != "dm-1" || got.msg.ID != "msg-dm-broadcast" {
		t.Errorf("unexpected publish call: %+v", got)
	}
}

func TestMessageService_CreateDMMessage_NoBroadcastWhenPersistFails(t *testing.T) {
	conv := activeDMConversation("ws-1", "dm-1")
	dms := &fakeDMStore{visibleConversation: conv}
	msgs := &fakeMessageStore{createErr: errors.New("db down")}
	pub := &fakePublisher{}

	svc := service.NewMessageService(&fakeChannelStore{}, dms, msgs)
	svc.SetPublisher(pub)

	_, err := svc.CreateDMMessage(context.Background(), service.CreateDMMessageInput{
		WorkspaceID: "ws-1", ConversationID: "dm-1", SenderID: user1, BodyText: "hey",
	})
	if err == nil {
		t.Fatal("expected error from failing store")
	}
	if pub.count() != 0 {
		t.Errorf("expected no publish calls on persist failure, got %d", pub.count())
	}
}

func TestMessageService_CreateChannelMessage_NoBroadcastWithoutPublisher(t *testing.T) {
	ch := publicActiveChannel("ws-1", "ch-1")
	channels := &fakeChannelStore{visibleChannel: ch}
	msgs := &fakeMessageStore{createdMessage: domain.Message{ID: "msg-1", WorkspaceID: "ws-1", ChannelID: "ch-1"}}

	// No publisher set — should not panic.
	_, err := service.NewMessageService(channels, &fakeDMStore{}, msgs).
		CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
			WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1, BodyText: "hello",
		})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMessageService_SetPublisherConcurrentWithCreateMessage_NoRace(t *testing.T) {
	ch := publicActiveChannel("ws-1", "ch-1")
	channels := &fakeChannelStore{visibleChannel: ch}
	msgs := &fakeMessageStore{createdMessage: domain.Message{
		ID: "msg-race", WorkspaceID: "ws-1", ChannelID: "ch-1",
		SenderID: user1, Kind: domain.MessageKindUser,
		Status: domain.MessageStatusActive,
	}}
	svc := service.NewMessageService(channels, &fakeDMStore{}, msgs)

	done := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
					svc.SetPublisher(&fakePublisher{})
				}
			}
		}()
	}

	for i := 0; i < 1_000; i++ {
		_, err := svc.CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
			WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1, BodyText: "hello",
		})
		if err != nil {
			close(done)
			wg.Wait()
			t.Fatalf("CreateChannelMessage: %v", err)
		}
	}
	close(done)
	wg.Wait()
}

// ── GetChannelMessage ─────────────────────────────────────────────────────────

func TestMessageService_GetChannelMessage_ReturnsMessage(t *testing.T) {
	ch := publicActiveChannel("ws-1", "ch-1")
	channels := &fakeChannelStore{visibleChannel: ch}
	msgs := &fakeMessageStore{
		messagesByKey: map[string]domain.Message{
			"ws-1:msg-1": {ID: "msg-1", WorkspaceID: "ws-1", ChannelID: "ch-1",
				SenderID: user1, Status: domain.MessageStatusActive},
		},
	}

	svc := service.NewMessageService(channels, &fakeDMStore{}, msgs)
	got, err := svc.GetChannelMessage(context.Background(), service.GetChannelMessageInput{
		WorkspaceID: "ws-1", ChannelID: "ch-1", CallerID: user1, MessageID: "msg-1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != "msg-1" {
		t.Errorf("expected msg-1, got %s", got.ID)
	}
}

func TestMessageService_GetChannelMessage_NotFoundForDifferentChannel(t *testing.T) {
	ch := publicActiveChannel("ws-1", "ch-1")
	channels := &fakeChannelStore{visibleChannel: ch}
	// Message exists in workspace but belongs to ch-2, not ch-1.
	msgs := &fakeMessageStore{
		messagesByKey: map[string]domain.Message{
			"ws-1:msg-x": {ID: "msg-x", WorkspaceID: "ws-1", ChannelID: "ch-2"},
		},
	}

	svc := service.NewMessageService(channels, &fakeDMStore{}, msgs)
	_, err := svc.GetChannelMessage(context.Background(), service.GetChannelMessageInput{
		WorkspaceID: "ws-1", ChannelID: "ch-1", CallerID: user1, MessageID: "msg-x",
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expected ErrNotFound for cross-channel message, got %v", err)
	}
}

func TestMessageService_GetChannelMessage_ForbiddenCallerReturnsNotFound(t *testing.T) {
	channels := &fakeChannelStore{getVisibleErr: domain.ErrNotFound}
	msgs := &fakeMessageStore{}

	svc := service.NewMessageService(channels, &fakeDMStore{}, msgs)
	_, err := svc.GetChannelMessage(context.Background(), service.GetChannelMessageInput{
		WorkspaceID: "ws-1", ChannelID: "ch-1", CallerID: "bad-user", MessageID: "msg-1",
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expected ErrNotFound for unauthorized caller, got %v", err)
	}
}

// ── GetDMMessage ──────────────────────────────────────────────────────────────

func TestMessageService_GetDMMessage_ReturnsMessage(t *testing.T) {
	conv := activeDMConversation("ws-1", "dm-1")
	dms := &fakeDMStore{visibleConversation: conv}
	msgs := &fakeMessageStore{
		messagesByKey: map[string]domain.Message{
			"ws-1:dmsg-1": {ID: "dmsg-1", WorkspaceID: "ws-1", DMConversationID: "dm-1",
				SenderID: user1, Status: domain.MessageStatusActive},
		},
	}

	svc := service.NewMessageService(&fakeChannelStore{}, dms, msgs)
	got, err := svc.GetDMMessage(context.Background(), service.GetDMMessageInput{
		WorkspaceID: "ws-1", ConversationID: "dm-1", CallerID: user1, MessageID: "dmsg-1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != "dmsg-1" {
		t.Errorf("expected dmsg-1, got %s", got.ID)
	}
}

func TestMessageService_GetDMMessage_ForbiddenCallerReturnsNotFound(t *testing.T) {
	dms := &fakeDMStore{getVisibleErr: domain.ErrNotFound}
	msgs := &fakeMessageStore{}

	svc := service.NewMessageService(&fakeChannelStore{}, dms, msgs)
	_, err := svc.GetDMMessage(context.Background(), service.GetDMMessageInput{
		WorkspaceID: "ws-1", ConversationID: "dm-1", CallerID: "outsider", MessageID: "dmsg-1",
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expected ErrNotFound for unauthorized DM caller, got %v", err)
	}
}

func TestMessageService_GetDMMessage_NotFoundForDifferentConversation(t *testing.T) {
	conv := activeDMConversation("ws-1", "dm-1")
	dms := &fakeDMStore{visibleConversation: conv}
	// Message belongs to dm-2, not dm-1.
	msgs := &fakeMessageStore{
		messagesByKey: map[string]domain.Message{
			"ws-1:dmsg-y": {ID: "dmsg-y", WorkspaceID: "ws-1", DMConversationID: "dm-2"},
		},
	}

	svc := service.NewMessageService(&fakeChannelStore{}, dms, msgs)
	_, err := svc.GetDMMessage(context.Background(), service.GetDMMessageInput{
		WorkspaceID: "ws-1", ConversationID: "dm-1", CallerID: user1, MessageID: "dmsg-y",
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expected ErrNotFound for cross-conversation message, got %v", err)
	}
}

// ── Broadcast survives request cancellation ────────────────────────────────────

// TestMessageService_BroadcastAfterCancelledContext verifies that the publisher
// is still called even when the caller's context is cancelled before the publish.
// This simulates a client disconnect that arrives after the message is persisted
// but before the hub broadcast runs.
func TestMessageService_CreateChannelMessage_BroadcastAfterCancelledContext(t *testing.T) {
	ch := publicActiveChannel("ws-1", "ch-1")
	channels := &fakeChannelStore{visibleChannel: ch}
	persistedMsg := domain.Message{
		ID: "msg-cancel-ch", WorkspaceID: "ws-1", ChannelID: "ch-1",
		SenderID: user1, Kind: domain.MessageKindUser, Status: domain.MessageStatusActive,
	}
	msgs := &fakeMessageStore{createdMessage: persistedMsg}
	pub := &fakePublisher{}

	svc := service.NewMessageService(channels, &fakeDMStore{}, msgs)
	svc.SetPublisher(pub)

	// Pre-cancel the context before calling CreateChannelMessage.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := svc.CreateChannelMessage(ctx, service.CreateChannelMessageInput{
		WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1, BodyText: "hello",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Publisher must be called despite the cancelled parent context.
	calls := waitForPublishCalls(t, pub, 1)
	// The publish context must NOT be cancelled — context.WithoutCancel ensures the
	// parent's cancellation is not propagated. This assertion makes the test regressive:
	// if WithoutCancel were removed the publish ctx would be cancelled and this would fail.
	if calls[0].ctxErrAtCall != nil {
		t.Errorf("expected non-cancelled ctx at publish time, got %v", calls[0].ctxErrAtCall)
	}
}

func TestMessageService_CreateDMMessage_BroadcastAfterCancelledContext(t *testing.T) {
	conv := activeDMConversation("ws-1", "dm-1")
	dms := &fakeDMStore{visibleConversation: conv}
	persistedMsg := domain.Message{
		ID: "msg-cancel-dm", WorkspaceID: "ws-1", DMConversationID: "dm-1",
		SenderID: user1, Kind: domain.MessageKindUser, Status: domain.MessageStatusActive,
	}
	msgs := &fakeMessageStore{createdMessage: persistedMsg}
	pub := &fakePublisher{}

	svc := service.NewMessageService(&fakeChannelStore{}, dms, msgs)
	svc.SetPublisher(pub)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := svc.CreateDMMessage(ctx, service.CreateDMMessageInput{
		WorkspaceID: "ws-1", ConversationID: "dm-1", SenderID: user1, BodyText: "hey",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	calls := waitForPublishCalls(t, pub, 1)
	if calls[0].ctxErrAtCall != nil {
		t.Errorf("expected non-cancelled ctx at publish time, got %v", calls[0].ctxErrAtCall)
	}
}

func TestMessageService_CreateChannelMessage_PublishDoesNotBlockRequestGoroutine(t *testing.T) {
	ch := publicActiveChannel("ws-1", "ch-1")
	channels := &fakeChannelStore{visibleChannel: ch}
	reqCtx, cancelReq := context.WithCancel(context.Background())
	persistedMsg := domain.Message{
		ID: "msg-async-ch", WorkspaceID: "ws-1", ChannelID: "ch-1",
		SenderID: user1, Kind: domain.MessageKindUser, Status: domain.MessageStatusActive,
	}
	msgs := &fakeMessageStore{createdMessage: persistedMsg, afterCreate: cancelReq}
	pub := newBlockingPublisher()

	svc := service.NewMessageService(channels, &fakeDMStore{}, msgs)
	svc.SetPublisher(pub)

	done := make(chan error, 1)
	go func() {
		_, err := svc.CreateChannelMessage(reqCtx, service.CreateChannelMessageInput{
			WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1, BodyText: "hello",
		})
		done <- err
	}()

	var call publishCall
	select {
	case call = <-pub.started:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("publish was not called")
	}
	if call.ctxErrAtCall != nil {
		close(pub.release)
		t.Fatalf("publish context must be detached from request cancellation, got %v", call.ctxErrAtCall)
	}

	select {
	case err := <-done:
		if err != nil {
			close(pub.release)
			t.Fatalf("CreateChannelMessage returned error: %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		close(pub.release)
		<-done
		t.Fatal("CreateChannelMessage blocked waiting for PublishMessageCreated")
	}

	close(pub.release)
	select {
	case <-pub.finished:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("publisher goroutine did not finish after release")
	}
}

func TestMessageService_CreateChannelMessage_DropsPublishWhenAsyncQueueFull(t *testing.T) {
	ch := publicActiveChannel("ws-1", "ch-1")
	channels := &fakeChannelStore{visibleChannel: ch}
	msgs := &fakeMessageStore{createdMessage: domain.Message{
		ID: "msg-saturated", WorkspaceID: "ws-1", ChannelID: "ch-1",
		SenderID: user1, Kind: domain.MessageKindUser,
		Status: domain.MessageStatusActive,
	}}
	pub := newCountingBlockingPublisher(service.DefaultPublishQueueCapacity)

	svc := service.NewMessageService(channels, &fakeDMStore{}, msgs)
	svc.SetPublisher(pub)

	for i := 0; i < service.DefaultPublishQueueCapacity; i++ {
		_, err := svc.CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
			WorkspaceID: "ws-1",
			ChannelID:   "ch-1",
			SenderID:    user1,
			BodyText:    fmt.Sprintf("hello %d", i),
		})
		if err != nil {
			close(pub.release)
			t.Fatalf("CreateChannelMessage %d: %v", i, err)
		}
	}
	waitForStartedPublishes(t, pub, service.DefaultPublishQueueCapacity)

	done := make(chan error, 1)
	go func() {
		_, err := svc.CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
			WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1, BodyText: "overflow",
		})
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			close(pub.release)
			t.Fatalf("overflow CreateChannelMessage returned error: %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		close(pub.release)
		t.Fatal("overflow publish blocked the HTTP path instead of dropping predictably")
	}

	if got := svc.DroppedPublishCount(); got != 1 {
		close(pub.release)
		t.Fatalf("DroppedPublishCount = %d, want 1", got)
	}
	if len(pub.started) != service.DefaultPublishQueueCapacity {
		close(pub.release)
		t.Fatalf("overflow publish should not call publisher; started=%d", len(pub.started))
	}

	close(pub.release)
}

func TestMessageService_CreateChannelMessage_PublishTimeoutReleasesAsyncSlot(t *testing.T) {
	ch := publicActiveChannel("ws-1", "ch-1")
	channels := &fakeChannelStore{visibleChannel: ch}
	msgs := &fakeMessageStore{createdMessage: domain.Message{
		ID: "msg-timeout", WorkspaceID: "ws-1", ChannelID: "ch-1",
		SenderID: user1, Kind: domain.MessageKindUser,
		Status: domain.MessageStatusActive,
	}}
	pub := newBlockingPublisher()

	svc := service.NewMessageService(channels, &fakeDMStore{}, msgs)
	svc.SetPublisher(pub)

	_, err := svc.CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
		WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1, BodyText: "hello",
	})
	if err != nil {
		t.Fatalf("CreateChannelMessage: %v", err)
	}

	select {
	case <-pub.started:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("publish was not called")
	}

	select {
	case finished := <-pub.finished:
		if !errors.Is(finished.ctxErrAtCall, context.DeadlineExceeded) {
			t.Fatalf("publisher finished with ctx err %v, want DeadlineExceeded", finished.ctxErrAtCall)
		}
	case <-time.After(service.DefaultPublishTimeout + time.Second):
		close(pub.release)
		t.Fatal("publisher goroutine did not finish after publish timeout")
	}

	if got := svc.DroppedPublishCount(); got != 0 {
		t.Fatalf("publish timeout should release slot without recording a queue drop, got %d", got)
	}
}
