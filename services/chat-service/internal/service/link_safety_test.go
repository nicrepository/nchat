package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"time"

	"github.com/google/uuid"
	"github.com/nicrepository/nchat/libs/go/platform/urlsafety"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// RF-21, as it behaves after the move to Cloudflare URL Scanner.
//
// Two things changed and both are asserted throughout: the unit is a canonical
// URL rather than a hostname, so a verdict about a domain no longer clears every
// path on it; and the check is asynchronous, so a URL nobody has scanned makes
// the message *withheld* rather than refused — the provider cannot answer inside
// an interactive request, and the alternatives were publishing it unchecked or
// refusing every message carrying a link nobody had sent before.
//
// The verdict source is the store itself, which is what production wires: one
// indexed read, no network. Nothing in this file can reach Cloudflare.

// safetyStore builds a message store with a seeded verdict table.
func safetyStore(verdicts map[string]urlsafety.Verdict) *fakeMessageStore {
	return &fakeMessageStore{
		createdMessage: domain.Message{ID: "msg-1", WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1},
		linkVerdicts:   verdicts,
	}
}

// messageServiceWith builds a channel-message service whose stores accept
// everything, so the only thing under test is the RF-21 gate.
func messageServiceWith(store *fakeMessageStore) (*service.MessageService, *fakePublisher) {
	channels := &fakeChannelStore{visibleChannel: publicActiveChannel("ws-1", "ch-1")}
	dms := &fakeDMStore{visibleConversation: activeDMConversation("ws-1", "dm-1")}
	svc := service.NewMessageService(channels, dms, store)
	svc.SetLinkSafety(store)
	publisher := &fakePublisher{}
	svc.SetPublisher(publisher)
	return svc, publisher
}

func createChannelMessage(svc *service.MessageService, body string) (domain.Message, error) {
	return svc.CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
		WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1, BodyText: body,
	})
}

func createDMMessage(svc *service.MessageService, body string) (domain.Message, error) {
	return svc.CreateDMMessage(context.Background(), service.CreateDMMessageInput{
		WorkspaceID: "ws-1", ConversationID: "dm-1", SenderID: user1, BodyText: body,
	})
}

// safe seeds URLs as cleared and fresh.
func safe(urls ...string) map[string]urlsafety.Verdict {
	verdicts := make(map[string]urlsafety.Verdict, len(urls))
	for _, url := range urls {
		verdicts[url] = urlsafety.VerdictSafe
	}
	return verdicts
}

// --- the three outcomes ---------------------------------------------------

// A message with no link never reaches the gate and behaves exactly as it did
// before RF-21.
func TestMessageWithoutLinksIsUnaffected(t *testing.T) {
	store := safetyStore(nil)
	svc, publisher := messageServiceWith(store)

	if _, err := createChannelMessage(svc, "bom dia, tudo certo?"); err != nil {
		t.Fatalf("CreateChannelMessage: %v", err)
	}
	waitForPublishCalls(t, publisher, 1)
	if store.createCalls != 1 {
		t.Fatalf("createCalls=%d", store.createCalls)
	}
	if store.linkVerdictCalls != 0 {
		t.Fatal("the verdict table was consulted for a message with no link")
	}
	if store.lastCreateInput.Status != "" || len(store.lastCreateInput.LinkScanURLs) != 0 {
		t.Fatalf("a linkless message was recorded as waiting on something: %+v", store.lastCreateInput)
	}
}

// The fast path: a URL already cleared and still fresh publishes immediately,
// with no scan queued and nothing withheld.
func TestCachedSafeURLPublishesImmediately(t *testing.T) {
	store := safetyStore(safe("https://example.com/artigo"))
	svc, publisher := messageServiceWith(store)

	if _, err := createChannelMessage(svc, "veja https://example.com/artigo"); err != nil {
		t.Fatalf("CreateChannelMessage: %v", err)
	}
	waitForPublishCalls(t, publisher, 1)
	if store.createCalls != 1 {
		t.Fatalf("createCalls=%d", store.createCalls)
	}
	if store.lastCreateInput.Status != "" {
		t.Fatalf("a cleared message was withheld: %q", store.lastCreateInput.Status)
	}
	if len(store.ensuredURLs) != 0 {
		t.Fatalf("a scan was queued for an already-decided URL: %v", store.ensuredURLs)
	}
}

// The property RF-21 exists for: a condemned link means no row and no broadcast.
func TestMaliciousURLIsNotPersisted(t *testing.T) {
	store := safetyStore(map[string]urlsafety.Verdict{
		"https://evil.example/login": urlsafety.VerdictMalicious,
	})
	svc, publisher := messageServiceWith(store)

	_, err := createChannelMessage(svc, "clique em https://evil.example/login")

	if !errors.Is(err, domain.ErrMaliciousURL) {
		t.Fatalf("want ErrMaliciousURL, got %v", err)
	}
	if store.createCalls != 0 || publisher.count() != 0 {
		t.Fatalf("a refused message was persisted or published: createCalls=%d published=%d",
			store.createCalls, publisher.count())
	}
}

// The new outcome: no verdict yet means accepted, withheld, and scanned.
func TestUnknownURLWithholdsTheMessage(t *testing.T) {
	store := safetyStore(nil)
	svc, publisher := messageServiceWith(store)

	if _, err := createChannelMessage(svc, "olha https://novo.example/post"); err != nil {
		t.Fatalf("an unscanned link must not refuse the send: %v", err)
	}
	// Persisted, so a restart does not lose it.
	if store.createCalls != 1 {
		t.Fatalf("createCalls=%d", store.createCalls)
	}
	// Withheld: the status every read path already excludes.
	if store.lastCreateInput.Status != domain.MessageStatusPendingLinkScan {
		t.Fatalf("status=%q", store.lastCreateInput.Status)
	}
	// Nobody else is told about it.
	assertNothingPublished(t, publisher)
	// And the scan that will decide it is queued.
	if len(store.ensuredURLs) != 1 || store.ensuredURLs[0] != "https://novo.example/post" {
		t.Fatalf("scan queue: %v", store.ensuredURLs)
	}
	// The message records what it is waiting on, in the same statement.
	if len(store.lastCreateInput.LinkScanURLs) != 1 {
		t.Fatalf("link edges: %v", store.lastCreateInput.LinkScanURLs)
	}
}

// --- path and query are part of the decision -------------------------------

