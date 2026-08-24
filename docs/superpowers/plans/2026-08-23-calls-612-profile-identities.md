# Calls Profile Identities (#612) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Calls show each participant's real profile name (with a local "(você)" suffix, never a "Você" replacement) and configured avatar, falling back to deterministic initials/colors — without touching call authorization, LiveKit grants, or the working name pipeline.

**Architecture:** Local identity comes from the existing `useSelfProfile()` cache; the direct-call counterpart's `avatarUrl` (already resolved, currently dropped) is threaded through; a new minimal batch identity endpoint (one per resource type: channel, group DM) resolves LiveKit's canonical participant-identity UUIDs to `{userId, displayName, avatarUrl}` for resource/group calls, scoped by the same active-membership join the existing details endpoints already use — no N+1, no full-roster assumption. A single extracted `PersonAvatarImage` component replaces three subtly-different avatar-fallback implementations. LiveKit's own name pipeline and `useCallMedia` are untouched; only avatar data is added on top.

**Tech Stack:** React + TypeScript (Vitest/RTL) on `apps/web`; Go + pgx (`pgxmock` unit tests) on `services/chat-service`.

**Spec:** The GitHub issue #612 body (calls-specific slice of #495), reproduced in the task prompt that produced this plan — no separate spec file.

## Global Constraints

- Canonical user IDs remain the only identity/join/authorization key; names and avatars are presentation-only and must never influence call authorization or LiveKit room grants.
- Reuse `apps/web/src/profile/selfProfile.ts` (`useSelfProfile`) for local identity — no new `GET /auth/me` fetch, no localStorage/BroadcastChannel persistence of profile data.
- Reuse the existing `DMCounterpart` contract for direct calls — no second peer-profile fetch.
- Do not rewrite the LiveKit/`useCallMedia` participant-name pipeline (`participant.identity` UUID keying, server-issued `Participant.name`). Only avatars are added on top of it.
- Do not use `ChannelDetails.onlineMembers` / `GroupDetails.participants` as a full roster (both are capped, and the channel one is online-filtered).
- No client HTTP request per participant tile (no N+1) — one batch request per resource call.
- Reuse `initialsFrom` / `avatarColorFor` (`apps/web/src/chat/messageDisplay.ts`) — no second initials algorithm.
- Do not touch #609 (one-call exclusivity), #622 (resource join semantics), #610 (media intent handoff), #611 (screen share), ownership lease/epoch protocol, LiveKit connect/reconnect, participant ordering, active-speaker stabilization, or screen-share publisher selection.
- Do not implement #613 (active-speaker highlighting), #615 (outgoing ringing popup), or #616 (popup redesign) — identity/avatar plumbing only.
- Backend addition (if any) must be narrowly presentation-oriented: authorize via existing workspace/resource visibility, return only `{user_id, display_name, avatar_url}`, never alter call authorization.
- Do not commit — the branch owner handles all git operations.
- **Verified scope decision:** `DedicatedCallStage` currently renders no remote tile at all for a direct ("user"-type) call opened in a dedicated tab — no video binding, no avatar slot, nothing beyond the header `<strong>{title}</strong>` (which already shows the peer's real name via `DedicatedCallPage`'s existing `target.name`). There is no existing display point whose identity is wrong; there is simply no display point. Inventing one would mean adding a new video/avatar tile and threading `media.hasRemoteVideo`/`bindRemoteMedia` into a component that has never accepted them — a media/layout change, not an identity fix, and outside "do not change working call lifecycle/media behavior unless required by a proven identity gap." This plan therefore does **not** add a direct-remote tile to `DedicatedCallStage`; `peer.avatarUrl` stays unused for the `target_type === "user"` dedicated-page case, same as today.

---

## File structure

**Backend — `services/chat-service/internal/`:**

- `domain/workspace.go` — modify: add `CallParticipantProfile` struct + `MaxCallParticipantProfileIDs` cap.
- `domain/errors.go` — modify: add two sentinel errors for the new cap/empty-list validation.
- `storage/member_store.go` — modify: add `ListChannelMemberProfilesByIDs` to `MemberStore` + `PGXMemberStore`.
- `storage/dm_store.go` — modify: add `ListParticipantProfilesByIDs` to `DMStore` + `PGXDMStore`.
- `service/channel_service.go` — modify: add `GetCallParticipantProfiles`.
- `service/dm_service.go` — modify: add `GetGroupCallParticipantProfiles` + shared `normalizeCallParticipantIDs` helper.
- `http/routes.go` — modify: two new route consts.
- `http/channel_handler.go` — modify: `channelProvider` method, `CallParticipants` handler, response types, error mapper.
- `http/dm_handler.go` — modify: `dmProvider` method, `GroupCallParticipants` handler, response types, error mapper.
- `http/router.go` — modify: register the two routes.
- Matching `_test.go` files for each layer above.

**Frontend — `apps/web/src/`:**

- `chat/messageDisplay.ts` — modify: add `localParticipantDisplayName`.
- `chat/PersonAvatarImage.tsx` — create: extracted image/initials-fallback component.
- `chat/ChatSidebar.tsx` — modify: `Avatar` delegates to `PersonAvatarImage` (no behavior change).
- `calls/IncomingCallPopup.tsx` + `.test.tsx` — modify/create: use `PersonAvatarImage` + `initialsFrom`.
- `chat/chatApi.ts` — modify: `fetchChannelCallParticipantProfiles`, `fetchGroupCallParticipantProfiles`.
- `calls/FloatingCallWindow.tsx` + `.test.tsx` (new) — modify/create: local/remote avatar props, real local name.
- `calls/DedicatedCallStage.tsx` + `.test.tsx` (new) — modify/create: avatar props for local + each participant.
- `calls/CallSessionProvider.tsx` + `.test.tsx` — modify: wire self-profile + peer avatar into `FloatingCallWindow`.
- `calls/DedicatedCallPage.tsx` + `.test.tsx` — modify: wire self-profile + batch participant-avatar fetch into `DedicatedCallStage`.
- `calls/CallPresentation.css` — modify: one shared `.call-avatar img` rule.

---

### Task 1: Domain — `CallParticipantProfile` type and request cap

**Files:**

- Modify: `services/chat-service/internal/domain/workspace.go`
- Modify: `services/chat-service/internal/domain/errors.go`
- Test: `services/chat-service/internal/domain/call_participant_profile_test.go` (create)

**Interfaces:**

- Produces: `domain.CallParticipantProfile{UserID, DisplayName, AvatarURL string}`, `domain.MaxCallParticipantProfileIDs = 50`, `domain.ErrTooManyCallParticipantsRequested`, `domain.ErrNoCallParticipantsRequested`.

- [ ] **Step 1: Write the failing test**

```go
// services/chat-service/internal/domain/call_participant_profile_test.go
package domain_test

import (
	"errors"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

func TestMaxCallParticipantProfileIDsIsAPerRequestBatchCap(t *testing.T) {
	if domain.MaxCallParticipantProfileIDs < 1 {
		t.Fatalf("MaxCallParticipantProfileIDs = %d, must allow at least one id", domain.MaxCallParticipantProfileIDs)
	}
	if domain.MaxCallParticipantProfileIDs > 200 {
		t.Fatalf("MaxCallParticipantProfileIDs = %d, larger than a call room's realistic size", domain.MaxCallParticipantProfileIDs)
	}
}

func TestCallParticipantProfileErrorsWrapInvalidInput(t *testing.T) {
	for name, err := range map[string]error{
		"too many":       domain.ErrTooManyCallParticipantsRequested,
		"none requested": domain.ErrNoCallParticipantsRequested,
	} {
		if !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("%s: want wrapped ErrInvalidInput, got %v", name, err)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/domain/... -run TestMaxCallParticipantProfileIDsIsAPerRequestBatchCap -run TestCallParticipantProfileErrorsWrapInvalidInput -v` (from `services/chat-service`)
Expected: FAIL — `undefined: domain.MaxCallParticipantProfileIDs` (compile error)

- [ ] **Step 3: Add the type, cap and errors**

In `domain/workspace.go`, add near `MaxDMDetailsParticipants` (after line 321):

```go
// CallParticipantProfile is the presentation-only identity of one call
// participant: canonical user ID, display name, avatar URL. Deliberately
// slimmer than ChannelMemberProfile/DMParticipantProfile — it carries no
// role and no presence, because a call tile needs neither and the batch
// response should return only what issue #612 asks for.
type CallParticipantProfile struct {
	UserID      string
	DisplayName string
	AvatarURL   string
}

// MaxCallParticipantProfileIDs bounds one call-participant-profile batch
// request (issue #612).
//
// A LiveKit room in this product has no enforced participant ceiling, so
// this is a defensive cap on the request payload, not a room-size limit:
// it stops a caller from turning "resolve who's already in the room" into
// an unbounded IN-list. 50 comfortably covers any real call while staying
// far below anything that could be used to fish for identities.
const MaxCallParticipantProfileIDs = 50
```

In `domain/errors.go`, add next to `ErrNoMembersRequested` (after line 134):

```go
	// ErrTooManyCallParticipantsRequested reports a batch above
	// MaxCallParticipantProfileIDs (issue #612).
	ErrTooManyCallParticipantsRequested = fmt.Errorf("%w: at most %d participant ids may be requested per call", ErrInvalidInput, MaxCallParticipantProfileIDs)
	// ErrNoCallParticipantsRequested reports an empty or all-blank id list.
	ErrNoCallParticipantsRequested = fmt.Errorf("%w: user_ids must contain at least one user", ErrInvalidInput)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/domain/... -run "TestMaxCallParticipantProfileIDsIsAPerRequestBatchCap|TestCallParticipantProfileErrorsWrapInvalidInput" -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add services/chat-service/internal/domain/workspace.go services/chat-service/internal/domain/errors.go services/chat-service/internal/domain/call_participant_profile_test.go
git commit -m "feat(calls): add CallParticipantProfile domain type"
```

---

### Task 2: Storage — channel batch identity query

**Files:**

- Modify: `services/chat-service/internal/storage/member_store.go`
- Test: `services/chat-service/internal/storage/member_store_test.go`

**Interfaces:**

- Consumes: `domain.CallParticipantProfile` (Task 1).
- Produces: `MemberStore.ListChannelMemberProfilesByIDs(ctx, workspaceID, channelID string, userIDs []string) ([]domain.CallParticipantProfile, error)`.

- [ ] **Step 1: Write the failing test**

Append to `member_store_test.go`:

```go
func TestPGXMemberStore_ListChannelMemberProfilesByIDs_ScopesToChannelMembership(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()
	mock.ExpectQuery(`(?s)FROM chat\.channel_members cm.*c\.workspace_id = \$1::uuid.*wm\.status = 'active'.*u\.status = 'active'.*cm\.channel_id = \$2::uuid.*user_id = ANY\(\$3::uuid\[\]\)`).
		WithArgs("ws-1", "ch-1", []string{"user-a", "user-b"}).
		WillReturnRows(pgxmock.NewRows([]string{"user_id", "display_name", "avatar_url"}).
			AddRow("user-a", "Ana Souza", "https://x/a.png"))

	got, err := storage.NewPGXMemberStore(mock).ListChannelMemberProfilesByIDs(
		context.Background(), "ws-1", "ch-1", []string{"user-a", "user-b"},
	)
	if err != nil {
		t.Fatalf("ListChannelMemberProfilesByIDs: %v", err)
	}
	// user-b is not a member of the channel and must not appear — no
	// invented identity for an id the join could not resolve.
	if len(got) != 1 || got[0].UserID != "user-a" || got[0].DisplayName != "Ana Souza" || got[0].AvatarURL != "https://x/a.png" {
		t.Fatalf("unexpected profiles: %#v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXMemberStore_ListChannelMemberProfilesByIDs_EmptyIDsSelectsNoRows(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()
	mock.ExpectQuery(`(?s)FROM chat\.channel_members`).
		WithArgs("ws-1", "ch-1", []string{}).
		WillReturnRows(pgxmock.NewRows([]string{"user_id", "display_name", "avatar_url"}))

	got, err := storage.NewPGXMemberStore(mock).ListChannelMemberProfilesByIDs(
		context.Background(), "ws-1", "ch-1", nil,
	)
	if err != nil {
		t.Fatalf("ListChannelMemberProfilesByIDs: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("nil ids must select nothing, got %#v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/storage/... -run TestPGXMemberStore_ListChannelMemberProfilesByIDs -v`
