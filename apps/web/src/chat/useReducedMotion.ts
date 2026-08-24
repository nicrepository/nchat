/**
 * Live `prefers-reduced-motion: reduce` state (issue #491).
 *
 * Every other reduced-motion rule in this app is a CSS media query, which is
 * enough for a CSS animation but not for this feature: nothing in CSS can stop
 * a native `<img>` GIF from animating. Knowing the preference in JS is what
 * lets the image preview choose the server's static frame instead of fetching
 * the animated original, so this is the one place `matchMedia` is read from
 * script rather than a stylesheet.
 *
 * Subscribed rather than read once, so a user who changes the OS setting while
 * a conversation is open sees the preview react without a reload.
 */

import { useEffect, useState } from "react";

const QUERY = "(prefers-reduced-motion: reduce)";

export function usePrefersReducedMotion(): boolean {
  const [reduced, setReduced] = useState(() => window.matchMedia?.(QUERY).matches ?? false);

  useEffect(() => {
    const mql = window.matchMedia?.(QUERY);
    if (!mql) return;
    const onChange = () => setReduced(mql.matches);
    mql.addEventListener("change", onChange);
    return () => mql.removeEventListener("change", onChange);
  }, []);

  return reduced;
}
