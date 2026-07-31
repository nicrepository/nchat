package storage_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// fakePool records what a store asked for and replays a scripted answer, so
// query shape and error mapping can be asserted without a live database. The
// SQL itself is exercised against PostgreSQL by the migration and integration
// suites, not here.
type fakePool struct {
	queryRow func(sql string, args ...any) pgx.Row
	query    func(sql string, args ...any) (pgx.Rows, error)
	exec     func(sql string, args ...any) (pgconn.CommandTag, error)
	pingErr  error
	closed   bool

	lastSQL  string
	lastArgs []any
}

func (p *fakePool) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	p.lastSQL, p.lastArgs = sql, args
	if p.queryRow == nil {
		return errRow{err: errors.New("no query configured")}
	}
	return p.queryRow(sql, args...)
}

func (p *fakePool) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	p.lastSQL, p.lastArgs = sql, args
	if p.exec == nil {
		return pgconn.NewCommandTag("UPDATE 1"), nil
	}
	return p.exec(sql, args...)
}

func (p *fakePool) Query(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
	p.lastSQL, p.lastArgs = sql, args
	if p.query == nil {
		return nil, errors.New("no query configured")
	}
	return p.query(sql, args...)
}

func (p *fakePool) Ping(context.Context) error { return p.pingErr }
func (p *fakePool) Close()                     { p.closed = true }

// valueRows replays scripted rows through the pgx.Rows surface the stores use:
// Next, Scan, Err and Close. Everything else panics rather than answering
// plausibly, so a store that starts relying on it fails loudly here.
type valueRows struct {
	rows   [][]any
	index  int
	err    error
	closed bool
}

func (r *valueRows) Next() bool {
	if r.err != nil || r.index >= len(r.rows) {
		return false
	}
	r.index++
	return true
}

func (r *valueRows) Scan(dest ...any) error {
	if r.index == 0 || r.index > len(r.rows) {
		return errors.New("scan called outside a row")
	}
	return valueRow{values: r.rows[r.index-1]}.Scan(dest...)
}

func (r *valueRows) Err() error { return r.err }
func (r *valueRows) Close()     { r.closed = true }

func (r *valueRows) CommandTag() pgconn.CommandTag { return pgconn.CommandTag{} }
func (r *valueRows) FieldDescriptions() []pgconn.FieldDescription {
	return nil
}
func (r *valueRows) Values() ([]any, error) { return nil, errors.New("not supported") }
func (r *valueRows) RawValues() [][]byte    { return nil }
func (r *valueRows) Conn() *pgx.Conn        { return nil }

// valueRow assigns the configured values to the scan destinations in order.
type valueRow struct {
	values []any
}

func (r valueRow) Scan(dest ...any) error {
	if len(dest) != len(r.values) {
		return errors.New("scan destination count mismatch")
	}
	for i, target := range dest {
		if err := assign(target, r.values[i]); err != nil {
			return err
		}
	}
	return nil
}

type errRow struct{ err error }

func (r errRow) Scan(...any) error { return r.err }

// assign copies a scripted value into a scan destination. Tests supply pgtype
// values directly, so nullability is expressed exactly as the driver would.
func assign(target, value any) error {
	pointer := reflect.ValueOf(target)
	if pointer.Kind() != reflect.Pointer || pointer.IsNil() {
		return errors.New("scan destination must be a non-nil pointer")
	}
	if value == nil {
		pointer.Elem().Set(reflect.Zero(pointer.Elem().Type()))
		return nil
	}
	scripted := reflect.ValueOf(value)
	if !scripted.Type().AssignableTo(pointer.Elem().Type()) {
		return fmt.Errorf("cannot assign %s to %s", scripted.Type(), pointer.Elem().Type())
	}
	pointer.Elem().Set(scripted)
	return nil
}
