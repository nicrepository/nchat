package storage_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
	"github.com/nicrepository/nchat/services/admin-service/internal/storage"
)

// authPolicyRow builds the row the settings query returns, in registry order,
// so a definition added to the catalog without a column here fails loudly
// instead of scanning into the wrong field.
func authPolicyRow(t *testing.T, revision int, overrides map[domain.ConfigKey]any) ([]string, []any) {
	t.Helper()
	columns := []string{"revision"}
	values := []any{revision}
	for _, definition := range domain.EditableConfigDefinitions(domain.ConfigDocumentAuthPolicy) {
		columns = append(columns, definition.Column)
		if override, ok := overrides[definition.Key]; ok {
			values = append(values, override)
			continue
		}
		switch {
		case definition.Type == domain.ConfigTypeBool:
			values = append(values, definition.Default.Bool)
		case definition.Default.Null:
			values = append(values, nil)
		default:
			values = append(values, definition.Default.Int)
		}
	}
	return columns, values
}

// testAuthorization is the identity a privileged write re-proves inside its
// transaction. Every mock spec below carries one, because a write without it is
// refused before any statement is sent — which is the point.
func testAuthorization() domain.MutationAuthorization {
	return domain.MutationAuthorization{
		SessionID:  "77777777-7777-7777-7777-777777777777",
		UserID:     userA,
		Capability: domain.CapabilityConfigManage,
	}
}

// expectAuthorized queues the two statements the transactional authorization
// check runs: the locking re-read of session and principal, then the current
// capability set.
func expectAuthorized(mock pgxmock.PgxPoolIface, capabilities ...string) {
	mock.ExpectQuery(`FROM auth.admin_sessions AS s`).
		WithArgs(anyArgs(2)...).
		WillReturnRows(pgxmock.NewRows([]string{"user_id"}).AddRow(userA))
	mock.ExpectQuery(`FROM auth.admin_principal_roles AS pr`).
		WithArgs(userA).
		WillReturnRows(pgxmock.NewRows([]string{"capabilities"}).AddRow(capabilities))
}

// appliedRow is what the compare-and-swap returns: the committed row, in the
// same projection a read uses, so the caller never needs a second query.
func appliedRow(t *testing.T, revision int, overrides map[domain.ConfigKey]any) *pgxmock.Rows {
	t.Helper()
	columns, values := authPolicyRow(t, revision, overrides)
	return pgxmock.NewRows(columns).AddRow(values...)
}

func TestReadAuthPolicy_ScansEveryDeclaredSettingWithItsType(t *testing.T) {
	mock := newMock(t)
	columns, values := authPolicyRow(t, 4, map[domain.ConfigKey]any{
		domain.ConfigKeyPasswordMinLength:      int64(16),
		domain.ConfigKeyPasswordRequireSymbol:  false,
		domain.ConfigKeyPasswordExpirationDays: int64(90),
	})
	mock.ExpectQuery(`FROM auth.auth_policy_settings`).
		WillReturnRows(pgxmock.NewRows(columns).AddRow(values...))

	state, err := storage.NewPGXConfigStore(mock).ReadAuthPolicy(context.Background())
	if err != nil {
		t.Fatalf("ReadAuthPolicy: %v", err)
	}

	if state.Revision != 4 || state.Document != domain.ConfigDocumentAuthPolicy {
		t.Fatalf("unexpected document state: %+v", state)
	}
	if len(state.Values) != len(domain.EditableConfigDefinitions(domain.ConfigDocumentAuthPolicy)) {
		t.Fatalf("expected every declared setting, got %d", len(state.Values))
	}
	expectValue(t, state, domain.ConfigKeyPasswordMinLength, domain.IntValue(16))
	expectValue(t, state, domain.ConfigKeyPasswordRequireSymbol, domain.BoolValue(false))
	expectValue(t, state, domain.ConfigKeyPasswordExpirationDays, domain.IntValue(90))
}

