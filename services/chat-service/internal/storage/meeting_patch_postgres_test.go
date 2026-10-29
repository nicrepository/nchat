package storage_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/nicrepository/nchat/services/chat-service/internal/config"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	httpapi "github.com/nicrepository/nchat/services/chat-service/internal/http"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// The competing commit runs immediately before the request's write locks its
// Meeting. On the old service path its separate GetMeeting has already ended.
type mpBeforeWritePool struct {
	storage.Pool
	beforeWrite func()
	begins      int
}

func (p *mpBeforeWritePool) Begin(ctx context.Context) (pgx.Tx, error) {
	p.begins++
	tx, err := p.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &mpBeforeWriteTx{Tx: tx, pool: p}, nil
}

type mpBeforeWriteTx struct {
	pgx.Tx
	pool *mpBeforeWritePool
}

func (tx *mpBeforeWriteTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if tx.pool.beforeWrite != nil && strings.Contains(sql, "FROM chat.meetings") && strings.Contains(sql, "FOR UPDATE") {
		before := tx.pool.beforeWrite
		tx.pool.beforeWrite = nil
		before()
	}
	return tx.Tx.QueryRow(ctx, sql, args...)
}

// Only transport dependencies are fakes; the service, transaction, facts,
// version checks and CAS below use the real router and PostgreSQL.
type mpTransport struct{}

func (mpTransport) GetDefaultWorkspace(context.Context) (domain.Workspace, error) {
	return domain.Workspace{ID: mtWS}, nil
}
func (mpTransport) ValidateActiveSession(context.Context, string, string) error { return nil }
func (mpTransport) AllowActionWithLimit(context.Context, string, string, int, int) (bool, error) {
	return true, nil
}

func mpPatchRequest(t *testing.T, ctx context.Context, pool storage.Pool, scope storage.MeetingScope, version int64, body string) (http.Handler, *http.Request) {
	t.Helper()
	key := strings.Repeat("q", 32)
	validator, err := httpapi.NewTokenValidator(key, "meeting-patch-test", "meeting-patch-test")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": scope.ActorID, "sid": scope.ActorID, "jti": scope.ActorID,
		"iss": "meeting-patch-test", "aud": "meeting-patch-test", "iat": now.Unix(),
		"nbf": now.Add(-time.Minute).Unix(), "exp": now.Add(time.Hour).Unix(),
	})
	signed, err := token.SignedString([]byte(key))
	if err != nil {
		t.Fatal(err)
	}
	handler := httpapi.NewMeetingHandler(mpTransport{}, maService(pool), mpTransport{})
	router := httpapi.NewRouter(config.Config{ServiceName: "chat-service", Env: "test"}, nil,
		httpapi.ReadinessState{}, validator, mpTransport{}, httpapi.NewSidebarHandler(nil),
		httpapi.NewMessageHandler(nil, nil, nil), nil, nil, nil, nil, handler, nil)
	r := httptest.NewRequest(http.MethodPatch, "/api/chat/meetings/"+scope.MeetingID, strings.NewReader(body)).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer "+signed)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("If-Match", `"`+strconv.FormatInt(version, 10)+`"`)
	return router, r
}

func mpPatchHTTP(t *testing.T, ctx context.Context, pool storage.Pool, scope storage.MeetingScope, version int64, body string) *httptest.ResponseRecorder {
	t.Helper()
	router, r := mpPatchRequest(t, ctx, pool, scope, version, body)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	return w
}

func TestMeetingPatchStalePolicyPostgreSQL(t *testing.T) {
	pool, ctx := meetingsPostgres(t)
	store := storage.NewPGXMeetingStore(pool)
	m := mtCreate(t, ctx, store, mtChannelConversation, "")
	policy := domain.MeetingJoinConversationMembers
	paused := &mpBeforeWritePool{Pool: pool, beforeWrite: func() {
		_, err := maService(pool).UpdateMeeting(ctx, service.UpdateMeetingInput{
			Scope: mtScope(mtOrganizer, m.ID), ExpectedVersion: 1, Patch: service.MeetingPatch{JoinPolicy: &policy},
		})
		mtWant(t, "organizer changes policy before cohost locks", err, nil)
	}}
	w := mpPatchHTTP(t, ctx, paused, mtScope(mtCohost, m.ID), 1, `{"title":"Cohost title"}`)
	if w.Code != 412 || !strings.Contains(w.Body.String(), `"stale_version"`) {
		t.Fatalf("stale cohost PATCH = %d %s, want 412", w.Code, w.Body.String())
	}
	read, roster, err := store.GetMeeting(ctx, mtScope(mtOrganizer, m.ID))
	mtWant(t, "read winner", err, nil)
	if read.Version != 2 || read.JoinPolicy != policy || read.Title != m.Title || len(roster) != 3 {
		t.Fatalf("persisted = %+v, roster %d", read, len(roster))
	}
	if paused.begins != 1 {
		t.Fatalf("PATCH opened %d transactions; want exactly one", paused.begins)
	}
}