// The finding this round exists for. A cleared domain root clears nothing else.
func TestSafeURLDoesNotClearAnotherPathOnTheSameHost(t *testing.T) {
	for name, body := range map[string]string{
		"different path":   "veja https://trusted.example/phishing",
		"open redirect":    "veja https://trusted.example/redirect?target=https://evil.example",
		"different query":  "veja https://trusted.example/download?id=2",
		"different scheme": "veja http://trusted.example/",
	} {
		t.Run(name, func(t *testing.T) {
			// Only the root is cleared.
			store := safetyStore(safe("https://trusted.example/"))
			svc, publisher := messageServiceWith(store)

			if _, err := createChannelMessage(svc, body); err != nil {
				t.Fatalf("CreateChannelMessage: %v", err)
			}
			if store.lastCreateInput.Status != domain.MessageStatusPendingLinkScan {
				t.Fatalf("%q inherited the root's clearance: status=%q",
					body, store.lastCreateInput.Status)
			}
			assertNothingPublished(t, publisher)
		})
	}
}

// A malicious path does not condemn the host either — the other direction of the
// same property, and the one that keeps a shared host usable.
func TestMaliciousPathDoesNotCondemnTheWholeHost(t *testing.T) {
	store := safetyStore(map[string]urlsafety.Verdict{
		"https://shared.example/bad":  urlsafety.VerdictMalicious,
		"https://shared.example/good": urlsafety.VerdictSafe,
	})
	svc, publisher := messageServiceWith(store)

	if _, err := createChannelMessage(svc, "veja https://shared.example/good"); err != nil {
		t.Fatalf("a cleared path on a host with a condemned one was refused: %v", err)
	}
	waitForPublishCalls(t, publisher, 1)
}

// Fragments are not part of the identity: they never reach the origin server, so
// splitting on them would only multiply scans.
func TestFragmentDoesNotCreateASeparateVerdict(t *testing.T) {
	store := safetyStore(safe("https://example.com/file"))
	svc, publisher := messageServiceWith(store)

	if _, err := createChannelMessage(svc, "veja https://example.com/file#secao-2"); err != nil {
		t.Fatalf("CreateChannelMessage: %v", err)
	}
	if store.lastCreateInput.Status != "" {
		t.Fatalf("a fragment forced a new scan: status=%q", store.lastCreateInput.Status)
	}
	waitForPublishCalls(t, publisher, 1)
}

// The Unicode correction from the previous round, restated: a rune whose
// lowercase form is a different number of bytes must not move where the URL is
// found. The old scanner located candidates in strings.ToLower(text) and sliced
// them out of text with those offsets.
func TestUnicodePrefixDoesNotShiftTheURLOffset(t *testing.T) {
	for name, body := range map[string]string{
		"http":   "İstanbul http://example.com/rota",
		"HTTPS":  "İstanbul HTTPS://example.com/rota",
		"mixed":  "İstanbul HtTpS://example.com/rota",
		"eszett": "STRAẞE https://example.com/rota",
		"many":   "İİİ ẞẞ İ https://example.com/rota",
	} {
		t.Run(name, func(t *testing.T) {
			store := safetyStore(safe("https://example.com/rota", "http://example.com/rota"))
			svc, publisher := messageServiceWith(store)

			if _, err := createChannelMessage(svc, body); err != nil {
				t.Fatalf("CreateChannelMessage: %v", err)
			}
			if store.lastCreateInput.Status != "" {
				t.Fatalf("the link was not found where it actually is: status=%q asked=%v",
					store.lastCreateInput.Status, store.lastVerdictURLs)
			}
			waitForPublishCalls(t, publisher, 1)
		})
	}
}

// --- several links in one message ------------------------------------------

func TestAllURLsMustBeDecidedBeforePublishing(t *testing.T) {
	for name, testCase := range map[string]struct {
		verdicts   map[string]urlsafety.Verdict
		wantErr    error
		wantStatus domain.MessageStatus
	}{
		"one safe one pending": {
			verdicts:   safe("https://a.example/x"),
			wantStatus: domain.MessageStatusPendingLinkScan,
		},
		"one safe one malicious": {
			verdicts: map[string]urlsafety.Verdict{
				"https://a.example/x": urlsafety.VerdictSafe,
				"https://b.example/y": urlsafety.VerdictMalicious,
			},
			wantErr: domain.ErrMaliciousURL,
		},
		"both pending": {wantStatus: domain.MessageStatusPendingLinkScan},
		"both safe": {
			verdicts:   safe("https://a.example/x", "https://b.example/y"),
			wantStatus: "",
		},
		// A pending URL beside a malicious one is still a refusal: one condemned
		// link is enough, and waiting for the other would be waiting to say no.
		"one pending one malicious": {
			verdicts: map[string]urlsafety.Verdict{"https://b.example/y": urlsafety.VerdictMalicious},
			wantErr:  domain.ErrMaliciousURL,
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := safetyStore(testCase.verdicts)
			svc, publisher := messageServiceWith(store)

			_, err := createChannelMessage(svc, "https://a.example/x e https://b.example/y")

			if testCase.wantErr != nil {
				if !errors.Is(err, testCase.wantErr) {
					t.Fatalf("want %v, got %v", testCase.wantErr, err)
				}
				if store.createCalls != 0 || publisher.count() != 0 {
					t.Fatal("a refused message left a trace")
				}
				return
			}
			if err != nil {
				t.Fatalf("CreateChannelMessage: %v", err)
			}
			if store.lastCreateInput.Status != testCase.wantStatus {
				t.Fatalf("status=%q want %q", store.lastCreateInput.Status, testCase.wantStatus)
			}
			if testCase.wantStatus == "" {
				waitForPublishCalls(t, publisher, 1)
				return
			}
			assertNothingPublished(t, publisher)
		})
	}
}

// The scan fan-out one message may cause is bounded, and going over is a refusal
// rather than a truncation — checking the first ten would make "add an eleventh
// link" a documented bypass.
func TestTooManyDistinctURLsIsRefused(t *testing.T) {
	var body strings.Builder
	for i := 0; i < 11; i++ {
		body.WriteString("https://example.com/p")
		body.WriteByte(byte('a' + i))
		body.WriteByte(' ')
	}
	store := safetyStore(nil)
	svc, _ := messageServiceWith(store)

	_, err := createChannelMessage(svc, body.String())

	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("want ErrInvalidInput, got %v", err)
	}
	if len(store.ensuredURLs) != 0 {
		t.Fatal("a refused message still queued scans")
	}
}

// Twenty links to one URL are one scan, which is what keeps the bound about
// distinct resources rather than about typing.
func TestRepeatedURLIsOneScan(t *testing.T) {
	store := safetyStore(nil)
	svc, _ := messageServiceWith(store)

	if _, err := createChannelMessage(svc, strings.Repeat("https://example.com/x ", 20)); err != nil {
		t.Fatalf("CreateChannelMessage: %v", err)
	}
	if len(store.ensuredURLs) != 1 {
		t.Fatalf("scans queued: %v", store.ensuredURLs)
	}
}

// --- URLs that cannot be scanned at all ------------------------------------

