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

// A substring check (e.g. strings.Contains for "full_name" and
// "display_name" as two independent assertions) would still pass if the two
// NULLIF arguments were swapped in the COALESCE call — it proves the words
// are present, never their order. DisplayNameExpr is deliberately one small,
// stable constant, so comparing it byte-for-byte against the exact expected
// SQL is what actually pins down precedence: full_name can only win over
// display_name if it is truly COALESCE's *first* argument, and the trailing
// empty-string fallback can only be reached once both NULLIFs have collapsed
// to NULL. The PostgreSQL integration test in media-service
// (authorizer_postgres_integration_test.go) is the real-database evidence
// that this SQL, executed for real, produces those three outcomes.
func TestDisplayNameExprIsExactlyFullNameThenDisplayNameThenEmpty(t *testing.T) {
	want := `COALESCE(NULLIF(BTRIM(u.full_name), ''), NULLIF(BTRIM(u.display_name), ''), '')`
	if DisplayNameExpr != want {
		t.Fatalf("DisplayNameExpr = %q, want %q", DisplayNameExpr, want)
	}
}
