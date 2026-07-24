// Package authsession contains the shared PostgreSQL contract for validating
// access-token sessions across NChat services.
package authsession

// ActiveSessionCTE validates the session identified by $1 for the user
// identified by $2. Consumers append their own SELECT and may use
// session_expires_at to cap downstream credentials.
const ActiveSessionCTE = `
	WITH active_session AS (
		SELECT s.user_id,
		       LEAST(
		           s.idle_expires_at,
		           COALESCE(s.absolute_expires_at, 'infinity'::timestamptz)
		       ) AS session_expires_at
		FROM auth.user_sessions AS s
		JOIN auth.users AS u ON u.id = s.user_id
		WHERE s.id = $1
		  AND s.user_id = $2
		  AND s.revoked_at IS NULL
		  AND s.idle_expires_at > now()
		  AND (s.absolute_expires_at IS NULL OR s.absolute_expires_at > now())
		  AND u.status = 'active'
		  AND u.deleted_at IS NULL
		LIMIT 1
	)`
