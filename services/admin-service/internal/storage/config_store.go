package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"

	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
)

// PGXConfigStore is the persistence of the Configuration & Secrets Management
// surface (issue #580).
//
// It writes exactly one row — auth.auth_policy_settings, whose id is fixed at 1
// by a column CHECK — and appends to the change history. It reads no
// environment, calls no other service and touches no Kubernetes object: the
// only configuration the Admin API can change is the configuration that lives
// in this database, and that is what makes the write path reviewable.
//
// Like PGXPolicyStore, it has no "set arbitrary key" method. The columns come
// from the registry, which is a compile-time literal; a request names a
// registry key and never a column.
type PGXConfigStore struct {
	pool Pool
}

func NewPGXConfigStore(pool Pool) *PGXConfigStore {
	return &PGXConfigStore{pool: pool}
}

// authPolicyColumns is the ordered column list of the auth policy document,
// derived from the registry so the query and the catalog cannot disagree about
// which fields exist.
//
// domain.ValidateConfigCatalog asserts every column here matches a plain
// identifier pattern, which is what makes substituting them into a statement
// safe. Values are always bound.
var authPolicyColumns = sync.OnceValue(func() []domain.ConfigDefinition {
	return domain.EditableConfigDefinitions(domain.ConfigDocumentAuthPolicy)
})

// authPolicyProjection is the column list every read and every write returns,
// so the state a write reports and the state a read reports are the same shape
// produced by the same scanner.
var authPolicyProjection = sync.OnceValue(func() string {
	definitions := authPolicyColumns()
	columns := make([]string, 0, len(definitions)+1)
	columns = append(columns, "revision")
	for _, definition := range definitions {
		columns = append(columns, definition.Column)
	}
	return strings.Join(columns, ", ")
})

var authPolicySelect = sync.OnceValue(func() string {
	return `SELECT ` + authPolicyProjection() + `
	        FROM auth.auth_policy_settings
	        WHERE id = 1`
})

// scanAuthPolicyRow reads one settings row into the document state.
//
// Shared by the read and the write so a column added to the registry lands in
// both, and a write can describe what it committed without a second query.
func scanAuthPolicyRow(row pgx.Row) (domain.ConfigDocumentState, error) {
	definitions := authPolicyColumns()
	revision := 0
	holders := make([]configScanHolder, len(definitions))
	targets := make([]any, 0, len(definitions)+1)
	targets = append(targets, &revision)
	for index, definition := range definitions {
		holders[index] = newConfigScanHolder(definition)
		targets = append(targets, holders[index].target())
	}
	if err := row.Scan(targets...); err != nil {
		return domain.ConfigDocumentState{}, err
	}
	values := make(map[domain.ConfigKey]domain.ConfigValue, len(definitions))
	for index, definition := range definitions {
		values[definition.Key] = holders[index].value()
	}
	return domain.ConfigDocumentState{
		Document: domain.ConfigDocumentAuthPolicy,
		Revision: revision,
		Values:   values,
	}, nil
}

// ReadAuthPolicy returns the stored authentication policy and its revision.
//
// The revision travels with the values out of the same read, so the number the
// console later echoes back names the exact state the form was filled from.
func (s *PGXConfigStore) ReadAuthPolicy(ctx context.Context) (domain.ConfigDocumentState, error) {
	if s == nil || s.pool == nil {
		return domain.ConfigDocumentState{}, domain.ErrUnavailable
	}
	state, err := scanAuthPolicyRow(s.pool.QueryRow(ctx, authPolicySelect()))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// The row is created by migration 000001 and cannot be deleted
			// without violating the single-row CHECK, so its absence is a
			// broken deployment rather than a missing object.
			return domain.ConfigDocumentState{}, domain.ErrUnavailable
		}
		return domain.ConfigDocumentState{}, fmt.Errorf("read auth policy: %w", err)
	}
	return state, nil
}

