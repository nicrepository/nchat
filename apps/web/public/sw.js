/*
 * NChat Service Worker (issue #747, parent #678).
 *
 * Lives in public/ so Vite serves it verbatim at /sw.js in dev and copies it to
 * the root of dist/ for the nginx image — the same URL in both, which is what
 * lets the registration below claim scope "/" without a Service-Worker-Allowed
 * header. It is a *classic* worker script on purpose: module workers are still
 * not supported in Firefox, which is a browser this product supports.
 *
 * Scope of this file: show a notification for a push, and open the app when one
 * is clicked. It does not cache, does not intercept fetch, and does not talk to
 * the API. Registration and the PushSubscription lifecycle live elsewhere
 * (src/notifications/serviceWorkerRegistration.ts and issue #745 respectively).
 *
 * # The push payload is untrusted input
 *
 * It is produced by our own notification-service (see
 * docs/architecture/notification-web-push.md), but it arrives through a third
 * party push service and is decrypted by the browser, so it is validated here
 * as if it were not ours. An unparseable, unversioned or incomplete payload
 * shows nothing at all: there is no default that is safe to invent.
 *
 * # A push is not authorization
 *
 * Nothing here decides what the reader may see. The click opens the app at an
 * internal path and the application asks the server for everything it renders,
 * with the session it already has — so a notification that reached a browser
 * whose access was revoked in the meantime lands on a page that refuses in the
 * ordinary way (issue #475), not on content this worker let through.
 *
 * # Lifecycle
 *
 * There is deliberately no install/activate handler, no skipWaiting() and no
 * clients.claim(). The default lifecycle — a new worker waits until every tab
 * of this origin is gone, then activates — is the one that cannot interrupt a
 * session in progress or swap the worker under a page mid-conversation. The
 * cost is that a tab opened before the first activation is *uncontrolled*, and
 * WindowClient.navigate() is not allowed on those; focusExistingWindow() below
 * treats that as the expected case rather than an error.
 */

/** Payload schema version, mirrored from worker.PushPayloadVersion (#746). */
const PUSH_PAYLOAD_VERSION = 1;

/** Every field version 1 declares as a non-empty string. */
const REQUIRED_PAYLOAD_FIELDS = ["id", "type", "source_type", "source_id", "occurred_at"];

/**
 * Where a click lands.
 *
 * Version 1 carries `source_type` + `source_id` — a message, reaction or call
 * id — and the application has no route that accepts any of them: a message id
 * cannot be resolved to its conversation without an endpoint that does not
 * exist. So the honest destination is the app root, and the reader arrives at
 * the sidebar with the unread conversation marked. A per-conversation deep
 * link needs a conversation reference in the payload, which is a change to the
 * contract in #746 and a new payload version, not something to guess at here.
 */
const APP_PATH = "/chat";

/** Local assets, always. A URL from a payload never becomes an icon. */
const NOTIFICATION_ICON = "/assets/nic-labs-icon.png";
const NOTIFICATION_BADGE = "/assets/favicon.png";

/**
 * Title per event type, from notificationevent.EventType. The set is closed and
 * the copy is written here — version 1 carries no sender and no preview (the
 * outbox stores neither), so there is nothing from the payload to render as
 * text and no way for one to reach the screen.
 *
 * A Map, not an object literal: the key comes from the payload, and a plain
 * object would answer "constructor" or "toString" with something inherited
 * that is not a title at all.
 */
const NOTIFICATION_TITLES = new Map([
  ["mention", "Você foi mencionado no NChat"],
  ["reply", "Responderam sua mensagem no NChat"],
  ["direct_message", "Nova mensagem direta no NChat"],
  ["channel_message", "Nova mensagem em um canal do NChat"],
  ["reaction", "Nova reação na sua mensagem"],
  ["call", "Chamada no NChat"],
]);

/** An event type this build does not know about is still a real notification. */
const FALLBACK_TITLE = "Nova notificação do NChat";

