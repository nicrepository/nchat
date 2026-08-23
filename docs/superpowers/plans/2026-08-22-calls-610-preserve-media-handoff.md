# Calls #610 — Preserve Media Control State Across Dedicated Handoff

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
>
> Executed inline in the same session that wrote this plan (full codebase context already loaded via 6 parallel research passes) rather than dispatched to fresh subagents, to avoid re-deriving ~450KB of already-gathered file/line context. Comprehensive tests are authored alongside each file's production code and run together, rather than one-scenario-at-a-time red/green, given the design below was fully specified and approved before this plan was written.

**Goal:** Preserve user mic/camera intent across main→dedicated→main call handoffs (direct and resource calls) without auto-enabling devices that were off, creating a second media owner, or deriving intent from generic SDK state.

**Architecture:** A new per-writer `media-intent` key space in `callOwnership.ts` (structurally identical to the existing `participation.v2` per-writer pattern) stores `{ownershipEpoch, revision, microphone, camera}`, ordered by a comparator shared with `resolveLeaseConflict`. `ownedMedia.connect` in `CallSessionProvider.tsx` is the sole choke point: it classifies the connection attempt as `fresh` or `recovery` (a mode threaded down causally from `useCallSignaling`/`useResourceCallSession`, never inferred from `call_type`/`callId`), reads the stored intent only on `recovery`, and writes the applied intent back durably before declaring the connection successful. Toggle wrappers in `CallSessionProvider` persist confirmed user intent the same way, fenced by the causal ownership lease captured before the toggle started.

**Tech Stack:** TypeScript, React, Vitest, existing `OwnershipCoordinator`/`BroadcastChannel`/`localStorage` primitives, LiveKit-backed `useCallMedia`.

**Spec:** Full spec is the issue body pasted into this session (27 numbered sections, direct/resource, ownership epoch/tabId ordering, fail-closed storage semantics). Not duplicated verbatim here — this plan is the engineering translation of it. Section numbers below (`§N`) refer to that spec.

> **SUPERSEDED — historical record only, not the final design.** This document captures the Revision 1 plan as originally written. Adversarial review across Revisions 2–5 (now merged to `develop` as commit `c29d436`) changed the media-intent design substantially; the API shapes and code blocks below (`writeMediaIntent(...): boolean`, `readMediaIntent(callId): MediaIntent | null` as a global cross-writer scan) no longer match the shipped code. Kept here as an implementation-history record; formatting may be normalized by Prettier, but the superseded task semantics are intentionally preserved. Do **not** use Tasks 4, 8, 9, or 10 below as a reference for the current API. The actual final design is, in `apps/web/src/calls/callOwnership.ts`'s own doc comments and `resolvePredecessor`/`readMediaIntentForLease`:
>
> - **Write-ahead pending/confirmed protocol** (Revision 3): a user toggle writes a durable `phase: "pending"` entry _before_ touching the SDK; only after the SDK confirms the change does a `phase: "confirmed"` write settle it, fenced by `expectedRevision` so a stale operation can never overwrite a newer one's entry. `writeMediaIntent(callId, lease, intent, phase, options?)` returns `{ok:true,revision} | {ok:false,reason:"stale"|"storage-error"}`, never a bare boolean.
> - **Predecessor provenance is proven, never scanned** (Revisions 4–5): `readMediaIntentForLease(callId)` (no `currentLease` parameter — it reads this coordinator's own currently-held lease) resolves to (1) this writer's own entry at the current epoch, or (2) the specific writer/epoch `resolvePredecessor` identified as the causal predecessor at claim time — never a global "highest epoch below mine" scan across all writers.
> - **`resolvePredecessor(existingForCall, predecessorHint, afterEpoch)`** is the only source of predecessor identity: a `predecessorHint` explicitly passed to `claim()` (validated against `afterEpoch`, never allowed to regress behind a newer `existingForCall`), or the `OwnerLease` this coordinator directly observed in storage at claim time (even expired — the crash case).
> - **No last-release fallback exists.** An earlier revision's `lastReleasedByCallId` map (this tab's own memory of "the epoch I last released for this callId") was removed after a formal audit: an empty `LEASE_KEY` never proves no intermediate owner claimed and released it in between.
> - **Unknown predecessor always degrades to `{microphone:false,camera:false}`** — never approximated from any historical/global-max source.

## Global Constraints

- No backend/migrations/media-service changes (§ intro).
- Do not touch #622 participation_id/P1-P2-P3/compensation/heartbeat/leave-fencing semantics in `useResourceCallSession.ts` (§14, §22).
- No new npm dependency; reuse existing `localStorage`/`BroadcastChannel`/Web Locks primitives already in `callOwnership.ts`.
- No `useEffect`-based intent persistence (§9) — persistence happens synchronously in the same async flow that confirmed the operation.
- No screen-share persistence, no #611–#616 scope (§24).
- Do not commit/push/rebase/reset — the user will review the diff.

---

## Task 1 — `callOwnership.ts`: shared ownership-claim comparator

**Files:**

- Modify: `apps/web/src/calls/callOwnership.ts`
- Test: `apps/web/src/calls/callOwnership.test.ts`

**Interfaces:**

- Produces: `export interface OwnershipClaim { epoch: number; tabId: string }` and `export function compareOwnershipClaims(a: OwnershipClaim, b: OwnershipClaim): number` — positive means `a` wins/is later, matching the existing `compareParticipationTokens` sign convention (§2).

- [ ] Add `OwnershipClaim`/`compareOwnershipClaims` above `resolveLeaseConflict`:

```ts
export interface OwnershipClaim {
  epoch: number;
  tabId: string;
}

/**
 * Total order over ownership claims: (epoch, tabId). Higher epoch always
 * wins. On a tie, the SAME tabId-wins rule as the pre-existing
 * resolveLeaseConflict (smaller tabId string wins) — extracted here so the
 * two can never diverge on which claim is "later". Returns >0 when `a`
 * wins/is later, <0 when `b` wins, 0 when identical.
 */
export function compareOwnershipClaims(a: OwnershipClaim, b: OwnershipClaim): number {
  if (a.epoch !== b.epoch) return a.epoch - b.epoch;
  if (a.tabId === b.tabId) return 0;
  return a.tabId.localeCompare(b.tabId) <= 0 ? 1 : -1;
}
```

- [ ] Rewrite `resolveLeaseConflict` in terms of it (behavior-preserving):

```ts
export function resolveLeaseConflict(a: OwnerLease, b: OwnerLease): OwnerLease {
  return compareOwnershipClaims(a, b) >= 0 ? a : b;
}
```

