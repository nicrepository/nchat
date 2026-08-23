import { randomId } from "../lib/randomId";

const CHANNEL_NAME = "nchat-call-ownership-v1";
const LEASE_KEY = "nchat.call.owner.v1";
// Participation storage (issue #570 follow-up) — a SEPARATE key space from
// LEASE_KEY/epoch, which belongs to the ownership lease, a different state
// machine that resets/moves independently of participation and carries no
// causal meaning for it.
//
// One key per (callId, writerId) — never a single shared key holding every
// writer's contribution. A shared key requires read-modify-write from
// multiple tabs, and localStorage.setItem has no compare-and-swap: a tab
// whose read predates another tab's write can still overwrite that write
// later with its own stale merge, because the write is a blind replace of
// the whole key, not a conditional update of one field (confirmed
// exploitable without Web Locks — the v1 shared-key design suffered exactly
// this "stale writer" regression). Scoping each writer to its OWN exclusive
// key removes the race structurally: no other tab ever writes THIS key, so
// there is nothing for a stale read to race against on the write path.
// "Current" is derived at read time as the max, by compareParticipationTokens,
// across every writer's key for that callId — never "whichever key a reader
// happens to look at" or "the most recently written key".
//
// No eviction/cap/cleanup here (deliberately out of scope for this issue):
// a callId's writer keys are the only record of the highest participation
// token ever seen for each writer, and deleting any of them from a stale
// snapshot reintroduces the same class of race this redesign exists to
// remove (a reader could decide a key is safely dominated, then that writer
// updates it, then the reader's stale deletion destroys the update). Keys
// accumulate — one per distinct (callId, writerId) pair ever seen — for as
// long as the browser keeps this localStorage origin. That growth is a
// documented residual/maintenance risk, not a #570 correctness bug: pruning
// requires a design that can safely identify a key as permanently
// irrelevant, which needs its own dedicated pass.
const PARTICIPATION_KEY_PREFIX = "nchat.call.participation.v2.";
// Media-intent storage (issue #610) — a SEPARATE key space from both
// LEASE_KEY and PARTICIPATION_KEY_PREFIX. Structurally identical to the
// participation per-writer pattern above and for the same reason: one
// exclusive key per (callId, writerId) so no writer ever needs
// read-modify-write on another writer's contribution. Survives release()
// removing OwnerLease — that is the entire point: mic/camera intent must
// outlive the ownership lease that was active when it was recorded, so a
// later claim (even after this callId's lease was fully released) can
// still recover it. Same no-eviction/residual-growth caveat as
// PARTICIPATION_KEY_PREFIX applies here.
const MEDIA_INTENT_KEY_PREFIX = "nchat.call.media-intent.v1.";
const MESSAGE_TYPES = new Set([
  "ready",
  "heartbeat",
  "released",
  "ack",
  "failure",
  "handoff",
  "claim",
  "takeover",
  "participating",
  "leaving",
  "left",
  "leave-cancelled",
]);
const TARGETED_TYPES = new Set(["handoff", "claim", "takeover"]);
// A participant's own join/leave lifecycle (issue #569/#570) — never a
// claim over the ownership lease, so these never compete for it and always
// bypass the epoch-staleness gate in onMessage below. "participating" means
// a join actually succeeded; "leaving" means a leave is in flight (no tab
// may reconnect it while pending); "left" means it was confirmed;
// "leave-cancelled" means it failed and that same participation is
// participating/recoverable again. Named for what actually happened to this
// participant's own membership — not the resource call globally, which may
// still be active for others.
//
// `generation`/`sequence` (not `epoch`, which belongs to the ownership
// lease and carries no causal meaning for participation) is how a receiver
// orders these deterministically: `generation` increments once per new
// join of a given call_id, so a message from an old, already-superseded
// participation can never outrank a newer one no matter how it collides or
// reorders in transit; `sequence` orders leaving/left/leave-cancelled
// within one participation's own lifecycle. Neither depends on wall-clock
// resolution.
const PARTICIPATION_TYPES = new Set(["participating", "leaving", "left", "leave-cancelled"]);

export interface OwnerLease {
  v: 1;
  callId: string;
  tabId: string;
  epoch: number;
  role: "main" | "dedicated";
  expiresAt: number;
}

// The value persisted at one writer's own key — deliberately its own shape,
// never OwnerLease: it has no role, no expiry, no epoch. writerId is never
// duplicated inside the value; it is already the key's own suffix, and the
// read path reconstructs it from there (see participationTokenFromKey).
interface ParticipationWriterEntry {
  v: 2;
  generation: number;
}

/**
 * A participation's causally-ordered identity: (generation, writerId).
 * `writerId` is the tabId that allocated `generation` for this callId —
 * without a real cross-process lock, two tabs racing to allocate can end up
 * both writing the same generation number (see allocateParticipationGeneration
 * below), so generation ALONE is not always unique. writerId makes the pair
 * always unique and, via compareParticipationTokens, always totally
 * orderable — every tab reaches the identical verdict about which of two
 * same-generation tokens is "later", regardless of arrival order.
 */
export interface ParticipationToken {
  generation: number;
  writerId: string;
}

/** User mic/camera intent — the shape media.connect()/toggles actually consume. */
export interface MediaIntent {
  microphone: boolean;
  camera: boolean;
}