Expected: FAIL — `undefined: (*storage.PGXMemberStore).ListChannelMemberProfilesByIDs`

- [ ] **Step 3: Implement**

In `member_store.go`, add to the `MemberStore` interface (after `ListOnlineChannelMemberProfiles`'s declaration, inside the interface block starting at line 18):

```go
	// ListChannelMemberProfilesByIDs resolves the subset of userIDs that are
	// active members of channelID, for the call-participant avatar/name
	// lookup (issue #612). Unlike ListOnlineChannelMemberProfiles this is not
	// online-filtered or capped/ordered — the caller already knows exactly
	// which identities it wants (a LiveKit room's current participant list,
	// bounded by MaxCallParticipantProfileIDs) and gets back only the ones
	// that are actually members of this channel; anyone else is silently
	// omitted rather than invented.
	ListChannelMemberProfilesByIDs(ctx context.Context, workspaceID, channelID string, userIDs []string) ([]domain.CallParticipantProfile, error)
```

Add the implementation after `ListOnlineChannelMemberProfiles` (after its closing brace, i.e. after line ~778 area — insert as a new method on `*PGXMemberStore`):

```go
// ListChannelMemberProfilesByIDs resolves presentation identities for a
// specific set of user IDs against one channel's active membership (issue
// #612). It reuses the same active-membership predicate as
// ListOnlineChannelMemberProfiles's active_members CTE — workspace-scoped
// active channel, active workspace membership, active non-deleted user — so
// "who may this caller see identities for" never drifts from "who may this
// caller see at all". There is no ORDER BY/LIMIT: the caller already named
// the exact set it wants (a LiveKit room's participants), bounded by
// MaxCallParticipantProfileIDs before this is ever called.
func (s *PGXMemberStore) ListChannelMemberProfilesByIDs(
	ctx context.Context, workspaceID, channelID string, userIDs []string,
) ([]domain.CallParticipantProfile, error) {
	// A nil slice sends NULL, and `= ANY(NULL)` is NULL rather than false;
	// an empty array is what makes "nobody requested" select no rows.
	if userIDs == nil {
		userIDs = []string{}
	}
	rows, err := s.pool.Query(ctx, `
		SELECT cm.user_id::text,
		       COALESCE(
		           NULLIF(BTRIM(u.full_name), ''),
		           NULLIF(BTRIM(u.display_name), ''),
		           ''
		       ) AS display_name,
		       COALESCE(u.avatar_url, '') AS avatar_url
		FROM chat.channel_members cm
		JOIN chat.channels c
		  ON c.id = cm.channel_id
		 AND c.workspace_id = $1::uuid
		 AND c.status = 'active'
		JOIN chat.workspace_members wm
		  ON wm.workspace_id = c.workspace_id
		 AND wm.user_id = cm.user_id
		 AND wm.status = 'active'
		JOIN auth.users u ON u.id = cm.user_id AND u.status = 'active' AND u.deleted_at IS NULL
		WHERE cm.channel_id = $2::uuid
		  AND cm.user_id = ANY($3::uuid[])`,
		workspaceID, channelID, userIDs)
	if err != nil {
		return nil, fmt.Errorf("list channel member profiles by ids: %w", err)
	}
	defer rows.Close()

	profiles := make([]domain.CallParticipantProfile, 0, len(userIDs))
	for rows.Next() {
		var profile domain.CallParticipantProfile
		if err := rows.Scan(&profile.UserID, &profile.DisplayName, &profile.AvatarURL); err != nil {
			return nil, fmt.Errorf("scan channel member profile: %w", err)
		}
		profiles = append(profiles, profile)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate channel member profiles: %w", err)
	}
	return profiles, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/storage/... -run TestPGXMemberStore_ListChannelMemberProfilesByIDs -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add services/chat-service/internal/storage/member_store.go services/chat-service/internal/storage/member_store_test.go
git commit -m "feat(calls): add channel call-participant profile batch lookup"
```

---

### Task 3: Storage — group DM batch identity query

**Files:**

- Modify: `services/chat-service/internal/storage/dm_store.go`
- Test: `services/chat-service/internal/storage/dm_store_test.go`

**Interfaces:**

- Produces: `DMStore.ListParticipantProfilesByIDs(ctx, workspaceID, conversationID string, userIDs []string) ([]domain.CallParticipantProfile, error)`.

- [ ] **Step 1: Write the failing test**

Append to `dm_store_test.go` (mirror the pgxmock style already used for `TestPGXDMStore_ListParticipantProfiles*`, check that name via `grep ListParticipantProfiles services/chat-service/internal/storage/dm_store_test.go` before writing so the mock args match this file's `pgxmock.NewPool()` helper usage):

```go
func TestPGXDMStore_ListParticipantProfilesByIDs_ScopesToActiveParticipants(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()
	mock.ExpectQuery(`(?s)FROM chat\.dm_members dm.*dc\.workspace_id = \$1::uuid.*wm\.status = 'active'.*u\.status = 'active'.*dm\.conversation_id = \$2::uuid.*dm\.status = 'active'.*user_id = ANY\(\$3::uuid\[\]\)`).
		WithArgs("ws-1", "conv-1", []string{"user-a", "user-b"}).
		WillReturnRows(pgxmock.NewRows([]string{"user_id", "display_name", "avatar_url"}).
			AddRow("user-a", "Ana Souza", ""))

	got, err := storage.NewPGXDMStore(mock).ListParticipantProfilesByIDs(
		context.Background(), "ws-1", "conv-1", []string{"user-a", "user-b"},
	)
	if err != nil {
		t.Fatalf("ListParticipantProfilesByIDs: %v", err)
	}
	if len(got) != 1 || got[0].UserID != "user-a" {
		t.Fatalf("unexpected profiles: %#v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/storage/... -run TestPGXDMStore_ListParticipantProfilesByIDs -v`
Expected: FAIL — `undefined: (*storage.PGXDMStore).ListParticipantProfilesByIDs`

- [ ] **Step 3: Implement**

In `dm_store.go`, add to the `DMStore` interface (after the `ListParticipantProfiles` declaration, around line 66):

```go
	// ListParticipantProfilesByIDs resolves the subset of userIDs that are
	// active participants of conversationID, for the call-participant
	// avatar/name lookup (issue #612). Unlike ListParticipantProfiles this is
	// not capped/ordered — the caller already knows exactly which identities
	// it wants (a LiveKit room's current participant list, bounded by
	// MaxCallParticipantProfileIDs).
	ListParticipantProfilesByIDs(ctx context.Context, workspaceID, conversationID string, userIDs []string) ([]domain.CallParticipantProfile, error)
```

Add the implementation after `ListParticipantProfiles` (after its closing brace, ~line 177):

```go
// ListParticipantProfilesByIDs resolves presentation identities for a
// specific set of user IDs against one conversation's active participants
// (issue #612). Same active-membership predicate as ListParticipantProfiles
// — active conversation in this workspace, active dm_members row, active
// workspace membership, active non-deleted user — so identity resolution
// cannot see further than the details panel already can. No ORDER BY/LIMIT:
// the caller named the exact set, bounded by MaxCallParticipantProfileIDs
// before this is ever called.
func (s *PGXDMStore) ListParticipantProfilesByIDs(
	ctx context.Context, workspaceID, conversationID string, userIDs []string,
) ([]domain.CallParticipantProfile, error) {
	if userIDs == nil {
		userIDs = []string{}
	}
	rows, err := s.pool.Query(ctx, `
		SELECT u.id::text,
		       COALESCE(
		           NULLIF(BTRIM(u.full_name), ''),
		           NULLIF(BTRIM(u.display_name), ''),
		           ''
		       ) AS display_name,
		       COALESCE(u.avatar_url, '') AS avatar_url
		FROM chat.dm_members dm
		JOIN chat.dm_conversations dc
		  ON dc.id = dm.conversation_id
		 AND dc.workspace_id = $1::uuid
		 AND dc.status = 'active'
		JOIN chat.workspace_members wm
		  ON wm.workspace_id = dc.workspace_id
		 AND wm.user_id = dm.user_id
		 AND wm.status = 'active'
		JOIN auth.users u ON u.id = dm.user_id AND u.status = 'active' AND u.deleted_at IS NULL
		WHERE dm.conversation_id = $2::uuid
		  AND dm.status = 'active'
		  AND dm.user_id = ANY($3::uuid[])`,
		workspaceID, conversationID, userIDs)
	if err != nil {
		return nil, fmt.Errorf("list dm participant profiles by ids: %w", err)
	}
	defer rows.Close()

	profiles := make([]domain.CallParticipantProfile, 0, len(userIDs))
	for rows.Next() {
		var profile domain.CallParticipantProfile
		if err := rows.Scan(&profile.UserID, &profile.DisplayName, &profile.AvatarURL); err != nil {
			return nil, fmt.Errorf("scan dm participant profile: %w", err)
		}
		profiles = append(profiles, profile)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate dm participant profiles: %w", err)
	}
	return profiles, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/storage/... -run TestPGXDMStore_ListParticipantProfilesByIDs -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add services/chat-service/internal/storage/dm_store.go services/chat-service/internal/storage/dm_store_test.go
git commit -m "feat(calls): add group DM call-participant profile batch lookup"
```

---

### Task 4: Service layer — channel + DM call-participant-profile use cases

**Files:**

- Modify: `services/chat-service/internal/service/channel_service.go`
- Modify: `services/chat-service/internal/service/dm_service.go`
- Modify: `services/chat-service/internal/service/fake_stores_test.go` (add fake method for `ListChannelMemberProfilesByIDs`)
- Modify: `services/chat-service/internal/service/dm_service_test.go`'s `fakeDMStore` (add fake method for `ListParticipantProfilesByIDs`)
- Test: `services/chat-service/internal/service/channel_service_test.go`
- Test: `services/chat-service/internal/service/dm_service_test.go`

**Interfaces:**

- Consumes: `storage.MemberStore.ListChannelMemberProfilesByIDs`, `storage.DMStore.ListParticipantProfilesByIDs` (Tasks 2, 3), `domain.MaxCallParticipantProfileIDs`, `domain.ErrTooManyCallParticipantsRequested`, `domain.ErrNoCallParticipantsRequested` (Task 1).
- Produces: `ChannelCallParticipantProfilesInput{WorkspaceID, CallerID, ChannelID string; UserIDs []string}`, `(*ChannelService).GetCallParticipantProfiles(ctx, input) ([]domain.CallParticipantProfile, error)`; `GroupCallParticipantProfilesInput{WorkspaceID, CallerID, ConversationID string; UserIDs []string}`, `(*DMService).GetGroupCallParticipantProfiles(ctx, input) ([]domain.CallParticipantProfile, error)`.

- [ ] **Step 1: Write the failing tests**

First check the existing fake-store method signatures so the additions match exactly:

Run: `grep -n "func (f \*fakeMemberStore)" services/chat-service/internal/service/fake_stores_test.go` and `grep -n "func (f \*fakeDMStore)" services/chat-service/internal/service/dm_service_test.go` to see the receiver/import style, then add:

```go
// In fake_stores_test.go, alongside the existing ListOnlineChannelMemberProfiles fake:
func (f *fakeMemberStore) ListChannelMemberProfilesByIDs(
	_ context.Context, _, _ string, userIDs []string,
) ([]domain.CallParticipantProfile, error) {
	if f.listChannelMemberProfilesByIDsErr != nil {
		return nil, f.listChannelMemberProfilesByIDsErr
	}
	var out []domain.CallParticipantProfile
	for _, id := range userIDs {
		if profile, ok := f.callParticipantProfiles[id]; ok {
			out = append(out, profile)
		}
	}
	return out, nil
}
```

Add the two backing fields (`listChannelMemberProfilesByIDsErr error` and `callParticipantProfiles map[string]domain.CallParticipantProfile`) to the `fakeMemberStore` struct definition in the same file.

Add the equivalent to `fakeDMStore` in `dm_service_test.go`:

```go
func (f *fakeDMStore) ListParticipantProfilesByIDs(
	_ context.Context, _, _ string, userIDs []string,
) ([]domain.CallParticipantProfile, error) {
	if f.listParticipantProfilesByIDsErr != nil {
		return nil, f.listParticipantProfilesByIDsErr
	}
	var out []domain.CallParticipantProfile
	for _, id := range userIDs {
		if profile, ok := f.callParticipantProfiles[id]; ok {
			out = append(out, profile)
		}
	}
	return out, nil
}
```

(Same struct-field additions on `fakeDMStore`.)

Then the real tests, in `channel_service_test.go`:

```go
func TestChannelService_GetCallParticipantProfiles_ResolvesOnlyRequestedActiveMembers(t *testing.T) {
	members := newFakeMemberStore()
	members.workspaceMembers["ws-1|caller"] = domain.WorkspaceMember{WorkspaceID: "ws-1", UserID: "caller", Status: "active"}
	members.callParticipantProfiles = map[string]domain.CallParticipantProfile{
		"user-a": {UserID: "user-a", DisplayName: "Ana Souza", AvatarURL: "https://x/a.png"},
	}
	channels := newFakeChannelStore()
	channels.visible["ws-1|ch-1|caller"] = domain.Channel{ID: "ch-1", WorkspaceID: "ws-1"}
	svc := service.NewChannelService(nil, channels, members)

	got, err := svc.GetCallParticipantProfiles(context.Background(), service.ChannelCallParticipantProfilesInput{
		WorkspaceID: "ws-1", CallerID: "caller", ChannelID: "ch-1", UserIDs: []string{"user-a", "user-missing"},
	})
	if err != nil {
		t.Fatalf("GetCallParticipantProfiles: %v", err)
	}
	if len(got) != 1 || got[0].UserID != "user-a" {
		t.Fatalf("unexpected profiles: %#v", got)
	}
}

func TestChannelService_GetCallParticipantProfiles_RejectsOversizedBatch(t *testing.T) {
	svc := service.NewChannelService(nil, newFakeChannelStore(), newFakeMemberStore())
	ids := make([]string, domain.MaxCallParticipantProfileIDs+1)
	for i := range ids {
		ids[i] = uuid.NewString()
	}
	_, err := svc.GetCallParticipantProfiles(context.Background(), service.ChannelCallParticipantProfilesInput{
		WorkspaceID: "ws-1", CallerID: "caller", ChannelID: "ch-1", UserIDs: ids,
	})
	if !errors.Is(err, domain.ErrTooManyCallParticipantsRequested) {
		t.Fatalf("want ErrTooManyCallParticipantsRequested, got %v", err)
	}
}

func TestChannelService_GetCallParticipantProfiles_UnauthorizedCallerGetsNotFound(t *testing.T) {
	svc := service.NewChannelService(nil, newFakeChannelStore(), newFakeMemberStore())
	_, err := svc.GetCallParticipantProfiles(context.Background(), service.ChannelCallParticipantProfilesInput{
		WorkspaceID: "ws-1", CallerID: "caller", ChannelID: "ch-1", UserIDs: []string{uuid.NewString()},
	})
	if !errors.Is(err, domain.ErrNotFound) && !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("want ErrNotFound/ErrForbidden for an unauthorized caller, got %v", err)
	}
}
```

(Adjust `newFakeMemberStore()`/`newFakeChannelStore()`/the `visible`/`workspaceMembers` map keys to whatever this test file's existing helpers actually use — read `channel_service_test.go`'s existing `TestChannelService_GetChannelDetails*` test, if one exists, or the closest visibility test, before writing these three, and copy its exact fixture-construction idiom.)

Mirror the same three tests in `dm_service_test.go` for `GetGroupCallParticipantProfiles`, following that file's existing `TestDMService_GetGroupDetails*` fixture idiom (conversation type must be `domain.DMConversationTypeGroup`; a 1:1 conversation must be rejected the same way `GetGroupDetails` rejects it).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/service/... -run "GetCallParticipantProfiles|GetGroupCallParticipantProfiles" -v`
Expected: FAIL — undefined methods/types

- [ ] **Step 3: Implement**

In `channel_service.go`, add after `GetChannelDetails` (after line 207):

```go
// ChannelCallParticipantProfilesInput asks for presentation identities of a
// specific set of call participants (issue #612). UserIDs is the caller's
// own LiveKit room roster — never trusted as "these are real members", only
// as "resolve these if they are".
type ChannelCallParticipantProfilesInput struct {
	WorkspaceID string
	CallerID    string
	ChannelID   string
	UserIDs     []string
}

// GetCallParticipantProfiles resolves display name and avatar for the
// requested user IDs, scoped to one channel's active membership (issue
// #612). Visibility is settled first, exactly like GetChannelDetails: an
// invisible or foreign channel is ErrNotFound before any identity is read,
// so this cannot be used to probe channel existence or membership. UserIDs
// that are not active members of the channel are silently omitted from the
// result rather than erroring — an unresolvable participant is a client-side
// "degrade to initials" case, not a request failure.
func (s *ChannelService) GetCallParticipantProfiles(ctx context.Context, input ChannelCallParticipantProfilesInput) ([]domain.CallParticipantProfile, error) {
	if _, err := s.requireActiveWorkspaceMember(ctx, input.WorkspaceID, input.CallerID); err != nil {
		return nil, err
	}
	channel, err := s.channels.GetVisibleChannelByID(ctx, input.WorkspaceID, input.ChannelID, input.CallerID)
	if err != nil {
		return nil, err
	}
	userIDs, err := normalizeCallParticipantIDs(input.UserIDs)
	if err != nil {
		return nil, err
	}
	profiles, err := s.members.ListChannelMemberProfilesByIDs(ctx, input.WorkspaceID, channel.ID, userIDs)
	if err != nil {
		return nil, fmt.Errorf("list channel member profiles by ids: %w", err)
	}
	return profiles, nil
}
```

In `dm_service.go`, add after `GetGroupDetails` (after line 383):

```go
// GroupCallParticipantProfilesInput asks for presentation identities of a
// specific set of group-call participants (issue #612).
type GroupCallParticipantProfilesInput struct {
	WorkspaceID    string
	CallerID       string
	ConversationID string
	UserIDs        []string
}

// GetGroupCallParticipantProfiles resolves display name and avatar for the
// requested user IDs, scoped to one group conversation's active
// participants (issue #612). Same access gate as GetGroupDetails — a 1:1
// conversation and one the caller does not participate in both come back as
// ErrNotFound — and unresolvable IDs are silently omitted rather than
// erroring, for the same reason GetCallParticipantProfiles omits them.
func (s *DMService) GetGroupCallParticipantProfiles(ctx context.Context, input GroupCallParticipantProfilesInput) ([]domain.CallParticipantProfile, error) {
	workspaceID := strings.TrimSpace(input.WorkspaceID)
	conversation, err := s.dms.GetVisibleConversationByID(
		ctx, workspaceID, strings.TrimSpace(input.ConversationID), strings.TrimSpace(input.CallerID),
	)
	if err != nil {
		return nil, err
	}
	if conversation.Type != domain.DMConversationTypeGroup {
		return nil, domain.ErrNotFound
	}
	userIDs, err := normalizeCallParticipantIDs(input.UserIDs)
	if err != nil {
		return nil, err
	}
	profiles, err := s.dms.ListParticipantProfilesByIDs(ctx, workspaceID, conversation.ID, userIDs)
	if err != nil {
		return nil, fmt.Errorf("list dm participant profiles by ids: %w", err)
	}
	return profiles, nil
}

// normalizeCallParticipantIDs canonicalises, de-duplicates and bounds a
// requested call-participant ID list (issue #612), the same shape
// normalizeAddMemberIDs applies to add-members but against the call cap
// instead of the add-members cap — the two batches answer different
// questions (who to add vs. whose identity to resolve) and must not share
// one one error message.
func normalizeCallParticipantIDs(raw []string) ([]string, error) {
	if len(raw) > domain.MaxCallParticipantProfileIDs {
		return nil, domain.ErrTooManyCallParticipantsRequested
	}
	unique := make(map[string]struct{}, len(raw))
	for _, rawID := range raw {
		trimmed := strings.TrimSpace(rawID)
		if trimmed == "" {
			return nil, fmt.Errorf("%w: user_ids cannot contain empty user IDs", domain.ErrInvalidInput)
		}
		userID, err := canonicalizeUserID(trimmed)
		if err != nil {
			return nil, err
		}
		unique[userID] = struct{}{}
	}
	if len(unique) == 0 {
		return nil, domain.ErrNoCallParticipantsRequested
	}
	userIDs := make([]string, 0, len(unique))
	for userID := range unique {
		userIDs = append(userIDs, userID)
	}
	sort.Strings(userIDs)
	return userIDs, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/service/... -run "GetCallParticipantProfiles|GetGroupCallParticipantProfiles" -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add services/chat-service/internal/service/channel_service.go services/chat-service/internal/service/dm_service.go services/chat-service/internal/service/fake_stores_test.go services/chat-service/internal/service/dm_service_test.go services/chat-service/internal/service/channel_service_test.go
git commit -m "feat(calls): add call-participant-profile service use cases"
```

---

### Task 5: HTTP — channel call-participants endpoint

**Files:**

- Modify: `services/chat-service/internal/http/routes.go`
- Modify: `services/chat-service/internal/http/channel_handler.go`
- Modify: `services/chat-service/internal/http/router.go`
- Modify: `services/chat-service/internal/http/channel_handler_test.go` (`fakeChannelProvider`)
- Test: `services/chat-service/internal/http/channel_call_participants_handler_test.go` (create)

**Interfaces:**

- Consumes: `service.ChannelCallParticipantProfilesInput`, `(*service.ChannelService).GetCallParticipantProfiles` (Task 4).
- Produces: `POST /api/chat/channels/{channelID}/call-participants` → `{"data":{"profiles":[{"user_id","display_name","avatar_url"}]}}`.

- [ ] **Step 1: Write the failing test**

```go
// services/chat-service/internal/http/channel_call_participants_handler_test.go
package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	httpapi "github.com/nicrepository/nchat/services/chat-service/internal/http"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
)

func TestChannelHandler_CallParticipants_ReturnsResolvedProfiles(t *testing.T) {
	channels := &fakeChannelProvider{
		callParticipantProfiles: []struct {
			UserID, DisplayName, AvatarURL string
		}{{"user-a", "Ana Souza", "https://x/a.png"}},
	}
	handler := httpapi.NewChannelHandler(fakeWorkspaceResolver{}, channels, fakeChannelRateLimiter{allow: true})

	body, _ := json.Marshal(map[string]any{"user_ids": []string{"user-a"}})
	req := httptest.NewRequest(http.MethodPost, "/api/chat/channels/ch-1/call-participants", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("channelID", "ch-1")
	req = req.WithContext(context.WithValue(req.Context(), httpapi.ContextUserIDKey, "caller"))
	recorder := httptest.NewRecorder()

	handler.CallParticipants(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var decoded struct {
		Data struct {
			Profiles []struct {
				UserID      string `json:"user_id"`
				DisplayName string `json:"display_name"`
				AvatarURL   string `json:"avatar_url"`
			} `json:"profiles"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(decoded.Data.Profiles) != 1 || decoded.Data.Profiles[0].UserID != "user-a" {
		t.Fatalf("unexpected body: %s", recorder.Body.String())
	}
}
```

Before writing this file, run `grep -n "ContextUserIDKey\|fakeWorkspaceResolver\|fakeChannelRateLimiter\|type fakeChannelProvider" services/chat-service/internal/http/*.go` to confirm the exact existing helper names/fields in this package's test files (this plan's names are best-effort from the earlier gap analysis and must be reconciled with whatever `channel_handler_test.go` already defines — reuse those fakes rather than redefining them; only add the `callParticipantProfiles` field and its method to the existing `fakeChannelProvider`).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/http/... -run TestChannelHandler_CallParticipants -v`
Expected: FAIL — `handler.CallParticipants undefined`

- [ ] **Step 3: Implement**

In `routes.go`, add after `RouteChannelMemberCandidates` (line 37):

```go
	// Issue #612 call-participant identity resolution. POST because the body
	// carries a batch of LiveKit participant-identity UUIDs — same reasoning
	// as RouteMessageLinkSafetyStatus being POST despite being a read.
	RouteChannelCallParticipants = "/api/chat/channels/{channelID}/call-participants"
```

And in the DM block, after `RouteDMMemberCandidates` (line 38):

```go
	RouteDMCallParticipants = "/api/chat/dm/{conversationID}/call-participants"
```

In `channel_handler.go`, add to the `channelProvider` interface (after `GetChannelDetails`, line 19):

```go
	// GetCallParticipantProfiles resolves presentation identities for a set
	// of call-participant user IDs, scoped to this channel (issue #612).
	GetCallParticipantProfiles(ctx context.Context, input service.ChannelCallParticipantProfilesInput) ([]domain.CallParticipantProfile, error)
```

Add request/response types and the handler, after `channelDetailsBody` (after line 345):

```go
// ── Call-participant profiles (issue #612) ───────────────────────────────────

type callParticipantProfilesRequest struct {
	UserIDs []string `json:"user_ids"`
}

type callParticipantProfileJSON struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
	AvatarURL   string `json:"avatar_url"`
}

type callParticipantProfilesResponse struct {
	Profiles []callParticipantProfileJSON `json:"profiles"`
}

func callParticipantProfilesBody(profiles []domain.CallParticipantProfile) callParticipantProfilesResponse {
	out := make([]callParticipantProfileJSON, 0, len(profiles))
	for _, profile := range profiles {
		out = append(out, callParticipantProfileJSON{
			UserID:      profile.UserID,
			DisplayName: profile.DisplayName,
			AvatarURL:   profile.AvatarURL,
		})
	}
	return callParticipantProfilesResponse{Profiles: out}
}

// callParticipantsRateLimit caps identity-resolution requests: a call joins
// and leaves change the room roster far less often than this allows, so the
// budget exists only to bound abuse, not to constrain real usage.
const callParticipantsRateLimit = 30

// callParticipantsAction is the shared limiter namespace for both
// call-participants routes (channel and group), mirroring addMembersAction.
const callParticipantsAction = "call_participants"

// CallParticipants handles POST /api/chat/channels/{channelID}/call-participants
// (issue #612).
//
// Transport concerns only: batch validation, the cap and de-duplication all
// live in ChannelService.GetCallParticipantProfiles. A user ID that is not
// an active member of this channel is silently absent from the response —
// never a 403/404 for that one ID — so the client's per-participant fallback
// (initials) is the only thing that ever surfaces an unresolved identity.
func (h *ChannelHandler) CallParticipants(w http.ResponseWriter, r *http.Request) {
	if h.workspaces == nil || h.channels == nil || h.limiter == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "channels not available")
		return
	}
	channelID := r.PathValue("channelID")
	if !validateTargetID(w, channelID, "channel_id") {
		return
	}
	callerID := GetContextUserID(r)
	if callerID == "" {
		writeUnauthorized(w)
		return
	}
	allowed, err := h.limiter.AllowActionWithLimit(r.Context(), callerID, callParticipantsAction, callParticipantsRateLimit, channelRateLimitWindowSeconds)
	if err != nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "channels not available")
		return
	}
	if !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(channelRateLimitWindowSeconds))
		httputil.WriteError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
		return
	}
	if !requireJSONContentType(w, r) {
		return
	}
	var request callParticipantProfilesRequest
	if !decodeStrictJSON(w, r, &request) {
		return
	}
	workspace, err := h.workspaces.GetDefaultWorkspace(r.Context())
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "workspace not found")
		} else {
			httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
		}
		return
	}
	profiles, err := h.channels.GetCallParticipantProfiles(r.Context(), service.ChannelCallParticipantProfilesInput{
		WorkspaceID: workspace.ID,
		CallerID:    callerID,
		ChannelID:   channelID,
		UserIDs:     request.UserIDs,
	})
	if err != nil {
		writeCallParticipantProfilesError(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, callParticipantProfilesBody(profiles))
}

// writeCallParticipantProfilesError mirrors writeChannelDetailsError's
// not-found folding (an unauthorized caller and a nonexistent/foreign
// channel are indistinguishable) plus writeAddMembersError's 400 for a
// malformed batch.
func writeCallParticipantProfilesError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalidInput):
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid request")
	case errors.Is(err, domain.ErrNotFound), errors.Is(err, domain.ErrForbidden):
		httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "channel not found")
	default:
		httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
	}
}
```

In `router.go`, register the route near `RouteChannelMemberCandidates` (after line 227's block, inside the same private-scope section that already registers `channels`-backed routes):

```go
		mux.Handle("POST "+RouteChannelCallParticipants, authMiddleware(http.HandlerFunc(channels.CallParticipants)))
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/http/... -run TestChannelHandler_CallParticipants -v`
Expected: PASS

- [ ] **Step 5: Build the whole service to catch interface-satisfaction errors**

Run: `go build ./...` (from `services/chat-service`)
Expected: succeeds — confirms `*service.ChannelService` still satisfies `channelProvider` and the router compiles with the new route.

- [ ] **Step 6: Commit**

```bash
git add services/chat-service/internal/http/routes.go services/chat-service/internal/http/channel_handler.go services/chat-service/internal/http/router.go services/chat-service/internal/http/channel_handler_test.go services/chat-service/internal/http/channel_call_participants_handler_test.go
git commit -m "feat(calls): add channel call-participants HTTP endpoint"
```

---

### Task 6: HTTP — group DM call-participants endpoint

**Files:**

- Modify: `services/chat-service/internal/http/dm_handler.go`
- Modify: `services/chat-service/internal/http/router.go`
- Modify: `services/chat-service/internal/http/dm_handler_test.go` (`fakeDMProvider`)
- Test: `services/chat-service/internal/http/group_call_participants_handler_test.go` (create)

**Interfaces:**

- Consumes: `service.GroupCallParticipantProfilesInput`, `(*service.DMService).GetGroupCallParticipantProfiles` (Task 4), `RouteDMCallParticipants` (Task 5), `callParticipantProfilesRequest`/`callParticipantProfilesResponse`/`callParticipantProfilesBody` (Task 5, same package).
- Produces: `POST /api/chat/dm/{conversationID}/call-participants` → same envelope shape as Task 5.

- [ ] **Step 1: Write the failing test**

Same structure as Task 5's test, retargeted at `dm_handler.go`'s `fakeDMProvider` and `handler.GroupCallParticipants`, hitting path `/api/chat/dm/conv-1/call-participants` with `req.SetPathValue("conversationID", "conv-1")`. Read `dm_handler_test.go`'s existing fakes/helpers first (same reconciliation note as Task 5, Step 1) before writing.

```go
// services/chat-service/internal/http/group_call_participants_handler_test.go
package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	httpapi "github.com/nicrepository/nchat/services/chat-service/internal/http"
)

func TestDMHandler_GroupCallParticipants_ReturnsResolvedProfiles(t *testing.T) {
	dms := &fakeDMProvider{
		callParticipantProfiles: []struct {
			UserID, DisplayName, AvatarURL string
		}{{"user-a", "Ana Souza", ""}},
	}
	handler := httpapi.NewDMHandler(fakeWorkspaceResolver{}, dms, fakeDMRateLimiter{allow: true})

	body, _ := json.Marshal(map[string]any{"user_ids": []string{"user-a"}})
	req := httptest.NewRequest(http.MethodPost, "/api/chat/dm/conv-1/call-participants", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("conversationID", "conv-1")
	req = req.WithContext(context.WithValue(req.Context(), httpapi.ContextUserIDKey, "caller"))
	recorder := httptest.NewRecorder()

	handler.GroupCallParticipants(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/http/... -run TestDMHandler_GroupCallParticipants -v`
Expected: FAIL — `handler.GroupCallParticipants undefined`

- [ ] **Step 3: Implement**

In `dm_handler.go`, add to the `dmProvider` interface (after `GetGroupDetails`, line 26):

```go
	// GetGroupCallParticipantProfiles resolves presentation identities for a
	// set of call-participant user IDs, scoped to this group conversation
	// (issue #612).
	GetGroupCallParticipantProfiles(ctx context.Context, input service.GroupCallParticipantProfilesInput) ([]domain.CallParticipantProfile, error)
```

Add the handler after `writeGroupDetailsError` (after line 240):

```go
// GroupCallParticipants handles POST /api/chat/dm/{conversationID}/call-participants
// (issue #612). Same shape as ChannelHandler.CallParticipants, sharing its
// request/response JSON types since both return the identical
// {user_id, display_name, avatar_url} shape.
func (h *DMHandler) GroupCallParticipants(w http.ResponseWriter, r *http.Request) {
	if h.workspaces == nil || h.dms == nil || h.limiter == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "dms not available")
		return
	}
	conversationID := r.PathValue("conversationID")
	if !validateTargetID(w, conversationID, "conversation_id") {
		return
	}
	callerID := GetContextUserID(r)
	if callerID == "" {
		writeUnauthorized(w)
		return
	}
	allowed, err := h.limiter.AllowActionWithLimit(r.Context(), callerID, callParticipantsAction, callParticipantsRateLimit, dmRateLimitWindowSeconds)
	if err != nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "dms not available")
		return
	}
	if !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(dmRateLimitWindowSeconds))
		httputil.WriteError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
		return
	}
	if !requireJSONContentType(w, r) {
		return
	}
	var request callParticipantProfilesRequest
	if !decodeStrictJSON(w, r, &request) {
		return
	}
	workspace, err := h.workspaces.GetDefaultWorkspace(r.Context())
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "workspace not found")
		} else {
			httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
		}
		return
	}
	profiles, err := h.dms.GetGroupCallParticipantProfiles(r.Context(), service.GroupCallParticipantProfilesInput{
		WorkspaceID:    workspace.ID,
		CallerID:       callerID,
		ConversationID: conversationID,
		UserIDs:        request.UserIDs,
	})
	if err != nil {
		writeCallParticipantProfilesError(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, callParticipantProfilesBody(profiles))
}
```

In `router.go`, register near `RouteDMMemberCandidates`/`RouteDMDetails` (inside the same conditional block that registers `directMessages`-backed routes, after line 259's block):

```go
		mux.Handle("POST "+RouteDMCallParticipants, authMiddleware(http.HandlerFunc(directMessages.GroupCallParticipants)))
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/http/... -run TestDMHandler_GroupCallParticipants -v`
Expected: PASS

- [ ] **Step 5: Run the full chat-service test suite and build**

Run: `go build ./... && go test ./...` (from `services/chat-service`)
Expected: all pass — confirms no interface-satisfaction breakage anywhere else in the service (e.g. any other `MemberStore`/`DMStore`/`channelProvider`/`dmProvider` implementer that needs the new methods, such as mocks in other packages).

- [ ] **Step 6: Commit**

```bash
git add services/chat-service/internal/http/dm_handler.go services/chat-service/internal/http/router.go services/chat-service/internal/http/dm_handler_test.go services/chat-service/internal/http/group_call_participants_handler_test.go
git commit -m "feat(calls): add group DM call-participants HTTP endpoint"
```

---

### Task 7: Frontend — `localParticipantDisplayName` helper

**Files:**

- Modify: `apps/web/src/chat/messageDisplay.ts`
- Test: `apps/web/src/chat/messageDisplay.test.ts` (check if this file exists via `Glob`; create if not, otherwise append)

**Interfaces:**

- Produces: `localParticipantDisplayName(displayName: string): string`.

- [ ] **Step 1: Write the failing test**

```ts
import { describe, expect, it } from "vitest";
import { localParticipantDisplayName } from "./messageDisplay";

describe("localParticipantDisplayName", () => {
  it("appends (você) to a real display name", () => {
    expect(localParticipantDisplayName("Caio Almeida")).toBe("Caio Almeida (você)");
  });

  it("falls back to a bare Você when there is no usable name", () => {
    expect(localParticipantDisplayName("")).toBe("Você");
    expect(localParticipantDisplayName("   ")).toBe("Você");
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd apps/web && npx vitest run src/chat/messageDisplay.test.ts`
Expected: FAIL — `localParticipantDisplayName is not a function`

- [ ] **Step 3: Implement**

In `messageDisplay.ts`, add after `initialsFrom` (after line 55):

```ts
/**
 * The local participant's call-presentation name (issue #612): the real
 * profile name with a "(você)" suffix, or a bare "Você" when there is no
 * usable name yet (profile loading/error/empty). The real name is always
 * primary — "Você" never *replaces* it, only stands in for it when there is
 * nothing else to show.
 */
export function localParticipantDisplayName(displayName: string): string {
  const trimmed = displayName.trim();
  return trimmed ? `${trimmed} (você)` : "Você";
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd apps/web && npx vitest run src/chat/messageDisplay.test.ts`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add apps/web/src/chat/messageDisplay.ts apps/web/src/chat/messageDisplay.test.ts
git commit -m "feat(calls): add localParticipantDisplayName helper"
```

---

### Task 8: Frontend — extract `PersonAvatarImage`, delegate `ChatSidebar`'s `Avatar`

**Files:**

- Create: `apps/web/src/chat/PersonAvatarImage.tsx`
- Test: `apps/web/src/chat/PersonAvatarImage.test.tsx` (create)
- Modify: `apps/web/src/chat/ChatSidebar.tsx` (lines 175-209, the `Avatar` function body)

**Interfaces:**

- Produces: `PersonAvatarImage({ src, initials, imgClassName, alt }: PersonAvatarImageProps)`.
- Consumes (in ChatSidebar): nothing new — `Avatar`'s external props (`initials`, `src`, `color`, `status`, `size`) are unchanged.

- [ ] **Step 1: Write the failing test**

```tsx
// apps/web/src/chat/PersonAvatarImage.test.tsx
import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { PersonAvatarImage } from "./PersonAvatarImage";

describe("PersonAvatarImage", () => {
  it("renders the image when src is usable", () => {
    render(<PersonAvatarImage src="https://x/a.png" initials="CA" imgClassName="img" />);
    expect(screen.getByRole("img")).toHaveAttribute("src", "https://x/a.png");
  });

  it("falls back to initials when src is absent", () => {
    render(<PersonAvatarImage initials="CA" imgClassName="img" />);
    expect(screen.queryByRole("img")).not.toBeInTheDocument();
    expect(screen.getByText("CA")).toBeInTheDocument();
  });

  it("falls back to initials, without a broken-image glyph, once the image errors", () => {
    render(<PersonAvatarImage src="https://x/broken.png" initials="CA" imgClassName="img" />);
    fireEvent.error(screen.getByRole("img"));
    expect(screen.queryByRole("img")).not.toBeInTheDocument();
    expect(screen.getByText("CA")).toBeInTheDocument();
  });

  it("retries a changed src after a previous one failed", () => {
    const { rerender } = render(
      <PersonAvatarImage src="https://x/broken.png" initials="CA" imgClassName="img" />,
    );
    fireEvent.error(screen.getByRole("img"));
    expect(screen.queryByRole("img")).not.toBeInTheDocument();

    rerender(<PersonAvatarImage src="https://x/new.png" initials="CA" imgClassName="img" />);
    expect(screen.getByRole("img")).toHaveAttribute("src", "https://x/new.png");
  });

  it("uses referrerPolicy no-referrer and an empty alt by default", () => {
    render(<PersonAvatarImage src="https://x/a.png" initials="CA" imgClassName="img" />);
    const img = screen.getByRole("img");
    expect(img).toHaveAttribute("referrerpolicy", "no-referrer");
    expect(img).toHaveAttribute("alt", "");
  });
});
```

Note: `getByRole("img")` requires a non-empty accessible name OR `screen.getByRole("img", { hidden: true })`/`container.querySelector("img")` — an `<img alt="">` is presentational and excluded from the accessibility tree by RTL's default role query. Use `container.querySelector("img")` (via `render(...).container`) instead of `screen.getByRole("img")` throughout this file once you confirm that in Step 2's failure output; adjust before Step 3.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd apps/web && npx vitest run src/chat/PersonAvatarImage.test.tsx`
Expected: FAIL — module not found. Also confirms/refutes the `getByRole("img")` note above; fix the test file's queries first if needed, then re-run to confirm the _intended_ failure (missing module, not a query error).

- [ ] **Step 3: Implement**

```tsx
// apps/web/src/chat/PersonAvatarImage.tsx
import { useState } from "react";

interface PersonAvatarImageProps {
  /** Optional picture. Initials render when absent or when loading fails. */
  src?: string;
  initials: string;
  imgClassName: string;
  /**
   * Accessible text for the image. "" (the default) is correct whenever an
   * adjacent element already names this person — the common case, since
   * every call site pairs this with a visible name nearby. Pass the full
   * display name only when the avatar is the sole visible identity for that
   * person, per issue #612's accessibility rule: initials must never be the
   * only identity exposed to screen readers, but a redundant name here would
   * double-announce it when a caption already exists.
   */
  alt?: string;
}

/**
 * Image-or-initials fallback shared by every avatar in the app (issue #612):
 * personalized image when usable, initials on load failure or when no image
 * is set, never a broken-image glyph. Extracted from ChatSidebar's local
 * `Avatar` component, which now delegates here instead of keeping its own
 * copy of this state machine.
 *
 * The caller owns the wrapping element (size, background color, shape) —
 * this renders only the image-or-text content, so three call sites with
 * three different wrapper markups do not have to agree on one.
 */
export function PersonAvatarImage({
  src,
  initials,
  imgClassName,
  alt = "",
}: PersonAvatarImageProps) {
  // A load failure is scoped to the URL that was current when it happened,
  // so a change of src must clear it — otherwise an A -> B -> A cycle would
  // never retry A. State is reset during render (guarded so it only runs
  // when src actually changes) rather than in an effect, matching
  // ChatSidebar's original implementation.
  const [failedSrc, setFailedSrc] = useState<string | null>(null);
  const [trackedSrc, setTrackedSrc] = useState(src);
  if (src !== trackedSrc) {
    setTrackedSrc(src);
    setFailedSrc(null);
  }
  const showImage = Boolean(src) && failedSrc !== src;

  return showImage ? (
    <img
      className={imgClassName}
      src={src}
      alt={alt}
      referrerPolicy="no-referrer"
      onError={() => setFailedSrc(src ?? null)}
    />
  ) : (
    <>{initials}</>
  );
}
```

Then in `ChatSidebar.tsx`, replace the `Avatar` function body (lines 175-209) with:

```tsx
function Avatar({ initials, src, color = "purple", status, size = "sm" }: AvatarProps) {
  return (
    <span
      className={`chat-sidebar__avatar chat-sidebar__avatar--${color} chat-sidebar__avatar--${size}`}
      aria-hidden="true"
    >
      <PersonAvatarImage src={src} initials={initials} imgClassName="chat-sidebar__avatar-img" />
      {status && <PresenceDot state={status} size={size} ringColor="var(--cs-sidebar-bg)" />}
    </span>
  );
}
```

Add the import near the top of `ChatSidebar.tsx` (alongside its other same-directory imports):

```ts
import { PersonAvatarImage } from "./PersonAvatarImage";
```

`useState` may become unused in `ChatSidebar.tsx` if `Avatar` was its only local-state consumer of that specific pattern — check with the existing `import { useState } from "react"` line; leave it if other components in the file still use `useState` (they do — `ChatSidebar` itself and others), so no import cleanup is needed there.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd apps/web && npx vitest run src/chat/PersonAvatarImage.test.tsx src/chat/ChatSidebar.test.tsx`
Expected: PASS on both — `ChatSidebar.test.tsx` passing unmodified is the proof that the extraction is behavior-preserving.

- [ ] **Step 5: Commit**

```bash
git add apps/web/src/chat/PersonAvatarImage.tsx apps/web/src/chat/PersonAvatarImage.test.tsx apps/web/src/chat/ChatSidebar.tsx
git commit -m "refactor(chat): extract PersonAvatarImage from ChatSidebar's Avatar"
```

---

### Task 9: Frontend — `IncomingCallPopup` uses the shared avatar + `initialsFrom`

**Files:**

- Modify: `apps/web/src/calls/IncomingCallPopup.tsx`
- Test: `apps/web/src/calls/IncomingCallPopup.test.tsx` (create — none exists today)

**Interfaces:**

- Consumes: `PersonAvatarImage` (Task 8), `initialsFrom` (existing, `../chat/messageDisplay`).
- No prop-shape change: `IncomingCallPopupProps` stays exactly as-is (`name`, `avatarUrl`, ...).

- [ ] **Step 1: Write the failing test**

```tsx
// apps/web/src/calls/IncomingCallPopup.test.tsx
import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import IncomingCallPopup from "./IncomingCallPopup";

const baseProps = {
  name: "Caio Almeida",
  targetKind: "user" as const,
  callType: "video" as const,
  onAccept: vi.fn(),
  onReject: vi.fn(),
};

describe("IncomingCallPopup", () => {
  it("renders the caller's real name", () => {
    render(<IncomingCallPopup {...baseProps} />);
    expect(screen.getByText("Caio Almeida")).toBeInTheDocument();
  });

  it("shows two-letter initials, not a single raw character, when there is no avatar", () => {
    const { container } = render(<IncomingCallPopup {...baseProps} />);
    expect(container.querySelector(".incoming-call__avatar")).toHaveTextContent("CA");
  });

  it("renders the personalized avatar image when avatarUrl is set", () => {
    const { container } = render(<IncomingCallPopup {...baseProps} avatarUrl="https://x/a.png" />);
    expect(container.querySelector("img")).toHaveAttribute("src", "https://x/a.png");
  });

  it("falls back to initials, not a broken-image glyph, when the avatar fails to load", () => {
    const { container } = render(
      <IncomingCallPopup {...baseProps} avatarUrl="https://x/broken.png" />,
    );
    fireEvent.error(container.querySelector("img")!);
    expect(container.querySelector("img")).not.toBeInTheDocument();
    expect(container.querySelector(".incoming-call__avatar")).toHaveTextContent("CA");
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd apps/web && npx vitest run src/calls/IncomingCallPopup.test.tsx`
Expected: FAIL on the initials test — current code shows `name.at(0)` (`"C"`), not `"CA"`.

- [ ] **Step 3: Implement**

In `IncomingCallPopup.tsx`, add the imports and replace the avatar div body (lines 48-50):

```tsx
import { initialsFrom } from "../chat/messageDisplay";
import { PersonAvatarImage } from "../chat/PersonAvatarImage";
```

```tsx
<div className="incoming-call__avatar" aria-hidden="true">
  <PersonAvatarImage
    src={avatarUrl}
    initials={initialsFrom(name)}
    imgClassName="incoming-call__avatar-img"
  />
</div>
```

Add a matching CSS class alias in `CallPresentation.css` (the existing rule targets `.incoming-call__avatar img`, which still matches since `imgClassName` only adds a class, it doesn't replace element type) — no CSS change is actually required here; confirm by inspection that `.incoming-call__avatar img { width:100%; height:100%; object-fit:cover; }` (line 64-68) still applies to `<img className="incoming-call__avatar-img">` nested inside `.incoming-call__avatar` (it does, `img` is a plain descendant-tag selector, class-agnostic).

- [ ] **Step 4: Run test to verify it passes**

Run: `cd apps/web && npx vitest run src/calls/IncomingCallPopup.test.tsx`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add apps/web/src/calls/IncomingCallPopup.tsx apps/web/src/calls/IncomingCallPopup.test.tsx
git commit -m "fix(calls): use shared avatar fallback and initialsFrom in IncomingCallPopup"
```

---

### Task 10: Frontend — `chatApi` batch call-participant-profile fetchers

**Files:**

- Modify: `apps/web/src/chat/chatApi.ts`
- Test: `apps/web/src/chat/chatApi.test.ts` (check via Glob for the existing test file name — likely `chatApi.test.ts`; append to it)

**Interfaces:**

- Consumes: `CHAT_BASE`, `authenticatedFetch`, `safeAvatarUrl` (all existing, same file).
- Produces: `interface CallParticipantProfile { userId: string; displayName: string; avatarUrl?: string }`; `fetchChannelCallParticipantProfiles(channelId: string, userIds: string[], signal?: AbortSignal): Promise<CallParticipantProfile[]>`; `fetchGroupCallParticipantProfiles(conversationId: string, userIds: string[], signal?: AbortSignal): Promise<CallParticipantProfile[]>`.

- [ ] **Step 1: Write the failing test**

Append to `chatApi.test.ts` (following that file's existing `fetchMock`/`authenticatedFetch`-mocking convention — read its top-of-file setup first via Grep for `describe("fetchChannelDetails"` to copy the exact fetch-mocking idiom):

```ts
describe("fetchChannelCallParticipantProfiles", () => {
  it("posts the requested user ids and maps the response", async () => {
    mockFetch.mockResolvedValueOnce(
      jsonResponse({
        data: {
          profiles: [
            { user_id: "user-a", display_name: "Ana Souza", avatar_url: "https://x/a.png" },
          ],
        },
      }),
    );
    const profiles = await fetchChannelCallParticipantProfiles("ch-1", ["user-a"]);
    expect(profiles).toEqual([
      { userId: "user-a", displayName: "Ana Souza", avatarUrl: "https://x/a.png" },
    ]);
    const [url, init] = mockFetch.mock.calls[0];
    expect(url).toContain("/channels/ch-1/call-participants");
    expect(JSON.parse(init.body as string)).toEqual({ user_ids: ["user-a"] });
  });

  it("omits avatarUrl when the server sends none", async () => {
    mockFetch.mockResolvedValueOnce(
      jsonResponse({ data: { profiles: [{ user_id: "user-a", display_name: "Ana Souza" }] } }),
    );
    const [profile] = await fetchChannelCallParticipantProfiles("ch-1", ["user-a"]);
    expect(profile.avatarUrl).toBeUndefined();
  });
});

describe("fetchGroupCallParticipantProfiles", () => {
  it("posts to the dm call-participants route", async () => {
    mockFetch.mockResolvedValueOnce(jsonResponse({ data: { profiles: [] } }));
    await fetchGroupCallParticipantProfiles("conv-1", ["user-a"]);
    const [url] = mockFetch.mock.calls[0];
    expect(url).toContain("/dm/conv-1/call-participants");
  });
});
```

(Replace `mockFetch`/`jsonResponse` with this test file's actual existing mock helper names — inspect the file before writing.)

- [ ] **Step 2: Run test to verify it fails**

Run: `cd apps/web && npx vitest run src/chat/chatApi.test.ts -t "CallParticipantProfiles"`
Expected: FAIL — functions not exported

- [ ] **Step 3: Implement**

Add near `fetchGroupDetails` (end of the "Group details" section, after its closing brace):

```ts
// ── Call-participant profiles (issue #612) ───────────────────────────────────

export interface CallParticipantProfile {
  userId: string;
  displayName: string;
  /** Absent when unset or when the stored URL is not a safe http(s) target. */
  avatarUrl?: string;
}

interface CallParticipantProfileResponse {
  user_id?: unknown;
  display_name?: unknown;
  avatar_url?: unknown;
}

interface CallParticipantProfilesEnvelope {
  data: { profiles?: unknown };
}

function mapCallParticipantProfile(raw: unknown): CallParticipantProfile | undefined {
  if (typeof raw !== "object" || raw === null) return undefined;
  const profile = raw as CallParticipantProfileResponse;
  if (typeof profile.user_id !== "string" || profile.user_id === "") return undefined;
  return {
    userId: profile.user_id,
    displayName: typeof profile.display_name === "string" ? profile.display_name : "",
    avatarUrl: safeAvatarUrl(profile.avatar_url),
  };
}

function mapCallParticipantProfiles(
  envelope: CallParticipantProfilesEnvelope,
): CallParticipantProfile[] {
  return Array.isArray(envelope.data.profiles)
    ? envelope.data.profiles
        .map(mapCallParticipantProfile)
        .filter((profile): profile is CallParticipantProfile => profile !== undefined)
    : [];
}

/**
 * Resolves presentation identities (name, avatar) for a set of call
 * participants in one round trip, scoped to a channel the caller can see
 * (issue #612). A user ID that is not an active member of the channel is
 * simply absent from the result — the caller degrades that tile to initials,
 * never to the raw UUID.
 */
export async function fetchChannelCallParticipantProfiles(
  channelId: string,
  userIds: string[],
  signal?: AbortSignal,
): Promise<CallParticipantProfile[]> {
  const res = await authenticatedFetch<CallParticipantProfilesEnvelope>(
    `${CHAT_BASE}/channels/${encodeURIComponent(channelId)}/call-participants`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ user_ids: userIds }),
      signal,
    },
  );
  return mapCallParticipantProfiles(res);
}

/**
 * Same as fetchChannelCallParticipantProfiles, scoped to a group conversation.
 */
export async function fetchGroupCallParticipantProfiles(
  conversationId: string,
  userIds: string[],
  signal?: AbortSignal,
): Promise<CallParticipantProfile[]> {
  const res = await authenticatedFetch<CallParticipantProfilesEnvelope>(
    `${CHAT_BASE}/dm/${encodeURIComponent(conversationId)}/call-participants`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ user_ids: userIds }),
      signal,
    },
  );
  return mapCallParticipantProfiles(res);
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd apps/web && npx vitest run src/chat/chatApi.test.ts -t "CallParticipantProfiles"`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add apps/web/src/chat/chatApi.ts apps/web/src/chat/chatApi.test.ts
git commit -m "feat(calls): add batch call-participant-profile API clients"
```

