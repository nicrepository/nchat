package service_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
)

// Issue #824, the send side. Three things belong to the service here: the flag
// reaches storage, the recipient bound is consulted exactly when it should be,
// and a send that asks for confirmation is a different send for idempotency
// purposes. Who the recipients actually are is derived in SQL and proved
// against a real database.

// ackParentID is a real UUID because reference validation parses it before it
// ever reaches the store.
const ackParentID = "99999999-9999-4999-8999-999999999999"

func ackGroupConversation() domain.DMConversation {
	return domain.DMConversation{
		ID: "group-1", WorkspaceID: "ws-1", Type: domain.DMConversationTypeGroup,
		Status: domain.DMConversationStatusActive,
	}
}

// createAckChannelMessage sends into a visible public channel, asking for
// acknowledgement or not, and returns the store it wrote through.
func createAckChannelMessage(t *testing.T, required bool, recipients int) (*fakeMessageStore, error) {
	t.Helper()
	msgs := &fakeMessageStore{
		createdMessage:            domain.Message{ID: "msg-ack"},
		acknowledgementRecipients: recipients,
	}
	_, err := service.NewMessageService(
		&fakeChannelStore{visibleChannel: publicActiveChannel("ws-1", "ch-1")}, &fakeDMStore{}, msgs,
	).CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
		WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1, BodyText: "restart the cluster",
		BodyFormat: domain.MessageBodyFormatV1, AcknowledgementRequired: required,
	})
	return msgs, err
}

// The flag reaches the statement that writes the message. Without this the
// recipient rows are never created and every other behaviour in this issue is
// unreachable.
func TestMessageService_CreateChannelMessage_ForwardsAcknowledgementRequired(t *testing.T) {
	msgs, err := createAckChannelMessage(t, true, 3)
	if err != nil {
		t.Fatalf("CreateChannelMessage: %v", err)
	}
	if !msgs.lastCreateInput.AcknowledgementRequired {
		t.Fatal("the acknowledgement request did not reach storage")
	}
}

func TestMessageService_CreateDMMessage_ForwardsAcknowledgementRequired(t *testing.T) {
	msgs := &fakeMessageStore{createdMessage: domain.Message{ID: "msg-ack-dm"}, acknowledgementRecipients: 2}
	_, err := service.NewMessageService(
		&fakeChannelStore{}, &fakeDMStore{visibleConversation: ackGroupConversation()}, msgs,
	).CreateDMMessage(context.Background(), service.CreateDMMessageInput{
		WorkspaceID: "ws-1", ConversationID: "group-1", SenderID: user1, BodyText: "please confirm",
		BodyFormat: domain.MessageBodyFormatV1, AcknowledgementRequired: true,
	})
	if err != nil {
		t.Fatalf("CreateDMMessage: %v", err)
	}
	if !msgs.lastCreateInput.AcknowledgementRequired {
		t.Fatal("the acknowledgement request did not reach storage")
	}
}

// An ordinary send costs no bound query at all. This is the assertion that
// keeps the feature free for the messages that do not use it — almost all of
// them — rather than adding a membership read to every send in the product.
func TestMessageService_CreateChannelMessage_OrdinarySendNeverCountsRecipients(t *testing.T) {
	msgs, err := createAckChannelMessage(t, false, 1000)
	if err != nil {
		t.Fatalf("CreateChannelMessage: %v", err)
	}
	if msgs.countAcknowledgementCalls != 0 {
		t.Fatalf("a send that asked for nothing counted recipients %d times", msgs.countAcknowledgementCalls)
	}
	if msgs.createCalls != 1 {
		t.Fatalf("expected the message to be written, createCalls=%d", msgs.createCalls)
	}
}

