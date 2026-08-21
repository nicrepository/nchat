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
  claim(callId: string, role: OwnerLease["role"], afterEpoch?: number): Promise<OwnerLease | null>;
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

export function isLeaseExpired(lease: Pick<OwnerLease, "expiresAt">, now: number): boolean {
  return lease.expiresAt <= now;
}

export function resolveLeaseConflict(a: OwnerLease, b: OwnerLease): OwnerLease {
  if (a.epoch !== b.epoch) return a.epoch > b.epoch ? a : b;
  return a.tabId.localeCompare(b.tabId) <= 0 ? a : b;
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
      const candidate: OwnerLease = {
        v: 1,
        callId,
        tabId,
        epoch: Math.max(existingForCall?.epoch ?? 0, afterEpoch ?? 0) + 1,
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
    claim(callId, role, afterEpoch) {
      return locks
        ? locks.request(`${CHANNEL_NAME}:${callId}`, () => runClaim(callId, role, afterEpoch))
        : runClaim(callId, role, afterEpoch);
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
