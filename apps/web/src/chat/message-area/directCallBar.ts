/**
 * The direct 1:1 call bar for a conversation view (#673), moved out of
 * ChatMessageArea (issue #834).
 */

import type { ActiveDirectCallBarProps } from "../../calls/ActiveDirectCallBar";
import type { ChatOutletContext } from "../ChatShell";
import type { DMCounterpart } from "../chatTypes";

/**
 * The direct 1:1 call bar for this view, or nothing (#673).
 *
 * Derived from ChatShell's own directCallSession — itself populated only once
 * CallSessionProvider's directPresentationCall authority is non-null (genuinely
 * active, media-connected, locally owned; never merely ringing — IncomingCallPopup
 * keeps owning that surface). Matched against THIS view's own server-resolved
 * counterpart (never the route or a display name) so the bar only ever appears in
 * the exact DM the call belongs to — navigating to a different conversation, or a
 * call belonging to a different DM, never shows it here.
 *
 * A module-level function rather than inline in ChatMessageArea, which the
 * resource call bar already left for the same reason: the component states what
 * it renders, and each bar decides for itself whether it applies.
 */
export function directCallBar(
  kind: "channel" | "dm",
  session: ChatOutletContext["directCallSession"],
  counterpart: DMCounterpart | undefined,
): ActiveDirectCallBarProps | null {
  if (kind !== "dm" || !session || !counterpart || counterpart.userId !== session.peerUserId) {
    return null;
  }
  return {
    title: `${session.callType === "video" ? "Chamada de vídeo" : "Chamada de voz"} — ${counterpart.displayName}`,
    startedAt: session.startedAt,
    peerUserId: session.peerUserId,
    peerName: counterpart.displayName,
    peerAvatarUrl: counterpart.avatarUrl,
    microphoneEnabled: session.microphoneEnabled,
    microphonePending: session.microphonePending,
    onToggleMicrophone: session.onToggleMicrophone,
    onLeave: session.onLeave,
    onOpenFullCall: session.onOpenFullCall,
  };
}