// expectValue asserts one scanned setting, type included: a boolean that came
// back as an integer is a scan wired to the wrong column, and comparing only
// the number would not notice.
func expectValue(t *testing.T, state domain.ConfigDocumentState, key domain.ConfigKey, expected domain.ConfigValue) {
	t.Helper()
	got := state.Values[key]
	if !got.Equal(expected) {
		t.Fatalf("%s = %+v, expected %+v", key, got, expected)
	}
}

// SQL NULL is an absent value, not zero: "passwords do not expire" and
// "passwords expire in zero days" are different policies.
func TestReadAuthPolicy_ScansNullAsAbsentAndNotZero(t *testing.T) {
	mock := newMock(t)
	columns, values := authPolicyRow(t, 1, map[domain.ConfigKey]any{
		domain.ConfigKeyPasswordExpirationDays: nil,
	})
	mock.ExpectQuery(`FROM auth.auth_policy_settings`).
		WillReturnRows(pgxmock.NewRows(columns).AddRow(values...))

	state, err := storage.NewPGXConfigStore(mock).ReadAuthPolicy(context.Background())
	if err != nil {
		t.Fatalf("ReadAuthPolicy: %v", err)
	}

	value := state.Values[domain.ConfigKeyPasswordExpirationDays]
	if !value.Null || value.Type != domain.ConfigTypeInt {
		t.Fatalf("expected a typed null, got %+v", value)
	}
	if value.Equal(domain.IntValue(0)) {
		t.Fatal("null must not compare equal to zero")
	}
}

