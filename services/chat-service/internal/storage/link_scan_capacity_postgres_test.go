package storage_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// Admission, against a real database.
//
// This is the half of the security finding that cannot be tested with a fake:
// the correctness claim is that concurrent callers cannot exceed a budget, and a
// fake store is single-threaded by construction, so it would pass whatever the
// SQL said. The shape being defended against is specific — two requests both
// read "9 of 10 used", both decide they fit, and the window ends at 12 — and
// under an attacker choosing the concurrency it is the normal case rather than a
// rare interleaving.
//
// Opt-in like its neighbours: needs CHAT_TEST_DATABASE_URL against a _test
// database.
func TestLinkScanAdmissionPostgreSQL(t *testing.T) {
	ctx := t.Context()
	pool := newLinkScanTestPool(t)

	const workspace = "00000000-0000-0000-0000-000000000001"
	store := storage.NewPGXMessageStore(pool)

	// Every subtest starts from an empty queue and an empty ledger, because both
	// are what the decision is computed from.
	reset := func(t *testing.T) {
		t.Helper()
		for _, statement := range []string{
			`DELETE FROM chat.message_link_scans`,
			`DELETE FROM chat.link_scans`,
			`DELETE FROM chat.link_scan_budget_usage`,
		} {
			if _, err := pool.Exec(ctx, statement); err != nil {
				t.Fatalf("reset: %v", err)
			}
		}
	}

	urls := func(prefix string, n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = "https://" + prefix + ".example/" + string(rune('a'+i))
		}
		return out
	}

	// [§50] The race, run for real. Two admissions of six against a budget of
	// ten: one fits, one does not, and the ledger must land on six — never
	// twelve, and never a partial four.
	t.Run("concurrent admissions cannot exceed the budget", func(t *testing.T) {
		reset(t)
		capacity := storage.LinkScanCapacity{
			WorkspaceNewURLBudget: 10, BudgetWindow: time.Hour, MaxPendingJobs: 1000,
		}

		var start sync.WaitGroup
		start.Add(1)
		results := make(chan storage.LinkScanAdmission, 2)
		var wg sync.WaitGroup
		for _, prefix := range []string{"first", "second"} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				start.Wait()
				admission, err := store.AdmitLinkScans(
					context.Background(), workspace, urls(prefix, 6), capacity)
				if err != nil {
					t.Errorf("AdmitLinkScans: %v", err)
					return
				}
				results <- admission
			}()
		}
		start.Done()
		wg.Wait()
		close(results)

		allowed := 0
		for admission := range results {
			if admission.Allowed() {
				allowed++
			}
		}
		if allowed != 1 {
			t.Fatalf("%d of 2 admissions were allowed, want exactly 1", allowed)
		}

		var used int
		if err := pool.QueryRow(ctx, `
			SELECT COALESCE(sum(used), 0) FROM chat.link_scan_budget_usage
			WHERE scope_type = 'workspace' AND scope_key = $1`, workspace).Scan(&used); err != nil {
			t.Fatalf("read usage: %v", err)
		}
		if used != 6 {
			t.Fatalf("budget used = %d, want exactly the admitted six", used)
		}

		// [§31] And all-or-nothing: the refused operation queued nothing at all,
		// not the four that would have fit.
		var queued int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM chat.link_scans`).Scan(&queued); err != nil {
			t.Fatalf("count jobs: %v", err)
		}
		if queued != 6 {
			t.Fatalf("queued %d jobs, want only the admitted six", queued)
		}
	})

	// [§28, §51] The unit is a canonical URL that needs new work. A URL already
	// queued is one somebody already paid for, and a second message naming it
	// simply waits on the same scan.
	t.Run("a url already queued costs nothing", func(t *testing.T) {
		reset(t)
		capacity := storage.LinkScanCapacity{
			WorkspaceNewURLBudget: 1, BudgetWindow: time.Hour, MaxPendingJobs: 1000,
		}
		shared := []string{"https://shared.example/a"}

		first, err := store.AdmitLinkScans(ctx, workspace, shared, capacity)
		if err != nil || !first.Allowed() || first.NewScanCost != 1 {
			t.Fatalf("first = %+v, err = %v", first, err)
		}
		// The budget is now spent. A second message naming the same URL must
		// still be admitted, because it asks the provider for nothing.
		second, err := store.AdmitLinkScans(ctx, workspace, shared, capacity)
		if err != nil {
			t.Fatalf("AdmitLinkScans: %v", err)
		}
		if !second.Allowed() {
			t.Fatalf("a URL already queued was refused: %+v", second)
		}
		if second.NewScanCost != 0 {
			t.Fatalf("cost = %d, want free", second.NewScanCost)
		}
	})

	// A fresh verdict is an answer nobody has to buy again.
	t.Run("a fresh verdict costs nothing", func(t *testing.T) {
		reset(t)
		const url = "https://decided.example/a"
		if _, err := pool.Exec(ctx, `
			INSERT INTO chat.link_scans (canonical_url, status, decided_at)
			VALUES ($1, 'safe', now())`, url); err != nil {
			t.Fatalf("seed verdict: %v", err)
		}
		admission, err := store.AdmitLinkScans(ctx, workspace, []string{url},
			storage.LinkScanCapacity{WorkspaceNewURLBudget: 1, BudgetWindow: time.Hour})
		if err != nil {
			t.Fatalf("AdmitLinkScans: %v", err)
		}
		if !admission.Allowed() || admission.NewScanCost != 0 {
			t.Fatalf("admission = %+v, want free", admission)
		}
		var used int
		if err := pool.QueryRow(ctx,
			`SELECT COALESCE(sum(used), 0) FROM chat.link_scan_budget_usage`).Scan(&used); err != nil {
			t.Fatalf("read usage: %v", err)
		}
		if used != 0 {
			t.Fatalf("budget used = %d for a cached answer", used)
		}
	})

	// [§55] The deployment-wide cap. A full queue is a slow provider, and
	// admitting more only makes every waiting message slower.
	t.Run("a full backlog refuses new work", func(t *testing.T) {
		reset(t)
		capacity := storage.LinkScanCapacity{
			WorkspaceNewURLBudget: 100, BudgetWindow: time.Hour, MaxPendingJobs: 2,
		}
		if admission, err := store.AdmitLinkScans(ctx, workspace, urls("full", 2), capacity); err != nil ||
			!admission.Allowed() {
			t.Fatalf("filling the backlog: %+v %v", admission, err)
		}
		admission, err := store.AdmitLinkScans(ctx, workspace, []string{"https://over.example/a"}, capacity)
		if err != nil {
			t.Fatalf("AdmitLinkScans: %v", err)
		}
		if admission.Result != storage.AdmissionBacklog {
			t.Fatalf("result = %q, want the backlog refusal", admission.Result)
		}
		// Refused before anything was created: the cap is not advisory.
		var queued int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM chat.link_scans`).Scan(&queued); err != nil {
			t.Fatalf("count jobs: %v", err)
		}
		if queued != 2 {
			t.Fatalf("queued %d jobs past the cap", queued)
		}
	})

	// [§37, §56] The provider allowance, shared. Two callers standing in for two
	// replicas take from the same window.
	t.Run("the provider allowance is shared between callers", func(t *testing.T) {
		reset(t)
		first, err := store.ReserveProviderSubmit(ctx, 1, time.Hour)
		if err != nil || !first {
			t.Fatalf("first reservation: %v %v", first, err)
		}
		second, err := store.ReserveProviderSubmit(ctx, 1, time.Hour)
		if err != nil {
			t.Fatalf("second reservation: %v", err)
		}
		if second {
			t.Fatal("a second replica was allowed past a window of one")
		}
	})

	// A limit of zero disables the ceiling rather than refusing everything: a
	// control that fails into "nothing works" is one somebody switches off.
	t.Run("an unset limit disables the ceiling", func(t *testing.T) {
		reset(t)
		admission, err := store.AdmitLinkScans(ctx, workspace, urls("unset", 5),
			storage.LinkScanCapacity{})
		if err != nil || !admission.Allowed() {
			t.Fatalf("admission = %+v, err = %v", admission, err)
		}
		allowed, err := store.ReserveProviderSubmit(ctx, 0, time.Hour)
		if err != nil || !allowed {
			t.Fatalf("reservation = %v, err = %v", allowed, err)
		}
	})

	// Spent windows are counters, not history.
	t.Run("expired windows are pruned", func(t *testing.T) {
		reset(t)
		if _, err := pool.Exec(ctx, `
			INSERT INTO chat.link_scan_budget_usage (scope_type, scope_key, window_start, used)
			VALUES ('workspace', $1, now() - interval '2 days', 5)`, workspace); err != nil {
			t.Fatalf("seed window: %v", err)
		}
		if err := store.PruneLinkScanBudget(ctx, time.Hour); err != nil {
			t.Fatalf("PruneLinkScanBudget: %v", err)
		}
		var rows int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM chat.link_scan_budget_usage`).Scan(&rows); err != nil {
			t.Fatalf("count windows: %v", err)
		}
		if rows != 0 {
			t.Fatalf("%d expired windows survived", rows)
		}
	})

	t.Cleanup(func() {
		for _, statement := range []string{
			`DELETE FROM chat.message_link_scans`,
			`DELETE FROM chat.link_scans`,
			`DELETE FROM chat.link_scan_budget_usage`,
		} {
			_, _ = pool.Exec(context.Background(), statement)
		}
	})
}