// The bound: at or below it the send proceeds, one past it the send is refused
// and nothing is written.
func TestMessageService_CreateChannelMessage_BoundsAcknowledgementRecipients(t *testing.T) {
	for _, tc := range []struct {
		name      string
		eligible  int
		wantAllow bool
	}{
		{name: "nobody eligible", eligible: 0, wantAllow: true},
		{name: "one recipient", eligible: 1, wantAllow: true},
		{name: "exactly the bound", eligible: domain.MaxAcknowledgementRecipients, wantAllow: true},
		{name: "one over the bound", eligible: domain.MaxAcknowledgementRecipients + 1, wantAllow: false},
		// Far over decides identically and — because the fake saturates exactly
		// as the store's LIMIT does — through the same ceiling value, never a
		// number this large.
		{name: "far over the bound", eligible: domain.MaxAcknowledgementRecipients * 100, wantAllow: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msgs, err := createAckChannelMessage(t, true, tc.eligible)
			assertBoundWasConsultedOnce(t, msgs)
			assertBoundDecision(t, msgs, err, tc.wantAllow)
		})
	}
}

// The bound costs exactly one query, against the target being sent to, and
// reads one row past the limit — so judging an enormous channel costs what
// judging a small one costs.
func assertBoundWasConsultedOnce(t *testing.T, msgs *fakeMessageStore) {
	t.Helper()
	if msgs.countAcknowledgementCalls != 1 || msgs.lastAcknowledgementChannelID != "ch-1" {
		t.Fatalf("expected exactly one count against ch-1, calls=%d channel=%q",
			msgs.countAcknowledgementCalls, msgs.lastAcknowledgementChannelID)
	}
	if msgs.lastAcknowledgementLimit != domain.MaxAcknowledgementRecipients+1 {
		t.Fatalf("counted up to %d, want %d",
			msgs.lastAcknowledgementLimit, domain.MaxAcknowledgementRecipients+1)
	}
}

func assertBoundDecision(t *testing.T, msgs *fakeMessageStore, err error, wantAllow bool) {
	t.Helper()
	if wantAllow {
		if err != nil || msgs.createCalls != 1 {
			t.Fatalf("expected the send to proceed, err=%v createCalls=%d", err, msgs.createCalls)
		}
		return
	}
	if !errors.Is(err, domain.ErrAcknowledgementRecipientsExceeded) {
		t.Fatalf("error = %v, want ErrAcknowledgementRecipientsExceeded", err)
	}
	if msgs.createCalls != 0 {
		t.Fatal("an over-bound send must write nothing at all")
	}
}

// The bound is counted against the conversation on the DM path, not against a
// channel it has none of.
func TestMessageService_CreateDMMessage_CountsTheConversationNotAChannel(t *testing.T) {
	msgs := &fakeMessageStore{
		createdMessage:            domain.Message{ID: "msg-ack-dm"},
		acknowledgementRecipients: domain.MaxAcknowledgementRecipients + 1,
	}
	_, err := service.NewMessageService(
		&fakeChannelStore{}, &fakeDMStore{visibleConversation: ackGroupConversation()}, msgs,
	).CreateDMMessage(context.Background(), service.CreateDMMessageInput{
		WorkspaceID: "ws-1", ConversationID: "group-1", SenderID: user1, BodyText: "please confirm",
		BodyFormat: domain.MessageBodyFormatV1, AcknowledgementRequired: true,
	})
	if !errors.Is(err, domain.ErrAcknowledgementRecipientsExceeded) {
		t.Fatalf("error = %v, want ErrAcknowledgementRecipientsExceeded", err)
	}
	if msgs.lastAcknowledgementDMID != "group-1" || msgs.lastAcknowledgementChannelID != "" {
		t.Fatalf("counted against %+v, want the conversation and no channel",
			[]string{msgs.lastAcknowledgementChannelID, msgs.lastAcknowledgementDMID})
	}
	if msgs.lastAcknowledgementSender != user1 {
		t.Fatalf("counted with sender %q: the author is excluded by passing them, not by omission",
			msgs.lastAcknowledgementSender)
	}
}

// The bound is checked after authorization. A caller who cannot post here must
// not be able to use the refusal to learn how large the channel is.
func TestMessageService_CreateChannelMessage_UnauthorizedSendNeverCountsRecipients(t *testing.T) {
	msgs := &fakeMessageStore{acknowledgementRecipients: 1000}
	_, err := service.NewMessageService(
		&fakeChannelStore{getVisibleErr: domain.ErrNotFound}, &fakeDMStore{}, msgs,
	).CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
		WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1, BodyText: "hello",
		BodyFormat: domain.MessageBodyFormatV1, AcknowledgementRequired: true,
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
	if msgs.countAcknowledgementCalls != 0 {
		t.Fatal("an unauthorized send must not measure the conversation it was refused")
	}
}