---

### Task 11: Frontend — `FloatingCallWindow` real local/remote identity

**Files:**

- Modify: `apps/web/src/calls/FloatingCallWindow.tsx`
- Modify: `apps/web/src/calls/CallPresentation.css`
- Test: `apps/web/src/calls/FloatingCallWindow.test.tsx` (create — none exists today)

**Interfaces:**

- Consumes: `PersonAvatarImage` (Task 8).
- Produces (new props on `FloatingCallWindowProps`): `localName: string` (replaces the hardcoded `"Você"`), `localAvatarUrl?: string`, `avatarUrl?: string` (remote).

- [ ] **Step 1: Write the failing test**

```tsx
// apps/web/src/calls/FloatingCallWindow.test.tsx
import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import FloatingCallWindow from "./FloatingCallWindow";

const baseControls = {
  microphoneEnabled: true,
  cameraEnabled: true,
  screenShareEnabled: false,
  pendingControl: null,
  onMicrophone: vi.fn(),
  onCamera: vi.fn(),
  onScreenShare: vi.fn(),
  onEnd: vi.fn(),
};

const baseProps = {
  title: "Caio Almeida",
  status: "connected" as const,
  participantCount: 2,
  controls: baseControls,
  onExpand: vi.fn(),
  hasRemoteVideo: false,
  remoteSeed: "peer-1",
  hasLocalVideo: false,
  localSeed: "user-1",
  localName: "Ana Souza (você)",
};

describe("FloatingCallWindow", () => {
  it("shows the local participant's real name with (você), never a bare Você replacing it", () => {
    const { container } = render(<FloatingCallWindow {...baseProps} />);
    expect(container.querySelector(".floating-call__local-avatar")).toHaveAttribute(
      "aria-label",
      "Ana Souza (você)",
    );
  });

  it("falls back to a bare Você when there is no local name yet", () => {
    const { container } = render(<FloatingCallWindow {...baseProps} localName="Você" />);
    expect(container.querySelector(".floating-call__local-avatar")).toHaveAttribute(
      "aria-label",
      "Você",
    );
    expect(container.querySelector(".floating-call__local-avatar")).toHaveTextContent("?");
  });

  it("renders the local avatar image when localAvatarUrl is set", () => {
    const { container } = render(
      <FloatingCallWindow {...baseProps} localAvatarUrl="https://x/local.png" />,
    );
    expect(container.querySelector(".floating-call__local-avatar img")).toHaveAttribute(
      "src",
      "https://x/local.png",
    );
  });

  it("falls back to deterministic initials when the local avatar fails to load", () => {
    const { container } = render(
      <FloatingCallWindow {...baseProps} localAvatarUrl="https://x/broken.png" />,
    );
    fireEvent.error(container.querySelector(".floating-call__local-avatar img")!);
    expect(container.querySelector(".floating-call__local-avatar img")).not.toBeInTheDocument();
    expect(container.querySelector(".floating-call__local-avatar")).toHaveTextContent("AS");
  });

  it("renders the remote avatar image for a direct call when avatarUrl is set", () => {
    const { container } = render(
      <FloatingCallWindow {...baseProps} avatarUrl="https://x/remote.png" />,
    );
    expect(container.querySelector(".floating-call__avatar img")).toHaveAttribute(
      "src",
      "https://x/remote.png",
    );
  });

  it("falls back to initials from the title when there is no remote avatar", () => {
    const { container } = render(<FloatingCallWindow {...baseProps} />);
    expect(container.querySelector(".floating-call__avatar")).toHaveTextContent("CA");
  });

  it("camera-on: renders no avatar fallback for either party", () => {
    const { container } = render(
      <FloatingCallWindow {...baseProps} hasLocalVideo hasRemoteVideo />,
    );
    expect(container.querySelector(".floating-call__avatar")).not.toBeInTheDocument();
    expect(container.querySelector(".floating-call__local-avatar")).not.toBeInTheDocument();
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd apps/web && npx vitest run src/calls/FloatingCallWindow.test.tsx`
Expected: FAIL — `localName` prop unused/no aria-label, remote/local still hardcode `initialsFrom("Você")`/no avatar image at all.

