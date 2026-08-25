/**
 * Live state of one CSS media query.
 *
 * Layout is CSS's job and stays there (issue #467): this exists only for the
 * handful of behaviours a stylesheet cannot express — a navigation drawer that
 * has to stop being modal the moment the sidebar goes back to being a column,
 * for instance. Nothing here measures anything, so there is no resize listener,
 * no layout read and no render loop; the browser evaluates the query and tells
 * us when the answer changes.
 *
 * `matchMedia` is optional because jsdom does not implement it: an environment
 * without it simply never matches, which is the correct answer for a query
 * about a viewport that does not exist.
 */

import { useEffect, useState } from "react";

export function useMediaQuery(query: string): boolean {
  const [matches, setMatches] = useState(() => window.matchMedia?.(query).matches ?? false);

  useEffect(() => {
    const mql = window.matchMedia?.(query);
    if (!mql) return;
    const onChange = () => setMatches(mql.matches);
    mql.addEventListener("change", onChange);
    return () => mql.removeEventListener("change", onChange);
  }, [query]);

  return matches;
}
