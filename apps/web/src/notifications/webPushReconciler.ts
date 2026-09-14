import { useEffect, useSyncExternalStore } from "react";

import { ApiRequestError } from "../lib/api";
import { getSessionGeneration, isAuthenticated, onAuthChange } from "../lib/authSession";
import { requestBrowserNotificationPermission } from "../chat/browserNotification";
import {
  deletePushSubscription,
  isPushEndpointConflict,
  listPushSubscriptions,
  registerPushSubscription,
  type PushSubscriptionRecord,
} from "./pushSubscriptionApi";
import {
  createLocalSubscription,
  getLocalSubscription,
  getPushRegistration,
  getWebPushDeviceId,
  readWebPushCredentials,
  readWebPushPermission,
  webPushCapability,
  type WebPushCapability,
} from "./webPushBrowser";

/**
 * Diagnosis and self-repair of this browser's Web Push state (issue #748).
 *
 * The one place that decides what the browser, the Service Worker, the local
 * PushSubscription and the notification-service each believe, whether those
 * beliefs agree, and — only when they do not and the user has already granted
 * permission — what to change so that they do.
 *
 * Three rules shape everything below.
 *
 * *Diagnose, then converge.* A pass reads the whole world before it writes
 * anything, because "a local subscription exists" is not evidence that the
 * backend can reach it: #745 keeps rows a provider has retired, and the browser
 * can rotate an endpoint without telling anyone.
 *
 * *A prompt needs a gesture.* `requestPermission()` is reachable from exactly
 * one exported function, `enableWebPush`, and only while the permission is
 * still `default`. Boot, login, focus and visibilitychange cannot reach it —
 * not by convention, but because reconcile has no call to it at all.
 *
 * *Push is auxiliary.* Nothing here rejects. Every browser failure, every
 * backend outage and every refused subscribe becomes a snapshot describing what
 * went wrong, so a chat that has nothing to do with notifications cannot be
 * taken down by them.
 */

/** Why push is impossible in this client — none of the three is the user's doing. */
export type WebPushUnavailableReason = "unsupported" | "insecure_context" | "not_configured";

export type WebPushPermissionState = "default" | "granted" | "denied";

/** `registering` is a worker that exists but has not activated yet — not a failure. */
export type WebPushWorkerState = "registering" | "active" | "failed";

export type WebPushLocalSubscriptionState = "present" | "absent";

/** What the notification-service says about *this* device: `unavailable` means it could not be asked. */
export type WebPushBackendState =
  | "unknown"
  | "connected"
  | "disconnected"
  | "invalid"
  | "unavailable";

export type WebPushHealth = "reconciling" | "healthy" | "reconnect_required" | "error";

/** Category only. The technical detail stays in the exception that produced it. */
export type WebPushErrorCategory = "browser" | "backend";

/**
 * The state #729 will render, and the only thing this module publishes.
 *
 * Two arms rather than one record of optionals: a browser with no Push API has
 * no permission, no worker and no subscription to describe, and a snapshot that
 * offered fields for them would be inviting a screen to read values that mean
 * nothing. Inside `available`, every axis is always present — `health` is the
 * summary, never a substitute for the axis that explains it.
 *
 * It carries no endpoint, no `p256dh` and no `auth`, and there is no field it
 * could carry them in.
 */
export interface WebPushAvailableSnapshot {
  readonly status: "available";
  readonly permission: WebPushPermissionState;
  readonly worker: WebPushWorkerState;
  readonly subscription: WebPushLocalSubscriptionState;
  readonly backend: WebPushBackendState;
  readonly health: WebPushHealth;
  readonly error: WebPushErrorCategory | null;
}

export type WebPushSnapshot =
  | { readonly status: "unavailable"; readonly reason: WebPushUnavailableReason }
  | WebPushAvailableSnapshot;

/**
 * Shortest gap between two event-driven reconciles.
 *
 * `focus` and `visibilitychange` both fire when a tab comes back to the front,
 * and a person alt-tabbing produces pairs of them. This is a coalescing window,
 * not a schedule: nothing fires on its own when the window expires, so there is
 * no timer to leak and no polling to accidentally build.
 */
