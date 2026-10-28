import { useCallback, useLayoutEffect, useRef, type RefObject } from "react";

/** Offset, not an item anchor: restore only at the recovered depth and a valid range. */
export function useSessionPresentation(
  rootRef: RefObject<HTMLElement | null>,
  ready: boolean,
  suspended: boolean,
  { pending = false, unavailable = false, scope = "", onAbandon }: {
    pending?: boolean;
    unavailable?: boolean;
    scope?: string;
    onAbandon?: () => void;
  } = {},
) {
  const saved = useRef<{ top: number; focus: string | null } | null>(null);
  const restore = useRef(false);
  const previousScope = useRef(scope);
  const abandon = useCallback(() => {
    restore.current = false;
    const root = rootRef.current;
    const active = document.activeElement;
    // Ordinary pagination ends recovery, but its control may need focus after refresh.
    saved.current = {
      top: root?.scrollTop ?? 0,
      focus: active instanceof HTMLElement && root?.contains(active)
        ? active.id || active.dataset.sessionFocus || null : null,
    };
    onAbandon?.();
  }, [onAbandon, rootRef]);
  useLayoutEffect(() => {
    if (previousScope.current !== scope) {
      saved.current = null;
      restore.current = false;
      previousScope.current = scope;
    }
    if (suspended) restore.current = true;
    const root = rootRef.current;
    if (!root) return;
    const keyOf = (element: HTMLElement) => element.id || element.dataset.sessionFocus || null;
    if (ready && !saved.current) root.scrollTop = 0;
    if (ready && restore.current && pending) root.scrollTop = 0;
    if (ready && restore.current && saved.current && !pending) {
      const maximum = Math.max(0, root.scrollHeight - root.clientHeight);
      root.scrollTop = !unavailable && saved.current.top <= maximum ? saved.current.top : 0;
      if (document.activeElement === document.body && saved.current.focus) {
        const target = unavailable ? undefined : [...root.querySelectorAll<HTMLElement>("[id], [data-session-focus]")]
          .find((element) => keyOf(element) === saved.current?.focus);
        (target ?? root).focus({ preventScroll: true });
      }
    }
    if (ready && !pending) restore.current = false;
    let observedTop = root.scrollTop;
    const scroll = () => {
      if (restore.current && (pending || !ready)) {
        if (root.scrollTop !== observedTop) abandon();
        observedTop = root.scrollTop;
        return;
      }
      saved.current = { top: root.scrollTop, focus: saved.current?.focus ?? null };
    };
    const focus = (event: FocusEvent) => {
      if (!(event.target instanceof HTMLElement)) return;
      if (event.target.closest("[data-reading-recovery]")) {
        observedTop = root.scrollTop;
        return;
      }
      if (restore.current && (pending || !ready)) abandon();
      saved.current = { top: root.scrollTop, focus: keyOf(event.target) ?? "reading-root-fallback" };
    };
    const blur = (event: FocusEvent) => {
      if (saved.current && event.relatedTarget instanceof Node && !root.contains(event.relatedTarget)) {
        saved.current.focus = null;
      }
    };
    root.addEventListener("scroll", scroll);
    root.addEventListener("focusin", focus);
    root.addEventListener("focusout", blur);
    return () => {
      root.removeEventListener("scroll", scroll);
      root.removeEventListener("focusin", focus);
      root.removeEventListener("focusout", blur);
    };
  }, [ready, rootRef, suspended, pending, unavailable, scope, abandon]);
  return abandon;
}