// An IP literal has no reputation to consult, and refusing it is what stops
// "host the phishing page on a bare address" from being the obvious bypass.
func TestUncheckableURLIsBlocked(t *testing.T) {
	for name, body := range map[string]string{
		"ipv4":        "veja http://192.0.2.10/login",
		"ipv6":        "veja http://[2001:db8::1]/login",
		"credentials": "veja https://user:pw@example.com/login",
	} {
		t.Run(name, func(t *testing.T) {
			store := safetyStore(nil)
			svc, publisher := messageServiceWith(store)

			_, err := createChannelMessage(svc, body)

			if !errors.Is(err, domain.ErrMaliciousURL) {
				t.Fatalf("want ErrMaliciousURL, got %v", err)
			}
			if store.createCalls != 0 || publisher.count() != 0 {
				t.Fatal("an uncheckable link was persisted or published")
			}
		})
	}
}

// "https://" inside a sentence is not a link, and must not refuse the message.
func TestSchemeWithoutAHostIsNotALink(t *testing.T) {
	store := safetyStore(nil)
	svc, publisher := messageServiceWith(store)

	if _, err := createChannelMessage(svc, "escreva https:// para comecar"); err != nil {
		t.Fatalf("a bare scheme must not refuse the message: %v", err)
	}
	waitForPublishCalls(t, publisher, 1)
}

// --- a failure is never a clearance -----------------------------------------

func TestVerdictTableFailureRefusesTheSend(t *testing.T) {
	store := safetyStore(nil)
	store.linkVerdictErr = errors.New("database unavailable")
	svc, publisher := messageServiceWith(store)

	_, err := createChannelMessage(svc, "veja https://example.com/x")

	if !errors.Is(err, domain.ErrURLCheckUnavailable) {
		t.Fatalf("want ErrURLCheckUnavailable, got %v", err)
	}
	if store.createCalls != 0 || publisher.count() != 0 {
		t.Fatal("a message was accepted while the verdict table was unreadable")
	}
}

// Failing to queue the scan is also not a clearance: a withheld message nobody
// scheduled a scan for would wait forever.
func TestFailureToQueueAScanRefusesTheSend(t *testing.T) {
	store := safetyStore(nil)
	store.ensureScansErr = errors.New("database unavailable")
	svc, _ := messageServiceWith(store)

	_, err := createChannelMessage(svc, "veja https://example.com/x")

	if !errors.Is(err, domain.ErrURLCheckUnavailable) {
		t.Fatalf("want ErrURLCheckUnavailable, got %v", err)
	}
	if store.createCalls != 0 {
		t.Fatal("a message was withheld with no scan to release it")
	}
}

// --- the other write paths --------------------------------------------------

// A DM is the likelier phishing vector of the two, not the lesser one.
func TestDMIsGatedToo(t *testing.T) {
	store := safetyStore(map[string]urlsafety.Verdict{
		"https://evil.example/x": urlsafety.VerdictMalicious,
	})
	svc, publisher := messageServiceWith(store)

	if _, err := createDMMessage(svc, "veja https://evil.example/x"); !errors.Is(err, domain.ErrMaliciousURL) {
		t.Fatalf("want ErrMaliciousURL, got %v", err)
	}
	assertNothingPublished(t, publisher)

	pendingStore := safetyStore(nil)
	pendingSvc, pendingPublisher := messageServiceWith(pendingStore)
	if _, err := createDMMessage(pendingSvc, "veja https://novo.example/x"); err != nil {
		t.Fatalf("CreateDMMessage: %v", err)
	}
	if pendingStore.lastCreateInput.Status != domain.MessageStatusPendingLinkScan {
		t.Fatalf("status=%q", pendingStore.lastCreateInput.Status)
	}
	assertNothingPublished(t, pendingPublisher)
}

func editableMessageStore(verdicts map[string]urlsafety.Verdict) *fakeMessageStore {
	store := safetyStore(verdicts)
	store.messagesByKey = map[string]domain.Message{"ws-1:msg-1": {
		ID: "msg-1", WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1,
		Kind: domain.MessageKindUser, Status: domain.MessageStatusActive,
	}}
	return store
}

func editMessage(svc *service.MessageService, body string) (domain.Message, error) {
	return svc.EditMessage(context.Background(), service.EditMessageInput{
		WorkspaceID: "ws-1", MessageID: "msg-1", EditorID: user1,
		Body: body, BodyFormat: domain.MessageBodyFormatV2,
	})
}

// Editing cannot be bypassed by sending a clean message and editing the link in.
func TestEditIsGated(t *testing.T) {
	store := editableMessageStore(map[string]urlsafety.Verdict{
		"https://evil.example/x": urlsafety.VerdictMalicious,
	})
	svc, _ := messageServiceWith(store)

	_, err := editMessage(svc, "agora com https://evil.example/x")

	if !errors.Is(err, domain.ErrMaliciousURL) {
		t.Fatalf("want ErrMaliciousURL, got %v", err)
	}
}

// An edit is the one path that cannot go pending: showing everyone an unscanned
// body, or silently keeping the old one while reporting success, are both worse
// than telling the author to retry. The already-published version is untouched.
func TestEditWithAnUnscannedURLIsDeferredNotApplied(t *testing.T) {
	store := editableMessageStore(nil)
	svc, _ := messageServiceWith(store)

	_, err := editMessage(svc, "agora com https://novo.example/x")

	if !errors.Is(err, domain.ErrURLCheckPending) {
		t.Fatalf("want ErrURLCheckPending, got %v", err)
	}
	// The scan was queued anyway, so the author's retry succeeds shortly.
	if len(store.ensuredURLs) != 1 {
		t.Fatalf("the deferred edit queued no scan: %v", store.ensuredURLs)
	}
}

// A cleared edit still applies immediately.
func TestEditWithAClearedURLApplies(t *testing.T) {
	store := editableMessageStore(safe("https://example.com/x"))
	svc, _ := messageServiceWith(store)

	if _, err := editMessage(svc, "agora com https://example.com/x"); err != nil {
		t.Fatalf("EditMessage: %v", err)
	}
}

// --- forwarding -------------------------------------------------------------

func forwardServiceWith(store *fakeMessageStore) (*service.MessageService, *fakePublisher) {
	svc := service.NewMessageService(
		&fakeChannelStore{visibleChannel: publicActiveChannel("ws-1", "ch-2")},
		&fakeDMStore{}, store,
	)
	svc.SetLinkSafety(store)
	publisher := &fakePublisher{}
	svc.SetPublisher(publisher)
	return svc, publisher
}

func forwardWith(svc *service.MessageService, key string) (service.ForwardChannelMessageOutput, error) {
	return svc.ForwardChannelMessage(context.Background(), service.ForwardChannelMessageInput{
		WorkspaceID: "ws-1", DestinationChannelID: "ch-2", ActorID: user1,
		SourceMessageID: "aabbccdd-1111-2222-3333-00000000000f",
		IdempotencyKey:  key,
	})
}