const RECONCILE_COALESCE_MS = 2000;

let snapshot: WebPushSnapshot | null = null;
/** The running pass, together with the session it belongs to. */
let inFlight: { pass: Pass; result: Promise<WebPushSnapshot> } | null = null;
let lastTriggerAt = 0;
let stopLifecycle: (() => void) | null = null;
const listeners = new Set<() => void>();

/**
 * The endpoint this page load last registered successfully.
 *
 * Memory only, never storage: it is derived from a capability URL and has no
 * business surviving the page. #745 deliberately does not return the endpoint
 * it holds, so this is what lets a pass tell "the backend already has these
 * bytes" from "the browser rotated the subscription under us" without a second
 * request. Unknown — after a reload, or after the session changed — means the
 * first pass re-registers once, which the contract defines as the same row and
 * the same generation.
 */
let lastRegisteredEndpoint: string | null = null;

function unavailable(reason: WebPushUnavailableReason): WebPushSnapshot {
  return { status: "unavailable", reason };
}

/**
 * Push is possible here, but this browser is not currently reachable — because
 * permission is not granted, the worker is not active, or nobody is signed in.
 * Nothing was mutated to learn this.
 */
function notConnected(
  permission: WebPushPermissionState,
  worker: WebPushWorkerState,
): WebPushAvailableSnapshot {
  return {
    status: "available",
    permission,
    worker,
    subscription: "absent",
    backend: "unknown",
    health: "reconnect_required",
    error: null,
  };
}

/**
 * The snapshot of a browser that may receive push, with `health` *derived*
 * rather than passed in: healthy is exactly "this browser has a subscription
 * and the backend can reach it", so no caller can assemble a snapshot that
 * claims to be fine while an axis says otherwise.
 */
function granted(
  subscription: WebPushLocalSubscriptionState,
  backend: WebPushBackendState,
  error: WebPushErrorCategory | null,
): WebPushAvailableSnapshot {
  return {
    status: "available",
    permission: "granted",
    worker: "active",
    subscription,
    backend,
    health: grantedHealth(subscription, backend, error),
    error,
  };
}

function grantedHealth(
  subscription: WebPushLocalSubscriptionState,
  backend: WebPushBackendState,
  error: WebPushErrorCategory | null,
): WebPushHealth {
  if (error !== null) return "error";
  return subscription === "present" && backend === "connected" ? "healthy" : "reconnect_required";
}

function reconciling(current: WebPushSnapshot): WebPushSnapshot {
  return current.status === "available" ? { ...current, health: "reconciling" } : current;
}

/** A backend failure is the service's; anything else came from the browser's APIs. */
function categoryOf(error: unknown): WebPushErrorCategory {
  return error instanceof ApiRequestError ? "backend" : "browser";
}

function backendStateOf(record: PushSubscriptionRecord | null): WebPushBackendState {
  if (record === null) return "disconnected";
  if (record.status === "active") return "connected";
  if (record.status === "invalid") return "invalid";
  return "disconnected";
}

function ownDevice(records: PushSubscriptionRecord[]): PushSubscriptionRecord | null {
  const deviceId = getWebPushDeviceId();
  return records.find((record) => record.deviceId === deviceId) ?? null;
}

/**
 * Whether an automatic pass has anything to change — the whole repair decision,
 * as one pure function over what was just observed.
 *
 * `disabled` is the one non-active status that is *not* a divergence: #745 uses
 * it for "the owner switched this device off", and re-registering it would mean
 * a person turning notifications off and the next window focus turning them
 * back on. Only an explicit `enableWebPush` undoes it. `invalid` is the
 * opposite — the provider retired the endpoint, and repairing that is the whole
 * point of this module.
 */
export function needsRepair(
  localEndpoint: string | null,
  remote: PushSubscriptionRecord | null,
  registeredEndpoint: string | null,
): boolean {
  if (remote?.status === "disabled") return false;
  if (localEndpoint === null) return true;
  if (remote === null || remote.status !== "active") return true;
  return localEndpoint !== registeredEndpoint;
}