// The write is a compare-and-swap in one statement. If the predicate ever loses
// the revision, two administrators stop conflicting and start overwriting each
// other silently, which is the failure this whole surface exists to prevent.
func TestApplyAuthPolicy_WritesUnderACompareAndSwap(t *testing.T) {
	mock := newMock(t)
	mock.ExpectBegin()
	expectAuthorized(mock, string(domain.CapabilityConfigManage))
	mock.ExpectQuery(`UPDATE auth.auth_policy_settings`).
		WithArgs(3, int64(16)).
		WillReturnRows(appliedRow(t, 4, map[domain.ConfigKey]any{domain.ConfigKeyPasswordMinLength: int64(16)}))
	mock.ExpectQuery(`INSERT INTO auth.admin_config_versions`).
		WithArgs(anyArgs(6)...).
		WillReturnRows(pgxmock.NewRows([]string{"id", "applied_at"}).AddRow(int64(7), epoch))
	mock.ExpectExec(`INSERT INTO auth.admin_config_version_changes`).
		WithArgs(anyArgs(4)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	outcome, err := storage.NewPGXConfigStore(mock).ApplyAuthPolicy(context.Background(), domain.ConfigApplyInput{
		ExpectedRevision: 3,
		ActorUserID:      userA,
		CorrelationID:    "req-1",
		Authorization:    testAuthorization(),
		Changes: []domain.ConfigChange{
			{Key: domain.ConfigKeyPasswordMinLength, From: domain.IntValue(12), To: domain.IntValue(16)},
		},
	})
	if err != nil {
		t.Fatalf("ApplyAuthPolicy: %v", err)
	}
	if outcome.Version.ID != 7 || outcome.Version.Revision != 4 {
		t.Fatalf("expected version 7 at revision 4, got %+v", outcome.Version)
	}
	// The committed state comes out of the write itself: no second read.
	if outcome.State.Revision != 4 || outcome.State.Values[domain.ConfigKeyPasswordMinLength].Int != 16 {
		t.Fatalf("expected the committed row to be returned, got %+v", outcome.State)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// The statement is the security boundary of the write path: column names come
// from the registry, everything else is bound.
func TestApplyAuthPolicy_StatementBindsValuesAndKeepsTheRevisionPredicate(t *testing.T) {
	mock := newMock(t)
	var statement string
	mock.ExpectBegin()
	expectAuthorized(mock, string(domain.CapabilityConfigManage))
	mock.ExpectQuery(`UPDATE auth.auth_policy_settings`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("captured"))

	// The mock rejects on the error above; what matters is the statement text,
	// which pgxmock exposes through the expectation it matched.
	_, err := storage.NewPGXConfigStore(mock).ApplyAuthPolicy(context.Background(), domain.ConfigApplyInput{
		ExpectedRevision: 3,
		Changes: []domain.ConfigChange{
			{Key: domain.ConfigKeyDeviceMaxPerUser, To: domain.IntValue(9)},
		},
	})
	if err == nil {
		t.Fatal("expected the captured failure to propagate")
	}

	statement = storage.AuthPolicyUpdateForTest([]domain.ConfigChange{
		{Key: domain.ConfigKeyDeviceMaxPerUser, To: domain.IntValue(9)},
		{Key: domain.ConfigKeyPasswordMinLength, To: domain.IntValue(16)},
	})
	if !strings.Contains(statement, "WHERE id = 1 AND revision = $1") {
		t.Fatalf("the compare-and-swap predicate is missing:\n%s", statement)
	}
	if !strings.Contains(statement, "revision = revision + 1") {
		t.Fatalf("the revision must advance with the write:\n%s", statement)
	}
	if !strings.Contains(statement, "max_devices_per_user = $2") || !strings.Contains(statement, "min_password_length = $3") {
		t.Fatalf("every value must be bound, not inlined:\n%s", statement)
	}
	for _, value := range []string{"9", "16"} {
		if strings.Contains(statement, "= "+value) {
			t.Fatalf("value %q was inlined into the statement:\n%s", value, statement)
		}
	}
}

func TestApplyAuthPolicy_LostRaceIsAConflict(t *testing.T) {
	mock := newMock(t)
	mock.ExpectBegin()
	expectAuthorized(mock, string(domain.CapabilityConfigManage))
	mock.ExpectQuery(`UPDATE auth.auth_policy_settings`).
		WithArgs(anyArgs(2)...).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectRollback()

	_, err := storage.NewPGXConfigStore(mock).ApplyAuthPolicy(context.Background(), domain.ConfigApplyInput{
		ExpectedRevision: 2,
		Changes:          []domain.ConfigChange{{Key: domain.ConfigKeyDeviceMaxPerUser, To: domain.IntValue(9)}},
		Authorization:    testAuthorization(),
	})

	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("expected a conflict, got %v", err)
	}
}

// The store refuses to write a key the registry does not declare as editable,
// even though the service already validated. A store that can be aimed at any
// column by a future caller is a store nobody can review.
func TestApplyAuthPolicy_RefusesAKeyTheRegistryDoesNotDeclareEditable(t *testing.T) {
	for _, key := range []domain.ConfigKey{"secret.smtp_password", "oidc.enabled", "auth.made.up"} {
		mock := newMock(t)
		_, err := storage.NewPGXConfigStore(mock).ApplyAuthPolicy(context.Background(), domain.ConfigApplyInput{
			ExpectedRevision: 1,
			Changes:          []domain.ConfigChange{{Key: key, To: domain.IntValue(1)}},
		})
		if !errors.Is(err, domain.ErrConfigNotEditable) {
			t.Fatalf("%s: expected the store to refuse, got %v", key, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("%s: the store must not open a transaction: %v", key, err)
		}
	}
}

func TestApplyAuthPolicy_RefusesAnEmptyChangeSet(t *testing.T) {
	mock := newMock(t)
	_, err := storage.NewPGXConfigStore(mock).
		ApplyAuthPolicy(context.Background(), domain.ConfigApplyInput{ExpectedRevision: 1, Authorization: testAuthorization()})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected a refusal, got %v", err)
	}
}

func TestListConfigVersions_LoadsChangesInOneQuery(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`FROM auth.admin_config_versions`).
		WithArgs("auth.policy", 25).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "revision", "applied_at", "actor_user_id", "email",
			"correlation_id", "reason", "reverts_revision",
		}).
			AddRow(int64(2), 3, epoch, userA, "admin@example.test", "req-2", "ajuste", 0).
			AddRow(int64(1), 2, epoch.Add(-time.Hour), userB, "other@example.test", "req-1", "", 0))
	mock.ExpectQuery(`FROM auth.admin_config_version_changes`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"version_id", "config_key", "value_from", "value_to"}).
			AddRow(int64(2), string(domain.ConfigKeyPasswordMinLength), "12", "16").
			AddRow(int64(1), string(domain.ConfigKeyPasswordExpirationDays), "null", "90"))

	versions, err := storage.NewPGXConfigStore(mock).
		ListConfigVersions(context.Background(), domain.ConfigDocumentAuthPolicy, 25)
	if err != nil {
		t.Fatalf("ListConfigVersions: %v", err)
	}

	if len(versions) != 2 {
		t.Fatalf("expected two versions, got %d", len(versions))
	}
	if versions[0].Revision != 3 || versions[0].ActorEmail != "admin@example.test" {
		t.Fatalf("unexpected version: %+v", versions[0])
	}
	if len(versions[0].Changes) != 1 || versions[0].Changes[0].To.Int != 16 {
		t.Fatalf("expected the change to be attached, got %+v", versions[0].Changes)
	}
	if !versions[1].Changes[0].From.Null || versions[1].Changes[0].From.Type != domain.ConfigTypeInt {
		t.Fatalf("expected a stored null to come back typed, got %+v", versions[1].Changes[0].From)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestGetConfigVersion_MissingVersionIsNotFound(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`FROM auth.admin_config_versions`).
		WithArgs("auth.policy", int64(99)).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "revision", "applied_at", "actor_user_id", "email",
			"correlation_id", "reason", "reverts_revision",
		}))

	_, err := storage.NewPGXConfigStore(mock).
		GetConfigVersion(context.Background(), domain.ConfigDocumentAuthPolicy, 99)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
}