/**
 * Belongs to the media connection ATTEMPT, never inferred from call_type,
 * callId, or any other generic state — see useCallSignaling/
 * useResourceCallSession/CallSessionProvider (issue #610).
 */
export type MediaConnectionMode = "fresh" | "recovery";

/**
 * Write-ahead safety marker (issue #610 privacy blocker follow-up).
 * "pending" is written SYNCHRONOUSLY, BEFORE the SDK device change it
 * describes is attempted — durable evidence that the previous "confirmed"
 * value at this writer's key may no longer reflect reality, even if
 * everything after this write (the SDK call, the later "confirmed" write,
 * this tab's OwnerLease heartbeat) subsequently fails. "confirmed" is
 * written only after the SDK genuinely applied that exact value. Recovery
 * (readMediaIntentForLease) treats a winning "pending" entry as OFF/OFF —
 * never as evidence of a specific device state — regardless of the
 * microphone/camera booleans it happens to carry (see MediaIntentEntry).
 */
export type MediaIntentPhase = "confirmed" | "pending";

export interface WriteMediaIntentOptions {
  /**
   * Optimistic-concurrency guard for a "confirmed" write meant to settle a
   * specific earlier "pending" write from the SAME writer: the write is
   * rejected as stale unless the currently-stored entry's revision still
   * equals this value. Without it, a slow/superseded operation's stale
   * "confirmed" commit could overwrite a NEWER operation's own pending/
   * confirmed entry for the same writer key (issue #610: "operação antiga
   * não pode confirmar um pending de operação nova"). Omit only for the
   * FIRST write of an attempt (the pending pre-write, or a fresh/recovery
   * connect's confirmed write), which has no prior revision of its own to
   * protect.
   */
  expectedRevision?: number;
}

export type WriteMediaIntentOutcome =
  | { ok: true; revision: number }
  /**
   * "stale": this write lost to a newer, causally-later fact — ownership
   * moved on (captured no longer matches the coordinator's current lease,
   * or captured.epoch is behind a recorded ownershipEpoch), or a newer
   * same-writer operation's revision already landed (expectedRevision
   * mismatch). Never a storage failure — no teardown/escalation is
   * warranted, the newer fact already governs.
   * "storage-error": the underlying storage.setItem genuinely threw
   * (quota/security/access/etc). The caller must treat this as a real
   * failure requiring its own fail-closed response — this layer cannot
   * fix it, only report it.
   */
  | { ok: false; reason: "stale" | "storage-error" };

// The value persisted at one writer's own media-intent key. `revision` only
// ever orders entries from the SAME writer at the SAME ownershipEpoch — it
// never decides a winner between different writers (see
// compareOwnershipClaims, which media-intent's own comparator delegates
// to, and which never looks at revision).
interface MediaIntentEntry extends MediaIntent {
  v: 1;
  ownershipEpoch: number;
  revision: number;
  phase: MediaIntentPhase;
}

/**
 * Total, deterministic order over participation tokens. A strictly higher
 * generation always wins, regardless of writerId — that is the normal case
 * (a real, later join). Only when generation ties (only reachable without a
 * real cross-process lock serializing allocateParticipationGeneration) does
 * writerId decide — an arbitrary but fixed and symmetric rule
 * (compare(a, b) === -compare(b, a)), so every tab that receives both
 * tokens, in either order, converges on the same single winner.
 */
export function compareParticipationTokens(a: ParticipationToken, b: ParticipationToken): number {
  if (a.generation !== b.generation) return a.generation - b.generation;
  if (a.writerId === b.writerId) return 0;
  return a.writerId < b.writerId ? 1 : -1;
}

type OwnershipMessageType = "ready" | "heartbeat" | "released" | "ack" | "failure";
export type ParticipationMessageType = "participating" | "leaving" | "left" | "leave-cancelled";

export type OwnershipMessage =
  | {
      v: 1;
      type: OwnershipMessageType;
      callId: string;
      tabId: string;
      epoch: number;
    }
  | {
      v: 1;
      type: "handoff" | "claim" | "takeover";
      callId: string;
      tabId: string;
      targetTabId: string;
      epoch: number;
    }
  | {
      v: 1;
      type: ParticipationMessageType;
      callId: string;
      tabId: string;
      epoch: number;
      /** Increments once per new join of this call_id. A message from an
       * old participation can never outrank a newer one, regardless of
       * delivery order or timestamp collisions — except when it ties with
       * `writerId` below (see compareParticipationTokens). */
      generation: number;
      /** The tabId that allocated `generation` for this participation —
       * never wall-clock time, which two independent tabs can collide on.
       * Together with `generation` this is a ParticipationToken: the
       * disambiguator for the rare case where two tabs raced to the same
       * generation without a real cross-process lock serializing it. */
      writerId: string;
      /** Orders leaving/left/leave-cancelled within one participation
       * (one token) — strictly increasing as that participation's own
       * lifecycle actually proceeds, so a reordered-in-transit message for
       * an earlier step can never overwrite a later one. */
      sequence: number;
    };

interface ChannelLike {
  postMessage(value: unknown): void;
  addEventListener(type: "message", listener: EventListener): void;
  removeEventListener(type: "message", listener: EventListener): void;
  close(): void;
}

