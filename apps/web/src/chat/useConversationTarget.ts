/**
 * Which conversation is on screen, and everything derived from that identity.
 *
 * Split out of ChatMessageArea (issue #496, CQ follow-up): the route parameter,
 * the focus query, the sidebar row behind it, the name to show, and the four
 * channel-or-DM values that follow from it were a dozen conditionals at the top
 * of a component that then went on to do six other things.
 *
 * The route id is decoded defensively and normalised before use, and re-encoded
 * on every navigate; nothing here grants access to anything, which the server
 * decides on its own for every request the page then makes.
 */

import { useLocation, useOutletContext, useParams } from "react-router";

import type { ChatOutletContext } from "./ChatShell";
import { normalizeChatTargetId } from "./chatTargetId";
import type { DMConversation, MentionTarget } from "./chatTypes";
import type { CodecFormat } from "./tiptapSerializer";
import { presenceTargetKey } from "./presence";
import { noopConversationDrafts } from "./useConversationDrafts";

const emptyOutletContext: ChatOutletContext = {
  currentUserId: "",
  workspaceId: "",
  channels: [],
  dms: [],
  drafts: noopConversationDrafts,
};

function safeDecodeURIComponent(value: string): string {
  try {
    return decodeURIComponent(value);
  } catch {
    return value;
  }
}

/**
 * The sidebar row behind the target, looked up once, and what the header shows
 * of it: the name and, for a channel, its visibility (issue #1024).
 *
 * Issue #475: the name used to fall back to the target's own route id, on the
 * theory that a conversation the sidebar has not delivered yet should still
 * be identifiable. But a conversation absent from the sidebar payload is
 * exactly what a non-member's target looks like, and the route id is a raw
 * UUID/slug — showing it as a title would leak the very identifier the
 * backend's non-enumerating 404 is designed to hide. Falls back to an empty
 * string instead; callers show a neutral placeholder while it is empty.
 */
function resolveConversation(
  kind: "channel" | "dm",
  targetId: string,
  ctx: ChatOutletContext,
): Pick<ConversationTarget, "activeDM" | "resolvedName" | "isPrivateChannel"> {
  if (kind === "channel") {
    const channel = ctx.channels.find((item) => item.id === targetId);
    return {
      activeDM: undefined,
      resolvedName: channel?.name ?? "",
      isPrivateChannel: channel?.type === "private",
    };
  }
  // The sidebar payload already carries the counterpart identity, so the header
  // reads it from the outlet context instead of issuing a per-DM request.
  const activeDM = ctx.dms.find((dm) => dm.id === targetId);
  return { activeDM, resolvedName: activeDM?.name ?? "", isPrivateChannel: false };
}

/** Channels and groups take mentions (and the v3 body that carries them); a 1:1 DM does not. */
function mentionSettings(
  kind: "channel" | "dm",
  targetId: string,
  activeDM: DMConversation | undefined,
): Pick<ConversationTarget, "mentionTarget" | "bodyFormat"> {
  const mentionsEnabled = kind === "channel" || activeDM?.type === "group";
  return {
    mentionTarget: mentionsEnabled && targetId ? { kind, id: targetId } : undefined,
    bodyFormat: mentionsEnabled ? "v3" : "v2",
  };
}

/**
 * Marks a navigation as a request to travel to its `?message=` (issue #896).
 *
 * The message id says *where*; this says *that the reader asked again*. Going
 * to the same message twice leaves the URL identical, so without a mark the
 * timeline cannot tell a second request from a re-render of the first. The
 * mark is merged into the state, never replacing it, so a pending reference
 * the composer holds survives the trip.
 */
export function withMessageJump(state: unknown): Record<string, unknown> {
  const base = typeof state === "object" && state !== null ? state : {};
  return { ...base, messageJump: true };
}

/**
 * The identity of the request behind the current `?message=`: the history
 * entry's own key when that entry was marked by withMessageJump, "" otherwise.
 *
 * "" for an external link and for any navigation that merely keeps the query —
 * sending a message replaces the entry without the mark — so only a reader's
 * explicit request can make the timeline travel to a message it already did.
 */
function messageJumpRequest(state: unknown, key: string): string {
  const marked =
    typeof state === "object" &&
    state !== null &&
    (state as Record<string, unknown>).messageJump === true;
  return marked ? key : "";
}

export interface ConversationTarget {
  ctx: ChatOutletContext;
  targetId: string;
  /** RF-09 deep link: the message the route asks the timeline to reveal. */
  focusMessageId: string;
  /** Which request asked for it; see messageJumpRequest. */
  focusRequest: string;
  /** The sidebar's row for this DM; undefined for a channel or an unknown id. */
  activeDM: DMConversation | undefined;
  resolvedName: string;
  isChannel: boolean;
  /** Issue #1024: the sidebar payload's own visibility, false for a DM or an unknown id. */
  isPrivateChannel: boolean;
  mentionTarget: MentionTarget | undefined;
  bodyFormat: CodecFormat;
  presenceTarget: string | undefined;
  uploadTarget: { kind: "channel" | "dm"; id: string } | null;
  composerPlaceholder: string;
}

export function useConversationTarget(kind: "channel" | "dm"): ConversationTarget {
  const params = useParams<{ id: string }>();
  const location = useLocation();
  const ctx = useOutletContext<ChatOutletContext>() ?? emptyOutletContext;

  const targetId = normalizeChatTargetId(safeDecodeURIComponent(params.id ?? ""));
  const focusMessageId = new URLSearchParams(location.search).get("message") ?? "";
  const conversation = resolveConversation(kind, targetId, ctx);
  const { resolvedName } = conversation;
  const isChannel = kind === "channel";

  return {
    ctx,
    targetId,
    focusMessageId,
    focusRequest: messageJumpRequest(location.state, location.key),
    ...conversation,
    isChannel,
    ...mentionSettings(kind, targetId, conversation.activeDM),
    presenceTarget: targetId ? presenceTargetKey(kind, targetId) : undefined,
    // RF-32 (issue #458): the route's own kind and id — the very pair the
    // composer is keyed by — so an attachment can never be posted to the
    // destination the reader just navigated away from.
    uploadTarget: targetId ? { kind, id: targetId } : null,
    composerPlaceholder: isChannel
      ? `Mensagem para #${resolvedName || "canal"}…`
      : `Mensagem para ${resolvedName || "conversa"}…`,
  };
}