- [ ] **Step 3: Implement**

In `FloatingCallWindow.tsx`:

1. Add the import:

```tsx
import { PersonAvatarImage } from "../chat/PersonAvatarImage";
```

2. Extend `FloatingCallWindowProps` (after `remoteSeed`, before `hasLocalVideo`, and after `localSeed`):

```ts
  /** Direct-call peer's avatar; absent for a resource/group call or a peer with no configured avatar. */
  avatarUrl?: string;
  /**
   * The local participant's call-presentation name (issue #612) — the real
   * profile name plus "(você)", or a bare "Você" fallback. Computed by the
   * caller (CallSessionProvider) via localParticipantDisplayName, since only
   * the caller knows the self-profile state.
   */
  localName: string;
  /** The local participant's configured avatar, when available. */
  localAvatarUrl?: string;
```

3. Add `avatarUrl`, `localName`, `localAvatarUrl` to the destructured props list.

4. Replace the remote avatar fallback block (lines 229-238):

```tsx
{
  !hasRemoteVideo && (
    <div className="floating-call__avatar-wrap">
      <div
        className={`floating-call__avatar call-avatar call-avatar--${avatarColorFor(remoteSeed)}`}
        aria-hidden="true"
      >
        <PersonAvatarImage
          src={avatarUrl}
          initials={initialsFrom(title)}
          imgClassName="call-avatar__img"
        />
      </div>
    </div>
  );
}
```

