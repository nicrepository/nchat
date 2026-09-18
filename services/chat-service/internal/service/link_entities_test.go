package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// The per-link read model (issue #807): what a page of messages carries about
// its links, derived from the body and two batch reads, and how a condemned
// span is withheld while the rest of the text survives.

type fakeLinkEntityStore struct {
	targets    map[string]storage.LinkTargetState
	previews   map[string]storage.LinkPreviewRow
	bodies     map[string]string
	targetErr  error
	previewErr error
	bodiesErr  error

	askedURLs      []string
	askedPreviews  []string
	askedWorkspace string
	askedBodies    []string
	queued         map[string][]string
}

func (f *fakeLinkEntityStore) LoadLinkTargets(_ context.Context, urls []string) (map[string]storage.LinkTargetState, error) {
	f.askedURLs = append(f.askedURLs, urls...)
	if f.targetErr != nil {
		return nil, f.targetErr
	}
	out := map[string]storage.LinkTargetState{}
	for _, url := range urls {
		if target, ok := f.targets[url]; ok {
			out[url] = target
		}
	}
	return out, nil
}

func (f *fakeLinkEntityStore) LoadLinkPreviews(_ context.Context, workspaceID string, urls []string) (map[string]storage.LinkPreviewRow, error) {
	f.askedWorkspace = workspaceID
	f.askedPreviews = append(f.askedPreviews, urls...)
	if f.previewErr != nil {
		return nil, f.previewErr
	}
	out := map[string]storage.LinkPreviewRow{}
	for _, url := range urls {
		if preview, ok := f.previews[url]; ok {
			out[url] = preview
		}
	}
	return out, nil
}

func (f *fakeLinkEntityStore) LoadMessageBodies(_ context.Context, ids []string) (map[string]string, error) {
	f.askedBodies = append(f.askedBodies, ids...)
	if f.bodiesErr != nil {
		return nil, f.bodiesErr
	}
	out := map[string]string{}
	for _, id := range ids {
		if body, ok := f.bodies[id]; ok {
			out[id] = body
		}
	}
	return out, nil
}

func (f *fakeLinkEntityStore) QueueLinkPreviews(_ context.Context, workspaceID string, urls []string) error {
	if f.queued == nil {
		f.queued = map[string][]string{}
	}
	f.queued[workspaceID] = append(f.queued[workspaceID], urls...)
	return nil
}

