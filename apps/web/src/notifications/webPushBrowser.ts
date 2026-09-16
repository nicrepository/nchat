import {
  getBrowserNotificationPermission,
  isBrowserNotificationSecureContext,
} from "../chat/browserNotification";
import { randomId } from "../lib/randomId";
import { registerNotificationServiceWorker } from "./serviceWorkerRegistration";

/**
 * The seam between the browser's push APIs and the rest of this feature
 * (issue #748).
 *
 * Every call to `navigator.serviceWorker`, `registration.pushManager` and
 * `PushSubscription` in the application lives here, so the reconciler is
 * orchestration over values it can be handed in a test, and no React component
 * ever reaches an API whose answer depends on the browser's mood.
 *
 * The Service Worker itself is still registered in exactly one place — #747's
 * `registerNotificationServiceWorker`, called through, never reimplemented.
 */

/**
 * Whether this browser can do Web Push at all, and if not, why.
 *
 * Whether the *deployment* delivers push is not a browser fact: it is the
 * notification-service's answer (`fetchPushVapidPublicKey`), because only the
 * process holding the private key knows which public key it signs with.
 */
export type WebPushCapability = "supported" | "unsupported" | "insecure_context";

/** `unsupported` here means the Notification API itself is absent or unreadable. */
export type WebPushPermission = "default" | "granted" | "denied" | "unsupported";

/** localStorage key holding this browser's device identity. Not a secret, and not a credential. */
const DEVICE_ID_KEY = "nchat.notifications.push.deviceId";

/** #745's `device_id` grammar. A stored value that does not match is not sent. */
const DEVICE_ID_RE = /^[A-Za-z0-9_.:-]{1,128}$/;

let memoryDeviceId: string | null = null;

/**
 * Whether push can work here at all, and if not, which of the two reasons.
 *
 * `registerNotificationServiceWorker` folds an insecure origin in with an
 * absent API, which is the right call for "should I register a worker"; a
 * person being told why their notifications are off needs the two apart.
 */
export function webPushCapability(): WebPushCapability {
  if (!isBrowserNotificationSecureContext()) return "insecure_context";
  if (!hasPushApis()) return "unsupported";
  return "supported";
}

function hasPushApis(): boolean {
  try {
    return (
      typeof window !== "undefined" &&
      "Notification" in window &&
      "PushManager" in window &&
      "serviceWorker" in navigator
    );
  } catch {
    return false;
  }
}

/** The browser's current answer, read fresh — nothing here caches a permission. */
export function readWebPushPermission(): WebPushPermission {
  return getBrowserNotificationPermission();
}

/**
 * A stable identifier for this browser profile.
 *
 * #745 keys a subscription by (workspace, user, device), so without a device
 * identity that survives a reload every page load would register a second row
 * for the same browser. It is a random opaque value, carries no authority and
 * grants nothing: the server takes the user from the session row and never from
 * anything the client sends.
 *
 * A browser that refuses storage keeps it in memory for the life of the page,
 * which is the most that can be promised there.
 */
export function getWebPushDeviceId(): string {
  if (memoryDeviceId !== null) return memoryDeviceId;
  memoryDeviceId = storedDeviceId() ?? createDeviceId();
  return memoryDeviceId;
}

function storedDeviceId(): string | null {
  try {
    const stored = localStorage.getItem(DEVICE_ID_KEY);
    // Local storage is not trusted input: a value that could not have come from
    // here would be rejected by the server anyway, so it is replaced instead.
    return stored !== null && DEVICE_ID_RE.test(stored) ? stored : null;
  } catch {
    return null;
  }
}

function createDeviceId(): string {
  const created = randomId();
  try {
    localStorage.setItem(DEVICE_ID_KEY, created);
  } catch {
    // Storage unavailable (private mode, blocked cookies): memory it is.
  }
  return created;
}

/** Drops the memoised device id. For test isolation only. */
export function _resetWebPushDeviceId(): void {
  memoryDeviceId = null;
}

/** The registration #747 owns, or null when this browser will not have one. */
export function getPushRegistration(): Promise<ServiceWorkerRegistration | null> {
  return registerNotificationServiceWorker();
}

/** Whatever subscription the *effective* registration currently holds. */
export function getLocalSubscription(
  registration: ServiceWorkerRegistration,
): Promise<PushSubscription | null> {
  return registration.pushManager.getSubscription();
}

/**
 * Creates one. `userVisibleOnly` is mandatory on Chrome and is also the honest
 * declaration: every push this application sends becomes a notification.
 *
 * `applicationServerKey` is the base64url VAPID public key, which
 * `pushManager.subscribe` accepts directly as a string.
 */
export function createLocalSubscription(
  registration: ServiceWorkerRegistration,
  applicationServerKey: string,
): Promise<PushSubscription> {
  return registration.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey });
}

/**
 * Whether a subscription was created for this VAPID public key.
 *
 * A subscription is bound to the key it was minted with. After the deployment
 * rotates its pair, a subscription minted for the old one still exists and
 * still registers, and every push to it is refused by the push service — a
 * browser that looks connected and is not. A browser that does not expose the
 * key it subscribed with gets the benefit of the doubt: re-subscribing on every
 * pass would be worse than the rotation it tries to detect.
 */
export function subscribedWithKey(
  subscription: PushSubscription,
  applicationServerKey: string,
): boolean {
  const current = subscription.options?.applicationServerKey;
  if (!current) return true;
  const expected = decodeBase64Url(applicationServerKey);
  const actual = new Uint8Array(current);
  return expected !== null && sameBytes(actual, expected);
}

function decodeBase64Url(value: string): Uint8Array | null {
  try {
    const base64 = value.replace(/-/g, "+").replace(/_/g, "/");
    return Uint8Array.from(atob(base64), (char) => char.charCodeAt(0));
  } catch {
    return null;
  }
}

function sameBytes(left: Uint8Array, right: Uint8Array): boolean {
  return left.length === right.length && left.every((byte, index) => byte === right[index]);
}

/** The endpoint and keys, exactly as #745 wants them. */
export interface WebPushCredentials {
  endpoint: string;
  p256dh: string;
  auth: string;
}

/**
 * Reads the credentials out of a subscription.
 *
 * The only place in the client that touches `p256dh` and `auth`, and its result
 * has exactly one destination: the POST body. It never reaches a snapshot, a
 * component, a log or Web Storage.
 */
export function readWebPushCredentials(subscription: PushSubscription): WebPushCredentials | null {
  const json = subscription.toJSON();
  const endpoint = json.endpoint;
  const p256dh = json.keys?.p256dh;
  const auth = json.keys?.auth;
  if (!endpoint || !p256dh || !auth) return null;
  return { endpoint, p256dh, auth };
}