// A forward creates a *new* message, so it is a way to publish content written
// before the check existed. It goes through the same gate, with the same three
// outcomes.
func TestForwardIsGated(t *testing.T) {
	for name, testCase := range map[string]struct {
		verdicts   map[string]urlsafety.Verdict
		wantErr    error
		wantStatus domain.MessageStatus
		wantPub    int
	}{
		"malicious": {
			verdicts: map[string]urlsafety.Verdict{"https://evil.example/x": urlsafety.VerdictMalicious},
			wantErr:  domain.ErrMaliciousURL,
		},
		"unscanned": {wantStatus: domain.MessageStatusPendingLinkScan},
		"safe":      {verdicts: safe("https://evil.example/x"), wantPub: 1},
	} {
		t.Run(name, func(t *testing.T) {
			store := safetyStore(testCase.verdicts)
			store.forwardSnapshot = storage.ForwardSnapshot{BodyText: "veja https://evil.example/x"}
			store.forwardedMessage = domain.Message{
				ID: "msg-fwd", WorkspaceID: "ws-1", ChannelID: "ch-2", SenderID: user1,
			}
			svc, publisher := forwardServiceWith(store)

			out, err := forwardWith(svc, "")

			if testCase.wantErr != nil {
				if !errors.Is(err, testCase.wantErr) {
					t.Fatalf("want %v, got %v", testCase.wantErr, err)
				}
				if store.forwardCalls != 0 {
					t.Fatal("a condemned forward was persisted")
				}
				return
			}
			if err != nil {
				t.Fatalf("ForwardChannelMessage: %v", err)
			}
			if store.lastForwardInput.Status != testCase.wantStatus {
				t.Fatalf("status=%q want %q", store.lastForwardInput.Status, testCase.wantStatus)
			}
			if testCase.wantPub > 0 {
				waitForPublishCalls(t, publisher, testCase.wantPub)
			} else {
				assertNothingPublished(t, publisher)
			}
			if out.Pending != (testCase.wantStatus == domain.MessageStatusPendingLinkScan) {
				t.Fatalf("Pending=%v", out.Pending)
			}
		})
	}
}

// The snapshot that was checked is the snapshot that is written. A concurrent
// edit of the source cannot swap the content between the two.
func TestForwardPersistsExactlyTheSnapshotThatWasChecked(t *testing.T) {
	store := safetyStore(safe("https://example.com/x"))
	snapshot := storage.ForwardSnapshot{
		BodyText: "veja https://example.com/x", BodyFormat: domain.MessageBodyFormatV3,
	}
	store.forwardSnapshot = snapshot
	svc, _ := forwardServiceWith(store)

	if _, err := forwardWith(svc, ""); err != nil {
		t.Fatalf("ForwardChannelMessage: %v", err)
	}
	if store.lastForwardInput.BodyText != snapshot.BodyText ||
		store.lastForwardInput.BodyFormat != snapshot.BodyFormat {
		t.Fatalf("the statement was given a different body: %+v", store.lastForwardInput)
	}
}

// The idempotency correction from the previous round, restated against the new
// gate: a replay is resolved before anything else and never re-checks.
func TestForwardReplayDoesNotConsultTheVerdictTable(t *testing.T) {
	store := safetyStore(map[string]urlsafety.Verdict{
		"https://evil.example/x": urlsafety.VerdictMalicious,
	})
	store.replayMessage = domain.Message{
		ID: "msg-fwd", WorkspaceID: "ws-1", ChannelID: "ch-2", SenderID: user1,
		ForwardedFromMessageID: "aabbccdd-1111-2222-3333-00000000000f",
	}
	svc, publisher := forwardServiceWith(store)

	out, err := forwardWith(svc, "action-1")

	if err != nil {
		t.Fatalf("a persisted forward must stay replayable: %v", err)
	}
	if !out.Replayed || out.Message.ID != "msg-fwd" {
		t.Fatalf("the original was not returned: %+v", out)
	}
	if store.linkVerdictCalls != 0 {
		t.Fatal("a replay consulted the verdict table")
	}
	if store.snapshotCalls != 0 || store.forwardCalls != 0 || publisher.count() != 0 {
		t.Fatal("a replay read, wrote or published")
	}
}

// --- the send path never blocks on the provider -----------------------------

// The whole reason this is asynchronous. The gate consults one store and calls
// nothing else — the store here has no network and no scanner, and a send with a
// brand-new URL still completes.
func TestSendPathTouchesOnlyTheVerdictTable(t *testing.T) {
	store := safetyStore(nil)
	svc, _ := messageServiceWith(store)

	if _, err := createChannelMessage(svc, "veja https://novo.example/x"); err != nil {
		t.Fatalf("CreateChannelMessage: %v", err)
	}
	if store.linkVerdictCalls != 1 {
		t.Fatalf("the verdict table was read %d times", store.linkVerdictCalls)
	}
	if len(store.lastVerdictURLs) != 1 || store.lastVerdictURLs[0] != "https://novo.example/x" {
		t.Fatalf("asked about %v", store.lastVerdictURLs)
	}
}

// Nothing runs when the deployment did not enable the check.
func TestSendIsUnchangedWhenSafetyIsNotWired(t *testing.T) {
	store := safetyStore(nil)
	channels := &fakeChannelStore{visibleChannel: publicActiveChannel("ws-1", "ch-1")}
	svc := service.NewMessageService(channels, &fakeDMStore{}, store)
	publisher := &fakePublisher{}
	svc.SetPublisher(publisher)

	if _, err := createChannelMessage(svc, "veja https://evil.example/login"); err != nil {
		t.Fatalf("CreateChannelMessage: %v", err)
	}
	if store.linkVerdictCalls != 0 {
		t.Fatal("the gate ran while the feature was off")
	}
	waitForPublishCalls(t, publisher, 1)
}

// assertNothingPublished fails if anything was broadcast.
//
// Publishing is detached, so "nothing yet" and "nothing ever" are only the same
// after the queue has had a chance to run. A short settle is the honest way to
// assert an absence against an asynchronous producer.
func assertNothingPublished(t *testing.T, publisher *fakePublisher) {
	t.Helper()
	// No sleeping, and none needed. A publish is enqueued synchronously by the
	// call under test — enqueuePublish takes its slot before returning, and only
	// the delivery is detached — so by the time that call has returned, either an
	// enqueue happened or it never will. Waiting "a bit" to see whether one shows
	// up would be asserting against a race rather than against the code.
	//
	// The positive control for this harness is
	// TestCachedSafeURLPublishesImmediately: the same fake, the same service,
	// observing a publish that does happen. Without it this assertion could pass
	// on a publisher that was never wired.
	if count := publisher.count(); count != 0 {
		t.Fatalf("a withheld or refused message was broadcast: %d", count)
	}
}