function isValidPushPayload(value) {
  if (typeof value !== "object" || value === null) return false;
  if (value.v !== PUSH_PAYLOAD_VERSION) return false;
  return REQUIRED_PAYLOAD_FIELDS.every(
    (field) => typeof value[field] === "string" && value[field].length > 0,
  );
}

/** The payload, or null when there is no notification to show. Fails closed. */
function readPushPayload(event) {
  if (!event.data) return null;
  let parsed;
  try {
    parsed = event.data.json();
  } catch {
    // Not JSON, or not decryptable into any — nothing to show.
    return null;
  }
  return isValidPushPayload(parsed) ? parsed : null;
}

function notificationOptionsFor(payload) {
  const options = {
    // The notification id, so an at-least-once redelivery of the same
    // notification replaces the one already on screen instead of stacking a
    // second copy (see "Idempotencia" in notification-web-push.md).
    tag: `nchat-notification-${payload.id}`,
    icon: NOTIFICATION_ICON,
    badge: NOTIFICATION_BADGE,
    // Re-read and re-validated on click: a notification can outlive the worker
    // version that created it, so the click handler never trusts this.
    data: { url: APP_PATH },
  };
  // When the event happened, not when the push arrived — the one thing
  // occurred_at is for, and a retried notification can be hours older.
  const occurredAt = Date.parse(payload.occurred_at);
  if (!Number.isNaN(occurredAt)) options.timestamp = occurredAt;
  return options;
}

/**
 * Same rule as src/lib/safeRedirect.ts: internal absolute paths only, never a
 * protocol-relative one. It is restated rather than imported because a classic
 * worker script cannot import from the application bundle; the two must not
 * drift, and the tests for both assert the same cases.
 */
function safeNotificationPath(value) {
  if (typeof value !== "string" || !value.startsWith("/")) return APP_PATH;
  if (value.startsWith("//") || value.startsWith("/\\")) return APP_PATH;
  return value;
}

function isAppWindow(client) {
  return typeof client.url === "string" && client.url.startsWith(`${self.location.origin}/`);
}

function isAlreadyAt(client, path) {
  const clientPath = client.url.slice(self.location.origin.length);
  return clientPath === path || clientPath.startsWith(`${path}/`);
}

async function focusExistingWindow(client, path) {
  if (isAlreadyAt(client, path)) {
    await client.focus();
    return;
  }
  try {
    const navigated = await client.navigate(path);
    await (navigated || client).focus();
  } catch {
    // navigate() is refused for a client this worker does not control, which is
    // every tab opened before the first activation. Focusing still works, and
    // showing the reader the app they already have beats failing the click.
    await client.focus();
  }
}

/** Reuse a window if there is one; open exactly one only when there is not. */
async function openNotificationTarget(path) {
  try {
    const windows = await self.clients.matchAll({ type: "window", includeUncontrolled: true });
    const existing = windows.find(isAppWindow);
    if (existing) {
      await focusExistingWindow(existing, path);
      return;
    }
    await self.clients.openWindow(path);
  } catch {
    // The Clients API refused the whole sequence. There is nothing left to try
    // and nothing to report: waitUntil() must not settle as a rejection, which
    // is an unhandled error in a context with no page to show it in.
  }
}

async function showPushNotification(payload) {
  try {
    await self.registration.showNotification(
      NOTIFICATION_TITLES.get(payload.type) ?? FALLBACK_TITLE,
      notificationOptionsFor(payload),
    );
  } catch {
    // Permission revoked between subscribing and this push, or the platform
    // refused. Same reasoning as above: swallowed, never rethrown into waitUntil.
  }
}

self.addEventListener("push", (event) => {
  const payload = readPushPayload(event);
  if (!payload) return;
  event.waitUntil(showPushNotification(payload));
});

self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  const data = event.notification.data;
  event.waitUntil(openNotificationTarget(safeNotificationPath(data && data.url)));
});
