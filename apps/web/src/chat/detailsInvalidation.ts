type Target = { target_type: "channel" | "dm"; target_id: string };
const listeners = new Set<(target: Target) => void>();
// Route-only signals share one connection already owned by the sidebar. Every
// open details host re-reads its authorized projection, including sidebar panels.
export function invalidateConversationDetails(target: Target): void {
  for (const listener of listeners) listener(target);
}
export function listenDetailsInvalidation(listener: (target: Target) => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}
