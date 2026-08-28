package storage_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
	"github.com/nicrepository/nchat/services/admin-service/internal/storage"
)

// The configuration surface makes three promises a mock cannot verify:
//
//   - two administrators saving at once produce one write and one conflict,
//     which is a property of the UPDATE's predicate under real concurrency;
//   - the change history physically cannot hold a string, which is a column
//     CHECK;
//   - a version and its change rows are written together or not at all.
//
// Gated on ADMIN_TEST_DATABASE_URL like the rest of this package's PostgreSQL
// suite, and skipped when it is unset.
//
//	ADMIN_TEST_DATABASE_URL=postgresql://nchat@localhost:5432/nchat_test \
//	  go test ./internal/storage/... -run PostgreSQL

// seedConfigWriter creates a real administrator holding admin.config.manage and
// returns the authorization a privileged write must now carry.
//
// These specs are about concurrency and history, not about who may write — but
// they go through the same transactional authorization a deployment does, so
// none of them can accidentally pass with authority the platform would refuse.
func seedConfigWriter(t *testing.T, pool *pgxpool.Pool) domain.MutationAuthorization {
	t.Helper()
	userID := grantAdmin(t, pool, "config-writer@example.test", "platform-superuser")
	return domain.MutationAuthorization{
		SessionID:  seedAdminSession(t, pool, userID),
		UserID:     userID,
		Capability: domain.CapabilityConfigManage,
	}
}

func TestPostgreSQLConfigStore_ConcurrentSavesProduceOneWriteAndOneConflict(t *testing.T) {
	pool := connectAdminTestDB(t)
	applyAuthMigrations(t, pool)
	store := storage.NewPGXConfigStore(pool)
	authorization := seedConfigWriter(t, pool)

	before, err := store.ReadAuthPolicy(context.Background())
	if err != nil {
		t.Fatalf("ReadAuthPolicy: %v", err)
	}

	applied, conflicts := raceTwoSaves(t, store, before, authorization)

	if applied != 1 || conflicts != 1 {
		t.Fatalf("expected exactly one write and one conflict, got %d applied and %d conflicts", applied, conflicts)
	}
	assertOneAppliedVersion(t, store, before.Revision)
}

// raceTwoSaves runs the save two administrators would perform from the same
// loaded form, at the same time, and reports how the two ended.
//
// Both echo the revision they read, which is the whole point: the second one to
// reach the row must find the predicate no longer true.
func raceTwoSaves(t *testing.T, store *storage.PGXConfigStore, before domain.ConfigDocumentState, authorization domain.MutationAuthorization) (applied, conflicts int) {
	t.Helper()
	ctx := context.Background()
	change := func(value int64) domain.ConfigApplyInput {
		return domain.ConfigApplyInput{
			Authorization:    authorization,
			ExpectedRevision: before.Revision,
			CorrelationID:    "req-race",
			Changes: []domain.ConfigChange{{
				Key:  domain.ConfigKeyDeviceMaxPerUser,
				From: before.Values[domain.ConfigKeyDeviceMaxPerUser],
				To:   domain.IntValue(value),
			}},
		}
	}

	var wait sync.WaitGroup
	results := make([]error, 2)
	wait.Add(2)
	for index, value := range []int64{11, 12} {
		go func() {
			defer wait.Done()
			_, results[index] = store.ApplyAuthPolicy(ctx, change(value))
		}()
	}
	wait.Wait()

	return classifyRace(t, results)
}

// classifyRace counts how the two concurrent writers ended.
//
// Only two outcomes are acceptable and anything else fails immediately: a
// database error dressed up as a conflict would make this spec pass while the
// compare-and-swap was broken.
func classifyRace(t *testing.T, results []error) (applied, conflicts int) {
	t.Helper()
	for _, err := range results {
		switch {
		case err == nil:
			applied++
		case errors.Is(err, domain.ErrConflict):
			conflicts++
		default:
			t.Fatalf("unexpected failure: %v", err)
		}
	}
	return applied, conflicts
}