func target(status string, fresh bool) storage.LinkTargetState {
	return storage.LinkTargetState{Status: status, Fresh: fresh, UpdatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func userMessage(id, body string) domain.Message {
	return domain.Message{ID: id, Kind: domain.MessageKindUser, BodyText: body, BodyFormat: domain.MessageBodyFormatV2}
}

func TestFindLinkOccurrencesKeepsTextAndCanonicalTarget(t *testing.T) {
	occ := findLinkOccurrences(`veja https://Example.com/A#frag e http://192.0.2.1/x e https://my\-site.example/p`)
	if len(occ) != 2 {
		t.Fatalf("occurrences = %+v", occ)
	}
	if occ[0].text != "https://Example.com/A#frag" || occ[0].canonical != "https://example.com/A" {
		t.Fatalf("first = %+v", occ[0])
	}
	// The escaped body is unescaped the way the reader sees it, and the IP literal
	// is not a link at all.
	if occ[1].text != "https://my-site.example/p" || occ[1].canonical != "https://my-site.example/p" {
		t.Fatalf("second = %+v", occ[1])
	}
}

func TestLinkSafetyForMapsEveryStoredStatus(t *testing.T) {
	cases := map[string]domain.LinkSafety{
		"safe": domain.LinkSafetySafe, "malicious": domain.LinkSafetyMalicious,
		"inconclusive": domain.LinkSafetyUnknown, "unknown": domain.LinkSafetyUnknown,
		"pending": domain.LinkSafetyPending, "": domain.LinkSafetyPending, "future": domain.LinkSafetyPending,
	}
	for status, want := range cases {
		if got := linkSafetyFor(target(status, true)); got != want {
			t.Errorf("%q -> %q, want %q", status, got, want)
		}
	}
}

func TestLinkForAppliesThePolicy(t *testing.T) {
	occ := linkOccurrence{text: "https://Example.com/a", canonical: "https://example.com/a"}

	safe := linkFor(3, occ, target("safe", true))
	if safe.Ordinal != 3 || safe.Href != "https://example.com/a" || safe.Click != domain.LinkClickDirect ||
		safe.Hostname != "example.com" || safe.Text != "https://Example.com/a" || safe.URL != "https://example.com/a" {
		t.Fatalf("safe = %+v", safe)
	}
	unknown := linkFor(0, occ, target("unknown", true))
	if unknown.Href != "" || unknown.Click != domain.LinkClickInterstitial || unknown.URL == "" {
		t.Fatalf("unknown = %+v", unknown)
	}
	pending := linkFor(0, occ, storage.LinkTargetState{})
	if pending.Href != "" || pending.Click != domain.LinkClickNone || pending.Safety != domain.LinkSafetyPending {
		t.Fatalf("pending = %+v", pending)
	}
	// A condemned link carries nothing a client could turn into a destination —
	// but it keeps the target's identity, which is how its release is matched.
	malicious := linkFor(0, occ, target("malicious", true))
	if malicious.Href != "" || malicious.URL != "" || malicious.Text != "" || malicious.Hostname != "" ||
		malicious.Click != domain.LinkClickNone || malicious.Safety != domain.LinkSafetyMalicious {
		t.Fatalf("malicious = %+v", malicious)
	}
	if malicious.TargetKey == "" || malicious.TargetKey != pending.TargetKey || malicious.TargetKey != domain.LinkTargetKey(occ.canonical) {
		t.Fatalf("identity = %q, want the target key every occurrence of %q carries", malicious.TargetKey, occ.canonical)
	}
}

// Every safe target with a preview is hydrated (issue #807 CQ round 2): the
// two-card limit is the client's presentation rule, not a property of the data,
// so a fourth target's card is described exactly like the first's.
func TestBuildMessageLinksHydratesEverySafePreview(t *testing.T) {
	occ := []linkOccurrence{
		{text: "https://a.test/", canonical: "https://a.test/"},
		{text: "https://a.test/", canonical: "https://a.test/"},
		{text: "https://b.test/", canonical: "https://b.test/"},
		{text: "https://c.test/", canonical: "https://c.test/"},
		{text: "https://d.test/", canonical: "https://d.test/"},
		{text: "https://e.test/", canonical: "https://e.test/"},
	}
	targets := map[string]storage.LinkTargetState{
		"https://a.test/": target("safe", true), "https://b.test/": target("unknown", true),
		"https://c.test/": target("safe", true), "https://d.test/": target("safe", true),
		"https://e.test/": target("safe", true),
	}
	ready := func(url string) storage.LinkPreviewRow {
		return storage.LinkPreviewRow{ID: "p-" + url, CanonicalURL: url, State: "ready", Title: "T", HasImage: true, ImageWidth: 4, ImageHeight: 2}
	}
	previews := map[string]storage.LinkPreviewRow{
		"https://a.test/": ready("https://a.test/"), "https://b.test/": ready("https://b.test/"),
		"https://c.test/": ready("https://c.test/"), "https://d.test/": ready("https://d.test/"),
		"https://e.test/": ready("https://e.test/"),
	}

	links := buildMessageLinks(occ, targets, previews)

	if len(links) != 6 {
		t.Fatalf("links = %d", len(links))
	}
	withCard := 0
	for i, link := range links {
		if link.Ordinal != i {
			t.Fatalf("ordinal %d = %d", i, link.Ordinal)
		}
		if link.Preview != nil {
			withCard++
		}
	}
	// a (both occurrences), c, d and e — four distinct safe targets, every one
	// with its preview. b is unknown and never carries one.
	if withCard != 5 || links[0].Preview == nil || links[1].Preview == nil || links[2].Preview != nil ||
		links[3].Preview == nil || links[4].Preview == nil || links[5].Preview == nil {
		t.Fatalf("cards = %d: %+v", withCard, links)
	}
	if got := links[0].Preview; got.ImageID != "p-https://a.test/" || got.Hostname != "a.test" || got.Title != "T" || got.ImageWidth != 4 {
		t.Fatalf("card = %+v", got)
	}
	// A preview that is not ready carries its state and hostname, nothing else.
	queued := previewFor(storage.LinkPreviewRow{CanonicalURL: "https://x.test/p", State: "queued", Title: "hidden"})
	if queued.State != domain.LinkPreviewQueued || queued.Hostname != "x.test" || queued.Title != "" {
		t.Fatalf("queued = %+v", queued)
	}
}

func TestRedactCondemnedLinksWithholdsOnlyTheCondemnedSpans(t *testing.T) {
	const bad = "https://evil.example/v1.5/login"
	targets := map[string]storage.LinkTargetState{
		bad: target("malicious", true), "https://good.example/a": target("safe", true),
	}
	occ := findLinkOccurrences("x " + bad + " y")

	cases := map[string]string{
		"plain":            "clique " + bad + " e https://good.example/a fim",
		"twice":            bad + " " + bad,
		"trailing period":  "veja " + bad + ".",
		"escaped grammar":  `veja https://evil.example/v1\.5/login agora`,
		"inside markup":    "**" + bad + "**",
		"fragment variant": "veja " + bad + "#section",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			got := redactCondemnedLinks(body, occ, targets)
			if strings.Contains(got, "evil") {
				t.Fatalf("condemned url survived redaction: %q", got)
			}
			if !strings.Contains(got, domain.LinkBlockedMarker) {
				t.Fatalf("no marker in %q", got)
			}
			if strings.Contains(body, "good.example") && !strings.Contains(got, "https://good.example/a") {
				t.Fatalf("a safe url was redacted: %q", got)
			}
		})
	}
	// Nothing condemned: the body is returned untouched.
	if got := redactCondemnedLinks("veja https://good.example/a", findLinkOccurrences("https://good.example/a"), targets); got != "veja https://good.example/a" {
		t.Fatalf("untouched body changed: %q", got)
	}
}

