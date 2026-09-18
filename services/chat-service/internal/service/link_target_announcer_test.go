package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// Convergence of one target into its messages (issue #807).

type fakeTargetIndex struct {
	targets  map[string]storage.LinkTargetState
	refs     []storage.LinkReference
	previews map[string]map[string]storage.LinkPreviewRow // workspace -> url -> row
	spaces   []string

	queued   map[string][]string
	revoked  []string
	refresh  int
	refsErr  error
	targetOK bool

	// The durable continuation, in memory: one row per key, leased while a
	// page runs. failPagesAfter makes the reference listing fail once a number
	// of pages have been served, to stage a failure in the middle.
	fanouts        map[string]*fakeFanout
	nextFanoutID   int
	nextClaimID    int
	pagesServed    int
	failPagesAfter int
}

type fakeFanout struct {
	storage.LinkFanout
	leased   bool
	finished bool
}

func newFakeTargetIndex() *fakeTargetIndex {
	return &fakeTargetIndex{
		targets: map[string]storage.LinkTargetState{}, previews: map[string]map[string]storage.LinkPreviewRow{},
		queued: map[string][]string{}, targetOK: true, fanouts: map[string]*fakeFanout{}, failPagesAfter: -1,
	}
}

// mintClaim hands the row to a new claimant; every earlier claim is stale.
func (f *fakeTargetIndex) mintClaim(row *fakeFanout) {
	f.nextClaimID++
	row.ClaimID = fmt.Sprint("claim-", f.nextClaimID)
	row.leased = true
}

func (f *fakeTargetIndex) BeginLinkFanout(_ context.Context, url, workspace, kind string) (storage.LinkFanout, error) {
	key := url + "|" + workspace + "|" + kind
	row, ok := f.fanouts[key]
	if !ok {
		f.nextFanoutID++
		row = &fakeFanout{LinkFanout: storage.LinkFanout{ID: fmt.Sprint("f", f.nextFanoutID), CanonicalURL: url, WorkspaceID: workspace, Kind: kind}}
		f.fanouts[key] = row
	}
	row.AfterMessageID, row.Attempts, row.finished = "", 0, false
	f.mintClaim(row)
	return row.LinkFanout, nil
}

func (f *fakeTargetIndex) ClaimDueLinkFanouts(_ context.Context, limit int) ([]storage.LinkFanout, error) {
	var due []storage.LinkFanout
	for _, row := range f.fanouts {
		if row.leased || row.finished || len(due) == limit {
			continue
		}
		row.Attempts++
		f.mintClaim(row)
		due = append(due, row.LinkFanout)
	}
	return due, nil
}

func (f *fakeTargetIndex) fanoutByID(id string) *fakeFanout {
	for _, row := range f.fanouts {
		if row.ID == id {
			return row
		}
	}
	return nil
}

func (f *fakeTargetIndex) AdvanceLinkFanout(_ context.Context, claim storage.LinkFanout, after string) error {
	row := f.fanoutByID(claim.ID)
	if row.finished || row.ClaimID != claim.ClaimID {
		return storage.ErrLinkFanoutConflict
	}
	row.AfterMessageID, row.Attempts, row.leased, row.ClaimID = after, 0, false, ""
	return nil
}

func (f *fakeTargetIndex) FinishLinkFanout(_ context.Context, claim storage.LinkFanout) error {
	row := f.fanoutByID(claim.ID)
	if row.finished || row.ClaimID != claim.ClaimID {
		return storage.ErrLinkFanoutConflict
	}
	row.finished = true
	return nil
}

// releaseLeases is the lease lapsing: what a later pass sees.
func (f *fakeTargetIndex) releaseLeases() {
	for _, row := range f.fanouts {
		row.leased = false
	}
}

func (f *fakeTargetIndex) openFanouts() int {
	open := 0
	for _, row := range f.fanouts {
		if !row.finished {
			open++
		}
	}
	return open
}

func (f *fakeTargetIndex) RefreshMessageLinkSafety(context.Context, string) ([]storage.MessageLinkSafetyChange, error) {
	f.refresh++
	return nil, nil
}

func (f *fakeTargetIndex) LoadLinkTargets(_ context.Context, urls []string) (map[string]storage.LinkTargetState, error) {
	if !f.targetOK {
		return nil, errors.New("boom")
	}
	out := map[string]storage.LinkTargetState{}
	for _, url := range urls {
		if t, ok := f.targets[url]; ok {
			out[url] = t
		}
	}
	return out, nil
}