5. Replace the local avatar fallback block (lines 241-248). This one has no adjacent visible name anywhere in the floating window, so per the issue's accessibility rule it must carry the full name itself — `aria-hidden` is removed and replaced with `role="img" aria-label={localName}`:

```tsx
{
  !hasLocalVideo && (
    <div
      className={`floating-call__local-avatar call-avatar call-avatar--${avatarColorFor(localSeed)}`}
      role="img"
      aria-label={localName}
    >
      <PersonAvatarImage
        src={localAvatarUrl}
        initials={initialsFrom(localName)}
        imgClassName="call-avatar__img"
      />
    </div>
  );
}
```

- [ ] **Step 4: Add the shared `.call-avatar img` CSS rule**

In `CallPresentation.css`, `.call-avatar` (lines 396-402) already wraps every one of these divs (`floating-call__avatar call-avatar ...`, `floating-call__local-avatar call-avatar ...`, `dedicated-call__avatar call-avatar ...`) but has no `overflow: hidden`, so a nested `<img>` would spill past the circle. Update it:

```css
.call-avatar {
  display: grid;
  place-items: center;
  overflow: hidden;
  border-radius: 50%;
  color: #f8fafc;
  font-weight: 700;
}
.call-avatar img {
  width: 100%;
  height: 100%;
  object-fit: cover;
}
```