// A send that asks for confirmation is a different send. Reusing one
// idempotency key across the two must not replay the one that asked for
// nothing, so the fingerprints must differ.
func TestMessageService_CreateChannelMessage_AcknowledgementChangesTheReplayIdentity(t *testing.T) {
	fingerprint := func(required bool) string {
		msgs := &fakeMessageStore{createdMessage: domain.Message{ID: "msg-fp"}}
		_, err := service.NewMessageService(
			&fakeChannelStore{visibleChannel: publicActiveChannel("ws-1", "ch-1")}, &fakeDMStore{}, msgs,
		).CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
			WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1, BodyText: "same text",
			BodyFormat: domain.MessageBodyFormatV1, IdempotencyKey: "key-1",
			AcknowledgementRequired: required,
		})
		if err != nil {
			t.Fatalf("CreateChannelMessage: %v", err)
		}
		return msgs.lastCreateInput.RequestFingerprint
	}
	plain, asking := fingerprint(false), fingerprint(true)
	if plain == "" || asking == "" {
		t.Fatal("both sends must record a fingerprint for the key they carried")
	}
	if plain == asking {
		t.Fatal("the same text sent plainly and asking for confirmation must not share a replay identity")
	}
}

// The compatibility half of the same rule: a send that asks for nothing hashes
// exactly as it did before this field existed. Stating the flag as false and
// omitting it are the same request, so they must produce the same fingerprint —
// which is what keeps a key already in flight when this ships replaying instead
// of becoming a conflict.
func TestMessageService_CreateChannelMessage_OrdinarySendKeepsItsReplayIdentity(t *testing.T) {
	fingerprint := func(input service.CreateChannelMessageInput) string {
		msgs := &fakeMessageStore{createdMessage: domain.Message{ID: "msg-compat"}}
		if _, err := service.NewMessageService(
			&fakeChannelStore{visibleChannel: publicActiveChannel("ws-1", "ch-1")}, &fakeDMStore{}, msgs,
		).CreateChannelMessage(context.Background(), input); err != nil {
			t.Fatalf("CreateChannelMessage: %v", err)
		}
		return msgs.lastCreateInput.RequestFingerprint
	}
	base := service.CreateChannelMessageInput{
		WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1, BodyText: "same text",
		BodyFormat: domain.MessageBodyFormatV1, IdempotencyKey: "key-1",
	}
	stated := base
	stated.AcknowledgementRequired = false

	if fingerprint(base) != fingerprint(stated) {
		t.Fatal("stating the flag as false must hash identically to omitting it")
	}
}

// ── the fan-out bound is a precondition of creating, not of replaying ────────
//
// Code review, #824: the bound was checked before the idempotency lookup, so a
// retry of a send that had already succeeded was judged against the
// conversation's membership *now*. A message created when 199 people were
// eligible stopped being retrievable the moment a 201st joined — the retry was
// answered with ErrAcknowledgementRecipientsExceeded instead of with the
// message it had already created.

// grownConversationRetry replays one identical send through a real create entry
// point, with the eligible recipient count crossing the bound in between.
//
// create is the entry point under test, so each path is exercised through its
// own authorization rather than through a shared helper that would hide the
// difference between them.
type grownConversationRetry struct {
	name   string
	create func(*fakeMessageStore) (domain.Message, error)
	store  func(recipients int) *fakeMessageStore
}

