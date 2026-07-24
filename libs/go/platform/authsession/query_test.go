package authsession

import (
	"strings"
	"testing"
)

func TestActiveSessionCTEContainsRequiredGuards(t *testing.T) {
	required := []string{
		"WITH active_session AS",
		"FROM auth.user_sessions AS s",
		"JOIN auth.users AS u ON u.id = s.user_id",
		"s.id = $1",
		"s.user_id = $2",
		"s.revoked_at IS NULL",
		"s.idle_expires_at > now()",
		"s.absolute_expires_at IS NULL OR s.absolute_expires_at > now()",
		"u.status = 'active'",
		"u.deleted_at IS NULL",
		"session_expires_at",
	}

	for _, fragment := range required {
		if !strings.Contains(ActiveSessionCTE, fragment) {
			t.Errorf("ActiveSessionCTE missing %q", fragment)
		}
	}
}
