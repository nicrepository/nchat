package storage_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/libs/go/platform/notificationevent"
	"github.com/nicrepository/nchat/services/notification-service/internal/storage"
)

// previewBatch is the claim size this plan is measured at, named once so the
// loop-count assertion and the fixture cannot disagree.
const previewBatch = 50

// Explain the actual store query; rollback ANALYZE's writes before the real claim.
type explainingPreviewPool struct {
	*pgxpool.Pool
	t       *testing.T
	enabled bool
}

func (p explainingPreviewPool) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	tx, err := p.Begin(ctx)
	if err != nil {
		p.t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var raw []byte
	if err := tx.QueryRow(ctx, "EXPLAIN (ANALYZE, BUFFERS, VERBOSE, FORMAT JSON) "+query, args...).Scan(&raw); err != nil {
		p.t.Fatal(err)
	}
	var plans []struct {
		Plan          previewPlanNode `json:"Plan"`
		ExecutionTime float64         `json:"Execution Time"`
	}
	if err := json.Unmarshal(raw, &plans); err != nil {
		p.t.Fatal(err)
	}
	// auth.users is touched twice per claimed row when previews are on: once for
	// the sender's display name, once for the SR-001 check on the recipient's
	// own account. Both are primary-key lookups inside the one claim statement.
	//
	// What the number defends is that it stays a *constant per row* — two, not
	// "two plus one per subscription" and not a second statement. A change that
	// turned either lookup into a query of its own would show up here as a loop
	// count that is no longer a multiple of the batch, or as this test being
	// edited to accept one.
	const previewAccountLookupsPerRow = 2
	loops := previewSenderLoops(plans[0].Plan)
	if want := float64(previewBatch * previewAccountLookupsPerRow); p.enabled && loops != want {
		p.t.Fatalf("preview on: auth.users lookups = %v, want %v (sender + recipient, per row)",
			loops, want)
	}
	if !p.enabled && loops != 0 {
		p.t.Fatalf("preview off read auth.users %v times", loops)
	}
	p.t.Logf("preview=%v batch=%d execution_ms=%.3f shared_hits=%d shared_reads=%d auth_users_loops=%.0f",
		p.enabled, previewBatch, plans[0].ExecutionTime, plans[0].Plan.Hits, plans[0].Plan.Reads, loops)
	if err := tx.Rollback(ctx); err != nil {
		p.t.Fatal(err)
	}
	return p.Pool.Query(ctx, query, args...)
}

type previewPlanNode struct {
	Relation string            `json:"Relation Name"`
	Schema   string            `json:"Schema"`
	Loops    float64           `json:"Actual Loops"`
	Hits     int               `json:"Shared Hit Blocks"`
	Reads    int               `json:"Shared Read Blocks"`
	Plans    []previewPlanNode `json:"Plans"`
}

func previewSenderLoops(node previewPlanNode) float64 {
	var loops float64
	if node.Schema == "auth" && node.Relation == "users" {
		loops = node.Loops
	}
	for _, child := range node.Plans {
		loops += previewSenderLoops(child)
	}
	return loops
}

func TestNotificationPreviewClaimPlanPostgreSQL(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		fixture := seedOutbox(t, notificationevent.StateEligible, previewBatch)
		for _, recipient := range fixture.users[1:] {
			execFixture(t, fixture.pool, `INSERT INTO chat.workspace_members (workspace_id, user_id) VALUES ($1::uuid, $2::uuid)`, notifyWorkerWorkspace, recipient)
		}
		pool := explainingPreviewPool{Pool: fixture.pool, t: t, enabled: enabled}
		events, err := storage.NewPGXNotificationOutboxStore(pool, enabled).ClaimDue(t.Context(), previewBatch, 5, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if len(events) != previewBatch {
			t.Fatalf("claimed %d events", len(events))
		}
		for _, event := range events {
			if (event.Presentation.Sender != "") != enabled {
				t.Fatal("unexpected presentation")
			}
		}
	}
}