func acknowledgementRetryPaths() []grownConversationRetry {
	original := domain.Message{ID: "msg-already-created"}
	newStore := func(recipients int) *fakeMessageStore {
		return &fakeMessageStore{
			createdMessage: original,
			// What the second lookup finds: the message the first call created.
			createReplayOnRetry:       original,
			acknowledgementRecipients: recipients,
		}
	}
	return []grownConversationRetry{
		{
			name:  "channel",
			store: newStore,
			create: func(msgs *fakeMessageStore) (domain.Message, error) {
				return service.NewMessageService(
					&fakeChannelStore{visibleChannel: publicActiveChannel("ws-1", "ch-1")}, &fakeDMStore{}, msgs,
				).CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
					WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1,
					BodyText: "restart the cluster", BodyFormat: domain.MessageBodyFormatV1,
					IdempotencyKey: "retry-after-growth", AcknowledgementRequired: true,
				})
			},
		},
		{
			// A 1:1 conversation cannot reach the bound, so the DM half is proved
			// where acknowledgement can actually fan out: a group.
			name:  "group dm",
			store: newStore,
			create: func(msgs *fakeMessageStore) (domain.Message, error) {
				return service.NewMessageService(
					&fakeChannelStore{}, &fakeDMStore{visibleConversation: ackGroupConversation()}, msgs,
				).CreateDMMessage(context.Background(), service.CreateDMMessageInput{
					WorkspaceID: "ws-1", ConversationID: "group-1", SenderID: user1,
					BodyText: "restart the cluster", BodyFormat: domain.MessageBodyFormatV1,
					IdempotencyKey: "retry-after-growth", AcknowledgementRequired: true,
				})
			},
		},
	}
}

// The regression: the same send, retried after the conversation outgrew the
// bound, still returns the message it already created.
func TestMessageService_Create_RetryReplaysAfterTheConversationOutgrewTheBound(t *testing.T) {
	for _, path := range acknowledgementRetryPaths() {
		t.Run(path.name, func(t *testing.T) {
			msgs := path.store(domain.MaxAcknowledgementRecipients - 1)

			first, err := path.create(msgs)
			if err != nil || msgs.createCalls != 1 {
				t.Fatalf("first send: err=%v createCalls=%d", err, msgs.createCalls)
			}

			// Somebody joins. The conversation is now past the bound, so a *new*
			// send asking for confirmation would be refused from here on.
			msgs.acknowledgementRecipients = domain.MaxAcknowledgementRecipients + 1
			countsBeforeRetry := msgs.countAcknowledgementCalls

			retried, err := path.create(msgs)
			assertReplayedTheOriginal(t, first, retried, err, msgs)
			// The bound is not merely tolerated on a replay, it is not consulted:
			// a replay creates nothing, so there is no fan-out to judge.
			if msgs.countAcknowledgementCalls != countsBeforeRetry {
				t.Fatalf("the retry spent %d recipient counts; a replay decides no fan-out",
					msgs.countAcknowledgementCalls-countsBeforeRetry)
			}
		})
	}
}

// assertReplayedTheOriginal proves the retry returned the earlier message and
// created nothing: no second message, and therefore no second set of recipient
// rows, since those are written by the creating statement itself.
func assertReplayedTheOriginal(
	t *testing.T, first, retried domain.Message, err error, msgs *fakeMessageStore,
) {
	t.Helper()
	if err != nil {
		t.Fatalf("the retry failed: %v", err)
	}
	if retried.ID != first.ID {
		t.Fatalf("retry returned %q, want the original %q", retried.ID, first.ID)
	}
	if msgs.createCalls != 1 {
		t.Fatalf("createCalls = %d, want 1 — a replay writes no second message and no second recipient set",
			msgs.createCalls)
	}
}

// The other half of the same rule, and the proof the guard was moved rather
// than removed: a *new* send into an over-bound conversation is still refused.
func TestMessageService_Create_NewSendIsStillRefusedAboveTheBound(t *testing.T) {
	for _, path := range acknowledgementRetryPaths() {
		t.Run(path.name, func(t *testing.T) {
			// No earlier send to replay: createReplayOnRetry never fires for a
			// first call, so this is the creation path with an over-bound target.
			msgs := path.store(domain.MaxAcknowledgementRecipients + 1)

			_, err := path.create(msgs)
			if !errors.Is(err, domain.ErrAcknowledgementRecipientsExceeded) {
				t.Fatalf("error = %v, want ErrAcknowledgementRecipientsExceeded", err)
			}
			if msgs.createCalls != 0 {
				t.Fatal("an over-bound new send must write nothing at all")
			}
		})
	}
}