func TestMeetingPatchLockedSnapshotPostgreSQL(t *testing.T) {
	pool, ctx := meetingsPostgres(t)
	store := storage.NewPGXMeetingStore(pool)
	m := mtCreate(t, ctx, store, mtChannelConversation, "")
	_, originalRoster, err := store.GetMeeting(ctx, mtScope(mtOrganizer, m.ID))
	mtWant(t, "original roster", err, nil)
	description, zone, policy := "Concurrent agenda", "UTC", domain.MeetingJoinConversationMembers
	start, end := m.StartsAt.Add(time.Hour), m.EndsAt.Add(2*time.Hour)
	var concurrent domain.Meeting
	paused := &mpBeforeWritePool{Pool: pool, beforeWrite: func() {
		concurrent, err = maService(pool).UpdateMeeting(ctx, service.UpdateMeetingInput{
			Scope: mtScope(mtOrganizer, m.ID), ExpectedVersion: 1, Patch: service.MeetingPatch{
				Description: &description, Timezone: &zone, StartsAt: &start, EndsAt: &end, JoinPolicy: &policy,
			},
		})
		mtWant(t, "commit expected version before PATCH locks", err, nil)
	}}
	// At request entry the row is still v1; the controlled commit produces v2
	// before the write reads. No omitted value may come from v1.
	w := mpPatchHTTP(t, ctx, paused, mtScope(mtOrganizer, m.ID), 2, `{"title":"New title"}`)
	if w.Code != 200 || w.Header().Get("ETag") != `"3"` {
		t.Fatalf("PATCH = %d %s", w.Code, w.Body.String())
	}
	read, roster, err := store.GetMeeting(ctx, mtScope(mtOrganizer, m.ID))
	mtWant(t, "read patched meeting", err, nil)
	want := concurrent.MeetingDetails
	want.Title = "New title"
	if read.MeetingDetails != want || read.Version != 3 {
		t.Fatalf("omitted fields reverted: got %+v, want %+v at v3", read, want)
	}
	if !reflect.DeepEqual(roster, originalRoster) {
		t.Fatal("PATCH altered roster")
	}
	if paused.begins != 1 {
		t.Fatalf("PATCH opened %d transactions; want exactly one", paused.begins)
	}
}

func TestMeetingPatchCohostPartialPostgreSQL(t *testing.T) {
	pool, ctx := meetingsPostgres(t)
	store := storage.NewPGXMeetingStore(pool)
	m := mtCreate(t, ctx, store, mtChannelConversation, "")
	w := mpPatchHTTP(t, ctx, pool, mtScope(mtCohost, m.ID), 1, `{"title":"Cohost title"}`)
	if w.Code != 200 || w.Header().Get("ETag") != `"2"` {
		t.Fatalf("cohost title = %d %s", w.Code, w.Body.String())
	}
	read, _, err := store.GetMeeting(ctx, mtScope(mtOrganizer, m.ID))
	mtWant(t, "read cohost edit", err, nil)
	if read.Title != "Cohost title" || read.JoinPolicy != m.JoinPolicy || read.Version != 2 {
		t.Fatalf("cohost edit = %+v", read)
	}
	before := mtSnapshot(t, ctx, pool, m.ID)
	denied := mpPatchHTTP(t, ctx, pool, mtScope(mtCohost, m.ID), 2, `{"join_policy":"conversation_members"}`)
	if denied.Code != 403 || mtSnapshot(t, ctx, pool, m.ID) != before {
		t.Fatalf("policy change = %d %s", denied.Code, denied.Body.String())
	}
}

func TestMeetingPatchAuthorizationPrecedencePostgreSQL(t *testing.T) {
	pool, ctx := meetingsPostgres(t)
	m := mtCreate(t, ctx, storage.NewPGXMeetingStore(pool), mtChannelConversation, "")
	winner := mpPatchHTTP(t, ctx, pool, mtScope(mtOrganizer, m.ID), 1, `{"title":"Version two"}`)
	if winner.Code != 200 {
		t.Fatalf("setup = %d %s", winner.Code, winner.Body.String())
	}
	before := mtSnapshot(t, ctx, pool, m.ID)
	for _, tt := range []struct {
		actor  string
		status int
	}{{mtMember, 404}, {mtAttendee, 403}, {mtCohost, 412}} {
		w := mpPatchHTTP(t, ctx, pool, mtScope(tt.actor, m.ID), 1, `{"join_policy":"conversation_members"}`)
		if w.Code != tt.status || mtSnapshot(t, ctx, pool, m.ID) != before {
			t.Fatalf("actor %s = %d %s, want %d without writes", tt.actor, w.Code, w.Body.String(), tt.status)
		}
	}
}

func TestMeetingPatchConcurrentHTTPPostgreSQL(t *testing.T) {
	pool, ctx := meetingsPostgres(t)
	store := storage.NewPGXMeetingStore(pool)
	m := mtCreate(t, ctx, store, mtChannelConversation, "")
	scope := mtScope(mtOrganizer, m.ID)
	otherRouter, otherRequest := mpPatchRequest(t, ctx, pool, scope, 1, `{"title":"Losing title","description":"Losing agenda"}`)
	done := make(chan *httptest.ResponseRecorder, 1)
	paused := &mtPausingPool{Pool: pool, steps: []mtPause{{"FOR UPDATE", func(pid int) {
		go func() {
			w := httptest.NewRecorder()
			otherRouter.ServeHTTP(w, otherRequest)
			done <- w
		}()
		mtWaitBlocked(t, pool, 1, pid)
	}}}}
	winner := mpPatchHTTP(t, ctx, paused, scope, 1, `{"title":"Winning title","description":"Winning agenda"}`)
	loser := <-done
	if winner.Code != 200 || winner.Header().Get("ETag") != `"2"` || loser.Code != 412 {
		t.Fatalf("simultaneous PATCH = %d %s / %d %s", winner.Code, winner.Body.String(), loser.Code, loser.Body.String())
	}
	read, roster, err := store.GetMeeting(ctx, mtScope(mtOrganizer, m.ID))
	mtWant(t, "persisted simultaneous PATCH", err, nil)
	want := m.MeetingDetails
	want.Title, want.Description = "Winning title", "Winning agenda"
	if read.MeetingDetails != want || read.Version != 2 || len(roster) != 3 {
		t.Fatalf("mixed or lost update = %+v, roster %d", read, len(roster))
	}
}