func (f *fakeTargetIndex) MessagesReferencingLink(_ context.Context, url, workspace, after string, limit int) ([]storage.LinkReference, error) {
	if f.refsErr != nil {
		return nil, f.refsErr
	}
	if f.pagesServed == f.failPagesAfter {
		f.failPagesAfter = -1
		return nil, errors.New("page failed")
	}
	f.pagesServed++
	var page []storage.LinkReference
	passed := after == ""
	for _, ref := range f.refs {
		if workspace != "" && ref.WorkspaceID != workspace {
			continue
		}
		if !passed {
			passed = ref.MessageID == after
			continue
		}
		page = append(page, ref)
		if len(page) == limit {
			break
		}
	}
	return page, nil
}

func (f *fakeTargetIndex) LinkWorkspacesReferencing(context.Context, string) ([]string, error) {
	return f.spaces, nil
}

func (f *fakeTargetIndex) LoadLinkPreviews(_ context.Context, workspace string, urls []string) (map[string]storage.LinkPreviewRow, error) {
	out := map[string]storage.LinkPreviewRow{}
	for _, url := range urls {
		if row, ok := f.previews[workspace][url]; ok {
			out[url] = row
		}
	}
	return out, nil
}

func (f *fakeTargetIndex) QueueLinkPreviews(_ context.Context, workspace string, urls []string) error {
	f.queued[workspace] = append(f.queued[workspace], urls...)
	return nil
}

func (f *fakeTargetIndex) RevokeLinkPreviews(_ context.Context, url string) ([]storage.LinkPreviewRow, error) {
	f.revoked = append(f.revoked, url)
	return nil, nil
}

type recordedLinkUpdate struct {
	workspace, targetType, targetID, messageID string
	link                                       domain.MessageLink
}

type fakeLinkUpdatePublisher struct{ updates []recordedLinkUpdate }

func (p *fakeLinkUpdatePublisher) PublishMessageLinkUpdated(_ context.Context, workspaceID, targetType, targetID, messageID string, link domain.MessageLink) {
	p.updates = append(p.updates, recordedLinkUpdate{workspaceID, targetType, targetID, messageID, link})
}

const announcedURL = "https://announce.example/page"

func TestAnnounceClearedTargetFansOutPerWorkspaceWithItsOwnPreview(t *testing.T) {
	index := newFakeTargetIndex()
	index.targets[announcedURL] = target("safe", true)
	index.refs = []storage.LinkReference{
		{MessageID: "m1", WorkspaceID: "ws-a", TargetType: storage.TargetChannel, TargetID: "ch-1"},
		{MessageID: "m2", WorkspaceID: "ws-b", TargetType: storage.TargetDM, TargetID: "dm-1"},
		{MessageID: "m3", WorkspaceID: "ws-a", TargetType: storage.TargetChannel, TargetID: "ch-2"},
	}
	index.spaces = []string{"ws-a", "ws-b"}
	index.previews["ws-a"] = map[string]storage.LinkPreviewRow{announcedURL: {ID: "p-a", CanonicalURL: announcedURL, State: "ready", Title: "Only A"}}
	publisher := &fakeLinkUpdatePublisher{}
	announcer := NewLinkTargetAnnouncer(index, publisher, nil, nil)
	announcer.SetPreviewEnabled(true)

	announcer.Announce(context.Background(), announcedURL)

	if len(publisher.updates) != 3 {
		t.Fatalf("updates = %+v", publisher.updates)
	}
	byMessage := map[string]recordedLinkUpdate{}
	for _, update := range publisher.updates {
		byMessage[update.messageID] = update
	}
	if a := byMessage["m1"]; a.link.Href != announcedURL || a.link.Preview == nil || a.link.Preview.Title != "Only A" || a.targetID != "ch-1" {
		t.Fatalf("ws-a update = %+v", a)
	}
	// ws-b has no preview row of its own and must not see ws-a's.
	if b := byMessage["m2"]; b.link.Href != announcedURL || b.link.Preview != nil || b.targetType != storage.TargetDM {
		t.Fatalf("ws-b update = %+v", b)
	}
	if byMessage["m1"].link.Text != "" || byMessage["m1"].link.Ordinal != 0 {
		t.Fatal("a target-level payload carries no per-occurrence fields")
	}
	// A clearance queues a preview in every workspace naming the URL.
	if len(index.queued["ws-a"]) != 1 || len(index.queued["ws-b"]) != 1 || len(index.revoked) != 0 {
		t.Fatalf("queued = %v revoked = %v", index.queued, index.revoked)
	}
	if index.refresh == 0 {
		t.Fatal("the aggregate marker must be refreshed too")
	}
}

