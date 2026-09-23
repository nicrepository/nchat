export interface SidebarConversationTarget {
  kind: "channel" | "dm";
  id: string;
}

/** Returns the neighbouring rendered sidebar row, never a conversation outside that view. */
export function adjacentSidebarConversation(
  conversations: readonly SidebarConversationTarget[],
  current: SidebarConversationTarget,
  direction: -1 | 1,
): SidebarConversationTarget | null {
  const index = conversations.findIndex(
    (conversation) => conversation.kind === current.kind && conversation.id === current.id,
  );
  return index < 0 ? null : (conversations[index + direction] ?? null);
}
