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
import type { DMConversation } from "./chatTypes";
import { presenceTargetKey } from "./presence";

const emptyOutletContext: ChatOutletContext = { currentUserId: "", channels: [], dms: [] };

function safeDecodeURIComponent(value: string): string {
  try {
    return decodeURIComponent(value);
  } catch {
    return value;
  }
}

/**
 * The conversation's display name, from the sidebar payload the header already
 * has. Falls back to the target's own id rather than to a blank, so a
 * conversation the sidebar has not delivered yet is still identifiable.
 */
function conversationName(
  kind: "channel" | "dm",
  targetId: string,
  ctx: ChatOutletContext,
  activeDM: DMConversation | undefined,
): string {
  if (kind === "channel") {
    return ctx.channels.find((channel) => channel.id === targetId)?.name ?? targetId;
  }
  return activeDM?.name ?? targetId;
}

export interface ConversationTarget {
  ctx: ChatOutletContext;
  targetId: string;
  /** RF-09 deep link: the message the route asks the timeline to reveal. */
  focusMessageId: string;
  /** The sidebar's row for this DM; undefined for a channel or an unknown id. */
  activeDM: DMConversation | undefined;
  resolvedName: string;
  isChannel: boolean;
  /** Set only for a channel: a DM has no channel id to give anything. */
  channelId: string | undefined;
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
  // The sidebar payload already carries the counterpart identity, so the header
  // reads it from the outlet context instead of issuing a per-DM request.
  const activeDM = kind === "dm" ? ctx.dms.find((dm) => dm.id === targetId) : undefined;
  const resolvedName = conversationName(kind, targetId, ctx, activeDM);
  const isChannel = kind === "channel";

  return {
    ctx,
    targetId,
    focusMessageId,
    activeDM,
    resolvedName,
    isChannel,
    channelId: isChannel ? targetId : undefined,
    presenceTarget: targetId ? presenceTargetKey(kind, targetId) : undefined,
    // RF-32 (issue #458): the route's own kind and id — the very pair the
    // composer is keyed by — so an attachment can never be posted to the
    // destination the reader just navigated away from.
    uploadTarget: targetId ? { kind, id: targetId } : null,
    composerPlaceholder: isChannel
      ? `Mensagem para #${resolvedName}…`
      : `Mensagem para ${resolvedName}…`,
  };
}
