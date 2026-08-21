import { useEffect, useState } from "react";

/**
 * The delay the console's search boxes use.
 *
 * Long enough that typing a name is one request rather than one per keystroke,
 * short enough that the table does not feel stuck. It is exported so the specs
 * assert against the same number the components use.
 */
export const SEARCH_DEBOUNCE_MS = 300;

/**
 * Returns `value` after it has stopped changing for `delay` milliseconds.
 *
 * The timer is cleared on every change and on unmount, so an in-flight debounce
 * cannot set state on a component that is gone — and a fast typist produces one
 * settled value rather than a queue of them.
 */
export function useDebouncedValue<T>(value: T, delay: number = SEARCH_DEBOUNCE_MS): T {
  const [settled, setSettled] = useState(value);

  useEffect(() => {
    const timer = setTimeout(() => setSettled(value), delay);
    return () => clearTimeout(timer);
  }, [value, delay]);

  return settled;
}
