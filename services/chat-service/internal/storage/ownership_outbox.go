package storage

import "context"

// DispatchOwnershipChanges reads committed mutations. Delivery is at least once:
// a crash after publishing and before commit causes a harmless refetch replay.
func (s *PGXOwnershipStore) DispatchOwnershipChanges(ctx context.Context, publish func(context.Context, string, string, string) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `SELECT id,workspace_id::text,conversation_kind,conversation_id::text
 FROM chat.ownership_outbox WHERE published_at IS NULL ORDER BY id LIMIT 50 FOR UPDATE SKIP LOCKED`)
	if err != nil {
		return err
	}
	type event struct {
		id                            int64
		workspace, kind, conversation string
	}
	var events []event
	for rows.Next() {
		var e event
		if err := rows.Scan(&e.id, &e.workspace, &e.kind, &e.conversation); err != nil {
			rows.Close()
			return err
		}
		events = append(events, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, e := range events {
		if err := publish(ctx, e.workspace, e.kind, e.conversation); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE chat.ownership_outbox SET published_at=now() WHERE id=$1`, e.id); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
