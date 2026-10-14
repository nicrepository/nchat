package conversationownership

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type snapshotRow struct {
	conversation, candidate string
	members, owners         int
}
type snapshotRows struct {
	values           []snapshotRow
	index            int
	closed           bool
	scanErr, readErr error
}

func (r *snapshotRows) Next() bool { r.index++; return r.index <= len(r.values) }
func (r *snapshotRows) Scan(dest ...any) error {
	if r.scanErr != nil {
		return r.scanErr
	}
	value := r.values[r.index-1]
	*dest[0].(*string) = "workspace"
	*dest[1].(*string) = "dm"
	*dest[2].(*string) = value.conversation
	*dest[3].(*int) = value.members
	*dest[4].(*int) = value.owners
	*dest[5].(*string) = value.candidate
	return nil
}
func (r *snapshotRows) Err() error { return r.readErr }
func (r *snapshotRows) Close()     { r.closed = true }

// A recording session checks the cursor lifecycle without pretending to apply
// SQL eligibility rules or to perform a database rollback.
type recordingSession struct {
	t                  *testing.T
	rows               *snapshotRows
	queryErr, writeErr error
	queryArgs          []any
	writes             [][]any
}

func (s *recordingSession) session() Session { return Session{Query: s.query, Exec: s.exec} }
func (s *recordingSession) query(_ context.Context, query string, args ...any) (Rows, error) {
	if query != SuccessorsSQL {
		s.t.Fatal("unexpected selector")
	}
	s.queryArgs = args
	return s.rows, s.queryErr
}
func (s *recordingSession) exec(_ context.Context, _ string, args ...any) error {
	if !s.rows.closed {
		s.t.Fatal("snapshot cursor must close before writes on the same transaction")
	}
	s.writes = append(s.writes, args)
	return s.writeErr
}
func requireEqual(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

// Eligibility and ordering belong to SQL and are covered by PostgreSQL tests.
// These snapshots exercise only the coordinator's decisions and write boundary.
func TestInvalidateSnapshotDecisions(t *testing.T) {
	cases := []struct {
		name     string
		rows     []snapshotRow
		promoted []string
		state    string
	}{
		{name: "no affected ownership"},
		{name: "other owner remains", rows: []snapshotRow{{"c1", "candidate", 2, 1}}},
		{name: "no remaining participants", rows: []snapshotRow{{"c1", "", 0, 0}}},
		{name: "selected successor", rows: []snapshotRow{{"c1", "selected", 1, 0}}, promoted: []string{"c1=selected"}},
		{name: "multiple conversations", rows: []snapshotRow{{"c1", "first", 1, 0}, {"c2", "unused", 2, 1}, {"c3", "third", 1, 0}}, promoted: []string{"c1=first", "c3=third"}},
		{name: "later conflict prevents all promotions", rows: []snapshotRow{{"c1", "first", 1, 0}, {"c2", "unused", 2, 1}, {"c3", "", 1, 0}}, state: "P0953"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			session := &recordingSession{t: t, rows: &snapshotRows{values: tc.rows}}
			err := Invalidate(context.Background(), session.session(), "departing", "workspace")
			requireEqual(t, SQLState(err), tc.state)
			if tc.state == "" && err != nil {
				t.Fatal(err)
			}
			requireEqual(t, session.queryArgs, []any{"workspace", "", "", "departing", true})
			var promoted []string
			for _, args := range session.writes {
				requireEqual(t, args[0], "dm")
				requireEqual(t, args[3:], []any{"owner", "", "invalidation"})
				promoted = append(promoted, args[1].(string)+"="+args[2].(string))
			}
			requireEqual(t, promoted, tc.promoted)
			requireEqual(t, session.rows.closed, true)
		})
	}
}

func TestSuccessionPropagatesErrorsBeforeFurtherWrites(t *testing.T) {
	failure := errors.New("database failure")
	cases := []struct {
		name                                 string
		queryErr, scanErr, readErr, writeErr error
		writes                               int
		closed                               bool
	}{
		{name: "query", queryErr: failure},
		{name: "scan", scanErr: failure, closed: true},
		{name: "read", readErr: failure, closed: true},
		{name: "promotion", writeErr: failure, writes: 1, closed: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows := &snapshotRows{values: []snapshotRow{{"c1", "first", 1, 0}, {"c2", "second", 1, 0}}, scanErr: tc.scanErr, readErr: tc.readErr}
			session := &recordingSession{t: t, rows: rows, queryErr: tc.queryErr, writeErr: tc.writeErr}
			err := Succeed(context.Background(), session.session(), Scope{"workspace", "dm", "c1"}, "departing", "actor")
			if !errors.Is(err, failure) {
				t.Fatalf("lost database error: %v", err)
			}
			requireEqual(t, session.queryArgs, []any{"workspace", "dm", "c1", "departing", false})
			requireEqual(t, len(session.writes), tc.writes)
			for _, args := range session.writes {
				requireEqual(t, args[3:], []any{"owner", "actor", "succession"})
			}
			requireEqual(t, rows.closed, tc.closed)
		})
	}
}
