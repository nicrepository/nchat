package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nicrepository/nchat/libs/go/platform/observability"
	"github.com/nicrepository/nchat/libs/go/platform/urlsafety"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
)

// Reading the body the way the reader will.
//
// Bodies in the v2/v3 grammars are stored with backslash escapes, so a link
// written as `my\-site.example` renders as `my-site.example`. Scanning the raw
// string would simply not see it, which would make "escape a hyphen" a bypass
// anyone could find — and the escaping is applied by the client, so an attacker
// controls it directly.

// The bypass, closed: an escaped link is unescaped first and then scanned like
// any other.
func TestAnEscapedURLIsStillChecked(t *testing.T) {
	store := safetyStore(map[string]urlsafety.Verdict{
		"https://my-site.example/login": urlsafety.VerdictMalicious,
	})
	svc, publisher := messageServiceWith(store)

	_, err := createChannelMessage(svc, `veja https://my\-site.example/login`)

	if !errors.Is(err, domain.ErrMaliciousURL) {
		t.Fatalf("an escaped malicious link was not recognised: %v", err)
	}
	if store.createCalls != 0 || publisher.count() != 0 {
		t.Fatal("an escaped malicious link produced a message")
	}
}

// The other direction, and the reason the rule is "only what the grammar
// escapes": unescaping must not manufacture a link no reader would ever be
// shown. `http:\/\/` does not render as a URL and must not become one here.
func TestUnescapingDoesNotManufactureALink(t *testing.T) {
	store := safetyStore(nil)
	svc, _ := messageServiceWith(store)

	if _, err := createChannelMessage(svc, `nao clique em http:\/\/evil.example/x`); err != nil {
		t.Fatalf("CreateChannelMessage: %v", err)
	}
	if store.linkVerdictCalls != 0 {
		t.Fatal("a backslash sequence that renders as text was treated as a link")
	}

	// Same rule inside the host: a dot the grammar does not escape keeps its
	// backslash, so `example\.example` names nothing and is not a link either.
	if _, err := createChannelMessage(svc, `veja https://example\.example/x`); err != nil {
		t.Fatalf("CreateChannelMessage: %v", err)
	}
	if store.linkVerdictCalls != 0 {
		t.Fatal("an unescapable sequence was turned into a host")
	}
}

// The conditional escape the client writes only after a digit, so "1.5" survives
// a round trip through the list grammar. The predicate looks at what has already
// been emitted, exactly as the client's unescape does.
func TestTheConditionalDotEscapeIsUnescapedLikeTheClientDoes(t *testing.T) {
	store := safetyStore(nil)
	svc, _ := messageServiceWith(store)

	// A dot escaped after a digit is what the list grammar writes so "1.5"
	// survives a round trip, so it is unescaped and the URL it hides is scanned.
	if _, err := createChannelMessage(svc, `preco 1\.5 em https://example.com/1\.5`); err != nil {
		t.Fatalf("CreateChannelMessage: %v", err)
	}
	if len(store.ensuredURLs) != 1 || store.ensuredURLs[0] != "https://example.com/1.5" {
		t.Fatalf("scanned %v, want the escaped dot resolved the way the reader sees it",
			store.ensuredURLs)
	}
	if store.lastCreateInput.Status != domain.MessageStatusPendingLinkScan {
		t.Fatalf("status = %q, want the unscanned link withheld", store.lastCreateInput.Status)
	}
}

// A trailing scheme fragment is shorter than the prefix it is compared against.
// It is not a link and must not refuse the message.
func TestATruncatedSchemeIsNotALink(t *testing.T) {
	store := safetyStore(nil)
	svc, _ := messageServiceWith(store)

	if _, err := createChannelMessage(svc, "termina em http"); err != nil {
		t.Fatalf("CreateChannelMessage: %v", err)
	}
	if store.linkVerdictCalls != 0 {
		t.Fatal("a truncated scheme was treated as a link")
	}
}

// The bootstrap assertion: "the flag was on and the gate is absent" is the exact
// state RF-21 must never reach, and this is the only way to observe it without
// sending a message through the whole service.
func TestHasLinkSafetyReportsTheWiring(t *testing.T) {
	store := safetyStore(nil)
	svc, _ := messageServiceWith(store)
	if !svc.HasLinkSafety() {
		t.Fatal("the gate reported itself absent after being wired")
	}

	// Optional, like every other reporter: attaching counters changes no
	// decision, and a nil receiver is a supported deployment.
	svc.SetAdmissionMetrics(urlsafety.NewPipelineMetrics(
		observability.NewMetrics(observability.Config{
			ServiceName: "chat-service", MetricsEnabled: true,
		}), "chat-service"))
	if _, err := createChannelMessage(svc, "veja https://example.com/y"); err != nil {
		t.Fatalf("CreateChannelMessage: %v", err)
	}

	var unwired *service.MessageService
	if unwired.HasLinkSafety() {
		t.Fatal("a nil service reported a gate")
	}
}

// A cancelled request is a cancelled request, not a provider outage. Reporting
// it as one would make every shutdown look like a Cloudflare incident.
func TestACancelledSendIsNotReportedAsAnOutage(t *testing.T) {
	for name, prepare := range map[string]func(*fakeMessageStore){
		"the verdict read is interrupted": func(s *fakeMessageStore) {
			s.linkVerdictErr = errors.New("interrupted")
		},
		"the admission is interrupted": func(s *fakeMessageStore) {
			s.ensureScansErr = errors.New("interrupted")
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := safetyStore(nil)
			prepare(store)
			svc, _ := messageServiceWith(store)

			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			_, err := svc.CreateChannelMessage(ctx, service.CreateChannelMessageInput{
				WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1,
				BodyText: "veja https://example.com/x",
			})

			if !errors.Is(err, context.Canceled) {
				t.Fatalf("err = %v, want the cancellation reported as itself", err)
			}
			if store.createCalls != 0 {
				t.Fatal("a cancelled send created a message")
			}
		})
	}
}