// Authorization is unaffected by the move: it still runs before the replay, so
// a sender who lost access to the target cannot read an old message back by
// presenting its key.
func TestMessageService_Create_ReplayStillRequiresCurrentAccess(t *testing.T) {
	original := domain.Message{ID: "msg-already-created"}
	msgs := &fakeMessageStore{createReplayMessage: original}
	_, err := service.NewMessageService(
		&fakeChannelStore{getVisibleErr: domain.ErrNotFound}, &fakeDMStore{}, msgs,
	).CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
		WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1, BodyText: "restart the cluster",
		BodyFormat: domain.MessageBodyFormatV1, IdempotencyKey: "retry-after-growth",
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
	if msgs.createReplayCalls != 0 {
		t.Fatal("an unauthorized caller reached the idempotency lookup")
	}
}

func TestMessageService_CreateDMMessage_ReplayStillRequiresCurrentMembership(t *testing.T) {
	msgs := &fakeMessageStore{createReplayMessage: domain.Message{ID: "msg-already-created"}}
	_, err := service.NewMessageService(
		&fakeChannelStore{}, &fakeDMStore{getVisibleErr: domain.ErrNotFound}, msgs,
	).CreateDMMessage(context.Background(), service.CreateDMMessageInput{
		WorkspaceID: "ws-1", ConversationID: "group-1", SenderID: user1, BodyText: "restart the cluster",
		BodyFormat: domain.MessageBodyFormatV1, IdempotencyKey: "retry-after-growth",
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
	if msgs.createReplayCalls != 0 {
		t.Fatal("an unauthorized caller reached the idempotency lookup")
	}
}

// ── announcing an acknowledgement change ─────────────────────────────────────
//
// Issue #824: a reply and a deletion both change what the person who asked is
// shown, and both happen inside the statement that writes the message — so the
// service announces them, and the subscriber re-reads the authorised summary.

// ackPublisher records the invalidations, and nothing else: it deliberately
// does not implement PublishMessageCreated's siblings beyond what the service
// needs, so a test reads exactly the events this issue added.
type ackPublisher struct {
	mu           sync.Mutex
	created      int
	invalidated  []ackInvalidation
	messageIDsIn []string
}

type ackInvalidation struct {
	WorkspaceID string
	TargetType  string
	TargetID    string
	MessageID   string
}

func (p *ackPublisher) PublishMessageCreated(
	_ context.Context, _, _, _ string, _ domain.Message,
) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.created++
}

func (p *ackPublisher) PublishMessageUpdated(
	_ context.Context, _, _, _ string, msg domain.Message,
) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.messageIDsIn = append(p.messageIDsIn, msg.ID)
}

func (p *ackPublisher) PublishAcknowledgementUpdated(
	_ context.Context, workspaceID, targetType, targetID, messageID string,
) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.invalidated = append(p.invalidated, ackInvalidation{
		WorkspaceID: workspaceID, TargetType: targetType, TargetID: targetID, MessageID: messageID,
	})
}

func (p *ackPublisher) snapshot() []ackInvalidation {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]ackInvalidation(nil), p.invalidated...)
}

// waitForInvalidations polls the publisher, because publishing is enqueued onto
// a bounded worker rather than performed inline.
func waitForInvalidations(t *testing.T, publisher *ackPublisher, want int) []ackInvalidation {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := publisher.snapshot(); len(got) >= want {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	return publisher.snapshot()
}

// A reply resolves its author's own pending request on the parent, so the
// person who asked is exactly the one holding a stale summary. The event names
// the *parent*, not the reply.
func TestMessageService_Reply_AnnouncesTheParentAcknowledgement(t *testing.T) {
	publisher := &ackPublisher{}
	msgs := &fakeMessageStore{
		createdMessage: domain.Message{
			ID: "msg-reply", WorkspaceID: "ws-1", ChannelID: "ch-1",
			SenderID: user1, ParentMessageID: ackParentID, Status: domain.MessageStatusActive,
		},
		// The parent the reply answers, so reference validation admits it.
		messagesByKey: map[string]domain.Message{
			"ws-1:" + ackParentID: {
				ID: ackParentID, WorkspaceID: "ws-1", ChannelID: "ch-1",
				SenderID: "user-asked", Status: domain.MessageStatusActive,
				AcknowledgementRequired: true,
			},
		},
	}
	svc := service.NewMessageService(
		&fakeChannelStore{visibleChannel: publicActiveChannel("ws-1", "ch-1")}, &fakeDMStore{}, msgs)
	svc.SetPublisher(publisher)

	if _, err := svc.CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
		WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1, BodyText: "on it",
		BodyFormat: domain.MessageBodyFormatV1, ParentMessageID: ackParentID,
	}); err != nil {
		t.Fatalf("CreateChannelMessage: %v", err)
	}

	invalidations := waitForInvalidations(t, publisher, 1)
	if len(invalidations) != 1 {
		t.Fatalf("published %d invalidations, want 1", len(invalidations))
	}
	if invalidations[0].MessageID != ackParentID {
		t.Fatalf("announced %q, want the parent whose request the reply resolved",
			invalidations[0].MessageID)
	}
	if invalidations[0].TargetType != "channel" || invalidations[0].TargetID != "ch-1" {
		t.Fatalf("announced to %+v, want the reply's own conversation", invalidations[0])
	}
}