/**
 * What a pass is allowed to do. `diagnose` repairs only a divergence nobody
 * chose; `connect` is a person asking to be reachable, and registers whatever
 * the diagnosis concluded.
 */
type PassIntent = "diagnose" | "connect";

/**
 * One run of the reconciliation, bound to the session it started under.
 *
 * The binding is the point. A pass is a sequence of awaits around a browser and
 * a backend, and a logout or a different login can land in any gap between two
 * of them. Everything a pass does after that gap would be done on behalf of an
 * identity that no longer exists — registering a browser against an account
 * that just signed out, or writing back state the new session had already
 * invalidated.
 */
interface Pass {
  readonly intent: PassIntent;
  readonly generation: number;
}

/**
 * Signals that the session a pass belonged to ended while it was running.
 *
 * Not a failure, and never reported as one: it is how a pass stops without
 * pretending to have an answer about a session it can no longer describe.
 */
class SessionEnded extends Error {
  constructor() {
    super("web push pass outlived its session");
    this.name = "SessionEnded";
  }
}

/**
 * Stops the pass unless it still belongs to the session that started it.
 *
 * Called immediately before every session-sensitive effect, and again after
 * every await that precedes one — a check that happened before an await
 * describes a session that may already be gone by the time the effect runs.
 */
function requireCurrentSession(generation: number): void {
  if (generation !== getSessionGeneration()) throw new SessionEnded();
}

/**
 * Settled marker for the queue below. Never inspects the outcome: the queue
 * orders operations, it does not care whether they succeeded.
 */
const settled = () => undefined;

/**
 * Tail of the browser mutations queued so far. Never rejects.
 */
let browserMutationTail: Promise<void> = Promise.resolve();

/**
 * Runs one browser mutation at a time, across the whole page.
 *
 * A session generation cannot do this job. There is one PushManager per
 * browser, not one per session, so two passes that legitimately belong to
 * *different* sessions are still two callers of the same object — and a guard
 * that only tells a pass it went stale can only say so once the `subscribe()`
 * it already started comes back.
 *
 * The queue is a queue, not a lock on success: the next operation waits for
 * this one to *settle*, so a rejected `subscribe()` orders the ones behind it
 * instead of poisoning them, and the rejection still reaches its own caller
 * untouched.
 */
function serializeBrowserMutation<T>(operation: () => Promise<T>): Promise<T> {
  const result = browserMutationTail.then(operation);
  browserMutationTail = result.then(settled, settled);
  return result;
}

/**
 * The subscription this browser holds, minting one only if it still has none.
 *
 * The read is deliberately repeated *inside* the serialized section. The
 * diagnosis that concluded "absent" happened before the wait, and whoever held
 * the section in the meantime may have created exactly the subscription this
 * pass was about to mint — serializing alone would turn two parallel
 * `subscribe()` calls into two sequential ones, which is the same bug slowed
 * down. Re-reading is what makes it one.
 */
function getOrCreateLocalSubscription(
  registration: ServiceWorkerRegistration,
  current: Pass,
): Promise<PushSubscription> {
  return serializeBrowserMutation(async () => {
    const existing = await getLocalSubscription(registration);
    if (existing !== null) return existing;
    // Waiting for the section is another gap the session can change in, and a
    // pass that lost its session must not leave a subscription behind it.
    requireCurrentSession(current.generation);
    return createLocalSubscription(registration);
  });
}

// ── The pass ────────────────────────────────────────────────────────────────

/**
 * Reads the world, decides, and converges. Returns the snapshot; committing it
 * is the runner's business, so a session that ended mid-pass cannot be
 * described by an answer computed for the one before it.
 */
