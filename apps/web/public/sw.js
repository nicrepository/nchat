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

/**
 * The payload versions this worker understands, mirrored from
 * worker.PushPayloadVersionLegacy (#746) and worker.PushPayloadVersion (#870).
 *
 * Both, not one. Version 1 is what a deployment that has not enabled previews
 * still sends, and it is also what a notification already in flight when the
 * server was reconfigured carries — so dropping it would be a regression with
 * no upside. Version 2 is version 1 plus two optional strings.
 *
 * Anything else is refused outright. A version this build has never heard of
 * may mean anything at all, and guessing is how a payload gets rendered under
 * rules that were not written for it.
 */
const SUPPORTED_PAYLOAD_VERSIONS = [1, 2];

/** Every field both versions declare as a non-empty string. */
const REQUIRED_PAYLOAD_FIELDS = ["id", "type", "source_type", "source_id", "occurred_at"];

/**
 * Ceilings for the two strings version 2 may add.
 *
 * The server bounds them far lower (titleMaxRunes / previewMaxRunes in
 * webpush_preview.go) and this is not a second opinion about how long a preview
 * should be — it is the check that makes the field's *shape* part of the
 * contract rather than something this worker trusts. A value past it is dropped
 * and the generic copy takes over, because a payload that disagrees with the
 * contract is a payload whose origin is not established.
 *
 * Counted in UTF-16 code units, which is what String.length gives a worker. The
 * server's limit is in characters and in bytes, so this is looser than both by
 * construction and can only ever reject something already wrong.
 */