func TestAnnounceCondemnedTargetRevokesPreviewsAndCarriesNoURL(t *testing.T) {
	index := newFakeTargetIndex()
	index.targets[announcedURL] = target("malicious", true)
	index.refs = []storage.LinkReference{{MessageID: "m1", WorkspaceID: "ws-a", TargetType: storage.TargetChannel, TargetID: "ch-1"}}
	index.spaces = []string{"ws-a"}
	publisher := &fakeLinkUpdatePublisher{}
	announcer := NewLinkTargetAnnouncer(index, publisher, nil, nil)
	announcer.SetPreviewEnabled(true)

	announcer.Announce(context.Background(), announcedURL)

	if len(index.revoked) != 1 || len(index.queued) != 0 {
		t.Fatalf("revoked = %v queued = %v", index.revoked, index.queued)
	}
	link := publisher.updates[0].link
	if link.Safety != domain.LinkSafetyMalicious || link.URL != "" || link.Href != "" || link.Hostname != "" || link.Preview != nil {
		t.Fatalf("condemned payload leaks: %+v", link)
	}
}

func TestAnnounceWithPreviewDisabledQueuesNothingAndSendsNoCard(t *testing.T) {
	index := newFakeTargetIndex()
	index.targets[announcedURL] = target("safe", true)
	index.refs = []storage.LinkReference{{MessageID: "m1", WorkspaceID: "ws-a", TargetType: storage.TargetChannel, TargetID: "ch-1"}}
	index.spaces = []string{"ws-a"}
	index.previews["ws-a"] = map[string]storage.LinkPreviewRow{announcedURL: {ID: "p", CanonicalURL: announcedURL, State: "ready"}}
	publisher := &fakeLinkUpdatePublisher{}
	announcer := NewLinkTargetAnnouncer(index, publisher, nil, nil)

	announcer.Announce(context.Background(), announcedURL)

	if len(index.queued) != 0 || publisher.updates[0].link.Preview != nil || publisher.updates[0].link.Href == "" {
		t.Fatalf("preview disabled: queued=%v update=%+v", index.queued, publisher.updates[0].link)
	}
}

// refsFor stages n messages naming the URL, in the id order the store lists them.
func refsFor(n int) []storage.LinkReference {
	refs := make([]storage.LinkReference, 0, n)
	for i := 0; i < n; i++ {
		refs = append(refs, storage.LinkReference{MessageID: fmt.Sprintf("m%05d", i), WorkspaceID: "ws", TargetType: storage.TargetChannel, TargetID: "c"})
	}
	return refs
}

// announcedIDs is the multiset of message ids published, for loss/duplication checks.
func announcedIDs(updates []recordedLinkUpdate) map[string]int {
	seen := map[string]int{}
	for _, update := range updates {
		seen[update.messageID]++
	}
	return seen
}

func requireEachOnce(t *testing.T, updates []recordedLinkUpdate, n int) {
	t.Helper()
	seen := announcedIDs(updates)
	if len(seen) != n {
		t.Fatalf("announced %d distinct of %d", len(seen), n)
	}
	for id, count := range seen {
		if count != 1 {
			t.Fatalf("%s announced %d times", id, count)
		}
	}
}

