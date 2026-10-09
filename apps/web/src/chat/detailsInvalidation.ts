type Target = { target_type: "channel" | "dm"; target_id: string };
const listeners = new Set<(target: Target | null) => void>();
// Route-only signals share one connection already owned by the sidebar. Every
// open details host re-reads its authorized projection, including sidebar panels.
export function invalidateConversationDetails(target: Target): void {
  for (const listener of listeners) listener(target);
}
// Subscription recovery revalidates every open panel, including sidebar panels
// whose target differs from the active route. No signal persists domain state.
export function invalidateOpenConversationDetails(): void {
  for (const listener of listeners) listener(null);
}
export function listenDetailsInvalidation(listener: (target: Target | null) => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}