async function computeSnapshot(current: Pass): Promise<WebPushSnapshot> {
  const capability: WebPushCapability = webPushCapability();
  if (capability !== "supported") return unavailable(capability);

  const permission = readWebPushPermission();
  if (permission === "unsupported") return unavailable("unsupported");

  const registration = await getPushRegistration();
  if (registration === null) return notConnected(permission, "failed");
  // A worker that has registered but not activated yet cannot carry a
  // subscription. It is not an error and it is not worth waiting on: the next
  // focus finds it active, and no timer had to exist for that to happen.
  if (registration.active === null) return notConnected(permission, "registering");

  // Everything past here mutates. Neither condition is ever repaired from here:
  // a permission is the user's to give, and an anonymous page has no account to
  // register a browser against.
  if (permission !== "granted") return notConnected(permission, "active");
  if (!isAuthenticated()) return notConnected(permission, "active");

  return convergeGranted(registration, current);
}

async function convergeGranted(
  registration: ServiceWorkerRegistration,
  current: Pass,
): Promise<WebPushSnapshot> {
  let local: WebPushLocalSubscriptionState = "absent";
  let backend: WebPushBackendState = "unknown";
  try {
    let subscription = await getLocalSubscription(registration);
    local = subscription === null ? "absent" : "present";

    const remote = ownDevice(await listPushSubscriptions());
    backend = backendStateOf(remote);

    if (
      current.intent === "diagnose" &&
      !needsRepair(subscription?.endpoint ?? null, remote, lastRegisteredEndpoint)
    ) {
      return granted(local, backend, null);
    }

    // The diagnosis above is the previous session's reading of the world if the
    // account changed while it was being read. Minting a subscription from it
    // would register this browser for whoever happens to be signed in now.
    requireCurrentSession(current.generation);
    subscription ??= await getOrCreateLocalSubscription(registration, current);
    local = "present";
    backend = backendStateOf(await registerLocalSubscription(registration, subscription, current));
    return granted(local, backend, null);
  } catch (error) {
    // A pass that outlived its session has no snapshot to offer and must not be
    // dressed up as a failure of push.
    if (error instanceof SessionEnded) throw error;
    // Whatever was already learned stays in the snapshot: "we know the backend
    // was unreachable" is more useful to a screen than a blanked-out state.
    return granted(local, failedBackend(backend, error), categoryOf(error));
  }
}

function failedBackend(backend: WebPushBackendState, error: unknown): WebPushBackendState {
  return categoryOf(error) === "backend" ? "unavailable" : backend;
}

/**
 * Presents a subscription to #745, recovering once from the one conflict the
 * contract defines a recovery for.
 */
async function registerLocalSubscription(
  registration: ServiceWorkerRegistration,
  subscription: PushSubscription,
  current: Pass,
): Promise<PushSubscriptionRecord> {
  try {
    return await sendSubscription(subscription, current);
  } catch (error) {
    if (!isPushEndpointConflict(error)) throw error;
  }
  // The endpoint belongs to someone else — the same browser profile signed in
  // as a different person, typically. Cancelling it mints a new one, which
  // nobody owns. Exactly one retry: a second conflict is a real failure.
  //
  // Cancelling is itself a mutation, and the conflict took a round trip to
  // learn about, so the session is confirmed again before this browser loses a
  // subscription on behalf of a pass that may no longer own it.
  requireCurrentSession(current.generation);
  // Cancelling and minting are one mutation of one PushManager: split, they
  // leave a window in which another pass reads "absent" and mints a second
  // subscription of its own.
  const replacement = await serializeBrowserMutation(async () => {
    await subscription.unsubscribe();
    return createLocalSubscription(registration);
  });
  return sendSubscription(replacement, current);
}

async function sendSubscription(
  subscription: PushSubscription,
  current: Pass,
): Promise<PushSubscriptionRecord> {
  const credentials = readWebPushCredentials(subscription);
  if (credentials === null) {
    throw new TypeError("web push subscription carries no keys");
  }
  // Between minting a subscription and presenting it there is another await,
  // and the bearer token this request carries is read when it is sent — so an
  // unchecked pass would register the previous session's diagnosis under the
  // current session's identity.
  requireCurrentSession(current.generation);
  const record = await registerPushSubscription({
    deviceId: getWebPushDeviceId(),
    ...credentials,
  });
  // The request itself is a gap the session can change in. `lastRegisteredEndpoint`
  // is what tells a later pass "the backend already has these bytes", and the
  // auth listener clears it precisely so the next session re-presents; restoring
  // it here would let session B read session A's registration as its own and
  // report a stale backend as healthy.
  requireCurrentSession(current.generation);
  lastRegisteredEndpoint = credentials.endpoint;
  return record;
}