// Bounded fan-out (issue #807): Announce runs one page, the rest continues
// from a durable cursor across passes, and nothing is lost or repeated.
func TestAnnounceFansOutOnePageThenContinues(t *testing.T) {
	index := newFakeTargetIndex()
	index.targets[announcedURL] = target("unknown", true)
	index.refs = refsFor(linkFanoutPage + 5)
	publisher := &fakeLinkUpdatePublisher{}
	announcer := NewLinkTargetAnnouncer(index, publisher, nil, nil)

	announcer.Announce(context.Background(), announcedURL)
	if len(publisher.updates) != linkFanoutPage {
		t.Fatalf("the first pass published %d, want one page of %d", len(publisher.updates), linkFanoutPage)
	}
	if publisher.updates[0].link.Click != domain.LinkClickInterstitial {
		t.Fatalf("unknown payload = %+v", publisher.updates[0].link)
	}
	// The page that ran released its lease with the cursor written, so the next
	// pass — here, the same announcer — continues from it, not from the start.
	if pages := announcer.Continue(context.Background()); pages != 1 {
		t.Fatalf("continuation ran %d pages, want 1", pages)
	}
	requireEachOnce(t, publisher.updates, linkFanoutPage+5)
	if index.openFanouts() != 0 {
		t.Fatal("a finished fan-out was left open")
	}
	// Nothing left to do: another pass is free.
	if pages := announcer.Continue(context.Background()); pages != 0 {
		t.Fatalf("a finished fan-out ran again: %d pages", pages)
	}
}

// One target with more pages than a pass may run: the pass stops at its
// bound and the next pass picks up the cursor. Announce runs one page; a pass
// of Continue runs at most linkFanoutPassRounds pages of one target.
func TestContinueBoundsWorkPerPassAndDrainsAcrossPasses(t *testing.T) {
	const pages = linkFanoutPassRounds + 3
	const n = (pages-1)*linkFanoutPage + 17
	index := newFakeTargetIndex()
	index.targets[announcedURL] = target("safe", true)
	index.refs = refsFor(n)
	publisher := &fakeLinkUpdatePublisher{}
	announcer := NewLinkTargetAnnouncer(index, publisher, nil, nil)

	announcer.Announce(context.Background(), announcedURL)
	if ran := announcer.Continue(context.Background()); ran != linkFanoutPassRounds {
		t.Fatalf("first pass ran %d pages, want the bound %d", ran, linkFanoutPassRounds)
	}
	if got := len(publisher.updates); got != (linkFanoutPassRounds+1)*linkFanoutPage {
		t.Fatalf("published %d after the bounded pass", got)
	}
	if index.openFanouts() != 1 {
		t.Fatal("the fan-out must stay open with its cursor for the next pass")
	}
	if ran := announcer.Continue(context.Background()); ran != pages-1-linkFanoutPassRounds {
		t.Fatalf("second pass ran %d pages", ran)
	}
	requireEachOnce(t, publisher.updates, n)
	if index.openFanouts() != 0 {
		t.Fatal("a drained fan-out was left open")
	}
}

// A failure in the middle keeps the cursor: the retry resumes from the last
// page recorded, and every message is still announced exactly once.
func TestContinueRetriesAFailedPageFromItsCursor(t *testing.T) {
	const n = 2*linkFanoutPage + 3
	index := newFakeTargetIndex()
	index.targets[announcedURL] = target("safe", true)
	index.refs = refsFor(n)
	index.failPagesAfter = 1 // the second page's listing fails once
	publisher := &fakeLinkUpdatePublisher{}
	announcer := NewLinkTargetAnnouncer(index, publisher, nil, nil)

	announcer.Announce(context.Background(), announcedURL)
	// The failed page stays leased with its cursor intact — the lease lapsing
	// is the retry — so this pass publishes nothing beyond the first page.
	if pages := announcer.Continue(context.Background()); pages != 1 {
		t.Fatalf("failed pass ran %d pages, want 1", pages)
	}
	if len(publisher.updates) != linkFanoutPage {
		t.Fatalf("a failed page published %d beyond the first page", len(publisher.updates)-linkFanoutPage)
	}
	index.releaseLeases()
	announcer.Continue(context.Background())
	requireEachOnce(t, publisher.updates, n)
	if index.openFanouts() != 0 {
		t.Fatal("the retried fan-out was left open")
	}
}

// Several targets converging at once share a pass in waiting order.
func TestContinueIsFairAcrossTargets(t *testing.T) {
	index := newFakeTargetIndex()
	publisher := &fakeLinkUpdatePublisher{}
	announcer := NewLinkTargetAnnouncer(index, publisher, nil, nil)
	for i := 0; i < linkFanoutClaimBatch+3; i++ {
		url := fmt.Sprintf("https://announce.example/%d", i)
		index.targets[url] = target("unknown", true)
		if _, err := index.BeginLinkFanout(context.Background(), url, "", storage.LinkFanoutKindTarget); err != nil {
			t.Fatal(err)
		}
	}
	index.releaseLeases()
	if pages := announcer.Continue(context.Background()); pages != linkFanoutClaimBatch+3 {
		t.Fatalf("one pass ran %d pages for %d waiting targets", pages, linkFanoutClaimBatch+3)
	}
	if index.openFanouts() != 0 {
		t.Fatalf("%d fan-outs left open", index.openFanouts())
	}
}