interface LockLike {
  request(name: string, callback: () => Promise<OwnerLease | null>): Promise<OwnerLease | null>;
}

type IntervalHandle = unknown;

export interface OwnershipCoordinatorOptions {
  tabId?: string;
  storage?: Storage;
  channel?: ChannelLike;
  locks?: LockLike | null;
  now?: () => number;
  settle?: () => Promise<void>;
  setInterval?: (callback: () => void, delay: number) => IntervalHandle;
  clearInterval?: (handle: IntervalHandle) => void;
  leaseMs?: number;
  heartbeatMs?: number;
  onOwnershipLost?: (lease: OwnerLease) => void;
}

export interface OwnershipCoordinator {
  readonly tabId: string;
  /**
   * `predecessorHint`, when the caller already knows it from a live signal
   * (e.g. a "handoff"/"released" message's own {tabId, epoch}), feeds the
   * causal predecessor for readMediaIntentForLease below — never derived
   * from scanning media-intent entries themselves (issue #610 audit: a
   * same-epoch entry from a writer that lost the ownership race without
   * Web Locks is never proof of having been the real predecessor, no
   * matter how it compares to other entries). The hint is validated, not
   * trusted blindly — see resolvePredecessor: it is rejected if its epoch
   * doesn't match `afterEpoch`, and can never regress behind a newer
   * OwnerLease this coordinator directly observes in storage. Without a
   * usable hint, the predecessor is derived only from that directly
   * observed OwnerLease (existingForCall) — see readMediaIntentForLease's
   * own doc for the full precedence.
   */
  claim(
    callId: string,
    role: OwnerLease["role"],
    afterEpoch?: number,
    predecessorHint?: OwnershipClaim,
  ): Promise<OwnerLease | null>;
  getLease(): OwnerLease | null;
  getOwner(callId: string): OwnerLease | null;
  release(callId: string): void;
  /**
   * Allocates a fresh ParticipationToken for callId — cross-tab and
   * reload-safe. Reads every writer's own persisted key for this callId,
   * derives the max via compareParticipationTokens, writes generation
   * max+1 to THIS tab's own key (nchat.call.participation.v2.<callId>.
   * <writerId>) — never any other writer's key — and returns that token.
   * Every real join (issue #570 follow-up) must call this to learn its
   * token — never compute a generation from this tab's own local, possibly
   * still-empty knowledge — so a brand-new or just-reloaded tab always
   * allocates strictly past whatever any writer, including one that has
   * since closed, last recorded for THIS callId specifically (a different
   * callId used in between never overwrites or resets it).
   *
   * Synchronous and best-effort: the max this call derives can be stale by
   * the time it writes (another tab may allocate concurrently), so two tabs
   * CAN legitimately both compute generation N. That is fine and requires
   * no fix — each writes only its own key, so neither write is ever lost,
   * both tokens stay persisted, and compareParticipationTokens' writerId
   * tie-break makes every tab that later reads them converge on the same
   * single winner regardless of arrival order. This is what makes the
   * allocator safe without Web Locks: correctness never depended on any one
   * key holding a globally-unique number, only on no writer ever
   * overwriting another's key — which per-writer keys guarantee
   * structurally, not probabilistically.
   */
  allocateParticipationGeneration(callId: string): ParticipationToken;
  /**
   * The current token for callId, without allocating a new one — the max,
   * by compareParticipationTokens, across every writer's own persisted key
   * for this callId. Never "whichever key was written last": every writer's
   * contribution independently survives (nothing overwrites another
   * writer's key), so this always reflects the true causal winner among
   * everything any writer has ever recorded for this callId, not merely
   * whatever a single last physical write happened to leave behind. Lets a
   * tab that took over an EXISTING participation via handoff/reconnect —
   * never its own join() — learn the token it must tag its own
   * leaving/left with, even though it never itself observed the
   * "participating" broadcast that created it. Returns null if this callId
   * has no recorded participation from any writer.
   */
  getParticipationToken(callId: string): ParticipationToken | null;
  /**
   * Persists this tab's mic/camera intent for callId at the given `phase`,
   * fenced by the causal lease captured before the connect/toggle that
   * produced `intent` began. Returns `{ok:true, revision}` on success, or
   * `{ok:false, reason}` — see WriteMediaIntentOutcome for what each
   * reason means and what the caller should (not) do about it. Rejects as
   * "stale" when: this coordinator is closed; `callId` is invalid;
   * `captured.tabId` is not this coordinator's own tabId; this
   * coordinator's CURRENT lease no longer matches `captured` (ownership
   * moved on since `captured` was taken); `captured.epoch` is lower than
   * the highest ownershipEpoch this writer has already persisted for
   * callId; or `options.expectedRevision` is given and no longer matches
   * the stored entry's revision (a newer same-writer operation already
   * landed). Rejects as "storage-error" only when the underlying
   * storage.setItem itself throws.
   */
  writeMediaIntent(
    callId: string,
    captured: OwnerLease,
    intent: MediaIntent,
    phase: MediaIntentPhase,
    options?: WriteMediaIntentOptions,
  ): WriteMediaIntentOutcome;
  /**
   * The mic/camera intent that is safe to apply as a preset for a connect
   * happening under THIS coordinator's own currently-held lease for
   * callId — never a plain "highest epoch below mine" scan (issue #610
   * audit: a same-epoch entry from a writer that lost the ownership race
   * without Web Locks is never proof of having been a real predecessor,
   * no matter how old it later looks relative to a future epoch — "is the
   * only entry at that epoch" is not evidence either). Requires this
   * coordinator to currently hold a lease for callId (returns null
   * otherwise). Checked in order:
   *   1. This writer's OWN entry at the CURRENT epoch — a same-epoch
   *      reconnect/retry with no new claim() in between; we already know
   *      our own state.
   *   2. The specific writer/epoch this coordinator identified as its
   *      causal predecessor when it claimed the current lease (see
   *      claim()'s `predecessorHint` doc) — read from THAT writer's own
   *      key, and only trusted if the entry found there still reports
   *      exactly that predecessor epoch (never "whatever that writer's
   *      key currently holds", which could since have moved on).
   *      Predecessor identity comes ONLY from resolvePredecessor: a
   *      `predecessorHint` given to claim() (validated against
   *      `afterEpoch` and never allowed to regress behind newer storage
   *      evidence — see that function's own doc), or else the OwnerLease
   *      this coordinator observed in storage immediately before winning
   *      (even if expired — the crash case). There is deliberately no
   *      other fallback: this tab's own memory of a callId it previously
   *      released is never used (removed after a formal audit — an empty
   *      LEASE_KEY never proves no intermediate owner claimed and
   *      released it in between), and BroadcastChannel history is never
   *      used either, since a reload can never replay it.
   *   3. Predecessor unknown/unprovable → null (privacy-safe: the caller
   *      degrades to OFF/OFF, never a global-max approximation).
   * Once a causal entry is selected this way, phase decides the returned
   * value: "confirmed" → its microphone/camera; "pending" → always
   * {microphone:false,camera:false} (evidence the true device state may
   * already have diverged from whatever booleans it carries — never
   * trusted as a specific preset).
   */
  readMediaIntentForLease(callId: string): MediaIntent | null;
  post(message: OwnershipMessage): void;
  subscribe(listener: (message: OwnershipMessage) => void): () => void;
  onOwnershipLost(listener: (lease: OwnerLease) => void): () => void;
  close(): void;
  isClosed(): boolean;
}