// An ordinary message answers nothing, so there is no acknowledgement anywhere
// that its arrival could have changed.
func TestMessageService_OrdinarySend_AnnouncesNoAcknowledgement(t *testing.T) {
	publisher := &ackPublisher{}
	msgs := &fakeMessageStore{createdMessage: domain.Message{
		ID: "msg-plain", WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1,
	}}
	svc := service.NewMessageService(
		&fakeChannelStore{visibleChannel: publicActiveChannel("ws-1", "ch-1")}, &fakeDMStore{}, msgs)
	svc.SetPublisher(publisher)

	if _, err := svc.CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
		WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1, BodyText: "olá",
		BodyFormat: domain.MessageBodyFormatV1,
	}); err != nil {
		t.Fatalf("CreateChannelMessage: %v", err)
	}
	// Give the publish worker the same chance the positive test gives it.
	if got := waitForInvalidations(t, publisher, 1); len(got) != 0 {
		t.Fatalf("an ordinary send announced %d acknowledgement invalidations", len(got))
	}
}

// Deleting a message withdraws every pending request on it, which is a change
// its sender is looking at.
func TestMessageService_Delete_AnnouncesTheWithdrawnAcknowledgement(t *testing.T) {
	publisher := &ackPublisher{}
	msgs := &fakeMessageStore{
		deletedMessage: domain.Message{
			ID: ackParentID, WorkspaceID: "ws-1", ChannelID: "ch-1",
			SenderID: user1, Status: domain.MessageStatusDeleted,
		},
		deleteChanged: true,
	}
	svc := service.NewMessageService(&fakeChannelStore{}, &fakeDMStore{}, msgs)
	svc.SetPublisher(publisher)

	if _, err := svc.DeleteMessage(context.Background(), service.DeleteMessageInput{
		WorkspaceID: "ws-1", MessageID: ackParentID, RequesterID: user1,
	}); err != nil {
		t.Fatalf("DeleteMessage: %v", err)
	}

	invalidations := waitForInvalidations(t, publisher, 1)
	if len(invalidations) != 1 || invalidations[0].MessageID != ackParentID {
		t.Fatalf("delete announced %+v, want the deleted message's own id", invalidations)
	}
}

// A delete that changed nothing — the message was already removed — announces
// nothing, on the same terms as the message.updated it also suppresses.
func TestMessageService_DeleteThatChangedNothing_AnnouncesNothing(t *testing.T) {
	publisher := &ackPublisher{}
	msgs := &fakeMessageStore{
		deletedMessage: domain.Message{ID: ackParentID, WorkspaceID: "ws-1", ChannelID: "ch-1"},
		deleteChanged:  false,
	}
	svc := service.NewMessageService(&fakeChannelStore{}, &fakeDMStore{}, msgs)
	svc.SetPublisher(publisher)

	if _, err := svc.DeleteMessage(context.Background(), service.DeleteMessageInput{
		WorkspaceID: "ws-1", MessageID: ackParentID, RequesterID: user1,
	}); err != nil {
		t.Fatalf("DeleteMessage: %v", err)
	}
	if got := waitForInvalidations(t, publisher, 1); len(got) != 0 {
		t.Fatalf("a no-op delete announced %d invalidations", len(got))
	}
}