func TestAnnouncePreviewReachesOnlyTheOwningWorkspace(t *testing.T) {
	index := newFakeTargetIndex()
	index.targets[announcedURL] = target("safe", true)
	index.refs = []storage.LinkReference{
		{MessageID: "m1", WorkspaceID: "ws-a", TargetType: storage.TargetChannel, TargetID: "ch-1"},
		{MessageID: "m2", WorkspaceID: "ws-b", TargetType: storage.TargetChannel, TargetID: "ch-2"},
	}
	preview := storage.LinkPreviewRow{ID: "p", WorkspaceID: "ws-a", CanonicalURL: announcedURL, State: "ready", Title: "T"}
	index.previews["ws-a"] = map[string]storage.LinkPreviewRow{announcedURL: preview}
	publisher := &fakeLinkUpdatePublisher{}
	announcer := NewLinkTargetAnnouncer(index, publisher, nil, nil)
	announcer.SetPreviewEnabled(true)

	announcer.AnnouncePreview(context.Background(), preview)

	if len(publisher.updates) != 1 || publisher.updates[0].messageID != "m1" || publisher.updates[0].link.Preview == nil {
		t.Fatalf("updates = %+v", publisher.updates)
	}
}

func TestAnnounceToleratesFailuresAndMissingWiring(t *testing.T) {
	var nilAnnouncer *LinkTargetAnnouncer
	nilAnnouncer.Announce(context.Background(), announcedURL) // no panic
	nilAnnouncer.AnnouncePreview(context.Background(), storage.LinkPreviewRow{})

	index := newFakeTargetIndex()
	index.targetOK = false
	announcer := NewLinkTargetAnnouncer(index, &fakeLinkUpdatePublisher{}, nil, nil)
	announcer.Announce(context.Background(), announcedURL)
	announcer.AnnouncePreview(context.Background(), storage.LinkPreviewRow{CanonicalURL: announcedURL})

	index = newFakeTargetIndex()
	index.targets[announcedURL] = target("safe", true)
	index.refsErr = errors.New("boom")
	publisher := &fakeLinkUpdatePublisher{}
	announcer = NewLinkTargetAnnouncer(index, nil, nil, nil)
	announcer.SetPublishers(publisher, nil)
	announcer.Announce(context.Background(), announcedURL)
	if len(publisher.updates) != 0 {
		t.Fatal("a failed listing must publish nothing")
	}
}

// brokenPreviewIndex fails every preview-side call so the announcer's warnings
// for those paths run; the target itself loads and fans out normally.
type brokenPreviewIndex struct{ *fakeTargetIndex }

func (brokenPreviewIndex) LinkWorkspacesReferencing(context.Context, string) ([]string, error) {
	return []string{"ws-1"}, errors.New("boom")
}

func (brokenPreviewIndex) QueueLinkPreviews(context.Context, string, []string) error {
	return errors.New("boom")
}

func (brokenPreviewIndex) RevokeLinkPreviews(context.Context, string) ([]storage.LinkPreviewRow, error) {
	return nil, errors.New("boom")
}

func (brokenPreviewIndex) LoadLinkPreviews(context.Context, string, []string) (map[string]storage.LinkPreviewRow, error) {
	return nil, errors.New("boom")
}