- [ ] Add `describe("compareOwnershipClaims", ...)` tests matching the existing `compareParticipationTokens` style (order by epoch regardless of tabId; tie broken by tabId, symmetric/antisymmetric; reflexive zero case) plus a direct regression: `compareOwnershipClaims({epoch:10,tabId:"a"}, {epoch:10,tabId:"z"})` winner keeps winning regardless of any `revision`-shaped field (there is none on `OwnershipClaim` — this proves the type itself excludes revision from the primitive).
- [ ] Confirm existing `resolveLeaseConflict` tests (§ existing file, lines ~97-117) still pass unchanged (behavior-preserving refactor).
- [ ] Run `pnpm --filter @nchat/web exec vitest run src/calls/callOwnership.test.ts`.

---

## Task 2 — `callOwnership.ts`: media-intent storage (shape, parser, key space)

**Files:**

- Modify: `apps/web/src/calls/callOwnership.ts`
- Test: `apps/web/src/calls/callOwnership.test.ts`

**Interfaces:**

- Produces: `export interface MediaIntent { microphone: boolean; camera: boolean }`, `export type MediaConnectionMode = "fresh" | "recovery"`, internal `MediaIntentEntry` shape + parser, `MEDIA_INTENT_KEY_PREFIX`, `mediaIntentKeyPrefix(callId)`/`mediaIntentKey(callId, writerId)`.

- [ ] Add constants/types near `PARTICIPATION_KEY_PREFIX`:

```ts
// Media-intent storage (issue #610) — a SEPARATE key space from both
// LEASE_KEY and PARTICIPATION_KEY_PREFIX. Structurally identical to the
// participation per-writer pattern for the same reason: one exclusive key
// per (callId, writerId) so no writer ever needs read-modify-write on
// another writer's contribution. Survives release() removing OwnerLease —
// that is the entire point: mic/camera intent must outlive the ownership
// lease that was active when it was recorded.
const MEDIA_INTENT_KEY_PREFIX = "nchat.call.media-intent.v1.";

export interface MediaIntent {
  microphone: boolean;
  camera: boolean;
}

/** Belongs to the connection ATTEMPT, never inferred from call_type/callId. */
export type MediaConnectionMode = "fresh" | "recovery";

interface MediaIntentEntry extends MediaIntent {
  v: 1;
  ownershipEpoch: number;
  revision: number;
}

export interface MediaIntentRecord extends MediaIntentEntry {
  writerId: string;
}

function mediaIntentKeyPrefix(callId: string): string {
  return `${MEDIA_INTENT_KEY_PREFIX}${encodeURIComponent(callId)}:`;
}

function mediaIntentKey(callId: string, writerId: string): string {
  return `${mediaIntentKeyPrefix(callId)}${writerId}`;
}

function parseMediaIntentEntry(value: string | null): MediaIntentEntry | null {
  if (!value) return null;
  try {
    const record = JSON.parse(value) as Record<string, unknown>;
    if (
      !record ||
      typeof record !== "object" ||
      Array.isArray(record) ||
      Object.keys(record).length !== 5 ||
      record.v !== 1 ||
      !isEpoch(record.ownershipEpoch) ||
      !isEpoch(record.revision) ||
      record.revision < 1 ||
      typeof record.microphone !== "boolean" ||
      typeof record.camera !== "boolean"
    ) {
      return null;
    }
    return record as unknown as MediaIntentEntry;
  } catch {
    return null;
  }
}
```

- [ ] Add strict-parser unit tests: valid entry round-trips; wrong `v`; extra/missing keys; non-boolean `microphone`/`camera`; negative/non-integer `ownershipEpoch`/`revision`; `revision: 0` rejected; malformed JSON; `null` input.
- [ ] Run `pnpm --filter @nchat/web exec vitest run src/calls/callOwnership.test.ts`.

---

## Task 3 — `callOwnership.ts`: claim epoch floor over media-intent

**Files:**

- Modify: `apps/web/src/calls/callOwnership.ts`
- Test: `apps/web/src/calls/callOwnership.test.ts`

**Interfaces:**

- Consumes: `mediaIntentKeyPrefix`, `parseMediaIntentEntry` (Task 2).
- Produces: internal `maxMediaIntentEpoch(callId): number` used inside `runClaim`.

- [ ] Add a reader for the max persisted `ownershipEpoch` across all media-intent writers for a callId, next to `readParticipationTokens`:

```ts
const maxMediaIntentEpoch = (callId: string): number => {
  const prefix = mediaIntentKeyPrefix(callId);
  let max = 0;
  for (let index = 0; index < storage.length; index += 1) {
    const key = storage.key(index);
    if (!key || !key.startsWith(prefix)) continue;
    const writerId = key.slice(prefix.length);
    if (!isBoundedString(writerId)) continue;
    const entry = parseMediaIntentEntry(storage.getItem(key));
    if (!entry) continue;
    if (entry.ownershipEpoch > max) max = entry.ownershipEpoch;
  }
  return max;
};
```

- [ ] In `runClaim`, change the epoch computation to include this floor and fail closed on overflow:

```ts
const floor = Math.max(existingForCall?.epoch ?? 0, afterEpoch ?? 0, maxMediaIntentEpoch(callId));
if (floor >= Number.MAX_SAFE_INTEGER) return null;
const candidate: OwnerLease = {
  v: 1,
  callId,
  tabId,
  epoch: floor + 1,
  role,
  expiresAt: now() + leaseMs,
};
```

(Replaces the existing `Math.max(existingForCall?.epoch ?? 0, afterEpoch ?? 0) + 1` line.)

- [ ] Tests: claim after `release()` (no OwnerLease left) still picks an epoch strictly greater than a previously-written media-intent `ownershipEpoch`; claim with `afterEpoch` below the media-intent floor still floors correctly; two concurrent claims computing the same floor converge via the existing tabId tie-break (no new mechanism); claim returns `null` (fails closed) when the floor is `Number.MAX_SAFE_INTEGER` or above; existing epoch/afterEpoch claim tests still pass unchanged.
- [ ] Run `pnpm --filter @nchat/web exec vitest run src/calls/callOwnership.test.ts`.

---

## Task 4 — `callOwnership.ts`: `writeMediaIntent` / `readMediaIntent` on the coordinator

**Files:**

- Modify: `apps/web/src/calls/callOwnership.ts`
- Test: `apps/web/src/calls/callOwnership.test.ts`

**Interfaces:**