func TestHydrateMessageLinksReadsTargetsAndPreviewsInBatches(t *testing.T) {
	store := &fakeLinkEntityStore{
		targets: map[string]storage.LinkTargetState{
			"https://a.test/": target("safe", true), "https://b.test/": target("pending", false),
		},
		previews: map[string]storage.LinkPreviewRow{
			"https://a.test/": {ID: "p1", CanonicalURL: "https://a.test/", State: "ready", Title: "A"},
		},
	}
	svc := &MessageService{}
	svc.SetLinkEntities(store)
	svc.SetLinkPreviewEnabled(true)
	messages := []domain.Message{
		userMessage("m1", "veja https://a.test/ e https://b.test/"),
		userMessage("m2", "sem links"),
		userMessage("m3", "de novo https://a.test/"),
		{ID: "m4", Kind: domain.MessageKindSystem, BodyText: "https://a.test/"},
	}

	if err := svc.hydrateMessageLinks(context.Background(), "ws-1", messages); err != nil {
		t.Fatalf("hydrateMessageLinks: %v", err)
	}
	if len(store.askedURLs) != 2 || store.askedWorkspace != "ws-1" || len(store.askedPreviews) != 1 {
		t.Fatalf("batch reads: urls=%v previews=%v ws=%q", store.askedURLs, store.askedPreviews, store.askedWorkspace)
	}
	if len(messages[0].Links) != 2 || messages[0].Links[0].Href != "https://a.test/" || messages[0].Links[0].Preview == nil ||
		messages[0].Links[1].Safety != domain.LinkSafetyPending {
		t.Fatalf("m1 links = %+v", messages[0].Links)
	}
	if messages[1].Links != nil || messages[3].Links != nil {
		t.Fatal("a message without user links must carry none")
	}
	if len(messages[2].Links) != 1 || messages[2].Links[0].Preview == nil {
		t.Fatalf("m3 links = %+v", messages[2].Links)
	}
}

func TestHydrateMessageLinksRedactsACondemnedBodyTheProjectionWithheld(t *testing.T) {
	store := &fakeLinkEntityStore{
		targets: map[string]storage.LinkTargetState{"https://evil.test/x": target("malicious", true), "https://ok.test/": target("safe", true)},
		bodies:  map[string]string{"m1": "cuidado https://evil.test/x mas https://ok.test/ serve"},
	}
	svc := &MessageService{}
	svc.SetLinkEntities(store)
	messages := []domain.Message{{
		ID: "m1", Kind: domain.MessageKindUser, BodyText: "", LinkSafety: domain.MessageLinkSafetyMalicious,
	}}

	if err := svc.hydrateMessageLinks(context.Background(), "ws-1", messages); err != nil {
		t.Fatalf("hydrateMessageLinks: %v", err)
	}
	if strings.Contains(messages[0].BodyText, "evil") || !strings.Contains(messages[0].BodyText, "https://ok.test/") {
		t.Fatalf("body = %q", messages[0].BodyText)
	}
	if len(messages[0].Links) != 2 || messages[0].Links[0].Safety != domain.LinkSafetyMalicious || messages[0].Links[1].Href != "https://ok.test/" {
		t.Fatalf("links = %+v", messages[0].Links)
	}

	// A failed body read keeps the projection's withholding: nothing is shown.
	store.bodiesErr = errors.New("db down")
	withheld := []domain.Message{{ID: "m1", Kind: domain.MessageKindUser, LinkSafety: domain.MessageLinkSafetyMalicious}}
	if err := svc.hydrateMessageLinks(context.Background(), "ws-1", withheld); err != nil || withheld[0].BodyText != "" || withheld[0].Links != nil {
		t.Fatalf("withheld = %+v (%v)", withheld[0], err)
	}
}

