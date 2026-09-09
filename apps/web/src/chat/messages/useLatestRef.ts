import { useLayoutEffect, useRef } from "react";

/**
 * A stable ref that always holds the newest value of a prop.
 *
 * Exists for one reason: the handlers this hook family hands to the WebSocket
 * must keep their identity across renders. A caller that recreates its callback
 * on every render — which panels routinely do — would otherwise restart the
 * socket effect and drop and re-establish every subscription. Latching the
 * callback here lets the handler stay stable while still calling the newest one.
 *
 * Written in a layout effect with no dependency list, so it is up to date after
 * every commit and before any effect or microtask can read it.
 */
export function useLatestRef<T>(value: T) {
  const ref = useRef(value);
  useLayoutEffect(() => {
    ref.current = value;
  });
  return ref;
}
