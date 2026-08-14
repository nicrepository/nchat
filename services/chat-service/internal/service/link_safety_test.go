package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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
	for i := 0; i < 20; i++ {
		if publisher.count() != 0 {
			t.Fatalf("a withheld or refused message was broadcast: %d", publisher.count())
		}
		time.Sleep(time.Millisecond)
	}
}
