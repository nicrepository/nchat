/**
 * Live `prefers-reduced-motion: reduce` state (issue #491).
 *
 * Every other reduced-motion rule in this app is a CSS media query, which is
 * enough for a CSS animation but not for this feature: nothing in CSS can stop
 * a native `<img>` GIF from animating. Knowing the preference in JS is what
 * lets the image preview choose the server's static frame instead of fetching
 * the animated original.
 *
 * Subscribed rather than read once, so a user who changes the OS setting while
 * a conversation is open sees the preview react without a reload — the
 * subscription itself is useMediaQuery's, shared with the layout query added by
 * issue #467 so there is one `matchMedia` lifecycle in the chat scope.
 */

import { useMediaQuery } from "./useMediaQuery";

const QUERY = "(prefers-reduced-motion: reduce)";

export function usePrefersReducedMotion(): boolean {
  return useMediaQuery(QUERY);
}
