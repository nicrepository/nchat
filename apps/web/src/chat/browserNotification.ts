/**
 * Native browser Notification for a new message while the tab is in the
 * background — the page-open counterpart to messageSound.ts. No Service
 * Worker, no Push API: `window.Notification` only works while this tab is
 * alive, which is exactly the scope of this module.
 *
 * `Notification.permission` is the single source of truth and is read fresh
 * on every call (same "no caching" choice as soundPreference.ts) — nothing
 * here persists a copy of it. Requesting permission is the caller's
 * responsibility to gate behind an explicit user gesture; this module never
 * calls requestPermission() on its own initiative.
 */

import { MENTION_TOKEN_RE, unescapeRichTextV3 } from "./richTextMarkers";

export type BrowserNotificationPermission = "default" | "granted" | "denied" | "unsupported";

const NOTIFICATION_ICON = "/assets/nic-labs-icon.png";
const PREVIEW_MAX_LENGTH = 140;

// Same technique as soundRules.ts: reuse the canonical (anchored) mention
// grammar unanchored and globally, instead of re-deriving the token pattern,
// to swap each raw token for its readable label in the notification preview.
const MENTION_TOKEN_GLOBAL_RE = new RegExp(MENTION_TOKEN_RE.source.replace(/^\^/, ""), "gi");

function isSecureContext(): boolean {
  try {
    return typeof window !== "undefined" && window.isSecureContext === true;
  } catch {
    return false;
  }
}

function hasNotificationApi(): boolean {
  try {
    return typeof window !== "undefined" && "Notification" in window;
  } catch {
    return false;
  }
}

/**
 * Exposed separately from getBrowserNotificationPermission() so the UI can
 * tell apart the two real causes folded into "unsupported" — a genuinely
 * absent API vs. an insecure origin (http://, not localhost) — and show the
 * right copy for each, even though both report the same permission value.
 */
export function isBrowserNotificationSecureContext(): boolean {
  return isSecureContext();
}

export function getBrowserNotificationPermission(): BrowserNotificationPermission {
  try {
    // Browsers refuse the API outright on an insecure origin — some (Firefox
    // included) report Notification.permission as "denied" for this even
    // though the site was never actually denied by the user, which would
    // otherwise send them down a "change the browser setting" dead end that
    // cannot fix an origin problem. Checked first, before even asking
    // whether the API exists.
    if (!isSecureContext()) return "unsupported";
    if (!hasNotificationApi()) return "unsupported";
    return window.Notification.permission;
  } catch {
    return "unsupported";
  }
}

export async function requestBrowserNotificationPermission(): Promise<BrowserNotificationPermission> {
  if (!isSecureContext() || !hasNotificationApi()) return "unsupported";
  try {
    return await window.Notification.requestPermission();
  } catch {
    // requestPermission() rejected or threw (e.g. blocked by a permissions
    // policy) — report whatever the browser's real current state is rather
    // than guessing "denied".
    return getBrowserNotificationPermission();
  }
}

/** Plain-text preview: mention tokens become their label, never raw markup. */
function buildNotificationPreview(bodyText: string): string {
  MENTION_TOKEN_GLOBAL_RE.lastIndex = 0;
  const withLabels = bodyText.replace(
    MENTION_TOKEN_GLOBAL_RE,
    (_match, label: string) => `@${unescapeRichTextV3(label)}`,
  );
  return withLabels.length > PREVIEW_MAX_LENGTH
    ? `${withLabels.slice(0, PREVIEW_MAX_LENGTH - 1)}…`
    : withLabels;
}

export interface ShowBrowserMessageNotificationInput {
  targetKind: "channel" | "dm";
  targetId: string;
  senderDisplayName: string;
  bodyText: string;
  /** Navigates via the app's own router — never assign location.href directly. */
  onNavigate: (path: string) => void;
}

export interface ShowBrowserMessageNotificationResult {
  shown: boolean;
}

/**
 * Eligibility (own message, duplicate, sound mode, active-conversation/focus)
 * is already decided by the caller via shouldPlayMessageSound — this function
 * only knows how to render and wire up one native notification, never why.
 */
export function showBrowserMessageNotification(
  input: ShowBrowserMessageNotificationInput,
): ShowBrowserMessageNotificationResult {
  if (getBrowserNotificationPermission() !== "granted") return { shown: false };

  let notification: Notification;
  try {
    notification = new window.Notification(input.senderDisplayName || "Nova mensagem", {
      body: buildNotificationPreview(input.bodyText),
      tag: `nchat-message-${input.targetKind}-${input.targetId}`,
      icon: NOTIFICATION_ICON,
    });
  } catch {
    return { shown: false };
  }

  notification.onclick = () => {
    try {
      window.focus();
    } catch {
      // Best-effort — a browser or embedder may refuse programmatic focus.
    }
    try {
      notification.close();
    } catch {
      // Already closed or unsupported — not fatal.
    }
    let path = "/chat";
    try {
      path = `/chat/${input.targetKind}/${encodeURIComponent(input.targetId)}`;
    } catch {
      path = "/chat";
    }
    try {
      input.onNavigate(path);
    } catch {
      try {
        input.onNavigate("/chat");
      } catch {
        // Navigation is unavailable — nothing more can be done from here.
      }
    }
  };

  return { shown: true };
}