// assertOneAppliedVersion checks that the losing writer left nothing behind:
// the revision moved once, the history has one row, and that row describes the
// value the settings row now holds.
func assertOneAppliedVersion(t *testing.T, store *storage.PGXConfigStore, beforeRevision int) {
	t.Helper()
	ctx := context.Background()

	after, err := store.ReadAuthPolicy(ctx)
	if err != nil {
		t.Fatalf("ReadAuthPolicy: %v", err)
	}
	if after.Revision != beforeRevision+1 {
		t.Fatalf("expected the revision to advance exactly once, got %d -> %d", beforeRevision, after.Revision)
	}

	versions, err := store.ListConfigVersions(ctx, domain.ConfigDocumentAuthPolicy, 10)
	if err != nil {
		t.Fatalf("ListConfigVersions: %v", err)
	}
	if len(versions) != 1 {
		t.Fatalf("expected exactly one recorded version, got %d", len(versions))
	}
	if versions[0].Revision != after.Revision || len(versions[0].Changes) != 1 {
		t.Fatalf("the history disagrees with the row: %+v", versions[0])
	}
	if !versions[0].Changes[0].To.Equal(after.Values[domain.ConfigKeyDeviceMaxPerUser]) {
		t.Fatal("the recorded change does not describe the stored value")
	}
}

// The CHECK is the structural guarantee that a credential can never enter the
// change history, whatever a future caller believes it is writing.
func TestPostgreSQLConfigStore_HistoryRefusesANonScalarValue(t *testing.T) {
	pool := connectAdminTestDB(t)
	applyAuthMigrations(t, pool)
	ctx := context.Background()

	var versionID int64
	err := pool.QueryRow(ctx, `
		INSERT INTO auth.admin_config_versions (document_key, revision)
		VALUES ('auth.policy', 2) RETURNING id`).Scan(&versionID)
	if err != nil {
		t.Fatalf("insert version: %v", err)
	}

	for _, value := range []string{`"hunter2"`, `{"secret":"x"}`, `["x"]`} {
		_, err := pool.Exec(ctx, `
			INSERT INTO auth.admin_config_version_changes (version_id, config_key, value_from, value_to)
			VALUES ($1, 'auth.password.min_length', '12'::jsonb, $2::jsonb)`, versionID, value)
		if err == nil {
			t.Fatalf("the history accepted %s", value)
		}
		if !strings.Contains(err.Error(), "scalar_check") {
			t.Fatalf("expected the scalar CHECK to refuse %s, got %v", value, err)
		}
	}
}