const MAX_TITLE_LENGTH = 200;
const MAX_BODY_PREVIEW_LENGTH = 400;

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
 *
 * Version 2 (#870) deliberately did not add one. It carries a conversation's
 * *name*, for the banner, which is text and not a reference: routing on it
 * would mean resolving a display name back to an id in the one place that must
 * make no authorization decision at all.
 */
const APP_PATH = "/chat";

/** Local assets, always. A URL from a payload never becomes an icon. */
const NOTIFICATION_ICON = "/assets/nic-labs-icon.png";
const NOTIFICATION_BADGE = "/assets/favicon.png";

/**
 * Title per event type, from notificationevent.EventType. The set is closed and
 * the copy is written here.
 *
 * It was the *only* title a version 1 payload could produce, and since #870 it
 * is the fallback: what a v1 payload still gets, and what a v2 payload gets
 * whenever the server had nothing it was allowed to say — a message deleted,
 * withheld or no longer readable by this recipient between the outbox row and
 * the claim. One fallback path for all of those, so a reader cannot tell which
 * of them happened, which is the point.
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
  // Issue #825. A reminder is the same urgent message asking again, so the copy
  // says that rather than announcing something new: the recipient has already
  // been told once and is being told that it is still waiting for them.
  ["urgent_reminder", "Mensagem urgente ainda aguarda você no NChat"],
]);

/** An event type this build does not know about is still a real notification. */
const FALLBACK_TITLE = "Nova notificação do NChat";

function isValidPushPayload(value) {
  if (typeof value !== "object" || value === null) return false;
  if (!SUPPORTED_PAYLOAD_VERSIONS.includes(value.v)) return false;
  return REQUIRED_PAYLOAD_FIELDS.every(
    (field) => typeof value[field] === "string" && value[field].length > 0,
  );
}

/**
 * A string with nothing a reader could see in it.
 *
 * Whitespace was the obvious half and `trim()` covered it. The other half is
 * that `\s` does not include the Unicode format category, so a title of a
 * single U+200B — a zero-width space — is a non-empty, non-whitespace string
 * that renders as a blank banner. The server cannot produce one (sanitizeLine
 * in webpush_preview.go drops the whole Cf category), which is exactly why this
 * check belongs here: it is the case that only reaches this worker from a
 * payload whose origin is not established.
 *
 * It says "only invisible characters", not "contains one". A ZWJ emoji sequence
 * carries U+200D and is kept, because the pictographs beside it are not in the
 * class; a combining accent is Mn, not Cf, and is kept; ordinary text of any
 * script is kept. The empty string matches, which is the answer it already had.
 *
 * Unicode property escapes need the `u` flag, supported by every browser that
 * supports the Push API this worker exists for — Chrome 64+, Firefox 78+,
 * Safari 11.1+. This file is served verbatim from public/ and is never
 * transpiled, so that is a runtime requirement and not a build-time one.
 */
const INVISIBLE_TEXT = /^[\s\p{Cf}]*$/u;

/**
 * One optional version 2 string, or "" when there is nothing usable (#870).
 *
 * Absent, empty, invisible, the wrong type or over the ceiling all collapse to
 * the same answer, deliberately: the caller has exactly one fallback to write
 * and no opportunity to treat a malformed field as a present one. Nothing is
 * repaired here — the string is tested, never rewritten, and what comes back is
 * either exactly what arrived or nothing. A field that does not match the
 * contract is not a field with a fixable value, and repairing it would be this
 * worker deciding presentation, which is what #870 moved to the server.
 */
function approvedText(value, maxLength) {
  if (typeof value !== "string") return "";
  if (value.length > maxLength || INVISIBLE_TEXT.test(value)) return "";
  return value;
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

function notificationOptionsFor(payload, v2Title) {
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
  // The preview, when the server approved one (#870). Assigned as `body`, which
  // showNotification renders as text: there is no element here, no innerHTML
  // and no parser, so markup in a message is the characters it is made of and
  // never markup. A payload that carries none leaves the key absent rather than
  // setting an empty string, so the banner is the same one v1 produced.
  if (v2Title) {
    const body = approvedText(payload.body_preview, MAX_BODY_PREVIEW_LENGTH);
    if (body) options.body = body;
  }
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
    // Version defines semantics: v1 ignores both optional fields even if
    // present. In v2 an invalid/missing title also suppresses the body.
    const v2Title = payload.v === 2 ? approvedText(payload.title, MAX_TITLE_LENGTH) : "";
    await self.registration.showNotification(
      v2Title || NOTIFICATION_TITLES.get(payload.type) || FALLBACK_TITLE,
      notificationOptionsFor(payload, v2Title),
    );
  } catch {
    // Permission revoked between subscribing and this push, or the platform
    // refused. Same reasoning as above: swallowed, never rethrown into waitUntil.
  }
}

/**
 * Whether an NChat window can present this event itself right now (issue #862).
 *
 * That is a window that is visible *and* focused — the same test the page
 * applies before it draws a toast (notificationPresentation's
 * isWindowFocused). Such a page presents what it sees arrive: nothing for the
 * conversation that is open, a toast for another one. An OS notification on top
 * of that would be the redundant alert #678 forbids.
 *
 * A window that is visible but not focused cannot: the page draws no toast
 * without focus, and the realtime decision never authorises an OS notification
 * of its own. Suppressing here would leave that reader with at most a chime, so
 * the push is shown. A hidden window is the same case. The worker cannot know
 * which conversation the page shows — the outbox row carries no session — so a
 * focused window is the whole test.
 *
 * A browser that enforces userVisibleOnly does not demand a notification while
 * a tab of the origin is visible. Any failure to ask reads as "no such window":
 * a missed suppression costs a duplicate, a wrong one costs the notification.
 */
async function hasFocusedAppWindow() {
  try {
    const windows = await self.clients.matchAll({ type: "window", includeUncontrolled: true });
    return windows.some(
      (client) =>
        isAppWindow(client) && client.visibilityState === "visible" && client.focused === true,
    );
  } catch {
    return false;
  }
}

async function presentPush(payload) {
  if (await hasFocusedAppWindow()) return;
  await showPushNotification(payload);
}

self.addEventListener("push", (event) => {
  const payload = readPushPayload(event);
  if (!payload) return;
  event.waitUntil(presentPush(payload));
});

self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  const data = event.notification.data;
  event.waitUntil(openNotificationTarget(safeNotificationPath(data && data.url)));
});