// --- create idempotency -----------------------------------------------------
//
// Forwarding has been idempotent since the round that added it; creating was
// not, and RF-21 made that gap matter. A message withheld for a link scan is
// delivered to nobody, so a client with a dropped response has every reason to
// send again — and would otherwise get a second withheld message and a second
// scan for the same URL.

func createWithKey(svc *service.MessageService, body, key string) (domain.Message, error) {
	return svc.CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
		WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1,
		BodyText: body, IdempotencyKey: key,
	})
}

// The replay itself: the original message comes back, nothing is written, and —
// the part that matters for RF-21 — no second scan is queued.
func TestCreateReplayReturnsTheOriginalWithoutRescanning(t *testing.T) {
	store := safetyStore(nil)
	store.createReplayMessage = domain.Message{
		ID: "msg-1", WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1,
		BodyText: "veja https://novo.example/x", Status: domain.MessageStatusPendingLinkScan,
	}
	svc, publisher := messageServiceWith(store)

	message, err := createWithKey(svc, "veja https://novo.example/x", "send-1")

	if err != nil {
		t.Fatalf("a retried send must succeed: %v", err)
	}
	if message.ID != "msg-1" {
		t.Fatalf("a retry created a different message: %+v", message)
	}
	if store.createCalls != 0 {
		t.Fatalf("a retry wrote a second message: createCalls=%d", store.createCalls)
	}
	// No verdict lookup, no scan queued: a replay creates nothing, so it must not
	// spend provider quota.
	if store.linkVerdictCalls != 0 || len(store.ensuredURLs) != 0 {
		t.Fatalf("a retry re-scanned: verdicts=%d scans=%v", store.linkVerdictCalls, store.ensuredURLs)
	}
	// A withheld message is still withheld; nothing new is broadcast.
	assertNothingPublished(t, publisher)
}

// A replay of an already-published message returns it just the same.
func TestCreateReplayReturnsAnActiveMessage(t *testing.T) {
	store := safetyStore(nil)
	store.createReplayMessage = domain.Message{
		ID: "msg-1", WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1,
		BodyText: "ola", Status: domain.MessageStatusActive,
	}
	svc, publisher := messageServiceWith(store)

	message, err := createWithKey(svc, "ola", "send-1")

	if err != nil || message.ID != "msg-1" {
		t.Fatalf("message=%+v err=%v", message, err)
	}
	if store.createCalls != 0 {
		t.Fatal("a retry of a published message wrote a second one")
	}
	// Already published once; a replay must not announce it again.
	assertNothingPublished(t, publisher)
}

// A key reused for different content is a mistake worth reporting, not a silent
// substitution of the old message.
func TestCreateReplayWithADifferentBodyConflicts(t *testing.T) {
	store := safetyStore(nil)
	store.createReplayMessage = domain.Message{
		ID: "msg-1", WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1,
		BodyText: "a primeira mensagem", Status: domain.MessageStatusActive,
	}
	// The stored operation is not the one being retried.
	store.createReplayFingerprint = "fingerprint-of-another-operation"
	svc, _ := messageServiceWith(store)

	_, err := createWithKey(svc, "uma mensagem diferente", "send-1")

	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
	if store.createCalls != 0 {
		t.Fatal("a conflicting key still wrote a message")
	}
}

// The identity of a key includes its destination and its sender, which is what
// stops one from replaying across channels or across users.
func TestCreateReplayIsScopedToSenderAndDestination(t *testing.T) {
	store := safetyStore(nil)
	svc, _ := messageServiceWith(store)

	if _, err := createWithKey(svc, "ola", "send-1"); err != nil {
		t.Fatalf("CreateChannelMessage: %v", err)
	}
	got := store.lastCreateReplayInput
	if got.WorkspaceID != "ws-1" || got.ChannelID != "ch-1" ||
		got.SenderID != user1 || got.IdempotencyKey != "send-1" {
		t.Fatalf("the replay lookup is not fully scoped: %+v", got)
	}
}

// Two identical sends racing collide on the unique index. The index is the
// authority — exactly one message exists — so the loser reads back the winner's
// rather than failing.
func TestConcurrentCreateResolvesToOneMessage(t *testing.T) {
	store := safetyStore(safe("https://example.com/x"))
	// The lookup misses (the winner had not committed yet), the insert collides,
	// and the second lookup finds what the winner wrote.
	store.createErr = storage.ErrCreateReplay
	store.createReplayOnRetry = domain.Message{
		ID: "msg-winner", WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1,
		BodyText: "veja https://example.com/x", Status: domain.MessageStatusActive,
	}
	svc, _ := messageServiceWith(store)

	message, err := createWithKey(svc, "veja https://example.com/x", "send-1")

	if err != nil {
		t.Fatalf("the loser of the race must still get the message: %v", err)
	}
	if message.ID != "msg-winner" {
		t.Fatalf("the loser did not read back the winner's message: %+v", message)
	}
}

// Without a key nothing changes: no lookup, and the send behaves exactly as it
// did before idempotency existed.
func TestCreateWithoutAKeyDoesNotLookUpAReplay(t *testing.T) {
	store := safetyStore(nil)
	svc, _ := messageServiceWith(store)

	if _, err := createChannelMessage(svc, "ola"); err != nil {
		t.Fatalf("CreateChannelMessage: %v", err)
	}
	if store.createReplayCalls != 0 {
		t.Fatalf("a keyless send looked for a replay: %d", store.createReplayCalls)
	}
	if store.createCalls != 1 {
		t.Fatalf("createCalls=%d", store.createCalls)
	}
}

// DMs carry the same contract, keyed by the conversation rather than a channel.
func TestDMCreateIsIdempotent(t *testing.T) {
	store := safetyStore(nil)
	store.createReplayMessage = domain.Message{
		ID: "dm-msg-1", WorkspaceID: "ws-1", SenderID: user1, BodyText: "ola",
	}
	svc, _ := messageServiceWith(store)

	message, err := svc.CreateDMMessage(context.Background(), service.CreateDMMessageInput{
		WorkspaceID: "ws-1", ConversationID: "dm-1", SenderID: user1,
		BodyText: "ola", IdempotencyKey: "send-1",
	})

	if err != nil || message.ID != "dm-msg-1" {
		t.Fatalf("message=%+v err=%v", message, err)
	}
	if store.createCalls != 0 {
		t.Fatal("a retried DM wrote a second message")
	}
	if got := store.lastCreateReplayInput; got.DMConversationID != "dm-1" || got.ChannelID != "" {
		t.Fatalf("the DM replay lookup is keyed wrongly: %+v", got)
	}
}

// --- SEC-1: a withheld message is not editable -----------------------------
//
// The finding: editing was permitted in every state except deleted, so a message
// being withheld for a link scan could be edited — and applying the edit
// broadcast message.updated, carrying the new body, to everyone subscribed to
// the target. That is exactly the delivery the withholding exists to prevent,
// and it also decoupled the content from the scan running against it.

