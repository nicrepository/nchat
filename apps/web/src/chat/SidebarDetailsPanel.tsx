/**
 * SidebarDetailsPanel — details for the conversation whose row menu was used
 * (issue #527).
 *
 * The details panel is otherwise owned by ChatMessageArea and scoped to the
 * conversation that is open. The row menu's "Detalhes" must act on *its own*
 * target, which may be a different conversation entirely, and it must not
 * navigate to get there — so the shell hosts a second instance for that target.
 *
 * Deliberately a thin host: it owns no data of its own, calls the same
 * useConversationDetails every other caller does, and renders the same panel
 * component. The one thing it does not carry is the pinned-message selection,
 * which belongs to the conversation you are reading rather than to one you are
 * inspecting from the sidebar.
 */

import ConversationDetailsPanel from "./ConversationDetailsPanel";
import type { ConversationRenameAction } from "./conversationRename";
import { useDirectMessageAccess, type DirectMessageCoordinator } from "./directMessage";
import { useConversationDetails } from "./useConversationDetails";
import { useReloadOnRename } from "./useReloadOnRename";

export interface SidebarDetailsTarget {
  /** The panel's own vocabulary: a group and a 1:1 are both DM rows. */
  kind: "channel" | "group" | "direct";
  id: string;
}

interface SidebarDetailsPanelProps {
  target: SidebarDetailsTarget | null;
  currentUserId: string;
  /**
   * Resolved by the shell for *this* target, which may not be the conversation
   * that is open (issue #893). Absent whenever the target is not renameable by
   * this caller, which is what leaves the panel with no rename affordance.
   */
  onRename?: ConversationRenameAction;
  /**
   * The canonical name of `target`, from the shell's sidebar payload — the
   * same projection the row itself renders (issue #893, CQ-893-02).
   *
   * It is a *signal*, never content: nothing here displays it, and the panel
   * keeps rendering its own display_name from GET /details. When it moves, the
   * authoritative projection this panel holds is stale and the panel refetches
   * it. That is what makes the row-menu panel converge on a rename by anyone —
   * this actor, another client through conversation.updated, or a reconnect
   * that simply refetches the list — with no realtime wiring of its own.
   */
  canonicalName: string;
  /**
   * The session's open-DM coordinator (issue #895), handed down rather than
   * built here. This panel and the conversation's own details panel can be open
   * at once and can address the same person; one registry is what makes the
   * second activation join the request in flight instead of sending another.
   *
   * What this panel *does* own is its lifetime, declared below. The shell has
   * none to lend it: closing this panel, or pointing it at a different
   * conversation, changes nothing about the route, and a reply for the person
   * it used to describe must not navigate once it no longer describes them.
   */
  coordinator: DirectMessageCoordinator;
  onClose: () => void;
}

export default function SidebarDetailsPanel({
  target,
  currentUserId,
  onRename,
  canonicalName,
  coordinator,
  onClose,
}: SidebarDetailsPanelProps) {
  // The hook accepts null and fetches nothing for it, so this is one hook call
  // whether or not a panel is open — no conditional hook, and no request for a
  // panel nobody asked for.
  const state = useConversationDetails(target ? { kind: target.kind, id: target.id } : null);
  // Same primitive the header's host uses, above the early return so the hook
  // order never depends on whether a panel is open. With no target the key is
  // inert and the identity check alone stops it from ever firing.
  useReloadOnRename(
    target ? `${target.kind}:${target.id}` : "",
    canonicalName,
    target !== null,
    state.reload,
  );
  /*
    This panel's own lifetime, as the coordinator sees it (issue #895).

    The key is what the panel is currently describing, so all three ways this
    surface can stop being entitled to a reply — closing, switching to another
    conversation, unmounting — are the same event: a new token, and the old
    one released. "closed" is a lifetime like any other; nothing is opened
    under it, and registering it unconditionally keeps the hook order
    independent of whether a panel is on screen.
  */
  const access = useDirectMessageAccess(
    coordinator,
    target ? `${target.kind}:${target.id}` : "closed",
  );
  if (!target) return null;
  return (
    <ConversationDetailsPanel
      key={`${target.kind}:${target.id}`}
      kind={target.kind}
      state={state}
      currentUserId={currentUserId}
      latestPin={null}
      onRename={onRename}
      openDM={access}
      onClose={onClose}
    />
  );
}