(Only the `overflow: hidden` line and the new `.call-avatar img` block are additions; the rest of `.call-avatar` is unchanged.) This single rule covers `.floating-call__avatar`, `.floating-call__local-avatar`, and `.dedicated-call__avatar` (Task 12) since all three already compose `.call-avatar`.

- [ ] **Step 5: Run test to verify it passes**

Run: `cd apps/web && npx vitest run src/calls/FloatingCallWindow.test.tsx`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add apps/web/src/calls/FloatingCallWindow.tsx apps/web/src/calls/FloatingCallWindow.test.tsx apps/web/src/calls/CallPresentation.css
git commit -m "feat(calls): render real local/remote identity in FloatingCallWindow"
```

---

### Task 12: Frontend — `DedicatedCallStage` real local + per-participant identity

**Files:**

- Modify: `apps/web/src/calls/DedicatedCallStage.tsx`
- Test: `apps/web/src/calls/DedicatedCallStage.test.tsx` (create — none exists today)

**Interfaces:**

- Consumes: `PersonAvatarImage` (Task 8).
- Produces: `DedicatedParticipant` gains `avatarUrl?: string`; the component's props gain `localDisplayName: string` (replaces the module-level `"Você"` constant) and `localAvatarUrl?: string`.

- [ ] **Step 1: Write the failing test**

```tsx
// apps/web/src/calls/DedicatedCallStage.test.tsx
import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import DedicatedCallStage from "./DedicatedCallStage";