// pendingMessageStore seeds one message withheld for a link scan.
func pendingMessageStore() *fakeMessageStore {
	store := safetyStore(nil)
	store.messagesByKey = map[string]domain.Message{"ws-1:msg-1": {
		ID: "msg-1", WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1,
		Kind: domain.MessageKindUser, Status: domain.MessageStatusPendingLinkScan,
		BodyText: "corpo original",
	}}
	return store
}

// Every observable consequence of the refusal, in one place: the error is the
// deterministic one, nothing was written, nothing was broadcast, and — the part
// that matters for quota — no link was classified and no scan was queued.
func TestEditingAWithheldMessageIsRefusedWithNoSideEffects(t *testing.T) {
	store := pendingMessageStore()
	svc, publisher := messageServiceWith(store)

	_, err := editMessage(svc, "agora com https://novo.example/x")

	if !errors.Is(err, domain.ErrEditForbidden) {
		t.Fatalf("want ErrEditForbidden, got %v", err)
	}
	// No write: the stored body is untouched.
	if store.editedMessage.ID != "" {
		t.Fatalf("a withheld message was edited: %+v", store.editedMessage)
	}
	// No provider work: the classification never ran, so no URL was looked up
	// and no scan was queued.
	if store.linkVerdictCalls != 0 {
		t.Fatalf("a refused edit consulted the verdict table %d times", store.linkVerdictCalls)
	}
	if len(store.ensuredURLs) != 0 {
		t.Fatalf("a refused edit queued scans: %v", store.ensuredURLs)
	}
	// And nothing at all was published — no message.updated, no fan-out.
	assertNothingPublished(t, publisher)
	if updates := publisher.updateCount(); updates != 0 {
		t.Fatalf("a refused edit emitted %d message.updated events", updates)
	}
}

// The refusal does not depend on the body: even an edit with no links at all is
// refused, because the state is what makes it ineligible.
func TestEditingAWithheldMessageIsRefusedEvenWithoutLinks(t *testing.T) {
	store := pendingMessageStore()
	svc, publisher := messageServiceWith(store)

	if _, err := editMessage(svc, "sem nenhum link"); !errors.Is(err, domain.ErrEditForbidden) {
		t.Fatalf("want ErrEditForbidden, got %v", err)
	}
	if publisher.updateCount() != 0 {
		t.Fatal("a refused edit emitted message.updated")
	}
}

// A deleted message stays refused, and an active one stays editable: the
// allowlist changed which states are editable without changing the others.
func TestEditEligibilityByState(t *testing.T) {
	for name, testCase := range map[string]struct {
		status  domain.MessageStatus
		wantErr error
	}{
		"active is editable":    {status: domain.MessageStatusActive},
		"deleted is refused":    {status: domain.MessageStatusDeleted, wantErr: domain.ErrEditForbidden},
		"withheld is refused":   {status: domain.MessageStatusPendingLinkScan, wantErr: domain.ErrEditForbidden},
		"unknown is refused":    {status: domain.MessageStatus("future"), wantErr: domain.ErrEditForbidden},
		"zero value is refused": {status: "", wantErr: domain.ErrEditForbidden},
	} {
		t.Run(name, func(t *testing.T) {
			store := safetyStore(nil)
			store.messagesByKey = map[string]domain.Message{"ws-1:msg-1": {
				ID: "msg-1", WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1,
				Kind: domain.MessageKindUser, Status: testCase.status,
			}}
			svc, _ := messageServiceWith(store)

			_, err := editMessage(svc, "novo corpo")

			if testCase.wantErr == nil {
				if err != nil {
					t.Fatalf("an active message must stay editable: %v", err)
				}
				return
			}
			if !errors.Is(err, testCase.wantErr) {
				t.Fatalf("want %v, got %v", testCase.wantErr, err)
			}
		})
	}
}

// --- CQ-3: the key stands for the whole operation, not just the body -------
//
// The finding: a replay compared only BodyText, so a key reused with the same
// text but a different attachment, format, parent or reference returned the
// original message — the caller believed a new one had been created, and it had
// not.

// createVariant sends with one field of the operation changed.
func createVariant(
	svc *service.MessageService, key string, mutate func(*service.CreateChannelMessageInput),
) (domain.Message, error) {
	input := service.CreateChannelMessageInput{
		WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1,
		BodyText: "mesmo texto", BodyFormat: domain.MessageBodyFormatV2,
		IdempotencyKey: key,
	}
	mutate(&input)
	return svc.CreateChannelMessage(context.Background(), input)
}

// Every field that changes what gets written must break the replay. A key that
// replayed across any of these would silently discard a send.
func TestIdempotencyKeyCoversTheWholeOperation(t *testing.T) {
	for name, mutate := range map[string]func(*service.CreateChannelMessageInput){
		"different body": func(in *service.CreateChannelMessageInput) {
			in.BodyText = "outro texto"
		},
		"different format": func(in *service.CreateChannelMessageInput) {
			in.BodyFormat = domain.MessageBodyFormatV3
		},
		"different parent": func(in *service.CreateChannelMessageInput) {
			in.ParentMessageID = "aabbccdd-1111-2222-3333-000000000001"
		},
		"different reference": func(in *service.CreateChannelMessageInput) {
			in.ReferencedMessageID = "aabbccdd-1111-2222-3333-000000000002"
		},
		"different attachment": func(in *service.CreateChannelMessageInput) {
			in.AttachmentIDs = []string{"aabbccdd-1111-2222-3333-000000000003"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := safetyStore(nil)
			// The stored operation is the unmutated one.
			baseline := createIdentityFingerprintFor(t, func(*service.CreateChannelMessageInput) {})
			store.createReplayMessage = domain.Message{
				ID: "msg-1", WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1,
				BodyText: "mesmo texto", Status: domain.MessageStatusActive,
			}
			store.createReplayFingerprint = baseline
			svc, _ := messageServiceWith(store)

			_, err := createVariant(svc, "send-1", mutate)

			if !errors.Is(err, domain.ErrConflict) {
				t.Fatalf("%s must conflict, got %v", name, err)
			}
			if store.createCalls != 0 {
				t.Fatal("a conflicting key still wrote a message")
			}
		})
	}
}

// createIdentityFingerprintFor recovers the fingerprint the service computes for
// a given operation, by observing what it asks the store about. Computing it in
// the test by hand would be re-implementing the thing under test.
func createIdentityFingerprintFor(
	t *testing.T, mutate func(*service.CreateChannelMessageInput),
) string {
	t.Helper()
	probe := safetyStore(nil)
	svc, _ := messageServiceWith(probe)
	if _, err := createVariant(svc, "probe-key", mutate); err != nil {
		t.Fatalf("probe send: %v", err)
	}
	if probe.lastCreateReplayInput.RequestFingerprint == "" {
		t.Fatal("the service computed no request fingerprint")
	}
	return probe.lastCreateReplayInput.RequestFingerprint
}

