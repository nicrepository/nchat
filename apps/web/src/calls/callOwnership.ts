import { randomId } from "../lib/randomId";

const CHANNEL_NAME = "nchat-call-ownership-v1";
const LEASE_KEY = "nchat.call.owner.v1";
const MESSAGE_TYPES = new Set([
  "ready",
  "heartbeat",
  "released",
  "ack",
  "failure",
  "ended",
  "handoff",
  "claim",
  "takeover",
]);
const TARGETED_TYPES = new Set(["handoff", "claim", "takeover"]);

export interface OwnerLease {
  v: 1;
  callId: string;
  tabId: string;
  epoch: number;
  role: "main" | "dedicated";
  expiresAt: number;
}

type OwnershipMessageType = "ready" | "heartbeat" | "released" | "ack" | "failure" | "ended";

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
  if (Object.keys(record).length !== (targeted ? 6 : 5)) return null;
  if (targeted && !isBoundedString(record.targetTabId)) return null;
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

  return {
    tabId,
    claim(callId, role, afterEpoch) {
      return locks
        ? locks.request(`${CHANNEL_NAME}:${callId}`, () => runClaim(callId, role, afterEpoch))
        : runClaim(callId, role, afterEpoch);
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