function isBoundedString(value: unknown): value is string {
  return typeof value === "string" && value.length > 0 && value.length <= 128;
}

function isEpoch(value: unknown): value is number {
  return Number.isSafeInteger(value) && Number(value) >= 0;
}

export function parseOwnershipMessage(value: unknown): OwnershipMessage | null {
  if (!value || typeof value !== "object" || Array.isArray(value)) return null;
  const record = value as Record<string, unknown>;
  if (
    record.v !== 1 ||
    typeof record.type !== "string" ||
    !MESSAGE_TYPES.has(record.type) ||
    !isBoundedString(record.callId) ||
    !isBoundedString(record.tabId) ||
    !isEpoch(record.epoch)
  ) {
    return null;
  }
  const targeted = TARGETED_TYPES.has(record.type);
  const participation = PARTICIPATION_TYPES.has(record.type);
  if (Object.keys(record).length !== (targeted ? 6 : participation ? 8 : 5)) return null;
  if (targeted && !isBoundedString(record.targetTabId)) return null;
  if (
    participation &&
    (!isEpoch(record.generation) || !isBoundedString(record.writerId) || !isEpoch(record.sequence))
  ) {
    return null;
  }
  return record as OwnershipMessage;
}

export function parseOwnerLease(value: string | null): OwnerLease | null {
  if (!value) return null;
  try {
    const record = JSON.parse(value) as Record<string, unknown>;
    if (
      !record ||
      typeof record !== "object" ||
      Array.isArray(record) ||
      Object.keys(record).length !== 6 ||
      record.v !== 1 ||
      !isBoundedString(record.callId) ||
      !isBoundedString(record.tabId) ||
      !isEpoch(record.epoch) ||
      (record.role !== "main" && record.role !== "dedicated") ||
      typeof record.expiresAt !== "number" ||
      !Number.isFinite(record.expiresAt)
    ) {
      return null;
    }
    return record as unknown as OwnerLease;
  } catch {
    return null;
  }
}

function parseParticipationWriterEntry(value: string | null): ParticipationWriterEntry | null {
  if (!value) return null;
  try {
    const record = JSON.parse(value) as Record<string, unknown>;
    if (
      !record ||
      typeof record !== "object" ||
      Array.isArray(record) ||
      Object.keys(record).length !== 2 ||
      record.v !== 2 ||
      !isEpoch(record.generation)
    ) {
      return null;
    }
    return record as unknown as ParticipationWriterEntry;
  } catch {
    return null;
  }
}