- Consumes: `OwnerLease`, `compareOwnershipClaims`, `MediaIntent`, `MediaIntentEntry`, `parseMediaIntentEntry`, `mediaIntentKey`/`mediaIntentKeyPrefix` (Tasks 1-2).
- Produces on `OwnershipCoordinator`:

```ts
writeMediaIntent(callId: string, lease: OwnerLease, intent: MediaIntent): boolean;
readMediaIntent(callId: string): MediaIntent | null;
```

(`writeMediaIntent` returns whether the write was accepted; `readMediaIntent` returns only `{microphone, camera}` per §5 — callers needing causal metadata read the coordinator's storage directly in tests only.)

- [ ] Add to `OwnershipCoordinatorOptions`... no new options needed — reuse `storage`, `tabId`, `closed`, `lease` (the coordinator's own current-lease closure variable) already in scope inside `createOwnershipCoordinator`.
- [ ] Add `runWriteMediaIntent` inside `createOwnershipCoordinator` (near `runAllocateParticipationGeneration`):

```ts
const runWriteMediaIntent = (
  callId: string,
  captured: OwnerLease,
  intent: MediaIntent,
): boolean => {
  if (closed || !isBoundedString(callId)) return false;
  if (captured.tabId !== tabId) return false;
  if (
    !lease ||
    lease.callId !== callId ||
    lease.tabId !== tabId ||
    lease.epoch !== captured.epoch
  ) {
    return false;
  }
  try {
    const key = mediaIntentKey(callId, tabId);
    const previous = parseMediaIntentEntry(storage.getItem(key));
    if (previous && captured.epoch < previous.ownershipEpoch) return false;
    const revision =
      previous && previous.ownershipEpoch === captured.epoch ? previous.revision + 1 : 1;
    const entry: MediaIntentEntry = {
      v: 1,
      ownershipEpoch: captured.epoch,
      revision,
      microphone: intent.microphone,
      camera: intent.camera,
    };
    storage.setItem(key, JSON.stringify(entry));
    return true;
  } catch {
    return false;
  }
};
```

Note the read-then-write for `previous` happens synchronously with no `await` between them — `localStorage.setItem` is synchronous, so no interleaving is possible within this call. A same-writer delayed write from an earlier, now-superseded epoch is rejected by `captured.epoch < previous.ownershipEpoch`; a same-writer delayed write at the SAME epoch as a newer confirmed toggle is a caller-level (React operation-currency) concern handled in Task 8, not here — document this explicitly in the comment above the function.

- [ ] Add `runReadMediaIntent`:

```ts
const runReadMediaIntent = (callId: string): MediaIntent | null => {
  if (!isBoundedString(callId)) return null;
  try {
    const prefix = mediaIntentKeyPrefix(callId);
    let winner: (MediaIntentEntry & { writerId: string }) | null = null;
    for (let index = 0; index < storage.length; index += 1) {
      const key = storage.key(index);
      if (!key || !key.startsWith(prefix)) continue;
      const writerId = key.slice(prefix.length);
      if (!isBoundedString(writerId)) continue;
      const entry = parseMediaIntentEntry(storage.getItem(key));
      if (!entry) continue;
      const candidate = { ...entry, writerId };
      if (
        !winner ||
        compareOwnershipClaims(
          { epoch: candidate.ownershipEpoch, tabId: candidate.writerId },
          { epoch: winner.ownershipEpoch, tabId: winner.writerId },
        ) > 0
      ) {
        winner = candidate;
      }
    }
    return winner ? { microphone: winner.microphone, camera: winner.camera } : null;
  } catch {
    return null;
  }
};
```

- [ ] Wire both into the returned coordinator object and the `OwnershipCoordinator` interface (with doc comments mirroring the participation-token ones — never eviction, one key per (callId, writerId) forever, same residual-growth note).
- [ ] Tests (in `callOwnership.test.ts`, new `describe("media intent", ...)` block, using the existing `SharedStorage`/`TestChannel` fakes and options-spread pattern):
  - write then read round-trips `{microphone,camera}` for all 4 boolean combinations (§17).
  - `writeMediaIntent` rejected when `captured.tabId !== coordinator.tabId`.
  - rejected when coordinator has no current lease, or current lease's `callId`/`tabId`/`epoch` doesn't match `captured`.
  - rejected when `captured.epoch < previous.ownershipEpoch` (stale epoch, same writer) — the "same writer E1 → E3 → delayed E1 write rejected" case from §23: claim epoch 1, write; claim epoch 3 (simulating release+reclaim), write; then attempt a write captured at epoch 1 again → rejected, storage still reflects epoch 3's value.
  - same epoch, sequential writes: revision increments 1 → 2 → 3 monotonically for the same writer/epoch.
  - epoch tie, winner determined by tabId only, `revision` never decides the cross-writer winner: write `{epoch:10, tabId:"loser", revision effectively 50 after many writes}` and `{epoch:10, tabId:"winner"}` where `"winner" < "loser"` lexicographically — `readMediaIntent` returns `"winner"`'s value regardless of revision count (§2's explicit test).
  - corrupt/missing entries are ignored by `readMediaIntent` (mixed valid+corrupt keys under the same callId prefix) — returns the best valid entry, not `null`, unless ALL are corrupt/missing, in which case `null`.
  - `readMediaIntent` never reads across `callId`s (percent-encoding boundary test mirroring the existing `participationKeyPrefix` comment, e.g. a callId containing `:`-like encoded text doesn't collide).
- [ ] Run `pnpm --filter @nchat/web exec vitest run src/calls/callOwnership.test.ts`.

---

## Task 5 — `useCallMedia.ts`: `MediaIntent`-aware `connect`, boolean-returning toggles

**Files:**

- Modify: `apps/web/src/chat/useCallMedia.ts`
- Test: `apps/web/src/chat/useCallMedia.test.tsx`

**Interfaces:**

- Consumes: `MediaIntent` from `../calls/callOwnership` (Task 2).
- Produces: `connect(call, token, serverUrl, initialIntent?: MediaIntent): Promise<MediaIntent | undefined>`; `toggleMicrophone(): Promise<boolean | undefined>`; `toggleCamera(): Promise<boolean | undefined>`. `CallMediaController`/`CallMediaSessionController` interfaces updated to match.

- [ ] Import `MediaIntent` from `../calls/callOwnership`.
- [ ] Change `connect`'s signature (interface at lines 78-83 and the `useCallback` at 730-735) to accept a 4th optional `initialIntent?: MediaIntent` and return `Promise<MediaIntent | undefined>`.
- [ ] Replace the device-enable block (lines 753-761) with:

```ts
await session.connect(serverUrl, token);
if (sessionRef.current !== session || generationRef.current !== generation) return undefined;
if (initialIntent) {
  // Recovery: apply the restored snapshot EXACTLY — never enable then
  // disable, never getUserMedia for a device whose restored intent is
  // false. Both setters take the target boolean directly.
  await session.setCameraEnabled(initialIntent.camera);
  if (sessionRef.current !== session || generationRef.current !== generation) return undefined;
  update({ cameraEnabled: initialIntent.camera });
  await session.setMicrophoneEnabled(initialIntent.microphone);
  if (sessionRef.current !== session || generationRef.current !== generation) return undefined;
  connectedCallIdRef.current = call.call_id;
  return { microphone: initialIntent.microphone, camera: initialIntent.camera };
}
// Fresh: existing entry defaults, unchanged.
let cameraEnabled = false;
if (call.call_type === "video") {
  await session.enableCamera();
  if (sessionRef.current !== session || generationRef.current !== generation) return undefined;
  update({ cameraEnabled: true });
  cameraEnabled = true;
}
await session.enableMicrophone();
if (sessionRef.current !== session || generationRef.current !== generation) return undefined;
connectedCallIdRef.current = call.call_id;
// microphoneEnabled itself was already confirmed by
// onMicrophoneStateChanged as part of enableMicrophone() above.
return { microphone: true, camera: cameraEnabled };
```

- [ ] `toggleMicrophone` (lines 808-835): change to return `Promise<boolean | undefined>`, resolving the actual applied value only when the operation is still the current one at completion, `undefined` otherwise:

```ts
const toggleMicrophone = useCallback(async (): Promise<boolean | undefined> => {
  const session = sessionRef.current;
  if (!session || pendingControlRef.current) return undefined;
  const operation: PendingMediaOperation = {
    control: "microphone",
    session,
    generation: generationRef.current,
  };
  pendingControlRef.current = operation;
  update({ pendingControl: "microphone", error: null });
  const next = !microphoneEnabledRef.current;
  try {
    await session.setMicrophoneEnabled(next);
    if (pendingControlRef.current !== operation) return undefined;
    return microphoneEnabledRef.current;
  } catch (error) {
    if (pendingControlRef.current === operation) {
      update({ error: mediaErrorMessage(error, "microphone") });
    }
    return undefined;
  } finally {
    if (pendingControlRef.current === operation) {
      pendingControlRef.current = null;
      update({ pendingControl: null });
    }
  }
}, [update]);
```

- [ ] `toggleCamera` (lines 837-861): same shape, still setting `cameraEnabled` itself (no SDK confirmation callback exists for camera):

```ts
const toggleCamera = useCallback(async (): Promise<boolean | undefined> => {
  const session = sessionRef.current;
  if (!session || pendingControlRef.current) return undefined;
  const operation: PendingMediaOperation = {
    control: "camera",
    session,
    generation: generationRef.current,
  };
  pendingControlRef.current = operation;
  update({ pendingControl: "camera", error: null });
  const enabled = !state.cameraEnabled;
  try {
    await session.setCameraEnabled(enabled);
    if (pendingControlRef.current !== operation) return undefined;
    update({ cameraEnabled: enabled });
    return enabled;
  } catch (error) {
    if (pendingControlRef.current === operation) {
      update({ error: mediaErrorMessage(error, "camera") });
    }
    return undefined;
  } finally {
    if (pendingControlRef.current === operation) {
      pendingControlRef.current = null;
      update({ pendingControl: null });
    }
  }
}, [state.cameraEnabled, update]);
```

- [ ] Update `CallMediaController`/`CallMediaSessionController` interfaces (lines ~47-84) with the new return types.
- [ ] Update `FakeSession` mock in the test file: `setCameraEnabled` needs to remain a plain resolved promise (no confirmation callback — matches production, since camera has none); add a case where `setCameraEnabled`/`setMicrophoneEnabled` are asserted to be called with the exact target boolean (never a bare `enableCamera()`/`enableMicrophone()` call) when `initialIntent` is supplied.
- [ ] New tests in `useCallMedia.test.tsx`:
  - `connect(call, token, url, {microphone:false, camera:false})` calls neither `enableCamera` nor `enableMicrophone`; calls `setCameraEnabled(false)` and `setMicrophoneEnabled(false)`; resulting `cameraEnabled`/`microphoneEnabled` state is `false`/`false`; resolves `{microphone:false, camera:false}`.
  - Same for the other 3 boolean combinations of `initialIntent`.
  - `connect` without `initialIntent` preserves exact current fresh-default behavior for video/audio call types (existing tests must still pass unchanged) and now resolves `{microphone:true, camera:true}` / `{microphone:true, camera:false}` respectively.
  - `connect` resolves `undefined` when `stop()` invalidates the session before the connect promise settles (extend the existing generation-guard test pattern).
  - `toggleMicrophone`/`toggleCamera` resolve the applied boolean on a normal successful toggle.
  - `toggleMicrophone`/`toggleCamera` resolve `undefined` on: SDK rejection, a stale/superseded operation (reuse the existing A/B session-race harness), and `stop()` invalidating generation mid-toggle.
- [ ] Run `pnpm --filter @nchat/web exec vitest run src/chat/useCallMedia.test.tsx`.

---

## Task 6 — `useCallSignaling.ts`: causal fresh/recovery mode classification

**Files:**

- Modify: `apps/web/src/chat/useCallSignaling.ts`
- Test: `apps/web/src/chat/useCallSignaling.test.ts`

**Interfaces:**

- Consumes: `MediaConnectionMode` from `../calls/callOwnership` (Task 2).
- Produces: `CallMediaBridge.connect` gains a required `mode: MediaConnectionMode` 4th parameter; `requestMedia` threads it through; new internal refs `attemptModeRef`, `everConnectedRef`.

- [ ] Change `CallMediaBridge` (line 19) from a `Pick` to an explicit interface so `connect`'s signature can diverge from `CallMediaSessionController`'s (which keeps its own `initialIntent`-based signature from Task 5 — the bridge and the raw hook are different layers per the architecture note):

```ts
export type CallMediaBridge = {
  startAudio: CallMediaSessionController["startAudio"];
  connect: (
    call: { call_id: string; call_type: CallType },
    token: string,
    serverUrl: string,
    mode: MediaConnectionMode,
  ) => Promise<void>;
  stop: CallMediaSessionController["stop"];
};
```

- [ ] Add refs near `localAuthorizationRef` (line 119):

```ts
// Causal fresh/recovery classification (issue #610) for the media
// connection ATTEMPT — never inferred from call_type or generic SDK
// state. Keyed by call_id so a new call never inherits a stale mode.
const attemptModeRef = useRef<{ callId: string; mode: MediaConnectionMode } | null>(null);
// Survives invalidateMediaRequest()/stopMedia() — unlike every other
// per-call media ref in this file — because "did this call EVER connect
// successfully before" must outlive a single connect/disconnect cycle for
// retryMedia's Caso A/B/C classification (§13) to work.
const everConnectedRef = useRef<Set<string>>(new Set());
```

- [ ] At the two places `localAuthorizationRef` is granted for a fresh, non-reconciled call.start/call.accept completion (inside the `onMessage` `eventCompletesPending` branch, current lines ~304-312), also set:

```ts
attemptModeRef.current = { callId: event.call.call_id, mode: "fresh" };
```

- [ ] In `activateMedia` (line 632, right before `await requestMedia(call)`), set:

```ts
attemptModeRef.current = { callId: call.call_id, mode: "recovery" };
```

- [ ] In `retryMedia` (before line 710's `localAuthorizationRef.current = ...`), compute the mode per §13's three cases instead of leaving it implicit:

```ts
const previousMode =
  attemptModeRef.current?.callId === call.call_id ? attemptModeRef.current.mode : "fresh";
const mode: MediaConnectionMode = everConnectedRef.current.has(call.call_id)
  ? "recovery"
  : previousMode;
attemptModeRef.current = { callId: call.call_id, mode };
```

(Caso A: never connected + previous mode fresh → stays fresh. Caso B: `everConnectedRef.has(call.call_id)` → recovery. Caso C: never connected + previous mode recovery → stays recovery.)

- [ ] Change `requestMedia` (lines 131-186) to read the mode from `attemptModeRef` (falling back to `"fresh"` if absent — the only path that can hit `requestMedia` without ever setting `attemptModeRef` is the very first call.start/accept authorization, which is itself fresh) and pass it through, and mark `everConnectedRef` on success:

```ts
const mode: MediaConnectionMode =
  attemptModeRef.current?.callId === call.call_id ? attemptModeRef.current.mode : "fresh";
await mediaRef.current?.connect(call, result.token, result.serverUrl, mode);
if (!current()) return;
everConnectedRef.current.add(call.call_id);
```

(Replaces the old 3-arg `connect` call at line 163; the `everConnectedRef.current.add(...)` line is new, placed right after the existing `if (!current()) return;` guard that already follows the connect await.)

- [ ] Ensure `invalidateMediaRequest`/call-end paths clear `attemptModeRef` when the call itself ends (not merely on stop-for-retry) — add `attemptModeRef.current = null` alongside wherever `mediaCallIdRef.current = ""` is cleared for a terminal call end (not for a retry/activate cycle, which must keep `everConnectedRef`'s history). Do NOT clear `everConnectedRef`'s entry for the ended call_id — a new call gets a new call_id and Set membership is naturally scoped.
- [ ] Update `mediaBridge()` test mock (line ~164-170) to accept the 4th `mode` parameter: `connect: vi.fn(async (_call, _token, _url, _mode) => undefined)`.
- [ ] New tests in `useCallSignaling.test.ts`:
  - `call.start` completing → `media.connect` called with `mode: "fresh"`.
  - `call.accept` completing → `mode: "fresh"`.
  - `activateMedia()` on a restored/reconciled call → `mode: "recovery"` (extend the existing "requires an explicit activation" test to also assert the mode argument).
  - `retryMedia()` Caso A: connect a fresh call, but have `media.connect` reject before ever resolving; `retryMedia()` → still `mode: "fresh"`.
  - `retryMedia()` Caso B: successful fresh connect first (`everConnectedRef` now has the call), then `retryMedia()` → `mode: "recovery"`.
  - `retryMedia()` Caso C: `activateMedia()` fails before connecting (recovery attempt, never succeeded), then `retryMedia()` → still `mode: "recovery"`.
  - `call.sync`/restored active call never auto-calls `connect` (existing behavior, assert unchanged).
- [ ] Run `pnpm --filter @nchat/web exec vitest run src/chat/useCallSignaling.test.ts`.

---

## Task 7 — `useResourceCallSession.ts`: explicit fresh/recovery mode for join/reconnect

**Files:**

- Modify: `apps/web/src/chat/useResourceCallSession.ts`
- Test: `apps/web/src/chat/useResourceCallSession.test.ts`

**Interfaces:**

- Consumes: `MediaConnectionMode` from `../calls/callOwnership`; the same `CallMediaBridge` type from Task 6 (import from `./useCallSignaling` as it already does, or re-export — confirm existing import path for `CallMediaBridge` in this file and update call sites there).
- Produces: `join(target: ResourceCallTarget, mode: MediaConnectionMode, onCallIdResolved?: (callId: string) => void): Promise<string | undefined>` (mode inserted as 2nd positional param); `reconnect()` unchanged signature, internally always classifies as `"recovery"`.

- [ ] Add `mode: MediaConnectionMode` as the 2nd parameter of `join` (line 261 area), threading it into both call sites of `mediaRef.current.connect(...)` (site A, lines 367-371):

```ts
await mediaRef.current.connect(
  { call_id: call.call_id, call_type: "audio" },
  result.token,
  result.serverUrl,
  mode,
);
```

- [ ] `reconnect()` (site B, lines 531-535): pass the literal `"recovery"` — never a parameter, per §14 ("resource.reconnect() => recovery" is unconditional):

```ts
await mediaRef.current.connect(
  { call_id: call.call_id, call_type: "audio" },
  result.token,
  result.serverUrl,
  "recovery",
);
```

- [ ] Update `ResourceCallController.join`'s type (lines 47-114) to include the new required `mode` parameter.
- [ ] Update every internal call to `join(...)` inside this file (none exist beyond the public API — confirm via grep) and update the `joinPromiseRef`/dedup-by-generation logic if it captures `target` only — it should also key on `mode` for the in-flight-dedup comparison only if two different-mode joins for the same target could race; per the research, `joinPromiseRef` dedups by `{generation, target, promise}` — leave the dedup key as `target`-based (mode doesn't change target identity) but store `mode` alongside for use when the deferred promise actually runs.
- [ ] New tests in `useResourceCallSession.test.ts`:
  - `join(target, "fresh")` → `media.connect` called with `mode: "fresh"` as 4th arg (extend existing join tests, e.g. the "joins a channel room" test at lines 94-111).
  - `join(target, "recovery")` (simulating `activateResourceParticipation`, even with a known `target.callId`) → `media.connect` called with `mode: "recovery"` — explicitly proving `target.callId` presence alone does NOT drive the mode (extend the existing "known call_id uses call.join" test at lines 1315-1326 to also assert mode independently of the callId-known branch).
  - `reconnect()` → `media.connect` always called with `mode: "recovery"` (extend the existing reconnect test at lines 744-786).
- [ ] Run `pnpm --filter @nchat/web exec vitest run src/chat/useResourceCallSession.test.ts`.

---

## Task 8 — `CallSessionProvider.tsx`: `ownedMedia.connect` choke point + `confirmedIntentRef`

**Files:**

- Modify: `apps/web/src/calls/CallSessionProvider.tsx`
- Test: `apps/web/src/calls/CallSessionProvider.test.tsx`

**Interfaces:**

- Consumes: `MediaIntent`, `MediaConnectionMode`, `writeMediaIntent`, `readMediaIntent` from `../calls/callOwnership`; the updated `CallMediaBridge` type (Task 6, now requiring `mode` as connect's 4th param); `useCallMedia`'s updated `connect(call, token, url, initialIntent?): Promise<MediaIntent | undefined>` (Task 5).
- Produces: `confirmedIntentRef: MutableRefObject<{ callId: string; microphone: boolean; camera: boolean } | null>` (module-internal, not exported); `ownedMedia: CallMediaBridge` now implementing the `mode`-aware signature; no change to the public `CallSessionContextValue` shape beyond `media.toggleMicrophone`/`media.toggleCamera` now resolving `Promise<boolean | undefined>` (already reflected by Task 5's type change flowing through).

- [ ] Add `const confirmedIntentRef = useRef<{ callId: string; microphone: boolean; camera: boolean } | null>(null);` near the other per-call refs (e.g. next to `handoffCall`/`handoffEpoch`, ~line 271).
- [ ] Rewrite `ownedMedia` (lines 486-515) per §15:

```ts
const ownedMedia = useMemo<CallMediaBridge>(
  () => ({
    startAudio: media.startAudio,
    connect: async (call, token, serverUrl, mode) => {
      const ownership = getOwnership();
      const current = ownership.getLease();
      const lease =
        current?.callId === call.call_id ? current : await ownership.claim(call.call_id, role);
      if (!lease) {
        setOwner("remote");
        throw new Error("call media is owned by another tab");
      }
      setOwner("local");
      const initialIntent: MediaIntent | undefined =
        mode === "recovery"
          ? (ownership.readMediaIntent(call.call_id) ?? { microphone: false, camera: false })
          : undefined;
      try {
        const applied = await media.connect(call, token, serverUrl, initialIntent);
        if (!applied) {
          // Superseded before the connect settled — not a failure of THIS
          // attempt's ownership/storage, just stale; nothing to persist,
          // nothing to declare successful either. Let the caller's own
          // currency guard decide what happens next.
          throw new Error("media connection was superseded before it completed");
        }
        const wrote = ownership.writeMediaIntent(call.call_id, lease, applied);
        if (!wrote) {
          // §11: no transaction between LiveKit and localStorage exists.
          // The device is live but the durable write that makes it safe
          // to hand off is not — fail closed: this is NOT a successful
          // media connection for handoff purposes.
          await media.stop();
          ownership.release(call.call_id);
          setOwner("none");
          throw new Error("failed to persist media intent for handoff");
        }
        confirmedIntentRef.current = { callId: call.call_id, ...applied };
        emitCallTechnicalEvent("join-success");
      } catch (error) {
        emitCallTechnicalEvent("join-failure");
        ownership.release(call.call_id);
        setOwner("none");
        throw error;
      }
    },
    stop: async () => {
      await media.stop();
      emitCallTechnicalEvent("track-cleanup");
    },
  }),
  [getOwnership, media, role, setOwner],
);
```

Note: the `catch` block already covers the `writeMediaIntent`-failure `throw` path too (it re-releases/re-sets-owner, which is redundant with the explicit release inside the `if (!wrote)` branch but harmless — `ownership.release` is a no-op if this tab no longer holds that lease/callId). Keep both for clarity and because `release`'s no-op-if-mismatched-lease behavior is already relied on elsewhere in this file.

- [ ] Add `confirmedIntentRef.current = null;` wherever a genuinely NEW callId begins its own fresh connect (i.e., right before the `ownedMedia.connect` body runs for a call whose `call.call_id !== confirmedIntentRef.current?.callId` — simplest correct placement: at the very top of `connect`, before computing `lease`, do nothing (leave old ref) UNLESS the new `call.call_id` differs from the ref's `callId`, in which case reset it to `null` so a stale different-call snapshot never leaks into `wrappedToggle*`'s snapshot-merging logic in Task 9). Concretely, add as the first line of `connect`:

```ts
if (confirmedIntentRef.current && confirmedIntentRef.current.callId !== call.call_id) {
  confirmedIntentRef.current = null;
}
```

- [ ] `directMedia` (line ~533) already forwards `connect: ownedMedia.connect` by reference — since both now share the identical 4-arg signature, no change needed there beyond re-confirming the type still matches after `CallMediaBridge`'s Task 6 update.
- [ ] Update every call site inside this file that calls `resource.join(...)` — i.e. `joinResourceParticipation` (~line 626) and `activateResourceParticipation` (~line 702) — to pass the mode explicitly, per §14:

```ts
// inside joinResourceParticipation
return joinResourceCall(target, "fresh", onCallIdResolved);
// inside activateResourceParticipation
return joinResourceCall(target, "recovery", onCallIdResolved);
```

(Adjust to match the exact existing destructured alias name `joinResourceCall` found by research at line 529, and the exact existing parameter list/order at each call site — insert `mode` as the 2nd positional argument per Task 7's new `join` signature.)

- [ ] New tests in `CallSessionProvider.test.tsx` (extending the existing `describe` blocks that already capture `owned = vi.mocked(useResourceCallSession).mock.calls[0]![0]` and drive `owned.connect(...)` directly):
  - `owned.connect(call, token, url, "fresh")` with no stored media-intent: `media.connect` called with `initialIntent: undefined`.
  - `owned.connect(call, token, url, "recovery")` with a stored intent (mock `ownership.readMediaIntent` to return `{microphone:true,camera:false}`): `media.connect` called with that exact object as 4th arg.
  - `owned.connect(..., "recovery")` with `ownership.readMediaIntent` returning `null` (missing/corrupt): `media.connect` called with `{microphone:false,camera:false}` (§16 absolute rule).
  - Successful connect calls `ownership.writeMediaIntent(callId, lease, appliedIntent)` and only then is the connection treated as successful (assert ordering via mock call order, or via a rejected `writeMediaIntent` in the next case).
  - `ownership.writeMediaIntent` returning `false` (durable write failure) → `media.stop()` called, `ownership.release` called, `owner` becomes `"none"`, and `owned.connect(...)` rejects (§11).
  - `media.connect` resolving `undefined` (superseded) → `owned.connect(...)` rejects, ownership released, no `writeMediaIntent` call.
- [ ] Run `pnpm --filter @nchat/web exec vitest run src/calls/CallSessionProvider.test.tsx`.

---

## Task 9 — `CallSessionProvider.tsx`: toggle wrappers, exposed via context

**Files:**

- Modify: `apps/web/src/calls/CallSessionProvider.tsx`
- Modify: `apps/web/src/calls/DedicatedCallPage.tsx` (only if the wrapping approach below requires it — see step 3)
- Test: `apps/web/src/calls/CallSessionProvider.test.tsx`

**Interfaces:**

- Consumes: `confirmedIntentRef`, `ownedMedia`/`getOwnership` (Task 8); `media.toggleMicrophone`/`media.toggleCamera` now `Promise<boolean | undefined>` (Task 5).
- Produces: `wrappedToggleMicrophone`, `wrappedToggleCamera`: `() => Promise<boolean | undefined>`, exposed as the `toggleMicrophone`/`toggleCamera` fields of the `media` object placed on the context value (never the raw `useCallMedia()` ones).

- [ ] Add the two wrappers near `ownedMedia` (after it, since they read `getOwnership`/`confirmedIntentRef` the same way):

```ts
const wrappedToggleMicrophone = useCallback(async (): Promise<boolean | undefined> => {
  const lease = getOwnership().getLease();
  const callId = lease?.callId;
  const result = await media.toggleMicrophone();
  if (result === undefined || !lease || !callId) return result;
  const camera =
    confirmedIntentRef.current?.callId === callId
      ? confirmedIntentRef.current.camera
      : media.cameraEnabled;
  const wrote = getOwnership().writeMediaIntent(callId, lease, { microphone: result, camera });
  if (wrote) confirmedIntentRef.current = { callId, microphone: result, camera };
  return result;
}, [getOwnership, media]);

const wrappedToggleCamera = useCallback(async (): Promise<boolean | undefined> => {
  const lease = getOwnership().getLease();
  const callId = lease?.callId;
  const result = await media.toggleCamera();
  if (result === undefined || !lease || !callId) return result;
  const microphone =
    confirmedIntentRef.current?.callId === callId
      ? confirmedIntentRef.current.microphone
      : media.microphoneEnabled;
  const wrote = getOwnership().writeMediaIntent(callId, lease, { microphone, camera: result });
  if (wrote) confirmedIntentRef.current = { callId, microphone, camera: result };
  return result;
}, [getOwnership, media]);
```

The causal lease is captured with `getOwnership().getLease()` **before** `await media.toggle...()` — if ownership is lost mid-toggle, `writeMediaIntent`'s own fencing (Task 4: `lease.epoch !== captured.epoch` / `lease.tabId !== tabId`) rejects the write, satisfying "stale toggle after ownership loss does NOT write" (§19) without any extra bookkeeping here.

- [ ] Build `const wrappedMedia = useMemo(() => ({ ...media, toggleMicrophone: wrappedToggleMicrophone, toggleCamera: wrappedToggleCamera }), [media, wrappedToggleMicrophone, wrappedToggleCamera]);` right after the wrappers.
- [ ] Replace every remaining reference to the raw `media` object that is EXTERNALLY visible (i.e., the `media` field placed on the memoized context `value`, and the `controls` object built for `FloatingCallWindow`) with `wrappedMedia`. Internal uses (`ownedMedia`'s own `media.connect`/`media.stop`, `directMedia`'s `stop`, `resource = useResourceCallSession(ownedMedia, ...)`) keep referencing the raw `media`/`ownedMedia` — only the two audited external hand-off points (§10's audit list) change:
  - Context value's `media` field (~line 1213/1246): `media: wrappedMedia,`
  - `controls` object (~lines 1291-1298): `onMicrophone: wrappedMedia.toggleMicrophone, onCamera: wrappedMedia.toggleCamera,` (or simply keep `media.toggleMicrophone` there and rename the local `media` variable — whichever keeps the diff smallest; prefer swapping the `controls` object's two fields explicitly to `wrappedToggleMicrophone`/`wrappedToggleCamera` directly, since that's the most legible and avoids any ambiguity about which `media` is in scope at that point in the function).
- [ ] Since `DedicatedCallPage.tsx:148-149` reads `session.media.toggleMicrophone`/`session.media.toggleCamera` off the context value (not off `controls`), swapping the context's `media` field to `wrappedMedia` automatically fixes that consumer too — **no change needed in `DedicatedCallPage.tsx`** as long as the context value's `media` field is `wrappedMedia`. Confirm this by grep after the change: `apps/web/src/calls/DedicatedCallPage.tsx` must show zero remaining references to a non-wrapped toggle.
- [ ] New tests in `CallSessionProvider.test.tsx`:
  - `session.media.toggleMicrophone()` (or however the test harness exposes the context value) resolving `true`/`false` calls `ownership.writeMediaIntent` with a snapshot merging the new mic value and the last confirmed (or current) camera value.
  - `media.toggleMicrophone` mock resolving `undefined` (stale/superseded per Task 5) → `ownership.writeMediaIntent` is NOT called, `confirmedIntentRef`-observable state (assert indirectly via a subsequent `readMediaIntent` call or a re-toggle) is untouched.
  - Toggle called after ownership is lost mid-flight (`ownership.getLease` returns a lease captured before, but by the time `writeMediaIntent` mock is invoked, simulate its own internal fencing rejecting by having the mock return `false`) → wrapper still resolves the boolean from `media.toggleMicrophone` (UI reflects it) but does not update anything that would leak into a later successful write's merged snapshot incorrectly.
  - Server/SDK mute scenario (§20): directly unit-test `useCallMedia`'s state effects are unaffected (already covered by Task 5's tests) plus one `CallSessionProvider` test confirming that a `media.microphoneEnabled` state flip NOT caused by calling `wrappedToggleMicrophone` never calls `ownership.writeMediaIntent` — i.e., only invoking the wrapper writes, never a `microphoneEnabled` state observer.
- [ ] Run `pnpm --filter @nchat/web exec vitest run src/calls/CallSessionProvider.test.tsx`.

---

## Task 10 — Full main↔dedicated / resource handoff integration tests

**Files:**

- Modify: `apps/web/src/calls/CallSessionProvider.test.tsx` (primary home for these — it already mocks `useCallMedia`/`useResourceCallSession`/`createOwnershipCoordinator` together, matching the multi-layer scenario needed)
- Possibly modify: `apps/web/src/calls/callOwnership.test.ts` for the pure-storage-layer half of each scenario (no React), if a scenario is cleaner expressed at that level.

**Interfaces:**

- Consumes: everything from Tasks 1-9.

- [ ] §17 matrix — direct main→dedicated, all 4 boolean combinations: two `CallSessionProvider` instances (or two coordinator+callOwnership-level simulations, matching the existing "two coordinators share one `SharedStorage`" pattern from `callOwnership.test.ts`) — main claims epoch E1, `ownedMedia.connect(..., "fresh")` persists intent I at E1; main `stop()`+ownership `release()`; dedicated claims epoch >E1; `ownership.readMediaIntent` still returns I; dedicated `ownedMedia.connect(..., "recovery")` applies I; dedicated writes I (or a possibly-adjusted applied snapshot) at its own new epoch; only after that does the test assert "connected".
- [ ] §18 — dedicated→main: dedicated at E2 toggles intent (via `wrappedToggleMicrophone`/`wrappedToggleCamera`), durable write at E2; dedicated stop/release; main claims epoch >E2; read finds E2's intent; main connects recovery; main writes E3.
- [ ] §19 — toggle vs handoff race, both orderings: (a) toggle SDK success → persist → wrapper resolves → THEN handoff stop/release happens → new owner reads the new value. (b) stop() invalidates the toggle's generation before `media.toggleMicrophone`'s promise resolves → wrapper receives `undefined` → no write, no `confirmedIntentRef` update. Implement without relying on React effect ordering — control promise resolution order explicitly with manual deferred promises, matching the existing `useCallMedia.test.tsx` `deferred<T>()` idiom (import or replicate the same tiny helper in this test file if not already present).
- [ ] §20 — server/SDK mute: `microphoneEnabled` flips to `false` via a callback path that is NOT the wrapper (simulate by directly calling the mocked `media`'s internal state update the way `useCallMedia`'s `onMicrophoneStateChanged` would, i.e. through whatever the CallSessionProvider test harness exposes for `media` state) while `confirmedIntentRef`/persisted intent stays `true`; then a subsequent real `wrappedToggleMicrophone()` call behaves normally and persists the new user-driven value.
- [ ] §21 — crash/reload, no `BroadcastChannel` message: construct a coordinator/provider instance against a `SharedStorage` that already has a previous owner's durable media-intent snapshot but no live lease (simulating a crash — lease expired/absent, media-intent entry present); a fresh claim + `readMediaIntent` recovers it correctly. Add the equivalent for a dedicated reload (matching the existing e2e `readLease`-after-reload style, but at the unit level with `callOwnership.ts` directly).
- [ ] §22 — resource #622 proof: main P1+E1+intent I → dedicated re-admission P2+E2 receives I → dedicated changes intent to J → main reclaim P3+E3 receives J; assert stale P1/E1/E2 writers never affect the CURRENT participation or media intent (two independent comparators, participation via `compareParticipationTokens`, ownership/media-intent via `compareOwnershipClaims` — assert both independently in the same test to prove they don't get mixed).
- [ ] Run the complete required validation suite listed in Task 11 after this task, since these are the tests most likely to reveal integration gaps from Tasks 1-9.

---

## Task 11 — Validation pass

**Files:** none (verification only).

- [ ] `pnpm --filter @nchat/web exec vitest run src/calls/callOwnership.test.ts src/calls/CallSessionProvider.test.tsx src/chat/useCallMedia.test.tsx src/chat/useCallSignaling.test.ts src/chat/useResourceCallSession.test.ts`
- [ ] `pnpm typecheck:web`
- [ ] `pnpm lint:web`
- [ ] `pnpm format:check:web`
- [ ] `pnpm --filter @nchat/web build`
- [ ] Attempt the focused e2e handoff test locally; since `call-floating-handoff.spec.ts`'s connected-media assertions are already `.skip`d in this repo (no real LiveKit/media-service available to the E2E project — confirmed by research), add one new, runnable (non-skipped) e2e test alongside the existing `readLease`/reload test that seeds a `nchat.call.media-intent.v1.*` key via `localStorage` before triggering the existing dedicated reload/reclaim flow, and asserts the key survives and is still well-formed after reload — this is the only part of the handoff that doesn't require a real LiveKit connection. Document in the final report that the full connected-media assertions remain infeasible locally for the same pre-existing infra reason the rest of that describe block is skipped.
- [ ] `git diff --check`
- [ ] `git status --short`
- [ ] `git diff --stat`
- [ ] Run a broader `src/chat` + `src/calls` vitest pass if time permits: `pnpm --filter @nchat/web exec vitest run src/chat src/calls`
- [ ] Compose the final report per the issue's §27 checklist (HEAD/merge-base, files touched, design summary, all 28 numbered points) — do not commit or push.

## Self-Review Notes (writing-plans skill)

- **Spec coverage:** §1-2 → Task 2. §3 → Task 3. §4-5 → Task 4. §6-7 → Task 5. §8 (confirmedIntentRef) → Tasks 8-9. §9 (no useEffect) → satisfied by construction (persistence lives inside async connect/toggle flows only). §10 (wrappers/audit) → Task 9. §11 (storage failure fail-closed) → Task 8. §12-14 (mode) → Tasks 2, 6, 7, 8. §15-16 (owned connect, recovery-no-snapshot) → Task 8. §17-23 (proof matrices) → Task 10. §24 (out of scope) → nothing in this plan touches screen-share persistence or #611-616. §25-27 → Tasks 10-11.
- **Type consistency check:** `MediaIntent`/`MediaConnectionMode` defined once (Task 2, `callOwnership.ts`) and imported everywhere else — no duplicate/divergent shape declarations. `connect`'s two distinct signatures (raw `useCallMedia.connect` with `initialIntent?`, vs `CallMediaBridge.connect` with required `mode`) are named consistently as "the device layer" vs "the bridge layer" throughout to avoid confusing the two during implementation.
- **No placeholders:** every task above includes literal code for the non-trivial logic; test tasks list concrete scenarios rather than "add tests for the above."