// The identical operation still replays: the stricter comparison must not turn
// every retry into a conflict.
func TestIdenticalOperationStillReplays(t *testing.T) {
	store := safetyStore(nil)
	store.createReplayMessage = domain.Message{
		ID: "msg-1", WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1,
		BodyText: "mesmo texto", Status: domain.MessageStatusActive,
	}
	svc, _ := messageServiceWith(store)

	message, err := createVariant(svc, "send-1", func(*service.CreateChannelMessageInput) {})

	if err != nil {
		t.Fatalf("an identical retry must replay: %v", err)
	}
	if message.ID != "msg-1" {
		t.Fatalf("message=%+v", message)
	}
	if store.createCalls != 0 {
		t.Fatal("a replay wrote a second message")
	}
}

// The destination is part of the identity, so the same key in a different
// channel is a different operation rather than a replay of the first.
func TestDestinationIsPartOfTheOperation(t *testing.T) {
	channel := createIdentityFingerprintFor(t, func(*service.CreateChannelMessageInput) {})

	probe := safetyStore(nil)
	svc, _ := messageServiceWith(probe)
	if _, err := svc.CreateDMMessage(context.Background(), service.CreateDMMessageInput{
		WorkspaceID: "ws-1", ConversationID: "dm-1", SenderID: user1,
		BodyText: "mesmo texto", BodyFormat: domain.MessageBodyFormatV2,
		IdempotencyKey: "probe-key",
	}); err != nil {
		t.Fatalf("probe dm send: %v", err)
	}
	if probe.lastCreateReplayInput.RequestFingerprint == channel {
		t.Fatal("a channel and a DM send produced the same operation identity")
	}
}

// ── Reconnect reconciliation ──────────────────────────────────────────────────
//
// Realtime delivery is best-effort: a message.blocked published while its
// author's socket was down reaches nobody, and their client would wait forever
// on an event that already came and went. These cover the service half of the
// recovery path — what it refuses to ask the database, and what it hands over
// when it does.

func TestLinkSafetyStatesAreScopedToTheCallersOwnMessages(t *testing.T) {
	store := safetyStore(nil)
	store.linkSafetyStates = []domain.MessageLinkSafetyState{
		{MessageID: messageID(1), State: domain.LinkSafetyStateBlocked},
	}
	svc, _ := messageServiceWith(store)

	states, err := svc.MessageLinkSafetyStates(context.Background(), service.LinkSafetyStatusInput{
		WorkspaceID: "ws-1", SenderID: user1, MessageIDs: []string{messageID(1)},
	})
	if err != nil {
		t.Fatalf("MessageLinkSafetyStates: %v", err)
	}
	if len(states) != 1 || states[0].State != domain.LinkSafetyStateBlocked {
		t.Fatalf("states = %+v", states)
	}
	// The scoping is not a suggestion the storage layer may ignore: the caller's
	// own id is what reaches it, so there is no argument the handler could pass
	// that would widen the query to somebody else's messages.
	if store.linkSafetyWorkspace != "ws-1" || store.linkSafetySender != user1 {
		t.Fatalf("query scope = %q/%q", store.linkSafetyWorkspace, store.linkSafetySender)
	}
}

func TestLinkSafetyStatesRefusesAnUnboundedBatch(t *testing.T) {
	store := safetyStore(nil)
	svc, _ := messageServiceWith(store)

	oversized := make([]string, service.MaxLinkSafetyStatusBatchSize+1)
	for i := range oversized {
		oversized[i] = uuid.NewString()
	}
	_, err := svc.MessageLinkSafetyStates(context.Background(), service.LinkSafetyStatusInput{
		WorkspaceID: "ws-1", SenderID: user1, MessageIDs: oversized,
	})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("oversized batch = %v, want ErrInvalidInput", err)
	}
	// Refused before any query ran, which is the point of the bound.
	if store.linkSafetyIDs != nil {
		t.Fatalf("the store was queried anyway: %v", store.linkSafetyIDs)
	}
}

func TestLinkSafetyStatesRefusesInputThatCouldNeverNameAMessage(t *testing.T) {
	store := safetyStore(nil)
	svc, _ := messageServiceWith(store)

	for name, ids := range map[string][]string{
		"empty":       {},
		"not a uuid":  {"' OR 1=1 --"},
		"blank entry": {""},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := svc.MessageLinkSafetyStates(context.Background(), service.LinkSafetyStatusInput{
				WorkspaceID: "ws-1", SenderID: user1, MessageIDs: ids,
			})
			if !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("err = %v, want ErrInvalidInput", err)
			}
		})
	}
}

// A client that repeats one id must not multiply the work.
func TestLinkSafetyStatesCollapsesRepeatedIDs(t *testing.T) {
	store := safetyStore(nil)
	svc, _ := messageServiceWith(store)

	id := messageID(1)
	if _, err := svc.MessageLinkSafetyStates(context.Background(), service.LinkSafetyStatusInput{
		WorkspaceID: "ws-1", SenderID: user1, MessageIDs: []string{id, id, id},
	}); err != nil {
		t.Fatalf("MessageLinkSafetyStates: %v", err)
	}
	if len(store.linkSafetyIDs) != 1 {
		t.Fatalf("queried ids = %v, want one", store.linkSafetyIDs)
	}
}

// messageID returns a stable UUID for the nth fixture message.
func messageID(n int) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte{byte(n)}).String()
}

// ── Admission: what a message costs the provider ──────────────────────────────
//
// The security finding, stated as a unit problem: the message limiter counts
// messages, Cloudflare bills scans, and one message may carry ten URLs. So a
// sender could spend ten times their apparent budget by writing ten links — and
// repeat it through create, DM, edit and forward, because each had its own
// limiter and none of them counted the thing that costs.
//
// These fix the unit. What is charged is a canonical URL nobody has a fresh
// verdict or an active job for; everything else is free, because everything else
// is free for the provider too.

// capacityStore builds a store whose admission answers what the test staged.
func capacityStore(result string, verdicts map[string]urlsafety.Verdict) *fakeMessageStore {
	store := safetyStore(verdicts)
	store.admissionResult = result
	return store
}