// The exact prefix every one of callId's writer keys starts with. callId is
// percent-encoded before the ":" delimiter — not merely trusted to already
// be UUID-shaped and colon-free, which callOwnership.ts's own `callId:
// string` signature never actually guarantees to its callers. encodeURIComponent
// never emits a literal ":" (it is one of the characters it always escapes,
// along with its own escape character "%", so a callId that already
// contains "%3A"-looking text can never collide with one that contains a
// real ":" either) and is injective for well-formed strings, so two
// DIFFERENT callIds can never encode to the same prefix — the boundary
// between the callId component and writerId is unambiguous by
// construction, never by an assumption about what characters callId
// happens to contain. encodeURIComponent can throw for a malformed
// lone-surrogate string; every caller of this (and of participationKey)
// already runs inside a try/catch that degrades gracefully on any storage
// failure, so that is never allowed to surface as an unhandled exception.
function participationKeyPrefix(callId: string): string {
  return `${PARTICIPATION_KEY_PREFIX}${encodeURIComponent(callId)}:`;
}

function participationKey(callId: string, writerId: string): string {
  return `${participationKeyPrefix(callId)}${writerId}`;
}

// Same construction/isolation guarantees as participationKeyPrefix above,
// for the separate media-intent key space.
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
      Object.keys(record).length !== 6 ||
      record.v !== 1 ||
      !isEpoch(record.ownershipEpoch) ||
      !isEpoch(record.revision) ||
      (record.revision as number) < 1 ||
      (record.phase !== "confirmed" && record.phase !== "pending") ||
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

export function isLeaseExpired(lease: Pick<OwnerLease, "expiresAt">, now: number): boolean {
  return lease.expiresAt <= now;
}

export interface OwnershipClaim {
  epoch: number;
  tabId: string;
}

/**
 * Total order over ownership claims: (epoch, tabId). Higher epoch always
 * wins. On a tie, the SAME tabId-wins rule as resolveLeaseConflict (smaller
 * tabId string wins) — extracted here so the two can never diverge on which
 * claim is "later". Returns >0 when `a` wins/is later, <0 when `b` wins, 0
 * when identical. Deliberately takes only {epoch, tabId} — a revision-like
 * field (see media-intent, issue #610) never participates in ordering
 * claims from DIFFERENT writers.
 */
export function compareOwnershipClaims(a: OwnershipClaim, b: OwnershipClaim): number {
  if (a.epoch !== b.epoch) return a.epoch - b.epoch;
  if (a.tabId === b.tabId) return 0;
  return a.tabId.localeCompare(b.tabId) <= 0 ? 1 : -1;
}

export function resolveLeaseConflict(a: OwnerLease, b: OwnerLease): OwnerLease {
  return compareOwnershipClaims(a, b) >= 0 ? a : b;
}

/**
 * The causal predecessor a new claim should record (issue #610 audit,
 * final blocker) — pure and directly testable, deliberately taking only
 * the two possible EVIDENCE sources plus the epoch this claim is meant to
 * supersede, never anything from wall-clock, BroadcastChannel history, or
 * this tab's own past-release memory (none of those prove no intermediate
 * owner existed in between).
 *
 * - No hint: `existingForCall` alone (an OwnerLease this coordinator just
 *   read from storage — even expired, the crash case — or null).
 * - A hint whose epoch does not match `afterEpoch` (when `afterEpoch` is
 *   given) describes a claim other than the one actually being superseded
 *   — rejected outright, falling back to `existingForCall`.
 * - Otherwise, the hint and `existingForCall` are compared with the exact
 *   same rule as resolveLeaseConflict/compareOwnershipClaims (never a
 *   bespoke rule, never revision): a hint can never regress behind
 *   directly-observed, newer/tied-and-canonical storage evidence — only
 *   ever fill in when storage shows nothing, or agrees.
 * - No hint and no existingForCall: predecessor is unknown (null) — never
 *   approximated. Callers of readMediaIntentForLease degrade to OFF/OFF.
 */
export function resolvePredecessor(
  existingForCall: OwnershipClaim | null,
  predecessorHint: OwnershipClaim | undefined,
  afterEpoch: number | undefined,
): OwnershipClaim | null {
  if (!predecessorHint) return existingForCall;
  if (afterEpoch !== undefined && predecessorHint.epoch !== afterEpoch) return existingForCall;
  if (!existingForCall) return predecessorHint;
  return compareOwnershipClaims(existingForCall, predecessorHint) > 0
    ? existingForCall
    : predecessorHint;
}