func TestConfigStore_RefusesAnUnknownDocument(t *testing.T) {
	store := storage.NewPGXConfigStore(newMock(t))

	if _, err := store.ReadDocument(context.Background(), "auth.unknown"); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected a refusal, got %v", err)
	}
	if _, err := store.ApplyDocument(context.Background(), "auth.unknown", domain.ConfigApplyInput{}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected a refusal, got %v", err)
	}
}

func TestReadDocument_DispatchesToTheOwningReader(t *testing.T) {
	mock := newMock(t)
	columns, values := authPolicyRow(t, 2, nil)
	mock.ExpectQuery(`FROM auth.auth_policy_settings`).
		WillReturnRows(pgxmock.NewRows(columns).AddRow(values...))

	state, err := storage.NewPGXConfigStore(mock).
		ReadDocument(context.Background(), domain.ConfigDocumentAuthPolicy)
	if err != nil {
		t.Fatalf("ReadDocument: %v", err)
	}
	if state.Revision != 2 || state.Document != domain.ConfigDocumentAuthPolicy {
		t.Fatalf("unexpected state: %+v", state)
	}
}

func TestApplyDocument_DispatchesToTheOwningWriter(t *testing.T) {
	mock := newMock(t)
	mock.ExpectBegin()
	expectAuthorized(mock, string(domain.CapabilityConfigManage))
	mock.ExpectQuery(`UPDATE auth.auth_policy_settings`).
		WithArgs(anyArgs(2)...).
		WillReturnRows(appliedRow(t, 3, nil))
	mock.ExpectQuery(`INSERT INTO auth.admin_config_versions`).
		WithArgs(anyArgs(6)...).
		WillReturnRows(pgxmock.NewRows([]string{"id", "applied_at"}).AddRow(int64(4), epoch))
	mock.ExpectExec(`INSERT INTO auth.admin_config_version_changes`).
		WithArgs(anyArgs(4)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	outcome, err := storage.NewPGXConfigStore(mock).ApplyDocument(context.Background(),
		domain.ConfigDocumentAuthPolicy, domain.ConfigApplyInput{
			ExpectedRevision: 2,
			RevertsRevision:  1,
			Authorization:    testAuthorization(),
			Changes: []domain.ConfigChange{
				{Key: domain.ConfigKeyPasswordExpirationDays, From: domain.IntValue(90), To: domain.NullValue(domain.ConfigTypeInt)},
			},
		})
	if err != nil {
		t.Fatalf("ApplyDocument: %v", err)
	}
	if outcome.Version.Revision != 3 || outcome.Version.RevertsRevision != 1 {
		t.Fatalf("unexpected version: %+v", outcome.Version)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// A boolean and an explicit null are bound as themselves, not as 0 and not as
// the empty string.
func TestApplyAuthPolicy_BindsBooleansAndNullsAsThemselves(t *testing.T) {
	mock := newMock(t)
	mock.ExpectBegin()
	expectAuthorized(mock, string(domain.CapabilityConfigManage))
	mock.ExpectQuery(`UPDATE auth.auth_policy_settings`).
		WithArgs(1, false, nil).
		WillReturnRows(appliedRow(t, 2, map[domain.ConfigKey]any{
			domain.ConfigKeyPasswordRequireSymbol:  false,
			domain.ConfigKeyPasswordExpirationDays: nil,
		}))
	mock.ExpectQuery(`INSERT INTO auth.admin_config_versions`).
		WithArgs(anyArgs(6)...).
		WillReturnRows(pgxmock.NewRows([]string{"id", "applied_at"}).AddRow(int64(1), epoch))
	mock.ExpectExec(`INSERT INTO auth.admin_config_version_changes`).
		WithArgs(anyArgs(4)...).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec(`INSERT INTO auth.admin_config_version_changes`).
		WithArgs(anyArgs(4)...).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	_, err := storage.NewPGXConfigStore(mock).ApplyAuthPolicy(context.Background(), domain.ConfigApplyInput{
		ExpectedRevision: 1,
		Authorization:    testAuthorization(),
		Changes: []domain.ConfigChange{
			{Key: domain.ConfigKeyPasswordRequireSymbol, From: domain.BoolValue(true), To: domain.BoolValue(false)},
			{Key: domain.ConfigKeyPasswordExpirationDays, From: domain.IntValue(90), To: domain.NullValue(domain.ConfigTypeInt)},
		},
	})
	if err != nil {
		t.Fatalf("ApplyAuthPolicy: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// The version row and the values are one transaction: a failure recording the
// change must not leave the values written.
func TestApplyAuthPolicy_FailureToRecordTheVersionRollsBackTheValues(t *testing.T) {
	mock := newMock(t)
	mock.ExpectBegin()
	expectAuthorized(mock, string(domain.CapabilityConfigManage))
	mock.ExpectQuery(`UPDATE auth.auth_policy_settings`).
		WithArgs(anyArgs(2)...).
		WillReturnRows(appliedRow(t, 2, nil))
	mock.ExpectQuery(`INSERT INTO auth.admin_config_versions`).
		WithArgs(anyArgs(6)...).
		WillReturnError(errors.New("history unavailable"))
	mock.ExpectRollback()

	_, err := storage.NewPGXConfigStore(mock).ApplyAuthPolicy(context.Background(), domain.ConfigApplyInput{
		ExpectedRevision: 1,
		Changes:          []domain.ConfigChange{{Key: domain.ConfigKeyDeviceMaxPerUser, To: domain.IntValue(9)}},
		Authorization:    testAuthorization(),
	})
	if err == nil {
		t.Fatal("expected the failure to propagate")
	}
	if errors.Is(err, domain.ErrConflict) {
		t.Fatal("a broken history is not a lost race")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestGetConfigVersion_ReturnsTheVersionWithItsChanges(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`FROM auth.admin_config_versions`).
		WithArgs("auth.policy", int64(7)).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "revision", "applied_at", "actor_user_id", "email",
			"correlation_id", "reason", "reverts_revision",
		}).AddRow(int64(7), 4, epoch, userA, "admin@example.test", "req-7", "ajuste", 3))
	mock.ExpectQuery(`FROM auth.admin_config_version_changes`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"version_id", "config_key", "value_from", "value_to"}).
			AddRow(int64(7), string(domain.ConfigKeyPasswordRequireSymbol), "true", "false"))

	version, err := storage.NewPGXConfigStore(mock).
		GetConfigVersion(context.Background(), domain.ConfigDocumentAuthPolicy, 7)
	if err != nil {
		t.Fatalf("GetConfigVersion: %v", err)
	}
	if version.RevertsRevision != 3 || version.Reason != "ajuste" {
		t.Fatalf("unexpected version: %+v", version)
	}
	if len(version.Changes) != 1 || version.Changes[0].To.Bool {
		t.Fatalf("expected the boolean transition, got %+v", version.Changes)
	}
}

// A history row that cannot be read is an error, not a silently empty diff:
// hiding a recorded change is worse than failing to show it.
func TestListConfigVersions_RefusesAnUnreadableStoredValue(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`FROM auth.admin_config_versions`).
		WithArgs("auth.policy", 25).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "revision", "applied_at", "actor_user_id", "email",
			"correlation_id", "reason", "reverts_revision",
		}).AddRow(int64(1), 2, epoch, userA, "a@example.test", "req-1", "", 0))
	mock.ExpectQuery(`FROM auth.admin_config_version_changes`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"version_id", "config_key", "value_from", "value_to"}).
			AddRow(int64(1), string(domain.ConfigKeyPasswordMinLength), "12", `"hunter2"`))

	if _, err := storage.NewPGXConfigStore(mock).
		ListConfigVersions(context.Background(), domain.ConfigDocumentAuthPolicy, 25); err == nil {
		t.Fatal("expected an unreadable stored value to fail loudly")
	}
}