const baseControls = {
  microphoneEnabled: true,
  cameraEnabled: true,
  screenShareEnabled: false,
  pendingControl: null,
  onMicrophone: vi.fn(),
  onCamera: vi.fn(),
  onScreenShare: vi.fn(),
  onEnd: vi.fn(),
};

const baseProps = {
  title: "Equipe Infra",
  status: "connected" as const,
  participantCount: 3,
  participants: [
    { identity: "user-a", displayName: "Ana Souza", hasVideo: false, avatarUrl: "https://x/a.png" },
    { identity: "user-b", displayName: "Bruno Lima", hasVideo: false },
  ],
  controls: baseControls,
  onMinimize: vi.fn(),
  hasLocalVideo: false,
  localSeed: "user-1",
  localDisplayName: "Caio Almeida (você)",
};

describe("DedicatedCallStage", () => {
  it("renders the local participant's real name with (você)", () => {
    render(<DedicatedCallStage {...baseProps} />);
    expect(screen.getByText("Caio Almeida (você)")).toBeInTheDocument();
  });

  it("renders each participant's own resolved name, not a shared resource name", () => {
    render(<DedicatedCallStage {...baseProps} />);
    expect(screen.getByText("Ana Souza")).toBeInTheDocument();
    expect(screen.getByText("Bruno Lima")).toBeInTheDocument();
  });

  it("renders a participant's own avatar image when avatarUrl is set", () => {
    const { container } = render(<DedicatedCallStage {...baseProps} />);
    const tiles = container.querySelectorAll(".dedicated-call__tile");
    expect(tiles[1]!.querySelector("img")).toHaveAttribute("src", "https://x/a.png");
  });

  it("falls back to that participant's own deterministic initials when they have no avatar", () => {
    const { container } = render(<DedicatedCallStage {...baseProps} />);
    const tiles = container.querySelectorAll(".dedicated-call__tile");
    expect(tiles[2]!.querySelector("img")).not.toBeInTheDocument();
    expect(tiles[2]).toHaveTextContent("BL");
  });

  it("renders the local avatar image when localAvatarUrl is set", () => {
    const { container } = render(
      <DedicatedCallStage {...baseProps} localAvatarUrl="https://x/local.png" />,
    );
    const tiles = container.querySelectorAll(".dedicated-call__tile");
    expect(tiles[0]!.querySelector("img")).toHaveAttribute("src", "https://x/local.png");
  });

  it("falls back to deterministic initials, no broken image, when a participant avatar fails to load", () => {
    const { container } = render(<DedicatedCallStage {...baseProps} />);
    const tiles = container.querySelectorAll(".dedicated-call__tile");
    fireEvent.error(tiles[1]!.querySelector("img")!);
    expect(tiles[1]!.querySelector("img")).not.toBeInTheDocument();
    expect(tiles[1]).toHaveTextContent("AS");
  });

  it("camera-on: renders no avatar fallback for that tile", () => {
    const { container } = render(
      <DedicatedCallStage
        {...baseProps}
        hasLocalVideo
        participants={[
          {
            identity: "user-a",
            displayName: "Ana Souza",
            hasVideo: true,
            avatarUrl: "https://x/a.png",
          },
        ]}
      />,
    );
    const tiles = container.querySelectorAll(".dedicated-call__tile");
    expect(tiles[0]!.querySelector(".dedicated-call__avatar")).not.toBeInTheDocument();
    expect(tiles[1]!.querySelector(".dedicated-call__avatar")).not.toBeInTheDocument();
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd apps/web && npx vitest run src/calls/DedicatedCallStage.test.tsx`
Expected: FAIL — no `avatarUrl` prop/rendering exists yet, local tile still says "Você" literally.

- [ ] **Step 3: Implement**

Replace the whole file's identity-relevant pieces:

1. Remove the module-level `const localDisplayName = "Você";` (line 7).
2. Add the import: `import { PersonAvatarImage } from "../chat/PersonAvatarImage";`
3. Extend `DedicatedParticipant`:

```ts
interface DedicatedParticipant {
  identity: string;
  displayName: string;
  hasVideo: boolean;
  bindVideo?: RefCallback<HTMLDivElement>;
  /** This participant's own configured avatar, resolved server-side (issue #612) — never a resource-level avatar. */
  avatarUrl?: string;
}
```

4. Add two new destructured props (`localDisplayName`, `localAvatarUrl`) to the component's prop list and its inline type, next to `localSeed`:

```ts
  /**
   * The local participant's call-presentation name (issue #612) — the real
   * profile name plus "(você)", or a bare "Você" fallback. Computed by
   * DedicatedCallPage via localParticipantDisplayName.
   */
  localDisplayName: string;
  /** The local participant's configured avatar, when available. */
  localAvatarUrl?: string;
```

5. Replace the local tile's avatar block (lines 97-104):

```tsx
{
  !hasLocalVideo && (
    <div
      className={`dedicated-call__avatar call-avatar call-avatar--${avatarColorFor(localSeed)}`}
      aria-hidden="true"
    >
      <PersonAvatarImage
        src={localAvatarUrl}
        initials={initialsFrom(localDisplayName)}
        imgClassName="call-avatar__img"
      />
    </div>
  );
}
```

(`aria-hidden` stays correct here, unlike FloatingCallWindow's local tile: `<span>{localDisplayName}</span>` on the next line already names this tile.)

6. Replace the per-participant avatar block (lines 110-117):

```tsx
{
  !participant.hasVideo && (
    <div
      className={`dedicated-call__avatar call-avatar call-avatar--${avatarColorFor(participant.identity)}`}
      aria-hidden="true"
    >
      <PersonAvatarImage
        src={participant.avatarUrl}
        initials={initialsFrom(participant.displayName)}
        imgClassName="call-avatar__img"
      />
    </div>
  );
}
```

7. Update `<span>{localDisplayName}</span>` (line 105) — it already reads the prop name correctly once the destructure is renamed; no further change needed there since the local variable and the prop now share the name `localDisplayName`.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd apps/web && npx vitest run src/calls/DedicatedCallStage.test.tsx`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add apps/web/src/calls/DedicatedCallStage.tsx apps/web/src/calls/DedicatedCallStage.test.tsx
git commit -m "feat(calls): render real local and per-participant identity in DedicatedCallStage"
```

---

### Task 13: Frontend — `CallSessionProvider` wires self-profile + peer avatar into `FloatingCallWindow`

**Files:**

- Modify: `apps/web/src/calls/CallSessionProvider.tsx`
- Modify: `apps/web/src/calls/CallSessionProvider.test.tsx`

**Interfaces:**

- Consumes: `useSelfProfile` (`../profile/selfProfile`), `localParticipantDisplayName` (Task 7), `FloatingCallWindowProps.localName/localAvatarUrl/avatarUrl` (Task 11).

- [ ] **Step 1: Write the failing test**

Read `CallSessionProvider.test.tsx`'s existing mocking setup first (`grep -n "vi.mock\|useSelfProfile\|directory\b" apps/web/src/calls/CallSessionProvider.test.tsx`) to match its fixture style exactly, then add tests such as:

```tsx
// Added to CallSessionProvider.test.tsx, near the existing FloatingCallWindow-rendering tests.
it("shows the local participant's real profile name with (você) in the floating window", async () => {
  mockFetchMyProfile.mockResolvedValue({ id: "current-user", displayName: "Ana Souza" });
  // ...render the provider with an active call the same way the existing
  // "renders FloatingCallWindow" test does...
  expect(await screen.findByLabelText("Ana Souza (você)")).toBeInTheDocument();
});

it("passes the direct peer's avatarUrl through to FloatingCallWindow", async () => {
  // ...directory fixture with a dms[].counterpart.avatarUrl set, same as the
  // existing peer-name test...
  const img = await screen.findByRole(
    "img" /* or container.querySelector, matching Task 11's note */,
  );
  expect(img).toHaveAttribute("src", "https://x/peer.png");
});
```

(These are illustrative — write them against whatever render helper and directory fixture `CallSessionProvider.test.tsx` already has for its existing "renders remote peer name"-style assertions, following that file's exact patterns rather than inventing a new one. Also add the `vi.mock("../profile/profileApi", ...)` + `_resetSelfProfile()` scaffolding from `ChatSidebar.test.tsx` (Task discovery), since `CallSessionProvider.test.tsx` does not have it yet.)

- [ ] **Step 2: Run test to verify it fails**

Run: `cd apps/web && npx vitest run src/calls/CallSessionProvider.test.tsx`
Expected: FAIL — `FloatingCallWindow` receives no `localName`/`avatarUrl`, so the aria-label/img assertions don't find anything.

- [ ] **Step 3: Implement**

In `CallSessionProvider.tsx`:

1. Add the imports:

```ts
import { useSelfProfile } from "../profile/selfProfile";
import { localParticipantDisplayName } from "../chat/messageDisplay";
```

2. Inside the provider component function, add (near where `directory`/`identity` are already read — this is a hook call, so it must sit unconditionally at the top level of the component, not inside the JSX-building section around line 1474+):

```ts
const selfProfile = useSelfProfile();
const selfDisplayName = selfProfile.status === "ready" ? selfProfile.profile.displayName : "";
const selfAvatarUrl = selfProfile.status === "ready" ? selfProfile.profile.avatarUrl : undefined;
const localName = localParticipantDisplayName(selfDisplayName);
```

3. In the `FloatingCallWindow` JSX (around line 1574-1604), add the three new props:

```tsx
          avatarUrl={peer?.avatarUrl}
          localName={localName}
          localAvatarUrl={selfAvatarUrl}
```

(`avatarUrl` here is the direct-call peer's — `peer` is already resolved at line 1484 and is `undefined` for a resource call, which is exactly the "never a resource-level avatar as a person's identity" rule: `resourceTarget` calls simply pass `avatarUrl={undefined}`.)

- [ ] **Step 4: Run test to verify it passes**

Run: `cd apps/web && npx vitest run src/calls/CallSessionProvider.test.tsx`
Expected: PASS (including every pre-existing test in the file — this is a wiring change, not a rewrite)

- [ ] **Step 5: Commit**

```bash
git add apps/web/src/calls/CallSessionProvider.tsx apps/web/src/calls/CallSessionProvider.test.tsx
git commit -m "feat(calls): wire real local and peer identity into CallSessionProvider"
```

---

### Task 14: Frontend — `DedicatedCallPage` wires self-profile + batch resource-participant avatars

**Files:**

- Modify: `apps/web/src/calls/DedicatedCallPage.tsx`
- Modify: `apps/web/src/calls/DedicatedCallPage.test.tsx`

**Interfaces:**

- Consumes: `useSelfProfile` (`../profile/selfProfile`), `localParticipantDisplayName` (Task 7), `fetchChannelCallParticipantProfiles`/`fetchGroupCallParticipantProfiles`/`CallParticipantProfile` (Task 10), `DedicatedCallStage`'s `localDisplayName`/`localAvatarUrl`/`DedicatedParticipant.avatarUrl` (Task 12).
- Produces: nothing new externally — this is the final wiring point.

- [ ] **Step 1: Write the failing test**

Add to `DedicatedCallPage.test.tsx` (using the file's existing `vi.mock` scaffolding, extended with a mock for the new `chatApi` export and the `useSelfProfile`/`profileApi` pattern from `ChatSidebar.test.tsx`):

```tsx
// Extend the existing vi.mock("../chat/chatApi", ...) call to also export the new fetcher:
vi.mock("../chat/chatApi", () => ({
  fetchSidebarData: vi.fn(),
  fetchChannelCallParticipantProfiles: vi.fn(),
  fetchGroupCallParticipantProfiles: vi.fn(),
}));

// Add near the top, alongside the other imports:
import { fetchChannelCallParticipantProfiles } from "../chat/chatApi";

const { mockFetchMyProfile } = vi.hoisted(() => ({ mockFetchMyProfile: vi.fn() }));
vi.mock("../profile/profileApi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../profile/profileApi")>();
  return { ...actual, fetchMyProfile: (signal?: AbortSignal) => mockFetchMyProfile(signal) };
});
```

```tsx
it("shows the local participant's real profile name with (você)", async () => {
  mockFetchMyProfile.mockResolvedValue({ id: "current-user", displayName: "Ana Souza" });
  renderPage();
  await waitFor(() => screen.getByText("Ana Souza (você)"));
});

it("resolves each resource participant's own name and avatar in one batch request, not one per tile", async () => {
  session.media.participants = [
    { identity: "user-a", displayName: "Ana Souza", hasVideo: false },
    { identity: "user-b", displayName: "Bruno Lima", hasVideo: false },
  ];
  vi.mocked(fetchChannelCallParticipantProfiles).mockResolvedValue([
    { userId: "user-a", displayName: "Ana Souza", avatarUrl: "https://x/a.png" },
  ]);
  renderPage();
  await waitFor(() => expect(fetchChannelCallParticipantProfiles).toHaveBeenCalledTimes(1));
  expect(fetchChannelCallParticipantProfiles).toHaveBeenCalledWith(
    "channel-1",
    expect.arrayContaining(["user-a", "user-b"]),
    expect.anything(),
  );
});

it("two distinct participants never share one resolved identity", async () => {
  session.media.participants = [
    { identity: "user-a", displayName: "Ana Souza", hasVideo: false },
    { identity: "user-b", displayName: "Bruno Lima", hasVideo: false },
  ];
  vi.mocked(fetchChannelCallParticipantProfiles).mockResolvedValue([
    { userId: "user-a", displayName: "Ana Souza", avatarUrl: "https://x/a.png" },
    { userId: "user-b", displayName: "Bruno Lima", avatarUrl: "https://x/b.png" },
  ]);
  const { container } = renderPage();
  await waitFor(() => screen.getByText("Ana Souza"));
  const imgs = Array.from(container.querySelectorAll(".dedicated-call__tile img"));
  expect(imgs.map((img) => img.getAttribute("src"))).toEqual(
    expect.arrayContaining(["https://x/a.png", "https://x/b.png"]),
  );
});

it("degrades safely when a participant's identity cannot be resolved", async () => {
  session.media.participants = [
    { identity: "user-unknown", displayName: "Participante", hasVideo: false },
  ];
  vi.mocked(fetchChannelCallParticipantProfiles).mockResolvedValue([]);
  const { container } = renderPage();
  await waitFor(() => screen.getByText("Participante"));
  expect(container.querySelector(".dedicated-call__tile")!).not.toHaveTextContent(
    /[0-9a-f]{8}-[0-9a-f]{4}/, // never the raw UUID
  );
});
```

Add `beforeEach` reset lines mirroring `ChatSidebar.test.tsx`'s `_resetSelfProfile()`/`mockFetchMyProfile.mockResolvedValue(...)` defaults, and a default `vi.mocked(fetchChannelCallParticipantProfiles).mockResolvedValue([])` so pre-existing tests in the file that don't care about identity keep passing unchanged.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd apps/web && npx vitest run src/calls/DedicatedCallPage.test.tsx`
Expected: FAIL — `DedicatedCallStage` never receives `localDisplayName`/participant `avatarUrl`, no batch fetch happens.

- [ ] **Step 3: Implement**

In `DedicatedCallPage.tsx`:

1. Add imports:

```ts
import { useState as useReactState } from "react"; // already imports useState; just add to the existing import line instead
import {
  fetchChannelCallParticipantProfiles,
  fetchGroupCallParticipantProfiles,
  type CallParticipantProfile,
} from "../chat/chatApi";
import { useSelfProfile } from "../profile/selfProfile";
import { localParticipantDisplayName } from "../chat/messageDisplay";
```

(Merge `useState`/`useEffect`/`useMemo`/`useRef` into the existing `import { useEffect, useMemo, useRef, useState } from "react";` line at the top — `useState` is not currently imported there, so add it.)

2. Add state and a self-profile read near the top of the component, alongside the existing `resolved`/`directory`/`error` state:

```ts
const selfProfile = useSelfProfile();
const selfDisplayName = selfProfile.status === "ready" ? selfProfile.profile.displayName : "";
const selfAvatarUrl = selfProfile.status === "ready" ? selfProfile.profile.avatarUrl : undefined;
const localDisplayName = localParticipantDisplayName(selfDisplayName);
const [participantProfiles, setParticipantProfiles] = useState<Map<string, CallParticipantProfile>>(
  new Map(),
);
```

3. Add a batch-fetch effect, after the existing `target` memo (after line 69) and before the activation effect:

```ts
// Batch-resolves every currently-known resource participant's identity in
// one request per change to the roster (issue #612) — never one request
// per tile. Direct calls have no resourceTarget and skip this entirely,
// since the counterpart contract already carries their identity.
const participantIdentities = media.participants
  .map((participant) => participant.identity)
  .join(",");
useEffect(() => {
  if (!resolved || (resolved.target_type !== "channel" && resolved.target_type !== "dm")) return;
  const ids = participantIdentities ? participantIdentities.split(",") : [];
  if (ids.length === 0) return;
  let active = true;
  const request =
    resolved.target_type === "channel"
      ? fetchChannelCallParticipantProfiles(resolved.target_id!, ids)
      : fetchGroupCallParticipantProfiles(resolved.target_id!, ids);
  request.then(
    (profiles) => {
      if (!active) return;
      setParticipantProfiles(new Map(profiles.map((profile) => [profile.userId, profile])));
    },
    () => undefined, // A failed lookup degrades to initials per-tile; it is not a page-level error.
  );
  return () => {
    active = false;
  };
}, [resolved, participantIdentities]);
```

4. Build the enriched participants array passed to `DedicatedCallStage`, replacing the plain `participants={media.participants}` prop (around line 196):

```tsx
        participants={media.participants.map((participant) => ({
          ...participant,
          avatarUrl: participantProfiles.get(participant.identity)?.avatarUrl,
        }))}
```

5. Add `localDisplayName={localDisplayName}` and `localAvatarUrl={selfAvatarUrl}` to the `DedicatedCallStage` props (near `localSeed`, around line 211).

- [ ] **Step 4: Run test to verify it passes**

Run: `cd apps/web && npx vitest run src/calls/DedicatedCallPage.test.tsx`
Expected: PASS (including every pre-existing test)

- [ ] **Step 5: Commit**

```bash
git add apps/web/src/calls/DedicatedCallPage.tsx apps/web/src/calls/DedicatedCallPage.test.tsx
git commit -m "feat(calls): resolve resource-call participant identities in DedicatedCallPage"
```

---

### Task 15: Full verification pass

**Files:** none (verification only).

- [ ] **Step 1: Backend — full chat-service suite**

Run (from `services/chat-service`): `go build ./... && go test ./... -count=1`
Expected: all pass.

- [ ] **Step 2: Frontend — focused calls/chat suite**

Run (from `apps/web`): `npx vitest run src/calls src/chat/PersonAvatarImage.test.tsx src/chat/chatApi.test.ts src/chat/messageDisplay.test.ts`
Expected: all pass.

- [ ] **Step 3: Frontend — full suite, typecheck, lint, format**

Run (from `apps/web`, or repo root per this project's actual script locations — confirm with `cat package.json` / the workspace root `package.json` if these differ):

```bash
npx vitest run
pnpm typecheck:web
pnpm lint
pnpm format:check
```

Expected: all pass. If `format:check` fails only on newly-added files, run the project's format-write script and re-diff — do not hand-fix formatting.

- [ ] **Step 4: `git diff --check` and status**

Run (from repo root): `git diff --check && git status --short`
Expected: no whitespace errors; status shows only the files this plan touched, nothing staged/committed beyond the task commits above (no commit needed for this step itself).

- [ ] **Step 5: Manual QA checklist (record as remaining items, not automated)**

- Start a direct call, confirm the other person's real name + avatar (or initials) show, and your own tile shows `<name> (você)`.
- Toggle camera on/off locally and confirm avatar fallback appears/disappears correctly.
- Break an avatar URL (e.g. via devtools network block) and confirm initials show with no broken-image icon, and that fixing the URL and re-rendering recovers the image.
- Join a channel/group call with 2+ other participants and confirm each tile shows that person's own name/avatar, not the channel/group's.
- Confirm no repeated per-tile network requests in devtools — one batch request per resource call identity resolution.
- Confirm the incoming-call popup still looks/behaves as before aside from the initials/broken-image fix.