// [§54] A refusal must leave nothing behind: no message, no job, no partial
// budget spend that a retry would have to pay again.
func TestCapacityRefusalCreatesNothing(t *testing.T) {
	for name, refusal := range map[string]string{
		"workspace budget": storage.AdmissionWorkspaceBudget,
		"global backlog":   storage.AdmissionBacklog,
	} {
		t.Run(name, func(t *testing.T) {
			store := capacityStore(refusal, nil)
			svc, publisher := messageServiceWith(store)

			_, err := createChannelMessage(svc, "veja https://novo.example/a e https://novo.example/b")

			if !errors.Is(err, domain.ErrLinkScanCapacity) {
				t.Fatalf("err = %v, want ErrLinkScanCapacity", err)
			}
			// Not a malicious link. The distinction is the point: a spent window
			// says nothing whatsoever about the content.
			if errors.Is(err, domain.ErrMaliciousURL) {
				t.Fatal("a capacity refusal was reported as a malicious link")
			}
			// Nothing queued, nothing created, nothing announced.
			if len(store.ensuredURLs) != 0 {
				t.Fatalf("jobs were queued anyway: %v", store.ensuredURLs)
			}
			if store.createCalls != 0 {
				t.Fatalf("a message was created: %d calls", store.createCalls)
			}
			if len(publisher.calls) != 0 {
				t.Fatalf("something was published: %v", publisher.calls)
			}
		})
	}
}

// [§52] Ten cleared links with no budget left still send. The budget is a limit
// on new provider work, and this message asks for none.
func TestFreshVerdictsCostNothing(t *testing.T) {
	urls := []string{
		"https://a.example/1", "https://a.example/2", "https://a.example/3",
		"https://a.example/4", "https://a.example/5",
	}
	// Staged as refused: if the message reached the admission at all it would be
	// rejected, so passing proves it never did.
	store := capacityStore(storage.AdmissionWorkspaceBudget, safe(urls...))
	svc, _ := messageServiceWith(store)

	message, err := createChannelMessage(svc, "veja "+strings.Join(urls, " "))

	if err != nil {
		t.Fatalf("a message of cleared links was refused: %v", err)
	}
	if message.Status == domain.MessageStatusPendingLinkScan {
		t.Fatal("a message of cleared links was withheld")
	}
	if store.admittedWorkspace != "" {
		t.Fatal("the admission was consulted for a message that needs no provider work")
	}
}

// [§51] One URL written three times is one URL. Deduplication happens before
// anything is charged, because it happens before anything is scanned.
func TestRepeatingOneURLCostsItOnce(t *testing.T) {
	store := safetyStore(nil)
	svc, _ := messageServiceWith(store)

	if _, err := createChannelMessage(svc,
		"https://novo.example/a https://novo.example/a https://novo.example/a#x"); err != nil {
		t.Fatalf("CreateChannelMessage: %v", err)
	}

	if len(store.ensuredURLs) != 1 {
		t.Fatalf("queued %v, want one canonical URL", store.ensuredURLs)
	}
}

// [§53] A mixed body charges only the parts that need new work. The already-safe
// one is answered from the verdict table and never reaches the admission.
func TestOnlyUnansweredURLsReachTheAdmission(t *testing.T) {
	store := safetyStore(safe("https://known.example/ok"))
	svc, _ := messageServiceWith(store)

	if _, err := createChannelMessage(svc,
		"https://known.example/ok https://novo.example/c https://novo.example/d"); err != nil {
		t.Fatalf("CreateChannelMessage: %v", err)
	}

	if len(store.ensuredURLs) != 2 {
		t.Fatalf("queued %v, want only the two unanswered URLs", store.ensuredURLs)
	}
	for _, url := range store.ensuredURLs {
		if url == "https://known.example/ok" {
			t.Fatal("an already-cleared URL was charged")
		}
	}
}

// [§32, §60, §61] One budget, every door. The bypass this closes is "I spent my
// create budget, so now I edit" — each path had its own limiter and none of them
// counted URLs.
func TestEveryEntryPointSpendsTheSameWorkspaceBudget(t *testing.T) {
	for name, send := range map[string]func(*service.MessageService) error{
		"create channel": func(svc *service.MessageService) error {
			_, err := createChannelMessage(svc, "veja https://novo.example/a")
			return err
		},
		"create dm": func(svc *service.MessageService) error {
			_, err := createDMMessage(svc, "veja https://novo.example/a")
			return err
		},
		"edit": func(svc *service.MessageService) error {
			_, err := svc.EditMessage(context.Background(), service.EditMessageInput{
				WorkspaceID: "ws-1", MessageID: "msg-1", EditorID: user1,
				Body: "veja https://novo.example/a", BodyFormat: domain.MessageBodyFormatV2,
			})
			return err
		},
		"forward": func(svc *service.MessageService) error {
			_, err := svc.ForwardChannelMessage(context.Background(), service.ForwardChannelMessageInput{
				WorkspaceID: "ws-1", DestinationChannelID: "ch-1",
				ActorID: user1, SourceMessageID: "msg-src",
			})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := capacityStore(storage.AdmissionWorkspaceBudget, nil)
			// The edit and forward paths read an existing message first; both are
			// staged with a body carrying the same unscanned link, so all four
			// doors ask for exactly the same new provider work.
			store.forwardSnapshot = storage.ForwardSnapshot{
				BodyText: "veja https://novo.example/a", BodyFormat: domain.MessageBodyFormatV2,
			}
			store.messagesByKey = map[string]domain.Message{
				"ws-1:msg-1": {
					ID: "msg-1", WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1,
					Kind: domain.MessageKindUser, Status: domain.MessageStatusActive,
					BodyText: "antes", BodyFormat: domain.MessageBodyFormatV2,
				},
			}
			svc, _ := messageServiceWith(store)

			err := send(svc)

			if !errors.Is(err, domain.ErrLinkScanCapacity) {
				t.Fatalf("err = %v, want ErrLinkScanCapacity", err)
			}
			// [§84] And the assertion that matters operationally: a refused
			// operation never reaches the provider, because it never creates a job
			// for the worker to pick up.
			if len(store.ensuredURLs) != 0 {
				t.Fatalf("jobs were queued for a refused operation: %v", store.ensuredURLs)
			}
			// The budget is keyed by workspace, not by endpoint. Every door
			// consults the same one.
			if store.admittedWorkspace != "ws-1" {
				t.Fatalf("admission scope = %q, want the workspace", store.admittedWorkspace)
			}
		})
	}
}

// The capacity the service was configured with is what reaches the store — a
// budget nobody passes down is not a budget.
func TestConfiguredCapacityReachesTheAdmission(t *testing.T) {
	store := safetyStore(nil)
	svc, _ := messageServiceWith(store)
	svc.SetLinkScanCapacity(storage.LinkScanCapacity{
		WorkspaceNewURLBudget: 7, BudgetWindow: time.Minute, MaxPendingJobs: 42,
	})

	if _, err := createChannelMessage(svc, "veja https://novo.example/a"); err != nil {
		t.Fatalf("CreateChannelMessage: %v", err)
	}

	if store.admittedCapacity.WorkspaceNewURLBudget != 7 ||
		store.admittedCapacity.MaxPendingJobs != 42 ||
		store.admittedCapacity.BudgetWindow != time.Minute {
		t.Fatalf("capacity = %+v", store.admittedCapacity)
	}
}
