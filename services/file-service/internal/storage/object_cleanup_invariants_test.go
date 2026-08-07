package storage

// The preview object key is derived in two languages, and they have to agree.
//
// Go derives it in domain.PreviewObjectKey when the worker stores the object.
// SQL derives it in previewObjectKeyExpr when a rejection or a removal enqueues
// that object for cleanup, and when the worker asks whether a key is still
// referenced. Nothing links the two at compile time.
//
// A divergence would not fail loudly. The queue would fill with keys that match
// no stored object — every cleanup succeeding against nothing, the real objects
// accumulating — while live previews read as unreferenced and became eligible
// for deletion. Both failures are silent, which is why the invariant is asserted
// here rather than left to the integration suite: this test needs no database
// and runs on every `go test ./...`.
//
// TestIntegrationPreviewObjectKeyExprMatchesDomain covers the other half, that
// PostgreSQL really evaluates the expression to that string, against a live
// database.

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
)

// previewObjectKeyExprPrefix reads the literal out of the SQL fragment the way
// migration_invariants_test.go reads statements out of a migration: by parsing
// exactly the shape the file is known to have, not by implementing SQL.
func previewObjectKeyExprPrefix(t *testing.T) string {
	t.Helper()
	open := strings.Index(previewObjectKeyExpr, "'")
	if open < 0 {
		t.Fatalf("the key expression has no string literal: %s", previewObjectKeyExpr)
	}
	rest := previewObjectKeyExpr[open+1:]
	close := strings.Index(rest, "'")
	if close < 0 {
		t.Fatalf("the key expression's literal is unterminated: %s", previewObjectKeyExpr)
	}
	return rest[:close]
}

func TestPreviewObjectKeyExprMatchesDomain(t *testing.T) {
	// Fixed, so the assertion is about the derivation and never about whatever
	// uuid.New happened to produce.
	id := uuid.MustParse("3fa301d5-455c-4026-beb7-4c1b7b037d8c")

	prefix := previewObjectKeyExprPrefix(t)
	// What PostgreSQL would build: the literal, then the column as text. The
	// canonical text of a uuid column is exactly uuid.UUID.String() — lowercase,
	// hyphenated — which is the representation this asserts.
	fromSQL := prefix + id.String()

	if want := domain.PreviewObjectKey(id); fromSQL != want {
		t.Fatalf("SQL derives %q, domain derives %q: the two have diverged", fromSQL, want)
	}
}

// The expression must read the column as text, not compare a uuid to a string.
// Dropping the cast would not change today's result but would change the type
// of the comparison, and a silent type change here is the same class of bug the
// test above exists for.
func TestPreviewObjectKeyExprCastsTheColumnToText(t *testing.T) {
	if !strings.Contains(previewObjectKeyExpr, "preview_object_id::text") {
		t.Fatalf("the key expression must derive from preview_object_id::text, got %s",
			previewObjectKeyExpr)
	}
}

// Every statement that names a preview object must go through the shared
// fragment. A statement that spelled the prefix out again would be a second
// definition, and the test above would not see it drift.
func TestEveryPreviewKeyStatementUsesTheSharedExpression(t *testing.T) {
	prefix := previewObjectKeyExprPrefix(t)
	for name, query := range map[string]string{
		"isObjectReferencedQuery":    isObjectReferencedQuery,
		"markScanRejectedQuery":      markScanRejectedQuery,
		"markAttachmentDeletedQuery": markAttachmentDeletedQuery,
	} {
		if !strings.Contains(query, previewObjectKeyExpr) {
			t.Fatalf("%s does not use previewObjectKeyExpr", name)
		}
		// One occurrence of the literal, and it is the one the fragment brought.
		if got := strings.Count(query, prefix); got != 1 {
			t.Fatalf("%s spells the preview prefix %d times, want exactly the shared one", name, got)
		}
	}
}