func TestListConfigVersions_EmptyHistoryAsksForNoChanges(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`FROM auth.admin_config_versions`).
		WithArgs("auth.policy", 25).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "revision", "applied_at", "actor_user_id", "email",
			"correlation_id", "reason", "reverts_revision",
		}))

	versions, err := storage.NewPGXConfigStore(mock).
		ListConfigVersions(context.Background(), domain.ConfigDocumentAuthPolicy, 25)
	if err != nil {
		t.Fatalf("ListConfigVersions: %v", err)
	}
	if len(versions) != 0 {
		t.Fatalf("expected an empty history, got %d", len(versions))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("the change query must not run for an empty page: %v", err)
	}
}

func TestConfigStore_UnwiredStoreIsUnavailable(t *testing.T) {
	var store *storage.PGXConfigStore

	if _, err := store.ReadAuthPolicy(context.Background()); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected unavailable, got %v", err)
	}
	if _, err := store.ApplyAuthPolicy(context.Background(), domain.ConfigApplyInput{}); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected unavailable, got %v", err)
	}
	if _, err := store.ListConfigVersions(context.Background(), domain.ConfigDocumentAuthPolicy, 10); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected unavailable, got %v", err)
	}
	if _, err := store.GetConfigVersion(context.Background(), domain.ConfigDocumentAuthPolicy, 1); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected unavailable, got %v", err)
	}
}