// ── Store ───────────────────────────────────────────────────────────────────

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

function commit(generation: number, next: WebPushSnapshot): WebPushSnapshot {
  // A pass that started under another session describes a browser/account pair
  // that is no longer the current one. The caller still gets what it computed;
  // the store does not adopt it.
  if (generation !== getSessionGeneration()) return next;
  snapshot = next;
  for (const listener of listeners) listener();
  return next;
}

/** The current state, without asking the browser anything. */
export function getWebPushSnapshot(): WebPushSnapshot {
  // Computed once, on first read, so a consumer that mounts before the first
  // pass sees the real capability rather than an invented one. `reconciling`
  // is the honest summary: the axes under it are not settled yet.
  snapshot ??= initialSnapshot();
  return snapshot;
}

function initialSnapshot(): WebPushSnapshot {
  const capability = webPushCapability();
  if (capability !== "supported") return unavailable(capability);
  const permission = readWebPushPermission();
  if (permission === "unsupported") return unavailable("unsupported");
  return { ...notConnected(permission, "registering"), health: "reconciling" };
}

// ── Public API ──────────────────────────────────────────────────────────────

/**
 * Diagnoses and repairs, coalescing concurrent callers onto one pass.
 *
 * Two simultaneous calls are one pass and therefore one `subscribe()` and one
 * registration — the client does not lean on the backend's idempotence to cover
 * a race it created itself. Never rejects.
 */
export function reconcileWebPush(): Promise<WebPushSnapshot> {
  return startPass("diagnose");
}

/**
 * Coalescing is *per session*.
 *
 * Two callers of the same session share one pass, so a race the client created
 * never becomes two subscriptions. A caller of a different session shares
 * nothing: the running pass read the world under an identity that is not this
 * caller's, and answering with its result — or waiting for it — would hand the
 * new session a diagnosis of the old one.
 */
function startPass(intent: PassIntent): Promise<WebPushSnapshot> {
  const generation = getSessionGeneration();
  if (inFlight !== null && inFlight.pass.generation === generation) return inFlight.result;

  const current: Pass = { intent, generation };
  const entry = { pass: current, result: runPass(current) };
  inFlight = entry;
  return entry.result;
}

async function runPass(current: Pass): Promise<WebPushSnapshot> {
  commit(current.generation, reconciling(getWebPushSnapshot()));
  try {
    return commit(current.generation, await computeSnapshot(current));
  } catch {
    // The session this pass belonged to ended: it published nothing, changed
    // nothing, and the pass the new session starts is the one that answers.
    // Anything else that reached here is still not allowed to reject — push is
    // auxiliary, and convergeGranted has already turned every real failure into
    // a snapshot of its own.
    return getWebPushSnapshot();
  } finally {
    // Only the pass that installed the entry may clear it: a newer session's
    // pass can already be the one running.
    if (inFlight?.pass === current) inFlight = null;
  }
}

/**
 * Runs a pass that is guaranteed to start *after* the caller's change.
 *
 * A pass already in flight read the permission, the session or the subscription
 * before that change existed, so sharing it would answer with a state the
 * caller has just invalidated.
 */
async function reconcileAfterChange(intent: PassIntent): Promise<WebPushSnapshot> {
  await inFlight?.result;
  return startPass(intent);
}

/**
 * The only path to a permission prompt, and it must be called from a user
 * gesture.
 *
 * The prompt is reachable only while the permission is `default`: `granted`
 * needs no prompt and `denied` cannot be undone by one — a browser refuses to
 * ask again, and asking is how a site earns a permanent block. Both fall
 * straight through to a reconcile, which repairs what it can and reports the
 * rest.
 */
