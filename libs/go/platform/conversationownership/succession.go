package conversationownership

import "context"

// Rows and Session keep the ownership protocol independent of a database driver.
// Both callbacks must use the same locked SERIALIZABLE transaction.
type Rows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close()
}

type Session struct {
	Query func(context.Context, string, ...any) (Rows, error)
	Exec  func(context.Context, string, ...any) error
}

type Scope struct {
	WorkspaceID, Kind, ConversationID string
}

type ownershipConflict struct{}

func (ownershipConflict) Error() string    { return "private conversation requires an active owner" }
func (ownershipConflict) SQLState() string { return "P0953" }

// SuccessorsSQL is the #1046 selector, grouped for external invalidations.
// The final flag limits discovery to conversations the invalidated user owns.
// Empty scope fields are internal only: global invalidation or a workspace scope.
const SuccessorsSQL = `WITH affected AS (
 SELECT DISTINCT p.workspace_id,p.kind,p.conversation_id
 FROM chat.active_ownership_participants p
 WHERE (NULLIF($1,'')::uuid IS NULL OR p.workspace_id=NULLIF($1,'')::uuid)
 AND ($2='' OR p.kind=$2)
 AND (NULLIF($3,'')::uuid IS NULL OR p.conversation_id=NULLIF($3,'')::uuid)
 AND (NOT $5 OR (p.user_id=$4::uuid AND p.role='owner'))
 AND (SELECT enabled FROM chat.ownership_rollout WHERE singleton)
), remaining AS (
 SELECT p.* FROM chat.active_ownership_participants p
 JOIN affected a USING(workspace_id,kind,conversation_id)
 WHERE p.user_id<>$4::uuid
)
 SELECT a.workspace_id::text,a.kind,a.conversation_id::text,
 count(p.user_id),count(p.user_id) FILTER (WHERE p.role='owner'),
 COALESCE((array_agg(p.user_id ORDER BY CASE p.role WHEN 'admin' THEN 0 ELSE 1 END,
 p.joined_at ASC,p.user_id ASC) FILTER (WHERE NOT p.guest AND p.role IN ('admin','member')))[1]::text,'')
 FROM affected a LEFT JOIN remaining p USING(workspace_id,kind,conversation_id)
 GROUP BY a.workspace_id,a.kind,a.conversation_id
 ORDER BY a.kind,a.conversation_id`

type succession struct {
	scope     Scope
	candidate string
}

// Succeed preserves ownership before a local departure under its conversation lock.
func Succeed(ctx context.Context, session Session, scope Scope, departing, actor string) error {
	return succeed(ctx, session, scope, departing, actor, "succession", false)
}

// Invalidate runs after locking all affected conversations, then validating and
// locking the lifecycle record. It never commits or changes that lifecycle record.
func Invalidate(ctx context.Context, session Session, userID, workspaceID string) error {
	return succeed(ctx, session, Scope{WorkspaceID: workspaceID}, userID, "", "invalidation", true)
}

func succeed(ctx context.Context, session Session, scope Scope, departing, actor, reason string, invalidation bool) error {
	changes, err := selectSuccessions(ctx, session, scope, departing, invalidation)
	if err != nil {
		return err
	}
	for _, change := range changes {
		s := change.scope
		if err := session.Exec(ctx, `SELECT chat.assign_ownership($1,$2::uuid,$3::uuid,$4,NULLIF($5,'')::uuid,$6)`,
			s.Kind, s.ConversationID, change.candidate, "owner", actor, reason); err != nil {
			return err
		}
	}
	return nil
}

func selectSuccessions(ctx context.Context, session Session, scope Scope, departing string, invalidation bool) ([]succession, error) {
	rows, err := session.Query(ctx, SuccessorsSQL, scope.WorkspaceID, scope.Kind, scope.ConversationID, departing, invalidation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var changes []succession
	for rows.Next() {
		var change succession
		var members, owners int
		if err := rows.Scan(&change.scope.WorkspaceID, &change.scope.Kind, &change.scope.ConversationID, &members, &owners, &change.candidate); err != nil {
			return nil, err
		}
		if owners > 0 || members == 0 {
			continue
		}
		if change.candidate == "" {
			return nil, ownershipConflict{}
		}
		changes = append(changes, change)
	}
	return changes, rows.Err()
}
