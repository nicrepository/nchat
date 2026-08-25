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
import { useConversationDetails } from "./useConversationDetails";

export interface SidebarDetailsTarget {
  /** The panel's own vocabulary: a group and a 1:1 are both DM rows. */
  kind: "channel" | "group" | "direct";
  id: string;
}

interface SidebarDetailsPanelProps {
  target: SidebarDetailsTarget | null;
  currentUserId: string;
  onClose: () => void;
}

export default function SidebarDetailsPanel({
  target,
  currentUserId,
  onClose,
}: SidebarDetailsPanelProps) {
  // The hook accepts null and fetches nothing for it, so this is one hook call
  // whether or not a panel is open — no conditional hook, and no request for a
  // panel nobody asked for.
  const state = useConversationDetails(target ? { kind: target.kind, id: target.id } : null);
  if (!target) return null;
  return (
    <ConversationDetailsPanel
      key={`${target.kind}:${target.id}`}
      kind={target.kind}
      state={state}
      currentUserId={currentUserId}
      latestPin={null}
      onClose={onClose}
    />
  );
}