export async function enableWebPush(): Promise<WebPushSnapshot> {
  if (webPushCapability() === "supported" && readWebPushPermission() === "default") {
    await requestBrowserNotificationPermission();
  }
  return reconcileAfterChange("connect");
}

/**
 * Switches this browser off: cancels the local subscription and disables the
 * backend row for this device. Both are idempotent, and neither can remove
 * anyone else's — #745 resolves ownership from the session row.
 */
export async function disableWebPush(): Promise<WebPushSnapshot> {
  await inFlight?.result;
  const generation = getSessionGeneration();
  const failure = await removeOwnSubscription(generation);
  lastRegisteredEndpoint = null;
  const next = await reconcileAfterChange("diagnose");
  if (failure === null || next.status !== "available") return next;
  return commit(generation, { ...next, health: "error", error: failure });
}

/** Returns the category that failed, or null. Never rejects. */
async function removeOwnSubscription(generation: number): Promise<WebPushErrorCategory | null> {
  try {
    const registration = await getPushRegistration();
    const subscription = registration === null ? null : await getLocalSubscription(registration);
    // Switching a browser off is a mutation on behalf of one account. After a
    // session change it would be the wrong account's, on both sides.
    requireCurrentSession(generation);
    await subscription?.unsubscribe();
    if (!isAuthenticated()) return null;
    const remote = ownDevice(await listPushSubscriptions());
    requireCurrentSession(generation);
    if (remote !== null) await deletePushSubscription(remote.id);
    return null;
  } catch (error) {
    // Nothing was attempted after the session ended, so there is no failure to
    // report — the new session's own reconcile describes what is true now.
    return error instanceof SessionEnded ? null : categoryOf(error);
  }
}

/**
 * Starts the one set of global listeners, and returns the function that stops
 * them.
 *
 * Called from `main.tsx` rather than a component: the state it maintains
 * belongs to the page, not to a tree that can unmount. A second call while the
 * first is still active is a no-op that hands back the same stopper, so a
 * StrictMode double-invoke or a remount cannot end up with two sets.
 */
export function startWebPushLifecycle(): () => void {
  if (stopLifecycle !== null) return stopLifecycle;

  const onFocus = () => triggerReconcile();
  const onVisibilityChange = () => {
    // Only coming back matters. Leaving changes nothing about this browser's
    // push state, and reconciling on the way out would double every switch.
    if (document.visibilityState === "visible") triggerReconcile();
  };
  // A session change is a change, not an event to coalesce: the account a
  // browser is registered against just became a different one.
  const offAuthChange = onAuthChange(() => {
    lastRegisteredEndpoint = null;
    lastTriggerAt = Date.now();
    void reconcileAfterChange("diagnose");
  });

  window.addEventListener("focus", onFocus);
  document.addEventListener("visibilitychange", onVisibilityChange);
  triggerReconcile();

  stopLifecycle = () => {
    window.removeEventListener("focus", onFocus);
    document.removeEventListener("visibilitychange", onVisibilityChange);
    offAuthChange();
    stopLifecycle = null;
  };
  return stopLifecycle;
}

function triggerReconcile(): void {
  const now = Date.now();
  if (now - lastTriggerAt < RECONCILE_COALESCE_MS) return;
  lastTriggerAt = now;
  // reconcileWebPush resolves a snapshot for every failure it can have, so
  // there is no rejection here for anything to leave unhandled.
  void reconcileWebPush();
}

/**
 * This browser's push health, for the screens that configure it (#729).
 *
 * The mount refresh is coalesced with the lifecycle's own events, so opening
 * the settings screen twice in a row is one pass, and StrictMode's double
 * effect is not two.
 */
export function useWebPushHealth(): WebPushSnapshot {
  useEffect(() => {
    triggerReconcile();
  }, []);
  return useSyncExternalStore(subscribe, getWebPushSnapshot);
}

/** Drops every piece of module state. For test isolation only. */
export function _resetWebPushState(): void {
  stopLifecycle?.();
  snapshot = null;
  inFlight = null;
  browserMutationTail = Promise.resolve();
  lastTriggerAt = 0;
  lastRegisteredEndpoint = null;
  listeners.clear();
}