// The precondition is part of the write, not a check before it. If it ever left
// the WHERE clause, reverting an old version would silently discard every
// change made after it and no test above would notice.
func TestApplyAuthPolicy_PreconditionsAreAssertedInsideTheWrite(t *testing.T) {
	statement := storage.AuthPolicyUpdateForTest(
		[]domain.ConfigChange{{Key: domain.ConfigKeyDeviceMaxPerUser, To: domain.IntValue(5)}},
		domain.ConfigPrecondition{Key: domain.ConfigKeyDeviceMaxPerUser, Value: domain.IntValue(20)},
		domain.ConfigPrecondition{Key: domain.ConfigKeyPasswordExpirationDays, Value: domain.NullValue(domain.ConfigTypeInt)},
	)

	if !strings.Contains(statement, "WHERE id = 1 AND revision = $1") {
		t.Fatalf("the revision predicate is missing:\n%s", statement)
	}
	if !strings.Contains(statement, "AND max_devices_per_user IS NOT DISTINCT FROM $3") {
		t.Fatalf("the value precondition is missing:\n%s", statement)
	}
	// IS NOT DISTINCT FROM and not `=`: a nullable setting can legitimately be
	// unset, and `column = NULL` is never true, which would turn "still absent"
	// into a permanent conflict.
	if !strings.Contains(statement, "AND password_expiration_days IS NOT DISTINCT FROM $4") {
		t.Fatalf("a null precondition must compare with IS NOT DISTINCT FROM:\n%s", statement)
	}
	if strings.Contains(statement, "password_expiration_days = $") {
		t.Fatalf("a precondition must never be compared with =:\n%s", statement)
	}
	// The committed row comes back from the same statement, so no read follows.
	if !strings.Contains(statement, "RETURNING revision, min_password_length") {
		t.Fatalf("the write must return the committed row:\n%s", statement)
	}
}

