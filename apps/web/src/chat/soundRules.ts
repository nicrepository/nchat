import { MENTION_TOKEN_RE } from "./richTextMarkers";
import type { SoundNotificationMode } from "./soundPreference";
import type { WSMessagePayload, WSSubscriptionTarget } from "./useChatWebSocket";

export type SoundCategory = "NONE" | "STANDARD" | "MENTION" | "DM";

// MENTION_TOKEN_RE is anchored to the start of a string (used by the renderer to
// walk the body char-by-char). Here we only need "does this token appear
// anywhere", so the same official grammar is reused unanchored and globally
// instead of duplicating the pattern.
const MENTION_TOKEN_GLOBAL_RE = new RegExp(MENTION_TOKEN_RE.source.replace(/^\^/, ""), "gi");

// Plain-text fallback for "@all" typed without going through the composer's
// mention picker (e.g. pasted, or from a client that predates it). A negative
// lookbehind keeps it from matching inside an email-like token
// (foo@allowed.com) or a name that merely starts with "all" (@allison).
const MENTIONS_ALL_TEXT_RE = /(?<![\w@])@all\b/i;

function isMentioned(bodyText: string, currentUserId: string): boolean {
  MENTION_TOKEN_GLOBAL_RE.lastIndex = 0;
  for (const match of bodyText.matchAll(MENTION_TOKEN_GLOBAL_RE)) {
    // The composer's structured mention:all: token — id is a fixed sentinel
    // (ALL_MENTION_ID), so only the type needs checking, not the id.
    if (match[2] === "all") return true;
    if (match[2] === "user" && match[3] === currentUserId) return true;
  }
  return MENTIONS_ALL_TEXT_RE.test(bodyText);
}

export interface ClassifiedSoundEvent {
  /** Priority bucket — DM > MENTION > STANDARD > NONE — drives the active/focus rule. */
  category: SoundCategory;
  /**
   * Whether the current user was actually named (a specific @mention or @all),
   * independent of category — a DM without a mention still has category "DM"
   * but isMentioned false, which "mentions" mode relies on to stay silent.
   */
  isMentioned: boolean;
}

export function classifySoundEvent(
  payload: WSMessagePayload,
  targetType: WSSubscriptionTarget["kind"],
  currentUserId: string,
): ClassifiedSoundEvent {
  if (payload.kind !== "user") return { category: "NONE", isMentioned: false };
  const mentioned = isMentioned(payload.body_text, currentUserId);
  if (targetType === "dm") return { category: "DM", isMentioned: mentioned };
  return { category: mentioned ? "MENTION" : "STANDARD", isMentioned: mentioned };
}

export interface SoundDecisionInput {
  mode: SoundNotificationMode;
  isDuplicate: boolean;
  isOwnMessage: boolean;
  category: SoundCategory;
  isMentioned: boolean;
  isActiveConversation: boolean;
  isWindowFocused: boolean;
}

export function shouldPlayMessageSound(input: SoundDecisionInput): boolean {
  if (input.mode === "off") return false;
  if (input.isDuplicate) return false;
  if (input.isOwnMessage) return false;
  if (input.category === "NONE") return false;
  if (input.mode === "mentions" && !input.isMentioned) return false;
  // "mentions_and_dms": a plain DM (no @mention) is allowed through — that's
  // the entire difference from "mentions" — but a plain channel message
  // (STANDARD) still isn't.
  if (
    input.mode === "mentions_and_dms" &&
    input.category !== "DM" &&
    input.category !== "MENTION"
  ) {
    return false;
  }
  if (input.isActiveConversation) {
    if (!input.isWindowFocused && (input.category === "DM" || input.category === "MENTION")) {
      return true;
    }
    return false;
  }
  return true;
}