func TestAnnounceKeepsFanningOutWhenThePreviewSideFails(t *testing.T) {
	index := newFakeTargetIndex()
	index.targets[announcedURL] = target("safe", true)
	index.refs = []storage.LinkReference{{MessageID: "m1", WorkspaceID: "ws-1", TargetType: storage.TargetChannel, TargetID: "c"}}
	publisher := &fakeLinkUpdatePublisher{}
	announcer := NewLinkTargetAnnouncer(brokenPreviewIndex{index}, publisher, nil, nil)
	announcer.SetPreviewEnabled(true)

	announcer.Announce(context.Background(), announcedURL)
	if len(publisher.updates) != 1 || publisher.updates[0].link.Preview != nil {
		t.Fatalf("updates = %+v", publisher.updates)
	}
	index.targets[announcedURL] = target("malicious", true)
	announcer.Announce(context.Background(), announcedURL)
	if len(publisher.updates) != 2 {
		t.Fatalf("a failed revoke must not stop the condemnation: %+v", publisher.updates)
	}

	// Queueing itself failing is logged, not fatal; the fan-out still happens.
	queueFails := newFakeTargetIndex()
	queueFails.targets[announcedURL] = target("safe", true)
	queueFails.spaces = []string{"ws-1"}
	queueFails.refs = index.refs
	publisher = &fakeLinkUpdatePublisher{}
	announcer = NewLinkTargetAnnouncer(brokenQueueIndex{queueFails}, publisher, nil, nil)
	announcer.SetPreviewEnabled(true)
	announcer.Announce(context.Background(), announcedURL)
	if len(publisher.updates) != 1 {
		t.Fatalf("updates = %+v", publisher.updates)
	}
}

type brokenQueueIndex struct{ *fakeTargetIndex }

func (brokenQueueIndex) QueueLinkPreviews(context.Context, string, []string) error {
	return errors.New("boom")
}

// Both workers hand convergence to the announcer once it is wired (issue
// #807); without it they keep the aggregate-only drain.
func TestWorkersConvergeThroughTheAnnouncerWhenWired(t *testing.T) {
	index := newFakeTargetIndex()
	index.targets[announcedURL] = target("safe", true)
	index.refs = []storage.LinkReference{{MessageID: "m1", WorkspaceID: "ws-1", TargetType: storage.TargetChannel, TargetID: "c"}}
	publisher := &fakeLinkUpdatePublisher{}
	announcer := NewLinkTargetAnnouncer(index, publisher, nil, nil)

	scan := &LinkScanService{logger: slog.Default()}
	scan.SetAnnouncer(announcer)
	scan.converge(context.Background(), announcedURL)
	reconcile := &LinkReconcileService{logger: slog.Default()}
	reconcile.SetAnnouncer(announcer)
	reconcile.converge(context.Background(), storage.InconclusiveScan{CanonicalURL: announcedURL})

	if len(publisher.updates) != 2 || index.refresh != 2 {
		t.Fatalf("updates = %d refresh = %d", len(publisher.updates), index.refresh)
	}
}

// Case E: a claim superseded mid-page — a newer announcement restarted the
// fan-out while the stale pass was still running — publishes what the target
// says *now* (every page re-reads it) and cannot advance or finish the
// continuation that replaced it, which then runs from the start.
func TestStaleFanoutClaimPublishesCurrentStateAndMovesNothing(t *testing.T) {
	index := newFakeTargetIndex()
	index.targets[announcedURL] = target("unknown", true)
	index.refs = refsFor(linkFanoutPage + 3)
	publisher := &fakeLinkUpdatePublisher{}
	announcer := NewLinkTargetAnnouncer(index, publisher, nil, nil)

	// A claims the continuation after its first page and holds it.
	announcer.Announce(context.Background(), announcedURL)
	stale, err := index.ClaimDueLinkFanouts(context.Background(), 1)
	if err != nil || len(stale) != 1 || stale[0].AfterMessageID == "" {
		t.Fatalf("claim = %+v %v, want the continuation at its cursor", stale, err)
	}
	// A newer verdict restarts the fan-out under a new claim.
	index.targets[announcedURL] = target("safe", true)
	announcer.Announce(context.Background(), announcedURL)
	published := len(publisher.updates)
	current := *index.fanoutByID(stale[0].ID) // the restart ran its own first page

	// A's page runs late: what it publishes is the safe state, not the unknown
	// one it was claimed for, and the restarted continuation is untouched.
	announcer.runPage(context.Background(), stale[0])
	for _, update := range publisher.updates[published:] {
		if update.link.Safety != domain.LinkSafetySafe {
			t.Fatalf("a stale claim published %s", update.link.Safety)
		}
	}
	row := index.fanoutByID(stale[0].ID)
	if row.finished || row.LinkFanout != current.LinkFanout {
		t.Fatalf("a stale claim moved the current continuation: %+v, want %+v", row.LinkFanout, current.LinkFanout)
	}
	// The current continuation finishes on its own.
	announcer.Continue(context.Background())
	if index.openFanouts() != 0 {
		t.Fatal("the restarted fan-out did not finish")
	}
}
