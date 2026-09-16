package storage_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// Issue #821: message priority against a real database.
//
// Three of these cannot be proved with a mock, because what is under test is
// something the database owns and a mock would have to reimplement: the column
// default that makes every pre-existing row mean standard, the CHECK constraint
// that is the last line against an invalid value, and the fact that the edit's
// UPDATE leaves the column alone. A pgxmock agrees with whatever the test hands
// it, including a column order that no longer matches the schema.
//
// Opt-in like its neighbours: needs CHAT_TEST_DATABASE_URL against a _test
// database carrying the real migrations, and it reuses the issue #741 fixture
// rather than seeding a second workspace of its own.
const priorityCheckConstraint = "messages_priority_check"

// insertRawMessage writes straight to the table, bypassing the store, so a test
// can express exactly what an older release — or a repair script — would have
// written. columns is appended to the fixed prefix; pass none to omit priority
// entirely, which is what a writer that predates the column does.
func insertRawMessage(t *testing.T, pool *pgxpool.Pool, id, priorityColumn, priorityValue string) error {
	t.Helper()
	columns, values := "", ""
	args := []any{id, notifyWorkspace, notifyChannel, notifyAuthor}
	if priorityColumn != "" {
		columns, values = ", "+priorityColumn, ", $5"
		args = append(args, priorityValue)
	}
	_, err := pool.Exec(t.Context(),
		`INSERT INTO chat.messages (id, workspace_id, channel_id, sender_id, kind, body_text, body_format, status`+
			columns+`) VALUES ($1, $2, $3, $4, 'user', 'priority fixture', 'v1', 'active'`+values+`)`, args...)
	return err
}

func readRawPriority(t *testing.T, pool *pgxpool.Pool, id string) string {
	t.Helper()
	var priority string
	if err := pool.QueryRow(t.Context(), `SELECT priority FROM chat.messages WHERE id = $1`, id).Scan(&priority); err != nil {
		t.Fatalf("read priority: %v", err)
	}
	return priority
}

// assertPriorityCheckViolation proves the refusal came from this issue's
// constraint and not from some other rule the row happened to break.
func assertPriorityCheckViolation(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected the database to refuse the row")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("expected a PostgreSQL error, got %v", err)
	}
	if pgErr.Code != "23514" {
		t.Fatalf("expected a check violation (23514), got %s: %v", pgErr.Code, err)
	}
	if pgErr.ConstraintName != priorityCheckConstraint {
		t.Fatalf("expected %s, got %s", priorityCheckConstraint, pgErr.ConstraintName)
	}
}

// A row written without naming the column — which is every row that existed
// before 000047, and everything an older release slot writes during a
// Blue/Green deploy — reads as standard. That is the backward compatibility
// this issue asks for, held by the schema rather than by a COALESCE somebody
// has to remember.
func TestMessagePriorityDefaultsToStandardPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	const id = "82100000-0000-4000-8000-000000000001"
	if err := insertRawMessage(t, pool, id, "", ""); err != nil {
		t.Fatalf("insert legacy-shaped message: %v", err)
	}
	if got := readRawPriority(t, pool, id); got != string(domain.MessagePriorityStandard) {
		t.Fatalf("a message written without a priority reads %q, want standard", got)
	}
}

// declaredPriorities is the vocabulary these tests expect the column to hold —
// the domain's three, stated once so a value added there and forgotten here is
// a compile-time change rather than a silent gap.
var declaredPriorities = []domain.MessagePriority{
	domain.MessagePriorityStandard,
	domain.MessagePriorityImportant,
	domain.MessagePriorityUrgent,
}

// The constraint accepts every value the domain declares, and stores it
// verbatim.
func TestMessagePriorityConstraintAcceptsDeclaredValuesPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	for i, priority := range declaredPriorities {
		t.Run(string(priority), func(t *testing.T) {
			id := fmt.Sprintf("82100000-0000-4000-8000-0000000001%02d", i)
			if err := insertRawMessage(t, pool, id, "priority", string(priority)); err != nil {
				t.Fatalf("insert %s: %v", priority, err)
			}
			if got := readRawPriority(t, pool, id); got != string(priority) {
				t.Fatalf("stored priority = %q, want %q", got, priority)
			}
		})
	}
}

// Everything else is refused at INSERT, by this constraint and not by some
// other rule the row happened to break.
func TestMessagePriorityConstraintRejectsUnsupportedValuesPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	for i, priority := range []string{"critical", "URGENT", "high", "", "standard "} {
		t.Run(priority, func(t *testing.T) {
			id := fmt.Sprintf("82100000-0000-4000-8000-0000000002%02d", i)
			assertPriorityCheckViolation(t, insertRawMessage(t, pool, id, "priority", priority))
		})
	}
}