export function createOwnershipCoordinator(
  options: OwnershipCoordinatorOptions = {},
): OwnershipCoordinator {
  const tabId = options.tabId ?? randomId();
  const storage = options.storage ?? localStorage;
  const channel = options.channel ?? new BroadcastChannel(CHANNEL_NAME);
  const now = options.now ?? Date.now;
  const settle =
    options.settle ?? (() => new Promise<void>((resolve) => globalThis.setTimeout(resolve, 50)));
  const scheduleInterval =
    options.setInterval ??
    ((callback: () => void, delay: number) => globalThis.setInterval(callback, delay));
  const cancelInterval =
    options.clearInterval ??
    ((handle: IntervalHandle) =>
      globalThis.clearInterval(handle as ReturnType<typeof globalThis.setInterval>));
  const leaseMs = options.leaseMs ?? 5000;
  const heartbeatMs = options.heartbeatMs ?? 1500;
  const nativeLocks = typeof navigator !== "undefined" ? navigator.locks : undefined;
  const locks: LockLike | null | undefined =
    options.locks === undefined && nativeLocks
      ? { request: async (name, callback) => await nativeLocks.request(name, callback) }
      : options.locks;
  const listeners = new Set<(message: OwnershipMessage) => void>();
  const highestEpoch = new Map<string, number>();
  let lease: OwnerLease | null = null;
  let heartbeat: IntervalHandle | null = null;
  let closed = false;
  let lossReported = false;
  let ownershipLost = options.onOwnershipLost;
  // The causal predecessor of the CURRENTLY held lease, for
  // readMediaIntentForLease (issue #610 audit) — recomputed every time a
  // claim succeeds (see resolvePredecessor/runClaim), cleared whenever
  // ownership is given up. Deliberately in-memory only, never persisted:
  // it only needs to be valid for the lifetime of the specific claim it
  // describes, and is always freshly (re)computed at claim time from
  // durable/live evidence.
  //
  // Deliberately NO "last epoch I myself released for this callId"
  // fallback exists here (removed after a formal audit): the absence of an
  // OwnerLease in storage never proves no one else claimed and released it
  // in between — a second tab can win, change intent, and release again
  // before this tab's own reclaim, leaving storage looking exactly like
  // "nobody touched it since I left". Guessing the predecessor from this
  // tab's own stale memory in that gap would silently resurrect a value a
  // real intermediate owner already superseded. Only two sources ever
  // establish provenance: a live, freshly-validated `predecessorHint`, or
  // an OwnerLease this coordinator itself just observed in storage (even
  // expired). Neither available -> predecessor is unknown, full stop.
  let currentPredecessor: OwnershipClaim | null = null;

  const readLease = () => parseOwnerLease(storage.getItem(LEASE_KEY));
  const writeLease = (next: OwnerLease) => storage.setItem(LEASE_KEY, JSON.stringify(next));
  const stopHeartbeat = () => {
    if (heartbeat === null) return;
    cancelInterval(heartbeat);
    heartbeat = null;
  };
  const loseOwnership = () => {
    if (!lease) return;
    const lost = lease;
    lease = null;
    currentPredecessor = null;
    stopHeartbeat();
    try {
      const stored = readLease();
      if (stored?.tabId === tabId && stored.epoch === lost.epoch) storage.removeItem(LEASE_KEY);
    } catch {
      // Expiry remains the fallback when storage becomes unavailable.
    }
    if (!lossReported) {
      lossReported = true;
      ownershipLost?.(lost);
    }
  };
  const onMessage: EventListener = (event) => {
    const message = parseOwnershipMessage((event as MessageEvent<unknown>).data);
    if (!message || message.tabId === tabId) return;
    // Participation facts (issue #570) never compete for the ownership
    // lease, so they always reach listeners unconditionally — never
    // silently dropped by the epoch-staleness gate below just because a
    // concurrent claim/takeover already advanced this callId's highest
    // known epoch. Ordering for these is judged on (generation, sequence)
    // by the listener, never on epoch.
    if (PARTICIPATION_TYPES.has(message.type)) {
      listeners.forEach((listener) => listener(message));
      return;
    }
    const seen = highestEpoch.get(message.callId) ?? -1;
    if (message.epoch < seen) return;
    highestEpoch.set(message.callId, message.epoch);
    if (
      lease?.callId === message.callId &&
      ["heartbeat", "claim", "takeover", "ack"].includes(message.type)
    ) {
      const competitor = { ...lease, tabId: message.tabId, epoch: message.epoch };
      if (resolveLeaseConflict(lease, competitor).tabId !== tabId) loseOwnership();
    }
    listeners.forEach((listener) => listener(message));
  };
  channel.addEventListener("message", onMessage);

  const runClaim = async (
    callId: string,
    role: OwnerLease["role"],
    afterEpoch?: number,
    predecessorHint?: OwnershipClaim,
  ): Promise<OwnerLease | null> => {
    if (closed || !isBoundedString(callId)) return null;
    try {
      const existing = readLease();
      const existingForCall = existing?.callId === callId ? existing : null;
      if (
        existingForCall &&
        !isLeaseExpired(existingForCall, now()) &&
        existingForCall.tabId !== tabId &&
        (afterEpoch === undefined || afterEpoch < existingForCall.epoch)
      ) {
        return null;
      }
      // issue #610 audit: the causal predecessor for readMediaIntentForLease
      // — see resolvePredecessor's own doc. Never guessed from anything
      // this tab itself remembers about a callId it previously released;
      // only live evidence (a validated hint) or durable evidence
      // (existingForCall, even expired) ever establishes provenance.
      const predecessor = resolvePredecessor(
        existingForCall ? { tabId: existingForCall.tabId, epoch: existingForCall.epoch } : null,
        predecessorHint,
        afterEpoch,
      );
      // release() removes the OwnerLease entirely while media-intent
      // entries (issue #610) survive — so the next epoch for this callId
      // must also floor above the highest ownershipEpoch any media-intent
      // writer has ever persisted, or a reclaim could reuse an epoch a
      // stale write already recorded intent against. Concurrent claims
      // computing the same floor still converge via the existing
      // tabId tie-break in resolveLeaseConflict/compareOwnershipClaims —
      // no new resolution rule needed.
      const floor = Math.max(
        existingForCall?.epoch ?? 0,
        afterEpoch ?? 0,
        maxMediaIntentEpoch(callId),
      );
      if (floor >= Number.MAX_SAFE_INTEGER) return null;
      const candidate: OwnerLease = {
        v: 1,
        callId,
        tabId,
        epoch: floor + 1,
        role,
        expiresAt: now() + leaseMs,
      };
      writeLease(candidate);
      await settle();
      let observed = readLease();
      if (
        !observed ||
        observed.callId !== callId ||
        resolveLeaseConflict(candidate, observed).tabId !== tabId
      ) {
        return null;
      }
      if (observed.tabId !== tabId || observed.epoch !== candidate.epoch) {
        writeLease(candidate);
        await settle();
        observed = readLease();
        if (
          observed?.callId !== callId ||
          observed.tabId !== tabId ||
          observed.epoch !== candidate.epoch
        ) {
          return null;
        }
      }
      lease = candidate;
      currentPredecessor = predecessor;
      lossReported = false;
      highestEpoch.set(callId, candidate.epoch);
      stopHeartbeat();
      heartbeat = scheduleInterval(() => {
        if (!lease || closed) return;
        try {
          const stored = readLease();
          if (
            stored &&
            stored.callId === lease.callId &&
            resolveLeaseConflict(lease, stored).tabId !== tabId
          ) {
            loseOwnership();
            return;
          }
          lease = { ...lease, expiresAt: now() + leaseMs };
          writeLease(lease);
          channel.postMessage({
            v: 1,
            type: "heartbeat",
            callId: lease.callId,
            tabId,
            epoch: lease.epoch,
          } satisfies OwnershipMessage);
        } catch {
          loseOwnership();
        }
      }, heartbeatMs);
      channel.postMessage({
        v: 1,
        type: existingForCall ? "takeover" : "claim",
        callId,
        tabId,
        targetTabId: tabId,
        epoch: candidate.epoch,
      } satisfies OwnershipMessage);
      return candidate;
    } catch {
      return null;
    }
  };

  // Every ParticipationToken any writer has ever persisted for callId — one
  // per (callId, writerId) key, each independently surviving forever (no
  // eviction here — see the PARTICIPATION_KEY_PREFIX comment above). Never
  // filtered/truncated: the caller derives "current" from the full set via
  // compareParticipationTokens, not from this function picking one.
  const readParticipationTokens = (callId: string): ParticipationToken[] => {
    const prefix = participationKeyPrefix(callId);
    const tokens: ParticipationToken[] = [];
    for (let index = 0; index < storage.length; index += 1) {
      const key = storage.key(index);
      if (!key || !key.startsWith(prefix)) continue;
      const writerId = key.slice(prefix.length);
      if (!isBoundedString(writerId)) continue;
      const entry = parseParticipationWriterEntry(storage.getItem(key));
      if (!entry) continue;
      tokens.push({ generation: entry.generation, writerId });
    }
    return tokens;
  };
  const maxParticipationToken = (callId: string): ParticipationToken | null =>
    readParticipationTokens(callId).reduce<ParticipationToken | null>(
      (max, token) => (!max || compareParticipationTokens(token, max) > 0 ? token : max),
      null,
    );

  // The highest ownershipEpoch any writer has ever persisted for callId —
  // the claim-epoch floor's contribution from media-intent (see runClaim).
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

  const runWriteMediaIntent = (
    callId: string,
    captured: OwnerLease,
    intent: MediaIntent,
    phase: MediaIntentPhase,
    options?: WriteMediaIntentOptions,
  ): WriteMediaIntentOutcome => {
    if (closed || !isBoundedString(callId)) return { ok: false, reason: "stale" };
    if (captured.tabId !== tabId) return { ok: false, reason: "stale" };
    if (
      !lease ||
      lease.callId !== callId ||
      lease.tabId !== tabId ||
      lease.epoch !== captured.epoch
    ) {
      return { ok: false, reason: "stale" };
    }
    try {
      const key = mediaIntentKey(callId, tabId);
      // Read-then-write happens synchronously with no `await` in between —
      // localStorage.setItem is synchronous, so no other continuation of
      // THIS tab can interleave here.
      const previous = parseMediaIntentEntry(storage.getItem(key));
      if (previous && captured.epoch < previous.ownershipEpoch) {
        return { ok: false, reason: "stale" };
      }
      // Optimistic-concurrency guard (issue #610): a "confirmed" write
      // meant to settle a specific earlier "pending" write from THIS
      // writer must never land if a newer same-writer operation already
      // advanced the revision — that would silently resurrect a stale
      // operation's value over a newer one's pending/confirmed entry.
      if (
        options?.expectedRevision !== undefined &&
        previous?.revision !== options.expectedRevision
      ) {
        return { ok: false, reason: "stale" };
      }
      const revision =
        previous && previous.ownershipEpoch === captured.epoch ? previous.revision + 1 : 1;
      const entry: MediaIntentEntry = {
        v: 1,
        ownershipEpoch: captured.epoch,
        revision,
        phase,
        microphone: intent.microphone,
        camera: intent.camera,
      };
      storage.setItem(key, JSON.stringify(entry));
      return { ok: true, revision };
    } catch {
      return { ok: false, reason: "storage-error" };
    }
  };

  // Resolves one writer's specific entry to the MediaIntent a caller may
  // apply, or null if that exact (writerId, expectedEpoch) fact isn't (or
  // is no longer) recorded. `expectedEpoch` must match exactly — never
  // "at or below" — so a writer key that has since moved on to some other
  // epoch of its own is never mistaken for the specific historical fact
  // being looked up.
  const resolveMediaIntentAt = (
    callId: string,
    writerId: string,
    expectedEpoch: number,
  ): MediaIntent | null => {
    if (!isBoundedString(writerId)) return null;
    const entry = parseMediaIntentEntry(storage.getItem(mediaIntentKey(callId, writerId)));
    if (!entry || entry.ownershipEpoch !== expectedEpoch) return null;
    // A "pending" entry is evidence the device may already have diverged
    // from whatever booleans it carries — never trusted as a specific
    // preset (issue #610 privacy blocker).
    if (entry.phase === "pending") return { microphone: false, camera: false };
    return { microphone: entry.microphone, camera: entry.camera };
  };

  const runReadMediaIntentForLease = (callId: string): MediaIntent | null => {
    if (!isBoundedString(callId) || !lease || lease.callId !== callId) return null;
    try {
      // 1. This writer's own entry at the CURRENT epoch — a same-epoch
      // reconnect/retry with no new claim() in between.
      const own = resolveMediaIntentAt(callId, tabId, lease.epoch);
      if (own) return own;
      // 2. The specific writer/epoch identified as this lease's causal
      // predecessor at claim time (issue #610 audit — see claim()'s
      // predecessorHint doc and currentPredecessor's own comment). Never a
      // scan across every entry: a same-epoch or otherwise-unproven writer
      // is never substituted in, no matter how it would compare.
      if (currentPredecessor) {
        const predecessorIntent = resolveMediaIntentAt(
          callId,
          currentPredecessor.tabId,
          currentPredecessor.epoch,
        );
        if (predecessorIntent) return predecessorIntent;
      }
      // 3. Predecessor unknown/unprovable — privacy-safe null; the caller
      // degrades to OFF/OFF, never a global-max approximation.
      return null;
    } catch {
      return null;
    }
  };

  const runAllocateParticipationGeneration = (callId: string): ParticipationToken => {
    const fallback: ParticipationToken = { generation: 1, writerId: tabId };
    if (closed || !isBoundedString(callId)) return fallback;
    try {
      const max = maxParticipationToken(callId);
      const token: ParticipationToken = { generation: (max?.generation ?? 0) + 1, writerId: tabId };
      // Writes ONLY this tab's own key — never touches any other writer's
      // key, so a stale max here can never destroy a concurrent writer's
      // own write. See the allocateParticipationGeneration doc comment.
      storage.setItem(
        participationKey(callId, tabId),
        JSON.stringify({ v: 2, generation: token.generation } satisfies ParticipationWriterEntry),
      );
      return token;
    } catch {
      return fallback;
    }
  };

  return {
    tabId,
    claim(callId, role, afterEpoch, predecessorHint) {
      return locks
        ? locks.request(`${CHANNEL_NAME}:${callId}`, () =>
            runClaim(callId, role, afterEpoch, predecessorHint),
          )
        : runClaim(callId, role, afterEpoch, predecessorHint);
    },
    allocateParticipationGeneration(callId) {
      return runAllocateParticipationGeneration(callId);
    },
    getParticipationToken(callId) {
      try {
        return maxParticipationToken(callId);
      } catch {
        return null;
      }
    },
    writeMediaIntent(callId, captured, intent, phase, options) {
      return runWriteMediaIntent(callId, captured, intent, phase, options);
    },
    readMediaIntentForLease(callId) {
      return runReadMediaIntentForLease(callId);
    },
    getLease: () => lease,
    getOwner(callId) {
      try {
        const owner = readLease();
        return owner?.callId === callId && !isLeaseExpired(owner, now()) ? owner : null;
      } catch {
        return null;
      }
    },
    release(callId) {
      if (!lease || lease.callId !== callId) return;
      const released = lease;
      lease = null;
      currentPredecessor = null;
      stopHeartbeat();
      try {
        const stored = readLease();
        if (stored?.tabId === tabId && stored.epoch === released.epoch)
          storage.removeItem(LEASE_KEY);
        channel.postMessage({
          v: 1,
          type: "released",
          callId,
          tabId,
          epoch: released.epoch,
        } satisfies OwnershipMessage);
      } catch {
        // Lease expiry remains the recovery path when storage or channel access fails.
      }
    },
    post(message) {
      if (closed || !parseOwnershipMessage(message)) return;
      try {
        channel.postMessage(message);
      } catch {
        // Coordination failure is handled by lease expiry and the caller's timeout.
      }
    },
    subscribe(listener) {
      if (closed) return () => undefined;
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    onOwnershipLost(listener) {
      ownershipLost = listener;
      return () => {
        if (ownershipLost === listener) ownershipLost = undefined;
      };
    },
    close() {
      if (closed) return;
      if (lease) this.release(lease.callId);
      closed = true;
      listeners.clear();
      channel.removeEventListener("message", onMessage);
      channel.close();
    },
    isClosed: () => closed,
  };
}