func TestHydrateMessageLinksServesNoPreviewWhenDisabledOrUnsafe(t *testing.T) {
	store := &fakeLinkEntityStore{
		targets:  map[string]storage.LinkTargetState{"https://a.test/": target("unknown", true)},
		previews: map[string]storage.LinkPreviewRow{"https://a.test/": {ID: "p1", CanonicalURL: "https://a.test/", State: "ready"}},
	}
	svc := &MessageService{}
	svc.SetLinkEntities(store)
	svc.SetLinkPreviewEnabled(true)
	messages := []domain.Message{userMessage("m1", "https://a.test/")}
	if err := svc.hydrateMessageLinks(context.Background(), "ws-1", messages); err != nil {
		t.Fatalf("hydrateMessageLinks: %v", err)
	}
	if messages[0].Links[0].Preview != nil || len(store.askedPreviews) != 0 {
		t.Fatal("a preview for a target that is not safe was served or even read")
	}

	store.targets["https://a.test/"] = target("safe", true)
	svc.SetLinkPreviewEnabled(false)
	messages = []domain.Message{userMessage("m1", "https://a.test/")}
	if err := svc.hydrateMessageLinks(context.Background(), "ws-1", messages); err != nil {
		t.Fatalf("hydrateMessageLinks: %v", err)
	}
	if messages[0].Links[0].Preview != nil || messages[0].Links[0].Href == "" {
		t.Fatal("preview disabled must leave the anchor and drop the card")
	}
}

func TestHydrateMessageLinksReportsReadFailures(t *testing.T) {
	svc := &MessageService{}
	store := &fakeLinkEntityStore{targetErr: errors.New("boom")}
	svc.SetLinkEntities(store)
	if err := svc.hydrateMessageLinks(context.Background(), "ws-1", []domain.Message{userMessage("m1", "https://a.test/")}); err == nil {
		t.Fatal("a target read failure must surface")
	}
	store = &fakeLinkEntityStore{targets: map[string]storage.LinkTargetState{"https://a.test/": target("safe", true)}, previewErr: errors.New("boom")}
	svc.SetLinkEntities(store)
	svc.SetLinkPreviewEnabled(true)
	if err := svc.hydrateMessageLinks(context.Background(), "ws-1", []domain.Message{userMessage("m1", "https://a.test/")}); err == nil {
		t.Fatal("a preview read failure must surface")
	}
	// No store at all: nothing to hydrate, nothing fails.
	if err := (&MessageService{}).hydrateMessageLinks(context.Background(), "ws-1", []domain.Message{userMessage("m1", "https://a.test/")}); err != nil {
		t.Fatalf("unwired: %v", err)
	}
}

func TestQueueLinkPreviewsOnlyWhenEnabled(t *testing.T) {
	store := &fakeLinkEntityStore{}
	svc := &MessageService{}
	svc.SetLinkEntities(store)
	svc.queueLinkPreviews(context.Background(), "ws-1", []string{"https://a.test/"})
	if len(store.queued) != 0 {
		t.Fatal("previews queued with the feature off")
	}
	svc.SetLinkPreviewEnabled(true)
	svc.queueLinkPreviews(context.Background(), "ws-1", []string{"https://a.test/"})
	svc.queueLinkPreviews(context.Background(), "ws-1", nil)
	if got := store.queued["ws-1"]; len(got) != 1 || got[0] != "https://a.test/" {
		t.Fatalf("queued = %v", store.queued)
	}
}

func TestLinkAccessPolicy(t *testing.T) {
	for safety, want := range map[domain.LinkSafety]struct {
		click   domain.LinkClick
		preview bool
	}{
		domain.LinkSafetySafe:      {domain.LinkClickDirect, true},
		domain.LinkSafetyUnknown:   {domain.LinkClickInterstitial, false},
		domain.LinkSafetyPending:   {domain.LinkClickNone, false},
		domain.LinkSafetyMalicious: {domain.LinkClickNone, false},
		domain.LinkSafety("new"):   {domain.LinkClickNone, false},
	} {
		click, preview := domain.LinkAccess(safety)
		if click != want.click || preview != want.preview {
			t.Errorf("%s: (%s, %v), want (%s, %v)", safety, click, preview, want.click, want.preview)
		}
	}
}