// A row that was valid when written cannot be moved to an invalid value later,
// and a refused UPDATE leaves it exactly as it was.
func TestMessagePriorityConstraintRejectsInvalidUpdatePostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	const id = "82100000-0000-4000-8000-000000000030"
	if err := insertRawMessage(t, pool, id, "priority", "urgent"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	_, err := pool.Exec(t.Context(), `UPDATE chat.messages SET priority = 'critical' WHERE id = $1`, id)
	assertPriorityCheckViolation(t, err)
	if got := readRawPriority(t, pool, id); got != "urgent" {
		t.Fatalf("a refused update must leave the row alone, got %q", got)
	}
}

// createWithPriority writes one message through the store at this priority.
// The idempotency key carries the priority so subtests of one test never
// collide on it.
func createWithPriority(
	t *testing.T, store *storage.PGXMessageStore, priority domain.MessagePriority,
) domain.Message {
	t.Helper()
	input := dmInput("notify-821-" + string(priority))
	input.Priority = priority
	return mustCreate(t, store, input)
}

// listedPriority reads one message back through the listing — the shared
// projection every other message query uses — and returns the priority it
// carries there.
func listedPriority(t *testing.T, store *storage.PGXMessageStore, messageID string) domain.MessagePriority {
	t.Helper()
	listed, err := store.ListDMMessages(t.Context(), storage.ListDMMessagesInput{
		WorkspaceID: notifyWorkspace, ConversationID: notifyConversation, UserID: notifyAuthor,
	})
	if err != nil {
		t.Fatalf("ListDMMessages: %v", err)
	}
	for _, message := range listed.Messages {
		if message.ID == messageID {
			return message.Priority
		}
	}
	t.Fatalf("created message %s is missing from the listing", messageID)
	return ""
}

// A priority written by the store comes back on the created message.
func TestMessagePriorityCreateRoundTripsPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	store := storage.NewPGXMessageStore(pool)
	for _, priority := range declaredPriorities {
		t.Run(string(priority), func(t *testing.T) {
			if created := createWithPriority(t, store, priority); created.Priority != priority {
				t.Fatalf("created Priority = %q, want %q", created.Priority, priority)
			}
		})
	}
}

// And it survives the shared projection. This is the assertion a mock cannot
// make: messageColumns and its scan are positional, and the only thing that can
// prove they still line up with the table is the table.
func TestMessagePriorityListingPreservesPriorityPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	store := storage.NewPGXMessageStore(pool)
	for _, priority := range declaredPriorities {
		t.Run(string(priority), func(t *testing.T) {
			created := createWithPriority(t, store, priority)
			if got := listedPriority(t, store, created.ID); got != priority {
				t.Fatalf("listed Priority = %q, want %q", got, priority)
			}
		})
	}
}

// Editing the body does not move the priority. The edit is the only write path
// to an existing message, and this exercises that path rather than a
// hand-written UPDATE that happens to resemble it.
func TestMessagePriorityEditPreservesPriorityPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	store := storage.NewPGXMessageStore(pool)
	for _, priority := range declaredPriorities {
		t.Run(string(priority), func(t *testing.T) {
			created := createWithPriority(t, store, priority)
			edited, err := store.EditMessage(t.Context(), storage.EditMessageInput{
				WorkspaceID: notifyWorkspace, MessageID: created.ID, EditorID: notifyAuthor,
				Body: "edited body", BodyFormat: domain.MessageBodyFormatV3,
			})
			if err != nil {
				t.Fatalf("EditMessage: %v", err)
			}
			if edited.BodyText != "edited body" || edited.Priority != priority {
				t.Fatalf("edit changed more than the body: body %q, priority %q (want %q)",
					edited.BodyText, edited.Priority, priority)
			}
		})
	}
}

// A message created without a stated priority is a standard one all the way
// through: the store binds the default, the database stores it, and the domain
// reads it back.
func TestMessagePriorityAbsentOnCreateIsStandardPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	created := mustCreate(t, storage.NewPGXMessageStore(pool), dmInput("notify-821-absent"))
	if created.Priority != domain.MessagePriorityStandard {
		t.Fatalf("created Priority = %q, want standard", created.Priority)
	}
	if got := readRawPriority(t, pool, created.ID); got != string(domain.MessagePriorityStandard) {
		t.Fatalf("stored priority = %q, want standard", got)
	}
}