// A precondition that no longer holds is a conflict, and it is the database
// that decides: the statement matches no row and nothing is written.
func TestApplyAuthPolicy_UnmetPreconditionIsAConflict(t *testing.T) {
	mock := newMock(t)
	mock.ExpectBegin()
	expectAuthorized(mock, string(domain.CapabilityConfigManage))
	mock.ExpectQuery(`UPDATE auth.auth_policy_settings`).
		WithArgs(anyArgs(3)...).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectRollback()

	_, err := storage.NewPGXConfigStore(mock).ApplyAuthPolicy(context.Background(), domain.ConfigApplyInput{
		ExpectedRevision: 3,
		Authorization:    testAuthorization(),
		Changes:          []domain.ConfigChange{{Key: domain.ConfigKeyDeviceMaxPerUser, To: domain.IntValue(5)}},
		Preconditions:    []domain.ConfigPrecondition{{Key: domain.ConfigKeyDeviceMaxPerUser, Value: domain.IntValue(20)}},
	})

	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("expected a conflict, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestApplyAuthPolicy_RefusesAPreconditionOnAKeyItMayNotWrite(t *testing.T) {
	mock := newMock(t)

	_, err := storage.NewPGXConfigStore(mock).ApplyAuthPolicy(context.Background(), domain.ConfigApplyInput{
		ExpectedRevision: 1,
		Authorization:    testAuthorization(),
		Changes:          []domain.ConfigChange{{Key: domain.ConfigKeyDeviceMaxPerUser, To: domain.IntValue(5)}},
		Preconditions:    []domain.ConfigPrecondition{{Key: "secret.smtp_password", Value: domain.TextValue("x")}},
	})

	if !errors.Is(err, domain.ErrConfigNotEditable) {
		t.Fatalf("expected the store to refuse, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("the store must not open a transaction: %v", err)
	}
}

// The transactional check is the first statement of the transaction, before
// anything is written. If it ever moved after the write, a revocation could
// land in between and the write would still commit.
func TestApplyAuthPolicy_AuthorizesBeforeItWrites(t *testing.T) {
	mock := newMock(t)
	mock.ExpectBegin()
	// A principal whose session and roles no longer authorize this write.
	mock.ExpectQuery(`FROM auth.admin_sessions AS s`).
		WithArgs(anyArgs(2)...).
		WillReturnRows(pgxmock.NewRows([]string{"user_id"}).AddRow(userA))
	mock.ExpectQuery(`FROM auth.admin_principal_roles AS pr`).
		WithArgs(userA).
		WillReturnRows(pgxmock.NewRows([]string{"capabilities"}).AddRow([]string{}))
	mock.ExpectRollback()

	_, err := storage.NewPGXConfigStore(mock).ApplyAuthPolicy(context.Background(), domain.ConfigApplyInput{
		ExpectedRevision: 1,
		Authorization:    testAuthorization(),
		Changes:          []domain.ConfigChange{{Key: domain.ConfigKeyDeviceMaxPerUser, To: domain.IntValue(9)}},
	})

	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("expected forbidden, got %v", err)
	}
	// The expectations end at the rollback: no UPDATE and no version insert
	// were sent, so a revoked capability leaves nothing behind.
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// A session the query no longer matches — revoked, expired, or belonging to a
// suspended account — is unauthenticated rather than merely unauthorized, so
// the console asks the operator to sign in again.
func TestApplyAuthPolicy_RevokedSessionIsUnauthorized(t *testing.T) {
	mock := newMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`FROM auth.admin_sessions AS s`).
		WithArgs(anyArgs(2)...).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectRollback()

	_, err := storage.NewPGXConfigStore(mock).ApplyAuthPolicy(context.Background(), domain.ConfigApplyInput{
		ExpectedRevision: 1,
		Authorization:    testAuthorization(),
		Changes:          []domain.ConfigChange{{Key: domain.ConfigKeyDeviceMaxPerUser, To: domain.IntValue(9)}},
	})

	if !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("expected unauthorized, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// A write that names no authorization is refused rather than checked against
// nothing: forgetting to populate it must fail closed, not open.
func TestApplyAuthPolicy_MissingAuthorizationIsRefused(t *testing.T) {
	incomplete := []domain.MutationAuthorization{
		{},
		{UserID: userA, Capability: domain.CapabilityConfigManage},
		{SessionID: "s", Capability: domain.CapabilityConfigManage},
		{SessionID: "s", UserID: userA},
		{SessionID: "s", UserID: userA, Capability: "admin.invented"},
	}
	for _, authorization := range incomplete {
		mock := newMock(t)
		mock.ExpectBegin()
		mock.ExpectRollback()

		_, err := storage.NewPGXConfigStore(mock).ApplyAuthPolicy(context.Background(), domain.ConfigApplyInput{
			ExpectedRevision: 1,
			Authorization:    authorization,
			Changes:          []domain.ConfigChange{{Key: domain.ConfigKeyDeviceMaxPerUser, To: domain.IntValue(9)}},
		})

		if !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("%+v: expected forbidden, got %v", authorization, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("%+v: nothing may be queried: %v", authorization, err)
		}
	}
}

// The superuser rule holds here exactly as it does in the middleware: it is one
// capability set implementation, so the two cannot disagree about what a grant
// means.
func TestApplyAuthPolicy_SuperuserSatisfiesAnyRequiredCapability(t *testing.T) {
	mock := newMock(t)
	mock.ExpectBegin()
	expectAuthorized(mock, string(domain.CapabilitySuperuser))
	mock.ExpectQuery(`UPDATE auth.auth_policy_settings`).
		WithArgs(anyArgs(2)...).
		WillReturnRows(appliedRow(t, 2, nil))
	mock.ExpectQuery(`INSERT INTO auth.admin_config_versions`).
		WithArgs(anyArgs(6)...).
		WillReturnRows(pgxmock.NewRows([]string{"id", "applied_at"}).AddRow(int64(1), epoch))
	mock.ExpectExec(`INSERT INTO auth.admin_config_version_changes`).
		WithArgs(anyArgs(4)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	// The principal holds only admin.superuser; the write demands
	// admin.config.manage.
	if _, err := storage.NewPGXConfigStore(mock).ApplyAuthPolicy(context.Background(), domain.ConfigApplyInput{
		ExpectedRevision: 1,
		Authorization:    testAuthorization(),
		Changes: []domain.ConfigChange{{
			Key:  domain.ConfigKeyDeviceMaxPerUser,
			From: domain.IntValue(5),
			To:   domain.IntValue(9),
		}},
	}); err != nil {
		t.Fatalf("ApplyAuthPolicy: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
