/**
 * The one place the NChat Service Worker is registered (issue #747).
 *
 * Centralised for two reasons. The browser keys a registration by (script URL,
 * scope), so a second call with the same pair is already idempotent on its
 * side — but two call sites would still be two places that could disagree
 * about the script URL or the scope, and a conflicting scope is a *second*
 * registration, not an update of the first. And a React component that
 * registered on mount would tie a worker whose whole point is running with no
 * page open to the lifetime of a component tree.
 *
 * So: called once from main.tsx, before React renders, and never from a
 * component. The in-flight promise is memoised so concurrent callers and a
 * StrictMode double-invoke share a single register() call.
 *
 * Nothing here can break the application. Every unsupported browser, insecure
 * origin and registration failure resolves to null; the chat does not depend
 * on the worker existing, and a failure to register must not stop it loading.
 */

/** Served from apps/web/public/sw.js — the root URL is what makes scope "/" legal. */
const SERVICE_WORKER_URL = "/sw.js";

/** Root scope: notifications belong to the whole app, not to one route. */
const SERVICE_WORKER_SCOPE = "/";

let pendingRegistration: Promise<ServiceWorkerRegistration | null> | null = null;

/**
 * A Service Worker needs a secure context, and Push needs PushManager. Both are
 * checked before touching either API so an old browser or an http:// origin
 * degrades to "no push" instead of throwing during bootstrap — the same
 * distinction browserNotification.ts already draws for the Notification API.
 */
function isServiceWorkerSupported(): boolean {
  try {
    return (
      typeof window !== "undefined" &&
      window.isSecureContext === true &&
      "serviceWorker" in navigator &&
      "PushManager" in window
    );
  } catch {
    return false;
  }
}

async function register(): Promise<ServiceWorkerRegistration | null> {
  if (!isServiceWorkerSupported()) return null;
  try {
    return await navigator.serviceWorker.register(SERVICE_WORKER_URL, {
      scope: SERVICE_WORKER_SCOPE,
    });
  } catch (error) {
    // Diagnostic only, and deliberately the whole of it: no payload, no
    // subscription, no session — a registration error carries none of those.
    console.warn("[nchat] service worker registration failed", error);
    return null;
  }
}

/**
 * Registers the worker, or reports null when this browser cannot have one.
 *
 * Repeat calls return the same promise and never produce a second registration.
 * A failed attempt stays memoised: retrying on every call would turn a browser
 * that refuses the worker into a loop, and there is nothing a retry could
 * change within one page load.
 */
export function registerNotificationServiceWorker(): Promise<ServiceWorkerRegistration | null> {
  pendingRegistration ??= register();
  return pendingRegistration;
}
