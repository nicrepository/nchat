/**
 * The workspace navigation's open/closed state while it is a drawer
 * (issue #467).
 *
 * Everything about *how* the navigation looks at each width is in
 * ChatShell.css. The only thing React has to know is behavioural: whether the
 * drawer is currently modal, because a modal drawer marks the rest of the shell
 * `inert` and answers Escape — and neither of those can be expressed in a
 * stylesheet.
 *
 * What this deliberately does not do: measure anything, listen for `resize`,
 * or hold a copy of the layout. It holds one boolean, and the media query
 * decides whether that boolean means anything right now.
 */

import { useCallback, useState } from "react";

import { useMediaQuery } from "./useMediaQuery";

/**
 * The widths where the navigation is a drawer instead of a column.
 *
 * Mirrors the `drawer` band documented at the top of ChatShell.css. CSS media
 * queries cannot read a custom property, so this is the one value stated in
 * both places; changing the band means changing both, and nothing else.
 */
export const NAV_DRAWER_QUERY = "(max-width: 1023.98px)";

export interface NavDrawer {
  /** Open — meaningful only while the navigation is a drawer. */
  open: boolean;
  /**
   * Open *and* a drawer: the state that makes the rest of the shell inert and
   * puts a backdrop over it.
   */
  modal: boolean;
  toggle: () => void;
  close: () => void;
}

export function useNavDrawer(pathname: string): NavDrawer {
  const isDrawer = useMediaQuery(NAV_DRAWER_QUERY);
  const [open, setOpen] = useState(false);

  // Two structural reasons to close, both handled the way React documents an
  // adjustment to a changing input — during render, guarded, never in an effect
  // that would render the stale value first:
  //
  //  - the route changed: picking a conversation is what the drawer is for, so
  //    its work is done and the conversation is what the user wants to see;
  //  - the viewport left drawer widths: a sidebar that is a column again must
  //    never leave the rest of the shell inert behind it.
  //
  // Neither touches the selected conversation, the sidebar's data, the details
  // panel or any provider. A resize changes this boolean and nothing else, so
  // no subtree is unmounted and the WebSocket is untouched.
  const context = `${isDrawer}|${pathname}`;
  const [lastContext, setLastContext] = useState(context);
  if (lastContext !== context) {
    setLastContext(context);
    setOpen(false);
  }

  const close = useCallback(() => setOpen(false), []);
  const toggle = useCallback(() => setOpen((current) => !current), []);

  return { open, modal: isDrawer && open, toggle, close };
}
