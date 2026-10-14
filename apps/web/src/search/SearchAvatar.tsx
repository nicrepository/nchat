import { useOutletContext } from "react-router";

import type { ChatOutletContext } from "../chat/ChatShell";
import type { DMConversation } from "../chat/chatTypes";
import GroupAvatar from "../chat/GroupAvatar";
import { avatarColorFor } from "../chat/messageDisplay";
import { UserAvatar } from "../chat/UserAvatar";

/**
 * A group's emoji as the sidebar's canonical list holds it (issue #1026).
 *
 * The search payload carries no identity, and asking for one per result would
 * be a request per row. The list is already in memory and kept current by the
 * sidebar's refetch on realtime updates, so the result follows a change of
 * identity without one. A group this reader's list does not hold yet is
 * Automático — never an invented emoji.
 */
function canonicalGroupEmoji(
  dms: readonly DMConversation[] | undefined,
  groupId: string,
): string | undefined {
  return dms?.find((dm) => dm.id === groupId && dm.type === "group")?.avatarEmoji;
}

/**
 * A canonical person avatar, or the group's own identity (issue #1026).
 *
 * A group is drawn by GroupAvatar alone — no colour derived from its id.
 */
export default function SearchAvatar({
  seed,
  name,
  url,
  kind = "person",
}: {
  seed: string;
  name: string;
  url?: string | null;
  kind?: "person" | "group";
}) {
  const outlet = useOutletContext<ChatOutletContext | null>();
  const workspaceId = outlet?.workspaceId ?? "";

  if (kind === "group") {
    return <GroupAvatar name={name} emoji={canonicalGroupEmoji(outlet?.dms, seed)} size="md" />;
  }

  return (
    <span
      className={`global-search__avatar global-search__avatar--${avatarColorFor(seed)}`}
      aria-hidden="true"
    >
      <UserAvatar userId={seed} workspaceId={workspaceId} displayName={name} avatarUrl={url} />
    </span>
  );
}
