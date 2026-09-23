/**
 * Reloads an open details panel when the conversation it describes is renamed
 * under it.
 *
 * The one reconciliation path for a name, shared by both hosts of
 * ConversationDetailsPanel (issue #893): the panel opened from the
 * conversation header and the one opened from a sidebar row's menu. It used to
 * live inside useConversationDetailsPanel, which only the first of the two
 * uses — so the second never converged, and the inline editor compensated with
 * a reload of its own, giving the first host two reloads for one rename. One
 * primitive, used by both, is what makes each rename cost exactly one refetch.
 *
 * The mechanism is deliberately indirect, and that is the point. A rename
 * lands in the canonical sidebar payload first — through this actor's own
 * refetch after a confirmed 200, or through conversation.updated for everybody
 * else — and the sidebar, the header and the row all read the name from there.
 * The panel does not: it holds its own display_name from GET /details, so
 * without this it would keep showing the old one until it was closed and
 * reopened (issue #527).
 *
 * So the sidebar is only ever the *signal* that the authoritative projection
 * moved. Nothing here copies a name into the panel's state, and the panel
 * answers by re-reading its own projection from the server — which is why one
 * mechanism covers a local rename, a remote one, a reconnect and any other
 * refetch of the canonical list, with no per-origin code and no second cache.
 *
 * Two properties keep it from firing when it should not:
 *
 *  - the name is watched *together with the target's identity*. Switching
 *    conversations also changes the name, but there useConversationDetails is
 *    already loading the new target from its own effect — reloading here too
 *    would abort that request and issue a second one for the same panel. So a
 *    changed identity is left alone, and only a name that moved under the same
 *    conversation triggers a refetch. That also makes A → B → A correct, and
 *    makes A → B with the same display name a no-op rather than a false match,
 *    because identity is compared first;
 *  - the watched name is the *canonical* one, never the one GET /details just
 *    returned. A reload therefore cannot be what triggers the next reload, so
 *    there is no cycle to break.
 *
 * The first render only records; it never reloads, because there is no
 * previous value to have moved away from.
 */

import { useEffect, useRef } from "react";

/**
 * @param key   stable identity of the target, `kind:id`. Never its name.
 * @param name  the canonical name for that target, from the sidebar payload.
 * @param open  whether a panel is actually showing this target.
 * @param reload refetches the details currently displayed.
 */
export function useReloadOnRename(
  key: string,
  name: string,
  open: boolean,
  reload: () => void,
): void {
  const last = useRef({ key, name });
  useEffect(() => {
    const previous = last.current;
    last.current = { key, name };
    if (previous.key !== key) return;
    if (previous.name === name) return;
    if (open) reload();
  }, [key, name, open, reload]);
}