// Deleting a version takes its change rows with it, and nothing else references
// them, so the history has no orphan rows to reason about.
func TestPostgreSQLConfigStore_VersionAndItsChangesAreOneUnit(t *testing.T) {
	pool := connectAdminTestDB(t)
	applyAuthMigrations(t, pool)
	store := storage.NewPGXConfigStore(pool)
	authorization := seedConfigWriter(t, pool)
	ctx := context.Background()

	before, err := store.ReadAuthPolicy(ctx)
	if err != nil {
		t.Fatalf("ReadAuthPolicy: %v", err)
	}
	outcome, err := store.ApplyAuthPolicy(ctx, domain.ConfigApplyInput{
		Authorization:    authorization,
		ExpectedRevision: before.Revision,
		Reason:           "teste de integridade",
		Changes: []domain.ConfigChange{
			{Key: domain.ConfigKeyLoginLockoutMinutes, From: before.Values[domain.ConfigKeyLoginLockoutMinutes], To: domain.IntValue(30)},
			{Key: domain.ConfigKeyPasswordExpirationDays, From: before.Values[domain.ConfigKeyPasswordExpirationDays], To: domain.IntValue(90)},
		},
	})
	if err != nil {
		t.Fatalf("ApplyAuthPolicy: %v", err)
	}

	stored, err := store.GetConfigVersion(ctx, domain.ConfigDocumentAuthPolicy, outcome.Version.ID)
	if err != nil {
		t.Fatalf("GetConfigVersion: %v", err)
	}
	if len(stored.Changes) != 2 || stored.Reason != "teste de integridade" {
		t.Fatalf("unexpected stored version: %+v", stored)
	}

	if _, err := pool.Exec(ctx, `DELETE FROM auth.admin_config_versions WHERE id = $1`, outcome.Version.ID); err != nil {
		t.Fatalf("delete version: %v", err)
	}
	var orphans int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM auth.admin_config_version_changes WHERE version_id = $1`, outcome.Version.ID).Scan(&orphans); err != nil {
		t.Fatalf("count orphans: %v", err)
	}
	if orphans != 0 {
		t.Fatalf("expected the change rows to cascade, %d remained", orphans)
	}
}

// The superseded-rollback guard, against the real database.
//
// A mock can prove the predicate is in the statement. Only PostgreSQL can prove
// that the predicate and the write are one atomic step, and that a rollback of
// a version somebody has since moved past matches no row at all.
func TestPostgreSQLConfigStore_SupersededRollbackWritesNothing(t *testing.T) {
	pool := connectAdminTestDB(t)
	applyAuthMigrations(t, pool)
	store := storage.NewPGXConfigStore(pool)
	authorization := seedConfigWriter(t, pool)
	ctx := context.Background()

	before, err := store.ReadAuthPolicy(ctx)
	if err != nil {
		t.Fatalf("ReadAuthPolicy: %v", err)
	}
	original := before.Values[domain.ConfigKeyDeviceMaxPerUser]

	// v1 and then v2, so v1 is no longer the version in force.
	first, err := store.ApplyAuthPolicy(ctx, domain.ConfigApplyInput{
		Authorization:    authorization,
		ExpectedRevision: before.Revision,
		Changes:          []domain.ConfigChange{{Key: domain.ConfigKeyDeviceMaxPerUser, From: original, To: domain.IntValue(20)}},
	})
	if err != nil {
		t.Fatalf("first apply: %v", err)
	}
	second, err := store.ApplyAuthPolicy(ctx, domain.ConfigApplyInput{
		Authorization:    authorization,
		ExpectedRevision: first.State.Revision,
		Changes:          []domain.ConfigChange{{Key: domain.ConfigKeyDeviceMaxPerUser, From: domain.IntValue(20), To: domain.IntValue(30)}},
	})
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}

	// Reverting v1 with the *current* revision: optimistic locking alone would
	// let this through, because nothing has changed since this caller read.
	_, err = store.ApplyAuthPolicy(ctx, domain.ConfigApplyInput{
		Authorization:    authorization,
		ExpectedRevision: second.State.Revision,
		Changes:          []domain.ConfigChange{{Key: domain.ConfigKeyDeviceMaxPerUser, From: domain.IntValue(30), To: original}},
		Preconditions:    []domain.ConfigPrecondition{{Key: domain.ConfigKeyDeviceMaxPerUser, Value: domain.IntValue(20)}},
	})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("expected a conflict for the superseded version, got %v", err)
	}

	after, err := store.ReadAuthPolicy(ctx)
	if err != nil {
		t.Fatalf("ReadAuthPolicy: %v", err)
	}
	if !after.Values[domain.ConfigKeyDeviceMaxPerUser].Equal(domain.IntValue(30)) {
		t.Fatalf("the later change must survive, got %+v", after.Values[domain.ConfigKeyDeviceMaxPerUser])
	}
	if after.Revision != second.State.Revision {
		t.Fatalf("a refused rollback must not advance the revision: %d -> %d", second.State.Revision, after.Revision)
	}

	versions, err := store.ListConfigVersions(ctx, domain.ConfigDocumentAuthPolicy, 10)
	if err != nil {
		t.Fatalf("ListConfigVersions: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("a refused rollback must record no version, got %d", len(versions))
	}
}

// The version still in force is reversible, and the write returns the state it
// committed — the same row, from the same statement, with no read in between.
func TestPostgreSQLConfigStore_RollbackOfTheVersionInForceSucceeds(t *testing.T) {
	pool := connectAdminTestDB(t)
	applyAuthMigrations(t, pool)
	store := storage.NewPGXConfigStore(pool)
	authorization := seedConfigWriter(t, pool)
	ctx := context.Background()

	before, err := store.ReadAuthPolicy(ctx)
	if err != nil {
		t.Fatalf("ReadAuthPolicy: %v", err)
	}
	original := before.Values[domain.ConfigKeyDeviceMaxPerUser]

	applied, err := store.ApplyAuthPolicy(ctx, domain.ConfigApplyInput{
		Authorization:    authorization,
		ExpectedRevision: before.Revision,
		Changes:          []domain.ConfigChange{{Key: domain.ConfigKeyDeviceMaxPerUser, From: original, To: domain.IntValue(20)}},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}

	reverted, err := store.ApplyAuthPolicy(ctx, domain.ConfigApplyInput{
		Authorization:    authorization,
		ExpectedRevision: applied.State.Revision,
		RevertsRevision:  applied.Version.Revision,
		Changes:          []domain.ConfigChange{{Key: domain.ConfigKeyDeviceMaxPerUser, From: domain.IntValue(20), To: original}},
		Preconditions:    []domain.ConfigPrecondition{{Key: domain.ConfigKeyDeviceMaxPerUser, Value: domain.IntValue(20)}},
	})
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}

	if !reverted.State.Values[domain.ConfigKeyDeviceMaxPerUser].Equal(original) {
		t.Fatalf("expected the original value back, got %+v", reverted.State.Values[domain.ConfigKeyDeviceMaxPerUser])
	}
	if reverted.State.Revision != applied.State.Revision+1 {
		t.Fatalf("expected the revision to advance once, got %d", reverted.State.Revision)
	}
	// Forward-only: the rollback is a third version naming the one it reverted.
	if reverted.Version.RevertsRevision != applied.Version.Revision {
		t.Fatalf("expected the new version to name the reverted one, got %+v", reverted.Version)
	}

	// The state the write reported is the state the database holds.
	stored, err := store.ReadAuthPolicy(ctx)
	if err != nil {
		t.Fatalf("ReadAuthPolicy: %v", err)
	}
	if stored.Revision != reverted.State.Revision {
		t.Fatalf("the committed state must match what was reported: %d vs %d", stored.Revision, reverted.State.Revision)
	}
}

// Under real concurrency, a rollback computed against a state somebody else is
// changing must lose rather than overwrite.
func TestPostgreSQLConfigStore_ConcurrentChangeBeatsAStaleRollback(t *testing.T) {
	pool := connectAdminTestDB(t)
	applyAuthMigrations(t, pool)
	store := storage.NewPGXConfigStore(pool)
	authorization := seedConfigWriter(t, pool)
	ctx := context.Background()

	before, err := store.ReadAuthPolicy(ctx)
	if err != nil {
		t.Fatalf("ReadAuthPolicy: %v", err)
	}
	original := before.Values[domain.ConfigKeyDeviceMaxPerUser]
	applied, err := store.ApplyAuthPolicy(ctx, domain.ConfigApplyInput{
		Authorization:    authorization,
		ExpectedRevision: before.Revision,
		Changes:          []domain.ConfigChange{{Key: domain.ConfigKeyDeviceMaxPerUser, From: original, To: domain.IntValue(20)}},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}

	rollback := domain.ConfigApplyInput{
		Authorization:    authorization,
		ExpectedRevision: applied.State.Revision,
		Changes:          []domain.ConfigChange{{Key: domain.ConfigKeyDeviceMaxPerUser, From: domain.IntValue(20), To: original}},
		Preconditions:    []domain.ConfigPrecondition{{Key: domain.ConfigKeyDeviceMaxPerUser, Value: domain.IntValue(20)}},
	}
	edit := domain.ConfigApplyInput{
		Authorization:    authorization,
		ExpectedRevision: applied.State.Revision,
		Changes:          []domain.ConfigChange{{Key: domain.ConfigKeyDeviceMaxPerUser, From: domain.IntValue(20), To: domain.IntValue(30)}},
	}

	var wait sync.WaitGroup
	results := make([]error, 2)
	wait.Add(2)
	for index, input := range []domain.ConfigApplyInput{rollback, edit} {
		go func() {
			defer wait.Done()
			_, results[index] = store.ApplyAuthPolicy(ctx, input)
		}()
	}
	wait.Wait()

	applications, conflicts := classifyRace(t, results)
	if applications != 1 || conflicts != 1 {
		t.Fatalf("expected exactly one write and one conflict, got %d applied and %d conflicts", applications, conflicts)
	}

	after, err := store.ReadAuthPolicy(ctx)
	if err != nil {
		t.Fatalf("ReadAuthPolicy: %v", err)
	}
	if after.Revision != applied.State.Revision+1 {
		t.Fatalf("exactly one of the two may have been written, revision is %d", after.Revision)
	}
}