// ApplyAuthPolicy writes the change set and records the version it produced.
//
// Three guarantees share one transaction, because separating any of them would
// leave a window: the administrator still holds the capability (revalidated
// under lock — see mutation_authorization.go), the document is still the one
// the change was computed against, and the values and their history are written
// together.
//
// The concurrency control is the UPDATE's own WHERE clause:
//
//	UPDATE ... WHERE id = 1 AND revision = $expected AND <every precondition>
//
// so every check and the write are one statement and one snapshot. Two things
// are asserted there, and they answer different questions:
//
//   - the revision catches an edit made since the form was loaded. Two
//     administrators saving at once produce one write and one conflict;
//   - the preconditions catch a *version* that is no longer in force. Reverting
//     "10 -> 20" after somebody moved the value to 30 must not silently discard
//     their change, and the revision cannot see that, because the console loaded
//     after they wrote.
//
// There is no read-then-write window for either, and no last-write-wins path.
//
// The statement also returns the committed row, so the caller never has to read
// the document back to describe what it just wrote. A second read could fail, or
// could observe a state somebody else already moved on from; neither may be
// allowed to contradict a commit that has happened.
//
// The version and its change rows are written in the same transaction as the
// values, so the history cannot record a change that did not happen and cannot
// omit one that did.
func (s *PGXConfigStore) ApplyAuthPolicy(ctx context.Context, input domain.ConfigApplyInput) (domain.ConfigApplyOutcome, error) {
	if s == nil || s.pool == nil {
		return domain.ConfigApplyOutcome{}, domain.ErrUnavailable
	}
	if len(input.Changes) == 0 {
		return domain.ConfigApplyOutcome{}, domain.ErrInvalidInput
	}
	// The statement is built first, outside the transaction: it only reads the
	// registry, and a change naming something this store may not write is
	// refused without opening one.
	statement, arguments, err := authPolicyUpdate(input)
	if err != nil {
		return domain.ConfigApplyOutcome{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.ConfigApplyOutcome{}, fmt.Errorf("begin config apply: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	outcome, err := applyAuthPolicyTx(ctx, tx, input, statement, arguments)
	if err != nil {
		return domain.ConfigApplyOutcome{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ConfigApplyOutcome{}, fmt.Errorf("commit config apply: %w", err)
	}
	return outcome, nil
}

// applyAuthPolicyTx is the whole write, in the order the guarantees require:
// authority, then concurrency, then the values, then the history.
//
// Nothing here commits. The caller does, once every step has succeeded, so an
// authority that has just been revoked leaves no configuration change, no
// revision and no version behind.
func applyAuthPolicyTx(ctx context.Context, tx pgx.Tx, input domain.ConfigApplyInput, statement string, arguments []any) (domain.ConfigApplyOutcome, error) {
	// First statement of the transaction, before anything is written: the
	// authority to write at all, taken under locks a concurrent revocation must
	// wait for.
	if err := authorizeMutationTx(ctx, tx, input.Authorization); err != nil {
		return domain.ConfigApplyOutcome{}, err
	}
	state, err := scanAuthPolicyRow(tx.QueryRow(ctx, statement, arguments...))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// The revision moved, a precondition no longer holds, or the row is
			// gone. The single-row CHECK makes the third impossible in a healthy
			// deployment, and the first two are the same answer to the caller:
			// the state this change was computed against no longer exists.
			return domain.ConfigApplyOutcome{}, domain.ErrConflict
		}
		return domain.ConfigApplyOutcome{}, fmt.Errorf("apply auth policy: %w", err)
	}
	version, err := insertConfigVersion(ctx, tx, domain.ConfigDocumentAuthPolicy, state.Revision, input)
	if err != nil {
		return domain.ConfigApplyOutcome{}, err
	}
	return domain.ConfigApplyOutcome{Version: version, State: state}, nil
}

// authPolicyUpdate builds the compare-and-swap statement.
//
// Column names are substituted from the registry; every value is a bound
// parameter. $1 is the expected revision, the new values follow in the order the
// change set names them, and the preconditions follow those.
//
// A precondition is compared with IS NOT DISTINCT FROM rather than =, because a
// nullable setting can legitimately be unset and `column = NULL` is never true —
// which would turn "this value is still absent" into a permanent conflict.
func authPolicyUpdate(input domain.ConfigApplyInput) (string, []any, error) {
	assignments := make([]string, 0, len(input.Changes))
	arguments := make([]any, 0, len(input.Changes)+len(input.Preconditions)+1)
	arguments = append(arguments, input.ExpectedRevision)
	for _, change := range input.Changes {
		definition, err := editableAuthPolicyDefinition(change.Key)
		if err != nil {
			return "", nil, err
		}
		arguments = append(arguments, configArgument(change.To))
		assignments = append(assignments, definition.Column+" = $"+strconv.Itoa(len(arguments)))
	}

	predicates := make([]string, 0, len(input.Preconditions))
	for _, precondition := range input.Preconditions {
		definition, err := editableAuthPolicyDefinition(precondition.Key)
		if err != nil {
			return "", nil, err
		}
		arguments = append(arguments, configArgument(precondition.Value))
		predicates = append(predicates,
			" AND "+definition.Column+" IS NOT DISTINCT FROM $"+strconv.Itoa(len(arguments)))
	}

	statement := `
	UPDATE auth.auth_policy_settings
	SET ` + strings.Join(assignments, ", ") + `,
	    revision = revision + 1,
	    updated_at = now()
	WHERE id = 1 AND revision = $1` + strings.Join(predicates, "") + `
	RETURNING ` + authPolicyProjection()
	return statement, arguments, nil
}

// editableAuthPolicyDefinition resolves a key the write path is about to name a
// column for.
//
// Unreachable through the service, which validates first. Refused here anyway so
// this store cannot be turned into a generic writer by a future caller that
// skips that step.
func editableAuthPolicyDefinition(key domain.ConfigKey) (domain.ConfigDefinition, error) {
	definition, ok := domain.LookupConfig(key)
	if !ok || !definition.Editable || definition.Document != domain.ConfigDocumentAuthPolicy {
		return domain.ConfigDefinition{}, domain.ErrConfigNotEditable
	}
	return definition, nil
}

const insertConfigVersionStatement = `
	INSERT INTO auth.admin_config_versions
	    (document_key, revision, actor_user_id, correlation_id, reason, reverts_revision)
	VALUES ($1, $2, $3::uuid, $4, $5, $6)
	RETURNING id, applied_at`

const insertConfigChangeStatement = `
	INSERT INTO auth.admin_config_version_changes (version_id, config_key, value_from, value_to)
	VALUES ($1, $2, $3::jsonb, $4::jsonb)`

func insertConfigVersion(ctx context.Context, tx pgx.Tx, document domain.ConfigDocument, revision int, input domain.ConfigApplyInput) (domain.ConfigVersion, error) {
	version := domain.ConfigVersion{
		Document:        document,
		Revision:        revision,
		ActorUserID:     input.ActorUserID,
		CorrelationID:   input.CorrelationID,
		Reason:          input.Reason,
		RevertsRevision: input.RevertsRevision,
		Changes:         input.Changes,
	}
	err := tx.QueryRow(ctx, insertConfigVersionStatement,
		string(document), revision, nullableText(input.ActorUserID),
		input.CorrelationID, input.Reason, nullableRevision(input.RevertsRevision),
	).Scan(&version.ID, &version.AppliedAt)
	if err != nil {
		return domain.ConfigVersion{}, fmt.Errorf("record config version: %w", err)
	}
	version.AppliedAt = version.AppliedAt.UTC()
	for _, change := range input.Changes {
		from, err := json.Marshal(change.From)
		if err != nil {
			return domain.ConfigVersion{}, fmt.Errorf("encode config change: %w", err)
		}
		to, err := json.Marshal(change.To)
		if err != nil {
			return domain.ConfigVersion{}, fmt.Errorf("encode config change: %w", err)
		}
		if _, err := tx.Exec(ctx, insertConfigChangeStatement,
			version.ID, string(change.Key), string(from), string(to)); err != nil {
			return domain.ConfigVersion{}, fmt.Errorf("record config change: %w", err)
		}
	}
	return version, nil
}

const listConfigVersionsStatement = `
	SELECT v.id, v.revision, v.applied_at,
	       COALESCE(v.actor_user_id::text, ''), COALESCE(u.email, ''),
	       COALESCE(v.correlation_id, ''), v.reason, COALESCE(v.reverts_revision, 0)
	FROM auth.admin_config_versions AS v
	LEFT JOIN auth.users AS u ON u.id = v.actor_user_id
	WHERE v.document_key = $1
	ORDER BY v.applied_at DESC, v.id DESC
	LIMIT $2`

const configVersionByIDStatement = `
	SELECT v.id, v.revision, v.applied_at,
	       COALESCE(v.actor_user_id::text, ''), COALESCE(u.email, ''),
	       COALESCE(v.correlation_id, ''), v.reason, COALESCE(v.reverts_revision, 0)
	FROM auth.admin_config_versions AS v
	LEFT JOIN auth.users AS u ON u.id = v.actor_user_id
	WHERE v.document_key = $1 AND v.id = $2`

const configChangesStatement = `
	SELECT version_id, config_key, value_from::text, value_to::text
	FROM auth.admin_config_version_changes
	WHERE version_id = ANY($1::bigint[])
	ORDER BY version_id DESC, config_key`

// ListConfigVersions returns the most recent applied changes of one document.
//
// Newest first and bounded by a limit the service clamps. Deliberately not
// cursor-paginated: the history of a configuration document grows by one row
// per administrative change, which is the same shape as the audit endpoint and
// the same reason it is served the same way.
func (s *PGXConfigStore) ListConfigVersions(ctx context.Context, document domain.ConfigDocument, limit int) ([]domain.ConfigVersion, error) {
	if s == nil || s.pool == nil {
		return nil, domain.ErrUnavailable
	}
	rows, err := s.pool.Query(ctx, listConfigVersionsStatement, string(document), limit)
	if err != nil {
		return nil, fmt.Errorf("list config versions: %w", err)
	}
	versions, err := scanConfigVersions(rows, document)
	if err != nil {
		return nil, err
	}
	if err := s.attachConfigChanges(ctx, versions); err != nil {
		return nil, err
	}
	return versions, nil
}

// GetConfigVersion reads one recorded version, with the fields it changed.
func (s *PGXConfigStore) GetConfigVersion(ctx context.Context, document domain.ConfigDocument, id int64) (domain.ConfigVersion, error) {
	if s == nil || s.pool == nil {
		return domain.ConfigVersion{}, domain.ErrUnavailable
	}
	rows, err := s.pool.Query(ctx, configVersionByIDStatement, string(document), id)
	if err != nil {
		return domain.ConfigVersion{}, fmt.Errorf("read config version: %w", err)
	}
	versions, err := scanConfigVersions(rows, document)
	if err != nil {
		return domain.ConfigVersion{}, err
	}
	if len(versions) == 0 {
		return domain.ConfigVersion{}, domain.ErrNotFound
	}
	if err := s.attachConfigChanges(ctx, versions); err != nil {
		return domain.ConfigVersion{}, err
	}
	return versions[0], nil
}

func scanConfigVersions(rows pgx.Rows, document domain.ConfigDocument) ([]domain.ConfigVersion, error) {
	defer rows.Close()
	versions := make([]domain.ConfigVersion, 0, 16)
	for rows.Next() {
		version := domain.ConfigVersion{Document: document}
		if err := rows.Scan(&version.ID, &version.Revision, &version.AppliedAt,
			&version.ActorUserID, &version.ActorEmail, &version.CorrelationID,
			&version.Reason, &version.RevertsRevision); err != nil {
			return nil, fmt.Errorf("scan config version: %w", err)
		}
		version.AppliedAt = version.AppliedAt.UTC()
		versions = append(versions, version)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read config versions: %w", err)
	}
	return versions, nil
}

// attachConfigChanges loads the change rows of every version in one query.
//
// One query rather than one per version: the history endpoint returns a page
// of versions and a per-row lookup would turn a listing into N+1 round trips
// against a table that is only ever read together with its parent.
func (s *PGXConfigStore) attachConfigChanges(ctx context.Context, versions []domain.ConfigVersion) error {
	if len(versions) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(versions))
	for _, version := range versions {
		ids = append(ids, version.ID)
	}
	rows, err := s.pool.Query(ctx, configChangesStatement, ids)
	if err != nil {
		return fmt.Errorf("list config changes: %w", err)
	}
	defer rows.Close()

	grouped := make(map[int64][]domain.ConfigChange, len(versions))
	for rows.Next() {
		var versionID int64
		var key, from, to string
		if err := rows.Scan(&versionID, &key, &from, &to); err != nil {
			return fmt.Errorf("scan config change: %w", err)
		}
		change, err := decodeConfigChange(domain.ConfigKey(key), from, to)
		if err != nil {
			return err
		}
		grouped[versionID] = append(grouped[versionID], change)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read config changes: %w", err)
	}
	for index := range versions {
		versions[index].Changes = grouped[versions[index].ID]
	}
	return nil
}

func decodeConfigChange(key domain.ConfigKey, from, to string) (domain.ConfigChange, error) {
	previous, err := domain.DecodeStoredConfigValue([]byte(from))
	if err != nil {
		return domain.ConfigChange{}, fmt.Errorf("decode config change %s: %w", key, err)
	}
	next, err := domain.DecodeStoredConfigValue([]byte(to))
	if err != nil {
		return domain.ConfigChange{}, fmt.Errorf("decode config change %s: %w", key, err)
	}
	return domain.ConfigChange{
		Key:  key,
		From: domain.RetypeStoredConfigValue(key, previous),
		To:   domain.RetypeStoredConfigValue(key, next),
	}, nil
}

// configScanHolder scans one column into the type its definition declares.
//
// A nullable integer is scanned through sql.NullInt64 so SQL NULL arrives as an
// absent value rather than as zero: "passwords do not expire" and "passwords
// expire in zero days" are different policies, and only one of them is a policy
// at all.
type configScanHolder struct {
	definition domain.ConfigDefinition
	number     int64
	optional   sql.NullInt64
	flag       bool
}

func newConfigScanHolder(definition domain.ConfigDefinition) configScanHolder {
	return configScanHolder{definition: definition}
}

func (h *configScanHolder) target() any {
	switch {
	case h.definition.Type == domain.ConfigTypeBool:
		return &h.flag
	case h.definition.Nullable:
		return &h.optional
	default:
		return &h.number
	}
}

func (h *configScanHolder) value() domain.ConfigValue {
	switch {
	case h.definition.Type == domain.ConfigTypeBool:
		return domain.BoolValue(h.flag)
	case h.definition.Nullable:
		if !h.optional.Valid {
			return domain.NullValue(h.definition.Type)
		}
		return domain.IntValue(h.optional.Int64)
	default:
		return domain.IntValue(h.number)
	}
}

// configArgument binds a typed value as the column's parameter.
func configArgument(value domain.ConfigValue) any {
	if value.Null {
		return nil
	}
	switch value.Type {
	case domain.ConfigTypeBool:
		return value.Bool
	case domain.ConfigTypeInt:
		return value.Int
	default:
		return value.Text
	}
}

func nullableRevision(revision int) any {
	if revision <= 0 {
		return nil
	}
	return revision
}

// ReadDocument resolves a document key to the reader that owns it.
//
// The dispatch is a switch and not a map lookup so an unknown document cannot
// be reached at all: there is one configuration document the Admin API can
// read as desired state, and anything else is refused rather than defaulted.
func (s *PGXConfigStore) ReadDocument(ctx context.Context, document domain.ConfigDocument) (domain.ConfigDocumentState, error) {
	switch document {
	case domain.ConfigDocumentAuthPolicy:
		return s.ReadAuthPolicy(ctx)
	default:
		return domain.ConfigDocumentState{}, domain.ErrInvalidInput
	}
}

// ApplyDocument resolves a document key to the writer that owns it.
func (s *PGXConfigStore) ApplyDocument(ctx context.Context, document domain.ConfigDocument, input domain.ConfigApplyInput) (domain.ConfigApplyOutcome, error) {
	switch document {
	case domain.ConfigDocumentAuthPolicy:
		return s.ApplyAuthPolicy(ctx, input)
	default:
		return domain.ConfigApplyOutcome{}, domain.ErrInvalidInput
	}
}
